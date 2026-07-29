package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// severity orders states by how much they need the reader, so the report can be
// read from the top and abandoned once it stops mattering.
//
// no-upstream sits near the bottom deliberately, not because it is unimportant -
// it is how you find out an agent left you on a branch that exists nowhere else -
// but because it is a standing condition rather than something that just happened.
// Putting it above a divergence would bury the thing that blocks syncing.
//
// Clean repos come last: they are already counted in the summary line, and their
// only job in the table is to be skipped.
func severity(s State) int {
	switch s {
	case StateDiverged:
		return 0 // blocks syncing and only a human can clear it
	case StateError:
		return 1
	case StateMissing:
		return 2
	case StateDirty:
		return 3 // blocks pull as well as push, so it silently stops updating
	case StateAhead:
		return 4
	case StateBehind:
		return 5
	case StateDetached:
		return 6
	case StateNoUpstream:
		return 7
	default:
		return 8 // StateUpToDate
	}
}

// sortForReport orders repos by severity, then by path. Path is the tiebreak so
// output stays deterministic, and so the order within a group is unchanged from
// when the whole report was path-sorted.
func sortForReport(repos []Repo) {
	sort.SliceStable(repos, func(a, b int) bool {
		sa, sb := severity(repos[a].State), severity(repos[b].State)
		if sa != sb {
			return sa < sb
		}
		return repos[a].Path < repos[b].Path
	})
}

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
	case StateNoUpstream:
		// Which of the two it is decides what the user has to do, so say it.
		if r.HasRemote {
			parts = append(parts, "never pushed: git push -u origin "+r.Branch)
		} else {
			parts = append(parts, "no remote: this exists only here")
		}
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

// ANSI colours for the state column.
const (
	cReset     = "\033[0m"
	cRed       = "\033[31m"
	cRedBold   = "\033[1;31m"
	cYellow    = "\033[33m"
	cCyan      = "\033[36m"
	cDim       = "\033[2m"
	cDimYellow = "\033[2;33m"
)

// stateColour maps a repo to the colour of its state cell, mirroring the severity
// order so colour reinforces the reading order instead of adding a second,
// competing signal.
//
// Colour is never the only carrier of meaning: every cell still spells out its
// state, so nothing is lost when colour is off, when output is piped or logged, or
// when the reader cannot distinguish the hues.
//
// no-upstream is the one state that splits. With a remote it is one `git push -u`
// from being safe; without one the commits exist nowhere else at all, which is the
// worst state in the report and is coloured accordingly.
func stateColour(r Repo) string {
	switch r.State {
	case StateDiverged:
		return cRedBold
	case StateError, StateMissing:
		return cRed
	case StateDirty:
		return cYellow
	case StateAhead, StateBehind:
		return cCyan
	case StateDetached:
		return cDimYellow
	case StateNoUpstream:
		if r.HasRemote {
			return cYellow
		}
		return cRed
	default:
		return cDim // clean
	}
}

// wantColour reports whether to emit ANSI codes: only to a terminal, and never
// when NO_COLOR is set (https://no-color.org). Piping to a pager therefore loses
// colour, which is the safe default - the alternative is escape codes turning up
// in a log file or an LLM prompt.
func wantColour(f *os.File) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return isTTY(f)
}

// padTo returns s padded with spaces to w visible columns, optionally wrapped in a
// colour. The colour is applied after the width is measured, which is the whole
// reason this exists rather than text/tabwriter: tabwriter measures a cell in
// bytes and has no way to discount escape sequences, so a coloured cell shifts
// every following column by the length of its escape codes. tabwriter.Escape does
// not help - it hides tabs and newlines from parsing, not width from measurement.
func padTo(s string, w int, colour string) string {
	pad := w - utf8.RuneCountInString(s)
	if pad < 0 {
		pad = 0
	}
	if colour != "" {
		s = colour + s + cReset
	}
	return s + strings.Repeat(" ", pad)
}

func writeHuman(w io.Writer, repos []Repo, colour bool) {
	type line struct{ path, branch, state, detail, colour string }
	lines := make([]line, 0, len(repos))
	var pathW, branchW, stateW int
	for _, r := range repos {
		l := line{
			path:   elide(shortPath(r.Path), pathWidth),
			branch: elide(r.Branch, branchWidth),
			state:  stateLabel(r.State),
			detail: detail(r),
		}
		if colour {
			l.colour = stateColour(r)
		}
		lines = append(lines, l)
		pathW = max(pathW, utf8.RuneCountInString(l.path))
		branchW = max(branchW, utf8.RuneCountInString(l.branch))
		stateW = max(stateW, utf8.RuneCountInString(l.state))
	}

	const gap = 2
	for _, l := range lines {
		row := padTo(l.path, pathW+gap, "") +
			padTo(l.branch, branchW+gap, "") +
			padTo(l.state, stateW+gap, l.colour) +
			l.detail
		fmt.Fprintln(w, strings.TrimRight(row, " "))
	}
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
	// Only sent for no-upstream repos, where it is the difference between "one
	// push away" and "needs a remote creating". Additive and omitempty, so an
	// existing consumer of this JSON is unaffected.
	HasRemote bool `json:"has_remote,omitempty"`
}

func writeJSON(w io.Writer, repos []Repo) {
	out := make([]jsonRepo, len(repos))
	for i, r := range repos {
		out[i] = jsonRepo{
			Path: r.Path, Branch: r.Branch, State: stateLabel(r.State),
			Ahead: r.Ahead, Behind: r.Behind, DirtyN: r.DirtyN,
			Conflict: r.Conflict, Err: r.Err, FetchErr: r.FetchErr, Action: r.Action,
			HasRemote: r.HasRemote,
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
	// Sorted once here so every format agrees. writeLLM is unaffected: it only
	// emits diverged repos, which share a severity and so keep their path order.
	sortForReport(repos)
	if format == "auto" {
		if isTTY(os.Stdout) {
			format = "human"
		} else {
			format = "llm"
		}
	}
	switch format {
	case "human":
		writeHuman(os.Stdout, repos, wantColour(os.Stdout))
		fmt.Fprintln(os.Stdout, summarize(repos))
	case "llm":
		// The human table goes to stderr here so stdout carries only the
		// resolution block. Colour follows stderr, not stdout, since that is
		// where a person is looking.
		writeHuman(os.Stderr, repos, wantColour(os.Stderr))
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
