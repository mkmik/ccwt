+++
title = "ccwt mr"
summary = "Where a review stands, what its pipeline did, and where it is deployed"
aka = ["ccwt pr", "review table"]
kind = "cli"
intent = "goal"
research = "done"
refs = ["mr.go", "pr.go"]

[[feature]]
id = "table"
title = "MR, STATUS, PIPELINE for the branch you're on, or a url"
status = "done"
needs = ["/forge/gitlab"]

[[feature]]
id = "markdown"
title = "Piped, the table comes out as markdown"
status = "done"

[[feature]]
id = "environments"
title = "ENV: the environments whose deployment has the merge request in it now"
status = "done"
needs = ["/cli/config"]

[[feature]]
id = "jira"
title = "A Jira key, or a list on stdin, gets a row per merge request naming it"
status = "done"

[[feature]]
id = "merge-train"
title = "merge train instead of gitlab's ci still running, asking the train when gitlab won't say"
status = "done"

[[feature]]
id = "threads"
title = "open comments: review threads still unresolved, linked to the first one"
status = "done"

[[feature]]
id = "approvers"
title = "APPROVED: who approved, bots by name"
status = "done"

[[feature]]
id = "github"
title = "Pull requests through gh: the same table, draft, changes requested, merge queue"
status = "done"
needs = ["/forge/github"]
+++
