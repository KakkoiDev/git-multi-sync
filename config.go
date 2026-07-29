package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// configDir is where gms keeps its config. GMS_CONFIG_DIR lets it live inside a
// repo gms already syncs (say ~/dotfiles/gms), so the same lists reach every
// machine without a server, a daemon, or hand-copying.
func configDir() string {
	if d := os.Getenv("GMS_CONFIG_DIR"); strings.TrimSpace(d) != "" {
		return normalize(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".git-multi-sync")
}

// reposFile holds pins: repos always included, even if an ignore pattern covers
// them or the scan cannot reach them.
func reposFile() string { return filepath.Join(configDir(), "repos") }

// ignoreFile holds globs excluding discovered repos.
func ignoreFile() string { return filepath.Join(configDir(), "ignore") }

// neverPushFile holds branch globs gms will never push, on any repo.
func neverPushFile() string { return filepath.Join(configDir(), "never-push") }

const reposHeader = "# git-multi-sync: repos always synced, one absolute path per line.\n" +
	"# These are pins: they win over the ignore file. '#' comments, blank lines ignored.\n"

const ignoreHeader = "# git-multi-sync: repos to leave alone. One glob per line.\n" +
	"# Globs, not regex: * stays within a path segment, ** crosses segments.\n" +
	"# Matched against both ~/short/form and the absolute path.\n" +
	"# A path pinned in the repos file wins over any pattern here.\n" +
	"#\n" +
	"# ~/bench-runs/**\n" +
	"# ~/.tmux-worktree/**\n"

const neverPushHeader = "# git-multi-sync: branches never pushed, on any repo. One glob per line.\n" +
	"# Case-sensitive, like git refs. Behind branches are still pulled.\n" +
	"#\n" +
	"# master\n" +
	"# main\n"

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

// loadLines reads a config file: one entry per line, '#' comments and blank lines
// skipped, duplicates dropped with the first occurrence winning so file order is
// preserved. A missing file yields no entries and no error - every config file is
// optional, and gms works with none of them.
func loadLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
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
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// loadRepos reads the pins, resolved to absolute paths.
func loadRepos() ([]string, error) {
	lines, err := loadLines(reposFile())
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range lines {
		p := normalize(line)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// Config is the resolved on-disk configuration. Every field is optional: with no
// config at all, gms syncs what it discovers and pushes any clean, ahead branch.
type Config struct {
	Pins      []string // absolute repo roots, always included, override Ignore
	Ignore    []string // globs excluding discovered repos
	NeverPush []string // branch globs never pushed
}

func loadConfig() (Config, error) {
	var c Config
	var err error
	if c.Pins, err = loadRepos(); err != nil {
		return c, fmt.Errorf("%s: %w", shortPath(reposFile()), err)
	}
	if c.Ignore, err = loadLines(ignoreFile()); err != nil {
		return c, fmt.Errorf("%s: %w", shortPath(ignoreFile()), err)
	}
	for _, p := range c.Ignore {
		if strings.HasPrefix(p, "!") {
			return c, fmt.Errorf("%s: %q is not supported. There is one way to re-include a repo: pin it with `gms add <path>`, which wins over every ignore pattern", shortPath(ignoreFile()), p)
		}
	}
	if c.NeverPush, err = loadLines(neverPushFile()); err != nil {
		return c, fmt.Errorf("%s: %w", shortPath(neverPushFile()), err)
	}
	return c, nil
}

// initConfig creates the config directory and a commented template for each file
// that does not exist yet. Existing files are never touched. Returns the directory.
func initConfig() (string, error) {
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		return "", err
	}
	for _, f := range []struct{ path, header string }{
		{reposFile(), reposHeader},
		{ignoreFile(), ignoreHeader},
		{neverPushFile(), neverPushHeader},
	} {
		if _, err := os.Stat(f.path); os.IsNotExist(err) {
			if err := os.WriteFile(f.path, []byte(f.header), 0o644); err != nil {
				return "", err
			}
		}
	}
	return configDir(), nil
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

// appendLine adds an entry to a config file if an equivalent one is not present.
// same decides equivalence, so paths can be compared after normalization while
// globs compare literally. Reports whether anything was written.
func appendLine(path, line string, same func(existing string) bool) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	existing, err := loadLines(path)
	if err != nil {
		return false, err
	}
	for _, e := range existing {
		if same(e) {
			return false, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	// A hand-edited file may lack a trailing newline, and appending blindly would
	// splice the new entry onto the last one.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, (line + "\n")...)
	return true, writeFileAtomic(path, data, 0o644)
}

// dropLine removes every entry matching match, preserving comments and blank lines
// because the config is documented as hand-editable. Reports how many went.
func dropLine(path string, match func(trimmed string) bool) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	removed := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && match(trimmed) {
			removed++
			continue
		}
		out = append(out, line)
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, writeFileAtomic(path, []byte(strings.Join(out, "\n")), 0o644)
}

// writeFileAtomic writes to a temporary file and renames over the target, so a
// crash or a full disk mid-write cannot leave the user's config truncated. The
// previous in-place rewrite could lose the whole repo list.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// addRepo pins a repo's root path. Returns the resolved root.
func addRepo(path string) (string, error) {
	abs := normalize(path)
	root, err := gitOut(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return abs, fmt.Errorf("not a git repository: %s", abs)
	}
	if _, err := initConfig(); err != nil {
		return root, err
	}
	_, err = appendLine(reposFile(), root, func(e string) bool { return normalize(e) == root })
	return root, err
}

// removeRepo unpins a repo. Returns the resolved target and whether an entry went.
func removeRepo(path string) (string, bool, error) {
	target := canonicalize(path)
	n, err := dropLine(reposFile(), func(t string) bool { return normalize(t) == target })
	return target, n > 0, err
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
