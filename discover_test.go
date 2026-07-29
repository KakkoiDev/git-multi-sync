package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// mkRepo creates a minimal repo root without running git: the walk only looks for
// a .git entry, so this keeps the scan tests fast and git-independent.
func mkRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mkGitlink creates a repo root whose .git is a file pointing at target, which is
// how git records a linked worktree or a submodule.
func mkGitlink(t *testing.T, dir, target string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeT(t, filepath.Join(dir, ".git"), "gitdir: "+target+"\n")
	return dir
}

func scanPaths(t *testing.T, root string, depth int) []string {
	t.Helper()
	fs, _ := scanRepos(root, 0, depth)
	out := make([]string, len(fs))
	for i, f := range fs {
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = rel
	}
	sort.Strings(out)
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestScanStopsAtFirstGitDir is the property that makes an unbounded $HOME scan
// affordable, and it is also correctness: a vendored clone inside a checkout is
// not a separate thing to sync. On this machine ~/Code/tinyagent contains
// llama.cpp, and ~/.tmux-worktree holds 928k directories that are never read
// because each worktree root is recognised and skipped.
func TestScanStopsAtFirstGitDir(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "outer"))
	mkRepo(t, filepath.Join(root, "outer", "vendor-clone"))
	mkRepo(t, filepath.Join(root, "outer", "deep", "nested-clone"))

	eq(t, scanPaths(t, root, 10), []string{"outer"})
}

func TestScanFindsSiblingsAfterARepo(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a"))
	mkRepo(t, filepath.Join(root, "b"))
	mkRepo(t, filepath.Join(root, "c"))
	eq(t, scanPaths(t, root, 10), []string{"a", "b", "c"})
}

func TestScanMaxDepth(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "one"))
	mkRepo(t, filepath.Join(root, "a", "two"))
	mkRepo(t, filepath.Join(root, "a", "b", "three"))

	eq(t, scanPaths(t, root, 1), []string{"one"})
	eq(t, scanPaths(t, root, 2), []string{"a/two", "one"})
	eq(t, scanPaths(t, root, 3), []string{"a/b/three", "a/two", "one"})
}

// TestScanReportsWhatItDidNotDescend is the no-silent-caps rule: a repo missed
// because of the depth limit must be visible as a count, or "nothing to sync" and
// "I did not look" are indistinguishable.
func TestScanReportsWhatItDidNotDescend(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a", "b", "deep"))

	_, st := scanRepos(root, 0, 1)
	if st.Repos != 0 {
		t.Fatalf("depth 1 should find nothing, found %d", st.Repos)
	}
	if st.TooDeep == 0 {
		t.Error("a directory was not descended but TooDeep is 0")
	}
	if st.MaxDepth != 1 {
		t.Errorf("MaxDepth = %d, want 1", st.MaxDepth)
	}
}

func TestScanBudgetStopsAndSaysSo(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		mkRepo(t, filepath.Join(root, n, "inner"))
	}
	fs, st := scanRepos(root, 3, 10)
	if !st.Stopped {
		t.Error("budget was exhausted but Stopped is false")
	}
	if len(fs) >= 5 {
		t.Errorf("found %d repos despite a 3-directory budget", len(fs))
	}
}

func TestScanPrunesByName(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "keep"))
	mkRepo(t, filepath.Join(root, "node_modules", "pkg"))
	mkRepo(t, filepath.Join(root, "app", "node_modules", "pkg"))
	mkRepo(t, filepath.Join(root, "__pycache__", "x"))

	eq(t, scanPaths(t, root, 10), []string{"keep"})
}

func TestScanPrunesByRelPath(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "Library", "Application Support", "pico-8"))
	mkRepo(t, filepath.Join(root, "Library", "Caches", "junk"))
	mkRepo(t, filepath.Join(root, "go", "pkg", "mod", "thing"))

	// The real case this encodes: ~/Library cannot be pruned by name because it
	// holds a repo the user owns, but its cache subtree can.
	eq(t, scanPaths(t, root, 10), []string{"Library/Application Support/pico-8"})
}

// TestScanPathWithSpaces covers ~/Library/Application Support/pico-8, a real repo
// on this machine. It is why the scan is Go filepath work and never a shell out.
func TestScanPathWithSpaces(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "Some Dir With Spaces", "my repo"))
	eq(t, scanPaths(t, root, 10), []string{"Some Dir With Spaces/my repo"})
}

