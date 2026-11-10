+++
title = "The mux seam"
summary = "One interface for everything ccwt says to a workspace manager: herdr's side now, dennet's next"
aka = ["mux", "mx", "herdrMux"]
kind = "core"
intent = "goal"
research = "done"
status = "done"
refs = ["mux.go", "herdr.go", "dennet.go", "TODO.md"]

[[needs]]
on = "/herdr/socket"
why = "herdr's side is what herdr.go already said, moved behind the interface"
+++

ccwt's words — workspace, tab, pane, agent — and ids it hands back unread.
`mx()` picks the mux from the environment at each call.
