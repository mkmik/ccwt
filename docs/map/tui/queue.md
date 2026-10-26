+++
title = "Queued prompts"
summary = "n: work to start once the worktree it waits on is finished"
aka = ["<queued>", "<new>", "tasks"]
kind = "ui"
intent = "goal"
research = "done"
refs = ["tasks.go", "tui.go"]

[[feature]]
id = "queue"
title = "n queues a prompt behind the selected row, as a tree under it"
status = "done"

[[feature]]
id = "edit"
title = "e in the details pane rewrites it; r drops it and what waits on it"
status = "done"

[[feature]]
id = "reorder"
title = "☰ reorder: hjkl move a queued prompt among its siblings, out and in"
status = "done"

[[feature]]
id = "promote"
title = "Removing the worktree promotes what waited on it to <new> rows"
status = "done"
needs = ["/cli/worktrees#remove"]

[[feature]]
id = "start"
title = "space on <new> makes the worktree, ccwt ws first, the prompt in a tab"
status = "done"
needs = ["/herdr/workspaces#open", "/agent/claude-code#prompt-file"]

[[feature]]
id = "title"
title = "A title over the prompt; it names the agent's session and the workspace"
status = "done"
needs = ["/tui/prompt-box#title"]

[[needs]]
on = "/store/tasks-db#queue"
why = "Where the prompts wait, shared by every ccwt"

[[needs]]
on = "/tui/prompt-box"
why = "The box the prompt is typed into"
+++
