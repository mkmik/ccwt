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
directory change. (On supporting terminals such as iTerm2, Ghostty, and WezTerm, `ccwt`
also emits an OSC 7 sequence so the terminal tracks the new working directory.)

## Quick start

```sh
ccwt new            # create a fresh worktree on a new branch, and cd into it
                    # ... do your work, run `claude`, commit, etc. ...

ccwt list           # show all Claude Code worktrees in this repo
ccwt ..             # jump back to the repository root
ccwt remove <name>  # delete a worktree and its branch when you're done
```

`ccwt new` creates a worktree at `.claude/worktrees/<name>` on a new branch
`worktree-<name>`. With no name it generates one (e.g. `dreamy-foraging-hickey`); pass a
name to choose your own. Run from inside an existing worktree, `ccwt new` returns that
worktree instead of nesting a new one.

`ccwt list` renders a table of the repo's worktrees with their branch, age, last commit,
and whether a Claude Code session is currently running in each:

```
NAME                     BRANCH                          AGE  CLAUDE  LAST COMMIT
dreamy-foraging-hickey   worktree-dreamy-foraging-hickey  2h  yes     Add the widget
calm-baking-otter        worktree-calm-baking-otter       1d  no      Fix the flux capacitor
```

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
| `ccwt new [name]` | Create a worktree under `.claude/worktrees/<name>` on a new branch `worktree-<name>`, and print `<name>`. Generates a name if omitted; reuses an existing worktree of the same name. When run inside a worktree it returns the enclosing one instead of creating a new one (override with `--force-create`). |
| `ccwt list` | List the repo's Claude Code worktrees with branch, age, running-session status, and last commit, sorted newest-first. |
| `ccwt remove <name>` | Remove the worktree at `.claude/worktrees/<name>` and delete its branch. Refuses if you're currently inside it. The branch is deleted only if merged; pass `-D` to force-delete an unmerged branch. |
| `ccwt new-worktree-name` | Print a generated worktree name (`adjective-verb-noun`) without creating anything. |
| `ccwt repo-root` | Print the root of the current git repository. Add `--root-worktree` to print the *enclosing* repo root when you're inside a `.claude/worktrees/<name>` worktree. |
| `ccwt ..` | Shorthand for `repo-root --root-worktree`: print (and, with shell integration, `cd` to) the enclosing repository root. |
| `ccwt init <shell>` | Emit the shell-integration snippet to source from your rc file. |
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
