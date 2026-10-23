# ccwt

A small command-line helper for managing coding-agent git worktrees.

An agent works best in a worktree of its own, one per task, under
`<repo-root>/.claude/worktrees/<name>`. `ccwt` lets you create, list, jump between, and
tear down those worktrees from your own shell, with a table saying what each one is about
and whether something is running in it.

**The manual is at <https://mkmik.github.io/ccwt/>.**

## Install

```sh
go install github.com/mkmik/ccwt@latest
```

Then, in your shell's rc file (after `compinit` under zsh), so that `ccwt` can `cd` you into
worktrees:

```sh
source <(ccwt init zsh)
```

## Quick start

```sh
ccwt new            # create a fresh worktree on a new branch, and cd into it
ccwt list           # show all agent worktrees in this repo
ccwt ..             # jump back to the repository root
ccwt remove <name>  # delete a worktree and its branch when you're done
ccwt                # the same list, full-screen and kept up to date
```

## The manual

The manual is an [mdBook](https://rust-lang.github.io/mdBook/) under `docs/`: its pages are
in `docs/src`, and `mdbook serve docs` shows them locally. Every push to `main` publishes it
to GitHub Pages.

## License

[MIT](LICENSE)
