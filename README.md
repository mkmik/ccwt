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

The snippet defines a thin `ccwt` shell function that wraps the binary and performs the
`cd` for you. Everything still works without it — you just won't get the automatic
directory change.

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

`ccwt new` creates a worktree at `.claude/worktrees/<name>` on a new branch
`worktree-<name>`. With no name it generates one (e.g. `dreamy-foraging-hickey`); pass a
name to choose your own. Run from inside an existing worktree, `ccwt new` returns that
worktree instead of nesting a new one.

`--switch <branch>` checks the new worktree out on an **existing** branch instead of
creating `worktree-<name>` — the equivalent of `ccwt new` followed by `git switch <branch>`:

```sh
ccwt new --switch foobar          # .claude/worktrees/dreamy-foraging-hickey, on branch foobar
ccwt new mywt --switch foobar     # .claude/worktrees/mywt, on branch foobar
```

`--path` prints the worktree's absolute path instead of its name — handy for scripts
that need to `cd` there or pass it to another tool:

```sh
ccwt new --path                   # /path/to/repo/.claude/worktrees/dreamy-foraging-hickey
```

`ccwt list` renders a table of the repo's worktrees with their branch, age, last commit,
whether a Claude Code session is currently running in each, and what the most recent
session there was about:

```
  NAME                      BRANCH                    AGE  CLAUDE    LAST COMMIT             SESSION
* dreamy-foraging-hickey    worktree-dreamy-…-hickey  2h   yes     * Add the widget          Goal was a widget on the dashboard; it's built and…
  calm-baking-otter       ✓ worktree-calm-bak…-otter  1d   no        Fix the flux capacitor  the flux capacitor drifts by a second an hour, fix…
```

A `*` before the name marks the worktree you're currently in; a `*` before the last commit
means that worktree has uncommitted changes; a `✓` before the branch means it's already
merged into `main` (or `master`), so the worktree is safe to `ccwt remove`. All three are
omitted when stdout isn't a terminal, so piped output stays parseable.

SESSION comes from the newest Claude Code transcript for that worktree: the last recap the
session produced (`/recap`, or one Claude wrote on its own), falling back to the first
prompt you typed when it never recapped. It's blank for a worktree nobody has run a
session in.

The table is sized to your terminal, so it never wraps. BRANCH is kept narrow even when
there's room — it's usually the worktree's own name with a `worktree-` in front — and the
space goes to SESSION. When the table has to give up more it takes it from whichever
column is widest at the time, and branches and commit subjects lose their middle rather
than their tail — the last word is usually what tells them apart:

```
  NAME                       BRANCH                   AGE  CLAUDE    LAST COMMIT              SESSION
* dreamy-foraging-hickey     worktree-dreamy-…-hickey 2h   yes     * Add the widget           Goal was a widget on the…
  calm-baking-otter        ✓ worktree-calm-b…-otter   1d   no        Fix the flux ca… (#41)   the flux capacitor drift…
```

`ccwt tui` shows that same table full-screen and keeps it up to date, repainting in place
so it doesn't flicker, with a status bar along the bottom:

```
  NAME                      BRANCH                    AGE  CLAUDE    LAST COMMIT             SESSION
* dreamy-foraging-hickey    worktree-dreamy-…-hickey  2h   yes     * Add the widget          Goal was a widget on the dashboard; it's built and…
  calm-baking-otter       ✓ worktree-calm-bak…-otter  1d   no        Fix the flux capacitor  the flux capacitor drifts by a second an hour, fix…

 ccwt  q:quit  p:pull  x:new  ↵:open  r:remove │ main  ↑1 ↓2
```

