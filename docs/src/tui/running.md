# What is running in there

`P` swaps the worktrees for what is running in them — [`ccwt ps`](../worktrees/ps.md),
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
means the pane it's running in, the same place [`ccwt nav ps`](../reference/commands.md#ccwt-nav)
would take you (herdr or tmux, whichever it's under); on the worktree line above it, the
workspace, as `space` does on the list. `esc` brings the worktrees back — the first `esc`
clears the search, if there is one — and so does another `P`.

A process's pane is an environment variable it inherited, and macOS hides the environment of
everything under `/bin`: a shell can't answer for itself, so its descendants are asked
instead, which is why going to a bare `-zsh` still lands in the right place as long as
something is running under it.
