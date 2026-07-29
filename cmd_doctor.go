package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"
)

// scanned pairs a discovered repo with its resolved remote identity.
type scanned struct {
	found
	Origin RepoID
	Push   RepoID
	Err    string
}

// resolveScanned fills in each repo's remote identity. This is one git subprocess
// per repo, so it fans out.
func resolveScanned(fs []found, jobs int) []scanned {
	paths := make([]string, len(fs))
	byPath := map[string]found{}
	for i, f := range fs {
		paths[i] = f.Path
		byPath[f.Path] = f
	}
	return fanOut(paths, jobs, func(p string) scanned {
		s := scanned{found: byPath[p]}
		if id, err := originID(p); err == nil {
			s.Origin = id
		}
		if id, err := pushTarget(p); err == nil {
			s.Push = id
		} else {
			s.Err = err.Error()
		}
		return s
	})
}

func cmdDoctor(c *command, args []string) int {
	var jobs, budget, depth *int
	var all *bool
	rest, code := parseFlags(c, args, func(fs *flag.FlagSet) {
		jobs = fs.Int("jobs", 8, "max repos inspected in parallel")
		all = fs.Bool("all", false, "list every discovered repo, not just the tally")
		budget = fs.Int("budget", defaultScanBudget, "max directories to read")
		depth = fs.Int("depth", defaultMaxDepth, "how far below the root to look for repos")
	})
	if code != flagsOK {
		return code
	}

	root, err := os.UserHomeDir()
	if err != nil {
		return errf("cannot determine home directory: %v", err)
	}
	if len(rest) > 0 {
		root = normalize(rest[0])
	}

	start := time.Now()
	fs, st := scanRepos(root, *budget, *depth)
	scanElapsed := time.Since(start)

	start = time.Now()
	repos := resolveScanned(fs, *jobs)
	resolveElapsed := time.Since(start)

	fmt.Printf("scan %s (depth %d)\n", shortPath(root), st.MaxDepth)
	fmt.Printf("  %d directories read, %d pruned, %d not descended (--depth), %d unreadable, in %s\n",
		st.Dirs, st.Pruned, st.TooDeep, st.Errs, scanElapsed.Round(time.Millisecond))
	if st.Stopped {
		fmt.Printf("  INCOMPLETE: stopped at the %d-directory budget; raise it with --budget\n", *budget)
	}

	kinds := map[repoKind]int{}
	noRemote := 0
	owners := map[string]int{}
	for _, r := range repos {
		kinds[r.found.Kind]++
		if r.Push.IsZero() {
			noRemote++
			continue
		}
		owners[r.Push.Owner]++
	}
	fmt.Printf("  %d repos, %d worktrees, %d submodules; identities resolved in %s\n",
		kinds[kindPrimary], kinds[kindWorktree], kinds[kindSubmodule], resolveElapsed.Round(time.Millisecond))

	type ownerCount struct {
		Owner string
		N     int
	}
	list := make([]ownerCount, 0, len(owners))
	for o, n := range owners {
		list = append(list, ownerCount{o, n})
	}
	sort.Slice(list, func(a, b int) bool {
		if list[a].N != list[b].N {
			return list[a].N > list[b].N
		}
		return list[a].Owner < list[b].Owner
	})

	fmt.Printf("\npush-target owners (%d distinct)\n", len(list))
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, oc := range list {
		fmt.Fprintf(tw, "  %s\t%d\n", oc.Owner, oc.N)
	}
	if noRemote > 0 {
		fmt.Fprintf(tw, "  (no push target)\t%d\n", noRemote)
	}
	tw.Flush()

	if *all {
		sort.Slice(repos, func(a, b int) bool { return repos[a].Path < repos[b].Path })
		fmt.Println("\nall discovered repos")
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, r := range repos {
			id := r.Push.Full()
			if id == "" {
				id = "-"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", shortPath(r.Path), r.found.Kind, id)
		}
		tw.Flush()
	}
	return 0
}
