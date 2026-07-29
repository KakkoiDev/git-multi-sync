package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSortForReportPutsWorkFirst pins the reading order: the report is meant to be
// read from the top and abandoned once it stops mattering.
func TestSortForReportPutsWorkFirst(t *testing.T) {
	repos := []Repo{
		{Path: "/z/clean", State: StateUpToDate},
		{Path: "/a/noupstream", State: StateNoUpstream},
		{Path: "/b/dirty", State: StateDirty},
		{Path: "/c/diverged", State: StateDiverged},
		{Path: "/d/ahead", State: StateAhead},
		{Path: "/e/behind", State: StateBehind},
		{Path: "/f/detached", State: StateDetached},
		{Path: "/g/error", State: StateError},
		{Path: "/h/missing", State: StateMissing},
	}
	sortForReport(repos)

	got := make([]string, len(repos))
	for i, r := range repos {
		got[i] = stateLabel(r.State)
	}
	want := []string{"DIVERGED", "ERROR", "MISSING", "DIRTY", "ahead", "behind", "detached", "no-upstream", "clean"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestSortForReportNoUpstreamBeatsCleanButLosesToWork is the specific placement
// asked for: no-upstream is real signal (an agent leaving you on a branch that
// exists nowhere else) but it is a standing condition, so it must not bury a
// divergence, and it must still rank above rows whose only job is to be skipped.
func TestSortForReportNoUpstreamBeatsCleanButLosesToWork(t *testing.T) {
	if severity(StateNoUpstream) <= severity(StateDiverged) {
		t.Error("no-upstream must not outrank a divergence")
	}
	if severity(StateNoUpstream) <= severity(StateDirty) {
		t.Error("no-upstream must not outrank uncommitted work")
	}
	if severity(StateNoUpstream) >= severity(StateUpToDate) {
		t.Error("no-upstream must outrank clean: it is signal, clean is not")
	}
}

// TestSortForReportIsDeterministic: path is the tiebreak, so two runs cannot
// disagree and a group's internal order is unchanged from path-sorted output.
func TestSortForReportIsDeterministic(t *testing.T) {
	mk := func() []Repo {
		return []Repo{
			{Path: "/c", State: StateDirty},
			{Path: "/a", State: StateDirty},
			{Path: "/b", State: StateDirty},
		}
	}
	repos := mk()
	sortForReport(repos)
	if repos[0].Path != "/a" || repos[1].Path != "/b" || repos[2].Path != "/c" {
		t.Errorf("within a group, order should be by path, got %v", repos)
	}
	for i := 0; i < 10; i++ {
		again := mk()
		sortForReport(again)
		for j := range again {
			if again[j].Path != repos[j].Path {
				t.Fatal("order varies between runs")
			}
		}
	}
}

// TestWriteLLMUnaffectedByReportOrder pins the piped-to-claude contract. Diverged
// repos all share a severity, so reordering the report cannot change the order or
// content of the resolution block.
func TestWriteLLMUnaffectedByReportOrder(t *testing.T) {
	base := []Repo{
		{Path: "/repo/b", Branch: "main", State: StateDiverged, Ahead: 1, Behind: 2, Conflict: []string{"f.txt"}},
		{Path: "/repo/clean", Branch: "main", State: StateUpToDate},
		{Path: "/repo/a", Branch: "main", State: StateDiverged, Ahead: 3, Behind: 1},
		{Path: "/repo/nu", Branch: "wip", State: StateNoUpstream},
	}

	pathSorted := append([]Repo(nil), base...)
	// Emulate the previous behaviour: whole report ordered by path.
	for i := 0; i < len(pathSorted); i++ {
		for j := i + 1; j < len(pathSorted); j++ {
			if pathSorted[j].Path < pathSorted[i].Path {
				pathSorted[i], pathSorted[j] = pathSorted[j], pathSorted[i]
			}
		}
	}
	var before bytes.Buffer
	writeLLM(&before, pathSorted)

	severitySorted := append([]Repo(nil), base...)
	sortForReport(severitySorted)
	var after bytes.Buffer
	writeLLM(&after, severitySorted)

	if before.String() != after.String() {
		t.Errorf("the resolution block changed:\n--- was ---\n%s\n--- is ---\n%s", before.String(), after.String())
	}
	if !strings.Contains(after.String(), "/repo/a") || !strings.Contains(after.String(), "/repo/b") {
		t.Error("both diverged repos should appear")
	}
	if strings.Contains(after.String(), "/repo/clean") || strings.Contains(after.String(), "/repo/nu") {
		t.Error("only diverged repos belong in the resolution block")
	}
}

// TestEmitSortsEveryFormat: sorting lives in emit so human, llm and json agree.
// A JSON consumer reading the first row to find the worst problem should get it.
func TestEmitSortsEveryFormat(t *testing.T) {
	repos := []Repo{
		{Path: "/z", State: StateUpToDate},
		{Path: "/a", State: StateDiverged},
	}
	var buf bytes.Buffer
	writeHuman(&buf, repos, false) // unsorted, to show the test would notice
	if strings.Index(buf.String(), "/z") > strings.Index(buf.String(), "/a") {
		t.Fatal("precondition: input should start unsorted")
	}
	sortForReport(repos)
	buf.Reset()
	writeHuman(&buf, repos, false)
	if strings.Index(buf.String(), "/a") > strings.Index(buf.String(), "/z") {
		t.Error("after sorting, the diverged repo should come first")
	}
}

// TestPadToMeasuresVisibleWidth is the bug that motivated dropping tabwriter:
// measured, an inline escape sequence shifted every following column by 9
// characters, and tabwriter.Escape did not help because it hides tabs and newlines
// from parsing, not width from measurement.
func TestPadToMeasuresVisibleWidth(t *testing.T) {
	plain := padTo("clean", 12, "")
	coloured := padTo("clean", 12, cDim)

	if len(plain) != 12 {
		t.Errorf("plain width = %d, want 12", len(plain))
	}
	// The coloured cell is longer in bytes but identical once codes are stripped.
	if strip(coloured) != plain {
		t.Errorf("stripped colour = %q, want %q", strip(coloured), plain)
	}
	if !strings.HasPrefix(coloured, cDim) || !strings.Contains(coloured, cReset) {
		t.Errorf("colour not applied: %q", coloured)
	}
	// Multi-byte content must be measured in runes, not bytes.
	if got := strip(padTo("héllo…", 10, cRed)); utf8.RuneCountInString(got) != 10 {
		t.Errorf("multibyte width = %d runes, want 10", utf8.RuneCountInString(got))
	}
	// Content wider than the column is never truncated by padding.
	if got := padTo("much-too-long", 4, ""); got != "much-too-long" {
		t.Errorf("over-wide cell = %q, should be returned intact", got)
	}
}

func strip(s string) string {
	for _, c := range []string{cReset, cRed, cRedBold, cYellow, cCyan, cDim, cDimYellow} {
		s = strings.ReplaceAll(s, c, "")
	}
	return s
}

// TestWriteHumanColumnsAlignWithColour is the regression fence for the tabwriter
// problem: with colour on, every row's columns must still start at the same
// visible offset.
func TestWriteHumanColumnsAlignWithColour(t *testing.T) {
	repos := []Repo{
		{Path: "/short", Branch: "main", State: StateDiverged, Ahead: 1, Behind: 1},
		{Path: "/a/much/longer/path/here", Branch: "feature/long-branch-name", State: StateUpToDate},
		{Path: "/mid/path", Branch: "wip", State: StateNoUpstream},
	}
	var plain, coloured bytes.Buffer
	writeHuman(&plain, repos, false)
	writeHuman(&coloured, repos, true)

	// Stripping the codes must reproduce the uncoloured table exactly.
	if strip(coloured.String()) != plain.String() {
		t.Errorf("colour changed the layout:\n--- plain ---\n%s\n--- stripped ---\n%s",
			plain.String(), strip(coloured.String()))
	}
	// And the state column must genuinely start at one offset for every row.
	var offsets []int
	for _, l := range strings.Split(strings.TrimRight(strip(coloured.String()), "\n"), "\n") {
		for _, label := range []string{"DIVERGED", "clean", "no-upstream"} {
			if i := strings.Index(l, label); i >= 0 {
				offsets = append(offsets, i)
			}
		}
	}
	if len(offsets) != 3 {
		t.Fatalf("expected one state per row, found %v", offsets)
	}
	for _, o := range offsets[1:] {
		if o != offsets[0] {
			t.Errorf("state column offsets differ: %v - colour broke alignment", offsets)
		}
	}
}

// TestColourNeverLeaksIntoMachineOutput: the resolution block is piped into an LLM
// and the JSON is parsed by tools. An escape sequence in either is corruption.
func TestColourNeverLeaksIntoMachineOutput(t *testing.T) {
	repos := []Repo{
		{Path: "/a", Branch: "main", State: StateDiverged, Ahead: 1, Behind: 2, Conflict: []string{"f.txt"}},
		{Path: "/b", Branch: "wip", State: StateNoUpstream},
	}
	var llm, js bytes.Buffer
	writeLLM(&llm, repos)
	writeJSON(&js, repos)

	for name, out := range map[string]string{"writeLLM": llm.String(), "writeJSON": js.String()} {
		if strings.Contains(out, "\033") {
			t.Errorf("%s emitted an ANSI escape: %q", name, out)
		}
	}
}

// TestWantColourRespectsNoColor covers the https://no-color.org convention. The TTY
// case cannot be exercised here because a test's stdout is a pipe, which is itself
// the behaviour that keeps escapes out of logs.
func TestWantColourRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if wantColour(os.Stdout) {
		t.Error("NO_COLOR must disable colour")
	}
	// Even set to empty, per the convention: presence is what counts.
	t.Setenv("NO_COLOR", "")
	if wantColour(os.Stdout) {
		t.Error("NO_COLOR present but empty must still disable colour")
	}
}

// TestStateColourCoversEveryState guards against a new state defaulting to the
// dim "nothing to see" colour, which would visually hide it.
func TestStateColourCoversEveryState(t *testing.T) {
	for _, s := range []State{
		StateMissing, StateError, StateDetached, StateNoUpstream,
		StateDirty, StateUpToDate, StateBehind, StateAhead, StateDiverged,
	} {
		got := stateColour(Repo{State: s})
		if got == "" {
			t.Errorf("%s has no colour", stateLabel(s))
		}
		if s != StateUpToDate && got == cDim {
			t.Errorf("%s is dim, which reads as 'ignore me'", stateLabel(s))
		}
	}
	// The split that carries real meaning: nowhere-else is worse than one-push-away.
	nowhere := stateColour(Repo{State: StateNoUpstream, HasRemote: false})
	onePush := stateColour(Repo{State: StateNoUpstream, HasRemote: true})
	if nowhere == onePush {
		t.Error("no-upstream with and without a remote must look different")
	}
}
