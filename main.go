package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
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
	var skipped, missing, refresh *bool
	var depth, jobs *int
	var rootFlag *string
	if _, code := parseFlags(c, args, func(fs *flag.FlagSet) {
		skipped = fs.Bool("skipped", false, "list the repos that were excluded, and why")
		missing = fs.Bool("missing", false, "list account repos that are not on this machine")
		refresh = fs.Bool("refresh", false, "refetch the GitHub catalog first")
		depth = fs.Int("depth", defaultMaxDepth, "how far below the root to look for repos")
		jobs = fs.Int("jobs", 8, "max repos inspected in parallel")
		rootFlag = fs.String("root", "", "directory to scan (default: your home directory)")
	}); code != flagsOK {
		return code
	}

	cfg, err := loadConfig()
	if err != nil {
		return errf("%v", err)
	}
	root, explicit, err := scanRoot(*rootFlag)
	if err != nil {
		return errf("%v", err)
	}
	if explicit {
		cfg.Pins = scopePins(cfg.Pins, root)
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

	// The catalog is optional: without gh, without a network, or on a self-hosted
	// setup, the local half of this listing is unchanged and only the not-cloned
	// tally goes away.
	var notHere []RemoteRepo
	catalogNote := ""
	if cat, at, warning, err := catalog(*refresh); err == nil {
		if warning != "" {
			catalogNote = warning
		} else {
			catalogNote = "catalog fetched " + humanAge(at)
		}
		have := localIDs(sel, *jobs)
		for _, r := range cat {
			if _, ok := have[strings.ToLower(r.FullName)]; !ok && !r.Archived {
				notHere = append(notHere, r)
			}
		}
		sort.Slice(notHere, func(a, b int) bool { return notHere[a].FullName < notHere[b].FullName })
	} else if *refresh || *missing {
		// Only complain when the catalog was actually asked for.
		return errf("%v", err)
	}

	if *missing {
		for _, r := range notHere {
			fmt.Fprintf(tw, "%s\t%s\n", r.FullName, humanSize(r.SizeKB))
		}
		tw.Flush()
		fmt.Printf("%s on the account, not on this machine (gms clone <name>)\n", plural(len(notHere), "repo"))
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
	if len(notHere) > 0 {
		fmt.Printf("%s on your GitHub account are not here (gms list --missing)\n", plural(len(notHere), "repo"))
	}
	if catalogNote != "" {
		fmt.Fprintf(os.Stderr, "gms: %s\n", catalogNote)
	}
	return 0
}

func cmdStatus(c *command, args []string) int { return run(c, args, false) }
func cmdSync(c *command, args []string) int   { return run(c, args, true) }

// run is the shared body of status and sync: parse flags, load repos, fan out,
// emit. doSync toggles whether the safe actions are performed.
func run(c *command, args []string, doSync bool) int {
	var cf *commonFlags
	var depth *int
	var rootFlag *string
	_, code := parseFlags(c, args, func(fs *flag.FlagSet) {
		cf = addCommonFlags(fs)
		depth = fs.Int("depth", defaultMaxDepth, "how far below the root to look for repos")
		rootFlag = fs.String("root", "", "directory to scan (default: your home directory)")
	})
	if code != flagsOK {
		return code
	}

	cfg, err := loadConfig()
	if err != nil {
		return errf("%v", err)
	}
	root, explicit, err := scanRoot(*rootFlag)
	if err != nil {
		return errf("%v", err)
	}
	if explicit {
		cfg.Pins = scopePins(cfg.Pins, root)
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
