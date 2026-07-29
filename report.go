package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// isTTY reports whether f is a character device (interactive terminal).
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func stateLabel(s State) string {
	switch s {
	case StateMissing:
		return "MISSING"
	case StateError:
		return "ERROR"
	case StateDetached:
		return "detached"
	case StateNoUpstream:
		return "no-upstream"
	case StateDirty:
		return "DIRTY"
	case StateUpToDate:
		return "clean"
	case StateBehind:
		return "behind"
	case StateAhead:
		return "ahead"
	case StateDiverged:
		return "DIVERGED"
	default:
		return "?"
	}
}

// detail builds the right-hand description column for the human table.
func detail(r Repo) string {
	var parts []string
	switch r.State {
	case StateMissing, StateError:
		parts = append(parts, r.Err)
	case StateDirty:
		parts = append(parts, fmt.Sprintf("%d uncommitted", r.DirtyN))
		if r.Behind > 0 {
			parts = append(parts, fmt.Sprintf("behind %d", r.Behind))
		}
		if r.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("ahead %d", r.Ahead))
		}
	case StateBehind:
		parts = append(parts, fmt.Sprintf("behind %d", r.Behind))
	case StateAhead:
		parts = append(parts, fmt.Sprintf("ahead %d", r.Ahead))
	case StateDiverged:
		parts = append(parts, fmt.Sprintf("local %d / remote %d", r.Ahead, r.Behind))
	}
	if r.FetchErr != "" {
		parts = append(parts, "fetch failed (stale?)")
	}
	if r.Action != "" {
		parts = append(parts, "-> "+r.Action)
	}
	return strings.Join(parts, ", ")
}

// shortPath replaces the home prefix with ~ for a compact, unambiguous label.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(p, home)
		}
	}
	return p
}

// Column caps for the human table. Without them one long entry sets the width for
// every row: a worktree named after a ticket title runs past 100 characters on its
// own, and tabwriter pads every other line to match.
const (
	pathWidth   = 52
	branchWidth = 28
)

// elide shortens s to at most max characters by cutting the middle and keeping
// both ends. The tail is kept deliberately: worktree paths and branch names are
// distinguished by their last segment, so truncating the end would render
// different rows identical.
func elide(s string, max int) string {
	r := []rune(s)
	if max < 8 || len(r) <= max {
		return s
	}
	end := (max - 1) * 2 / 3
	start := max - 1 - end
	return string(r[:start]) + "…" + string(r[len(r)-end:])
}

func writeHuman(w io.Writer, repos []Repo) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, r := range repos {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			elide(shortPath(r.Path), pathWidth), elide(r.Branch, branchWidth),
			stateLabel(r.State), detail(r))
	}
	tw.Flush()
}

// writeLLM writes the resolution prompt for diverged repos and reports whether
// any were written. Paths are absolute because the consuming LLM runs elsewhere.
func writeLLM(w io.Writer, repos []Repo) bool {
	var diverged []Repo
	for _, r := range repos {
		if r.State == StateDiverged {
			diverged = append(diverged, r)
		}
	}
	if len(diverged) == 0 {
		return false
	}

	fmt.Fprintln(w, "# git-multi-sync: repos needing manual resolution")
	fmt.Fprintln(w, "These repos diverged from origin. Resolve each one and push. Use the absolute paths below.")
	fmt.Fprintln(w)
	for _, r := range diverged {
		fmt.Fprintf(w, "## %s (branch: %s)\n", r.Path, r.Branch)
		fmt.Fprintf(w, "State: diverged - %s local, %s remote\n", plural(r.Ahead, "commit"), plural(r.Behind, "commit"))
		fmt.Fprintln(w, "Steps:")
		fmt.Fprintf(w, "  cd %s\n", r.Path)
		fmt.Fprintln(w, "  git pull            # will conflict")
		fmt.Fprintln(w, "  # resolve the conflicts in the working tree, then:")
		fmt.Fprintln(w, "  git add -A && git commit --no-edit")
		fmt.Fprintln(w, "  git push")
		if len(r.Conflict) > 0 {
			fmt.Fprintln(w, "Likely conflict files (changed on both sides):")
			for _, f := range r.Conflict {
				fmt.Fprintf(w, "  %s\n", f)
			}
		}
		fmt.Fprintln(w)
	}
	return true
}

// jsonRepo is the stable serialization shape (State as a string).
type jsonRepo struct {
	Path     string   `json:"path"`
	Branch   string   `json:"branch"`
	State    string   `json:"state"`
	Ahead    int      `json:"ahead"`
	Behind   int      `json:"behind"`
	DirtyN   int      `json:"dirty_files"`
	Conflict []string `json:"conflict_files,omitempty"`
	Err      string   `json:"error,omitempty"`
	FetchErr string   `json:"fetch_error,omitempty"`
	Action   string   `json:"action,omitempty"`
}

func writeJSON(w io.Writer, repos []Repo) {
	out := make([]jsonRepo, len(repos))
	for i, r := range repos {
		out[i] = jsonRepo{
			Path: r.Path, Branch: r.Branch, State: stateLabel(r.State),
			Ahead: r.Ahead, Behind: r.Behind, DirtyN: r.DirtyN,
			Conflict: r.Conflict, Err: r.Err, FetchErr: r.FetchErr, Action: r.Action,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

// summarize is a one-line tally of states.
func summarize(repos []Repo) string {
	count := map[State]int{}
	for _, r := range repos {
		count[r.State]++
	}
	order := []State{
		StateUpToDate, StateBehind, StateAhead, StateDiverged, StateDirty,
		StateNoUpstream, StateDetached, StateError, StateMissing,
	}
	var parts []string
	for _, s := range order {
		if count[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count[s], strings.ToLower(stateLabel(s))))
		}
	}
	return fmt.Sprintf("%s: %s", plural(len(repos), "repo"), strings.Join(parts, ", "))
}

// emit renders results per the requested format. "auto" sends human output to a
// TTY, or (when piped) the human summary to stderr and the LLM block to stdout so
// `gms sync | claude -p` receives only actionable conflict prompts.
func emit(repos []Repo, format string) {
	if format == "auto" {
		if isTTY(os.Stdout) {
			format = "human"
		} else {
			format = "llm"
		}
	}
	switch format {
	case "human":
		writeHuman(os.Stdout, repos)
		fmt.Fprintln(os.Stdout, summarize(repos))
	case "llm":
		writeHuman(os.Stderr, repos)
		fmt.Fprintln(os.Stderr, summarize(repos))
		if !writeLLM(os.Stdout, repos) {
			fmt.Fprintln(os.Stderr, "Nothing to resolve.")
		}
	case "json":
		writeJSON(os.Stdout, repos)
	default:
		fatal("unknown format %q (want auto|human|llm|json)", format)
	}
}
