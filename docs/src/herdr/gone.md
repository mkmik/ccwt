# When `ccwt ws` has gone

The tab `c` starts [`ccwt ws`](ws.md) in is named `ws`, and the name outlives the tui: quit
it, or restart Herdr — which brings the tabs back but not what was running in them — and the
tab is a bare shell under the same name. The tui names each of its worktrees' workspaces in
that state, in a yellow box in its bottom-right corner:

```
                                ┌─ ccwt ws not running ↻ ─┐
                                │ audit restore           │
                                └─────────────────────────┘
 ☰  q:quit  p:pull  g:git  /:search  l:log  a:agents  x:new  c:new+ws  n:queue │ main
```

It looks every ten seconds, and with [`herdr_events`](../configuration.md#herdr_events) on it
also listens to Herdr's events and looks again whenever a tab or a workspace is made, renamed
or closed — the ten-second look stays either way, since Herdr says nothing when a program
exits back to its shell. It covers only the workspaces open on the worktrees it lists — the
ones Herdr shows under the repo, or under any known project with `-g` — and leaves another
repo's to that repo's tui. Only the tui does this, not `ccwt ws`, and only when it is running
in a Herdr pane.

With [`-g`](../projects.md) the box is `ccwt tui not running` instead, and names the
projects' own workspaces — the ones open on each configured repo's root checkout — that have
no ccwt in any pane of any tab: the repo tuis that are gone.

A click on the `↻` in its title starts them again, once you have read the plan it shows and
said `y` to it. Each workspace gets its tui back in the tab it belongs in — `prj` for a
project's own workspace under `-g`, running `ccwt`; `ws` for a worktree's, running `ccwt ws`.
A workspace with no such tab gets one, made first in its bar; one whose tab is at its shell
has the tui typed there; one whose tab is busy with something else is left to it, and the plan
says so.
