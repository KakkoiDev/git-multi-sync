# git-multi-sync (`gms`)

Keep the projects you work on backed up and in step across every machine, with one
command. Nothing to register: `gms` finds the repos itself. Merge conflicts are
described, not resolved, so you can pipe them to an LLM.

## Why

Working across several machines means losing track of what was pushed where: you
pull `master` on the laptop only to find the latest work is still unpushed on the
desktop. Worse, some work is on no server at all - a project you started, committed
to, and never pushed.

That is a **visibility + reliable-push** problem, not a merge problem. `gms` makes
the state of every repo visible and brings the clean ones in line with origin
automatically. It never auto-resolves conflicts; it describes them so an LLM (or
you) can fix them.

The reason it discovers repos rather than tracking a list: a list you maintain by
hand is a list you forget to add to, and the repos missing from it are exactly the
ones nobody is watching. A tool for finding work that exists on no server cannot
depend on you remembering to enrol the place it went missing.

The two states worth acting on are the ones `gms` cannot fix for you:

- **`no-upstream` with a remote configured** - commits exist, `git push -u` was
  never run. One command from being backed up.
- **`no-upstream` with no remote at all** - the project exists only on this machine.
  It needs a repo created before anything can back it up.

Everything else, `gms` handles: behind gets fast-forwarded, ahead gets pushed.

## Install

```sh
# simplest, if you have Go (installs the binary as `git-multi-sync`):
go install github.com/KakkoiDev/git-multi-sync@latest

# or the installer, which builds and installs it as `gms`:
curl -fsSL https://raw.githubusercontent.com/KakkoiDev/git-multi-sync/main/install.sh | bash
./install.sh                       # from a clone -> ~/.local/bin
BINDIR=/usr/local/bin ./install.sh

# or by hand:
go build -o gms . && cp gms ~/bin/
```

The binary works under either name. Installed as `git-multi-sync`, `git`
discovers it as a subcommand: `git multi-sync sync`.

### Short name (`gms`)

