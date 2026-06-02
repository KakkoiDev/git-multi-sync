package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// gitEnv returns the process environment with GIT_TERMINAL_PROMPT=0 so any git
// command that would prompt for credentials fails fast instead of hanging a
// parallel sync forever.
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}

// gitOut runs `git -C dir args...` and returns trimmed stdout. Use for queries
// where the clean stdout matters (rev-list, status --porcelain, etc.).
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	stdout := strings.TrimSpace(out.String())
	if err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return stdout, fmt.Errorf("%w: %s", err, firstLine(msg))
		}
		return stdout, err
	}
	return stdout, nil
}

// gitRun runs an action (fetch/pull/push) and returns combined output, since the
// human-relevant detail (Fast-forward, commit ranges, push results) lands on
// stderr.
func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// isGitRepo reports whether dir is inside a git working tree.
func isGitRepo(dir string) bool {
	out, err := gitOut(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}
