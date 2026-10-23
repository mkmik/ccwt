# Removing worktrees

Every removal — `ccwt remove`, `ccwt done`, `ccwt gc`, and `r` in the tui — first writes a
line about the worktree to the [worklog](../tui/worklog.md), since the branch and the
directory are about to go.

## `ccwt remove <name>`

Remove the worktree at `.claude/worktrees/<name>` and delete its branch. `.` means the
worktree you're currently in; removing the one you're in cds you to the repo root, like
`ccwt ..`.

The branch is deleted only if merged: an unmerged branch refuses the whole removal,
worktree included, so nothing is stranded, and so does a worktree with uncommitted changes,
and so does one with an agent working in it (the `✳` of `ccwt list`). Pass `-D` to remove
anyway (unmerged branch deleted, uncommitted changes thrown away, working agent
interrupted), or `--keep-branch` to remove only the worktree.

Under [Herdr](../herdr/index.md#closing-workspaces-on-removal) the worktree's workspace is
closed too, once those checks pass — your own one last, after the removal, with focus handed
to the repo's own workspace first if yours is the workspace on screen — and a terminal is
asked before that happens (`-y`/`--yes` skips the question).

## `ccwt done`

Finish with the worktree you're in: `ccwt remove . -y` — the removal, and then, under
[Herdr](../herdr/index.md#closing-workspaces-on-removal), closing the workspace you're sitting
in, without asking (closing it is what "done" means). Same checks and same flags (`-D`,
`--keep-branch`); a refusal leaves the workspace open. Outside Herdr it's just
`ccwt remove .`.

## `ccwt gc`

Remove every worktree that's finished with: branch already merged (the `✓` of `ccwt list`),
nothing uncommitted in it (no `*` or `±`), no agent working in it (no `✳`) and no agent
session running in it (a `no` in the `AGENT` column). Prints the list it found and asks
before touching anything — `-y`/`--yes` skips the question. Each removal is exactly what
`ccwt remove <name>` does, branch included. The worktree you're standing in is never
removed — it says so on stderr and leaves it to `ccwt remove .`.

`G` in the tui's `☰` menu runs the same thing on the repo in view.
