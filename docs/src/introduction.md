# Introduction

`ccwt` is a small command-line helper for managing coding-agent git worktrees.

An agent works best in a worktree of its own, one per task, under
`<repo-root>/.claude/worktrees/<name>`. `ccwt` lets you create, list, jump between, and
tear down those worktrees from your own shell, with a table saying what each one is about
and whether something is running in it.

It started out as a [Claude Code](https://claude.com/claude-code) helper, and Claude Code
is still hardcoded in a handful of places: the `.claude/worktrees/` layout and the
adjective-verb-noun naming scheme (which is what keeps `ccwt` and `claude --worktree`
interoperable), the `AGENT` column, which looks for running Claude Code processes, and
`TOPIC`, the freshness sort and the worklog's session page, which all read Claude Code
transcripts. The rest — the worktrees, the table, the tui, the queue, the `✳` marker —
doesn't care which agent you run, and [`task_command`](configuration.md#task_command) is
where you say which one that is.

## How this book is laid out

- [Getting started](getting-started.md) installs `ccwt` and hooks it into your shell.
- **Worktrees** is the commands you type: making, listing and removing worktrees, and
  asking where their reviews stand.
- **The tui** is the full-screen view a bare `ccwt` opens, which keeps that list up to date
  and queues the work that comes next.
- **Herdr** is what changes when [Herdr](https://herdr.dev) is running. None of it is needed
  for the rest.
- **Setting up** covers the config file and running Claude Code through `ccwt`.
- [Commands](reference/commands.md) lists every command and flag in one place.
