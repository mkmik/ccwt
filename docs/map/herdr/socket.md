+++
title = "Herdr socket"
summary = "Talk to herdr over HERDR_SOCKET_PATH instead of running herdr"
aka = ["herdrAsk", "herdrListen"]
kind = "bridge"
intent = "induced"
research = "done"
refs = ["TODO.md", "main.go"]

[[feature]]
id = "ask"
title = "herdrAsk: one request, one line back; the cli outside herdr"
status = "done"

[[feature]]
id = "listen"
title = "herdrListen: an events.subscribe held open, redialled while herdr is away"
status = "done"

[[feature]]
id = "events"
title = "tab, pane, agent-status and rename events each re-read and redraw"
status = "todo"
note = "Stage 2: the list's corner listens; the ws round doesn't yet"
needs = ["#listen"]

[[feature]]
id = "slow-tick"
title = "The tick goes slow, as the fallback for a missed event"
status = "todo"
note = "herdr says nothing when a program exits back to its shell, so a look stays"
needs = ["#events"]

[[needs]]
on = "/tools/herdr"
why = "What it talks to"
+++
