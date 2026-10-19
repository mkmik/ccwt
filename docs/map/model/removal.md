+++
title = "Can it go?"
summary = "The one test remove, done, gc and the views ask: merged, clean, idle"
aka = ["removeBlocked", "merged check"]
kind = "core"
intent = "induced"
research = "done"
refs = ["main.go"]

[[feature]]
id = "merged"
title = "Merged into main or origin/main, squash and rebase included"
status = "done"

[[feature]]
id = "dirty"
title = "Uncommitted changes block it, merged or not"
status = "done"

[[feature]]
id = "busy"
title = "An agent working in it blocks it"
status = "done"
needs = ["/herdr/agents#working"]

[[feature]]
id = "mr-merged"
title = "r turns up as soon as the merge request says merged, not on the next slow tick"
status = "done"
note = "The wider idea from 2026-10-16: one signal source speaking wakes the others"
needs = ["/cli/mr"]

[[needs]]
on = "/tools/git-tree-merged"
soft = true
why = "Squash merges read as merged; without it plain ancestry, which errs safe"

[[needs]]
on = "/tools/git"
why = "Ancestry and status"
+++
