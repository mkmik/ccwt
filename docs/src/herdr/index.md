# Herdr integration

[Herdr](https://herdr.dev) manages terminal workspaces for coding agents. `ccwt` doesn't need
it — everything in the chapters before this one works on its own. When Herdr *is* running,
these extras appear, and this part of the book is the whole of it.

## The `✳` marker

In `ccwt list` and the tui, `✳` in the [leading glyphs](../worktrees/listing.md#the-leading-glyphs)
means Herdr says an agent is working in that worktree right now. It outranks `*`, `±`, `☐` and
`✓`, and it's the case git can't see at all: a branch made a minute ago with nothing committed
to it is merged and clean. It comes from Herdr rather than from the agent's own files, so it
holds for whatever agent is running there — unlike the `AGENT` column, which knows Claude Code
alone. `ccwt remove`, `ccwt done` and `ccwt gc` all refuse a worktree marked this way
(`ccwt remove -D` interrupts the agent anyway).

## Agent dots

In the tui, each worktree's row leads with Herdr's own mark for the agents in it, ahead of the
other two glyphs — the dot [`ccwt ws`](ws.md#agent-marks) gives a tab, by the same rules:
red blocked, yellow working, green done, grey idle, and a `·` where Herdr has no agent.
Several agents in one worktree show whichever most wants you, and green is a turn that has
ended since anyone last looked at its tab, which going there — `space` — clears. An agent is in
the worktree its own directory or its pane's is in, as for `✳`.

```
      NAME                    BRANCH                    AGE  AGENT  TOPIC
●   ✳ dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on the dashboard; it's built and…
○   ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
```

`AGENT` goes on saying whether Claude Code is running there. `ccwt list` has no dots: it draws
the table once, with nothing to remember what you have read, and would call every finished
turn unread.

## Opening workspaces from the tui

`space` opens the selected worktree as its own Herdr workspace (via `herdr worktree open`),
and a double-click on its row does the same. `x` creates a worktree and opens it without
leaving the list, and `c` does that and starts [`ccwt ws`](ws.md) in it — the workspace's own
tui, which is where its agents are seeded from. These keys only exist when the tui is itself
running in a Herdr pane — elsewhere there's no session to open a workspace in, so they're
dropped from the bar and do nothing.

## Workspace naming

A workspace opened on a *new* worktree is named after the worktree: Herdr would otherwise list
it under its repo by branch, prefix and all. Reopening one leaves its name alone, so a
workspace you've renamed yourself stays renamed. And the name is what `NAME` shows, in
`ccwt list` and the tui alike, when they run in a Herdr pane: a worktree open as a workspace
goes by the workspace's name, so the list reads like Herdr's sidebar. Piped output keeps the
worktree's own name, which is the one `ccwt remove` takes.

## Closing workspaces on removal

Once its checks pass, [`ccwt remove`](../worktrees/removing.md) closes the workspace open on
the worktree, ending the agent living in it — including your own, which is closed last of
all, after the worktree is gone and the shell has been cd'd out. Because a workspace
vanishing is a bigger surprise than a directory, a terminal is asked first: `remove <name>
and close its herdr workspace? [y/N]` on stderr, answered "no" by default. `-y`/`--yes` skips
the question, and so does a `ccwt remove` whose stderr isn't a terminal — a script or the tui
gets on with it. `ccwt done` is `ccwt remove . -y`. Closing the workspace you're looking at
would leave Herdr focused on whatever it had open before — some unrelated pane — so when
yours is the one on screen, the repo's own workspace, the one open on the root checkout, is
focused first: you land back in the repo the worktree came from. Switch to another workspace
while the removal runs and nothing moves — closing a workspace nobody is looking at takes no
focus with it, and where you switched to is where you meant to be.

## The plugin

The plugin in [`herdr-plugin/`](https://github.com/mkmik/ccwt/tree/main/herdr-plugin) adds a
**New ccwt worktree** action to Herdr — the same thing the tui's `x` does — and **Open a ccwt
project**, which is [`ccwt open`](../reference/commands.md#ccwt-open): `o` in `ccwt -g`, from
wherever you are.
