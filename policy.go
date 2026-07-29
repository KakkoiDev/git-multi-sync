package main

import (
	"path"
	"strings"
)

// Policy is the per-repo authority for sync actions. Its zero value denies push,
// so a repo reaching syncRepo through a path gms did not vet is inert.
type Policy struct {
	Push bool
	Why  string // always non-empty when Push is false
}

// policyInput is everything evalPolicy needs. Kept as an explicit struct rather
// than a Config so evalPolicy stays pure and table-testable with no filesystem.
type policyInput struct {
	Branch    string   // current branch, "" when detached
	NeverPush []string // branch globs never pushed, on any repo
}

// evalPolicy decides whether gms may push a repo.
//
// There is deliberately only one reason to refuse: the branch is one the user
// declared off limits. An earlier design also filtered by the remote's owner, on
// the theory that it prevented pushing to repos the user merely cloned to read.
// That was wrong on the facts - syncRepo only pushes a repo that is *ahead*, and
// you are never ahead on a clone you only read (measured: 0 of 14 third-party
// clones on a real machine). The filter was guarding against something that
// cannot happen, at the cost of a config file and three modes to learn.
//
// What remains is the whole model: a clean repo gets synced, a dirty one is the
// user's problem, and a branch named in never-push is never pushed. If a vendored
// clone ever does go ahead because the user patched it locally, the push fails
// loudly with a permission error and nothing is damaged - a worse outcome than
// silence, which is the right way round.
func evalPolicy(in policyInput) Policy {
	for _, b := range in.NeverPush {
		if in.Branch != "" && globMatchCase(b, in.Branch) {
			return Policy{Why: "branch " + in.Branch + " is never-push"}
		}
	}
	return Policy{Push: true}
}

// globMatch matches with gitignore-like semantics, folding case: '*' matches
// within one '/'-separated segment, '**' matches zero or more whole segments,
// '?' matches one non-'/' character, and [a-z] classes work as in path.Match.
// Case is folded because hostnames, GitHub owners and macOS paths are all
// case-insensitive.
func globMatch(pattern, s string) bool { return matchGlob(pattern, s, true) }

// globMatchCase is globMatch without case folding, for git branch names, which
// are case-sensitive.
func globMatchCase(pattern, s string) bool { return matchGlob(pattern, s, false) }

func matchGlob(pattern, s string, fold bool) bool {
	if fold {
		pattern, s = strings.ToLower(pattern), strings.ToLower(s)
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(s, "/"))
}

func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(seg); i++ {
				if matchSegments(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}
