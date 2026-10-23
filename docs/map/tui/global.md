+++
title = "ccwt -g"
summary = "Every known project in one tui, a foldable section each"
aka = ["-g", "global"]
kind = "ui"
intent = "goal"
research = "done"
refs = ["tui.go", "seen.go", "config.go"]

[[feature]]
id = "sections"
title = "A section per project, ↵ folds; every action applies to the row's project"
status = "done"

[[feature]]
id = "known"
title = "The known projects come from the seen table, not just the config"
status = "done"
needs = ["/store/tasks-db#seen"]

[[feature]]
id = "hide"
title = "hide in the ☰ menu leaves a project out for good"
status = "done"
needs = ["/store/tasks-db#hidden"]

[[feature]]
id = "open"
title = "o: pick a project by typing, ● open ○ closed; ↵ switches to or opens it, p pulls, P copies the path"
status = "done"
needs = ["/herdr/workspaces#open"]

[[feature]]
id = "open-cmd"
title = "ccwt open: o from anywhere, the -g tui found by its pid file and woken by SIGUSR1"
status = "done"
needs = ["#open"]

[[feature]]
id = "categories"
title = "categories: o puts a project last under its ------ <name> divider"
status = "done"
needs = ["/cli/config", "/herdr/workspaces#order"]

[[feature]]
id = "herdr-order"
title = "What herdr has open first, in its sidebar's order; the rest folded behind …"
status = "done"
needs = ["/herdr/workspaces#order"]

[[feature]]
id = "dividers"
title = "herdr's ------ dividers drawn as a one-pixel kitty graphics rule"
status = "done"

[[feature]]
id = "prj-down"
title = "The corner box names the projects whose prj tab has no ccwt"
status = "done"
needs = ["/tui/list-view#ws-down"]

[[needs]]
on = "/tui/list-view"
why = "It is the list view, over every project"
+++
