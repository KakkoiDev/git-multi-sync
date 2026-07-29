package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func testCatalog(t *testing.T) []RemoteRepo {
	t.Helper()
	cat, err := decodeCatalog(catalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestResolveSpec(t *testing.T) {
	cat := testCatalog(t)

	for _, tc := range []struct{ spec, want string }{
		{"aidb", "KakkoiDev/aidb"},
		{"AIDB", "KakkoiDev/aidb"}, // GitHub names are case-insensitive
		{"KakkoiDev/aidb", "KakkoiDev/aidb"},
		{"meetsmore/meetsone", "meetsmore/meetsone"},
		{"meetsone", "meetsmore/meetsone"},
		{"aidb.git", "KakkoiDev/aidb"},
		{"KakkoiDev/aidb/", "KakkoiDev/aidb"},
	} {
		got, err := resolveSpec(cat, tc.spec)
		if err != nil {
			t.Errorf("resolveSpec(%q): %v", tc.spec, err)
			continue
		}
		if got.FullName != tc.want {
			t.Errorf("resolveSpec(%q) = %q, want %q", tc.spec, got.FullName, tc.want)
		}
	}
}

// TestResolveSpecAmbiguousNamesEveryCandidate: picking an owner for the user is how
// the wrong repo gets cloned, so an ambiguous name has to fail and say what it
// could have meant.
func TestResolveSpecAmbiguousNamesEveryCandidate(t *testing.T) {
	cat := append(testCatalog(t),
		RemoteRepo{FullName: "KakkoiDev/shared"},
		RemoteRepo{FullName: "meetsmore/shared"})

	_, err := resolveSpec(cat, "shared")
	if err == nil {
		t.Fatal("an ambiguous bare name must not resolve")
	}
	for _, want := range []string{"KakkoiDev/shared", "meetsmore/shared"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits candidate %q", err, want)
		}
	}
	// Fully qualifying it resolves.
	got, err := resolveSpec(cat, "meetsmore/shared")
	if err != nil || got.FullName != "meetsmore/shared" {
		t.Errorf("qualified spec: %+v, %v", got, err)
	}
}

func TestResolveSpecUnknownSuggests(t *testing.T) {
	cat := testCatalog(t)
	_, err := resolveSpec(cat, "meetson")
	if err == nil {
		t.Fatal("an unknown name must be an error")
	}
	if !strings.Contains(err.Error(), "meetsone") {
		t.Errorf("a near miss should be suggested, got %q", err)
	}

	_, err = resolveSpec(cat, "totally-unrelated-xyz")
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Errorf("with nothing similar, do not invent a suggestion: %v", err)
	}
}

func TestCloneTargetAvoidsCollisions(t *testing.T) {
	root := t.TempDir()
	r := RemoteRepo{FullName: "KakkoiDev/thing"}

	got, err := cloneTarget(root, r)
	if err != nil || got != filepath.Join(root, "thing") {
		t.Fatalf("got %q, %v", got, err)
	}

	// With the obvious name taken, fall back to an owner prefix rather than
	// overwriting whatever is there.
	mkRepo(t, filepath.Join(root, "thing"))
	got, err = cloneTarget(root, r)
	if err != nil || got != filepath.Join(root, "KakkoiDev-thing") {
		t.Fatalf("got %q, %v", got, err)
	}

	mkRepo(t, filepath.Join(root, "KakkoiDev-thing"))
	if _, err = cloneTarget(root, r); err == nil {
		t.Error("with both names taken it must refuse, not pick a third")
	}
}

// TestCloneRootFollowsTheExistingLayout: the destination is derived from where
// repos already are, so gms imposes no layout and needs no configuration for it.
func TestCloneRootFollowsTheExistingLayout(t *testing.T) {
	home := "/home/u"
	sel := []Selected{
		{Path: "/home/u/Code/a", Kind: kindPrimary},
		{Path: "/home/u/Code/b", Kind: kindPrimary},
		{Path: "/home/u/Code/c", Kind: kindPrimary},
		{Path: "/home/u/dotfiles", Kind: kindPrimary},
		{Path: "/home/u/elsewhere/x", Kind: kindPrimary},
	}
	if got := cloneRoot(sel, home); got != "/home/u/Code" {
		t.Errorf("cloneRoot = %q, want /home/u/Code", got)
	}
}

