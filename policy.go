package main

import (
	"path"
	"strings"
)

// Mode is how much authority gms has over a repo it discovered on its own.
type Mode int

const (
	ModeStatus Mode = iota // report only
	ModePull               // report and ff-pull, never push
	ModeSync               // report, ff-pull, and push
)

func (m Mode) String() string {
	switch m {
	case ModeSync:
		return "sync"
	case ModePull:
		return "pull-only"
	default:
		return "status-only"
	}
}

// parseMode reads a mode from an owners config field.
func parseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "sync":
		return ModeSync, true
	case "pull-only", "pull":
		return ModePull, true
	case "status-only", "status":
		return ModeStatus, true
	}
	return ModeStatus, false
}

// Policy is the per-repo authority for sync actions. Its zero value denies push,
// so a repo that reaches syncRepo through a path gms did not vet is inert.
type Policy struct {
	Push bool
	Why  string // always non-empty when Push is false
}

// ownerRule is one line of the owners config.
type ownerRule struct {
	Host  string // "" matches any host
	Owner string // glob
	Mode  Mode
	Text  string // the line as written, for explaining a decision
}

// matchOwner returns the authority granted by the first rule matching id.
func matchOwner(rules []ownerRule, id RepoID) (ownerRule, bool) {
	if id.IsZero() {
		return ownerRule{}, false
	}
	for _, r := range rules {
		if r.Host != "" && !globMatch(r.Host, id.Host) {
			continue
		}
		if globMatch(r.Owner, id.Owner) {
			return r, true
		}
	}
	return ownerRule{}, false
}

// policyInput is everything evalPolicy needs. Kept as an explicit struct rather
// than a Config so evalPolicy stays pure and table-testable with no filesystem.
type policyInput struct {
	Pinned     bool     // listed in the repos file, i.e. the user named it
	Branch     string   // current branch, "" when detached
	NeverPush  []string // branch globs never pushed, whatever the owner or pin
	PushTarget RepoID   // where git push would actually land
	TargetErr  string   // why PushTarget is unknown, if it is
	Rules      []ownerRule
}

// evalPolicy decides whether gms may push a repo. It is the only function that
// sets Push=true.
//
// Precedence, and the reasoning for it:
//  1. never-push wins over everything, including a pin. It is the user stating a
//     branch is off limits, and a pin is about which repo, not which branch.
//  2. A pin grants full authority. Naming a specific path is an explicit act, so
//     it gets explicit consequences - otherwise pinning a repo back in after an
//     ignore could not actually sync it.
//  3. Otherwise the owner rule decides, and no matching rule means no push.
func evalPolicy(in policyInput) Policy {
	for _, b := range in.NeverPush {
		if in.Branch != "" && globMatchCase(b, in.Branch) {
			return Policy{Why: "branch " + in.Branch + " is never-push"}
		}
	}
	if in.Pinned {
		return Policy{Push: true}
	}
	if in.TargetErr != "" {
		return Policy{Why: in.TargetErr}
	}
	if in.PushTarget.IsZero() {
		return Policy{Why: "no push target"}
	}
	rule, ok := matchOwner(in.Rules, in.PushTarget)
	if !ok {
		return Policy{Why: "owner " + in.PushTarget.Owner + " is not in owners"}
	}
	if rule.Mode == ModeSync {
		return Policy{Push: true}
	}
	return Policy{Why: "owner " + in.PushTarget.Owner + " is " + rule.Mode.String()}
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
