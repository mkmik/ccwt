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
  - [x] `space`/`↵`/double-click go to the tab; `c` opens the prompt; `g` is the history of the worktree under the tab
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
  - [ ] the workspace's review pushed rather than polled: a GraphQL subscription over `wss://<host>/-/cable`, glab's token as `Authorization: Bearer`, the query and variables in the `GraphqlChannel` subscribe identifier itself (not a later `execute` message). gitlab-master (17.7.7) has `mergeRequestMergeStatusUpdated`, `mergeRequestApprovalStateUpdated`, `mergeRequestReviewersUpdated` by `issuableId` (`gid://gitlab/MergeRequest/<id>`); a probe got `confirm_subscription`. An event refetches the row over REST; the minute poll stays as a slow fallback, since there is no pipeline subscription and a dropped socket loses events. Still unproven: that the merge-status one fires when a merge train lands. It's GitLab's frontend's api, undocumented for clients, and needs a websocket dependency
  - [ ] close a tab from the table (`r` is the workspace's — worktree and all; closing one tab would be a key of its own)
  - [x] a way to open a workspace with `ccwt ws` already in its first tab — the tui's `c`, which runs it there instead of the agent (`TestNewWorkspaceRunsWs`); the herdr plugin's own action still opens a bare worktree
  - [x] the list names, in a box in its corner, the workspaces whose `ws` tab has no ccwt running in it (`herdrWsDown`, `TestListWatchesTheWsTabsForTheirCcwt`): a look when herdr's events say a tab or workspace was made, renamed or closed, and every 10s besides
  - [x] only its own worktrees' workspaces: another repo's are that repo's list's to name, and their panes go unasked
  - [x] start `ccwt ws` again in those tabs, rather than only saying so — the corner's `↻`, after a plan and a `y` (`TestRestartMakesOrReusesThePrjTab`); under -g it is `ccwt` in a `prj` tab, made first when there is none

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
- The corner warning names only the list's own worktrees' workspaces, the ones
  herdr's sidebar shows under the repo (the configured projects' under -g).
  Herdr-wide, every repo's list named every other repo's too. Only the list
  looks: a ws view in every workspace, each watching all of them, is the
  traffic the socket plan cuts.

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
  - [x] `events.subscribe` on a connection held open: `tab.*`, `pane.updated`/`created`/`closed`/`agent_detected`, `workspace.renamed` — each one re-reads and redraws, as a tick does now. Not `pane.agent_status_changed`: herdr 0.9.5 takes it only for one `pane_id`, and an agent's status comes as a `pane.updated` anyway
  - [x] reconnect when herdr goes away and comes back (a restart, `herdr update --handoff`)
  - [x] off unless the config says `herdr_events = true`, the list's corner included: every ws view hears every workspace's events
  - `herdrListen` is that connection, redialled every couple of seconds while herdr is away; the list's corner warning and the ws view's round both listen on it
  - [ ] the tick goes slow, as the fallback for a missed event, rather than away
    - [x] the ws view: its tick redraws from what it last heard (`wsTabs`), and asks herdr only half a second after an event in its own workspace — or a tab focused anywhere, which is how one of ours loses the focus — or once a minute without one. In a probe session: 3 questions in 20s idle against 28, none while another workspace's agent worked
    - [ ] the list: `agent.list` and `workspace.list` still go every tick
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

# dennet beside herdr — task list

