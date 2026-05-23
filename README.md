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
snippet for your shell from your rc file:

```sh
# bash / zsh — add to ~/.bashrc or ~/.zshrc
source <(ccwt init zsh)     # or: source <(ccwt init bash)
```

```fish
# fish — add to ~/.config/fish/config.fish
ccwt init fish | source
```

This defines a thin `ccwt` shell function that wraps the binary and performs the `cd`
for you. Everything still works without it — you just won't get the automatic directory
change. (On supporting terminals such as iTerm2, Ghostty, and WezTerm, `ccwt` also emits
an OSC 7 sequence so the terminal tracks the new working directory.)

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
| `ccwt init <bash\|zsh\|fish>` | Emit the shell-integration snippet to source from your rc file. |
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
