# What's running in them

`ccwt ps` answers the other half of that question — not what each worktree is about, but
what is running in it right now, as a process tree per worktree:

```
frolicking-tumbling-duckling
  49757  -zsh
  79860  -zsh
    9935  ↳ claude --worktree frolicking-tumbling-duckling
snug-juggling-dream
  23136  -zsh
  23730  ↳ go test ./...
```

A tree's root is the topmost process whose working directory is in the worktree — the shell
you started there, or a leftover test server nobody stopped — and under it hangs whatever it
spawned, wherever *that* one's working directory has since wandered off to. By default only
the first generation is shown, which is the shell and what it's busy with; `--depth`/`-d`
goes further (`-d 0` for the shells alone, `--depth=-1` for everything). Worktrees with
nothing running in them are left out, and `-g` spans every known project, a section
per project, as `ccwt list -g` does.

The working directories come from `lsof`, so a process whose cwd can't be read simply isn't
placed in a worktree.

The [tui](../tui/running.md) shows the same thing on `P`, kept up to date, and there
a process is somewhere you can go.
