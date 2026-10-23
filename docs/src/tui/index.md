# The tui

`ccwt tui` — or just `ccwt`, which is what a bare invocation runs — shows the
[`ccwt list`](../worktrees/listing.md) table full-screen and keeps it up to date, repainting
in place so it doesn't flicker, with a status bar along the bottom:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on the dashboard; it's built and…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor

 ☰  q:quit  p:pull  g:git  /:search  l:log  a:agents  x:new  space:open  n:queue  m:vcs  d:details  r:remove │ main ↑1     v1.2.3
```

The tui is meant to be parked in a pane — e.g. the main pane of a
[Herdr](../herdr/index.md) workspace — as a live view of what's running where.

## The bar

`q` (or Ctrl-C) quits, `p` runs `git pull` and reports the result in the bar — though while the branch is
behind what the background fetch already brought in, it only fast-forwards to that, which
takes no network; once caught up, `p` again fetches for real. The rest of
the bar is the branch you launched it from and how far it has drifted from its upstream
(`↑` ahead, `↓` behind), and the far corner is the version of the `ccwt` binary drawing it —
dropped on a terminal too narrow to hold both it and the keys. Those counts are kept honest by a background `git fetch` of
`origin/main` alone; `--fetch` (default `1m`) sets how often, or `0` never. `--interval`
(default `2s`) sets the refresh rate.

The arrow keys (or `j`/`k`) select a worktree, and the bar grows the actions that apply to
it: `r` removes it, refusing an unmerged branch, uncommitted changes or a working agent,
exactly like [`ccwt remove`](../worktrees/removing.md). Clicking a row selects it. Three more
keys — `x`, `c` and `space` — appear only under
[Herdr](../herdr/index.md#opening-workspaces-from-the-tui).

`a` narrows the list to the worktrees an agent is running in — one herdr has, idle or not,
or a Claude Code session — and `a` again brings the rest back.

## Selecting text

Dragging selects text and copies it. A terminal stops selecting for itself once an
application asks to be told about clicks, so the tui does the selecting: press, drag, let go,
and what the drag covered — from where it started to where it ended, whole lines in between,
as text reads — goes on the clipboard (`pbcopy`, `wl-copy` or `xclip`, and OSC 52 where
there's none of those, so it works over `ssh` too). The selection stays inverted until the
next keystroke, and a press that never moved is still an ordinary click.

## The `☰` menu

The `☰` in the corner is that same list of actions as a menu: click it and they drop out of
the bar, one per line, to be clicked instead of typed (the arrows and `↵` work too, `esc`
closes it, and clicking the `☰` again shuts it). A menu entry is the key it names — picking
one does exactly what typing it does — so the menu and the bar can't drift apart. Two entries
are only there: `G` runs [`ccwt gc`](../worktrees/removing.md#ccwt-gc) on the repo in view,
collecting every worktree that's merged, clean and idle in one go — rare enough that the bar
has no room for it, and picking it is the confirmation — and `P` swaps the worktrees for
[what is running in them](running.md).

## History

`g` shows the history in a window over the list: whatever your own `git ll` alias prints,
run in the worktree the selected row is in — or in the repo itself when the row isn't in
one. It's the same kind of page the worklog opens, and scrolls the same way: the arrows (or
`j`/`k`) by a line, `space`/`b` by a screenful, `g`/`G` to either end, `esc` back to the
list. A git with no `ll` alias of its own has nothing to print, and says so in the bar.

## Opening the review

`m` opens the worktree's review in your browser — the branch is here, the review is over
there. Which one that is depends on where `origin` points: a GitHub remote is a pull request
and [`gh`](https://cli.github.com) finds it, a GitLab one is a merge request and
[`glab`](https://gitlab.com/gitlab-org/cli) does. Either cli already knows which host the
remote is and which token to ask it with; ccwt only decides which of the two to run, and
hands the url it prints to `open` (macOS) or `xdg-open`. Without the cli installed, or on a
branch with no review yet, the bar just says there's none to open.

`github.com`, `gitlab.com`, and any host with `gitlab` in its name are recognised as they
are. A self-hosted instance that doesn't keep the word — `code.example.com` — needs a line
in [`~/.config/ccwt/config.toml`](../configuration.md#forges), since there's nothing in the
hostname to go on:

```toml
[forges]
"code.example.com" = "glab"
```

To see where the review stands without leaving the terminal, there's
[`ccwt mr`](../worktrees/mr.md).

## Details

`d` opens the selected worktree's details as a window over the list: every column's value in
full, one per line and wrapped to the width, instead of the row the table had to cut down to
fit. It's modal — `esc` closes it, and while it's up the other keys do nothing (bar `e` on a
[queued prompt](queue.md#editing-a-prompt)) and the list behind it holds still. Every column the table
knows about is in there, including any the config leaves out of the list.

```
    NAME                  BRANCH                    AGE  AGENT  TOPIC
  ✓ calm-baking-otter     worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
    ┌─ dreamy-foraging-hickey ───────────────────────────────────────────────────────┐
    │ NAME    dreamy-foraging-hickey                                                 │
    │ BRANCH  worktree-dreamy-foraging-hickey                                        │
    │ AGE     2h                                                                     │
    │ AGENT   yes                                                                    │
    │ TOPIC   ✳ Goal was a widget on the dashboard; it's built and wired up, and the │
    │         numbers on it are still the mocked ones                                │
    └────────────────────────────────────────────────────────────────────────────────┘
```
