package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		f    facts
		want State
	}{
		{"not a repo", facts{isRepo: false}, StateMissing},
		{"detached", facts{isRepo: true, detached: true}, StateDetached},
		{"no upstream", facts{isRepo: true, hasUpstream: false}, StateNoUpstream},
		{"dirty wins over behind", facts{isRepo: true, hasUpstream: true, dirty: true, behind: 3}, StateDirty},
		{"dirty wins over ahead", facts{isRepo: true, hasUpstream: true, dirty: true, ahead: 2}, StateDirty},
		{"dirty wins over diverged", facts{isRepo: true, hasUpstream: true, dirty: true, ahead: 1, behind: 2}, StateDirty},
		{"up to date", facts{isRepo: true, hasUpstream: true}, StateUpToDate},
		{"behind", facts{isRepo: true, hasUpstream: true, behind: 2}, StateBehind},
		{"ahead", facts{isRepo: true, hasUpstream: true, ahead: 2}, StateAhead},
		{"diverged", facts{isRepo: true, hasUpstream: true, ahead: 1, behind: 1}, StateDiverged},
	}
	for _, c := range cases {
		if got := classify(c.f); got != c.want {
			t.Errorf("%s: classify(%+v) = %v, want %v", c.name, c.f, got, c.want)
		}
	}
}

// --- integration harness against real git, isolated in temp dirs (no network) ---

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newOrigin makes a bare repo plus a primary clone with one pushed commit on main.
func newOrigin(t *testing.T, root, name string) (bare, primary string) {
	t.Helper()
	bare = filepath.Join(root, name+".git")
	gitT(t, root, "init", "--bare", "-b", "main", bare)
	primary = filepath.Join(root, name)
	gitT(t, root, "clone", bare, primary)
	writeT(t, filepath.Join(primary, "f.txt"), "1\n")
	gitT(t, primary, "add", "-A")
	gitT(t, primary, "commit", "-m", "c1")
	gitT(t, primary, "push", "-u", "origin", "main")
	return bare, primary
}

func TestIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()

	t.Run("behind then ff-pull", func(t *testing.T) {
		_, primary := newOrigin(t, root, "behind")
		other := filepath.Join(root, "behind-b")
		gitT(t, root, "clone", filepath.Join(root, "behind.git"), other)
		// advance origin from primary
		writeT(t, filepath.Join(primary, "f.txt"), "2\n")
		gitT(t, primary, "commit", "-am", "c2")
		gitT(t, primary, "push")

		r := examine(other, true, false)
		if r.State != StateBehind || r.Behind != 1 {
			t.Fatalf("got state=%s behind=%d, want behind 1", stateLabel(r.State), r.Behind)
		}
		r = examine(other, true, true)
		if r.Action != "ff-pulled 1 commit" {
			t.Fatalf("action = %q, want ff-pulled 1 commit", r.Action)
		}
		if after := examine(other, true, false); after.State != StateUpToDate {
			t.Fatalf("after pull state=%s, want clean", stateLabel(after.State))
		}
	})

	t.Run("ahead then push", func(t *testing.T) {
		_, primary := newOrigin(t, root, "ahead")
		writeT(t, filepath.Join(primary, "f.txt"), "local\n")
		gitT(t, primary, "commit", "-am", "local")

		r := examine(primary, true, false)
		if r.State != StateAhead || r.Ahead != 1 {
			t.Fatalf("got state=%s ahead=%d, want ahead 1", stateLabel(r.State), r.Ahead)
		}
		r = examine(primary, true, true)
		if r.Action != "pushed 1 commit" {
			t.Fatalf("action = %q, want pushed 1 commit", r.Action)
		}
		if after := examine(primary, true, false); after.State != StateUpToDate {
			t.Fatalf("after push state=%s, want clean", stateLabel(after.State))
		}
	})

	t.Run("diverged is detected, not touched", func(t *testing.T) {
		_, primary := newOrigin(t, root, "div")
		other := filepath.Join(root, "div-b")
		gitT(t, root, "clone", filepath.Join(root, "div.git"), other)
		// local commit on the second clone
		writeT(t, filepath.Join(other, "f.txt"), "from-b\n")
		gitT(t, other, "commit", "-am", "b-change")
		// conflicting commit pushed from primary
		writeT(t, filepath.Join(primary, "f.txt"), "from-a\n")
		gitT(t, primary, "commit", "-am", "a-change")
		gitT(t, primary, "push")

		r := examine(other, true, true)
		if r.State != StateDiverged {
			t.Fatalf("state = %s, want DIVERGED", stateLabel(r.State))
		}
		if r.Action != "needs resolve" {
			t.Fatalf("action = %q, want needs resolve", r.Action)
		}
		if len(r.Conflict) != 1 || r.Conflict[0] != "f.txt" {
			t.Fatalf("conflict files = %v, want [f.txt]", r.Conflict)
		}
		// still diverged after sync: nothing was merged or pushed
		if after := examine(other, true, false); after.State != StateDiverged {
			t.Fatalf("after sync state=%s, want still DIVERGED", stateLabel(after.State))
		}
	})

	t.Run("dirty is reported, not touched", func(t *testing.T) {
		_, primary := newOrigin(t, root, "dirty")
		writeT(t, filepath.Join(primary, "f.txt"), "uncommitted\n")

		r := examine(primary, true, true)
		if r.State != StateDirty || r.DirtyN != 1 {
			t.Fatalf("got state=%s dirty=%d, want DIRTY 1", stateLabel(r.State), r.DirtyN)
		}
		if r.Action != "skipped (dirty)" {
			t.Fatalf("action = %q, want skipped (dirty)", r.Action)
		}
		// change is intact
		if got := gitT(t, primary, "status", "--porcelain"); got == "" {
			t.Fatal("dirty change was lost")
		}
	})

	t.Run("no upstream", func(t *testing.T) {
		_, primary := newOrigin(t, root, "noup")
		gitT(t, primary, "checkout", "-b", "feature")
		if r := examine(primary, true, false); r.State != StateNoUpstream {
			t.Fatalf("state = %s, want no-upstream", stateLabel(r.State))
		}
	})

	t.Run("missing path", func(t *testing.T) {
		if r := examine(filepath.Join(root, "nope"), true, false); r.State != StateMissing {
			t.Fatalf("state = %s, want MISSING", stateLabel(r.State))
		}
	})
}
