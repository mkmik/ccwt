+++
title = "GitHub"
summary = "Pull requests, checks, merge queues and deployments through gh"
kind = "bridge"
intent = "induced"
research = "done"
refs = ["pr.go"]

[[feature]]
id = "prs"
title = "The pull request of the branch, pushed under another name or mid-rebase too"
status = "done"

[[feature]]
id = "checks"
title = "Checks on the last commit; ci still running while unrequired ones go"
status = "done"

[[needs]]
on = "/tools/gh"
why = "The host and the token"
+++
