+++
title = "tasks.db"
summary = "$XDG_STATE_HOME/ccwt/tasks.db: queue, worklog and seen table"
kind = "core"
intent = "induced"
research = "done"
refs = ["tasks.go", "worklog.go", "seen.go"]

[[feature]]
id = "queue"
title = "Queued prompts and their chains, dropped ones marked rather than deleted"
status = "done"

[[feature]]
id = "worklog"
title = "A row per removed worktree, with its topic"
status = "done"

[[feature]]
id = "seen"
title = "Every repo and worktree ccwt ran in or made, with first and last use"
status = "done"

[[feature]]
id = "hidden"
title = "A hidden column on the seen table"
status = "done"
needs = ["#seen"]

[[needs]]
on = "/modules/sqlite"
why = "The database"
+++
