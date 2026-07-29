package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// plural renders a count with a naive pluralized unit ("1 commit", "2 commits").
// Only for words: a unit like MB does not take an s.
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// humanSize renders a size given in kilobytes. Repos range from a few KB to a
// gigabyte, and rounding a 300 KB repo to "0 MB" tells the reader nothing.
func humanSize(kb int) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1f GB", float64(kb)/(1024*1024))
	case kb >= 1024:
		return fmt.Sprintf("%d MB", kb/1024)
	default:
		return fmt.Sprintf("%d KB", kb)
	}
}

// errf prints to stderr and returns exit code 2 (usage/config error). Commands
// return codes rather than exiting so the whole CLI is callable from tests.
func errf(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	return 2
}

// fatal prints to stderr and exits with code 2. Used only where a return path
// does not exist (emit's unknown-format case).
func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(2)
}
