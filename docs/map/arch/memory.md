+++
title = "Memory per instance"
summary = "Shave what each of the dozens of ccwt processes costs"
kind = "core"
intent = "goal"
research = "done"
refs = ["TODO.md"]

[[feature]]
id = "transcripts"
title = "No tui holds every live transcript at once"
status = "done"
needs = ["/model/topic#streamed"]

[[feature]]
id = "squash-check"
title = "The squash-merge check no longer holds main's history in memory"
status = "done"

[[feature]]
id = "gogc"
title = "One P and GOGC 25 for a tui"
status = "done"

[[feature]]
id = "herdr-socket"
title = "Ask herdr over its socket rather than a process per question"
status = "done"
needs = ["/herdr/socket#ask"]

[[feature]]
id = "rust"
title = "Rewrite it in Rust"
status = "dropped"
note = "Heap profiles said the cost is per-process runtime and sqlite, not the data; keep Go, re-architect instead"
+++

44 instances on 2026-10-12, ~18 MB each. The rest is the daemon idea.
