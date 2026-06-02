# git-multi-sync (`gms`)

Keep many git repos in sync across machines with one command, and hand merge
conflicts to an LLM by piping the output.

## Why

Heavy agentic coding across multiple machines means losing track of what was
pushed where: you pull `master` on the laptop only to find the latest work is
still unpushed on the desktop. That is a **visibility + reliable-push** problem,
not a merge problem. `gms` makes the state of every repo visible and brings the
clean ones in line with origin automatically. It never auto-resolves conflicts;
it describes them so an LLM (or you) can fix them.

## Install

Requires Go and git.

```sh
go build -o gms .
cp gms ~/bin/      # or anywhere on your PATH
# or: go install .   (installs to $(go env GOPATH)/bin)
```

Because the binary is named `git-multi-sync` when installed that way, `git`
discovers it as a subcommand too: `git multi-sync sync`. Build as `gms` for a
shorter alias.

## Configure

```sh
gms init                 # creates ~/.git-multi-sync/repos
gms add ~/code/project   # track a repo (defaults to the current directory)
gms add .
gms list                 # show tracked repos and whether each still exists
```

`~/.git-multi-sync/repos` is plain text: one absolute path per line, `#` for
comments, blank lines ignored. Edit it by hand or sync it between machines.

## Use

```sh
gms status               # fetch and report the state of every repo
gms sync                 # ff-pull behind repos, push ahead repos, report the rest
```

### What `sync` does, per repo

| Repo state            | Action                                    |
|-----------------------|-------------------------------------------|
| behind (clean)        | `git pull --ff-only`                      |
| ahead (clean)         | `git push`                                |
| up to date            | nothing                                   |
| diverged              | report only (described for resolution)    |
| dirty (uncommitted)   | report only, left untouched               |
| detached / no upstream| report only                               |

### Resolve conflicts with an LLM

```sh
gms sync | claude -p
```

When stdout is **piped**, the human summary goes to **stderr** (still visible in
your terminal) and **stdout carries only the resolution prompt** for diverged
repos: absolute paths, the steps to run, and the likely conflict files. The LLM
resolves and pushes; you re-run `gms sync` to confirm everything is clean.

Force the format with `--format human|llm|json` (default `auto` picks `human` on
a terminal, `llm` when piped). Other flags: `--no-fetch`, `--jobs N`.

## Safety model

`gms` is safe by default. It will never:

1. `--force` push (any form),
2. act on a dirty worktree,
3. merge, rebase, or auto-resolve a conflict,
4. push a diverged branch.

Pulls are always `--ff-only`. Diverged repos are described, never modified. All
git calls run with `GIT_TERMINAL_PROMPT=0` so a missing credential fails fast
instead of hanging the run. A failed fetch (e.g. offline) does not abort the run;
the repo is flagged "fetch failed (stale?)" and classified against last-known
remote refs.

## Make it a habit

The cure for stranded work is pushing from every machine often. Run `gms sync`
on a schedule or shell hook so origin stays canonical:

```sh
# crontab: sync every 30 minutes
*/30 * * * * /Users/you/bin/gms sync >/dev/null 2>&1
```

## Develop

```sh
go test ./...    # unit table tests for classify() + git integration harness
go vet ./...
```
