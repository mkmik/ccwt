# `ccwt ws` — task list

`ccwt ws` is a tui for the first tab of a Herdr workspace: it asks for a seed
prompt, spawns agents in other tabs of that workspace, and keeps a table of
those tabs to jump between. Over time it learns what each tab is about, and
which tickets and MRs each one touches. This file is the plan, kept between
stages; tick things off as they land.

## Stages

- [x] 1. `ccwt ws`: the seed prompt and the tab table
  - [x] `ccwt ws` runs the tui in a ws mode (as `P` runs it in the ps one); refuses to start outside Herdr
  - [x] seed prompt: `herdr tab create --cwd <the workspace's worktree> --no-focus`, `task_command '<prompt>'` in the pane the create names — no worktree and no label of its own
  - [x] opens on the seed prompt when the workspace has no other tab yet
  - [x] tab table: TAB, AGENT (Herdr's `agent_status`, blank for `unknown`), DIR (the first pane's cwd), TITLE (its terminal title); `*` on the tui's own tab
  - [x] `space`/`↵`/double-click go to the tab; `n` opens the prompt; `g` is the history of the worktree under the tab
  - [x] tests: the table (`TestWsTableListsTheWorkspaceTabs`), the seed (`TestWsSeedStartsAnAgentInANewTab`)
- [ ] 2. What each tab is about
  - [ ] a TOPIC column: an agent's one-line summary of the tab, from its transcript or `herdr pane read`, with a glyph saying where it came from (as the list's TOPIC has `✳`/`⎇`); TITLE stays the fallback
  - [ ] summaries cached and refreshed only when the pane moved on (the `cached` helper in main.go)
  - [ ] `d` details pane for a tab — `detailPane` takes its labels from the worktree table today, so the ws view leaves `cells` nil
- [ ] 3. Tickets and MRs
  - [ ] the ticket a tab is on: Jira keys in its prompt, branch or commits (`/jira`-style lookups, not curl)
  - [x] the workspace's own review, as `ccwt mr` prints it, in a section above the tab table — one quiet line while the branch has no merge request yet (`mrSection`), looked up in the background so the frame never waits on gitlab
  - [ ] the review its branch has: `glab`/`gh`, as `m` finds it for a worktree; `m` on a tab opens it
  - [ ] columns for both, and their state (open, approved, merged) in the table
- [ ] 4. Later, if it earns its keep
  - [ ] queue a follow-up prompt behind a tab (the `tasks.db` chains already do this for worktrees)
  - [x] `r`: `ccwt done` for the workspace the tui is in, on the bar only when the removal would go through without -D — `removeBlocked` is the one test `remove`, `done` and the ws view all ask
  - [ ] close a tab from the table (`r` is the workspace's — worktree and all; closing one tab would be a key of its own)
  - [x] a way to open a workspace with `ccwt ws` already in its first tab — the tui's `c`, which runs it there instead of the agent (`TestNewWorkspaceRunsWs`); the herdr plugin's own action still opens a bare worktree
  - [x] the list names, in a box in its corner, the workspaces whose `ws` tab has no ccwt running in it (`herdrWsDown`, `TestListWatchesTheWsTabsForTheirCcwt`): a look when herdr's events say a tab or workspace was made, renamed or closed, and every 10s besides
  - [ ] start `ccwt ws` again in those tabs, rather than only saying so

## Decisions

- One worktree per workspace, not per seed prompt: the agents of a workspace
  are working on one thing, and they share its checkout. `c` on the list makes
  that worktree and puts `ccwt ws` in it.
- New tabs open unfocused: the conductor stays on screen, the table shows the
  tab, `space` goes to it.
- Tabs go unlabelled: with one worktree for all of them the name would be the
  same every time, and herdr names a tab by what runs in it.
- The seed isn't recorded in `tasks.db`: it runs at once, and the tab is the
  record. Stage 4's follow-ups would be.
- Claude Code's trust question is asked by the ws view itself, first, when the
  agent is `claude`: an unfocused tab would otherwise sit at the dialog with the
  seed prompt behind it. Yes goes on the repository's main checkout in
  `~/.claude.json`, which is Claude Code's own key for it, so one answer covers
  every worktree; no leaves Claude Code to ask.
- A tab named `ws` is ccwt's wherever it is, so the corner warning is
  herdr-wide rather than the repo's. Only the list looks: a ws view in every
  workspace, each watching all of them, is the traffic the socket plan cuts.

# Talking to herdr over its socket — task list

ccwt asks herdr things by running `herdr`: a process per question, and a `ping`
on herdr's socket ahead of each. A `ccwt ws` re-reads its tabs every 2s, three
questions a round, and there is one in every workspace, so the processes grow
with the workspaces. Herdr hands every pane the socket the cli itself talks
to, in `HERDR_SOCKET_PATH`.

## Stages

- [x] 1. What repeats goes over the socket
  - [x] `herdrAsk`: one request, one line back — over the socket under herdr, through the cli anywhere else, the same JSON either way (the cli prints what the socket answers)
  - [x] the ws view's round (`tab.list`, `pane.list`, `agent.list`), the list's (`agent.list`, `workspace.list`), and the badge once a minute (`workspace.report_metadata`)
  - [x] tests: `TestHerdrIsAskedOverItsSocket`; `TestMain` drops the herdr environment `go test` inherits, so no test reaches the live herdr through its socket
- [ ] 2. Hear from herdr instead of asking every 2s
  - [ ] `events.subscribe` on a connection held open: `tab.*`, `pane.updated`/`created`/`closed`, `pane.agent_status_changed`, `workspace.renamed` — each one re-reads and redraws, as a tick does now
  - [ ] reconnect when herdr goes away and comes back (a restart, `herdr update --handoff`)
  - `herdrListen` is that connection, redialled every couple of seconds while herdr is away; the list's corner warning listens on it, the ws view's round doesn't yet
  - [ ] the tick goes slow, as the fallback for a missed event, rather than away
- [ ] 3. Later, if it earns its keep
  - [ ] the one-off reads (`nav ws`, `pane get`, `worktree list`, …): a process per keypress rather than per round
  - [ ] the actions: `pane run` is the cli's own — it types the line — with no method to call instead, so they stay on the cli

## Decisions

- The socket only where herdr names it: outside herdr the cli finds the socket
  by itself, so `ccwt ls` or `ccwt remove` in a plain terminal still see the
  agents at work.
- A connection per request: a fraction of a millisecond, and nothing to
  reconnect. Stage 2's subscription is the first thing that wants one held
  open.
- No `ping` first, as the cli does: a request herdr can't take comes back as
  an error line, which `herdrAsk` returns as an error.
- `encoding/json/v2` for the request, which writes a nil map as `{}`: herdr
  refuses `"params": null`.
- Herdr has no event for what is in a pane's foreground: a program exiting
  back to its shell says nothing at all (a probe session, herdr 0.9.1). So
  whatever watches for a process — the list's corner — keeps a slow look going
  alongside its events.
