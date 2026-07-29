package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Selected is a repo gms will act on.
type Selected struct {
	Path   string
	Kind   repoKind
	Pinned bool
}

// Skipped is a discovered repo that was excluded, and why. Every exclusion carries
// a reason and is reportable: a repo silently dropped from the sync set is the
// failure gms exists to prevent.
type Skipped struct {
	Path   string
	Reason string
}

// matchIgnore reports the first pattern excluding path, if any.
//
// Patterns are tried against both the tilde-shortened path and the absolute one.
// The short form is what makes a config portable: with GMS_CONFIG_DIR pointing
// into a synced repo, the same ignore file has to work on machines whose home
// directory is at a different place.
func matchIgnore(patterns []string, path string) (string, bool) {
	short := shortPath(path)
	for _, p := range patterns {
		if globMatch(p, short) || globMatch(p, path) {
			return p, true
		}
	}
	return "", false
}

// worktreesOf lists the linked worktrees a repo knows about, excluding the repo
// itself and any entry whose directory is gone.
//
// This is how gms reaches worktrees the depth-bounded scan cannot: a branch name
// containing slashes puts its worktree several levels below the checkout it
// belongs to, and on a real machine that hides most of them. Asking git is exact
// where a deeper scan is both slower and still a guess.
//
// Stale entries are detected by stat, not by the porcelain markers. Measured on a
// real repo, two worktrees whose directories had been deleted were reported as
// "locked initializing" rather than "prunable".
func worktreesOf(dir string) []string {
	// git worktree list is only worth a subprocess when the repo has worktrees.
	if _, err := os.Stat(filepath.Join(dir, ".git", "worktrees")); err != nil {
		return nil
	}
	out, err := gitOut(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		p, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		if canonical(p) == canonical(dir) {
			continue // the main checkout, which the scan already found
		}
		paths = append(paths, p)
	}
	return paths
}

// scanRoot resolves the directory to scan: the flag if given, else the user's home
// directory.
//
// It exists because sync is the only command that writes to a repo, and for a long
// time it was also the only one that could not be aimed anywhere but $HOME. That
// combination is backwards, and it had a cost: a test run that set GMS_CONFIG_DIR
// to a scratch directory still scanned the real home directory and pushed four
// repos nobody had asked it to. Isolating the config is not isolating the repos.
//
// A missing directory is an error rather than an empty result, so a typo or an
// unmounted volume cannot read as "nothing to sync".
// It reports whether the root was given explicitly, because that changes what pins
// mean - see scopePins.
func scanRoot(flag string) (root string, explicit bool, err error) {
	if strings.TrimSpace(flag) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("cannot determine home directory: %w", err)
		}
		return home, false, nil
	}
	root = normalize(flag)
	fi, err := os.Stat(root)
	if err != nil {
		return "", true, fmt.Errorf("cannot scan %s: %w", shortPath(root), err)
	}
	if !fi.IsDir() {
		return "", true, fmt.Errorf("cannot scan %s: not a directory", shortPath(root))
	}
	return root, true, nil
}

// scopePins drops pins from outside root, and is only applied when a root was named
// explicitly.
//
// Without it --root would not isolate anything: pins are absolute paths added
// unconditionally, so `gms sync --root /tmp/scratch` would still push every pinned
// repo elsewhere on the machine - which is the accident --root exists to prevent.
//
// It is deliberately not applied to the default root. There, a pin outside $HOME is
// the escape hatch it is meant to be: the way to sync a repo on another volume.
func scopePins(pins []string, root string) []string {
	var out []string
	for _, p := range pins {
		if under(p, root) {
			out = append(out, p)
		}
	}
	return out
}

// under reports whether path is root or sits below it.
//
// Both the raw and the symlink-resolved forms are compared, because
// EvalSymlinks fails on a path that does not exist and returns it unchanged. A pin
// naming a repo on an unmounted volume, or one that has since been deleted, would
// otherwise compare an unresolved path against a resolved root and be dropped even
// though it is plainly inside it. Matching on either basis cannot wrongly admit a
// path from outside the root, since neither comparison would succeed.
func under(path, root string) bool {
	sep := string(os.PathSeparator)
	for _, pair := range [2][2]string{
		{path, root},
		{canonical(path), canonical(root)},
	} {
		p, r := pair[0], pair[1]
		if p == r || strings.HasPrefix(p, strings.TrimSuffix(r, sep)+sep) {
			return true
		}
	}
	return false
}

// canonical resolves symlinks, for comparing a path against one git reported.
// git prints resolved paths while the scan reports the path it walked, so the two
// can name the same directory differently - on macOS /var is a symlink to
// /private/var, which is enough on its own. Used only for comparison: the walked
// path is what gets displayed, because that is the name the user recognises.
func canonical(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// selectRepos builds the set gms acts on: every repo discovered under root, plus
// every worktree those repos know about, minus the ignore patterns, with the pins
// added on top.
//
// Pins are applied last so they survive an ignore pattern and so a pinned repo
// outside the scan root, or below its depth limit, still enters the set. That
// ordering is the whole reason the ignore file needs no re-include syntax:
// re-including one path out of an ignored tree is `gms add`, not a config edit.
func selectRepos(root string, cfg Config, depth int) ([]Selected, []Skipped, scanStats) {
	discovered, st := scanRepos(root, 0, depth)

	// Membership is keyed on the canonical path throughout, so the same directory
	// reached two ways cannot enter the set twice.
	seen := map[string]bool{}
	for _, f := range discovered {
		seen[canonical(f.Path)] = true
	}
	// Ranging over discovered while appending is safe: range evaluates the slice
	// once, and a worktree has no worktrees of its own to enumerate.
	for _, f := range discovered {
		for _, wt := range worktreesOf(f.Path) {
			if seen[canonical(wt)] {
				continue
			}
			seen[canonical(wt)] = true
			discovered = append(discovered, found{Path: wt, Kind: kindWorktree, Parent: f.Path})
			st.Repos++
		}
	}

	pinned := map[string]bool{}
	for _, p := range cfg.Pins {
		pinned[canonical(p)] = true
	}

	var sel []Selected
	var skip []Skipped
	for _, f := range discovered {
		if pinned[canonical(f.Path)] {
			continue // added below, from the pin list, so it is never duplicated
		}
		if f.Kind == kindSubmodule {
			skip = append(skip, Skipped{f.Path, "submodule of " + shortPath(f.Parent)})
			continue
		}
		if pat, ok := matchIgnore(cfg.Ignore, f.Path); ok {
			skip = append(skip, Skipped{f.Path, "ignored by " + pat})
			continue
		}
		sel = append(sel, Selected{Path: f.Path, Kind: f.Kind})
	}
	for _, p := range cfg.Pins {
		sel = append(sel, Selected{Path: p, Pinned: true})
	}

	sort.Slice(sel, func(a, b int) bool { return sel[a].Path < sel[b].Path })
	sort.Slice(skip, func(a, b int) bool { return skip[a].Path < skip[b].Path })
	return sel, skip, st
}

// selectedPaths is the path list in the order fanOut results will come back in.
func selectedPaths(sel []Selected) []string {
	out := make([]string, len(sel))
	for i, s := range sel {
		out[i] = s.Path
	}
	return out
}
