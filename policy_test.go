package main

import "testing"

// TestPolicyZeroValueDeniesPush guards the fail-closed invariant. If a future
// refactor reorders Policy's fields or constructs one without evalPolicy, a repo
// must still be inert rather than pushable.
func TestPolicyZeroValueDeniesPush(t *testing.T) {
	var p Policy
	if p.Push {
		t.Fatal("Policy{}.Push must be false: an unvetted repo must never push")
	}
}

// TestModeZeroValueIsLeastAuthority is the same argument for Mode: an ownerRule
// built without parseMode must grant the least, not the most.
func TestModeZeroValueIsLeastAuthority(t *testing.T) {
	var m Mode
	if m != ModeStatus {
		t.Errorf("zero Mode = %v, want ModeStatus", m)
	}
}

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Mode
		ok   bool
	}{
		{"", ModeSync, true}, // an owners line with no mode field means full sync
		{"sync", ModeSync, true},
		{"SYNC", ModeSync, true},
		{"pull-only", ModePull, true},
		{"pull", ModePull, true},
		{"status-only", ModeStatus, true},
		{"status", ModeStatus, true},
		{"  sync  ", ModeSync, true},
		{"push", ModeStatus, false},
		{"yes", ModeStatus, false},
	} {
		got, ok := parseMode(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseMode(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

var kakkoiSync = ownerRule{Owner: "KakkoiDev", Mode: ModeSync, Text: "KakkoiDev sync"}
var meetsPull = ownerRule{Owner: "meetsmore", Mode: ModePull, Text: "meetsmore pull-only"}

func TestEvalPolicy(t *testing.T) {
	rules := []ownerRule{kakkoiSync, meetsPull}
	mine := RepoID{"github.com", "KakkoiDev", "dotfiles"}
	work := RepoID{"github.com", "meetsmore", "meetsone"}
	third := RepoID{"github.com", "openai", "plugins"}

	for _, tc := range []struct {
		name string
		in   policyInput
		push bool
	}{
		{"own repo syncs", policyInput{Branch: "main", PushTarget: mine, Rules: rules}, true},
		{"employer repo is pull-only", policyInput{Branch: "main", PushTarget: work, Rules: rules}, false},
		{"third party never pushes", policyInput{Branch: "main", PushTarget: third, Rules: rules}, false},
		{"no rules means no push", policyInput{Branch: "main", PushTarget: mine}, false},
		{"unresolved target denies", policyInput{Branch: "main", TargetErr: "no push remote", Rules: rules}, false},
		{"zero target denies", policyInput{Branch: "main", Rules: rules}, false},

		// A pin is the user naming a specific path, so it outranks the owner mode.
		// Without this, pinning a repo back in after an ignore could not sync it.
		{"pin overrides pull-only owner", policyInput{Pinned: true, Branch: "main", PushTarget: work, Rules: rules}, true},
		{"pin overrides unknown owner", policyInput{Pinned: true, Branch: "main", PushTarget: third, Rules: rules}, true},
		{"pin works with no target", policyInput{Pinned: true, Branch: "main"}, true},

		// never-push is about a branch, a pin is about a repo, so never-push wins.
		{"never-push beats own repo", policyInput{Branch: "master", NeverPush: []string{"master"}, PushTarget: mine, Rules: rules}, false},
		{"never-push beats a pin", policyInput{Pinned: true, Branch: "master", NeverPush: []string{"master"}, PushTarget: mine, Rules: rules}, false},
		{"never-push does not touch other branches", policyInput{Branch: "feature/x", NeverPush: []string{"master", "main"}, PushTarget: mine, Rules: rules}, true},
		{"never-push accepts globs", policyInput{Branch: "release/2.0", NeverPush: []string{"release/*"}, PushTarget: mine, Rules: rules}, false},
		{"never-push is case-sensitive like git", policyInput{Branch: "Master", NeverPush: []string{"master"}, PushTarget: mine, Rules: rules}, true},
		{"detached head ignores never-push", policyInput{Branch: "", NeverPush: []string{"master"}, PushTarget: mine, Rules: rules}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evalPolicy(tc.in)
			if got.Push != tc.push {
				t.Errorf("Push = %v, want %v (Why=%q)", got.Push, tc.push, got.Why)
			}
		})
	}
}

// TestEvalPolicyWhyAlwaysSetWhenDenied is a property over the whole input space
// that matters: a repo that silently stops pushing, with no reason shown, would
// reintroduce the exact stranded-work bug gms exists to prevent.
func TestEvalPolicyWhyAlwaysSetWhenDenied(t *testing.T) {
	rules := [][]ownerRule{nil, {kakkoiSync}, {kakkoiSync, meetsPull}, {{Owner: "*", Mode: ModeStatus}}}
	targets := []RepoID{{}, {"github.com", "KakkoiDev", "x"}, {"github.com", "openai", "x"}, {"gitlab.com", "grp/sub", "x"}}
	branches := []string{"", "main", "master", "release/1"}
	nevers := [][]string{nil, {"master"}, {"release/*"}, {"*"}}
	errs := []string{"", "no push remote"}

	for _, r := range rules {
		for _, tgt := range targets {
			for _, b := range branches {
				for _, n := range nevers {
					for _, e := range errs {
						for _, pin := range []bool{false, true} {
							in := policyInput{Pinned: pin, Branch: b, NeverPush: n, PushTarget: tgt, TargetErr: e, Rules: r}
							p := evalPolicy(in)
							if !p.Push && p.Why == "" {
								t.Fatalf("denied with no reason: %+v", in)
							}
							if p.Push && p.Why != "" {
								t.Fatalf("allowed but carries a reason %q: %+v", p.Why, in)
							}
						}
					}
				}
			}
		}
	}
}

func TestMatchOwner(t *testing.T) {
	rules := []ownerRule{
		{Host: "github.com", Owner: "KakkoiDev", Mode: ModeSync},
		{Owner: "meetsmore", Mode: ModePull},
		{Host: "git.company.internal", Owner: "team-*", Mode: ModeSync},
	}
	for _, tc := range []struct {
		name string
		id   RepoID
		ok   bool
		mode Mode
	}{
		{"host and owner match", RepoID{"github.com", "KakkoiDev", "x"}, true, ModeSync},
		{"owner match folds case", RepoID{"github.com", "kakkoidev", "x"}, true, ModeSync},
		{"host mismatch skips the rule", RepoID{"gitlab.com", "KakkoiDev", "x"}, false, ModeStatus},
		{"empty host matches any host", RepoID{"gitlab.com", "meetsmore", "x"}, true, ModePull},
		{"self-hosted owner glob", RepoID{"git.company.internal", "team-payments", "x"}, true, ModeSync},
		{"no rule matches", RepoID{"github.com", "openai", "x"}, false, ModeStatus},
		{"zero id never matches", RepoID{}, false, ModeStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := matchOwner(rules, tc.id)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && r.Mode != tc.mode {
				t.Errorf("mode = %v, want %v", r.Mode, tc.mode)
			}
		})
	}
}

// TestMatchOwnerFirstRuleWins pins the precedence so a broad "*" placed last
// cannot silently upgrade an owner listed above it, and vice versa.
func TestMatchOwnerFirstRuleWins(t *testing.T) {
	rules := []ownerRule{{Owner: "meetsmore", Mode: ModePull}, {Owner: "*", Mode: ModeSync}}
	r, ok := matchOwner(rules, RepoID{"github.com", "meetsmore", "x"})
	if !ok || r.Mode != ModePull {
		t.Errorf("got %v,%v want ModePull,true", r.Mode, ok)
	}
	r, ok = matchOwner(rules, RepoID{"github.com", "someone", "x"})
	if !ok || r.Mode != ModeSync {
		t.Errorf("got %v,%v want ModeSync,true", r.Mode, ok)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		want       bool
	}{
		{"*", "anything", true},
		{"*", "a/b", false}, // '*' does not cross a separator
		{"**", "a/b/c", true},
		{"**", "", true},
		{"KakkoiDev", "kakkoidev", true}, // folded
		{"Kakkoi*", "KakkoiDev", true},
		{"team-?", "team-a", true},
		{"team-?", "team-ab", false},
		{"[a-c]at", "bat", true},
		{"[a-c]at", "dat", false},
		{"a/b", "a/b", true},
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", false},
		{"a/**", "a/b/c", true},
		{"a/**", "a", true}, // '**' matches zero segments
		{"**/c", "a/b/c", true},
		{"a/**/d", "a/b/c/d", true},
		{"a/**/d", "a/d", true},
		{"grp/sub", "grp/sub", true},

		// The regex-vs-glob trap: '.' is a literal in a glob. Documenting globs as
		// "regex" would make users expect fooxjs to match here.
		{"foo.js", "foo.js", true},
		{"foo.js", "fooxjs", false},
		{"foo+", "foo", false},
		{"foo", "foobar", false}, // patterns are anchored at both ends
		{"oo", "foo", false},
	} {
		if got := globMatch(tc.pattern, tc.s); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

func TestGlobMatchCaseIsExact(t *testing.T) {
	if globMatchCase("master", "Master") {
		t.Error("branch matching must be case-sensitive: git treats Master and master as different refs")
	}
	if !globMatchCase("master", "master") {
		t.Error("exact branch name should match")
	}
	if !globMatchCase("release/*", "release/2.0") {
		t.Error("branch globs should work")
	}
}
