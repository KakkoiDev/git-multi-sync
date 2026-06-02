package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

const usageText = `git-multi-sync (gms) - keep many git repos in sync across machines

Usage:
  gms init              create ~/.git-multi-sync/repos
  gms add [path]        track a repo (default: current directory)
  gms list              show tracked repos
  gms status [flags]    fetch and report state of every tracked repo
  gms sync   [flags]    ff-pull behind repos, push ahead repos, report the rest
  gms help              show this message

Flags (status, sync):
  --format auto|human|llm|json   output format (default auto)
  --no-fetch                     skip 'git fetch'
  --jobs N                       max repos processed in parallel (default 8)

sync is safe by default: it only fast-forward pulls and pushes clean repos.
Diverged repos are never auto-merged; they are described for resolution, e.g.

  gms sync | claude -p

When stdout is piped, the human summary goes to stderr and only the resolution
prompt for diverged repos goes to stdout.`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usageText)
		return
	}
	switch os.Args[1] {
	case "init":
		cmdInit()
	case "add":
		cmdAdd(os.Args[2:])
	case "list":
		cmdList()
	case "status":
		cmdStatus(os.Args[2:])
	case "sync":
		cmdSync(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Println(usageText)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", os.Args[1], usageText)
		os.Exit(2)
	}
}

func cmdInit() {
	rf, err := initConfig()
	if err != nil {
		fatal("init failed: %v", err)
	}
	fmt.Printf("config ready: %s\n", rf)
	fmt.Println("add repos with `gms add <path>` or by editing that file.")
}

func cmdAdd(args []string) {
	target := "."
	if len(args) > 0 {
		target = args[0]
	}
	abs, err := addRepo(target)
	if err != nil {
		fatal("add failed: %v", err)
	}
	fmt.Printf("tracking %s\n", abs)
}

func cmdList() {
	paths, err := loadRepos()
	if err != nil {
		fatal("cannot read config: %v (run `gms init`)", err)
	}
	if len(paths) == 0 {
		fmt.Println("no repos tracked. add one with `gms add <path>`.")
		return
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
}

func cmdStatus(args []string) { run(args, false) }
func cmdSync(args []string)   { run(args, true) }

// run is the shared body of status and sync: parse flags, load repos, fan out,
// emit. doSync toggles whether the safe actions are performed.
func run(args []string, doSync bool) {
	name := "status"
	if doSync {
		name = "sync"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	format := fs.String("format", "auto", "output format: auto|human|llm|json")
	noFetch := fs.Bool("no-fetch", false, "skip git fetch")
	jobs := fs.Int("jobs", 8, "max parallel repos")
	fs.Parse(args)

	paths, err := loadRepos()
	if err != nil {
		fatal("cannot read config: %v (run `gms init`)", err)
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "no repos tracked. add one with `gms add <path>`.")
		return
	}

	fetch := !*noFetch
	repos := fanOut(paths, *jobs, func(p string) Repo {
		return examine(p, fetch, doSync)
	})
	emit(repos, *format)
}
