+++
title = "Workspace badge"
summary = "A glyph on the workspace in herdr's sidebar for where its review stands"
aka = ["report_metadata", "▣", "■"]
kind = "bridge"
intent = "goal"
research = "done"
refs = ["ws.go"]

[[feature]]
id = "review"
title = "Open, merged, and ± for merged with local changes"
status = "done"

[[feature]]
id = "pipeline"
title = "What the review's pipeline is doing: ▣ running, a cross for failed"
status = "done"

[[feature]]
id = "merge-train"
title = "■ for a merge train, apart from any other pipeline"
status = "done"
needs = ["/cli/mr#merge-train"]

[[needs]]
on = "/cli/mr"
why = "Where the review stands"

[[needs]]
on = "/herdr/socket"
why = "workspace.report_metadata, with a ttl since nothing says when the tui stops"
+++
