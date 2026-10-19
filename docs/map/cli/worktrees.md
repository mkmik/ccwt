+++
title = "Worktree lifecycle"
summary = "new, cd, .., remove, done, gc, lock: make worktrees and tear them down"
aka = ["ccwt new", "ccwt remove", "ccwt done", "ccwt gc", "ccwt lock", "repo-root"]
kind = "cli"
intent = "goal"
research = "done"
refs = ["main.go", "internal/gitutil/gitutil.go", "internal/namegen/namegen.go"]

[[feature]]
id = "new"
title = "ccwt new: a worktree under .claude/worktrees on worktree-<name>, --switch, --path"
status = "done"

[[feature]]
id = "worktreeinclude"
title = "Copy the gitignored files .worktreeinclude names into a new worktree"
status = "done"

[[feature]]
id = "remove"
title = "ccwt remove: refuse unmerged, dirty or busy worktrees unless -D"
status = "done"
needs = ["/model/removal"]

[[feature]]
id = "close-workspace"
title = "remove closes the worktree's herdr workspace, ours last, focus to the repo's"
status = "done"
needs = ["/herdr/workspaces#close"]

[[feature]]
id = "done"
title = "ccwt done: remove . -y"
status = "done"
needs = ["#remove", "#close-workspace"]

[[feature]]
id = "gc"
title = "ccwt gc: every merged, clean, idle worktree, after asking"
status = "done"
needs = ["/model/removal"]

[[feature]]
id = "worklog"
title = "Every removal writes a line to the worklog first"
status = "done"
needs = ["/store/tasks-db#worklog", "/model/topic"]

[[feature]]
id = "lock"
title = "ccwt lock: put ccwt's lock on a worktree missing it"
status = "done"

[[needs]]
on = "/model/worktrees"
why = "Where the worktrees are and what they're called"

[[needs]]
on = "/modules/kong"
why = "The command line's parser"
+++

The commands a shell uses to make a worktree, go into it, and get rid of it.
The layout is Claude Code's, so `claude --worktree` and ccwt see the same
worktrees.
