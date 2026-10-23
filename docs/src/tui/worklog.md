# The worklog

Removing a worktree also removes the only record of what it was for: the branch is deleted,
the directory is gone, and the agent files its transcript under a path that no longer
exists. So `ccwt remove` writes a line about it first — when it went, what it was called,
the `TOPIC` the list showed, and the url of its merge request or pull request, so you can
still ask [`ccwt mr`](../worktrees/mr.md) where the work has been promoted to after it
merged — into `$XDG_STATE_HOME/ccwt/tasks.db`, next to the queued prompts, and every removal
goes in: `ccwt remove`, `ccwt done`, `ccwt gc`, and `r` in the tui.

## In the tui

`l` shows that log as a window over the list, newest first; `esc` (or `l` again) closes it.
Its rows select like the list's — the arrows (or `j`/`k`) walk them, a click lands on one —
and `↵` opens the selected removal's page.

```
    NAME                  BRANCH                    AGE  AGENT  TOPIC
  ✓ calm-baking-otter     worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
    ┌─ worklog ──────────────────────────────────────────────────────────────────────┐
    │ AGE  NAME                    TOPIC                                             │
    │ 2h   kind-munching-melody    ✳ Kept a log of removed worktrees, with a pane in… │
    │ 1d   dreamy-foraging-hickey  ⎇ Quote a queued prompt on its way to th… (#79)   │
    └────────────────────────────────────────────────────────────────────────────────┘
```

## A removal's page

That page is everything left to know about one removed worktree: what the log recorded, and
then the whole of the last Claude Code session that ran in it — the prompts, what the agent
said back, and the tools it reached for, which survive the removal because transcripts are
filed under your home directory rather than in the tree they were about. It scrolls:
the arrows (or `j`/`k`) by a line, `space`/`b` by a screenful, `g`/`G` to either end, `esc`
back to the log.

```
    ┌─ dreamy-foraging-hickey ───────────────────────────────────────────────────────┐
    │ NAME     dreamy-foraging-hickey                                                │
    │ PROJECT  /Users/you/src/ccwt                                                   │
    │ REMOVED  1d ago, 2026-08-12 09:31                                              │
    │ PATH     /Users/you/src/ccwt/.claude/worktrees/dreamy-foraging-hickey          │
    │ TOPIC    ⎇ Quote a queued prompt on its way to the pane (#79)                  │
    │ REVIEW   https://github.com/you/ccwt/pull/79                                   │
    │ SESSION  ~/.claude/projects/-Users-you-src-ccwt--claude-worktrees-dreamy-…     │
    │                                                                                │
    │ › the prompt reaches the pane unquoted, fix it                                 │
    │                                                                                │
    │   The prompt is pasted into a shell line, so it needs shell quoting.           │
    │                                                                                │
    │   ⚒ Read: tui.go                                                               │
    └────────────────────────────────────────────────────────────────────────────────┘
```

Tool *results* are left out — they are the bulk of the megabytes, and what a session did
reads perfectly well from the prompts, the prose and the calls. The `SESSION` line says
where the raw transcript is if you want the rest of it.

## On the command line

`ccwt worklog` prints the same table on the command line, `-n` (default 20) saying how many
lines of it. `-g` spans every known project instead of this repo's, and adds a
`PROJECT` column, since a worktree name on its own doesn't say which repo it was in; the
pane does the same when the tui is running under `-g`. Once any of the removals had a
review, a `REVIEW` column at the end gives its url whole.
