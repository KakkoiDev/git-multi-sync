---
name: git-multi-sync
description: Sync every git repository on the machine and resolve cross-repo merge conflicts with the `gms` CLI. Use when the user wants to pull and push all their repos at once, see which repos are dirty/ahead/behind/diverged, find work that exists on no server, get back in sync after working on another machine, fix conflicts in repos that diverged, or clone repos from their GitHub account onto a new machine. Triggers include "sync my repos", "push everything", "are my repos in sync", "pull all my projects", "any unpushed work", "resolve conflicts across my repos", "clone my repos", and "gms".
user-invocable: true
---

# git-multi-sync (gms)

`gms` syncs every git repo on the machine in one shot: it fast-forward-pulls repos
that are behind, pushes repos that are ahead, and reports the rest. It never merges,
rebases, force-pushes, or touches a dirty worktree. Diverged repos are described,
not auto-resolved - resolving them is this skill's job.

**There is no list to maintain.** `gms` finds the repos in the user's home directory
itself, so a repo cloned today is synced on the next run. The model is one rule:

> Clean repo -> `gms` synced it. Dirty repo -> the user's to clean.

This skill is tool-agnostic: use whatever shell and file-editing tools your agent
provides.

## Prerequisites

- `gms` (or `git-multi-sync`) on PATH. Check with `gms help`. If missing, install
  from https://github.com/KakkoiDev/git-multi-sync.
- No configuration is required. `gms sync` and `gms status` work immediately and
  never need the network beyond `git fetch`.
- Only `gms clone` and `gms list --refresh` need the GitHub CLI authenticated.

## Workflow

1. **See the state** (read-only apart from a fetch):
   ```sh
   gms status
   ```
   Columns are repo, branch, state, detail. States: `clean`, `behind`, `ahead`,
   `DIVERGED`, `DIRTY`, `no-upstream`, `detached`.

2. **Sync the safe repos**:
   ```sh
   gms sync
   ```
   This fast-forward-pulls behind repos and pushes ahead repos automatically.
   Report the per-repo actions (the `-> ff-pulled / pushed / ...` lines).

3. **Resolve diverged repos.** When its stdout is piped, `gms sync` writes a
   resolution block listing each diverged repo by absolute path with its likely
   conflict files. Capture it so you do nothing when there is nothing to resolve
   (an empty block means no divergence):
   ```sh
   out=$(gms sync) && [ -n "$out" ] && printf '%s\n' "$out"
   ```
   For each diverged repo in the block:
   - `cd` into the absolute path,
   - run `git pull` (it will conflict),
   - resolve the conflicts in the listed files,
   - `git add -A && git commit`,
   - `git push`.

4. **Confirm**: re-run `gms status`; every repo should read `clean`.

## Rules

- Never `git push --force`. Never rebase or auto-merge to paper over a divergence
  unless asked - do a normal merge and let the user review.
- A `DIRTY` repo has uncommitted changes: do not pull or push it. Tell the user to
  commit or stash, then re-run.
- A `no-upstream` or `detached` repo needs a branch with an upstream before it can
  sync.
- Resolve conflicts by understanding both sides, never by blindly taking one. If a
  conflict is non-obvious or risks losing work, stop and ask.

## Finding stranded work

The most valuable thing `gms` reports is a repo holding work that exists on no
server. Two states mean that, and neither can be fixed by syncing:

- `DIRTY` - uncommitted changes. Also **blocks both pull and push**, so a
  permanently-dirty repo silently stops updating. Worth telling the user about.
- `no-upstream` - commits on a branch that tracks nothing.

```sh
gms status --format json    # machine-readable; filter on state
```

Do not suggest ignoring a repo to quiet these. An ignore is keyed on a path,
outlives the reason, and hides the pull as well as the report.

## Scope management

Nothing needs registering. These exist for the exceptions.

```sh
gms list                 # what gms will sync
gms list --skipped       # what it left out, and why
gms ignore <path|glob>   # leave a tree alone (a directory becomes <dir>/**)
gms ignore               # list patterns and how many repos each matches
gms unignore <pattern>   # undo
gms add [path]           # pin: force one in, overriding any ignore
gms remove [path]        # unpin
gms doctor               # what was scanned, what was found, what was skipped
```

Precedence is one line: `pin > ignore > whatever was discovered`. So to
re-include one repo out of an ignored tree, **pin it** - there is no `!` syntax,
and a `!` line is reported as an error.

Patterns are globs, not regex: `*` stays inside a path segment, `**` crosses
segments, `.` is literal.

Two limits worth knowing before concluding a repo is fine:

- The scan stops at **depth 4** below home. `gms doctor` reports how many
  directories it did not descend into; `--depth N` looks further.
- Worktrees are enumerated from each repo, so they are found at any depth.

## Filling a new machine

Needs the GitHub CLI authenticated.

```sh
gms list --missing            # on the account, not on this machine
gms clone <name>              # by name, across owners
gms clone <owner>/<name>      # when a bare name is ambiguous
gms clone all                 # confirms the count and size first
```

`clone all` refuses on a non-TTY without `--yes`, so never assume it ran in a
script. "Already here" is decided by remote identity, not directory name.

## Sharing config between machines

`GMS_CONFIG_DIR` points `gms` at a config directory inside a repo it already
syncs, so the lists travel with it.

```sh
export GMS_CONFIG_DIR="$HOME/dotfiles/gms"
```

## One-shot through an assistant CLI

Pipe the diverged block straight into an assistant in print mode. Pass a prompt
argument so it does not error on empty input:

```sh
gms sync | claude -p "Resolve each diverged repo below: cd in, pull, fix, commit, push." --allowedTools "Bash,Edit,Read"
```

Swap `claude -p ...` for any assistant CLI that reads a prompt on stdin.
