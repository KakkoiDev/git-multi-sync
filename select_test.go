package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempConfig points gms at a throwaway config directory. Setting HOME is not
// enough on its own, because GMS_CONFIG_DIR overrides it - a developer with that
// variable set would otherwise have tests rewrite their real config.
func tempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GMS_CONFIG_DIR", dir)
	return dir
}

func TestConfigDirRespectsEnv(t *testing.T) {
	dir := tempConfig(t)
	if got := configDir(); got != dir {
		t.Errorf("configDir() = %q, want %q", got, dir)
	}
	// An empty or whitespace value must fall back to $HOME, so unsetting it in a
	// shell profile does not silently point config at the process's cwd.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GMS_CONFIG_DIR", "  ")
	if got := configDir(); got != filepath.Join(home, ".git-multi-sync") {
		t.Errorf("configDir() with blank env = %q, want the $HOME default", got)
	}
}

func TestLoadLinesMissingFileIsNotAnError(t *testing.T) {
	tempConfig(t)
	got, err := loadLines(ignoreFile())
	if err != nil {
		t.Errorf("missing optional config should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

func TestLoadLinesSkipsCommentsAndDedupes(t *testing.T) {
	dir := tempConfig(t)
	p := filepath.Join(dir, "f")
	writeT(t, p, "# a comment\n\n  ~/a  \n~/b\n~/a\n\t# indented comment\n~/c\n")
	got, err := loadLines(p)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got, []string{"~/a", "~/b", "~/c"})
}

// TestLoadConfigRejectsBangPattern pins that there is exactly one way to
// re-include a repo. Accepting "!pattern" silently as a literal would look like
// gitignore and behave differently, which is worse than refusing it.
func TestLoadConfigRejectsBangPattern(t *testing.T) {
	dir := tempConfig(t)
	writeT(t, filepath.Join(dir, "ignore"), "~/a/**\n!~/a/keep\n")
	_, err := loadConfig()
	if err == nil {
		t.Fatal("a ! pattern should be a config error, not a silent no-op")
	}
	if !strings.Contains(err.Error(), "gms add") {
		t.Errorf("the error should point at the supported alternative, got %q", err)
	}
}

// TestAppendLineHandlesMissingTrailingNewline covers a hand-edited config whose
// last line has no newline. Appending blindly would splice two entries together.
func TestAppendLineHandlesMissingTrailingNewline(t *testing.T) {
	dir := tempConfig(t)
	p := filepath.Join(dir, "f")
	writeT(t, p, "~/first")
	added, err := appendLine(p, "~/second", func(e string) bool { return e == "~/second" })
	if err != nil || !added {
		t.Fatalf("appendLine: added=%v err=%v", added, err)
	}
	got, _ := loadLines(p)
	eq(t, got, []string{"~/first", "~/second"})
}

func TestAppendLineIsIdempotent(t *testing.T) {
	dir := tempConfig(t)
	p := filepath.Join(dir, "f")
	same := func(want string) func(string) bool {
		return func(e string) bool { return e == want }
	}
	if added, _ := appendLine(p, "x", same("x")); !added {
		t.Error("first append should write")
	}
	if added, _ := appendLine(p, "x", same("x")); added {
		t.Error("second append should be a no-op")
	}
	got, _ := loadLines(p)
	eq(t, got, []string{"x"})
}

// TestDropLinePreservesComments matters because the config is documented as
// hand-editable; losing a user's comments on every remove would be a data loss.
func TestDropLinePreservesComments(t *testing.T) {
	dir := tempConfig(t)
	p := filepath.Join(dir, "f")
	writeT(t, p, "# keep me\n~/a\n\n# and me\n~/b\n")
	n, err := dropLine(p, func(s string) bool { return s == "~/a" })
	if err != nil || n != 1 {
		t.Fatalf("dropLine: n=%d err=%v", n, err)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{"# keep me", "# and me"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("comment %q was lost:\n%s", want, b)
		}
	}
	got, _ := loadLines(p)
	eq(t, got, []string{"~/b"})
}

func TestDropLineMissingFileAndNoMatch(t *testing.T) {
	dir := tempConfig(t)
	if n, err := dropLine(filepath.Join(dir, "absent"), func(string) bool { return true }); n != 0 || err != nil {
		t.Errorf("missing file: n=%d err=%v, want 0/nil", n, err)
	}
	p := filepath.Join(dir, "f")
	writeT(t, p, "~/a\n")
	if n, _ := dropLine(p, func(s string) bool { return s == "~/nope" }); n != 0 {
		t.Errorf("no match should remove nothing, removed %d", n)
	}
}

func TestWriteFileAtomicLeavesNoTempFile(t *testing.T) {
	dir := tempConfig(t)
	p := filepath.Join(dir, "f")
	if err := writeFileAtomic(p, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Error("a .tmp file was left behind")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "data\n" {
		t.Errorf("content = %q", b)
	}
}

// TestMatchIgnoreShortAndAbsoluteForms is what makes a shared config portable.
// With GMS_CONFIG_DIR pointing into a synced repo, the same ignore file has to
// work on a machine whose home directory sits somewhere else.
func TestMatchIgnoreShortAndAbsoluteForms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "bench-runs", "work", "pr-1")

	if _, ok := matchIgnore([]string{"~/bench-runs/**"}, target); !ok {
		t.Error("the ~ form should match")
	}
	if _, ok := matchIgnore([]string{filepath.Join(home, "bench-runs") + "/**"}, target); !ok {
		t.Error("the absolute form should match")
	}
	if pat, ok := matchIgnore([]string{"~/other/**", "~/bench-runs/**"}, target); !ok || pat != "~/bench-runs/**" {
		t.Errorf("should report the matching pattern, got %q ok=%v", pat, ok)
	}
	if _, ok := matchIgnore([]string{"~/Code/**"}, target); ok {
		t.Error("an unrelated pattern must not match")
	}
}

// TestPinBeatsIgnore is the precedence rule, and the reason the ignore file needs
// no re-include syntax: putting one path back is a command, not a config edit.
func TestPinBeatsIgnore(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "tree", "keep"))
	mkRepo(t, filepath.Join(root, "tree", "drop"))
	mkRepo(t, filepath.Join(root, "elsewhere"))

	cfg := Config{
		Ignore: []string{filepath.Join(root, "tree") + "/**"},
		Pins:   []string{filepath.Join(root, "tree", "keep")},
	}
	sel, skip, _ := selectRepos(root, cfg, 10)

	var got []string
	for _, s := range sel {
		got = append(got, filepath.Base(s.Path))
	}
	eq(t, got, []string{"elsewhere", "keep"})

	for _, s := range sel {
		if filepath.Base(s.Path) == "keep" && !s.Pinned {
			t.Error("the re-included repo should be marked pinned")
		}
	}
	if len(skip) != 1 || filepath.Base(skip[0].Path) != "drop" {
		t.Errorf("skip = %+v, want just drop", skip)
	}
	if !strings.Contains(skip[0].Reason, "ignored by") {
		t.Errorf("skip reason = %q, should name the pattern", skip[0].Reason)
	}
}

