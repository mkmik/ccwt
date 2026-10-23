# Configuration

`ccwt` reads `$XDG_CONFIG_HOME/ccwt/config.toml` (`~/.config/ccwt/config.toml` when that
variable isn't set). Everything in it is optional. `ccwt config view` prints that file and
`ccwt config edit` opens it in `$EDITOR`, either one creating an empty file first when you
don't have one yet.

`[[projects]]` and `[[categories]]` are covered in
[Several projects at once](projects.md#the-known-projects). The rest is below.

## `branch_prefix`

`branch_prefix` sets what `ccwt new` puts in front of a worktree's name to make its branch
(`worktree-` when unset), for repos that want their branches namespaced:

```toml
branch_prefix = "mkm/"
```

It applies from the moment you set it: worktrees created under an older prefix keep their
branches, and `ccwt remove` on one of them leaves that branch behind rather than deleting
it — remove it with `git branch -D` if you want it gone.

## `columns`

`columns` picks which columns `ccwt list` and `ccwt tui` draw, in the order you name them
— `name`, `branch`, `age`, `agent`, `topic`, all of them when unset:

```toml
columns = ["name", "age", "topic"]
```

The `*`/`✓`/`±`/`☐`/`✳` markers ride on `name`, so leaving it out leaves them out too.

## `sort`

`sort` is the order `ccwt list` and `ccwt tui` put the worktrees in — `freshness` when
unset, the last commit or the last thing written to the newest session there, whichever is
younger, or `commit` for git's answer alone. `--sort` overrides it for one run:

```toml
sort = "commit"
```

## `task_command`

`task_command` is the agent cli — which agent `ccwt` starts for you, and the one place that
choice lives: what a [queued prompt](tui/queue.md#when-its-turn-comes) is handed to when its
worktree is made, and what the tui's [`c`](herdr/index.md#opening-workspaces-from-the-tui)
runs on its own. It is `claude` when unset, and split on spaces so flags get through:

```toml
task_command = "claude --permission-mode plan"
```

## `forges`

`forges` says which cli knows about a host's reviews, for the tui's
[`m`](tui/index.md#opening-the-review) key and for [`ccwt mr`](worktrees/mr.md), which asks
GitHub for a `"gh"` host and GitLab for any other. Only a host whose name doesn't give it
away needs a line — a self-hosted GitLab most of all, since `github.com`, `gitlab.com` and
anything with `gitlab` in the name are guessed:

```toml
[forges]
"code.example.com" = "glab"
```

## `environments`

`environments` renames an environment for [`ccwt mr`](worktrees/mr.md)'s `ENV`
column: the name GitLab or GitHub knows it by on the left, what to call it on the right.
Deployment pipelines name environments to be unambiguous in a settings page, which is
longer than a column you only glance at wants:

```toml
[environments]
production = "prod"
staging = "stg"
```

## `herdr_events`

`herdr_events` has the tuis listen to [Herdr](herdr/index.md)'s events over its
socket. [`ccwt ws`](herdr/ws.md) then asks Herdr about its tabs only when something changes
in its own workspace — a tab, a pane's title or directory, an agent's state — or a tab
anywhere takes the focus, and once a minute besides, rather than every two seconds; a burst
of changes is one look, half a second after it starts. The list's
[corner box](herdr/gone.md) looks again when a `ws` tab comes or goes, on top of its
ten-second look. It is off by default:

```toml
herdr_events = true
```

It is read at startup; [`ccwt restart`](tui/restart.md) has the running tuis pick up a change.
