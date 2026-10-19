+++
title = "Topic and freshness"
summary = "What a worktree is about, from its agent's transcript or its last commit"
aka = ["TOPIC", "recap"]
kind = "core"
intent = "induced"
research = "done"
refs = ["main.go"]

[[feature]]
id = "recap"
title = "✳ the last recap in the newest transcript, or the first prompt"
status = "done"
needs = ["/agent/claude-code#transcripts"]

[[feature]]
id = "commit"
title = "⎇ the last commit, where no session ran"
status = "done"

[[feature]]
id = "freshness"
title = "Freshness: the younger of the last commit and the last transcript line"
status = "done"

[[feature]]
id = "streamed"
title = "Read transcripts without holding every live one in memory"
status = "done"
needs = ["#recap"]
+++
