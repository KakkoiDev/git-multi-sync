package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".git-multi-sync")
}

func reposFile() string { return filepath.Join(configDir(), "repos") }

const reposHeader = "# git-multi-sync: one absolute repo path per line. '#' comments, blank lines ignored.\n"

// expandTilde resolves a leading ~ to the user's home directory.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// normalize turns a config entry into an absolute path.
func normalize(p string) string {
	p = expandTilde(p)
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// loadRepos reads the config file, skipping comments/blanks and de-duplicating.
func loadRepos() ([]string, error) {
	f, err := os.Open(reposFile())
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := normalize(line)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, sc.Err()
}

// initConfig creates ~/.git-multi-sync and an empty repos file if absent.
// Returns the repos file path.
func initConfig() (string, error) {
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		return "", err
	}
	rf := reposFile()
	if _, err := os.Stat(rf); os.IsNotExist(err) {
		if err := os.WriteFile(rf, []byte(reposHeader), 0o644); err != nil {
			return "", err
		}
	}
	return rf, nil
}

// canonicalize returns the git repo root for a path, or the normalized absolute
// path if it is not inside a repo. Storing the root means a repo added from any
// subdirectory resolves to the same entry, so add dedupes and remove matches.
func canonicalize(path string) string {
	abs := normalize(path)
	if root, err := gitOut(abs, "rev-parse", "--show-toplevel"); err == nil {
		return root
	}
	return abs
}

// addRepo records a repo's root path if it is a git repo and not already tracked.
// Returns the resolved root.
func addRepo(path string) (string, error) {
	abs := normalize(path)
	root, err := gitOut(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return abs, fmt.Errorf("not a git repository: %s", abs)
	}
	if _, err := initConfig(); err != nil {
		return root, err
	}
	existing, _ := loadRepos()
	for _, e := range existing {
		if e == root {
			return root, nil // already tracked
		}
	}
	f, err := os.OpenFile(reposFile(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return root, err
	}
	defer f.Close()
	_, err = f.WriteString(root + "\n")
	return root, err
}

// removeRepo drops a repo from the config, preserving comments and blank lines.
// Returns the resolved target and whether a matching entry was found.
func removeRepo(path string) (string, bool, error) {
	target := canonicalize(path)
	data, err := os.ReadFile(reposFile())
	if err != nil {
		if os.IsNotExist(err) {
			return target, false, nil
		}
		return target, false, err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && normalize(trimmed) == target {
			found = true
			continue
		}
		out = append(out, line)
	}
	if !found {
		return target, false, nil
	}
	return target, true, os.WriteFile(reposFile(), []byte(strings.Join(out, "\n")), 0o644)
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
