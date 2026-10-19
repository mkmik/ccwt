+++
title = "ccwt nav"
summary = "Go to the pane a process runs in, or to a herdr workspace by fuzzy name"
aka = ["ccwt go", "goto", "jump"]
kind = "cli"
intent = "goal"
research = "outline"
refs = ["main.go"]

[[feature]]
id = "ps"
title = "nav ps: the pane the best-matching process runs in, under herdr or tmux"
status = "done"

[[feature]]
id = "ws"
title = "nav ws: the best fuzzy match on a workspace's title or its worktree's name"
status = "done"
needs = ["/herdr/workspaces"]

[[feature]]
id = "picker"
title = "When several match, a table of them to pick from instead of the tightest"
status = "todo"
note = "Asked for on 2026-10-07 as `ccwt go <name>`: straight there on one match, a list with arrows and ↵ on several"
needs = ["#ws", "/tui/frame"]
+++

`ccwt go` was the name asked for. `nav ws` does the one-match half of it.