// TestScanDoesNotFollowSymlinks matters because $HOME is full of them: on this
// machine 10 top-level entries link into ~/dotfiles and ~/.claude/skills holds 36
// more, several pointing back into repos already being scanned. Following them
// would report the same repo under several paths.
func TestScanDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := t.TempDir()
	real := mkRepo(t, filepath.Join(root, "real"))
	if err := os.Symlink(real, filepath.Join(root, "link-to-repo")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	outside := mkRepo(t, filepath.Join(t.TempDir(), "outside"))
	if err := os.Symlink(outside, filepath.Join(root, "link-outside")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	eq(t, scanPaths(t, root, 10), []string{"real"})
}

func TestScanSymlinkCycleTerminates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "r"))
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if err := os.Symlink("..", filepath.Join(root, "up")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	// The test finishing at all is the assertion; a followed cycle would hang
	// until the budget stopped it, and would report duplicates.
	eq(t, scanPaths(t, root, 10), []string{"r"})
}

func TestScanUnreadableDirIsNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "visible"))
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	fs, st := scanRepos(root, 0, 10)
	if st.Errs == 0 {
		t.Error("an unreadable directory should be counted in Errs")
	}
	found := false
	for _, f := range fs {
		if filepath.Base(f.Path) == "visible" {
			found = true
		}
	}
	if !found {
		t.Error("an unreadable directory must not abort the scan")
	}
}

func TestScanClassifiesWorktreesAndSubmodules(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "primary"))
	mkGitlink(t, filepath.Join(root, "wt"), "/somewhere/parent/.git/worktrees/wt")
	mkGitlink(t, filepath.Join(root, "sub"), "/somewhere/super/.git/modules/sub")

	fs, _ := scanRepos(root, 0, 10)
	got := map[string]found{}
	for _, f := range fs {
		got[filepath.Base(f.Path)] = f
	}
	if len(got) != 3 {
		t.Fatalf("found %d repos, want 3: %v", len(got), fs)
	}
	if k := got["primary"].Kind; k != kindPrimary {
		t.Errorf("primary kind = %v", k)
	}
	if f := got["wt"]; f.Kind != kindWorktree || f.Parent != "/somewhere/parent" {
		t.Errorf("worktree = %+v, want kindWorktree with parent /somewhere/parent", f)
	}
	if f := got["sub"]; f.Kind != kindSubmodule || f.Parent != "/somewhere/super" {
		t.Errorf("submodule = %+v, want kindSubmodule with parent /somewhere/super", f)
	}
}

func TestClassifyGitEntry(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		content string
		kind    repoKind
		parent  string
		ok      bool
	}{
		{"worktree", "gitdir: /Users/c/Code/meetsone/.git/worktrees/on-call\n", kindWorktree, "/Users/c/Code/meetsone", true},
		{"submodule", "gitdir: /Users/c/Code/super/.git/modules/lib\n", kindSubmodule, "/Users/c/Code/super", true},
		{"relative gitlink", "gitdir: ../other/.git\n", kindPrimary, "", true},
		{"no trailing newline", "gitdir: /a/b/.git/worktrees/w", kindWorktree, "/a/b", true},
		{"not a gitlink", "this is not a git file\n", kindPrimary, "", false},
		{"empty", "", kindPrimary, "", false},
		{"gitdir with no value", "gitdir:\n", kindPrimary, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name)
			writeT(t, p, tc.content)
			k, parent, ok := classifyGitEntry(p, false)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && (k != tc.kind || parent != tc.parent) {
				t.Errorf("got %v/%q, want %v/%q", k, parent, tc.kind, tc.parent)
			}
		})
	}
	if k, _, ok := classifyGitEntry(filepath.Join(dir, "nope"), true); !ok || k != kindPrimary {
		t.Error("a .git directory is a primary checkout")
	}
}

// TestFanOutKeepsIndexAlignment pins the invariant introduced when fanOut stopped
// sorting its own results: callers now sort the input and rely on results lining
// up with it. A mismatch would silently attribute one repo's state to another.
func TestFanOutKeepsIndexAlignment(t *testing.T) {
	in := []string{"e", "a", "d", "b", "c", "f", "g", "h"}
	got := fanOut(in, 4, func(s string) string { return s + "!" })
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i]+"!" {
			t.Errorf("index %d: got %q, want %q", i, got[i], in[i]+"!")
		}
	}
}

func TestFanOutZeroLimit(t *testing.T) {
	got := fanOut([]string{"a", "b"}, 0, func(s string) string { return s })
	eq(t, got, []string{"a", "b"})
}
