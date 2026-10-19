+++
title = "Agent status"
summary = "What herdr says about the agents in each pane"
kind = "bridge"
intent = "induced"
research = "done"
refs = ["ws.go", "tui.go"]

[[feature]]
id = "working"
title = "✳: an agent is working in that worktree, whatever agent it is"
status = "done"

[[feature]]
id = "dots"
title = "Herdr's dot: red blocked, yellow working, green done, grey idle, from its palette"
status = "done"

[[feature]]
id = "unread"
title = "Green until read, from state_change_seq rather than watching the change"
status = "done"
needs = ["#dots"]

[[needs]]
on = "/herdr/socket"
why = "agent.list"
+++
