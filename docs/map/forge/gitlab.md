+++
title = "GitLab"
summary = "Merge requests, pipelines, trains and deployments, with glab's host and token"
kind = "bridge"
intent = "induced"
research = "done"
refs = ["mr.go"]

[[feature]]
id = "rest"
title = "One connection for every question rather than a glab each"
status = "done"

[[feature]]
id = "timeouts"
title = "A hung request no longer freezes the ws view's review"
status = "done"

[[needs]]
on = "/tools/glab"
why = "The host and the token"
+++
