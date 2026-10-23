# Several projects at once

`-g` makes `ccwt list` and `ccwt tui` span every project you've configured instead of just
the repo you're standing in, a section per project, each holding that project's worktrees
newest-first:

```
    NAME                      BRANCH                    AGE  AGENT  TOPIC
▾ ccwt (2)
  * dreamy-foraging-hickey    worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on…
  ✓ calm-baking-otter         worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
▸ platform (14)
```

In the tui the sections fold: select one and press `↵`, or double-click it, and it collapses to
the header line — the count stays, so you can see what's tucked away. `platform` above is
folded shut. Folding has its own key so that it can't misfire into opening a worktree:
`space` only opens, `↵` only folds.

It works from anywhere — including outside a git repository — and the directory you launch
it in takes no part in it: under `-g` the repos are the known ones and nothing else.
Every action applies to the project the selected row belongs to: `r` removes from that
repo, `x` creates one in that project — on a section header too, which is how you make the
first worktree in a project that has none — `p` pulls it, and the status bar shows that
project's branch state. With nothing selected yet there's no repo to act on, so those keys
say so instead of reaching for the current directory. The background `--fetch` covers every
known project.

## The known projects

The known projects are every repo `ccwt` has been run in, in any of its worktrees, in the
order it first was — kept in `$XDG_STATE_HOME/ccwt/tasks.db`, and left out once the repo is
no longer there. "hide" in the `☰` menu, on a project's section header, leaves one out for
good, however much `ccwt` goes on being run there. A repo it hasn't been run in yet, or one to
put at the top, goes in [the config file](configuration.md) — each entry the main checkout
of a repo, the one whose `.claude/worktrees/` the worktrees live under:

```toml
[[projects]]
path = "~/src/ccwt"

[[projects]]
path = "~/src/platform"
```

## Opening a project

Under [Herdr](herdr/index.md), `o` lists the known projects, most recently used first, `●`
those Herdr has a workspace on and `○` the rest — typing filters them, `↑↓` or a click
selects — after which `p` runs `git pull` in it and `P` copies its path — `↵` or a second
click switches to an open one's workspace, or opens a closed one: a new workspace in the repo,
with `ccwt` running in its first tab, named `prj`. [`ccwt open`](reference/commands.md#ccwt-open)
brings the same list up from anywhere.

Categories name the projects checked out under a directory (the longest match wins). Under
Herdr, a workspace named `------ <name>` heads a category's section of the sidebar, and a
project `o` opens goes last in its own, just above the next `------` divider:

```toml
[[categories]]
name = "work"
path = "~/w"

[[categories]]
name = "personal"
path = "~/p"
```
