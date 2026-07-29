package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

// ignoreSpec turns a user-supplied argument into a stored pattern.
//
// An existing directory is stored as a glob covering it and everything below,
// because `gms ignore ~/bench-runs` plainly means the tree, not one repo whose
// root happens to be that directory. Anything else is stored verbatim, so a glob
// typed by hand reaches the file unchanged. The tilde form is preserved rather
// than expanded: a config shared through GMS_CONFIG_DIR has to work on a machine
// whose home directory is somewhere else.
func ignoreSpec(arg string) string {
	arg = strings.TrimSpace(arg)
	arg = strings.TrimSuffix(arg, "/")
	if strings.ContainsAny(arg, "*?[") {
		return arg
	}
	if fi, err := os.Stat(normalize(arg)); err == nil && fi.IsDir() {
		return shortPath(normalize(arg)) + "/**"
	}
	return arg
}

func cmdIgnore(c *command, args []string) int {
	rest, code := parseFlags(c, args, nil)
	if code != flagsOK {
		return code
	}
	if len(rest) == 0 {
		return listIgnorePatterns()
	}

	cfg, err := loadConfig()
	if err != nil {
		return errf("%v", err)
	}
	for _, arg := range rest {
		pat := ignoreSpec(arg)
		if strings.HasPrefix(pat, "!") {
			return errf("%q is not supported. To put a repo back, pin it: gms add <path>", pat)
		}
		added, err := appendLine(ignoreFile(), pat, func(e string) bool { return e == pat })
		if err != nil {
			return errf("cannot write %s: %v", shortPath(ignoreFile()), err)
		}
		if !added {
			fmt.Printf("already ignored: %s\n", pat)
			continue
		}
		fmt.Printf("ignoring %s\n", pat)
		if n := countPinned(cfg.Pins, pat); n > 0 {
			fmt.Printf("  note: %s still pinned and will keep syncing (gms remove <path> to drop)\n", plural(n, "repo"))
		}
	}
	return 0
}

// countPinned reports how many pins a new pattern would cover but not silence,
// since a pin outranks every ignore. Saying so at the moment of the edit is the
// only place the user is looking.
func countPinned(pins []string, pattern string) int {
	n := 0
	for _, p := range pins {
		if _, ok := matchIgnore([]string{pattern}, p); ok {
			n++
		}
	}
	return n
}

// listIgnorePatterns prints each pattern with how many discovered repos it
// currently matches, so a pattern that has stopped matching anything is visible
// rather than accumulating quietly.
func listIgnorePatterns() int {
	cfg, err := loadConfig()
	if err != nil {
		return errf("%v", err)
	}
	if len(cfg.Ignore) == 0 {
		fmt.Printf("no ignore patterns (%s)\n", shortPath(ignoreFile()))
		return 0
	}
	root, err := os.UserHomeDir()
	if err != nil {
		return errf("cannot determine home directory: %v", err)
	}
	_, skip, _ := selectRepos(root, cfg, defaultMaxDepth)

	hits := map[string]int{}
	for _, s := range skip {
		if pat, ok := strings.CutPrefix(s.Reason, "ignored by "); ok {
			hits[pat]++
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, p := range cfg.Ignore {
		note := fmt.Sprintf("%d matched", hits[p])
		if hits[p] == 0 {
			note = "matches nothing"
		}
		fmt.Fprintf(tw, "%s\t%s\n", p, note)
	}
	tw.Flush()
	return 0
}

func cmdUnignore(c *command, args []string) int {
	rest, code := parseFlags(c, args, nil)
	if code != flagsOK {
		return code
	}
	if len(rest) == 0 {
		return errf("usage: %s", c.invocation())
	}
	exit := 0
	for _, arg := range rest {
		// Both forms are accepted because `gms ignore ~/x` stores "~/x/**": the user
		// should be able to undo it by naming either what they typed or what landed
		// in the file.
		pat := strings.TrimSpace(arg)
		derived := ignoreSpec(arg)
		n, err := dropLine(ignoreFile(), func(t string) bool { return t == pat || t == derived })
		if err != nil {
			return errf("cannot write %s: %v", shortPath(ignoreFile()), err)
		}
		if n == 0 {
			fmt.Fprintf(os.Stderr, "not an ignore pattern: %s\n", pat)
			exit = 1
			continue
		}
		fmt.Printf("removed %s\n", pat)
	}
	return exit
}
