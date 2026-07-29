package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

func main() { os.Exit(dispatch(os.Args[1:])) }

// pathArg returns the first positional argument, defaulting to the current
// directory so `gms add` and `gms remove` work with no arguments.
func pathArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "."
}

func cmdInit(c *command, args []string) int {
	if _, code := parseFlags(c, args, nil); code != flagsOK {
		return code
	}
	dir, err := initConfig()
	if err != nil {
		return errf("init failed: %v", err)
	}
	fmt.Printf("config ready: %s\n", shortPath(dir))
	fmt.Println("gms finds repos on its own - there is nothing to register.")
	fmt.Println("  gms doctor            what was found, and what was skipped")
	fmt.Println("  gms ignore <glob>     leave some of it alone")
	fmt.Println("  gms add <path>        force one in, overriding any ignore")
	return 0
}

func cmdAdd(c *command, args []string) int {
	rest, code := parseFlags(c, args, nil)
	if code != flagsOK {
		return code
	}
	abs, err := addRepo(pathArg(rest))
	if err != nil {
		return errf("add failed: %v", err)
	}
	fmt.Printf("tracking %s\n", abs)
	return 0
}

func cmdRemove(c *command, args []string) int {
	rest, code := parseFlags(c, args, nil)
	if code != flagsOK {
		return code
	}
	resolved, removed, err := removeRepo(pathArg(rest))
	if err != nil {
		return errf("remove failed: %v", err)
	}
	if !removed {
		// Unpinning something that was never pinned used to be the whole story.
		// Now most repos arrive by discovery, so the useful answer is which verb
		// the user actually wanted.
		fmt.Fprintf(os.Stderr, "not pinned: %s\n", shortPath(resolved))
		if isGitRepo(resolved) {
			fmt.Fprintf(os.Stderr, "gms found this one by itself. to stop syncing it: gms ignore %s\n", shortPath(resolved))
		}
		return 1
	}
	fmt.Printf("unpinned %s\n", shortPath(resolved))
	return 0
}

func cmdList(c *command, args []string) int {
	var skipped *bool
	var depth *int
	if _, code := parseFlags(c, args, func(fs *flag.FlagSet) {
		skipped = fs.Bool("skipped", false, "list the repos that were excluded, and why")
		depth = fs.Int("depth", defaultMaxDepth, "how far below home to look for repos")
	}); code != flagsOK {
		return code
	}

	cfg, err := loadConfig()
	if err != nil {
		return errf("%v", err)
	}
	root, err := os.UserHomeDir()
	if err != nil {
		return errf("cannot determine home directory: %v", err)
	}
	sel, skip, _ := selectRepos(root, cfg, *depth)

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	if *skipped {
		for _, s := range skip {
			fmt.Fprintf(tw, "%s\t%s\n", elide(shortPath(s.Path), pathWidth), s.Reason)
		}
		tw.Flush()
		fmt.Printf("%s skipped\n", plural(len(skip), "repo"))
		return 0
	}
	for _, s := range sel {
		note := s.Kind.String()
		if s.Pinned {
			note = "pinned"
		}
		mark := ""
		if !isGitRepo(s.Path) {
			if pathExists(s.Path) {
				mark = "not-a-repo"
			} else {
				mark = "MISSING"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", elide(shortPath(s.Path), pathWidth), note, mark)
	}
	tw.Flush()
	fmt.Printf("%s synced, %d skipped (gms list --skipped)\n", plural(len(sel), "repo"), len(skip))
	return 0
}

func cmdStatus(c *command, args []string) int { return run(c, args, false) }
func cmdSync(c *command, args []string) int   { return run(c, args, true) }

// run is the shared body of status and sync: parse flags, load repos, fan out,
// emit. doSync toggles whether the safe actions are performed.
func run(c *command, args []string, doSync bool) int {
	var cf *commonFlags
	var depth *int
	_, code := parseFlags(c, args, func(fs *flag.FlagSet) {
		cf = addCommonFlags(fs)
		depth = fs.Int("depth", defaultMaxDepth, "how far below home to look for repos")
	})
	if code != flagsOK {
		return code
	}

	cfg, err := loadConfig()
	if err != nil {
		return errf("%v", err)
	}
	root, err := os.UserHomeDir()
	if err != nil {
		return errf("cannot determine home directory: %v", err)
	}

	sel, _, _ := selectRepos(root, cfg, *depth)
	if len(sel) == 0 {
		fmt.Fprintln(os.Stderr, "no repos found. run `gms doctor` to see what was scanned.")
		return 0
	}

	// selectRepos returns a sorted set and fanOut is index-aligned with its input,
	// which is what makes the report deterministic.
	fetch := !*cf.noFetch
	repos := fanOut(selectedPaths(sel), *cf.jobs, func(p string) Repo {
		return examine(p, fetch, doSync, cfg.NeverPush)
	})
	emit(repos, *cf.format)
	return 0
}
