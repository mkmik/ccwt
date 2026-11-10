+++
title = "Reading dennet"
summary = "Spaces, Surfaces and Dens as ccwt's workspaces, tabs and panes, and a watch for its events"
kind = "bridge"
intent = "goal"
research = "done"
refs = ["dennet.go", "TODO.md"]

[[feature]]
id = "pick"
title = "mx() picks dennet inside a den (DEN_SOCKET)"
status = "done"

[[feature]]
id = "lists"
title = "here, workspaces, sidebar, tabs and snapshot from Spaces, Surfaces and Dens"
status = "done"
needs = ["#pick"]

[[feature]]
id = "agents"
title = "Agent status from a Den's title: a spinner while Claude Code works, ✳ when idle"
status = "done"
note = "No blocked; Claude Code only, until the agent hook"
needs = ["#lists"]

[[feature]]
id = "watch"
title = "listen as a watch on spaces, surfaces and dens"
status = "done"
needs = ["#lists"]

[[needs]]
on = "/dennet/mux"
why = "dennet.go is the interface's second side"

[[needs]]
on = "/tools/dennet"
why = "What it reads"
+++

JSON over HTTP on dennet's unix socket, with the standard library: client-go
would add megabytes to each of the 40-odd ccwts running.
