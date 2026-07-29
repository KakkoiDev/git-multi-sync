package main

import (
	"strings"
	"testing"
)

// TestPolicyZeroValueDeniesPush guards the fail-closed invariant. If a future
// refactor reorders Policy's fields or builds one without evalPolicy, a repo must
// still be inert rather than pushable.
func TestPolicyZeroValueDeniesPush(t *testing.T) {
	var p Policy
	if p.Push {
		t.Fatal("Policy{}.Push must be false: an unvetted repo must never push")
	}
}

func TestEvalPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   policyInput
		push bool
	}{
		// The whole model: a clean repo gets pushed unless its branch is off limits.
		{"clean branch pushes", policyInput{Branch: "main"}, true},
		{"no never-push list", policyInput{Branch: "master"}, true},

		{"never-push blocks the named branch", policyInput{Branch: "master", NeverPush: []string{"master"}}, false},
		{"never-push leaves other branches alone", policyInput{Branch: "feature/x", NeverPush: []string{"master", "main"}}, true},
		{"never-push accepts globs", policyInput{Branch: "release/2.0", NeverPush: []string{"release/*"}}, false},
		{"never-push star blocks everything", policyInput{Branch: "anything", NeverPush: []string{"*"}}, false},

		// Git refs are case-sensitive, so Master and master are different branches
		// and gms must not silently treat one rule as covering both.
		{"never-push is case-sensitive like git", policyInput{Branch: "Master", NeverPush: []string{"master"}}, true},

		// A detached HEAD has no branch to match, and syncRepo skips it anyway.
		{"detached head has no branch to block", policyInput{Branch: "", NeverPush: []string{"master"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evalPolicy(tc.in)
			if got.Push != tc.push {
				t.Errorf("Push = %v, want %v (Why=%q)", got.Push, tc.push, got.Why)
			}
		})
	}
}

// TestEvalPolicyWhyAlwaysSetWhenDenied is the property that matters most: a repo
// that silently stops pushing, with no reason shown, would reintroduce the exact
// stranded-work bug gms exists to prevent.
func TestEvalPolicyWhyAlwaysSetWhenDenied(t *testing.T) {
	for _, b := range []string{"", "main", "master", "release/1", "Master", "a/b/c"} {
		for _, n := range [][]string{nil, {}, {"master"}, {"release/*"}, {"*"}, {"main", "master"}} {
			p := evalPolicy(policyInput{Branch: b, NeverPush: n})
			if !p.Push && p.Why == "" {
				t.Fatalf("denied with no reason: branch=%q never-push=%v", b, n)
			}
			if p.Push && p.Why != "" {
				t.Fatalf("allowed but carries a reason %q: branch=%q never-push=%v", p.Why, b, n)
			}
		}
	}
}

// TestEvalPolicyNamesTheBranchItBlocked pins that the reason is specific enough to
// act on. "not pushed" without naming the branch sends the user hunting.
func TestEvalPolicyNamesTheBranchItBlocked(t *testing.T) {
	p := evalPolicy(policyInput{Branch: "master", NeverPush: []string{"mast*"}})
	if p.Push {
		t.Fatal("should be denied")
	}
	if !strings.Contains(p.Why, "master") {
		t.Errorf("Why = %q, should name the branch", p.Why)
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

		// The real ignore patterns this has to support.
		{"~/.tmux-worktree/**", "~/.tmux-worktree/meetsone/on-call", true},
		{"~/.tmux-worktree/**", "~/Code/meetsone", false},
		{"~/bench-runs/**", "~/bench-runs/work/pr-23934", true},

		// The regex-vs-glob trap: '.' is a literal in a glob. Calling these
		// "regex" in the docs would make users expect fooxjs to match here.
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
