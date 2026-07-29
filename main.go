package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
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
	rf, err := initConfig()
	if err != nil {
		return errf("init failed: %v", err)
	}
	fmt.Printf("config ready: %s\n", rf)
	fmt.Println("add repos with `gms add <path>` or by editing that file.")
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
		fmt.Fprintf(os.Stderr, "not tracked: %s\n", resolved)
		return 1
	}
	fmt.Printf("removed %s\n", resolved)
	return 0
}

func cmdList(c *command, args []string) int {
	if _, code := parseFlags(c, args, nil); code != flagsOK {
		return code
	}
	paths, err := loadRepos()
	if err != nil {
		return errf("cannot read config: %v (run `gms init`)", err)
	}
	if len(paths) == 0 {
		fmt.Println("no repos tracked. add one with `gms add <path>`.")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, p := range paths {
		mark := "ok"
		if !isGitRepo(p) {
			if pathExists(p) {
				mark = "not-a-repo"
			} else {
				mark = "MISSING"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\n", shortPath(p), mark)
	}
	tw.Flush()
	return 0
}

func cmdStatus(c *command, args []string) int { return run(c, args, false) }
func cmdSync(c *command, args []string) int   { return run(c, args, true) }

// run is the shared body of status and sync: parse flags, load repos, fan out,
// emit. doSync toggles whether the safe actions are performed.
func run(c *command, args []string, doSync bool) int {
	var cf *commonFlags
	_, code := parseFlags(c, args, func(fs *flag.FlagSet) { cf = addCommonFlags(fs) })
	if code != flagsOK {
		return code
	}

	paths, err := loadRepos()
	if err != nil {
		return errf("cannot read config: %v (run `gms init`)", err)
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "no repos tracked. add one with `gms add <path>`.")
		return 0
	}

	// fanOut returns results index-aligned with its input, so sorting the paths
	// here is what makes the report deterministic.
	sort.Strings(paths)
	fetch := !*cf.noFetch
	repos := fanOut(paths, *cf.jobs, func(p string) Repo {
		return examine(p, fetch, doSync)
	})
	emit(repos, *cf.format)
	return 0
}
