+++
title = "ccwt ps"
summary = "What is running in each worktree, as a process tree per worktree"
kind = "cli"
intent = "goal"
research = "done"
refs = ["ps.go"]

[[feature]]
id = "trees"
title = "A tree per worktree, rooted at the topmost process whose cwd is in it"
status = "done"

[[feature]]
id = "depth"
title = "--depth, and -g for every known project"
status = "done"

[[needs]]
on = "/tools/lsof"
why = "Where each process's working directory is"

[[needs]]
on = "/model/worktrees"
why = "Which directories are worktrees"
+++
