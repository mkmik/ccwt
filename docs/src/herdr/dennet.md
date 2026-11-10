# Under dennet and dense

[dennet](https://github.com/mkmik/dennet) keeps workspaces, tabs and terminals as objects
that other programs can read and change, and dense (a Ghostty fork) shows them: a **Space**
is a workspace in dense's sidebar, a **Surface** is one of its tabs, and a **Den** is the
terminal in that tab, kept running in the background by den.

When `ccwt` runs inside a den — the den sets `DEN_SOCKET` — it talks to dennet instead of
Herdr. Everything else in this part of the book works the same way, with these differences.

## How workspaces map

- A worktree's workspace is a Space whose working directory is the worktree, placed under
  the Space of its repository. `ccwt` finds a worktree's Space by that directory alone, so a
  Space made from dense's sidebar needs one set before `ccwt` can tell what it is for:

  ```sh
  dennet patch space <name> --type merge -p '{"spec":{"workingDir":"/path/to/worktree"}}'
  ```

- `x` and `c` make that Space, and the repository's Space too when dennet has none, with a
  tab in it. `c` then types `ccwt ws` into the tab with `den send` and names the tab `ws`.
- Closing a workspace (`ccwt remove`, `ccwt done`, `r`) deletes the Space, then its tabs, then
  their dens, which stops the programs in them.
- The merge request's mark goes in the Space's badges.

## What doesn't work yet

- **Going somewhere** needs a dense that reads the request. `space` on a tab, `ccwt nav ws`,
  and opening a workspace ask the dense window you just typed into to show it; other
  windows keep showing what they show. The request is an annotation on the tab `ccwt`
  runs in, `dense.mkm.pub/show`; an older dense ignores it. `ccwt nav ps` doesn't know dens.
- **Agent status** comes from the terminal title Claude Code sets: a spinner while it works,
  `✳` when it waits for you. Other agents show no dot, and there is no red "blocked" one.
- **No green "unread" dot**, since `ccwt` can't see which tab you look at: a finished agent
  is grey.
- **Popups.** `ccwt open` shows the picker in its own terminal.
- **Sidebar dividers.** dense's sidebar is a tree, so the categories of [several projects at
  once](../projects.md) don't move a new Space anywhere.
- `ccwt pause` quiets only what is asked of Herdr.

The plan, and what dennet, dense or den would need for each of these, is in the repository's
`TODO.md`.