ccwt opens workspaces on worktrees, starts agents in their tabs, reads what
the agents are doing and puts badges on the workspaces. Until now it did all
of that with herdr. [dennet](https://github.com/mkmik/dennet) keeps the same
kind of things as Kubernetes-style objects, and dense (a Ghostty fork) draws
them:

- a **Space** is a workspace. Spaces form a tree: a repo's Space at the top,
  its worktrees' Spaces under it (`spec.parent`). `spec.workingDir` is the
  checkout, `spec.title` the name, `spec.order` the place among its siblings.
- a **Surface** is a tab of a Space (`spec.space`, `spec.order`, `spec.title`).
- a **Den** is the terminal in a tab (`spec.terminal.den`). den runs it in the
  background and reports `status.cwd`, `status.title`, `status.pid`, the
  screen. One terminal per tab: no splits.

Its api has what herdr's socket has for reading: get, list, and a watch that
streams every change, over a unix socket, as plain JSON over HTTP.

Everything ccwt says to herdr now goes through one Go interface, `mux`
(mux.go); herdr's side is herdr.go. dennet's side is to be dennet.go, picked
where ccwt runs inside a den. Neither side is finished on dennet's part: the
gaps below are what dennet, dense or den would need to add.

## How the two line up

| ccwt asks (`mux`) | herdr | dennet |
|---|---|---|
| `inside`, `here` | `HERDR_ENV`, `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID` | `DEN_SOCKET` is `<state>/den/<ns>/<name>.sock`, which names the Den; its Surface is the one whose `terminal.den` is that name, and the Space is that Surface's |
| `workspaces`, `sidebar`, `worktreeWorkspaces`, `repoWorkspaces` | `workspace list`, `worktree list` | Spaces, by `workingDir` and `parent`; order by walking the tree |
| `tabs`, `snapshot` | `tab list`, `pane list`, `api snapshot` | Surfaces of the Space by `order`, each with its Den's status |
| `listen` | `events.subscribe` | `?watch=1` on spaces, surfaces and dens; list again on `410 Gone` |
| `openWorktree`, `openProject`, `newTab` | `worktree open`, `tab create` | create the Space (and the repo's, when missing), a Surface and a Den |
| `run` | `pane run`: types a line at the shell | `den send`: types a line and Enter, on a den of this machine |
| `renameTab`, `moveTab`, `renameWorkspace` | `tab rename`/`move`, `workspace rename` | Surface `title`/`order`, Space `title` |
| `closeWorkspace` | `workspace close` | delete the Dens (that stops them), the Surfaces, then the Space |
| `badge` | a `ccwt` token with a ttl | `spec.badges` — see gap 4 |
| `agents` | `agent list`: working, blocked, idle, done, and a counter | none — gap 2 |
| `focusTab`, `focusWorkspace` | `tab focus`, `workspace focus` | a request on our own Surface, obeyed by dense — gap 1 |
| `focused` | `workspace get` | our own Space only: the window that typed into our den shows it — gap 1 |
| `procs` | `pane process-info` | none — gap 3 |
| `popup` | a plugin pane | none — gap 6 |
| `file` | `workspace move` under a `------` divider | none — gap 8 |

## Gaps

What dennet (or dense, or den) does not have yet, and what ccwt does meanwhile.

1. **Focus.** Nothing in dennet says which Space and tab dense shows: each
   dense window keeps its own selection, so different windows can show
   different tabs. ccwt needs to know for the green dot going grey once a tab
   has been looked at; meanwhile it stays green until the tab's agent works
   again.
   - Asking dense to show something (`space` on a row, `nav ws`, `done`
     landing on the repo's workspace, `x`/`c` bringing the new workspace to
     the front) changes no den, no dennet and no terminal code, only dense's
     app code, since restarting dens stops what runs in them:
     - ccwt writes the request on its own Surface, as an annotation:
       `dense.mkm.pub/show: {"space":…,"surface":…,"at":…}`. The time makes
       each request a change, even when it repeats the one before. It is
       written only when ccwt asks for something, never on a click in dense,
       and nothing has to take it off again.
     - Every dense window already watches the Surfaces. A window acts on a
       request only when both of these hold:
       - the Surface that asks is the tab in front of that window;
       - that window got a key press in the last 5 seconds.
     - That is the window whose key press made ccwt ask: the rule den uses for
       the terminal size, worked out by dense itself, with an `NSEvent`
       monitor.
     - The window shows the Space or tab, waiting up to 10 seconds for one
       that dennet's watch hasn't brought in yet.
     - A request already there when a watch starts is old, and ignored.
   - Turned down: a private OSC that den would hand only to the client that
     sets the size. It needs a new den, and a terminal extension in dense.
2. **Agent status.** herdr finds the agents in its panes and says what each
   is doing. den does not. (a) Claude Code writes its state into the terminal
   title: a spinner glyph while it works, `✳` when it is idle — a den's
   `status.title` reads `◐ CCWT Dennet and Herder integration` mid-turn. No
   "blocked", Claude Code only, and `lastOutputTime` stands in for herdr's
   counter. (b) A Claude Code hook (`ccwt hook`) that writes the state onto
   the Den, as `herdr pane report-agent` does: exact, and "blocked" too. (a)
   first.
3. **What runs in front.** herdr says which processes are in the foreground
   of a pane; a Den has only its program's `status.pid`. The list's corner and
   `↻` ask it to see whether a `ws` tab still runs ccwt. On this machine ccwt
   can walk the processes under that pid itself (ps.go does already); for a
   den on another Host, den would have to report it.
4. **Badges with an owner and an end.** herdr's badge is ccwt's own token, and
   goes away by itself when nobody renews it. `spec.badges` is a plain list:
   ccwt has to tell its badge from others (by its glyph, until a badge has a
   `source`), and one left by a ccwt that died stays. Wants `source` and an
   expiry on a badge.
5. **Deleting a workspace.** dennet has no garbage collector: deleting a
   Space leaves its Surfaces and Dens. ccwt deletes them itself.
6. **Popups.** No plugin panes: `ccwt open` falls back to the picker in its
   own terminal, as it does outside herdr.
7. **Typing into a den on another Host.** `den send` needs the den's socket,
   on that machine. dennet's `dens/input` (todo in its map) would do it from
   here.
8. **Dividers.** herdr's sidebar is flat, with `------` workspaces between
   categories; dense's is a tree. Undecided: a Space per category with the
   repos under it, or no categories.

## Stages

- [x] 1. One seam: everything ccwt says to herdr goes through `mux` — mux.go
  is the interface and what is the same for both, herdr.go is herdr's side.
  Nothing ccwt does changed: the herdr tests fake herdr's cli and socket,
  which the seam left where they were, and pass unchanged but for renames
- [x] 2. dennet, reading
  - [x] dennet.go: JSON over HTTP on dennet's socket (`$XDG_RUNTIME_DIR/dennet/dennet.sock`, else `$XDG_STATE_HOME/dennet/dennet.sock`, the first that is there), standard library only
  - [x] `mx()` picks dennet inside a den (`DEN_SOCKET`, no `HERDR_ENV`); `TestMain` drops `DEN_*` as it drops `HERDR_*`
  - [x] `here` (remembered once found), `workspaces`, `sidebar`, `worktreeWorkspaces`, `repoWorkspaces`, `tabs`, `paneAt`, `paneTab`, `place`, `snapshot`
  - [x] `agents` from Den titles (gap 2a): `✳` idle, a half circle or braille spinner working
  - [x] `listen`: a watch each on spaces, surfaces and — for the ws view — dens, only what touches its own Space poking it
  - [x] tests against a fake dennet on a unix socket (`TestDennetReads`, `TestDennetListens`, `TestDenAgent`); tried live with `ccwt nav ws --dry-run`, which found the copied herdr workspaces by title
- [x] 3. dennet, acting
  - [x] `openWorktree`, `openProject`, `newTab`: a Space named after the directory (dennet's own name when that is taken), under the repo's Space, made when missing; a Den as dense makes one, and a Surface of the same name
  - [x] `run` with `den send`, tried again for a few seconds while a new den starts; `renameTab`, `moveTab`, `renameWorkspace`
  - [x] `closeWorkspace` (gap 5): the Space, its Surfaces, then their Dens — ours last, since deleting it stops this ccwt; what is gone already is fine
  - [x] `badge` on `spec.badges` (gap 4): ccwt's own told apart by its glyphs, written only on a change, with the resourceVersion so another's badge isn't written over
  - [x] `procs` from the process table: the terminal's foreground group (`ps -o tpgid`) read off the Den's `status.pid`
  - [x] tests against a fake dennet that keeps what it is told (`TestDennetNewWorkspace`, `TestDennetActs`, `TestForeground`); tried live in a namespace of its own: a tab made, `echo` sent and run, `sleep` seen in front, all of it closed again
  - [x] the manual's "Under dennet and dense"
- [x] 3b. Going somewhere (gap 1): `focusTab`, `focusWorkspace`, and
  `openWorktree`/`openProject` showing what they open, as a request on our
  Surface. dense's side, `ShowRequest` and `SpaceTabs.obeys`, is in dense's
  worktree piped-honking-backus, not committed yet. dennet took the
  annotation in a probe namespace.
- [ ] 4. Waiting on dennet, dense or den: which tab a window shows (1), the agent hook (2b),
  what runs in front on another Host (3), badge owners (4), cascading deletes
  (5), remote input (7), categories (8)

## Decisions

- `mux` speaks ccwt's words — workspace, tab, pane, agent — and its ids are
  whatever the mux calls things, handed back to it unread.
- What dennet has nothing for (focus, popups) says so on the bar rather than
  hiding its key: the keys are the ones herdr has, and the bar says why one
  does nothing.
- No green "unread" dot under dennet: a tab is never seen being looked at
  (gap 1), so `tabs` leaves the counter at 0 and a settled agent is grey.
- `ccwt pause` is herdr's: dennet's lists and watches go on while it is in
  force. The watch still waits for `herdr_events = true`, as herdr's does.
- The corner looks at what runs in each den with a `ps` of its own, a fork per
  pane every 10s. ponytail: one `ps` a round would do, if there are ever enough
  `ws` tabs for it to show.
- `mx()` chooses afresh at each call, from the environment, so a test changes
  it with `t.Setenv`. `herdrMux` keeps the cli it was made with, so a rename
  put off for a second still goes to the herdr it was meant for.
- Reads come from a list-then-watch per resource (`dennetStore`), started by
  the first read and shared by every view in the process, not a list of all
  three resources on every tick. What an action writes from (`dennetOpen`,
  `moveTab`, `createTab`, `closeWorkspace`, `badge`) still asks dennet itself,
  so it never acts on what a watch hasn't brought in yet.
- The standard library, not client-go, to talk to dennet: the api is JSON over
  HTTP, and client-go would add megabytes to each of the 40-odd ccwts running
  (see "One ccwt for every pane").
- An agent is typed into its tab with `den send`, as herdr's `pane run` types
  it, rather than started as the Den's command: one flow for both muxes, the
  prompt already travels in a file (promptArg) so the tty's line limit doesn't
  bite, and live the den took the line within milliseconds of being made, the
  shell running it once it was up.

# One ccwt for every pane — the idea, not yet a plan

Every pane runs a whole ccwt: 44 of them on 2026-10-12 (19 lists, 23 ws views,
one `-g`), ~18 MB of physical footprint each, ~760 MB together. Heap profiles
of all of them, live, said the data isn't what costs: `ccwt -g` covers every
configured repo in one process at 39 MB. Each instance pays instead for

- 2.9 MB of Go runtime: what a Go binary that only sleeps weighs (`sleep` is 1.0)
- 5.3 MB for linking modernc sqlite, before a database is opened: libc's init
  tables, `netdb.init` among them
- ~10 MB of its own heap, ~3 MB of it live. GOGC 25 or 10, and even
  `debug.FreeOSMemory` every second, moved the total by under 3%

## Stages, if it earns its keep

- [ ] 1. The tuis stop linking sqlite: the queue read behind a `ccwt` subprocess
  or a plain file. ~2 MB of footprint an instance (a ws view built without
  the driver: 11.3 MB against 13.4), ~90 MB at today's count
- [ ] 2. A daemon gathers: one process does the git, transcript, herdr and MR
  rounds for every repo and workspace, and the tuis subscribe to it over a unix
  socket rather than each asking for itself. Also cuts the cpu: `-g` repeats
  every list's rounds, and every ws view repeats its worktree's merged check
- [ ] 3. Thin clients: the pane runs a small `ccwt` that hands its tty over the
  socket (`SCM_RIGHTS`), and the daemon draws on it. ~1 MB a client in C or
  Rust, ~3 MB in Go; with the daemon at ~40–50 MB, ~90–180 MB in all

## Things stage 3 has to answer

- `$EDITOR`, and anything else that reads the tty, has to run in the client:
  a child of the daemon isn't in the tty's foreground process group, and a read
  from it stops it with SIGTTIN
- SIGWINCH and ^C go to the client, which forwards them
- `herdrRunsCcwt` knows a tui by its process name, so the client is `ccwt` too
- an upgrade re-execs the daemon, and the clients, still holding their ttys,
  reconnect and hand them over again. That is also the answer to one hung
  daemon freezing every pane
- per-session state: the drawing code reads 42 package vars, 25 `os.Getenv`s,
  4 `os.Getwd`s, and prints to stdout. A refactor, not a rewrite
