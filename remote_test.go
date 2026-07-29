package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseRemote covers every URL shape actually present on a developer machine.
// The list was collected by reading remote.*.url out of every git repo under $HOME
// rather than invented, so a form that exists in the wild cannot be missing.
func TestParseRemote(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want RepoID
		ok   bool
	}{
		{"scp", "git@github.com:KakkoiDev/dotfiles.git", RepoID{"github.com", "KakkoiDev", "dotfiles"}, true},
		{"scp no suffix", "git@github.com:KakkoiDev/dotfiles", RepoID{"github.com", "KakkoiDev", "dotfiles"}, true},
		{"https", "https://github.com/KakkoiDev/webmods.git", RepoID{"github.com", "KakkoiDev", "webmods"}, true},
		{"https no suffix", "https://github.com/tmux-plugins/tpm", RepoID{"github.com", "tmux-plugins", "tpm"}, true},
		{"https trailing slash", "https://github.com/tree-sitter/tree-sitter-go/", RepoID{"github.com", "tree-sitter", "tree-sitter-go"}, true},
		{"tpm userinfo", "https://git::@github.com/KakkoiDev/tmux-worktree", RepoID{"github.com", "KakkoiDev", "tmux-worktree"}, true},
		{"ssh scheme", "ssh://git@github.com/owner/repo.git", RepoID{"github.com", "owner", "repo"}, true},
		{"ssh scheme with port", "ssh://git@github.com:22/owner/repo.git", RepoID{"github.com", "owner", "repo"}, true},
		{"uppercase host", "https://GitHub.COM/Owner/Repo.git", RepoID{"github.com", "Owner", "Repo"}, true},
		{"gitlab subgroup", "https://gitlab.com/group/sub/proj.git", RepoID{"gitlab.com", "group/sub", "proj"}, true},
		{"sourcehut tilde owner", "https://git.sr.ht/~ecs/pgtk", RepoID{"git.sr.ht", "~ecs", "pgtk"}, true},
		{"codeberg", "https://codeberg.org/foxy/foot-themes", RepoID{"codeberg.org", "foxy", "foot-themes"}, true},
		{"self-hosted gitea", "git@git.company.internal:team/service.git", RepoID{"git.company.internal", "team", "service"}, true},

		{"empty", "", RepoID{}, false},
		{"whitespace", "   ", RepoID{}, false},
		{"file url", "file:///Users/cyril/.cargo/git/db/foo-123", RepoID{}, false},
		{"bare local path", "/srv/git/project.git", RepoID{}, false},
		{"relative path", "../sibling", RepoID{}, false},
		{"host only", "https://github.com/", RepoID{}, false},
		{"one path segment", "https://github.com/lonely.git", RepoID{}, false},
		{"scp one segment", "git@github.com:lonely.git", RepoID{}, false},
		{"garbage", "not a url", RepoID{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRemote(tc.in)
			if ok != tc.ok {
				t.Fatalf("parseRemote(%q) ok = %v, want %v (got %+v)", tc.in, ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("parseRemote(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseRemoteDiscardsCredentials pins that a credential in a remote URL can
// never reach output. tpm writes URLs of the form https://git::@github.com/o/n,
// and gms prints RepoIDs into human tables, JSON and the LLM prompt.
func TestParseRemoteDiscardsCredentials(t *testing.T) {
	for _, raw := range []string{
		"https://git::@github.com/KakkoiDev/tmux-worktree",
		"https://user:hunter2@github.com/KakkoiDev/secret.git",
		"https://token@github.com/KakkoiDev/secret.git",
		"https://user:p@ss@github.com/KakkoiDev/secret.git",
	} {
		id, ok := parseRemote(raw)
		if !ok {
			t.Fatalf("parseRemote(%q) failed", raw)
		}
		for _, leak := range []string{"@", ":", "hunter2", "token", "p@ss", "git::"} {
			if strings.Contains(id.String(), leak) {
				t.Errorf("parseRemote(%q).String() = %q, leaks %q", raw, id.String(), leak)
			}
		}
		if id.Host != "github.com" {
			t.Errorf("parseRemote(%q).Host = %q, want github.com", raw, id.Host)
		}
	}
}

func TestRepoIDEqualFoldsCase(t *testing.T) {
	a := RepoID{"github.com", "KakkoiDev", "Dotfiles"}
	b := RepoID{"GitHub.com", "kakkoidev", "dotfiles"}
	if !a.Equal(b) {
		t.Errorf("%v should equal %v (GitHub and DNS are case-insensitive)", a, b)
	}
	if a.Equal(RepoID{"github.com", "KakkoiDev", "other"}) {
		t.Error("different names must not be equal")
	}
}

func TestRepoIDZero(t *testing.T) {
	for _, id := range []RepoID{{}, {Host: "github.com"}, {Host: "github.com", Owner: "o"}} {
		if !id.IsZero() {
			t.Errorf("%+v should be zero", id)
		}
		if id.String() != "" || id.Full() != "" {
			t.Errorf("%+v should render empty, got %q / %q", id, id.String(), id.Full())
		}
	}
}

// --- git-backed tests ---

func skipWithoutGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestOriginIDNoRemote(t *testing.T) {
	skipWithoutGit(t)
	dir := filepath.Join(t.TempDir(), "solo")
	gitT(t, t.TempDir(), "init", "-b", "main", dir)
	if _, err := originID(dir); err == nil {
		t.Error("a repo with no origin must not yield a RepoID")
	}
}

// TestOriginIDUsesDeclaredURL pins the choice of `git config --get
// remote.origin.url` over `git remote get-url`: with an insteadOf rewrite in
// place, get-url reports the local mirror while the repo's identity is still the
// GitHub URL it declares.
func TestOriginIDUsesDeclaredURL(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	dir := filepath.Join(root, "r")
	gitT(t, root, "init", "-b", "main", dir)
	gitT(t, dir, "remote", "add", "origin", "https://github.com/KakkoiDev/thing.git")
	gitT(t, dir, "config", "url."+root+"/local/.insteadOf", "https://github.com/KakkoiDev/")

	if got := gitT(t, dir, "remote", "get-url", "origin"); !strings.HasPrefix(got, root) {
		t.Fatalf("precondition failed: insteadOf did not rewrite, get-url = %q", got)
	}
	id, err := originID(dir)
	if err != nil {
		t.Fatalf("originID: %v", err)
	}
	want := RepoID{"github.com", "KakkoiDev", "thing"}
	if id != want {
		t.Errorf("originID = %+v, want %+v (the declared URL, not the rewritten one)", id, want)
	}
}

// TestPushTargetPrefersUpstreamOverOrigin is the ~/Code/deer case: origin is the
// upstream project (zdavison/deer) while the branch pushes to the user's own fork
// (KakkoiDev/deer). Deciding push authority from origin would refuse a repo the
// user owns, so pushTarget must follow @{push}.
func TestPushTargetPrefersUpstreamOverOrigin(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	bare, primary := newOrigin(t, root, "deer")

	// origin keeps the upstream project's URL; fork is where the branch pushes.
	gitT(t, primary, "remote", "set-url", "origin", "git@github.com:zdavison/deer.git")
	gitT(t, primary, "remote", "add", "fork", bare)
	gitT(t, primary, "config", "branch.main.remote", "fork")
	gitT(t, primary, "config", "branch.main.merge", "refs/heads/main")

	// Before fork/main exists, @{push} cannot resolve, so this exercises the
	// config-chain fallback. Falling back to origin here would evaluate the wrong
	// owner entirely.
	if got := pushRemote(primary); got != "fork" {
		t.Fatalf("pushRemote before fetch = %q, want fork (config-chain fallback)", got)
	}

	// After fetching, @{push} resolves and must give the same answer.
	gitT(t, primary, "fetch", "fork")
	if got := pushRemote(primary); got != "fork" {
		t.Fatalf("pushRemote after fetch = %q, want fork (@{push})", got)
	}

	gitT(t, primary, "remote", "set-url", "fork", "git@github.com:KakkoiDev/deer.git")
	id, err := pushTarget(primary)
	if err != nil {
		t.Fatalf("pushTarget: %v", err)
	}
	want := RepoID{"github.com", "KakkoiDev", "deer"}
	if id != want {
		t.Errorf("pushTarget = %+v, want %+v", id, want)
	}
	if o, _ := originID(primary); o.Owner != "zdavison" {
		t.Errorf("precondition: origin owner = %q, want zdavison", o.Owner)
	}
}

// TestPushTargetDoesNotTrustOriginOverBranchRemote is the dangerous inverse of
// the deer case: origin belongs to the user, but the branch pushes to a third
// party. Resolving to origin would hand a push authorization to a repo gms never
// vetted, so the resolved target must be the third party and the policy must deny.
func TestPushTargetDoesNotTrustOriginOverBranchRemote(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	bare, primary := newOrigin(t, root, "thing")
	gitT(t, primary, "remote", "set-url", "origin", "git@github.com:KakkoiDev/thing.git")
	gitT(t, primary, "remote", "add", "elsewhere", bare)
	gitT(t, primary, "remote", "set-url", "elsewhere", "git@github.com:someone-else/thing.git")
	gitT(t, primary, "config", "branch.main.remote", "elsewhere")

	id, err := pushTarget(primary)
	if err != nil {
		t.Fatalf("pushTarget: %v", err)
	}
	if id.Owner != "someone-else" {
		t.Fatalf("pushTarget owner = %q, want someone-else", id.Owner)
	}
	p := evalPolicy(policyInput{Branch: "main", PushTarget: id, Rules: []ownerRule{kakkoiSync}})
	if p.Push {
		t.Error("gms must not authorize a push to someone-else just because origin is the user's")
	}
}

func TestPushTargetFallsBackToOrigin(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	_, primary := newOrigin(t, root, "plain")
	gitT(t, primary, "remote", "set-url", "origin", "git@github.com:KakkoiDev/plain.git")
	id, err := pushTarget(primary)
	if err != nil {
		t.Fatalf("pushTarget: %v", err)
	}
	if want := (RepoID{"github.com", "KakkoiDev", "plain"}); id != want {
		t.Errorf("pushTarget = %+v, want %+v", id, want)
	}
}

func TestPushTargetNoRemote(t *testing.T) {
	skipWithoutGit(t)
	root := t.TempDir()
	dir := filepath.Join(root, "solo")
	gitT(t, root, "init", "-b", "main", dir)
	if _, err := pushTarget(dir); err == nil {
		t.Error("a repo with no remote must not resolve a push target")
	}
}
