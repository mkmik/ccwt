# ccwt plugin for [Herdr](https://herdr.dev)

Two actions:

- **New ccwt worktree** (`ccwt.new-worktree`) runs `ccwt new` in the repo your
  focused pane is in, then opens the resulting worktree as its own Herdr
  workspace (`herdr worktree open`), so the worktree layout, branch naming and
  locking stay ccwt's job.
- **Open a ccwt project** (`ccwt.open-project`) opens a Herdr popup over the
  focused pane with ccwt's project picker in it (`ccwt open`), the one
  `o` opens in `ccwt -g`. `esc` closes it and leaves you where you were; a pick
  goes to the project's workspace.

Requires [`ccwt`](https://github.com/mkmik/ccwt) on `PATH` (or set `CCWT_BIN`)
and Herdr 0.8.0+.

## Install

```sh
herdr plugin install mkmik/ccwt/herdr-plugin
# or, for local development:
herdr plugin link ~/w/mmikulicic/ccwt/herdr-plugin
```

## Use

```sh
herdr plugin action invoke ccwt.new-worktree
herdr plugin action invoke ccwt.open-project
```

or bind keys in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+w"
type = "plugin_action"
command = "ccwt.new-worktree"
description = "new ccwt worktree"

[[keys.command]]
key = "prefix+o"
type = "plugin_action"
command = "ccwt.open-project"
description = "open a ccwt project"
```

Failures show up as a Herdr notification and in `herdr plugin log list --plugin ccwt`.
