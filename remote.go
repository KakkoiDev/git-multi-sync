package main

import (
	"fmt"
	"strings"
)

// RepoID is a repo's identity on a hosting service, derived from a remote URL.
// Owner may contain '/' (GitLab subgroups) or a leading '~' (sourcehut).
type RepoID struct {
	Host  string // "github.com", lowercased
	Owner string // "KakkoiDev"
	Name  string // "dotfiles", never with a .git suffix
}

func (id RepoID) IsZero() bool { return id.Host == "" || id.Owner == "" || id.Name == "" }

// Full is the host-less form used in config and output: "KakkoiDev/dotfiles".
func (id RepoID) Full() string {
	if id.IsZero() {
		return ""
	}
	return id.Owner + "/" + id.Name
}

// String is the fully qualified form: "github.com/KakkoiDev/dotfiles". It never
// contains credentials, because parseRemote discards userinfo before returning.
func (id RepoID) String() string {
	if id.IsZero() {
		return ""
	}
	return id.Host + "/" + id.Owner + "/" + id.Name
}

// Equal folds case: GitHub treats owner and repo names case-insensitively, and
// DNS hostnames are case-insensitive.
func (a RepoID) Equal(b RepoID) bool {
	return strings.EqualFold(a.Host, b.Host) &&
		strings.EqualFold(a.Owner, b.Owner) &&
		strings.EqualFold(a.Name, b.Name)
}

// parseRemote splits a git remote URL into host, owner and name. It handles every
// form found in the wild: scp-like (git@host:owner/name.git), scheme URLs with or
// without userinfo and port, and a missing .git suffix. Userinfo is discarded
// rather than stored, so a URL like https://git::@github.com/o/n (written by tpm)
// cannot leak a credential into output or JSON.
//
// It deliberately fails for URLs with no host-side identity - file://, plain local
// paths, and anything with fewer than two path segments - because a repo with no
// remote identity can never be owner-scoped.
func parseRemote(raw string) (RepoID, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return RepoID{}, false
	}

	var authority, path string
	if i := strings.Index(s, "://"); i >= 0 {
		scheme := strings.ToLower(s[:i])
		if scheme == "file" {
			return RepoID{}, false
		}
		rest := s[i+3:]
		j := strings.Index(rest, "/")
		if j < 0 {
			return RepoID{}, false
		}
		authority, path = rest[:j], rest[j+1:]
		authority = stripUserinfo(authority)
		if k := strings.LastIndex(authority, ":"); k >= 0 {
			authority = authority[:k] // drop :port
		}
	} else if i := strings.Index(s, ":"); i > 0 && !strings.Contains(s[:i], "/") {
		authority, path = stripUserinfo(s[:i]), s[i+1:]
	} else {
		return RepoID{}, false // a bare local path has no identity
	}

	if authority == "" {
		return RepoID{}, false
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		return RepoID{}, false
	}
	for _, sg := range segs {
		if sg == "" {
			return RepoID{}, false
		}
	}

	return RepoID{
		Host:  strings.ToLower(authority),
		Owner: strings.Join(segs[:len(segs)-1], "/"),
		Name:  segs[len(segs)-1],
	}, true
}

// stripUserinfo removes everything through the last '@'. The last, not the first,
// so a password containing '@' cannot leave a fragment behind.
func stripUserinfo(authority string) string {
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		return authority[i+1:]
	}
	return authority
}

// originID reads the repo's declared origin URL. It uses `git config --get`
// rather than `git remote get-url` on purpose: the declared URL is the repo's
// identity, while get-url applies url.*.insteadOf rewrites and can report a
// mirror or a local cache instead.
func originID(dir string) (RepoID, error) {
	url, err := gitOut(dir, "config", "--get", "remote.origin.url")
	if err != nil || url == "" {
		return RepoID{}, fmt.Errorf("no origin remote")
	}
	id, ok := parseRemote(url)
	if !ok {
		return RepoID{}, fmt.Errorf("unrecognized remote URL")
	}
	return id, nil
}

// remoteNames lists the repo's configured remotes.
func remoteNames(dir string) []string {
	out, err := gitOut(dir, "remote")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// pushRemote resolves which remote `git push` would use. Returns "" when it
// cannot tell.
//
// @{push} is asked first because it is git's own answer. It only resolves once
// the remote-tracking ref exists, though, so the fallback walks the same config
// chain git documents for push resolution: branch.<name>.pushRemote,
// remote.pushDefault, branch.<name>.remote, then origin. Falling straight back to
// origin would be wrong in both directions - it would evaluate a fork checkout
// against the upstream owner, and worse, it would authorize a push against
// origin's owner when branch.<name>.remote points somewhere gms never vetted.
func pushRemote(dir string) string {
	remotes := remoteNames(dir)
	if len(remotes) == 0 {
		return ""
	}
	known := func(name string) bool {
		for _, r := range remotes {
			if r == name {
				return true
			}
		}
		return false
	}

	// @{push} renders as "<remote>/<branch>". Branch names contain '/' and remote
	// names cannot, but match against the real remote list rather than assume.
	for _, ref := range []string{"@{push}", "@{u}"} {
		ab, err := gitOut(dir, "rev-parse", "--abbrev-ref", ref)
		if err != nil || ab == "" {
			continue
		}
		for _, r := range remotes {
			if strings.HasPrefix(ab, r+"/") {
				return r
			}
		}
	}

	branch, _ := gitOut(dir, "symbolic-ref", "--short", "-q", "HEAD")
	keys := []string{"remote.pushDefault"}
	if branch != "" {
		keys = []string{"branch." + branch + ".pushRemote", "remote.pushDefault", "branch." + branch + ".remote"}
	}
	for _, k := range keys {
		if v, err := gitOut(dir, "config", "--get", k); err == nil && v != "" && known(v) {
			return v
		}
	}

	if known("origin") {
		return "origin"
	}
	if len(remotes) == 1 {
		return remotes[0]
	}
	return ""
}

// pushTarget resolves where `git push` would actually send commits. This is not
// the same as origin: in a fork checkout, origin can be the upstream project
// while @{push} points at the user's own fork. Push authority must be decided
// from this, not from origin, or gms would refuse to push repos the user owns
// and consider pushing ones they do not.
//
// `git remote get-url --push` is used here (unlike originID) because
// pushInsteadOf rewrites change where the push really lands.
func pushTarget(dir string) (RepoID, error) {
	remote := pushRemote(dir)
	if remote == "" {
		return RepoID{}, fmt.Errorf("no push remote")
	}
	url, err := gitOut(dir, "remote", "get-url", "--push", remote)
	if err != nil || url == "" {
		return RepoID{}, fmt.Errorf("remote %s has no push URL", remote)
	}
	id, ok := parseRemote(url)
	if !ok {
		return RepoID{}, fmt.Errorf("unrecognized push URL for remote %s", remote)
	}
	return id, nil
}
