+++
title = "Review pushed, not polled"
summary = "Hear when a review changes instead of asking every minute"
aka = ["websocket", "action cable", "GraphQL subscription"]
kind = "bridge"
intent = "aspiration"
research = "outline"
refs = ["TODO.md"]

[[feature]]
id = "cable"
title = "A GraphQL subscription over wss://<host>/-/cable, an event refetching over REST"
status = "todo"
note = "A probe got confirm_subscription on gitlab-master 17.7.7; unproven that it fires when a train lands; frontend api, undocumented"

[[feature]]
id = "bulk"
title = "Watch all my merge requests at once rather than one per workspace"
status = "todo"
note = "Asked on 2026-10-12: can GraphQL bulk-watch a group of MRs?"

[[needs]]
on = "/forge/gitlab"
why = "The REST refetch an event triggers, and the minute poll as fallback"

[[needs]]
fuzzy = "a websocket client dependency"
why = "Action Cable"
+++

The web-session-cookie route was looked at on 2026-10-12 and judged a bad idea.
