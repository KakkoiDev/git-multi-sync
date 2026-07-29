package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RemoteRepo is one repo on the GitHub account.
type RemoteRepo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Private       bool   `json:"private"`
	SSHURL        string `json:"ssh_url"`
	CloneURL      string `json:"clone_url"`
	SizeKB        int    `json:"size"`
}

// ID is the repo's identity, for joining against a local checkout's remote.
func (r RemoteRepo) ID() RepoID {
	id, _ := parseRemote("https://github.com/" + r.FullName)
	return id
}

func (r RemoteRepo) Owner() string {
	if i := strings.IndexByte(r.FullName, '/'); i >= 0 {
		return r.FullName[:i]
	}
	return ""
}

func (r RemoteRepo) Name() string {
	if i := strings.IndexByte(r.FullName, '/'); i >= 0 {
		return r.FullName[i+1:]
	}
	return r.FullName
}

// catalogFile is deliberately under the OS cache directory rather than the config
// directory. GMS_CONFIG_DIR usually points inside a repo gms itself syncs, and a
// machine-written cache does not belong in version control.
func catalogFile() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "git-multi-sync", "github.ndjson")
}

// ghAvailable reports why the GitHub catalog cannot be reached, if it cannot.
// The message says what still works, because sync and status never need gh and a
// user hitting this should not think the tool is broken.
func ghAvailable() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub catalog needs the GitHub CLI (https://cli.github.com).\n" +
			"sync, status and list do not: they work offline from what is on disk")
	}
	cmd := exec.Command("gh", "auth", "status")
	cmd.Env = ghEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh is not authenticated: run `gh auth login`\n%s", firstLine(string(out)))
	}
	return nil
}

// ghEnv keeps gh non-interactive and unpaged, so it can never block a scheduled run.
func ghEnv() []string {
	return append(os.Environ(),
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"GH_PAGER=cat",
		"NO_COLOR=1",
	)
}

// runGh runs gh and returns trimmed stdout.
func runGh(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	cmd.Env = ghEnv()
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// catalogJQ flattens each repo to the fields gms uses. affiliation deliberately
// omits "collaborator": an outside-collaborator invitation to someone else's repo
// should not silently appear in your own account listing.
const catalogJQ = `.[] | {full_name, default_branch, archived, private, ssh_url, clone_url, size}`

// fetchCatalog asks GitHub for every repo the account can push to. Around three
// seconds and three pages for a 238-repo account, which is why the result is
// cached and never fetched by sync or status.
func fetchCatalog() ([]RemoteRepo, error) {
	if err := ghAvailable(); err != nil {
		return nil, err
	}
	cmd := exec.Command("gh", "api", "--paginate",
		"-H", "Accept: application/vnd.github+json",
		"user/repos?affiliation=owner,organization_member&per_page=100",
		"--jq", catalogJQ)
	cmd.Env = ghEnv()
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = firstLine(string(ee.Stderr))
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh api failed: %s", msg)
	}
	return decodeCatalog(string(out))
}

// decodeCatalog reads the concatenated JSON objects gh --jq emits. json.Decoder
// handles a stream of values natively, so no line splitting is involved.
func decodeCatalog(s string) ([]RemoteRepo, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	var out []RemoteRepo
	for dec.More() {
		var r RemoteRepo
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("cannot parse the GitHub response: %w", err)
		}
		if r.FullName != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

func saveCatalog(repos []RemoteRepo) error {
	p := catalogFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, r := range repos {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return writeFileAtomic(p, []byte(b.String()), 0o644)
}

// loadCatalog reads the cache and when it was written. The file's mtime is the
// fetch time, so there is no second metadata file to keep consistent with it.
func loadCatalog() ([]RemoteRepo, time.Time, error) {
	p := catalogFile()
	fi, err := os.Stat(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	repos, err := decodeCatalog(string(b))
	return repos, fi.ModTime(), err
}

// fetchCatalogFn is a seam so the degrade-to-cache path can be tested without a
// network or a gh install. That path is load-bearing - it is what keeps gms usable
// offline - so it needs a test more than it needs to avoid an indirection.
var fetchCatalogFn = fetchCatalog

// catalog returns the GitHub catalog, refreshing when asked or when no cache
// exists yet.
//
// There is deliberately no expiry. A stale catalog is reported with its age and
// still used; auto-expiring it would turn a multi-second network call into a
// random tax on an interactive command. A refresh that fails while a usable cache
// exists is a warning, not an error - that is what keeps `gms list` working on a
// plane.
func catalog(refresh bool) (repos []RemoteRepo, fetched time.Time, warning string, err error) {
	cached, at, cacheErr := loadCatalog()
	if !refresh && cacheErr == nil {
		return cached, at, "", nil
	}
	fresh, fetchErr := fetchCatalogFn()
	if fetchErr == nil {
		if err := saveCatalog(fresh); err != nil {
			return fresh, time.Now(), "could not write the catalog cache: " + err.Error(), nil
		}
		return fresh, time.Now(), "", nil
	}
	if cacheErr == nil {
		return cached, at, fmt.Sprintf("using the catalog cached %s: %v", humanAge(at), fetchErr), nil
	}
	return nil, time.Time{}, "", fetchErr
}

// humanAge renders a cache timestamp the way someone deciding whether to trust it
// would ask about it.
func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
