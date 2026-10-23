# Getting started

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

`bash` works the same way; under fish it's `ccwt init fish | source`.

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
ccwt list -g        # ... or in every project ccwt knows
ccwt ..             # jump back to the repository root
ccwt remove <name>  # delete a worktree and its branch when you're done
ccwt remove .       # ... or the one you're in: deletes it and cds you out
ccwt gc             # ... or all the finished ones at once, after confirming
```

A bare `ccwt` opens [the tui](tui/index.md): the same list, full-screen and kept up to date.
