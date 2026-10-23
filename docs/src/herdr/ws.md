# A workspace's own tui: `ccwt ws`

The [tui](../tui/index.md) is the repo's view: its worktrees, whatever workspace each is open
in. `ccwt ws` is a workspace's, in two sections: where the workspace's review stands, and
under it the tabs working towards it.

```
  MR              STATUS          PIPELINE            TITLE
  acme/…/api!42   needs approval  failed: unit, lint  Wire the widget to real numbers

  TAB  AGENT  DIR                     TITLE
* 1    ·      dreamy-foraging-hickey  zsh
  2    ●      dreamy-foraging-hickey  Wiring the widget to real numbers
  3    ○      dreamy-foraging-hickey  Waiting on your review

 ☰  q:quit  /:search  n:agent  space:go  g:git │ started
```

It refuses to run outside Herdr. `--interval` (default `2s`) sets the refresh rate.

## The review

The first section is what [`ccwt mr`](../worktrees/mr.md) prints for the branch the workspace
is working on — can it go in, and did its pipeline pass — which is the question the whole
workspace exists to get a yes to. A fresh worktree hasn't got one yet, and says so in a line
rather than in a row of column names with nothing under it:

```
  no merge request yet
```

The lookup is a handful of GitLab or GitHub round trips, so it runs in the background and is
asked again at most once a minute: the table you're reading never waits on the network, and
the row appears under the heading a moment after the review does.

## The tabs

The second section is the workspace's tabs — the tab's label, whether Herdr sees an agent
working in it, the directory it sits in, and what its terminal calls itself, which for an
agent is its own one-line account of what it is doing — and `space` (or `↵`, or a
double-click) goes to the selected tab. A `*` leads the tab the tui itself is in. The rows
come in the order the tabs sit along the tab bar, so dragging one about in Herdr moves its
row with it. They're the only rows the selection walks: the review above them is something to
read, and it stays put while they scroll.

## Agent marks

The `AGENT` mark is Herdr's own: a circle, and the state is the colour of it —
red blocked, yellow working, green done, grey idle, and a `·` for a tab Herdr sees no agent
in at all. Herdr keeps that state per agent rather than per tab, so a tab split between
several shows whichever of them most wants you: an approval waiting on an answer first, then
a turn that has ended with something to read, then work still going on. Herdr draws the three
busy states as the same circle too, so a workspace reads the same in the tab bar and in the
table below it — in the same colours, too: the dots are asked for by palette number, and
Herdr's theme is what answers.

Green is the one mark the table keeps itself. Herdr's `done` is the server's seen state, and
one look spends it for good: sit in a tab until its agent finishes, and every turn it ends
after that is plain `idle`, however far away you were for it. Herdr's own tui doesn't take
that answer either — it keeps the badge per client, each tracking what it has been shown. So
this one goes on when an agent has settled and has moved since the last time anyone was
looking at its tab, and comes off when you go: `space`, or Herdr saying that tab is the one
you are looking at.

What it reads that off is `state_change_seq`, Herdr's own counter for when an agent last
changed state, rather than off watching for the change itself — which means a `ccwt ws` that
has only just come up still says which tabs have something waiting in them. A conductor that
has to witness the turn end to colour it says nothing about the ones that ended first, and
`ccwt ws` restarts itself on every ccwt upgrade. The cost is a third round trip to Herdr per
round, next to the tab and pane lists the table is built from.

## Seeding agents

`n` opens the same box the [queue](../tui/queue.md) types into, and what goes in it is a seed
prompt: `↵` opens a tab of the workspace in the workspace's own worktree — the one the tui is
standing in — and runs [`task_command`](../configuration.md#task_command) there with the
prompt. No worktree and no tab name of its own: the agents of one workspace are working on
one thing, in one checkout, and an unnamed tab is named by what runs in it, which is the agent
saying what it's doing. The tab opens behind the one you're typing in, so the table shows it
and you go when you're ready. A workspace with no other tab yet opens straight on that box,
since the first thing to do in a fresh workspace is say what it's for; `esc` puts it away.
`g` shows the history of the worktree under the selected tab, and `/`, the mouse and the `☰`
are the tui's own.

`T` is what to seed an agent with, when the repo keeps a [todography](https://github.com/mkmik/todography)
map: `todography next`, the wanted work whose needs are met, in a pane over the table. `c` on
an item opens the seed prompt on its `t8y:` url, the title the url without the scheme, and
leaves it there for you to say more before `↵` starts the agent. `/` narrows the items as
it does in the tui's pane.

## Trusting the repository

When the agents are Claude Code — `task_command` is `claude`, or unset — and Claude Code
hasn't been told to trust the repository yet, `ccwt ws` asks that first, over everything
else, the seed prompt included: an agent started in a tab nobody is looking at would
otherwise sit at Claude Code's own trust dialog with your prompt waiting behind it. `y`
records the answer where that dialog would, in `~/.claude.json` (or `$CLAUDE_CONFIG_DIR`'s),
under Claude Code's own lock, and on the repository's main checkout — which is what Claude
Code files it under, so one yes covers every worktree of it. `n` leaves Claude Code to ask
for itself. The home directory is never asked about: Claude Code only ever trusts that one
for a session.

## Finishing the workspace

`r` is the other end of that: [`ccwt done`](../worktrees/removing.md#ccwt-done) for the
workspace itself — the worktree the tui is standing in is removed, branch and all, and the
workspace closes behind it, this tui with it. It's on the bar only when that removal would go
through as it stands: branch merged, nothing uncommitted, and no agent still working in
there — the three checks `ccwt remove` makes before `-D`, asked of the very same code, so `r`
on the bar means this workspace is finished with. Whether it's there is a property of the
workspace and not of any one tab, so it doesn't come and go as you walk the table: it's on the
bar or it isn't, whichever row is selected. A tui run somewhere that isn't a worktree — the
repo itself — never has one.

This is part of a longer plan — what each tab is about, and which tickets and reviews each
one touches — and [`TODO.md`](https://github.com/mkmik/ccwt/blob/main/TODO.md) has the rest
of it.
