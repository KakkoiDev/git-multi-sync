package main

import (
	"bytes"
	"flag"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// captureOutput swaps os.Stdout and os.Stderr for pipes, runs fn, and returns
// what was written to each. flag.FlagSet resolves os.Stderr at write time, so a
// command printing its own usage is captured too. The reads run in goroutines so
// output larger than the pipe buffer cannot deadlock.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr

	outC := make(chan string, 1)
	errC := make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outC <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errC <- string(b) }()

	// The writers must be closed before reading the channels or ReadAll blocks,
	// so restore runs on the normal path; the defer is only a panic safety net,
	// and Once keeps it from double-closing.
	var once sync.Once
	restore := func() {
		once.Do(func() {
			os.Stdout, os.Stderr = oldOut, oldErr
			wOut.Close()
			wErr.Close()
		})
	}
	defer restore()
	fn()
	restore()
	return <-outC, <-errC
}

func TestCommandNamesAndAliasesUnique(t *testing.T) {
	seen := map[string]string{}
	for _, c := range commands {
		for _, n := range append([]string{c.Name}, c.Aliases...) {
			if prev, dup := seen[n]; dup {
				t.Errorf("%q is claimed by both %q and %q", n, prev, c.Name)
			}
			seen[n] = c.Name
		}
	}
}

func TestEveryCommandHasShort(t *testing.T) {
	for _, c := range commands {
		if c.Name == "" {
			t.Error("command with empty Name")
		}
		if c.Short == "" {
			t.Errorf("%s: empty Short, so it renders as a blank help line", c.Name)
		}
		if c.Run == nil {
			t.Errorf("%s: nil Run", c.Name)
		}
	}
}

func TestUsageListsEveryVisibleCommand(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	got := buf.String()
	for _, c := range commands {
		if c.Hidden {
			continue
		}
		if !strings.Contains(got, "gms "+c.Name) {
			t.Errorf("usage() omits %q", c.Name)
		}
		if !strings.Contains(got, c.Short) {
			t.Errorf("usage() omits the Short for %q", c.Name)
		}
	}
}

// TestCommonFlagsRender is the drift guard promised by commonFlagValues: a flag
// added to addCommonFlags but not given a placeholder would render as a bare
// "--name" with no value hint.
func TestCommonFlagsRender(t *testing.T) {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	addCommonFlags(fs)
	var buf bytes.Buffer
	writeCommonFlags(&buf)
	got := buf.String()

	fs.VisitAll(func(f *flag.Flag) {
		if !strings.Contains(got, "--"+f.Name) {
			t.Errorf("writeCommonFlags omits --%s", f.Name)
		}
		isBool := f.DefValue == "false" || f.DefValue == "true"
		if _, ok := commonFlagValues[f.Name]; !ok && !isBool {
			t.Errorf("--%s takes a value but has no entry in commonFlagValues", f.Name)
		}
	})
	for name := range commonFlagValues {
		if fs.Lookup(name) == nil {
			t.Errorf("commonFlagValues has a stale entry %q that addCommonFlags does not register", name)
		}
	}
}

func TestAliasesResolve(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"remove", "remove"},
		{"rm", "remove"},
		{"sync", "sync"},
		{"nope", ""},
	} {
		c := lookup(tc.in)
		got := ""
		if c != nil {
			got = c.Name
		}
		if got != tc.want {
			t.Errorf("lookup(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDispatchHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		var code int
		stdout, _ := captureOutput(t, func() { code = dispatch(args) })
		if code != 0 {
			t.Errorf("dispatch(%v) = %d, want 0", args, code)
		}
		if !strings.Contains(stdout, usageHeader) {
			t.Errorf("dispatch(%v) did not print usage to stdout", args)
		}
	}
}

func TestDispatchUnknownCommandExits2(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() { code = dispatch([]string{"bogus"}) })
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, `unknown command "bogus"`) {
		t.Errorf("stderr missing the unknown-command message: %q", stderr)
	}
	if !strings.Contains(stderr, usageHeader) {
		t.Error("unknown command should print usage to stderr")
	}
}

// TestEveryCommandAcceptsHelpFlag also pins that --help short-circuits before a
// command does any work: none of these may create config or run git.
func TestEveryCommandAcceptsHelpFlag(t *testing.T) {
	for _, c := range commands {
		var code int
		captureOutput(t, func() { code = c.Run(c, []string{"--help"}) })
		if code != 0 {
			t.Errorf("gms %s --help = %d, want 0", c.Name, code)
		}
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	for _, name := range []string{"add", "list", "status", "sync"} {
		c := lookup(name)
		var code int
		captureOutput(t, func() { code = c.Run(c, []string{"--definitely-not-a-flag"}) })
		if code != 2 {
			t.Errorf("gms %s --definitely-not-a-flag = %d, want 2", name, code)
		}
	}
}

// TestDocsCoverAllCommands turns documentation drift into a red test. The bar is
// "documented in at least one of the two files": README.md targets humans and
// SKILL.md targets agents, and neither wants every command (README has no reason
// to document `gms help`). It still catches a command nobody documented at all,
// and a doc that references a command which no longer exists.
func TestDocsCoverAllCommands(t *testing.T) {
	docs := []string{"README.md", "skills/git-multi-sync/SKILL.md"}
	texts := make([]string, len(docs))
	for i, d := range docs {
		b, err := os.ReadFile(d)
		if err != nil {
			t.Fatalf("cannot read %s: %v", d, err)
		}
		texts[i] = string(b)
	}

	for _, c := range commands {
		if c.Hidden {
			continue
		}
		found := false
		for _, txt := range texts {
			if strings.Contains(txt, "gms "+c.Name) {
				found = true
			}
		}
		if !found {
			t.Errorf("command %q appears in no doc: add `gms %s` to %s", c.Name, c.Name, strings.Join(docs, " or "))
		}
	}

	ref := regexp.MustCompile(`\bgms ([a-z][a-z-]*)`)
	for i, txt := range texts {
		// Only code is checked. In prose "gms" is the subject of a sentence, so
		// "what gms will sync" would otherwise read as a reference to a command
		// named "will".
		for _, code := range codeSpans(txt) {
			for _, m := range ref.FindAllStringSubmatch(code, -1) {
				if lookup(m[1]) == nil {
					t.Errorf("%s references `gms %s` in a code example, which is not a command", docs[i], m[1])
				}
			}
		}
	}
}

// codeSpans returns the fenced-block and inline-code content of a markdown
// document, which is where a command is actually being named rather than talked
// about.
func codeSpans(md string) []string {
	var out []string
	inline := regexp.MustCompile("`([^`\n]+)`")
	fenced := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			// A trailing shell comment is prose that happens to sit inside a code
			// fence: "gms list  # what gms will sync" is one command, not two.
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			out = append(out, line)
			continue
		}
		for _, m := range inline.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	}
	return out
}
