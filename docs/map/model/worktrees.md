+++
title = "Worktree discovery"
summary = "The repo's .claude/worktrees, their branches, ages and names"
kind = "core"
intent = "induced"
research = "done"
refs = ["internal/gitutil/gitutil.go", "internal/namegen/namegen.go"]

[[feature]]
id = "discover"
title = "List the worktrees under .claude/worktrees with branch and age"
status = "done"

[[feature]]
id = "names"
title = "adjective-verb-noun names, as claude --worktree makes them"
status = "done"

[[feature]]
id = "cached-scans"
title = "No rescan of every worktree every round or keypress"
status = "done"

[[needs]]
on = "/tools/git"
why = "Everything about a worktree is a git question"
+++
