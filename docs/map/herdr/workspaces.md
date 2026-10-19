+++
title = "Workspaces"
summary = "Open, name, order and close herdr workspaces on worktrees"
kind = "bridge"
intent = "induced"
research = "done"
refs = ["main.go", "tui.go"]

[[feature]]
id = "open"
title = "herdr worktree open, so herdr knows the repo; a new worktree's workspace named after it"
status = "done"

[[feature]]
id = "close"
title = "Close a worktree's workspace, focusing the repo's first when it's the one on screen"
status = "done"

[[feature]]
id = "order"
title = "The sidebar's order, its ------ dividers and where a new workspace goes"
status = "done"

[[needs]]
on = "/herdr/socket"
why = "workspace.list and friends"
+++
