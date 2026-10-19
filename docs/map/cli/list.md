+++
title = "ccwt list"
summary = "The worktree table, printed once"
aka = ["ccwt ls"]
kind = "cli"
intent = "goal"
research = "done"
refs = ["main.go"]

[[feature]]
id = "table"
title = "NAME, BRANCH, AGE, AGENT, TOPIC, sized to the terminal"
status = "done"

[[feature]]
id = "glyphs"
title = "Leading glyphs: * here, ✓ merged, ☐ out for review, * dirty, ± merged but dirty, ✳ agent working"
status = "done"
needs = ["/model/removal", "/herdr/agents#working"]

[[feature]]
id = "freshness"
title = "Freshest first: last commit or last transcript line; --sort and sort in the config"
status = "done"
needs = ["/model/topic"]

[[feature]]
id = "global"
title = "-g: every known project, a section each"
status = "done"
needs = ["/store/tasks-db#seen"]

[[feature]]
id = "herdr-names"
title = "Under herdr, NAME is the workspace's name, and herdr's sidebar order comes first"
status = "done"
needs = ["/herdr/workspaces#order"]

[[feature]]
id = "columns"
title = "columns in the config picks and orders the columns"
status = "done"
needs = ["/cli/config"]

[[feature]]
id = "no-headers"
title = "--no-headers, and no glyphs when piped"
status = "done"

[[needs]]
on = "/model/worktrees"
why = "The rows"
+++
