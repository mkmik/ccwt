# Using ccwt with Claude Code

Claude Code has a built-in `--worktree` flag that runs a session in its own worktree.
The catch: Claude creates and uses that worktree *internally*, but your terminal tab's
working directory stays at the base project root. Open a new tab or split and you land
back in the base repo, not in the worktree the session is actually using.

`ccwt new` fixes this by creating the worktree *up front*:

1. it prints the worktree name on **stdout** — capture it and pass it to `claude --worktree`;
2. it emits an [OSC 7](https://gitlab.freedesktop.org/terminal-wg/specifications/-/merge_requests/7)
   "current directory" report on **stderr**, which the terminal reads to update its notion
   of the cwd.

Terminals and multiplexers that honour OSC 7 (iTerm2, Ghostty, WezTerm, tmux/cmux, …)
will then open new tabs and splits *in the worktree directory*. Because `ccwt` and Claude
Code share the same [`.claude/worktrees/<name>` layout](worktrees/creating.md#layout), the
name printed by `ccwt new` is exactly what `claude --worktree` expects.

> This used to be doable with a Claude Code hook that printed the escape code itself, but
> Claude Code no longer lets hooks emit raw escape sequences to the terminal. Emitting it
> from `ccwt new` in a small wrapper around `claude` is the way to get it back.

## Sample wrapper

Save this as `claude-wt` somewhere on your `PATH`, `chmod +x` it, and run it instead of
`claude`. It allocates a fresh worktree per session unless you ask for a specific one:

```bash
#!/usr/bin/env bash
# claude-wt — run each Claude Code session in its own ccwt worktree, and let the
# terminal's cwd follow into it (via the OSC 7 sequence ccwt emits on stderr).
#
#   claude-wt                  # fresh worktree, terminal cd's into it
#   claude-wt --worktree foo   # use the worktree named "foo"
#   claude-wt --no-worktree    # skip worktree handling entirely (wrapper-only flag)
set -eo pipefail

args=()
has_worktree=0
no_worktree=0
for arg in "$@"; do
    case "$arg" in
        --no-worktree)           no_worktree=1 ;;                 # wrapper-only; drop it
        --worktree|--worktree=*)  has_worktree=1; args+=("$arg") ;;
        *)                        args+=("$arg") ;;
    esac
done

if [ "$has_worktree" -eq 0 ] && [ "$no_worktree" -eq 0 ]; then
    # `ccwt new` prints the worktree name on stdout (captured here) and emits the
    # OSC 7 cwd report on stderr, which we deliberately let flow to the terminal.
    worktree=$(ccwt new)
    args=(--worktree "$worktree" "${args[@]}")
fi

exec claude "${args[@]}"
```

Naming the wrapper something other than `claude` (here, `claude-wt`) keeps `exec claude`
from re-invoking the wrapper itself. If you'd rather call it `claude`, point the `exec`
line at the real binary by absolute path instead (e.g. `exec "$HOME/.local/bin/claude"`).

The key detail is that command substitution — `worktree=$(ccwt new)` — captures only
stdout, so the OSC 7 sequence on stderr still reaches the terminal. Don't redirect or
swallow stderr, or you'll lose the cwd report.
