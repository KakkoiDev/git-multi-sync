package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// catalogFixture is real output from `gh api --paginate ... --jq`, captured rather
// than invented, so a change in the response shape shows up here.
const catalogFixture = `{"archived":false,"clone_url":"https://github.com/KakkoiDev/aidb.git","default_branch":"master","full_name":"KakkoiDev/aidb","private":false,"size":3210,"ssh_url":"git@github.com:KakkoiDev/aidb.git"}
{"archived":true,"clone_url":"https://github.com/meetsmore/old-thing.git","default_branch":"main","full_name":"meetsmore/old-thing","private":true,"size":12,"ssh_url":"git@github.com:meetsmore/old-thing.git"}
{"archived":false,"clone_url":"https://github.com/meetsmore/meetsone.git","default_branch":"master","full_name":"meetsmore/meetsone","private":true,"size":1016832,"ssh_url":"git@github.com:meetsmore/meetsone.git"}
`

// isolateCache points the catalog cache at a temp directory. os.UserCacheDir
// derives from HOME, so this keeps a test from reading or writing the real cache.
func isolateCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

func TestDecodeCatalog(t *testing.T) {
	got, err := decodeCatalog(catalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("decoded %d repos, want 3", len(got))
	}
	if got[0].FullName != "KakkoiDev/aidb" || got[0].SizeKB != 3210 {
		t.Errorf("first repo = %+v", got[0])
	}
	if !got[1].Archived {
		t.Error("the archived flag must survive: it is what excludes 26 repos from clone all")
	}
	if got[2].SizeKB != 1016832 {
		t.Errorf("size = %d, want 1016832 (a ~1 GB repo is why clone all confirms first)", got[2].SizeKB)
	}
}

func TestDecodeCatalogEmptyAndGarbage(t *testing.T) {
	if got, err := decodeCatalog(""); err != nil || len(got) != 0 {
		t.Errorf("empty input: got %v, %v", got, err)
	}
	if _, err := decodeCatalog("not json at all"); err == nil {
		t.Error("garbage should be an error, not silently zero repos")
	}
}

func TestRemoteRepoOwnerNameAndID(t *testing.T) {
	r := RemoteRepo{FullName: "meetsmore/meetsone"}
	if r.Owner() != "meetsmore" || r.Name() != "meetsone" {
		t.Errorf("owner/name = %q/%q", r.Owner(), r.Name())
	}
	want := RepoID{"github.com", "meetsmore", "meetsone"}
	if r.ID() != want {
		t.Errorf("ID = %+v, want %+v", r.ID(), want)
	}
	// A malformed entry must not panic or produce a half-built identity.
	odd := RemoteRepo{FullName: "nameonly"}
	if odd.Owner() != "" || odd.Name() != "nameonly" || !odd.ID().IsZero() {
		t.Errorf("odd = %q/%q id=%+v", odd.Owner(), odd.Name(), odd.ID())
	}
}

func TestCatalogCacheRoundTrip(t *testing.T) {
	isolateCache(t)
	in, _ := decodeCatalog(catalogFixture)
	if err := saveCatalog(in); err != nil {
		t.Fatal(err)
	}
	out, at, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) || out[2].FullName != in[2].FullName {
		t.Errorf("round trip changed the data: %+v", out)
	}
	if time.Since(at) > time.Minute {
		t.Errorf("fetch time = %v, should be about now", at)
	}
	if _, err := os.Stat(catalogFile() + ".tmp"); err == nil {
		t.Error("a .tmp file was left behind")
	}
}