func TestPinOutsideScanRootIsIncluded(t *testing.T) {
	root := t.TempDir()
	outside := mkRepo(t, filepath.Join(t.TempDir(), "far-away"))
	cfg := Config{Pins: []string{outside}}

	sel, _, _ := selectRepos(root, cfg, 10)
	if len(sel) != 1 || sel[0].Path != outside || !sel[0].Pinned {
		t.Errorf("sel = %+v, want the pinned path outside the root", sel)
	}
}

func TestPinIsNotDuplicatedWhenAlsoDiscovered(t *testing.T) {
	root := t.TempDir()
	p := mkRepo(t, filepath.Join(root, "both"))
	sel, _, _ := selectRepos(root, Config{Pins: []string{p}}, 10)
	if len(sel) != 1 {
		t.Fatalf("a discovered repo that is also pinned should appear once, got %+v", sel)
	}
	if !sel[0].Pinned {
		t.Error("it should be reported as pinned")
	}
}

func TestSelectSkipsSubmodulesWithAReason(t *testing.T) {
	root := t.TempDir()
	mkGitlink(t, filepath.Join(root, "sub"), "/somewhere/super/.git/modules/sub")
	sel, skip, _ := selectRepos(root, Config{}, 10)
	if len(sel) != 0 {
		t.Errorf("a submodule is governed by its superproject, got %+v", sel)
	}
	if len(skip) != 1 || !strings.Contains(skip[0].Reason, "submodule") {
		t.Errorf("skip = %+v, want a submodule reason", skip)
	}
}

