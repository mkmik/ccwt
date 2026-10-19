+++
title = "One ccwt for every pane"
summary = "A daemon gathers for every repo; thin clients hand it their tty"
aka = ["daemon", "SCM_RIGHTS", "thin client"]
kind = "core"
intent = "aspiration"
research = "outline"
refs = ["TODO.md"]

[[feature]]
id = "no-sqlite"
title = "The tuis stop linking sqlite: ~2 MB an instance"
status = "todo"

[[feature]]
id = "gather"
title = "One process does the git, transcript, herdr and MR rounds; tuis subscribe"
status = "todo"
needs = ["#no-sqlite"]

[[feature]]
id = "thin-clients"
title = "A small ccwt hands its tty over a unix socket; the daemon draws on it"
status = "todo"
note = "$EDITOR runs in the client; SIGWINCH and ^C forwarded; reconnect on daemon re-exec"
needs = ["#gather"]

[[needs]]
on = "/arch/memory"
why = "Where it starts from"

[[needs]]
fuzzy = "per-session state instead of 42 package vars, 25 Getenvs, 4 Getwds and stdout"
why = "One process drawing many sessions"
+++
