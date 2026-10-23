# Creating worktrees

`ccwt new` creates a worktree at `.claude/worktrees/<name>` on a new branch
`worktree-<name>` (the prefix is [configurable](../configuration.md#branch_prefix)). With no name
it generates one (e.g. `dreamy-foraging-hickey`); pass a name to choose your own. Run from
inside an existing worktree, it returns that worktree instead of nesting a new one.
The new branch starts from the upstream of the repo's checked-out branch (`origin/main`,
usually, which the tui keeps fetched), not from local `main`, so it doesn't need a `p` first.

```sh
ccwt new --switch foobar          # check out an existing branch instead of creating one
ccwt new mywt --switch foobar     # ... in a worktree of your own name
ccwt new --path                   # print the absolute path instead of the name
```

## Carrying `.env` and friends over

A worktree is a fresh checkout, so the gitignored files your build needs — `.env`,
local credentials, editor settings — aren't in it. List them in a `.worktreeinclude`
file at the repository root and `ccwt new` copies them into every worktree it creates:

```
.env
.env.local
config/secrets.json
```

The syntax is `.gitignore`'s. Only files that *both* match a pattern and are ignored by
git are copied, so a broad pattern can't duplicate a tracked file or drag in a stray
scratch file — and file modes carry over, so a `0600` secret stays `0600`. This is the
same file [Claude Code reads](https://code.claude.com/docs/en/worktrees) when it creates
a worktree, so either tool gives you the same result.

## Layout

`ccwt` follows Claude Code's convention:

- worktrees live at `<repo-root>/.claude/worktrees/<name>`
- each is checked out on a branch named `worktree-<name>` (`branch_prefix` renames the
  `worktree-` part)

Because the layout matches, worktrees you create with `ccwt` are visible to Claude Code
and vice versa.
