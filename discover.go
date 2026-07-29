package main

import (
	"os"
	"path/filepath"
	"strings"
)

// repoKind distinguishes a real checkout from a linked worktree or a submodule.
// A linked worktree is a legitimate place for unpushed work, so it is discovered
// in its own right. A submodule is governed by its superproject, so it is not.
type repoKind int

const (
	kindPrimary repoKind = iota
	kindWorktree
	kindSubmodule
)

func (k repoKind) String() string {
	switch k {
	case kindWorktree:
		return "worktree"
	case kindSubmodule:
		return "submodule"
	default:
		return "repo"
	}
}

// found is a repo root on disk, before any policy is applied.
type found struct {
	Path   string
	Kind   repoKind
	Parent string // the owning checkout, for a worktree or submodule
}

// scanStats records what the walk did. A bounded scan must never be silent about
// what it skipped: "nothing to sync" and "I gave up early" have to look different,
// and every directory not descended is counted so `doctor` can report it.
type scanStats struct {
	Dirs     int  // directories read
	Pruned   int  // directories skipped by name or path
	TooDeep  int  // directories not descended because of the depth limit
	Repos    int  // repo roots found
	Errs     int  // unreadable directories
	Stopped  bool // budget exhausted, so the result is incomplete
	MaxDepth int  // the limit that was applied
}

// prunedNames are directory names never descended into, at any depth. They are
// large, tool-managed, and hold no repo a user works on. This list only affects
// walk cost - correctness comes from the owner allowlist, so a repo hidden in one
// of these would have been excluded anyway.
var prunedNames = map[string]bool{
	"node_modules": true,
	"__pycache__":  true,
	".Trash":       true,
	".venv":        true,
	".venvs":       true,
	".tox":         true,
	".terraform":   true,
}

// prunedRelPaths are skipped by their path relative to the scan root, for
// directories whose bare name is too broad to prune. ~/Library cannot be pruned by
// name because it holds a real repo (Application Support/pico-8), but its cache and
// toolchain subtrees never will.
//
// This list is a speed optimization only, and is verified not to change the result:
// with it emptied, the same repos are found, in 457ms instead of 182ms. Correctness
// comes from the owner allowlist, so a repo hidden below one of these would be
// rejected anyway. Being relative to the scan root, it only takes effect for the
// default $HOME scan.
var prunedRelPaths = map[string]bool{
	"Library/Android":          true,
	"Library/Caches":           true,
	"Library/Containers":       true,
	"Library/Developer":        true,
	"Library/Group Containers": true,
	"Library/Python":           true,
	"go/pkg":                   true,
	".npm/_cacache":            true,
	".cache":                   true,
	".lmstudio":                true,
	".nodenv/versions":         true,
	".rustup/toolchains":       true,
}

// defaultScanBudget caps directories read so a pathological tree cannot hang a
// scheduled sync. Measured on a working machine the real walk reads far fewer,
// because it stops at every repo root instead of descending into it.
const defaultScanBudget = 300000

// defaultMaxDepth bounds how far below the scan root a repo is looked for. It is
// the main cost control, and it is preferred over a longer prune list because it
// is platform-neutral: a name blocklist is endless and OS-specific, while depth
// reflects how people actually organise checkouts.
//
// Measured on this machine: every repo the user works on sits at depth 1-4
// (~/dotfiles, ~/Code/x, ~/.tmux-worktree/meetsone/branch/name,
// ~/Library/Application Support/pico-8). Below that are only tool-managed clones
// (editor grammars at depth 5, package caches deeper still), which the owner
// allowlist would reject anyway.
const defaultMaxDepth = 4

// scanner holds walk state so the recursion stays a small method.
type scanner struct {
	root     string
	budget   int
	maxDepth int
	st       scanStats
	out      []found
}

// scanRepos walks root depth-first and returns every repo root at or below it.
// It stops at each repo root rather than descending, which is what makes an
// unbounded $HOME scan affordable: a single checkout can hold hundreds of
// thousands of directories that are of no interest once its root is known.
//
// Symlinks are never followed. On a real machine $HOME is full of them - here 10
// top-level entries link into ~/dotfiles and ~/.claude/skills holds 36 more,
// several pointing back into repos already being scanned - so following them
// would yield the same repo under many paths, and cycles.
func scanRepos(root string, budget, maxDepth int) ([]found, scanStats) {
	if budget <= 0 {
		budget = defaultScanBudget
	}
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	s := &scanner{root: root, budget: budget, maxDepth: maxDepth}
	s.st.MaxDepth = maxDepth
	s.descend(root, 0)
	return s.out, s.st
}

// pruned reports whether dir should be skipped without being read.
func (s *scanner) pruned(dir, name string) bool {
	if prunedNames[name] {
		return true
	}
	rel, err := filepath.Rel(s.root, filepath.Join(dir, name))
	return err == nil && prunedRelPaths[rel]
}

func (s *scanner) descend(dir string, depth int) bool {
	if s.st.Dirs >= s.budget {
		s.st.Stopped = true
		return false
	}
	s.st.Dirs++

	ents, err := os.ReadDir(dir)
	if err != nil {
		s.st.Errs++
		return true // an unreadable directory is not fatal to the whole scan
	}

	// A .git entry makes this a repo root. Record it and do not descend: nothing
	// inside a checkout is a separate repo worth syncing, and skipping it is the
	// difference between reading a few thousand directories and a million.
	for _, e := range ents {
		if e.Name() != ".git" {
			continue
		}
		if k, parent, ok := classifyGitEntry(filepath.Join(dir, ".git"), e.IsDir()); ok {
			s.out = append(s.out, found{Path: dir, Kind: k, Parent: parent})
			s.st.Repos++
		}
		return true
	}

	for _, e := range ents {
		// IsDir is false for a symlink, so symlinks are skipped here by construction.
		if !e.IsDir() {
			continue
		}
		if s.pruned(dir, e.Name()) {
			s.st.Pruned++
			continue
		}
		if depth+1 > s.maxDepth {
			s.st.TooDeep++
			continue
		}
		if !s.descend(filepath.Join(dir, e.Name()), depth+1) {
			return false
		}
	}
	return true
}

// classifyGitEntry inspects a .git path without running git. A directory is a
// primary checkout. A file holds "gitdir: <path>", where a path under
// .git/worktrees/ is a linked worktree and one under .git/modules/ is a submodule.
func classifyGitEntry(gitPath string, isDir bool) (repoKind, string, bool) {
	if isDir {
		return kindPrimary, "", true
	}
	b, err := os.ReadFile(gitPath)
	if err != nil {
		return kindPrimary, "", false
	}
	line := strings.TrimSpace(string(b))
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if target == line || target == "" {
		return kindPrimary, "", false // not a gitlink file
	}
	if i := strings.Index(target, "/.git/worktrees/"); i >= 0 {
		return kindWorktree, target[:i], true
	}
	if i := strings.Index(target, "/.git/modules/"); i >= 0 {
		return kindSubmodule, target[:i], true
	}
	return kindPrimary, "", true
}