`q` (or Ctrl-C) quits, `p` runs `git pull` and reports the result in the bar. The rest of
the bar is the branch you launched it from and how far it has drifted from its upstream
(`↑` ahead, `↓` behind). Those counts are kept honest by a background `git fetch` of
`origin/main` alone; `--fetch` (default `1m`) sets how often, or `0` never.
`--interval` (default `2s`) sets the refresh rate. It's meant to be parked in a pane — e.g. the main pane of a
[Herdr](https://herdr.dev) workspace — as a live view of what's running where.

The arrow keys (or `j`/`k`) select a worktree, and the bar grows the actions that apply to
it: `↵` opens it as its own Herdr workspace (via `herdr worktree open`, like the plugin in
`herdr-plugin/`), and `r` removes it, refusing an unmerged branch exactly like `ccwt remove`.
Clicking a row opens it directly. `x` creates a worktree and opens it — the same thing the
plugin's **New ccwt worktree** action does, without leaving the list.

The Herdr actions (`x`, `↵`, click-to-open) only exist when the tui is itself running in a
Herdr pane; elsewhere there's no session to open a workspace in, so they're dropped from the
bar and their keys do nothing.

## Several projects at once

`-g` makes `ccwt list` and `ccwt tui` span every project you've configured instead of just
the repo you're standing in, a section per project, each holding that project's worktrees
newest-first:

```
  NAME                        BRANCH                    AGE  CLAUDE    LAST COMMIT
▾ ccwt (2)
  dreamy-foraging-hickey      worktree-dreamy-…-hickey  2h   yes     * Add the widget
  calm-baking-otter         ✓ worktree-calm-bak…-otter  1d   no        Fix the flux capacitor
▸ platform (14)
```

In the tui the sections fold: select one and press `↵`, or click it, and it collapses to
the header line — the count stays, so you can see what's tucked away. `platform` above is
folded shut.

It works from anywhere — including outside a git repository — and the directory you launch
it in takes no part in it: under `-g` the repos are the configured ones and nothing else.
Every action applies to the project the selected row belongs to: `r` removes from that
repo, `↵` opens that worktree, `x` creates one in that project — on a section header too,
which is how you make the first worktree in a project that has none — `p` pulls it, and the
status bar shows that project's branch state. With nothing selected yet there's no repo to
act on, so those keys say so instead of reaching for the current directory. The background
`--fetch` covers every configured project.

The projects come from `$XDG_CONFIG_HOME/ccwt/config.toml` (`~/.config/ccwt/config.toml`
when that variable isn't set) — each entry the main checkout of a repo, the one whose
`.claude/worktrees/` the worktrees live under:

```toml
[[projects]]
path = "~/src/ccwt"

[[projects]]
path = "~/src/platform"
```

`ccwt config view` prints that file and `ccwt config edit` opens it in `$EDITOR`, either one
creating an empty file first when you don't have one yet.

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
| `ccwt list` | List the repo's Claude Code worktrees with branch, age, running-session status, and last commit, sorted newest-first. `-g` lists every project in `$XDG_CONFIG_HOME/ccwt/config.toml` instead, a section per project. |
| `ccwt tui` | Show the `ccwt list` table full-screen, refreshing in place without flicker, over a status bar showing how far the current branch is ahead/behind its upstream. `q` (or Ctrl-C) quits, `p` runs `git pull`. Arrow keys (or `j`/`k`) select a worktree; `↵` opens it as a Herdr workspace, a click on its row does the same, and `r` removes it (merged-only, like `ccwt remove`). `-g` spans the configured projects as a foldable section each (`↵`, or a click on the header, folds one shut) and ignores the current directory entirely, every action (`p` included) applying to the selected row's project. `--interval` (default `2s`) sets the refresh rate, `--fetch` (default `1m`) how often `origin/main` is fetched in the background. |
| `ccwt remove <name>` | Remove the worktree at `.claude/worktrees/<name>` and delete its branch. `.` means the worktree you're currently in; removing the one you're in cds you to the repo root, like `ccwt ..`. The branch is deleted only if merged: an unmerged branch refuses the whole removal, worktree included, so nothing is stranded. Pass `-D` to force-delete the branch too, or `--keep-branch` to remove only the worktree. |
| `ccwt gc` | Remove every worktree that's finished with: branch already merged (the `✓` of `ccwt list`) and no Claude Code session running in it (a `no` in the `CLAUDE` column). Prints the list it found and asks before touching anything — `-y`/`--yes` skips the question. Each removal is exactly what `ccwt remove <name>` does, branch included. |
| `ccwt new-worktree-name` | Print a generated worktree name (`adjective-verb-noun`) without creating anything. |
| `ccwt repo-root` | Print the root of the current git repository. Add `--root-worktree` to print the *enclosing* repo root when you're inside a `.claude/worktrees/<name>` worktree. |
| `ccwt ..` | Shorthand for `repo-root --root-worktree`: print (and, with shell integration, `cd` to) the enclosing repository root. |
| `ccwt init <shell>` | Emit the shell-integration snippet to source from your rc file. |
| `ccwt config view` / `ccwt config edit` | Print the config file, or open it in `$EDITOR` (`vi` if unset). Both create an empty one if there isn't any. |
| `ccwt --version` | Print version information. |

### Layout

`ccwt` follows Claude Code's convention:

- worktrees live at `<repo-root>/.claude/worktrees/<name>`
- each is checked out on a branch named `worktree-<name>`

Because the layout matches, worktrees you create with `ccwt` are visible to Claude Code
and vice versa.

</details>

## License

[MIT](LICENSE)
