package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
)

const usageHeader = "git-multi-sync (gms) - keep many git repos in sync across machines"

const usageFooter = `sync is safe by default: it only fast-forward pulls and pushes clean repos.
Diverged repos are never auto-merged; they are described for resolution, e.g.

  gms sync | claude -p

When stdout is piped, the human summary goes to stderr and only the resolution
prompt for diverged repos goes to stdout.`

// command is one CLI verb. This table is the single source of truth for dispatch,
// for the rendered usage text, and for the docs-coverage test, so a new command
// cannot exist in one of the three and be missing from the others.
type command struct {
	Name    string
	Aliases []string
	Args    string // placeholder shown after the name, e.g. "[path]"
	Short   string
	Hidden  bool
	Run     func(c *command, args []string) int
}

// commands is filled in init rather than by a var literal: cmdHelp reads
// commands, and a literal initializer referencing it would be an
// initialization cycle.
var commands []*command

func init() {
	commands = []*command{
		{Name: "init", Short: "create ~/.git-multi-sync/repos", Run: cmdInit},
		{Name: "add", Args: "[path]", Short: "pin a repo, overriding any ignore (default: current directory)", Run: cmdAdd},
		{Name: "remove", Aliases: []string{"rm"}, Args: "[path]", Short: "unpin a repo (default: current directory)", Run: cmdRemove},
		{Name: "ignore", Args: "[path|glob...]", Short: "leave repos alone; with no argument, list the patterns", Run: cmdIgnore},
		{Name: "unignore", Args: "<path|glob...>", Short: "drop an ignore pattern", Run: cmdUnignore},
		{Name: "list", Args: "[flags]", Short: "show the repos gms will sync", Run: cmdList},
		{Name: "status", Args: "[flags]", Short: "fetch and report state of every tracked repo", Run: cmdStatus},
		{Name: "sync", Args: "[flags]", Short: "ff-pull behind repos, push ahead repos, report the rest", Run: cmdSync},
		{Name: "clone", Args: "<name|owner/name|all>", Short: "clone repos from your GitHub account", Run: cmdClone},
		{Name: "doctor", Args: "[dir]", Short: "scan for repos and report what was found and why", Run: cmdDoctor},
		{Name: "help", Short: "show this message", Run: cmdHelp},
	}
}

func lookup(name string) *command {
	for _, c := range commands {
		if c.Name == name {
			return c
		}
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// invocation renders the command as the user types it: "gms remove [path]".
func (c *command) invocation() string {
	if c.Args == "" {
		return "gms " + c.Name
	}
	return "gms " + c.Name + " " + c.Args
}

// dispatch resolves the first argument to a command and runs it, returning the
// process exit code. Split from main so tests can drive the whole CLI in-process.
func dispatch(args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return 0
	}
	switch args[0] {
	case "-h", "--help":
		usage(os.Stdout)
		return 0
	}
	c := lookup(args[0])
	if c == nil {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
	return c.Run(c, args[1:])
}

// usage renders the help text from the command table.
func usage(w io.Writer) {
	fmt.Fprintln(w, usageHeader)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, c := range commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\n", c.invocation(), c.Short)
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags (status, sync):")
	writeCommonFlags(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, usageFooter)
}

// commonFlags holds the values shared by status and sync.
type commonFlags struct {
	format  *string
	noFetch *bool
	jobs    *int
}

// addCommonFlags registers the flags shared by status and sync. It is the only
// place those flags are declared; the usage text is rendered from it.
func addCommonFlags(fs *flag.FlagSet) *commonFlags {
	return &commonFlags{
		format:  fs.String("format", "auto", "output format"),
		noFetch: fs.Bool("no-fetch", false, "skip 'git fetch'"),
		jobs:    fs.Int("jobs", 8, "max repos processed in parallel"),
	}
}

// commonFlagValues names the value placeholder for each non-boolean common flag.
// Kept beside addCommonFlags, and asserted complete by TestCommonFlagsRender, so
// a new flag cannot be registered without appearing in the help text.
var commonFlagValues = map[string]string{
	"format": "auto|human|llm|json",
	"jobs":   "N",
}

func writeCommonFlags(w io.Writer) {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	addCommonFlags(fs)
	tw := tabwriter.NewWriter(w, 0, 2, 3, ' ', 0)
	fs.VisitAll(func(f *flag.Flag) {
		name := "--" + f.Name
		if v, ok := commonFlagValues[f.Name]; ok {
			name += " " + v
		}
		desc := f.Usage
		if f.DefValue != "" && f.DefValue != "false" {
			desc += " (default " + f.DefValue + ")"
		}
		fmt.Fprintf(tw, "  %s\t%s\n", name, desc)
	})
	tw.Flush()
}

// flagsOK is the sentinel exit code meaning "flags parsed, keep going". Real
// exit codes are >= 0, so a negative sentinel cannot collide with one.
const flagsOK = -1

// parseFlags gives a command its own FlagSet, so `gms <cmd> --help` works and an
// unknown flag is a usage error instead of being silently taken as a path.
// register may be nil for commands with no flags of their own.
func parseFlags(c *command, args []string, register func(*flag.FlagSet)) (rest []string, exit int) {
	fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintf(out, "Usage: %s\n  %s\n", c.invocation(), c.Short)
		if register != nil {
			fmt.Fprintln(out, "\nFlags:")
			fs.PrintDefaults()
		}
	}
	if register != nil {
		register(fs)
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil, 0
		}
		return nil, 2
	}
	return fs.Args(), flagsOK
}

func cmdHelp(c *command, args []string) int {
	if _, code := parseFlags(c, args, nil); code != flagsOK {
		return code
	}
	usage(os.Stdout)
	return 0
}
