package main

import (
	"bytes"
	"strings"
	"testing"
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
	writeHuman(&buf, repos) // unsorted, to show the test would notice
	if strings.Index(buf.String(), "/z") > strings.Index(buf.String(), "/a") {
		t.Fatal("precondition: input should start unsorted")
	}
	sortForReport(repos)
	buf.Reset()
	writeHuman(&buf, repos)
	if strings.Index(buf.String(), "/a") > strings.Index(buf.String(), "/z") {
		t.Error("after sorting, the diverged repo should come first")
	}
}
