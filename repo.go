package main

import (
	"sort"
	"strings"
)

// State is the synced status of a single repo relative to its upstream.
type State int

const (
	StateMissing    State = iota // path gone or not a git repo
	StateError                   // a git command failed (e.g. offline)
	StateDetached                // detached HEAD, no branch
	StateNoUpstream              // branch has no @{u}
	StateDirty                   // uncommitted changes (never acted on)
	StateUpToDate                // clean, ahead 0, behind 0
	StateBehind                  // clean, behind only -> ff-pull
	StateAhead                   // clean, ahead only -> push
	StateDiverged                // clean, ahead and behind -> manual resolve
)

// facts are the raw inputs to classification, isolated so classify() is a pure,
// testable function with no git dependency.
type facts struct {
	isRepo      bool
	detached    bool
	hasUpstream bool
	dirty       bool
	ahead       int
	behind      int
}

// classify maps raw git facts to a State. Precedence matters: a dirty worktree
// is reported as Dirty regardless of ahead/behind, because sync never touches it.
func classify(f facts) State {
	switch {
	case !f.isRepo:
		return StateMissing
	case f.detached:
		return StateDetached
	case !f.hasUpstream:
		return StateNoUpstream
	case f.dirty:
		return StateDirty
	case f.ahead > 0 && f.behind > 0:
		return StateDiverged
	case f.ahead > 0:
		return StateAhead
	case f.behind > 0:
		return StateBehind
	default:
		return StateUpToDate
	}
}

// Repo is the inspected result for one configured path.
type Repo struct {
	Path     string
	Branch   string
	State    State
	Ahead    int
	Behind   int
	DirtyN   int
	Conflict []string // likely conflict files, diverged only
	Err      string   // for Missing/Error
	FetchErr string   // non-empty if the pre-inspection fetch failed
	Action   string   // what sync did
	Policy   Policy   // whether this repo may be pushed, and why not
}

// inspect gathers facts for an already-validated git repo and classifies it.
func inspect(path string) Repo {
	r := Repo{Path: path}

	branch, brErr := gitOut(path, "symbolic-ref", "--short", "-q", "HEAD")
	detached := brErr != nil || branch == ""
	r.Branch = branch

	_, upErr := gitOut(path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	hasUp := upErr == nil

	st, stErr := gitOut(path, "status", "--porcelain")
	if stErr != nil {
		r.State = StateError
		r.Err = stErr.Error()
		return r
	}
	dirty := st != ""
	if dirty {
		r.DirtyN = countLines(st)
	}

	if hasUp && !detached {
		// `git rev-list --left-right --count @{u}...HEAD` => "<behind>\t<ahead>"
		if counts, err := gitOut(path, "rev-list", "--left-right", "--count", "@{u}...HEAD"); err == nil {
			if fields := strings.Fields(counts); len(fields) == 2 {
				r.Behind = atoi(fields[0])
				r.Ahead = atoi(fields[1])
			}
		}
	}

	r.State = classify(facts{
		isRepo:      true,
		detached:    detached,
		hasUpstream: hasUp,
		dirty:       dirty,
		ahead:       r.Ahead,
		behind:      r.Behind,
	})
	if r.State == StateDiverged {
		r.Conflict = conflictFiles(path)
	}
	return r
}

// conflictFiles returns files changed on both the local and remote side since the
// merge base. Best-effort hint for the LLM, not an authoritative conflict list.
func conflictFiles(path string) []string {
	local, _ := gitOut(path, "diff", "--name-only", "@{u}...HEAD")
	remote, _ := gitOut(path, "diff", "--name-only", "HEAD...@{u}")
	localSet := map[string]bool{}
	for _, f := range strings.Split(local, "\n") {
		if f != "" {
			localSet[f] = true
		}
	}
	var both []string
	for _, f := range strings.Split(remote, "\n") {
		if f != "" && localSet[f] {
			both = append(both, f)
		}
	}
	sort.Strings(both)
	return both
}

// examine validates the path, optionally fetches, inspects, and optionally syncs.
// A fetch failure (e.g. offline) does not fail the repo: classification proceeds
// against the last-known remote refs and FetchErr is recorded so the report can
// flag that ahead/behind may be stale.
func examine(path string, fetch, doSync bool, neverPush []string) Repo {
	if !isGitRepo(path) {
		r := Repo{Path: path, State: StateMissing}
		if !pathExists(path) {
			r.Err = "path does not exist"
		} else {
			r.Err = "not a git repository"
		}
		return r
	}

	var fetchErr string
	if fetch {
		if out, err := gitRun(path, "fetch", "--quiet"); err != nil {
			fetchErr = firstLine(out)
			if fetchErr == "" {
				fetchErr = err.Error()
			}
		}
	}

	r := inspect(path)
	r.FetchErr = fetchErr
	// Evaluated after inspect because the decision needs the current branch.
	r.Policy = evalPolicy(policyInput{Branch: r.Branch, NeverPush: neverPush})
	if doSync {
		syncRepo(&r)
	}
	return r
}

// syncRepo performs the safe action for a repo's state. It never merges, never
// rebases, never force-pushes, and never touches a dirty worktree.
func syncRepo(r *Repo) {
	switch r.State {
	case StateBehind:
		if out, err := gitRun(r.Path, "pull", "--ff-only"); err != nil {
			r.State = StateError
			r.Err = out
			r.Action = "pull failed: " + firstLine(out)
		} else {
			r.Action = "ff-pulled " + plural(r.Behind, "commit")
		}
	case StateAhead:
		// The reason is always shown. A repo that quietly stops pushing looks
		// exactly like one that is in sync, which is the bug gms exists to prevent.
		if !r.Policy.Push {
			r.Action = "not pushed: " + r.Policy.Why
			return
		}
		if out, err := gitRun(r.Path, "push"); err != nil {
			r.State = StateError
			r.Err = out
			r.Action = "push failed: " + firstLine(out)
		} else {
			r.Action = "pushed " + plural(r.Ahead, "commit")
		}
	case StateDiverged:
		r.Action = "needs resolve"
	case StateDirty:
		r.Action = "skipped (dirty)"
	case StateUpToDate:
		r.Action = "up to date"
	default:
		r.Action = "skipped"
	}
}
