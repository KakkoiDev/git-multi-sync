package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// localIDs maps every selected repo's remote identity to where it lives, so clone
// can tell "already here" from "not cloned yet" by identity rather than by path.
//
// Path would be wrong: on this machine ~/.aidb holds KakkoiDev/claude-database and
// ~/Code/data-star holds KakkoiDev/try-data-star. Deciding by directory name would
// clone a second copy of a repo that is already here.
func localIDs(sel []Selected, jobs int) map[string]string {
	paths := selectedPaths(sel)
	ids := fanOut(paths, jobs, func(p string) string {
		if id, err := originID(p); err == nil {
			return strings.ToLower(id.Full())
		}
		return ""
	})
	out := map[string]string{}
	for i, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := out[id]; !dup {
			out[id] = paths[i]
		}
	}
	return out
}

// cloneRoot is where new clones land: the directory that already holds the most
// repos, so gms follows the layout the machine already has instead of asking for
// one. Falls back to home when there is no established layout to follow.
func cloneRoot(sel []Selected, home string) string {
	count := map[string]int{}
	for _, s := range sel {
		if s.Kind == kindPrimary && !s.Pinned {
			count[filepath.Dir(s.Path)]++
		}
	}
	best, bestN := "", 0
	for dir, n := range count {
		// Ties broken by path so the choice cannot vary between runs.
		if n > bestN || (n == bestN && dir < best) {
			best, bestN = dir, n
		}
	}
	if bestN < 2 {
		return home
	}
	return best
}

// resolveSpec finds the catalog entry a user's argument names. A bare name is
// matched across owners, and an ambiguous one is an error listing the candidates:
// guessing which owner was meant is how the wrong repo gets cloned.
func resolveSpec(cat []RemoteRepo, spec string) (RemoteRepo, error) {
	spec = strings.TrimSuffix(strings.TrimSpace(spec), "/")
	spec = strings.TrimSuffix(spec, ".git")

	var byFull, byName []RemoteRepo
	for _, r := range cat {
		if strings.EqualFold(r.FullName, spec) {
			byFull = append(byFull, r)
		}
		if strings.EqualFold(r.Name(), spec) {
			byName = append(byName, r)
		}
	}
	if len(byFull) == 1 {
		return byFull[0], nil
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		if sug := suggest(cat, spec); len(sug) > 0 {
			return RemoteRepo{}, fmt.Errorf("no repo named %q. did you mean: %s", spec, strings.Join(sug, ", "))
		}
		return RemoteRepo{}, fmt.Errorf("no repo named %q on this account (try `gms clone --refresh`)", spec)
	default:
		full := make([]string, 0, len(byName))
		for _, r := range byName {
			full = append(full, r.FullName)
		}
		sort.Strings(full)
		return RemoteRepo{}, fmt.Errorf("%q is ambiguous across owners: %s. name one exactly", spec, strings.Join(full, ", "))
	}
}

