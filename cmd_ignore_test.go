package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIgnoreSpec pins the translation from what a user types to what is stored.
func TestIgnoreSpec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tree := filepath.Join(home, "scratch")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ in, want string }{
		// An existing directory means the tree. `gms ignore ~/scratch` plainly means
		// everything under it, not one repo whose root is exactly that directory.
		{tree, "~/scratch/**"},
		{tree + "/", "~/scratch/**"},
		{"~/scratch", "~/scratch/**"},

		// A glob is stored verbatim, so what the user typed reaches the file.
		{"~/scratch/**", "~/scratch/**"},
		{"~/Code/*-experiment", "~/Code/*-experiment"},
		{"~/a/[abc]", "~/a/[abc]"},

		// A path that does not exist cannot be assumed to be a directory.
		{"~/not-there", "~/not-there"},
		{"  ~/scratch  ", "~/scratch/**"},
	} {
		if got := ignoreSpec(tc.in); got != tc.want {
			t.Errorf("ignoreSpec(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestIgnoreSpecKeepsTildeForm is what makes a shared config portable: storing the
// expanded path would bake one machine's home directory into a file meant to be
// synced to another.
func TestIgnoreSpecKeepsTildeForm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tree := filepath.Join(home, "bench-runs")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	got := ignoreSpec(tree)
	if !strings.HasPrefix(got, "~/") {
		t.Errorf("ignoreSpec(%q) = %q, want a ~-relative pattern", tree, got)
	}
	if strings.Contains(got, home) {
		t.Errorf("ignoreSpec(%q) = %q, leaks this machine's home directory", tree, got)
	}
}

func TestIgnoreCommandWritesAndIsIdempotent(t *testing.T) {
	tempConfig(t)
	c := lookup("ignore")

	captureOutput(t, func() {
		if code := c.Run(c, []string{"~/scratch/**"}); code != 0 {
			t.Errorf("first ignore = %d", code)
		}
	})
	captureOutput(t, func() {
		if code := c.Run(c, []string{"~/scratch/**"}); code != 0 {
			t.Errorf("second ignore = %d, should be a no-op not a failure", code)
		}
	})

	got, _ := loadLines(ignoreFile())
	eq(t, got, []string{"~/scratch/**"})
}

func TestIgnoreCommandRejectsBangArgument(t *testing.T) {
	tempConfig(t)
	c := lookup("ignore")
	var code int
	_, stderr := captureOutput(t, func() { code = c.Run(c, []string{"!~/keep"}) })
	if code == 0 {
		t.Error("a ! argument should be refused, not stored")
	}
	if !strings.Contains(stderr, "gms add") {
		t.Errorf("the refusal should point at the supported alternative, got %q", stderr)
	}
	if got, _ := loadLines(ignoreFile()); len(got) != 0 {
		t.Errorf("nothing should have been written, got %v", got)
	}
}

func TestUnignoreRoundTrip(t *testing.T) {
	tempConfig(t)
	ign, unign := lookup("ignore"), lookup("unignore")

	captureOutput(t, func() { ign.Run(ign, []string{"~/a/**", "~/b/**"}) })
	captureOutput(t, func() {
		if code := unign.Run(unign, []string{"~/a/**"}); code != 0 {
			t.Errorf("unignore = %d", code)
		}
	})
	got, _ := loadLines(ignoreFile())
	eq(t, got, []string{"~/b/**"})
}

// TestUnignoreAcceptsWhatTheUserTyped covers the asymmetry `gms ignore ~/x`
// creates: it stores "~/x/**", and the user should be able to undo it by naming
// either form rather than having to read the file first.
func TestUnignoreAcceptsWhatTheUserTyped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tempConfig(t)
	tree := filepath.Join(home, "scratch")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}

	ign, unign := lookup("ignore"), lookup("unignore")
	captureOutput(t, func() { ign.Run(ign, []string{tree}) })
	if got, _ := loadLines(ignoreFile()); len(got) != 1 || got[0] != "~/scratch/**" {
		t.Fatalf("stored %v, want [~/scratch/**]", got)
	}
	captureOutput(t, func() {
		if code := unign.Run(unign, []string{tree}); code != 0 {
			t.Errorf("unignore by the typed path = %d, want 0", code)
		}
	})
	if got, _ := loadLines(ignoreFile()); len(got) != 0 {
		t.Errorf("after unignore: %v, want empty", got)
	}
}

func TestUnignoreUnknownPatternExitsOne(t *testing.T) {
	tempConfig(t)
	c := lookup("unignore")
	var code int
	_, stderr := captureOutput(t, func() { code = c.Run(c, []string{"~/never-added"}) })
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not an ignore pattern") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestUnignoreWithNoArgsIsAUsageError(t *testing.T) {
	tempConfig(t)
	c := lookup("unignore")
	var code int
	captureOutput(t, func() { code = c.Run(c, nil) })
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestIgnorePreservesComments(t *testing.T) {
	dir := tempConfig(t)
	writeT(t, filepath.Join(dir, "ignore"), "# my notes\n~/a/**\n")
	ign, unign := lookup("ignore"), lookup("unignore")
	captureOutput(t, func() { ign.Run(ign, []string{"~/b/**"}) })
	captureOutput(t, func() { unign.Run(unign, []string{"~/a/**"}) })

	b, _ := os.ReadFile(filepath.Join(dir, "ignore"))
	if !strings.Contains(string(b), "# my notes") {
		t.Errorf("comment was lost:\n%s", b)
	}
	got, _ := loadLines(ignoreFile())
	eq(t, got, []string{"~/b/**"})
}

// TestCountPinnedWarnsWhenAPinSurvives covers the one case where ignoring
// something does not silence it. Pins outrank ignore, so saying so at the moment
// of the edit is the only place the user is looking.
func TestCountPinnedWarnsWhenAPinSurvives(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pins := []string{filepath.Join(home, "tree", "keep"), filepath.Join(home, "other")}

	if n := countPinned(pins, "~/tree/**"); n != 1 {
		t.Errorf("countPinned = %d, want 1", n)
	}
	if n := countPinned(pins, "~/nothing/**"); n != 0 {
		t.Errorf("countPinned = %d, want 0", n)
	}
}

func TestRemoveOnDiscoveredRepoPointsAtIgnore(t *testing.T) {
	skipWithoutGit(t)
	tempConfig(t)
	root := t.TempDir()
	_, repo := newOrigin(t, root, "found")

	c := lookup("remove")
	var code int
	_, stderr := captureOutput(t, func() { code = c.Run(c, []string{repo}) })
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not pinned") {
		t.Errorf("stderr = %q, should say it was never pinned", stderr)
	}
	if !strings.Contains(stderr, "gms ignore") {
		t.Errorf("stderr = %q, should point at the verb that actually stops syncing it", stderr)
	}
}