func TestCloneRootIgnoresWorktreesAndPins(t *testing.T) {
	home := "/home/u"
	sel := []Selected{
		// Worktrees cluster under one directory but are not a place to clone into.
		{Path: "/home/u/.wt/a", Kind: kindWorktree},
		{Path: "/home/u/.wt/b", Kind: kindWorktree},
		{Path: "/home/u/.wt/c", Kind: kindWorktree},
		// A pin is somewhere deliberate and unusual, so it is not evidence of layout.
		{Path: "/home/u/odd/p", Kind: kindPrimary, Pinned: true},
		{Path: "/home/u/odd/q", Kind: kindPrimary, Pinned: true},
		{Path: "/home/u/src/a", Kind: kindPrimary},
		{Path: "/home/u/src/b", Kind: kindPrimary},
	}
	if got := cloneRoot(sel, home); got != "/home/u/src" {
		t.Errorf("cloneRoot = %q, want /home/u/src", got)
	}
}

func TestCloneRootFallsBackToHome(t *testing.T) {
	home := "/home/u"
	if got := cloneRoot(nil, home); got != home {
		t.Errorf("cloneRoot(nil) = %q, want %q", got, home)
	}
	// One repo is not an established layout.
	one := []Selected{{Path: "/home/u/random/only", Kind: kindPrimary}}
	if got := cloneRoot(one, home); got != home {
		t.Errorf("cloneRoot(one) = %q, want %q", got, home)
	}
}

func TestCloneRootIsDeterministic(t *testing.T) {
	home := "/home/u"
	sel := []Selected{
		{Path: "/home/u/aaa/1", Kind: kindPrimary},
		{Path: "/home/u/aaa/2", Kind: kindPrimary},
		{Path: "/home/u/bbb/1", Kind: kindPrimary},
		{Path: "/home/u/bbb/2", Kind: kindPrimary},
	}
	first := cloneRoot(sel, home)
	for i := 0; i < 20; i++ {
		if got := cloneRoot(sel, home); got != first {
			t.Fatalf("cloneRoot varies between runs: %q then %q", first, got)
		}
	}
}

func TestCloneURLFollowsGhProtocol(t *testing.T) {
	r := RemoteRepo{
		CloneURL: "https://github.com/o/n.git",
		SSHURL:   "git@github.com:o/n.git",
	}
	if got := cloneURL(r, true); got != r.SSHURL {
		t.Errorf("ssh preferred: got %q", got)
	}
	if got := cloneURL(r, false); got != r.CloneURL {
		t.Errorf("https preferred: got %q", got)
	}
	// With only one URL available, use it whatever the preference.
	if got := cloneURL(RemoteRepo{SSHURL: r.SSHURL}, false); got != r.SSHURL {
		t.Errorf("ssh-only: got %q", got)
	}
}

// TestLocalIDsKeysByIdentityNotPath is the ~/.aidb case: that directory holds
// KakkoiDev/claude-database. Keying on the directory name would report the repo as
// missing and clone a second copy.
func TestLocalIDsKeysByIdentityNotPath(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	_, repo := newOrigin(t, root, "directory-name")
	gitT(t, repo, "remote", "set-url", "origin", "git@github.com:KakkoiDev/actual-repo-name.git")

	got := localIDs([]Selected{{Path: repo, Kind: kindPrimary}}, 2)
	if at, ok := got["kakkoidev/actual-repo-name"]; !ok || at != repo {
		t.Errorf("localIDs = %v, want the repo keyed by its remote identity", got)
	}
	if _, ok := got["kakkoidev/directory-name"]; ok {
		t.Error("must not be keyed by directory name")
	}
}

func TestLocalIDsSkipsReposWithNoRemote(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	solo := filepath.Join(root, "solo")
	gitT(t, root, "init", "-b", "main", solo)

	got := localIDs([]Selected{{Path: solo, Kind: kindPrimary}}, 2)
	if len(got) != 0 {
		t.Errorf("a repo with no remote has no identity to key on, got %v", got)
	}
}

func TestCloneWithNoArgsIsAUsageError(t *testing.T) {
	c := lookup("clone")
	var code int
	captureOutput(t, func() { code = c.Run(c, nil) })
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}