// suggest offers up to three catalog names containing spec, so a typo does not
// dead-end.
func suggest(cat []RemoteRepo, spec string) []string {
	if len(spec) < 3 {
		return nil
	}
	low := strings.ToLower(spec)
	var out []string
	for _, r := range cat {
		if strings.Contains(strings.ToLower(r.Name()), low) {
			out = append(out, r.FullName)
		}
	}
	sort.Strings(out)
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// cloneTarget picks a destination, falling back to an owner-prefixed name when the
// obvious one is taken. It never overwrites and never removes anything.
func cloneTarget(root string, r RemoteRepo) (string, error) {
	first := filepath.Join(root, r.Name())
	if !pathExists(first) {
		return first, nil
	}
	second := filepath.Join(root, r.Owner()+"-"+r.Name())
	if !pathExists(second) {
		return second, nil
	}
	return "", fmt.Errorf("%s and %s both exist already", shortPath(first), shortPath(second))
}

// cloneURL matches whatever protocol gh is configured for, so a clone gms makes
// authenticates the same way as one made by hand.
func cloneURL(r RemoteRepo, sshPreferred bool) string {
	if sshPreferred && r.SSHURL != "" {
		return r.SSHURL
	}
	if r.CloneURL != "" {
		return r.CloneURL
	}
	return r.SSHURL
}

// cloneResult is one repo's outcome, kept so the summary can distinguish cloned
// from skipped from failed rather than reporting a single count.
type cloneResult struct {
	Full   string
	Target string
	Reason string // why it was skipped, empty if it was cloned
	Err    string
}

func cmdClone(c *command, args []string) int {
	var jobs *int
	var into *string
	var yes, refresh, withArchived *bool
	rest, code := parseFlags(c, args, func(fs *flag.FlagSet) {
		jobs = fs.Int("jobs", 4, "max clones in parallel; network bound, so lower than sync")
		into = fs.String("into", "", "where to clone (default: wherever most repos already are)")
		yes = fs.Bool("yes", false, "skip the confirmation prompt for `all`")
		refresh = fs.Bool("refresh", false, "refetch the GitHub catalog first")
		withArchived = fs.Bool("include-archived", false, "include archived repos when cloning all")
	})
	if code != flagsOK {
		return code
	}
	if len(rest) == 0 {
		return errf("usage: %s", c.invocation())
	}

	cat, _, warning, err := catalog(*refresh)
	if err != nil {
		return errf("%v", err)
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "gms: %s\n", warning)
	}
	if len(cat) == 0 {
		return errf("the GitHub catalog is empty; run `gms clone --refresh`")
	}

	cfg, err := loadConfig()
	if err != nil {
		return errf("%v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return errf("cannot determine home directory: %v", err)
	}
	sel, _, _ := selectRepos(home, cfg, defaultMaxDepth)
	have := localIDs(sel, 8)

	root := *into
	if root == "" {
		root = cloneRoot(sel, home)
	} else {
		root = normalize(root)
	}

	var want []RemoteRepo
	if len(rest) == 1 && strings.EqualFold(rest[0], "all") {
		for _, r := range cat {
			if r.Archived && !*withArchived {
				continue
			}
			want = append(want, r)
		}
	} else {
		for _, spec := range rest {
			r, err := resolveSpec(cat, spec)
			if err != nil {
				return errf("%v", err)
			}
			want = append(want, r)
		}
	}

	// Split before doing any work, so the confirmation prompt can state exactly
	// what will happen rather than a total that includes repos already here.
	var todo []RemoteRepo
	var results []cloneResult
	for _, r := range want {
		if at, ok := have[strings.ToLower(r.FullName)]; ok {
			results = append(results, cloneResult{Full: r.FullName, Target: at, Reason: "already at " + shortPath(at)})
			continue
		}
		todo = append(todo, r)
	}
	sort.Slice(todo, func(a, b int) bool { return todo[a].FullName < todo[b].FullName })

	if len(todo) == 0 {
		reportClones(results)
		return 0
	}

	if len(todo) > 1 && !*yes {
		var kb int
		for _, r := range todo {
			kb += r.SizeKB
		}
		fmt.Printf("%s to clone into %s, about %s\n", plural(len(todo), "repo"), shortPath(root), humanSize(kb))
		if len(results) > 0 {
			fmt.Printf("  %d already here and will be skipped\n", len(results))
		}
		if !promptYes("continue?") {
			// A non-TTY lands here too: a piped or scheduled run must never trigger
			// hundreds of clones because nobody was there to say no.
			fmt.Fprintln(os.Stderr, "nothing cloned. pass --yes to skip this prompt.")
			return 2
		}
	}

	sshPreferred := false
	if proto, err := runGh("config", "get", "git_protocol"); err == nil {
		sshPreferred = strings.TrimSpace(proto) == "ssh"
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return errf("cannot create %s: %v", shortPath(root), err)
	}

	specs := make([]string, len(todo))
	byFull := map[string]RemoteRepo{}
	for i, r := range todo {
		specs[i] = r.FullName
		byFull[r.FullName] = r
	}
	cloned := fanOut(specs, *jobs, func(full string) cloneResult {
		r := byFull[full]
		target, err := cloneTarget(root, r)
		if err != nil {
			return cloneResult{Full: full, Reason: err.Error()}
		}
		// No --depth: a shallow clone complicates pushing, which is the point of
		// this tool.
		if out, err := gitRun(".", "clone", "--quiet", cloneURL(r, sshPreferred), target); err != nil {
			return cloneResult{Full: full, Target: target, Err: firstLine(out)}
		}
		return cloneResult{Full: full, Target: target}
	})

	results = append(results, cloned...)
	reportClones(results)
	for _, r := range results {
		if r.Err != "" {
			return 1
		}
	}
	return 0
}

func reportClones(results []cloneResult) {
	sort.Slice(results, func(a, b int) bool { return results[a].Full < results[b].Full })
	ok, skipped, failed := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Err != "":
			failed++
			fmt.Printf("FAILED  %s: %s\n", r.Full, r.Err)
		case r.Reason != "":
			skipped++
			fmt.Printf("skip    %s: %s\n", r.Full, r.Reason)
		default:
			ok++
			fmt.Printf("cloned  %s -> %s\n", r.Full, shortPath(r.Target))
		}
	}
	fmt.Printf("%d cloned, %d skipped, %d failed\n", ok, skipped, failed)
}

// promptYes asks for confirmation on stdin. A non-TTY answers no, so a scheduled
// or piped run cannot be taken as consent.
func promptYes(question string) bool {
	if !isTTY(os.Stdin) {
		return false
	}
	fmt.Printf("%s [y/N] ", question)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	}
	return false
}
