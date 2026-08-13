# ccwt

A small command-line helper for managing [Claude Code](https://claude.com/claude-code) git worktrees.

Claude Code can run agents in isolated git worktrees under `.claude/worktrees/<name>`.
`ccwt` lets you create, list, jump between, and tear down those same worktrees from
your own shell — using the same layout and the same adjective-verb-noun naming scheme
that Claude Code uses, so the two stay interoperable.

## Install

```sh
go install github.com/mkmik/ccwt@latest
```

This drops a `ccwt` binary in `$(go env GOPATH)/bin` (make sure that's on your `PATH`).

## Shell integration (recommended)

A program can't change its parent shell's working directory, so on its own `ccwt new`
can only *print* the path of the worktree it created. To make `ccwt` actually `cd` you
into a worktree (and `ccwt ..` jump you back to the repo root), source the integration
snippet from your shell's rc file:

```sh
source <(ccwt init zsh)
```

Everything still works without it — you just won't get the automatic directory change.

Under zsh the same snippet also installs completion: `ccwt <TAB>` lists the commands,
and `ccwt cd <TAB>` (like `remove` and `lock`) lists the worktrees of the repo you're
standing in. Source it *after* `compinit`, which is what defines `compdef` — sourced
before, you keep the `cd` and silently get no completion.

## Quick start

```sh
ccwt new            # create a fresh worktree on a new branch, and cd into it
                    # ... do your work, run `claude`, commit, etc. ...

ccwt list           # show all Claude Code worktrees in this repo
ccwt list -g        # ... or in every project in ~/.config/ccwt/config.toml
ccwt ..             # jump back to the repository root
ccwt remove <name>  # delete a worktree and its branch when you're done
ccwt remove .       # ... or the one you're in: deletes it and cds you out
ccwt gc             # ... or all the finished ones at once, after confirming
```

## Creating worktrees

`ccwt new` creates a worktree at `.claude/worktrees/<name>` on a new branch
`worktree-<name>` (the prefix is [configurable](#several-projects-at-once)). With no name
it generates one (e.g. `dreamy-foraging-hickey`); pass a name to choose your own. Run from
inside an existing worktree, it returns that worktree instead of nesting a new one.

```sh
ccwt new --switch foobar          # check out an existing branch instead of creating one
ccwt new mywt --switch foobar     # ... in a worktree of your own name
ccwt new --path                   # print the absolute path instead of the name
```

## Listing worktrees

`ccwt list` renders a table of the repo's worktrees with their branch, age, whether a
Claude Code session is currently running in each, and what each one is about:

```
    NAME                    BRANCH                    AGE  CLAUDE  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes     ✳ Goal was a widget on the dashboard; it's built and…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor
```

Every row leads with two glyphs. The first is a `*` on the worktree you're currently in.
The second says whether the worktree can go: `✓` means its branch is already merged into
`main` (or `master`), so it's safe to `ccwt remove`; `☐` means it isn't merged but every
commit is already on its upstream, so nothing here is waiting on you — it's waiting on a
review or on CI; `*` means it has uncommitted changes, which isn't safe to remove whatever
git makes of the branch or the remote; `✳` beats them all and means an
agent is working in there right now — see [Herdr integration](#herdr-integration-optional).
The glyphs are omitted when stdout isn't a terminal, so piped output stays parseable.

TOPIC says what the worktree is about, and its glyph says where that came from. `✳` is a
Claude Code session — the newest transcript for that worktree, showing the last recap it
produced (`/recap`, or one Claude wrote on its own), or the first prompt you typed when it
never recapped. `⎇` is the last commit, for a worktree nobody has run a session in.

The table is sized to your terminal, so it never wraps. BRANCH is kept narrow even when
there's room — it's usually the worktree's own name with a `worktree-` in front — and the
space goes to TOPIC. Branches and commit subjects lose their middle rather than their tail,
since the last word is usually what tells them apart, while a session summary simply stops:

```
    NAME                     BRANCH                   AGE  CLAUDE  TOPIC
* * dreamy-foraging-hickey   worktree-dreamy-…-hickey 2h   yes     ✳ Goal was a widget on…
  ✓ calm-baking-otter        worktree-calm-b…-otter   1d   no      ⎇ Fix the flux ca… (#41)
```

## The tui

`ccwt tui` — or just `ccwt`, which is what a bare invocation runs — shows that same table
full-screen and keeps it up to date, repainting in place so it doesn't flicker, with a
status bar along the bottom:

```
    NAME                    BRANCH                    AGE  CLAUDE  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes     ✳ Goal was a widget on the dashboard; it's built and…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor

 ccwt  q:quit  p:pull  /:search  l:log  x:new  space:open  n:queue  g:vcs  d:details  r:remove │ main ↑1
```

`q` (or Ctrl-C) quits, `p` runs `git pull` and reports the result in the bar. The rest of
the bar is the branch you launched it from and how far it has drifted from its upstream
(`↑` ahead, `↓` behind). Those counts are kept honest by a background `git fetch` of
`origin/main` alone; `--fetch` (default `1m`) sets how often, or `0` never. `--interval`
(default `2s`) sets the refresh rate.

The arrow keys (or `j`/`k`) select a worktree, and the bar grows the actions that apply to
it: `r` removes it, refusing an unmerged branch, uncommitted changes or a working agent,
exactly like `ccwt remove`. Clicking a row selects it. Two more keys — `x` and `space` —
appear only under [Herdr](#herdr-integration-optional).

`g` opens the worktree's review in your browser — the branch is here, the review is over
there. Which one that is depends on where `origin` points: a GitHub remote is a pull request
and [`gh`](https://cli.github.com) finds it, a GitLab one is a merge request and
[`glab`](https://gitlab.com/gitlab-org/cli) does. Either cli already knows which host the
remote is and which token to ask it with; ccwt only decides which of the two to run, and
hands the url it prints to `open` (macOS) or `xdg-open`. Without the cli installed, or on a
branch with no review yet, the bar just says there's none to open.

`github.com`, `gitlab.com`, and any host with `gitlab` in its name are recognised as they
are. A self-hosted instance that doesn't keep the word — `code.example.com` — needs a line
in `~/.config/ccwt/config.toml`, since there's nothing in the hostname to go on:

```toml
[forges]
"code.example.com" = "glab"
```

`d` opens the selected worktree's details as a window over the list: every column's value in
full, one per line and wrapped to the width, instead of the row the table had to cut down to
fit. It's modal — `esc` closes it, and while it's up the other keys do nothing (bar `e` on a
[queued prompt](#queued-prompts)) and the list behind it holds still. Every column the table
knows about is in there, including any the config leaves out of the list.

```
    NAME                  BRANCH                    AGE  CLAUDE  TOPIC
  ✓ calm-baking-otter     worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor
    ┌─ dreamy-foraging-hickey ───────────────────────────────────────────────────────┐
    │ NAME    dreamy-foraging-hickey                                                 │
    │ BRANCH  worktree-dreamy-foraging-hickey                                        │
    │ AGE     2h                                                                     │
    │ CLAUDE  yes                                                                    │
    │ TOPIC   ✳ Goal was a widget on the dashboard; it's built and wired up, and the │
    │         numbers on it are still the mocked ones                                │
    └────────────────────────────────────────────────────────────────────────────────┘
```

### The worklog

Removing a worktree also removes the only record of what it was for: the branch is deleted,
the directory is gone, and Claude Code files its transcript under a path that no longer
exists. So `ccwt remove` writes a line about it first — when it went, what it was called,
and the `TOPIC` the list showed — into `$XDG_STATE_HOME/ccwt/tasks.db`, next to the queued
prompts, and every removal goes in: `ccwt remove`, `ccwt done`, `ccwt gc`, and `r` in the
tui.

`l` shows that log as a window over the list, newest first; `esc` (or `l` again) closes it.
Its rows select like the list's — the arrows (or `j`/`k`) walk them, a click lands on one —
and `↵` opens the selected removal's page.

```
    NAME                  BRANCH                    AGE  CLAUDE  TOPIC
  ✓ calm-baking-otter     worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor
    ┌─ worklog ──────────────────────────────────────────────────────────────────────┐
    │ AGE  NAME                    TOPIC                                             │
    │ 2h   kind-munching-melody    ✳ Kept a log of removed worktrees, with a pane in… │
    │ 1d   dreamy-foraging-hickey  ⎇ Quote a queued prompt on its way to th… (#79)   │
    └────────────────────────────────────────────────────────────────────────────────┘
```

That page is everything left to know about one removed worktree: what the log recorded, and
then the whole of the last Claude Code session that ran in it — the prompts, what Claude
said back, and the tools it reached for, which survive the removal because Claude Code files
transcripts under your home directory rather than in the tree they were about. It scrolls:
the arrows (or `j`/`k`) by a line, `space`/`b` by a screenful, `g`/`G` to either end, `esc`
back to the log.

```
    ┌─ dreamy-foraging-hickey ───────────────────────────────────────────────────────┐
    │ NAME     dreamy-foraging-hickey                                                │
    │ PROJECT  /Users/you/src/ccwt                                                   │
    │ REMOVED  1d ago, 2026-08-12 09:31                                              │
    │ PATH     /Users/you/src/ccwt/.claude/worktrees/dreamy-foraging-hickey          │
    │ TOPIC    ⎇ Quote a queued prompt on its way to the pane (#79)                  │
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

`ccwt worklog` prints the same table on the command line, `-n` (default 20) saying how many
lines of it. `-g` spans every configured project instead of this repo's, and adds a
`PROJECT` column, since a worktree name on its own doesn't say which repo it was in; the
pane does the same when the tui is running under `-g`.

### Queued prompts

Work you've thought of but can't start yet — because it needs the worktree you're in to be
finished first — goes on the list as a prompt queued behind it. `n`, for what to do next,
opens a box over the middle of the list, and what you type there is recorded under the
selected row:

```
    NAME                    BRANCH                    AGE  CLAUDE  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes     ✳ Goal was a widget on…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor
          ┌─ dreamy-foraging-hickey ──────────────────────────┐
          │ rebase onto main, open the PR, and then port the  │
          │ widget to the mobile layout█                      │
          │                                                   │
          └───────────────────────────────────────────────────┘

 ↵:queue  ctrl-g:$EDITOR  esc:cancel
```

It's three fifths of the terminal, that size whatever is in it — a box that grew as you
typed would shift the list behind it line by line — and the row the prompt will hang off is
one of the ones still showing around it. `↵` records it, `esc` throws it away. Behind
another queued prompt the box's top rule names that prompt instead, since a chain can have
several links and the worktree's name wouldn't say which one you're extending.

Once recorded it's a row of the list:

```
    NAME                    BRANCH                    AGE  CLAUDE  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes     ✳ Goal was a widget on…
    ↳ <queued>                                        4m           rebase onto main and open the PR
      ↳ <queued>                                      3m           then port the widget to the mobile layout
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor
```

It has no worktree of its own yet, and `<queued>` in NAME says that's why the column is
otherwise empty, the way `<new>` does below for a prompt whose turn has come.

`n` on a queued prompt queues another one behind *that* one instead, so a chain of "and
then" is a tree hanging off the worktree it starts from. `d` shows a long prompt in full in
the details pane, and `r` deletes one — along with everything queued behind it, since
nothing waiting on a prompt that isn't going to happen can happen either.

`e` in that details pane — where the prompt is legible in full, which is where you notice it
needs a word changing — reopens it in the same box for rewriting. The edit box takes the
pane's place rather than stacking on it, `↵` saves, and the prompt stays where it is in the
chain: whatever was waiting on it still is.

The box is a line editor, since the word that needs changing is rarely the last one: `←` and
`→` move the caret, Ctrl-`←`/`→` move it a word at a time, `home` and `end` (or Ctrl-A and
Ctrl-E) go to either end, `backspace` and `delete` take the character on their own side of
it, and Ctrl-W, Ctrl-U and Ctrl-K rub out the word before the caret, everything before it
and everything after it. Text goes in where the caret is, and the box scrolls to wherever
that is, so the start of a prompt taller than the box is reachable.

For a prompt that wants more than that, Ctrl-G finishes it in `$EDITOR` — the same key
Claude Code's own prompt box binds it to, since it's the same box for the same purpose. The
tui hands over the screen and the keyboard, opens `$VISUAL`, `$EDITOR` or `vi` on what
you've typed so far, and takes back whatever you save, with the list drawn again around it.
An editor that quits without saving leaves the prompt as it was. What comes back is one
line — newlines fold to spaces, because a prompt is one line in the box, in TOPIC and in
the details pane alike.

`n` is also vim's next-match key, and while a search pattern is in force that's what it
stays: `/`, then `n` and `N` to walk the matches, and `esc` to clear the pattern and get the
queue key back. The bar says which one is live.

Removing the worktree does *not* take the chain with it — it starts it. Every prompt that
was waiting on that worktree moves up to a row of its own, called `<new>` because its turn
has come and it hasn't got a worktree yet, with whatever was queued behind it still under
it:

```
    NAME                    BRANCH                    AGE  CLAUDE  TOPIC
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor
    <new>                                             2h           rebase onto main and open the PR
    ↳ <queued>                                        2h           then port the widget to the mobile layout
```

`space` (or a double-click) on a `<new>` row is where the worktree finally gets made: a
fresh one, opened as its own [Herdr](#herdr-integration-optional) workspace with the prompt
running in it — `claude "<the prompt>"`, or whatever `task_command` in the config says, e.g.

```toml
task_command = "claude --permission-mode plan"
```

The row is then a worktree like any other, and the rest of the chain hangs off it, waiting
on the work that has just started rather than on the prompt that started it.

A `<new>` row is also where work that isn't waiting for *anything* starts, and `n` with
nothing selected is how you write one down: there's no row to hang it off, so it goes on the
list as a `<new>` of its own straight away — a worktree that hasn't been made yet, with the
prompt to run in it when it is. `esc` is how you get back to nothing selected, and `space`
on the row makes the worktree, exactly as it does for one that got there by having its
worktree removed. Under `-g` this needs a project selected, its section header being enough
to say which repo the worktree is to be made in; with nothing selected there the bar drops
the key.

The queue lives in `$XDG_STATE_HOME/ccwt/tasks.db` (`~/.local/state/ccwt/tasks.db` by
default) — one SQLite database for every project, so a prompt queued in a project's own tui
shows up in `tui -g` and the other way round, within a refresh interval either way.

`/` searches, as in vim or less: type a pattern into the bar and the selection moves to the
match as you type, `enter` accepts it, `esc` puts back the pattern and the row you started
from, and `n`/`N` walk the rest of the matches forwards and back, wrapping around the ends.
`?` searches upwards instead. Every match on screen is picked out, not just the selected
one; a bare `esc` clears them, and clears the selection with them — nothing is selected
again, and the bar drops the keys that only apply to a row.

The pattern is a [regular expression](https://pkg.go.dev/regexp/syntax), always
case-insensitive, matched against the whole line as drawn — name, branch, age and topic
alike, so `/✓` finds the worktrees whose branch is merged and `/yes` the ones with Claude
running in them. One that doesn't compile simply matches nothing, which is what half of one
is while you're still typing it.

Something parked in a pane for days goes on running the binary it started from, so the tui
watches that binary and says so when it's replaced — by `go install`, by a package manager,
by a downloaded release:

```
 ccwt  q:quit  p:pull  /:search │ upgraded to v0.64.0 — restart ccwt
```

The notice stays up until you do restart it, and the version is the one you'd be restarting
into (asked of the new binary itself; it's dropped from the line if it won't answer).

## Several projects at once

`-g` makes `ccwt list` and `ccwt tui` span every project you've configured instead of just
the repo you're standing in, a section per project, each holding that project's worktrees
newest-first:

```
    NAME                      BRANCH                    AGE  CLAUDE  TOPIC
▾ ccwt (2)
  * dreamy-foraging-hickey    worktree-dreamy-…-hickey  2h   yes     ✳ Goal was a widget on…
  ✓ calm-baking-otter         worktree-calm-bak…-otter  1d   no      ⎇ Fix the flux capacitor
▸ platform (14)
```

In the tui the sections fold: select one and press `↵`, or double-click it, and it collapses to
the header line — the count stays, so you can see what's tucked away. `platform` above is
folded shut. Folding has its own key so that it can't misfire into opening a worktree:
`space` only opens, `↵` only folds.

It works from anywhere — including outside a git repository — and the directory you launch
it in takes no part in it: under `-g` the repos are the configured ones and nothing else.
Every action applies to the project the selected row belongs to: `r` removes from that
repo, `x` creates one in that project — on a section header too, which is how you make the
first worktree in a project that has none — `p` pulls it, and the status bar shows that
project's branch state. With nothing selected yet there's no repo to act on, so those keys
say so instead of reaching for the current directory. The background `--fetch` covers every
configured project.

The projects come from `$XDG_CONFIG_HOME/ccwt/config.toml` (`~/.config/ccwt/config.toml`
when that variable isn't set) — each entry the main checkout of a repo, the one whose
`.claude/worktrees/` the worktrees live under:

```toml
[[projects]]
path = "~/src/ccwt"

[[projects]]
path = "~/src/platform"
```

`branch_prefix` sets what `ccwt new` puts in front of a worktree's name to make its branch
(`worktree-` when unset), for repos that want their branches namespaced:

```toml
branch_prefix = "mkm/"
```

It applies from the moment you set it: worktrees created under an older prefix keep their
branches, and `ccwt remove` on one of them leaves that branch behind rather than deleting
it — remove it with `git branch -D` if you want it gone.

`columns` picks which columns `ccwt list` and `ccwt tui` draw, in the order you name them
— `name`, `branch`, `age`, `claude`, `topic`, all of them when unset:

```toml
columns = ["name", "age", "topic"]
```

The `*`/`✓`/`☐`/`✳` markers ride on `name`, so leaving it out leaves them out too.

`task_command` is what a [queued prompt](#queued-prompts) is handed to when its worktree is
made — `claude` when unset, split on spaces so flags get through:

```toml
task_command = "claude --permission-mode plan"
```

`forges` says which cli knows about a host's reviews, for the tui's [`g`](#the-tui) key.
Only a host whose name doesn't give it away needs a line — a self-hosted GitLab most of
all, since `github.com`, `gitlab.com` and anything with `gitlab` in the name are guessed:

```toml
[forges]
"code.example.com" = "glab"
```

`ccwt config view` prints that file and `ccwt config edit` opens it in `$EDITOR`, either one
creating an empty file first when you don't have one yet.

## Herdr integration (optional)

[Herdr](https://herdr.dev) manages terminal workspaces for coding agents. `ccwt` doesn't need
it — everything above works on its own. When Herdr *is* running, these extras appear, and
this section is the whole of it:

**The `✳` marker.** In `ccwt list` and the tui, `✳` in the leading glyphs means Herdr says an
agent is working in that worktree right now. It outranks `*`, `☐` and `✓`, and it's the case git
can't see at all: a branch made a minute ago with nothing committed to it is merged and clean.
It comes from Herdr rather than from Claude Code, so it holds for whatever agent is running
there. `ccwt remove`, `ccwt done` and `ccwt gc` all refuse a worktree marked this way
(`ccwt remove -D` interrupts the agent anyway).

**Opening workspaces from the tui.** `space` opens the selected worktree as its own Herdr
workspace (via `herdr worktree open`), and a double-click on its row does the same. `x`
creates a worktree and opens it without leaving the list. These keys only exist when the tui
is itself running in a Herdr pane — elsewhere there's no session to open a workspace in, so
they're dropped from the bar and do nothing.

**Workspace naming.** A workspace opened on a *new* worktree is named after the worktree:
Herdr would otherwise list it under its repo by branch, prefix and all. Reopening one leaves
its name alone, so a workspace you've renamed yourself stays renamed.

**Closing workspaces on removal.** Once its checks pass, `ccwt remove` closes the workspace
open on the worktree, ending the agent living in it. Your own workspace is the exception, so
an agent can still `ccwt done` on itself — `ccwt done` is `ccwt remove .` plus closing the
workspace you're sitting in.

**The plugin in `herdr-plugin/`** adds a **New ccwt worktree** action to Herdr — the same
thing the tui's `x` does.

The tui is meant to be parked in a pane — e.g. the main pane of a Herdr workspace — as a live
view of what's running where.

## Using ccwt with Claude Code

Claude Code has a built-in `--worktree` flag that runs a session in its own worktree.
The catch: Claude creates and uses that worktree *internally*, but your terminal tab's
working directory stays at the base project root. Open a new tab or split and you land
back in the base repo, not in the worktree the session is actually using.

`ccwt new` fixes this by creating the worktree *up front*:

1. it prints the worktree name on **stdout** — capture it and pass it to `claude --worktree`;
2. it emits an [OSC 7](https://gitlab.freedesktop.org/terminal-wg/specifications/-/merge_requests/7)
   "current directory" report on **stderr**, which the terminal reads to update its notion
   of the cwd.

Terminals and multiplexers that honour OSC 7 (iTerm2, Ghostty, WezTerm, tmux/cmux, …)
will then open new tabs and splits *in the worktree directory*. Because `ccwt` and Claude
Code share the same `.claude/worktrees/<name>` layout, the name printed by `ccwt new` is
exactly what `claude --worktree` expects.

> This used to be doable with a Claude Code hook that printed the escape code itself, but
> Claude Code no longer lets hooks emit raw escape sequences to the terminal. Emitting it
> from `ccwt new` in a small wrapper around `claude` is the way to get it back.

### Sample wrapper

Save this as `claude-wt` somewhere on your `PATH`, `chmod +x` it, and run it instead of
`claude`. It allocates a fresh worktree per session unless you ask for a specific one:

```bash
#!/usr/bin/env bash
# claude-wt — run each Claude Code session in its own ccwt worktree, and let the
# terminal's cwd follow into it (via the OSC 7 sequence ccwt emits on stderr).
#
#   claude-wt                  # fresh worktree, terminal cd's into it
#   claude-wt --worktree foo   # use the worktree named "foo"
#   claude-wt --no-worktree    # skip worktree handling entirely (wrapper-only flag)
set -eo pipefail

args=()
has_worktree=0
no_worktree=0
for arg in "$@"; do
    case "$arg" in
        --no-worktree)           no_worktree=1 ;;                 # wrapper-only; drop it
        --worktree|--worktree=*)  has_worktree=1; args+=("$arg") ;;
        *)                        args+=("$arg") ;;
    esac
done

if [ "$has_worktree" -eq 0 ] && [ "$no_worktree" -eq 0 ]; then
    # `ccwt new` prints the worktree name on stdout (captured here) and emits the
    # OSC 7 cwd report on stderr, which we deliberately let flow to the terminal.
    worktree=$(ccwt new)
    args=(--worktree "$worktree" "${args[@]}")
fi

exec claude "${args[@]}"
```

Naming the wrapper something other than `claude` (here, `claude-wt`) keeps `exec claude`
from re-invoking the wrapper itself. If you'd rather call it `claude`, point the `exec`
line at the real binary by absolute path instead (e.g. `exec "$HOME/.local/bin/claude"`).

The key detail is that command substitution — `worktree=$(ccwt new)` — captures only
stdout, so the OSC 7 sequence on stderr still reaches the terminal. Don't redirect or
swallow stderr, or you'll lose the cwd report.

## Command reference

<details>
<summary>All commands and flags</summary>

| Command | Description |
| --- | --- |
| `ccwt new [name]` | Create a worktree under `.claude/worktrees/<name>` on a new branch `worktree-<name>`, and print `<name>`. Generates a name if omitted; reuses an existing worktree of the same name. `--switch <branch>` checks the worktree out on an existing branch instead of creating one (`ccwt new` + `git switch <branch>`). When run inside a worktree it returns the enclosing one instead of creating a new one (override with `--force-create`). `--path` prints the worktree's absolute path instead of `<name>`. |
| `ccwt cd <name>` | `cd` into an existing worktree under `.claude/worktrees/<name>` (with shell integration) — never creates it, errors if it doesn't exist, and the name is required. `ccwt cd ..` is shorthand for `ccwt ..`, and `ccwt cd -` jumps to the previous directory (`$OLDPWD`), like the shell's `cd -`. |
| `ccwt list` | List the repo's Claude Code worktrees with branch, age, running-session status, and last commit, sorted newest-first. `-g` lists every project in `$XDG_CONFIG_HOME/ccwt/config.toml` instead, a section per project. `--no-headers` leaves out the header row, for feeding the table to `cut`, `awk` or a shell loop. |
| `ccwt tui` | The default command, so a bare `ccwt` runs it. Show the `ccwt list` table full-screen, refreshing in place without flicker, over a status bar showing how far the current branch is ahead/behind its upstream. `q` (or Ctrl-C) quits, `p` runs `git pull`. Arrow keys (or `j`/`k`) select a worktree, a click on its row selects it, `/` (or `?`) searches the way vim does — incrementally, case-insensitive regexp, all matches highlighted, `n`/`N` for the next and previous while a pattern is in force; `d` shows the selected worktree's column values in full in a pane over the list (`esc` closes it), and `r` removes it, like `ccwt remove`. `l` shows the [worklog](#the-worklog) — the worktrees already removed and what they were about — in a pane of the same kind, whose rows select like the list's; `↵` (or a double-click) opens the selected removal's page, which is what the log recorded about it followed by the whole Claude Code session that ran in it, scrolled with the arrows, `space`/`b` and `g`/`G`. `n` with no pattern in force queues a prompt behind the selected row — work to start once that worktree (or the prompt above it) is finished — typed into a box over the list and drawn as a tree under the row it waits on, kept in `$XDG_STATE_HOME/ccwt/tasks.db` and so shared with every other `ccwt` running; `e` in a queued prompt's details pane rewrites it in place — the box is a line editor, with the arrows, `home`/`end` and Ctrl-W/U/K, and Ctrl-G finishes the prompt in `$EDITOR`, as it does in Claude Code — `r` deletes it and everything queued behind it, and removing the worktree promotes what was queued on it to `<new>` rows — worktrees waiting to be made, which `space` makes and starts the prompt in. `n` with nothing selected queues a prompt that waits on nothing, which is a `<new>` row from the start; under `-g` that takes a project selected, its section header included. `-g` spans the configured projects as a foldable section each (`↵`, or a double-click on the header, folds one shut) and ignores the current directory entirely, every action (`p` included) applying to the selected row's project. `--interval` (default `2s`) sets the refresh rate, `--fetch` (default `1m`) how often `origin/main` is fetched in the background. The bar also says when the `ccwt` binary underneath it has been upgraded, and to which version. Under [Herdr](#herdr-integration-optional) `space` opens the selected worktree as a workspace (double-click does too) and `x` creates one and opens it. |
| `ccwt remove <name>` | Remove the worktree at `.claude/worktrees/<name>` and delete its branch. `.` means the worktree you're currently in; removing the one you're in cds you to the repo root, like `ccwt ..`. The branch is deleted only if merged: an unmerged branch refuses the whole removal, worktree included, so nothing is stranded, and so does a worktree with uncommitted changes, and so does one with an agent working in it (the `✳` of `ccwt list`). Pass `-D` to remove anyway (unmerged branch deleted, uncommitted changes thrown away, working agent interrupted), or `--keep-branch` to remove only the worktree. Under [Herdr](#herdr-integration-optional) the worktree's workspace is closed first, once those checks pass. |
| `ccwt done` | Finish with the worktree you're in: `ccwt remove .`, and then — under [Herdr](#herdr-integration-optional) — close the workspace you're sitting in, the one `remove` leaves alone. Same checks and same flags (`-D`, `--keep-branch`); a refusal leaves the workspace open. Outside Herdr it's just `ccwt remove .`. |
| `ccwt gc` | Remove every worktree that's finished with: branch already merged (the `✓` of `ccwt list`), nothing uncommitted in it (no `*`), no agent working in it (no `✳`) and no Claude Code session running in it (a `no` in the `CLAUDE` column). Prints the list it found and asks before touching anything — `-y`/`--yes` skips the question. Each removal is exactly what `ccwt remove <name>` does, branch included. The worktree you're standing in is never removed — it says so on stderr and leaves it to `ccwt remove .`. |
| `ccwt worklog` | Print the worktrees that have been removed, newest first, with what each one was about — the `TOPIC` its row had, recorded by the removal because nothing else survives it. `-n` (default 20) says how many lines. `-g` covers every project in `$XDG_CONFIG_HOME/ccwt/config.toml` rather than this repo's, with a `PROJECT` column saying which one each removal was in. The log lives in `$XDG_STATE_HOME/ccwt/tasks.db`, alongside the queued prompts, and `l` in the tui shows the same table. |
| `ccwt new-worktree-name` | Print a generated worktree name (`adjective-verb-noun`) without creating anything. |
| `ccwt repo-root` | Print the root of the current git repository. Add `--root-worktree` to print the *enclosing* repo root when you're inside a `.claude/worktrees/<name>` worktree. |
| `ccwt ..` | Shorthand for `repo-root --root-worktree`: print (and, with shell integration, `cd` to) the enclosing repository root. |
| `ccwt init <shell>` | Emit the shell-integration snippet to source from your rc file. For `zsh` the snippet carries completion too: commands, and worktree names for `cd`, `remove` and `lock`. |
| `ccwt config view` / `ccwt config edit` | Print the config file, or open it in `$EDITOR` (`vi` if unset). Both create an empty one if there isn't any. The file holds the `[[projects]]` `-g` spans, `branch_prefix` — what `ccwt new` puts in front of a worktree's name to make its branch (`worktree-` when unset) — `columns`, which columns the table draws, `task_command`, what a queued prompt is run with (`claude` when unset), and `forges`, which cli finds a host's reviews for the tui's `g`. |
| `ccwt --version` | Print version information. |

### Layout

`ccwt` follows Claude Code's convention:

- worktrees live at `<repo-root>/.claude/worktrees/<name>`
- each is checked out on a branch named `worktree-<name>` (`branch_prefix` renames the
  `worktree-` part)

Because the layout matches, worktrees you create with `ccwt` are visible to Claude Code
and vice versa.

</details>

## License

[MIT](LICENSE)
