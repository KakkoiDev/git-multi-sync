---
name: git-multi-sync
description: Sync many git repositories across machines and resolve cross-repo merge conflicts with the `gms` CLI. Use when the user wants to pull and push all their tracked repos at once, see which repos are dirty/ahead/behind/diverged, get back in sync after working on another machine, or fix conflicts in repos that diverged. Triggers include "sync my repos", "push everything", "are my repos in sync", "pull all my projects", "resolve conflicts across my repos", and "gms".
user-invocable: true
---

# git-multi-sync (gms)

`gms` syncs a list of git repos in one shot: it fast-forward-pulls repos that are
behind, pushes repos that are ahead, and reports the rest. It never merges,
rebases, force-pushes, or touches a dirty worktree. Diverged repos are described,
not auto-resolved - resolving them is this skill's job.

This skill is tool-agnostic: use whatever shell and file-editing tools your agent
provides.

## Prerequisites

- `gms` (or `git-multi-sync`) on PATH. Check with `gms help`. If missing, install
  from https://github.com/KakkoiDev/git-multi-sync.
- A config at `~/.git-multi-sync/repos` (one repo path per line). If empty, add
  repos with `gms add <path>`.

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

## Config management

```sh
gms add [path]      # track a repo (defaults to cwd; resolves to the repo root)
gms remove [path]   # stop tracking
gms list            # show tracked repos
```

## One-shot through an assistant CLI

Pipe the diverged block straight into an assistant in print mode. Pass a prompt
argument so it does not error on empty input:

```sh
gms sync | claude -p "Resolve each diverged repo below: cd in, pull, fix, commit, push." --allowedTools "Bash,Edit,Read"
```

Swap `claude -p ...` for any assistant CLI that reads a prompt on stdin.
