+++
title = "ccwt tui"
summary = "The repo's worktree table, live, with an action per key"
aka = ["ccwt", "the list"]
kind = "ui"
intent = "goal"
research = "done"
refs = ["tui.go"]

[[feature]]
id = "table"
title = "The ccwt list table, refreshed every --interval"
status = "done"
needs = ["/cli/list"]

[[feature]]
id = "details"
title = "d: every column of the selected row in full"
status = "done"

[[feature]]
id = "history"
title = "g: your git ll in a page over the list"
status = "done"
needs = ["/tools/git"]

[[feature]]
id = "review-link"
title = "m: open the branch's review in the browser"
status = "done"
needs = ["/forge/gitlab", "/forge/github"]

[[feature]]
id = "pull"
title = "p: fast-forward to what the background fetch brought, a real pull once caught up"
status = "done"
needs = ["/model/fetch"]

[[feature]]
id = "remove"
title = "r and G: remove one, or collect the finished ones"
status = "done"
needs = ["/cli/worktrees#remove", "/cli/worktrees#gc"]

[[feature]]
id = "agents-filter"
title = "a: only the worktrees an agent is running in"
status = "done"
needs = ["/herdr/agents"]

[[feature]]
id = "running"
title = "P: what is running in each worktree, and space goes to it"
status = "done"
needs = ["/cli/ps", "/cli/nav#ps"]

[[feature]]
id = "worklog"
title = "l: the worklog, and ↵ opens a removal's page with its whole session"
status = "done"
needs = ["/cli/worklog", "/agent/claude-code#transcripts"]

[[feature]]
id = "dots"
title = "Each row leads with herdr's dot for its agents, as ws draws one for a tab"
status = "done"
needs = ["/herdr/agents#unread"]

[[feature]]
id = "open"
title = "space, x, c: open the worktree as a workspace, make one, make one with ccwt ws in it"
status = "done"
needs = ["/herdr/workspaces#open"]

[[feature]]
id = "ws-down"
title = "The corner box: workspaces whose ws tab lost its ccwt, and ↻ to start them again"
status = "done"
needs = ["/herdr/socket#listen"]

[[feature]]
id = "todography"
title = "T: todography next in a pane, c seeds an agent from an item, as ccwt ws does"
status = "done"
needs = ["/tui/ws#todography"]

[[needs]]
on = "/tui/frame"
why = "Drawing, keys and the mouse"

[[needs]]
on = "/tui/upgrade"
soft = true
why = "A tui parked for days picks up the new binary"
+++
