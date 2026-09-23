# ccwt

A small command-line helper for managing coding-agent git worktrees.

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
doesn't care which agent you run, and `task_command` is where you say which one that is.

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
                    # ... do your work, run your agent, commit, etc. ...

ccwt list           # show all agent worktrees in this repo
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

### Carrying `.env` and friends over

A worktree is a fresh checkout, so the gitignored files your build needs — `.env`,
local credentials, editor settings — aren't in it. List them in a `.worktreeinclude`
file at the repository root and `ccwt new` copies them into every worktree it creates:

```
.env
.env.local
config/secrets.json
```

The syntax is `.gitignore`'s. Only files that *both* match a pattern and are ignored by
git are copied, so a broad pattern can't duplicate a tracked file or drag in a stray
scratch file — and file modes carry over, so a `0600` secret stays `0600`. This is the
same file [Claude Code reads](https://code.claude.com/docs/en/worktrees) when it creates
a worktree, so either tool gives you the same result.

## Listing worktrees

`ccwt list` renders a table of the repo's worktrees with their branch, age, whether an
agent session is currently running in each, and what each one is about:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on the dashboard; it's built and…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
```

Every row leads with two glyphs. The first is a `*` on the worktree you're currently in.
The second says whether the worktree can go: `✓` means its branch is already merged into
`main` (or `master`), so it's safe to `ccwt remove` — a [squash, a rebase or a
cherry-pick](#squash-merges) counts too; `☐` means it isn't merged but every
commit is already on its upstream, so nothing here is waiting on you — it's waiting on a
review or on CI; `*` means it has uncommitted changes, which isn't safe to remove whatever
git makes of the branch or the remote; `✳` beats them all and means an
agent is working in there right now — see [Herdr integration](#herdr-integration-optional).
The glyphs are omitted when stdout isn't a terminal, so piped output stays parseable.

The freshest worktree is on top. Freshness is the younger of two things: the worktree's
last commit, and the last line written to its newest agent session — so a worktree an
agent has been working in all afternoon without committing sorts above one whose commit is
newer but that nobody has touched since, and one an agent started twenty minutes ago sorts
to the top rather than to the bottom on its zero commits. `--sort=commit` is git's answer
on its own, which is what the list did before there was a choice; the default is
[configurable](#several-projects-at-once) and `--sort` overrides it, on both `ccwt list` and
`ccwt tui`.

TOPIC says what the worktree is about, and its glyph says where that came from. `✳` is an
agent session — the newest transcript for that worktree, showing the last recap it produced
(`/recap`, or one the agent wrote on its own), or the first prompt you typed when it never
recapped. `⎇` is the last commit, for a worktree nobody has run a session in.

AGENT and TOPIC's `✳` both read Claude Code's own transcripts, so an agent that files its
sessions somewhere else reads as `no` with its last commit for a topic — the worktree, and
everything else the row says about it, is the same either way.

The table is sized to your terminal, so it never wraps. BRANCH is kept narrow even when
there's room — it's usually the worktree's own name with a `worktree-` in front — and the
space goes to TOPIC. Branches and commit subjects lose their middle rather than their tail,
since the last word is usually what tells them apart, while a session summary simply stops:

```
    NAME                     BRANCH                   AGE  AGENT  TOPIC
* * dreamy-foraging-hickey   worktree-dreamy-…-hickey 2h   yes    ✳ Goal was a widget on…
  ✓ calm-baking-otter        worktree-calm-b…-otter   1d   no     ⎇ Fix the flux ca… (#41)
```

### Squash merges

Squash, rebase and cherry-pick all rewrite commit SHAs, so the branch a PR landed from is
no ancestor of `main` afterwards — which is how most projects merge, and it leaves `ccwt`
refusing to remove worktrees whose work is long since shipped.

If the `git-tree-merged` plugin is on your `PATH`, `ccwt` asks
it instead: it compares tree hashes rather than SHAs, so a branch whose content is on
`main` byte for byte reads as merged however the history was rewritten. Without it `ccwt`
falls back to plain ancestry, which errs the safe way — a missed `✓`, never a wrong one.

### What's running in them

`ccwt ps` answers the other half of that question — not what each worktree is about, but
what is running in it right now, as a process tree per worktree:

```
frolicking-tumbling-duckling
  49757  -zsh
  79860  -zsh
    9935  ↳ claude --worktree frolicking-tumbling-duckling
snug-juggling-dream
  23136  -zsh
  23730  ↳ go test ./...
```

A tree's root is the topmost process whose working directory is in the worktree — the shell
you started there, or a leftover test server nobody stopped — and under it hangs whatever it
spawned, wherever *that* one's working directory has since wandered off to. By default only
the first generation is shown, which is the shell and what it's busy with; `--depth`/`-d`
goes further (`-d 0` for the shells alone, `--depth=-1` for everything). Worktrees with
nothing running in them are left out, and `-g` spans every configured project, a section
per project, as `ccwt list -g` does.

The [tui](#what-is-running-in-there) shows the same thing on `P`, kept up to date, and there
a process is somewhere you can go.

## The tui

`ccwt tui` — or just `ccwt`, which is what a bare invocation runs — shows that same table
full-screen and keeps it up to date, repainting in place so it doesn't flicker, with a
status bar along the bottom:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on the dashboard; it's built and…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor

 ☰  q:quit  p:pull  g:git  /:search  l:log  x:new  space:open  n:queue  m:vcs  d:details  r:remove │ main ↑1     v1.2.3
```

`q` (or Ctrl-C) quits, `p` runs `git pull` and reports the result in the bar. The rest of
the bar is the branch you launched it from and how far it has drifted from its upstream
(`↑` ahead, `↓` behind), and the far corner is the version of the `ccwt` binary drawing it —
dropped on a terminal too narrow to hold both it and the keys. Those counts are kept honest by a background `git fetch` of
`origin/main` alone; `--fetch` (default `1m`) sets how often, or `0` never. `--interval`
(default `2s`) sets the refresh rate.

The arrow keys (or `j`/`k`) select a worktree, and the bar grows the actions that apply to
it: `r` removes it, refusing an unmerged branch, uncommitted changes or a working agent,
exactly like `ccwt remove`. Clicking a row selects it. Three more keys — `x`, `c` and
`space` — appear only under [Herdr](#herdr-integration-optional).

Dragging selects text and copies it. A terminal stops selecting for itself once an
application asks to be told about clicks, so the tui does the selecting: press, drag, let go,
and what the drag covered — from where it started to where it ended, whole lines in between,
as text reads — goes on the clipboard (`pbcopy`, `wl-copy` or `xclip`, and OSC 52 where
there's none of those, so it works over `ssh` too). The selection stays inverted until the
next keystroke, and a press that never moved is still an ordinary click.

The `☰` in the corner is that same list of actions as a menu: click it and they drop out of
the bar, one per line, to be clicked instead of typed (the arrows and `↵` work too, `esc`
closes it, and clicking the `☰` again shuts it). A menu entry is the key it names — picking
one does exactly what typing it does — so the menu and the bar can't drift apart. Two entries
are only there: `G` runs [`ccwt gc`](#command-reference) on the repo in view,
collecting every worktree that's merged, clean and idle in one go — rare enough that the bar
has no room for it, and picking it is the confirmation — and `P` swaps the worktrees for
[what is running in them](#what-is-running-in-there).

`g` shows the history in a window over the list: whatever your own `git ll` alias prints,
run in the worktree the selected row is in — or in the repo itself when the row isn't in
one. It's the same kind of page the worklog opens, and scrolls the same way: the arrows (or
`j`/`k`) by a line, `space`/`b` by a screenful, `g`/`G` to either end, `esc` back to the
list. A git with no `ll` alias of its own has nothing to print, and says so in the bar.

`m` opens the worktree's review in your browser — the branch is here, the review is over
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

### What is running in there

`P` swaps the worktrees for what is running in them — [`ccwt ps`](#whats-running-in-them),
in the tui's own window and refreshed on the same interval, `-g` included when the tui was
started with it:

```
RUNNING
calm-baking-otter
  23136  -zsh
  23730  ↳ go test ./...
dreamy-foraging-hickey
  49757  -zsh
  79860  ↳ claude --resume

 ☰  q:quit  /:search  space:go  esc:worktrees │
```

It's the same list, not a window over one: the arrows walk it, `/` and `n`/`N` search it,
clicking selects, and the rows scroll the way the worktrees do. What changes is what a row
is, and so what there is to do with one — `space` goes to where it is. On a process that
means the pane it's running in, the same place [`ccwt nav ps`](#command-reference) would
take you (herdr or tmux, whichever it's under); on the worktree line above it, the workspace,
as `space` does on the list. `esc` brings the worktrees back — the first `esc` clears the
search, if there is one — and so does another `P`.

A process's pane is an environment variable it inherited, and macOS hides the environment of
everything under `/bin`: a shell can't answer for itself, so its descendants are asked
instead, which is why going to a bare `-zsh` still lands in the right place as long as
something is running under it.

### The worklog

Removing a worktree also removes the only record of what it was for: the branch is deleted,
the directory is gone, and the agent files its transcript under a path that no longer
exists. So `ccwt remove` writes a line about it first — when it went, what it was called,
and the `TOPIC` the list showed — into `$XDG_STATE_HOME/ccwt/tasks.db`, next to the queued
prompts, and every removal goes in: `ccwt remove`, `ccwt done`, `ccwt gc`, and `r` in the
tui.

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
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
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
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on…
    ↳ <queued>                                        4m          rebase onto main and open the PR
      ↳ <queued>                                      3m          then port the widget to the mobile layout
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
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

The box is a line editor, since the word that needs changing is rarely the last one, and its
keys are the ones the zsh line editor and Claude Code's own box use: `←` and `→` move the
caret, Ctrl-`←`/`→` (or Alt-B/F) move it a word at a time, `↑` and `↓` (or Ctrl-P/N) move it
a line at a time, `home` and `end` (or Ctrl-A and Ctrl-E) go to either end of the line,
`backspace` and `delete` take the character on their own side of it, and Ctrl-W, Alt-D,
Ctrl-U and Ctrl-K rub out the word before the caret, the word after it, the rest of the line
before it and the rest of the line after it. Text goes in where the caret is, and the box
scrolls to wherever that is, so the start of a prompt taller than the box is reachable.

Shift-`↵` puts a line break in — `↵` on its own records the prompt — so a prompt can be a
paragraph rather than a sentence. Only `↵` and `esc` close the box: a key it doesn't know,
whatever your terminal sends for Cmd-`←`, does nothing at all rather than throwing away what
you have typed. (A terminal has to be told to send something for shift-`↵`, since by default
it sends the same thing `↵` does. Claude Code's own `/terminal-setup` does that for iTerm2
and VS Code, and this box takes what it binds; Ctrl-J is the line break everywhere else.)
The table folds the breaks back to spaces for the one row it has to draw the prompt on.

For a prompt that wants more than that, Ctrl-G finishes it in `$EDITOR` — the same key
Claude Code's own prompt box binds it to, since it's the same box for the same purpose. The
tui hands over the screen and the keyboard, opens `$VISUAL`, `$EDITOR` or `vi` on what
you've typed so far, and takes back whatever you save, with the list drawn again around it.
An editor that quits without saving leaves the prompt as it was.

`n` is also vim's next-match key, and while a search pattern is in force that's what it
stays: `/`, then `n` and `N` to walk the matches, and `esc` to clear the pattern and get the
queue key back. The bar says which one is live.

Removing the worktree does *not* take the chain with it — it starts it. Every prompt that
was waiting on that worktree moves up to a row of its own, called `<new>` because its turn
has come and it hasn't got a worktree yet, with whatever was queued behind it still under
it:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
    <new>                                             2h          rebase onto main and open the PR
    ↳ <queued>                                        2h          then port the widget to the mobile layout
```

`space` (or a double-click) on a `<new>` row is where the worktree finally gets made: a
fresh one, opened as its own [Herdr](#herdr-integration-optional) workspace with the prompt
running in it — `claude "<the prompt>"`, or whatever `task_command` in the config says, e.g.

```toml
task_command = "claude --permission-mode plan"
```

The prompt itself travels in a file — the pane is handed `claude "$(cat …)"`, and the file
deletes itself as it is read. That command is typed at the pane's shell prompt, and a
terminal takes about a kilobyte of a typed line before it drops the rest, which a prompt
worth queueing is easily longer than; a filename is the same hundred characters however long
the prompt is, and the shell never has to be taught to read back the quotes, newlines and
backslashes in it.

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
alike, so `/✓` finds the worktrees whose branch is merged and `/yes` the ones with an agent
running in them. One that doesn't compile simply matches nothing, which is what half of one
is while you're still typing it.

Something parked in a pane for days goes on running the binary it started from, so the tui
watches that binary and says so when it's replaced — by `go install`, by a package manager,
by a downloaded release:

```
 ☰  q:quit  p:pull  /:search │ upgraded to v0.64.0 — restarting when idle
```

Then it does: once the keys have been quiet for four seconds, with nothing typed and no pane
open, the process replaces itself with the new binary — an `exec`, so it keeps its pid, its
pane and its flags — and the list comes back fresh; the selection and any pattern in force
are what doesn't survive it. The version is the one you're restarting into (asked of the new
binary itself; it's dropped from the line if it won't answer). Windows has no `exec`, so
there the notice reads `restart ccwt` and stays up until you do.

## Several projects at once

`-g` makes `ccwt list` and `ccwt tui` span every project you've configured instead of just
the repo you're standing in, a section per project, each holding that project's worktrees
newest-first:

```
    NAME                      BRANCH                    AGE  AGENT  TOPIC
▾ ccwt (2)
  * dreamy-foraging-hickey    worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on…
  ✓ calm-baking-otter         worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
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
— `name`, `branch`, `age`, `agent`, `topic`, all of them when unset:

```toml
columns = ["name", "age", "topic"]
```

The `*`/`✓`/`☐`/`✳` markers ride on `name`, so leaving it out leaves them out too.

`sort` is the order `ccwt list` and `ccwt tui` put the worktrees in — `freshness` when
unset, the last commit or the last thing written to the newest session there, whichever is
younger, or `commit` for git's answer alone. `--sort` overrides it for one run:

```toml
sort = "commit"
```

`task_command` is the agent cli — which agent `ccwt` starts for you, and the one place that
choice lives: what a [queued prompt](#queued-prompts) is handed to when its worktree is made,
and what the tui's [`c`](#herdr-integration-optional) runs on its own. It is `claude` when
unset, and split on spaces so flags get through:

```toml
task_command = "claude --permission-mode plan"
```

`forges` says which cli knows about a host's reviews, for the tui's [`m`](#the-tui) key.
Only a host whose name doesn't give it away needs a line — a self-hosted GitLab most of
all, since `github.com`, `gitlab.com` and anything with `gitlab` in the name are guessed:

```toml
[forges]
"code.example.com" = "glab"
```

`environments` renames a GitLab environment for [`ccwt mr`](#command-reference)'s `ENV`
column: the name GitLab knows it by on the left, what to call it on the right. Deployment
pipelines name environments to be unambiguous in a settings page, which is longer than a
column you only glance at wants:

```toml
[environments]
production = "prod"
staging = "stg"
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
It comes from Herdr rather than from the agent's own files, so it holds for whatever agent is
running there — unlike the `AGENT` column, which knows Claude Code alone. `ccwt remove`,
`ccwt done` and `ccwt gc` all refuse a worktree marked this way (`ccwt remove -D` interrupts
the agent anyway).

**Opening workspaces from the tui.** `space` opens the selected worktree as its own Herdr
workspace (via `herdr worktree open`), and a double-click on its row does the same. `x`
creates a worktree and opens it without leaving the list, and `c` does that and starts
`ccwt ws` in it — the workspace's own tui, below, which is where its agents are seeded
from. These keys only exist when the tui
is itself running in a Herdr pane — elsewhere there's no session to open a workspace in, so
they're dropped from the bar and do nothing.

**Workspace naming.** A workspace opened on a *new* worktree is named after the worktree:
Herdr would otherwise list it under its repo by branch, prefix and all. Reopening one leaves
its name alone, so a workspace you've renamed yourself stays renamed. And the name is what
`NAME` shows, in `ccwt list` and the tui alike, when they run in a Herdr pane: a worktree open
as a workspace goes by the workspace's name, so the list reads like Herdr's sidebar. Piped
output keeps the worktree's own name, which is the one `ccwt remove` takes.

**A workspace's own tui: `ccwt ws`.** The tui above is the repo's view: its worktrees,
whatever workspace each is open in. `ccwt ws` is a workspace's, in two sections: where the
workspace's review stands, and under it the tabs working towards it.

```
  MR              STATUS          PIPELINE            TITLE
  acme/…/api!42   needs approval  failed: unit, lint  Wire the widget to real numbers

  TAB  AGENT  DIR                     TITLE
* 1    ·      dreamy-foraging-hickey  zsh
  2    ●      dreamy-foraging-hickey  Wiring the widget to real numbers
  3    ○      dreamy-foraging-hickey  Waiting on your review

 ☰  q:quit  /:search  n:agent  space:go  g:git │ started
```

The first section is what [`ccwt mr`](#the-commands) prints for the branch the workspace is
working on — can it go in, and did its pipeline pass — which is the question the whole
workspace exists to get a yes to. A fresh worktree hasn't got one yet, and says so in a line
rather than in a row of column names with nothing under it:

```
  no merge request yet
```

The lookup is a handful of GitLab round trips, so it runs in the background and is asked
again at most once a minute: the table you're reading never waits on the network, and the row
appears under the heading a moment after the review does.

The second section is the workspace's tabs — the tab's label, whether Herdr sees an agent
working in it, the directory it sits in, and what its terminal calls itself, which for an
agent is its own one-line account of what it is doing — and `space` (or `↵`, or a
double-click) goes to the selected tab. A `*` leads the tab the tui itself is in. The rows
come in the order the tabs sit along the tab bar, so dragging one about in Herdr moves its
row with it. They're the only rows the selection walks: the review above them is something to
read, and it stays put while they scroll.

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

`n` opens the same box the [queue](#queued-prompts) types into, and what goes in it is a seed
prompt: `↵` opens a tab of the workspace in the workspace's own worktree — the one the tui is
standing in — and runs `task_command` there with the prompt. No worktree and no tab name of
its own: the agents of one workspace are working on one thing, in one checkout, and an
unnamed tab is named by what runs in it, which is the agent saying what it's doing. The tab
opens behind the one you're typing in, so the table shows it and you go when you're ready. A workspace with no
other tab yet opens straight on that box, since the first thing to do in a fresh workspace is
say what it's for; `esc` puts it away. `g` shows the history of the worktree under the
selected tab, and `/`, the mouse and the `☰` are the tui's own.

When the agents are Claude Code — `task_command` is `claude`, or unset — and Claude Code
hasn't been told to trust the repository yet, `ccwt ws` asks that first, over everything
else, the seed prompt included: an agent started in a tab nobody is looking at would
otherwise sit at Claude Code's own trust dialog with your prompt waiting behind it. `y`
records the answer where that dialog would, in `~/.claude.json` (or `$CLAUDE_CONFIG_DIR`'s),
under Claude Code's own lock, and on the repository's main checkout — which is what Claude
Code files it under, so one yes covers every worktree of it. `n` leaves Claude Code to ask
for itself. The home directory is never asked about: Claude Code only ever trusts that one
for a session.

`r` is the other end of that: [`ccwt done`](#the-commands) for the workspace itself — the
worktree the tui is standing in is removed, branch and all, and the workspace closes behind
it, this tui with it. It's on the bar only when that removal would go through as it stands:
branch merged, nothing uncommitted, and no agent still working in there — the three checks
`ccwt remove` makes before `-D`, asked of the very same code, so `r` on the bar means this
workspace is finished with. Whether it's there is a property of the workspace and not of any
one tab, so it doesn't come and go as you walk the table: it's on the bar or it isn't,
whichever row is selected. A tui run somewhere that isn't a worktree — the repo itself —
never has one.

This is part of a longer plan — what each tab is about, and which tickets and reviews each
one touches — and `TODO.md` has the rest of it.

**Closing workspaces on removal.** Once its checks pass, `ccwt remove` closes the workspace
open on the worktree, ending the agent living in it — including your own, which is closed
last of all, after the worktree is gone and the shell has been cd'd out. Because a workspace
vanishing is a bigger surprise than a directory, a terminal is asked first: `remove <name>
and close its herdr workspace? [y/N]` on stderr, answered "no" by default. `-y`/`--yes` skips
the question, and so does a `ccwt remove` whose stderr isn't a terminal — a script or the tui
gets on with it. `ccwt done` is `ccwt remove . -y`. Closing the workspace you're looking at
would leave Herdr focused on whatever it had open before — some unrelated pane — so when
yours is the one on screen, the repo's own workspace, the one open on the root checkout, is
focused first: you land back in the repo the worktree came from. Switch to another workspace
while the removal runs and nothing moves — closing a workspace nobody is looking at takes no
focus with it, and where you switched to is where you meant to be.

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
| `ccwt list` | List the repo's agent worktrees with branch, age, running-session status, and last commit, freshest first — the last commit or the last thing written to the newest agent session there, whichever is younger. `--sort=commit` orders by the commit alone, `--sort=freshness` is the default, and `sort` in the config file picks which one you get without the flag. `-g` lists every project in `$XDG_CONFIG_HOME/ccwt/config.toml` instead, a section per project. `--no-headers` leaves out the header row, for feeding the table to `cut`, `awk` or a shell loop. |
| `ccwt tui` | The default command, so a bare `ccwt` runs it. Show the `ccwt list` table full-screen, refreshing in place without flicker, over a status bar showing how far the current branch is ahead/behind its upstream, with the `ccwt` version in its far corner. `q` (or Ctrl-C) quits, `p` runs `git pull`. Arrow keys (or `j`/`k`) select a worktree, a click on its row selects it, dragging selects text and copies it to the clipboard, the `☰` in the bar's corner drops the bar's actions out as a clickable menu, `/` (or `?`) searches the way vim does — incrementally, case-insensitive regexp, all matches highlighted, `n`/`N` for the next and previous while a pattern is in force; `d` shows the selected worktree's column values in full in a pane over the list (`esc` closes it), `g` shows the history your own `git ll` alias prints, in a scrolling page over the list, `m` opens the branch's review in the browser, and `r` removes it, like `ccwt remove`. `l` shows the [worklog](#the-worklog) — the worktrees already removed and what they were about — in a pane of the same kind, whose rows select like the list's; `↵` (or a double-click) opens the selected removal's page, which is what the log recorded about it followed by the whole agent session that ran in it, scrolled with the arrows, `space`/`b` and `g`/`G`. `n` with no pattern in force queues a prompt behind the selected row — work to start once that worktree (or the prompt above it) is finished — typed into a box over the list and drawn as a tree under the row it waits on, kept in `$XDG_STATE_HOME/ccwt/tasks.db` and so shared with every other `ccwt` running; `e` in a queued prompt's details pane rewrites it in place — the box is a line editor, with the arrows, `home`/`end` and Ctrl-W/U/K, shift-`↵` for a line break, and Ctrl-G to finish the prompt in `$EDITOR`, as in Claude Code — `r` deletes it and everything queued behind it, and removing the worktree promotes what was queued on it to `<new>` rows — worktrees waiting to be made, which `space` makes and starts the prompt in. `n` with nothing selected queues a prompt that waits on nothing, which is a `<new>` row from the start; under `-g` that takes a project selected, its section header included. `-g` spans the configured projects as a foldable section each (`↵`, or a double-click on the header, folds one shut) and ignores the current directory entirely, every action (`p` included) applying to the selected row's project. `--interval` (default `2s`) sets the refresh rate, `--fetch` (default `1m`) how often `origin/main` is fetched in the background, and `--sort` orders the worktrees, as it does on `ccwt list`. The bar also says when the `ccwt` binary underneath it has been upgraded, and to which version — and once the keys have been quiet for four seconds the tui restarts itself into it. Under [Herdr](#herdr-integration-optional) `space` opens the selected worktree as a workspace (double-click does too), `x` creates one and opens it, and `c` also starts `ccwt ws`, the workspace's own tui, in it. |
| `ccwt ws` | The tui for the first tab of a [Herdr](#herdr-integration-optional) workspace, in two sections. First the workspace's own review, as `ccwt mr` prints it for the branch the tui is standing on — one line saying there isn't one while the branch has none, and looked up in the background (at most once a minute) so the table never waits on GitLab. Then that workspace's tabs as a table — label, agent status, directory, terminal title — with `space`/`↵` (or a double-click) going to the selected tab, `n` asking for a seed prompt that starts an agent in a tab of its own (`task_command` run with the prompt in the workspace's own worktree, the tab unnamed and opened behind the current one), `g` the history of the worktree under the tab, `r` being `ccwt done` for the workspace itself — the worktree the tui is standing in removed and the workspace closed behind it, offered only when that removal would go through (merged branch, nothing uncommitted, no agent still working there), and so on the bar or not whichever row is selected — and `/` searching as in the tui. Opens on the prompt when the workspace has no other tab — and before that, when the agents are Claude Code and it doesn't trust the repository yet, on whether it may: `y` records the trust in Claude Code's own config, as its dialog would. `--interval` (default `2s`) sets the refresh rate. Refuses to run outside Herdr. |
| `ccwt remove <name>` | Remove the worktree at `.claude/worktrees/<name>` and delete its branch. `.` means the worktree you're currently in; removing the one you're in cds you to the repo root, like `ccwt ..`. The branch is deleted only if merged: an unmerged branch refuses the whole removal, worktree included, so nothing is stranded, and so does a worktree with uncommitted changes, and so does one with an agent working in it (the `✳` of `ccwt list`). Pass `-D` to remove anyway (unmerged branch deleted, uncommitted changes thrown away, working agent interrupted), or `--keep-branch` to remove only the worktree. Under [Herdr](#herdr-integration-optional) the worktree's workspace is closed too, once those checks pass — your own one last, after the removal, with focus handed to the repo's own workspace first if yours is the workspace on screen — and a terminal is asked before that happens (`-y`/`--yes` skips the question). |
| `ccwt done` | Finish with the worktree you're in: `ccwt remove . -y` — the removal, and then, under [Herdr](#herdr-integration-optional), closing the workspace you're sitting in, without asking (closing it is what "done" means). Same checks and same flags (`-D`, `--keep-branch`); a refusal leaves the workspace open. Outside Herdr it's just `ccwt remove .`. |
| `ccwt gc` | Remove every worktree that's finished with: branch already merged (the `✓` of `ccwt list`), nothing uncommitted in it (no `*`), no agent working in it (no `✳`) and no agent session running in it (a `no` in the `AGENT` column). Prints the list it found and asks before touching anything — `-y`/`--yes` skips the question. Each removal is exactly what `ccwt remove <name>` does, branch included. The worktree you're standing in is never removed — it says so on stderr and leaves it to `ccwt remove .`. |
| `ccwt worklog` | Print the worktrees that have been removed, newest first, with what each one was about — the `TOPIC` its row had, recorded by the removal because nothing else survives it. `-n` (default 20) says how many lines. `-g` covers every project in `$XDG_CONFIG_HOME/ccwt/config.toml` rather than this repo's, with a `PROJECT` column saying which one each removal was in. The log lives in `$XDG_STATE_HOME/ccwt/tasks.db`, alongside the queued prompts, and `l` in the tui shows the same table. |
| `ccwt ps` | Show what is running in each worktree, [a process tree per worktree](#whats-running-in-them): every process whose working directory is in the worktree while its parent's isn't — the shell you started there, or something orphaned that outlived it — with what each one spawned underneath. `--depth`/`-d` (default 1) says how many generations below those roots to draw: `0` for the roots alone, `--depth=-1` for the whole tree (spelled with an `=`, since a bare `-1` reads as a flag). Worktrees with nothing running in them are left out. `-g` covers every project in `$XDG_CONFIG_HOME/ccwt/config.toml`, a section per project, like `ccwt list -g`. The working directories come from `lsof`, so a process whose cwd can't be read simply isn't placed in a worktree. |
| `ccwt mr <thing>` | Say where a review stands and what its pipeline did, without opening a browser: a table with a row per merge request, saying where each one `STATUS` stands — `merged`, `can be merged`, `conflict`, `needs approval`, `open comments`, `merge train` for one queued on a merge train or waiting on a pipeline to join it — GitLab itself calls that `ci still running`, which reads as a pipeline to go and wait on when it's the train's own and there is nothing to do about it — or whatever else GitLab's own mergeability check says, spelled out (`need rebase`, `ci still running`) — what its `PIPELINE` did: `green`, `running`, or `failed: lint, test, …`, naming the first few failed jobs; and the `ENV` it is running on — the environments whose current deployment carries the commit this merge request landed as (`stg`, `stg, prod`), which is empty until it's merged *and* released. `[environments]` in the config file renames them for the column (`production = "prod"`). `<thing>` is either a merge request url, which gets a table of the one row, or a Jira key (`PROJ-1234`) or its browse url, which gets a row per merge request that names the ticket, with the title to tell them apart. Left out entirely it's the merge request of the branch you're standing on; `-` reads a list of things off stdin, one per line, and puts all of their merge requests in the one table (a merge request that answers to two of them is still one row). On a terminal the `MR` column is a link to the merge request, a `merged` status is green — the one word either table spends colour on, so that the colour still means something when it turns up — and a spinner on stderr says the lookups are still going; piped or redirected, the table comes out as markdown — a rule under the header, the `MR` column as a markdown link, and nothing shortened to fit a width nothing has — so it lands in a merge request comment, a ticket or a chat message as a table rather than as columns that only line up in a monospace font. A merge request already `merged` or `closed` has no pipeline reported — it's history, and there's nothing to do about it either way — when none of the rows has one the `PIPELINE` column isn't drawn at all. `ENV` is always drawn, empty for a merge request that isn't running anywhere. `--no-gitlab-environments` (it's on by default) fills `ENV` with `??` instead and sends none of those lookups — a deployments call per project and a merge-base call per environment — for when the answer isn't worth the round trips; `??` says nobody asked, which isn't what an empty cell says. It's read off the project's deployments (newest first, so the first mention of an environment is what's on it now) and answered by asking GitLab whether the environment's commit has the merge request's in its history — "on it now", rather than GitLab's own MR badge, which means "reached it at least once" and survives a rollback. It asks [`glab`](https://gitlab.com/gitlab-org/cli), which already knows the host and the token; the ticket's merge requests come from GitLab's own search for the key, so one that mentions the ticket only in a commit message or a comment isn't found. |
| `ccwt new-worktree-name` | Print a generated worktree name (`adjective-verb-noun`) without creating anything. |
| `ccwt nav ps <pattern>`<br>`ccwt nav ws <pattern>` | Go to where something is. `ps` searches your running processes by command line and puts you in the pane the best match is running in; `ws` searches [Herdr](#herdr-integration-optional) workspaces by title and by the name of the worktree each has open, and goes to the best match. Both patterns are fuzzy — the characters in order, not necessarily adjacent, case ignored, so `ccwt nav ws mtls` finds the workspace titled "mTLS subresource" and `ccwt nav ws duckling` the one holding `frolicking-tumbling-duckling`; of everything a pattern matches, the tightest run wins and the earlier one breaks the tie. `ps` finds the pane by reading the variable a multiplexer exports into it (`HERDR_PANE_ID`, `TMUX_PANE`) off the process itself rather than by asking the multiplexer, so Herdr and tmux both answer; it never matches `ccwt` itself or anything that spawned it, the pane you're already in being nowhere to navigate to. macOS hides the environment of system binaries, so a bare shell can't be found this way — search for the agent or the program running in the pane instead. `--dry-run` goes nowhere and prints where it would have gone instead, three lines: the workspace's name, the tab's name, and the tab's address — `w16:t2`, what `herdr tab focus` takes (under tmux the same three are the session, the window and the pane id). |
| `ccwt repo-root` | Print the root of the current git repository. Add `--root-worktree` to print the *enclosing* repo root when you're inside a `.claude/worktrees/<name>` worktree. |
| `ccwt ..` | Shorthand for `repo-root --root-worktree`: print (and, with shell integration, `cd` to) the enclosing repository root. |
| `ccwt init <shell>` | Emit the shell-integration snippet to source from your rc file. For `zsh` the snippet carries completion too: commands, and worktree names for `cd`, `remove` and `lock`. |
| `ccwt config view` / `ccwt config edit` | Print the config file, or open it in `$EDITOR` (`vi` if unset). Both create an empty one if there isn't any. The file holds the `[[projects]]` `-g` spans, `branch_prefix` — what `ccwt new` puts in front of a worktree's name to make its branch (`worktree-` when unset) — `columns`, which columns the table draws, `sort`, the order the worktrees come in (`freshness` when unset), `task_command`, the agent cli a queued prompt is run with (`claude` when unset), `forges`, which cli finds a host's reviews for the tui's `m`, and `environments`, what `ccwt mr`'s `ENV` column calls each GitLab environment (`production = "prod"`). |
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
