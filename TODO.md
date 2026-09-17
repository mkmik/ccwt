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
  - [ ] the review its branch has: `glab`/`gh`, as `m` finds it for a worktree; `m` on a tab opens it
  - [ ] columns for both, and their state (open, approved, merged) in the table
- [ ] 4. Later, if it earns its keep
  - [ ] queue a follow-up prompt behind a tab (the `tasks.db` chains already do this for worktrees)
  - [x] `r`: `ccwt done` for the workspace the tui is in, on the bar only when the removal would go through without -D — `removeBlocked` is the one test `remove`, `done` and the ws view all ask
  - [ ] close a tab from the table (`r` is the workspace's — worktree and all; closing one tab would be a key of its own)
  - [x] a way to open a workspace with `ccwt ws` already in its first tab — the tui's `c`, which runs it there instead of the agent (`TestNewWorkspaceRunsWs`); the herdr plugin's own action still opens a bare worktree

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