// TestCatalogUsesCacheWithoutNetwork pins that a second call does not fetch. The
// fetch costs seconds, so an interactive command must not pay it every time.
func TestCatalogUsesCacheWithoutNetwork(t *testing.T) {
	isolateCache(t)
	in, _ := decodeCatalog(catalogFixture)
	if err := saveCatalog(in); err != nil {
		t.Fatal(err)
	}
	called := false
	old := fetchCatalogFn
	fetchCatalogFn = func() ([]RemoteRepo, error) { called = true; return nil, errors.New("must not be called") }
	t.Cleanup(func() { fetchCatalogFn = old })

	got, _, warning, err := catalog(false)
	if err != nil || called {
		t.Errorf("catalog(false) fetched (called=%v) err=%v", called, err)
	}
	if len(got) != 3 || warning != "" {
		t.Errorf("got %d repos, warning=%q", len(got), warning)
	}
}

// TestCatalogFallsBackToCacheWhenFetchFails is the offline behaviour: a failed
// refresh with a usable cache is a warning, not an error, so `gms list` keeps
// working on a plane.
func TestCatalogFallsBackToCacheWhenFetchFails(t *testing.T) {
	isolateCache(t)
	in, _ := decodeCatalog(catalogFixture)
	if err := saveCatalog(in); err != nil {
		t.Fatal(err)
	}
	old := fetchCatalogFn
	fetchCatalogFn = func() ([]RemoteRepo, error) { return nil, errors.New("no network") }
	t.Cleanup(func() { fetchCatalogFn = old })

	got, _, warning, err := catalog(true)
	if err != nil {
		t.Fatalf("a failed refresh with a cache must not be fatal, got %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d repos, want the cached 3", len(got))
	}
	if warning == "" {
		t.Error("using a stale cache must be reported, or the user cannot tell it is stale")
	}
	if !strings.Contains(warning, "no network") {
		t.Errorf("the warning should say why the refresh failed, got %q", warning)
	}
}

// TestCatalogErrsWithNoCacheAndNoNetwork is the one case that must fail: there is
// nothing to show and no way to get it.
func TestCatalogErrsWithNoCacheAndNoNetwork(t *testing.T) {
	isolateCache(t)
	old := fetchCatalogFn
	fetchCatalogFn = func() ([]RemoteRepo, error) { return nil, errors.New("no network") }
	t.Cleanup(func() { fetchCatalogFn = old })

	if _, _, _, err := catalog(false); err == nil {
		t.Error("no cache and no network should be an error")
	}
}

func TestHumanSize(t *testing.T) {
	for _, tc := range []struct {
		kb   int
		want string
	}{
		{0, "0 KB"},
		{3, "3 KB"},
		// 300 KB rounded to "0 MB" told the reader nothing, which is why this exists.
		{300, "300 KB"},
		{1024, "1 MB"},
		{3210, "3 MB"},
		{1016832, "993 MB"}, // meetsmore/meetsone, the largest repo on this account
		{1048576, "1.0 GB"},
		{2621440, "2.5 GB"},
	} {
		if got := humanSize(tc.kb); got != tc.want {
			t.Errorf("humanSize(%d) = %q, want %q", tc.kb, got, tc.want)
		}
	}
}

func TestHumanAge(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{now, "just now"},
		{now.Add(-5 * time.Minute), "5 minutes ago"},
		{now.Add(-3 * time.Hour), "3 hours ago"},
		{now.Add(-5 * 24 * time.Hour), "5 days ago"},
	} {
		if got := humanAge(tc.at); got != tc.want {
			t.Errorf("humanAge(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestGhMissingMessageSaysWhatStillWorks(t *testing.T) {
	// Nothing is on PATH, so LookPath("gh") must fail.
	t.Setenv("PATH", t.TempDir())
	err := ghAvailable()
	if err == nil {
		t.Skip("gh resolved anyway; cannot test the missing-gh message here")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cli.github.com") {
		t.Errorf("the message should say where to get gh, got %q", msg)
	}
	// A user without gh must not conclude that gms is broken.
	if !strings.Contains(msg, "sync") || !strings.Contains(msg, "offline") {
		t.Errorf("the message should say sync and status still work offline, got %q", msg)
	}
}
