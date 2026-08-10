# ccwt plugin for [Herdr](https://herdr.dev)

One action: **New ccwt worktree**. It runs `ccwt new` in the repo your focused
pane is in, then opens the resulting worktree as its own Herdr workspace
(`herdr worktree open`), so the worktree layout, branch naming and locking stay
ccwt's job.

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
```

or bind a key in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+w"
type = "plugin_action"
command = "ccwt.new-worktree"
description = "new ccwt worktree"
```

Failures show up as a Herdr notification and in `herdr plugin log list --plugin ccwt`.