`go install` names the binary `git-multi-sync` (the module's last path element),
and it lands in `go env GOPATH`/bin (usually `~/go/bin`) - make sure that dir is
on your `PATH`. For the shorter `gms`, alias it in your shell rc:

```sh
alias gms='git-multi-sync'        # ~/.zshrc, ~/.bashrc, ...
```

or symlink it, so it also works in scripts and non-interactive shells:

```sh
ln -s "$(go env GOPATH)/bin/git-multi-sync" "$(go env GOPATH)/bin/gms"
```

The `install.sh` route already installs the binary as `gms`, so no alias needed.

## Configure

**There is nothing to register.** `gms` finds the git repos in your home
directory by itself, so a repo you clone today is synced on the next run.

```sh
gms init                 # optional: writes commented config templates
gms list                 # what gms will sync
gms list --skipped       # what it left out, and why
gms ignore ~/scratch     # leave a tree alone
gms ignore               # list the patterns, and how many repos each matches
gms unignore ~/scratch   # undo that
gms add ~/odd/place      # force one in, overriding any ignore
gms remove ~/odd/place   # undo that
```

`add` and `remove` resolve any path inside a repo to its root, so running them
from a subdirectory affects the whole repo and never creates duplicates.

### The rule

**Clean repo → `gms` synced it. Dirty repo → yours to clean.** That is the whole
model. There is no per-repo or per-owner policy to learn.

### Recommended first setup

Discovery finds every repo in your home directory, which is more than you want to
watch. Look before you schedule anything:

```sh
gms list                      # everything in scope
gms status --no-fetch         # what state it is all in
```

Then ignore what is not a project you are backing up. Two categories are worth it
on most machines:

```sh
gms ignore ~/scratch          # throwaway trees: benchmarks, experiments, notes
gms ignore ~/.tmux-worktree   # linked worktrees, if you manage their branches yourself
```

**Ignoring worktrees has a cost, so decide deliberately.** Each checkout syncs only
its own branch, so pushing a repo's `master` does not cover a worktree sitting on
`feature/x`. Ignore them and commits on those branches are yours to push. Keep them
and every worktree becomes a row in every report - on a machine with 47 of them,
that is the whole report.

Repos you merely cloned to read need no rule: `gms` only pushes a branch that is
**clean and ahead**, and you are never ahead on a repo you do not commit to.

### The three files

All plain text, one entry per line, `#` for comments, blank lines ignored. Every
one is optional - `gms` works with none of them. Edits preserve your comments.

| file | holds |
|------|-------|
| `ignore` | globs for repos to leave alone |
| `repos` | **pins**: repos always synced, overriding `ignore` |
| `never-push` | branch names never pushed (behind branches still get pulled) |

Precedence is one line, and pins come last:

```
pin (repos / gms add)  >  ignore  >  whatever was discovered
```

That is why `ignore` has no `!` re-include syntax: putting one path back is a
command, not a config edit. A leading `!` is reported as an error rather than
being silently treated as a literal.

Patterns are **globs, not regex**: `*` stays inside one path segment, `**` crosses
segments, and `.` is an ordinary character. They are matched against both `~/short`
and absolute paths, so a config file shared between machines works on both.

### Sharing config between machines

Point `GMS_CONFIG_DIR` at a directory inside a repo `gms` already syncs, and the
lists travel with it. No server, no daemon, no hand-copying.

```sh
# in ~/.zshrc, itself inside ~/dotfiles
export GMS_CONFIG_DIR="$HOME/dotfiles/gms"
```

## Use

```sh
gms status               # fetch and report the state of every repo
gms sync                 # ff-pull behind repos, push ahead repos, report the rest
gms doctor               # scan for repos and report what was found, and why
```

### Filling a new machine

`clone` is the one part that talks to GitHub. It needs the [GitHub CLI](https://cli.github.com)
authenticated; nothing else does.

```sh
gms list --missing            # on your account, not on this machine
gms clone kanji-roots         # by name, across all your owners
gms clone meetsmore/meetsone  # or fully qualified, when a name is ambiguous
gms clone all                 # everything, after confirming the count and size
gms clone all --yes           # skip the prompt
```

New clones land wherever most of your repos already are - no configuration, and no
new layout imposed. Override with `--into`.

"Already here" is decided by the repo's **remote identity**, not its directory
name, so a repo checked out under a different name is recognised rather than cloned
twice. Archived repos are excluded from `all` unless you pass `--include-archived`.

The account listing is cached and never expires. A refresh that fails while a cache
exists is a warning with the cache's age, so `gms list` keeps working offline. Only
`--refresh` and `clone` ever hit the network.

### `doctor`: what is on this machine

`doctor` walks your home directory for git repos and reports what it found without
touching anything. It stops at each repo root instead of descending into it, so a
checkout holding hundreds of thousands of files costs one directory read.

```sh
gms doctor                    # tally: repos found, worktrees, owners
gms doctor --all              # one line per repo, with its push target
gms doctor --depth 6          # look deeper than the default 4
gms doctor ~/work             # scan somewhere other than $HOME
```

Repos are keyed by **where `git push` would actually send commits**, not by
`origin`. In a fork checkout those differ: `origin` is often the upstream project
while the branch pushes to your own fork.

The scan is bounded by depth (default 4) and by a directory budget, and it reports
what it skipped for either reason - a repo that was not looked for should never be
confused with a repo that is in sync.

### What `sync` does, per repo

| Repo state              | Action                                              |
|-------------------------|-----------------------------------------------------|
| behind (clean)          | `git pull --ff-only`                                |
| ahead (clean)           | `git push`                                          |
| up to date              | nothing                                             |
| diverged                | report only (described for resolution)              |
| dirty (uncommitted)     | report only, left untouched                         |
| no upstream, has remote | report only: names the `git push -u` that fixes it  |
| no upstream, no remote  | report only: this project exists on no server       |
| detached                | report only                                         |

A **dirty** repo blocks both pull and push, so one left permanently dirty quietly
stops updating. That is why `gms` keeps reporting it rather than letting you silence
it: an ignore is keyed on a path, outlives the reason, and hides the pull too.

### Resolve conflicts with an LLM

When stdout is **piped**, the human summary goes to **stderr** (still visible in
your terminal) and **stdout carries only the resolution prompt** for diverged
repos: absolute paths, the steps to run, and the likely conflict files. When
nothing has diverged, stdout is empty.

Give the LLM an explicit instruction and allow it tools - the piped block is its
context. In Claude Code's print mode, `-p` only acts when the relevant tools are
allowed:

```sh
gms sync | claude -p \
  "Each section below is a repo that diverged from origin. For each: cd into the
   absolute path, run git pull, resolve the conflicts, commit, and push." \
  --allowedTools "Bash,Edit,Read"
```

> Pass a prompt argument as above. The bare `gms sync | claude -p` (no prompt)
> **errors when nothing has diverged** - `claude -p` rejects empty stdin with
> `Input must be provided...`. To skip the LLM entirely when there is nothing to
> resolve, guard on the captured output instead:
>
> ```sh
> out=$(gms sync) && [ -n "$out" ] && printf '%s\n' "$out" \
>   | claude -p "Resolve each diverged repo below: pull, fix, commit, push." \
>     --allowedTools "Bash,Edit,Read"
> ```
>
> (`gms sync`'s summary still prints to stderr; only the resolution block is
> captured into `$out`.)

The same pattern works with any assistant CLI that reads a prompt on stdin (just
swap `claude -p ...` for its equivalent). After it finishes, re-run `gms sync` to
confirm every repo is clean.

Force the format with `--format human|llm|json` (default `auto` picks `human` on
a terminal, `llm` when piped). Other flags: `--no-fetch`, `--jobs N`, `--depth N`.

## Safety model

`gms` is safe by default. It will never:

1. `--force` push (any form),
2. act on a dirty worktree,
3. merge, rebase, or auto-resolve a conflict,
4. push a diverged branch,
5. push a branch listed in `never-push`.

Pulls are always `--ff-only`. Diverged repos are described, never modified. All
git calls run with `GIT_TERMINAL_PROMPT=0` so a missing credential fails fast
instead of hanging the run. A failed fetch (e.g. offline) does not abort the run;
the repo is flagged "fetch failed (stale?)" and classified against last-known
remote refs.

Whenever `gms` declines to push, it says which repo and why. A repo that quietly
stopped pushing is indistinguishable from one that is in sync, which is the exact
failure this tool exists to catch.

Discovering repos does not widen what gets pushed. A push only happens when a
branch is **clean and ahead**, so a clone you only read from is never a candidate -
you would have to have committed to it. If you did, and you have no write access,
the push fails loudly and nothing is damaged.

## Make it a habit

The cure for stranded work is pushing from every machine often. Run `gms sync`
on a schedule or shell hook so origin stays canonical:

```sh
# crontab: sync every 30 minutes
*/30 * * * * /Users/you/bin/gms sync >/dev/null 2>&1
```

Two things to know before scheduling it, since discovery makes the set much larger
than a hand-kept list:

- **Run `gms list` first** and see what is in scope. Add `ignore` patterns for
  scratch directories before a scheduled job starts reporting on them.
- **A scheduled run will push any clean, ahead branch it finds**, including in work
  repos, which fires their CI unattended. If that matters, put the branch in
  `never-push` or the repo in `ignore`.

## Agent skill

A portable [agentskills.io](https://agentskills.io) skill ships in
[`skills/git-multi-sync/`](skills/git-multi-sync/SKILL.md). It teaches an agent to
run `gms status`/`sync`, report what changed, and resolve diverged repos. The
`SKILL.md` is tool-agnostic - it works with any agent that loads that format.

For Claude Code, symlink it into your skills dir:

```sh
ln -s "$PWD/skills/git-multi-sync" ~/.claude/skills/git-multi-sync
```

For other agents (e.g. pi), point their skill loader at the same directory.

## Develop

```sh
go test ./...    # unit table tests for classify() + git integration harness
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
