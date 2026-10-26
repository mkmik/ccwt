# Commands

Every command and flag, in one place. The chapters before this one say the same things at
more length and with examples.

## `ccwt new [name]`

Create a worktree under `.claude/worktrees/<name>` on a new branch `worktree-<name>`, and
print `<name>`. Generates a name if omitted; reuses an existing worktree of the same name.
`--switch <branch>` checks the worktree out on an existing branch instead of creating one
(`ccwt new` + `git switch <branch>`). When run inside a worktree it returns the enclosing one
instead of creating a new one (override with `--force-create`). `--path` prints the
worktree's absolute path instead of `<name>`. See [Creating worktrees](../worktrees/creating.md).

## `ccwt cd <name>`

`cd` into an existing worktree under `.claude/worktrees/<name>` (with
[shell integration](../getting-started.md#shell-integration-recommended)) — never creates
it, errors if it doesn't exist, and the name is required. `ccwt cd ..` is shorthand for
`ccwt ..`, and `ccwt cd -` jumps to the previous directory (`$OLDPWD`), like the shell's
`cd -`.

## `ccwt list`

List the repo's agent worktrees with branch, age, running-session status, and last commit,
freshest first — the last commit or the last thing written to the newest agent session
there, whichever is younger. `--sort=commit` orders by the commit alone, `--sort=freshness`
is the default, and `sort` in the config file picks which one you get without the flag. `-g`
lists every known project instead, a section per project. `--no-headers` leaves out the
header row, for feeding the table to `cut`, `awk` or a shell loop. `ccwt ls` is the same
command. See
[Listing worktrees](../worktrees/listing.md).

## `ccwt tui`

The default command, so a bare `ccwt` runs it. Show the `ccwt list` table full-screen,
refreshing in place without flicker, over a status bar showing how far the current branch is
ahead/behind its upstream, with the `ccwt` version in its far corner. See
[The tui](../tui/index.md).

`q` (or Ctrl-C) quits, `p` runs `git pull` (a fast-forward to what was already fetched,
while the branch is behind it). Arrow keys (or `j`/`k`) select a worktree, a click on its row
selects it, dragging selects text and copies it to the clipboard, the `☰` in the bar's corner
drops the bar's actions out as a clickable menu, `/` (or `?`) searches the way vim does —
incrementally, case-insensitive regexp, all matches highlighted, `n`/`N` for the next and
previous while a pattern is in force; `d` shows the selected worktree's column values in full
in a pane over the list (`esc` closes it), `g` shows the history your own `git ll` alias
prints, in a scrolling page over the list, `m` opens the branch's review in the browser, and
`r` removes it, like `ccwt remove`. `a` narrows the list to the worktrees an agent is running
in — one herdr has, idle or not, or a Claude Code session — and `a` again brings the rest
back.

`l` shows the [worklog](../tui/worklog.md) — the worktrees already removed and what they were
about — in a pane of the same kind, whose rows select like the list's; `↵` (or a
double-click) opens the selected removal's page, which is what the log recorded about it
followed by the whole agent session that ran in it, scrolled with the arrows, `space`/`b` and
`g`/`G`.

`n` with no pattern in force [queues a prompt](../tui/queue.md) behind the selected row —
work to start once that worktree (or the prompt above it) is finished — typed into a box over
the list and drawn as a tree under the row it waits on, kept in
`$XDG_STATE_HOME/ccwt/tasks.db` and so shared with every other `ccwt` running; `e` in a
queued prompt's details pane rewrites it in place — the box is a line editor, with the
arrows, `home`/`end` and Ctrl-W/U/K, shift-`↵` for a line break, and Ctrl-G to finish the
prompt in `$EDITOR`, as in Claude Code — "reorder" in the `☰` menu moves it with `hjkl`
(among its siblings, out from under its parent, under the one above), `r` deletes it and
everything queued behind it, and
removing the worktree promotes what was queued on it to `<new>` rows — worktrees waiting to
be made, which `space` makes and starts the prompt in. `n` with nothing selected queues a
prompt that waits on nothing, which is a `<new>` row from the start; under `-g` that takes a
project selected, its section header included. `T` lists `todography next`, and `c` on an
item opens that box on its `t8y:` url.

`-g` spans the [known projects](../projects.md) as a foldable section each (`↵`, or a
double-click on the header, folds one shut) and ignores the current directory entirely, every
action (`p` included) applying to the selected row's project. `--interval` (default `2s`)
sets the refresh rate, `--fetch` (default `1m`) how often `origin/main` is fetched in the
background, and `--sort` orders the worktrees, as it does on `ccwt list`. The bar also says
when the `ccwt` binary underneath it has been [upgraded](../tui/restart.md), and to which
version — and once the keys have been quiet for four seconds the tui restarts itself into
it.

Under [Herdr](../herdr/index.md) `space` opens the selected worktree as a workspace
(double-click does too), `x` creates one and opens it, and `c` also starts `ccwt ws`, the
workspace's own tui, in it; each row leads with Herdr's dot for the agents in its worktree,
as `ccwt ws` draws one for a tab; a box in the bottom-right corner names those of its
worktrees' workspaces whose `ws` tab has no `ccwt` running in it.

## `ccwt ws`

The tui for the first tab of a [Herdr](../herdr/index.md) workspace, in two sections. See
[A workspace's own tui](../herdr/ws.md).

First the workspace's own review, as `ccwt mr` prints it for the branch the tui is standing
on — one line saying there isn't one while the branch has none, and looked up in the
background (at most once a minute) so the table never waits on GitLab or GitHub.

Then that workspace's tabs as a table — label, agent status, directory, terminal title — with
`space`/`↵` (or a double-click) going to the selected tab, `c` asking for a seed prompt that
starts an agent in a tab of its own (`task_command` run with the prompt in the workspace's own
worktree, the tab unnamed and opened behind the current one), `g` the history of the worktree
under the tab, `r` being `ccwt done` for the workspace itself — the worktree the tui is
standing in removed and the workspace closed behind it, offered only when that removal would
go through (merged branch, nothing uncommitted, no agent still working there), and so on the
bar or not whichever row is selected — and `/` searching as in the tui.

Opens on the prompt when the workspace has no other tab — and before that, when the agents
are Claude Code and it doesn't trust the repository yet, on whether it may: `y` records the
trust in Claude Code's own config, as its dialog would. `--interval` (default `2s`) sets the
refresh rate. Refuses to run outside Herdr.

## `ccwt remove <name>`

Remove the worktree at `.claude/worktrees/<name>` and delete its branch, refusing an unmerged
branch, uncommitted changes or a working agent unless given `-D`. `--keep-branch` removes
only the worktree, `-y`/`--yes` skips the question about closing its Herdr workspace. See
[Removing worktrees](../worktrees/removing.md#ccwt-remove-name).

## `ccwt done`

`ccwt remove . -y`: remove the worktree you're in and, under Herdr, close the workspace
you're sitting in. See [Removing worktrees](../worktrees/removing.md#ccwt-done).

## `ccwt gc`

Remove every worktree that's merged, clean and idle, after asking (`-y`/`--yes` skips the
question). See [Removing worktrees](../worktrees/removing.md#ccwt-gc).

## `ccwt lock <name>`

Lock a worktree the way `ccwt new` does, so `git worktree prune` can't reclaim it while its
directory is unavailable. `.` means the worktree you're currently in.

## `ccwt worklog`

Print the worktrees that have been removed, newest first, with what each one was about — the
`TOPIC` its row had, recorded by the removal because nothing else survives it — and the url
of its merge request or pull request, for `ccwt mr` to say where it has been promoted to
since. `-n` (default 20) says how many lines. `-g` covers every known project rather than
this repo's, with a `PROJECT` column saying which one each removal was in. The log lives in
`$XDG_STATE_HOME/ccwt/tasks.db`, alongside the queued prompts, and `l` in the tui shows the
same table. See [The worklog](../tui/worklog.md).

## `ccwt ps`

Show what is running in each worktree, [a process tree per worktree](../worktrees/ps.md):
every process whose working directory is in the worktree while its parent's isn't — the
shell you started there, or something orphaned that outlived it — with what each one spawned
underneath. `--depth`/`-d` (default 1) says how many generations below those roots to draw:
`0` for the roots alone, `--depth=-1` for the whole tree (spelled with an `=`, since a bare
`-1` reads as a flag). Worktrees with nothing running in them are left out. `-g` covers every
known project, a section per project, like `ccwt list -g`. The working directories come from
`lsof`, so a process whose cwd can't be read simply isn't placed in a worktree.

## `ccwt mr <thing>`

Also `ccwt pr <thing>`. Say where a review stands and what its pipeline did, without opening
a browser: `STATUS`, `APPROVED`, `PIPELINE` and `ENV` for a merge request or pull request
url, for every review that names a Jira key, or, with no argument, for the branch you're
standing on; `-` reads a list of those off stdin. Piped, the table comes out as markdown.
`--no-environments` skips the `ENV` lookups. See [Where a review stands](../worktrees/mr.md).

## `ccwt open`

Bring up the project picker `o` opens in `ccwt -g`, from anywhere. Under
[Herdr](../herdr/index.md), with the [plugin](../herdr/index.md#the-plugin) linked, it is a
popup over the pane you're in: `esc` closes it and leaves you there, a pick goes to the
project's workspace. Otherwise it goes to the pane `ccwt -g` is running in and opens the picker
there. Whatever the tui had
up is closed first (a search is given up as `esc` would), unless it's a prompt being typed,
which is left alone. The `-g` tui writes its pid to `$XDG_STATE_HOME/ccwt/global.pid` and
`ccwt open` sends it `SIGUSR1`; with more than one running, the last one started is the one
reached. Not on Windows. See [Opening a project](../projects.md#opening-a-project).

`ccwt open --here` is the picker on its own, in the terminal it is run in: it is what runs in
the popup.

## `ccwt new-worktree-name`

Print a generated worktree name (`adjective-verb-noun`) without creating anything.

## `ccwt nav`

```sh
ccwt nav ps <pattern>
ccwt nav ws <pattern>
```

Go to where something is. `ps` searches your running processes by command line and puts you
in the pane the best match is running in; `ws` searches [Herdr](../herdr/index.md) workspaces
by title and by the name of the worktree each has open, and goes to the best match.

Both patterns are fuzzy — the characters in order, not necessarily adjacent, case ignored, so
`ccwt nav ws mtls` finds the workspace titled "mTLS subresource" and `ccwt nav ws duckling`
the one holding `frolicking-tumbling-duckling`; of everything a pattern matches, the tightest
run wins and the earlier one breaks the tie.

`ps` finds the pane by reading the variable a multiplexer exports into it (`HERDR_PANE_ID`,
`TMUX_PANE`) off the process itself rather than by asking the multiplexer, so Herdr and tmux
both answer; it never matches `ccwt` itself or anything that spawned it, the pane you're
already in being nowhere to navigate to. macOS hides the environment of system binaries, so a
bare shell can't be found this way — search for the agent or the program running in the pane
instead.

`--dry-run` goes nowhere and prints where it would have gone instead, three lines: the
workspace's name, the tab's name, and the tab's address — `w16:t2`, what `herdr tab focus`
takes (under tmux the same three are the session, the window and the pane id).

## `ccwt pause`, `ccwt unpause`

Stop every `ccwt` from asking [Herdr](../herdr/index.md) for status on its own, and start it
again: a way to see whether Herdr is slow because of them. Paused, the tui and `ccwt ws` go
on showing what Herdr last said rather than asking every round (`ccwt list`, with nothing
heard before, leaves Herdr's names and `✳` out), the workspace badge isn't sent, and the
corner box stops watching the `ws` tabs; the bar says `herdr paused`. What a key or a command
does still reaches Herdr — workspaces and tabs are made, opened, focused and closed as ever,
and a removal still asks whether an agent is working. The pause is a file,
`$XDG_STATE_HOME/ccwt/paused`, so it holds for every `ccwt` running and for the ones started
after.

## `ccwt restart`

Have every running tui and `ccwt ws` restart itself the way an
[upgrade](../tui/restart.md) does — once its keys have been quiet for four seconds, in the
same pane with the same flags — to pick up config only read at startup, such as
`herdr_events`. It writes `$XDG_STATE_HOME/ccwt/restart`, which each one compares with what
it said when it started.

## `ccwt repo-root`

Print the root of the current git repository. Add `--root-worktree` to print the
*enclosing* repo root when you're inside a `.claude/worktrees/<name>` worktree.

## `ccwt ..`

Shorthand for `repo-root --root-worktree`: print (and, with shell integration, `cd` to) the
enclosing repository root.

## `ccwt init <shell>`

Emit the [shell-integration](../getting-started.md#shell-integration-recommended) snippet to
source from your rc file: `zsh`, `bash` or `fish`. For `zsh` the snippet carries completion too: commands, and
worktree names for `cd`, `remove` and `lock`.

## `ccwt config view`, `ccwt config edit`

Print the config file, or open it in `$EDITOR` (`vi` if unset). Both create an empty one if
there isn't any. The file holds `[[projects]]`, repos for `-g` to span besides the ones
`ccwt` has been run in, `branch_prefix` — what `ccwt new` puts in front of a worktree's name
to make its branch (`worktree-` when unset) — `columns`, which columns the table draws,
`sort`, the order the worktrees come in (`freshness` when unset), `task_command`, the agent
cli a queued prompt is run with (`claude` when unset), `forges`, which cli finds a host's
reviews for the tui's `m` and `ccwt mr`, `environments`, what `ccwt mr`'s `ENV` column calls
each environment (`production = "prod"`), `categories`, which section of Herdr's sidebar `o`
puts a project in, and `herdr_events`, whether the tuis listen to Herdr's events (off when
unset). See [Configuration](../configuration.md).

## `ccwt --version`

Print version information.