func TestSelectIsDeterministic(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"c", "a", "b"} {
		mkRepo(t, filepath.Join(root, n))
	}
	first, _, _ := selectRepos(root, Config{}, 10)
	for i := 0; i < 3; i++ {
		again, _, _ := selectRepos(root, Config{}, 10)
		if len(again) != len(first) {
			t.Fatal("selection size varies between runs")
		}
		for j := range first {
			if again[j].Path != first[j].Path {
				t.Fatalf("order varies between runs at %d", j)
			}
		}
	}
}

// TestSelectFindsWorktreesBeyondScanDepth is the fix for the depth limit. A branch
// name with slashes puts its worktree several levels below the checkout it belongs
// to, so a depth-bounded scan cannot see it; asking git is exact.
func TestSelectFindsWorktreesBeyondScanDepth(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	_, primary := newOrigin(t, root, "proj")

	deep := filepath.Join(root, "wt", "feature", "TICKET-1", "long-branch-name")
	gitT(t, primary, "worktree", "add", "-b", "feature/TICKET-1/long", deep)

	// Depth 1 cannot reach the worktree: it sits four levels below the root.
	fs, _ := scanRepos(root, 0, 1)
	for _, f := range fs {
		if f.Path == deep {
			t.Fatal("precondition failed: the scan reached it, so this proves nothing")
		}
	}

	sel, _, _ := selectRepos(root, Config{}, 1)
	found := false
	for _, s := range sel {
		// Compared canonically: a worktree found by asking git carries git's
		// resolved path, while one found by the walk carries the walked path.
		if canonical(s.Path) == canonical(deep) {
			found = true
			if s.Kind != kindWorktree {
				t.Errorf("kind = %v, want worktree", s.Kind)
			}
		}
	}
	if !found {
		t.Errorf("worktree below the depth limit was not enumerated; got %+v", sel)
	}
	// And the main checkout must appear exactly once, not also as its own worktree.
	n := 0
	for _, s := range sel {
		if canonical(s.Path) == canonical(primary) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("main checkout appears %d times, want 1", n)
	}
}

// TestWorktreesOfSkipsDeletedDirectories covers entries git still lists after
// their directory is gone. On a real repo two such entries were reported as
// "locked initializing" rather than "prunable", so the marker cannot be trusted
// and the path has to be stat'd.
func TestWorktreesOfSkipsDeletedDirectories(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	_, primary := newOrigin(t, root, "proj")
	wt := filepath.Join(root, "gone")
	gitT(t, primary, "worktree", "add", "-b", "tmp", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	if out := gitT(t, primary, "worktree", "list", "--porcelain"); !strings.Contains(out, wt) {
		t.Skip("git no longer lists the deleted worktree; nothing to filter")
	}
	for _, p := range worktreesOf(primary) {
		if p == wt {
			t.Error("a worktree whose directory is gone must not be selected")
		}
	}
}

func TestWorktreesOfExcludesTheRepoItself(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	_, primary := newOrigin(t, root, "proj")
	gitT(t, primary, "worktree", "add", "-b", "b", filepath.Join(root, "w"))
	got := worktreesOf(primary)
	if len(got) != 1 {
		t.Fatalf("got %v, want exactly the one linked worktree", got)
	}
	for _, p := range got {
		// Compared canonically. A plain p == primary check cannot fail here: git
		// reports a resolved path and primary is the unresolved one, which is the
		// very mismatch this guards against.
		if canonical(p) == canonical(primary) {
			t.Error("the main checkout must not be reported as its own worktree")
		}
	}
}

func TestWorktreesOfNoWorktreesIsFree(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	_, primary := newOrigin(t, root, "proj")
	if got := worktreesOf(primary); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestElide(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"short", 20, "short"},
		{"exactly-ten", 11, "exactly-ten"},
		{"abcdefghijklmnop", 10, "abc…klmnop"},
		{"tiny", 3, "tiny"}, // below the useful minimum, returned unchanged
	} {
		if got := elide(tc.in, tc.max); got != tc.want {
			t.Errorf("elide(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
	// The tail is kept because worktree rows are distinguished by their last
	// segment; truncating the end would make different repos render identically.
	a := elide("~/.tmux-worktree/meetsone/feature/SOBA-306/two-row-budget", 30)
	b := elide("~/.tmux-worktree/meetsone/feature/SOBA-306/other-thing", 30)
	if a == b {
		t.Errorf("two different worktrees elided to the same string: %q", a)
	}
	if len([]rune(elide(strings.Repeat("x", 200), 40))) != 40 {
		t.Error("elide should respect its maximum")
	}
}
