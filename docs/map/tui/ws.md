+++
title = "ccwt ws"
summary = "A workspace's own tui: its review, its agents' tabs, and seed prompts for more"
aka = ["conductor", "workspace tui"]
kind = "ui"
intent = "goal"
research = "outline"
refs = ["ws.go", "TODO.md"]

[[feature]]
id = "tabs"
title = "The tab table: TAB, AGENT, DIR, TITLE, in tab-bar order; space goes"
status = "done"

[[feature]]
id = "seed"
title = "c: a seed prompt runs task_command in a new unfocused tab of the worktree"
status = "done"
needs = ["/tui/prompt-box", "/agent/claude-code#prompt-file"]

[[feature]]
id = "named-seed"
title = "The seed's title names the agent (--name) and the workspace"
status = "done"
needs = ["#seed", "/tui/prompt-box#title", "/agent/claude-code#flags"]

[[feature]]
id = "trust"
title = "Ask Claude Code's trust question first, and record a yes in ~/.claude.json"
status = "done"
needs = ["/agent/claude-code#trust"]

[[feature]]
id = "dots"
title = "AGENT is herdr's dot, green until someone has read the tab"
status = "done"
needs = ["/herdr/agents#unread"]

[[feature]]
id = "review"
title = "The workspace's review, as ccwt mr prints it, over the tabs"
status = "done"
needs = ["/cli/mr"]

[[feature]]
id = "done"
title = "r: ccwt done for the workspace, on the bar only when it would go through"
status = "done"
needs = ["/model/removal#mr-merged", "/cli/worktrees#done"]

[[feature]]
id = "badge"
title = "Tells herdr where the review stands, for the sidebar's badge"
status = "done"
needs = ["/herdr/badge"]

[[feature]]
id = "todography"
title = "T: todography next in a pane; c seeds an agent with an item's t8y: url"
status = "done"
needs = ["/tools/todography", "#seed"]

[[feature]]
id = "events"
title = "Redraw on herdr's events rather than asking every 2s"
status = "done"
note = "With herdr_events = true; the 2s tick stays until /herdr/socket#slow-tick"
needs = ["/herdr/socket#events"]

[[feature]]
id = "topic"
title = "A TOPIC column: an agent's one-line summary of each tab, with a glyph for its source"
status = "todo"
note = "Stage 2 in TODO.md; TITLE stays the fallback"
needs = ["/model/topic"]

[[feature]]
id = "topic-cache"
title = "Tab summaries cached, refreshed only when the pane moved on"
status = "todo"
needs = ["#topic"]

[[feature]]
id = "details"
title = "d: a details pane for a tab"
status = "todo"
note = "detailPane takes its labels from the worktree table; the ws view leaves cells nil"
needs = ["/tui/frame#panes"]

[[feature]]
id = "ticket"
title = "The Jira ticket a tab is on, from its prompt, branch or commits"
status = "todo"
note = "Stage 3 in TODO.md"
needs = ["/cli/mr#jira"]

[[feature]]
id = "tab-review"
title = "The review a tab's branch has; m on a tab opens it"
status = "todo"
needs = ["/cli/mr"]

[[feature]]
id = "review-columns"
title = "Columns for ticket and review, with their state, in the tab table"
status = "todo"
needs = ["#ticket", "#tab-review"]

[[needs]]
on = "/herdr/socket"
why = "Its tabs, panes and agents, read every round"

[[needs]]
on = "/tui/frame"
why = "Drawing, keys and the mouse"
+++

One worktree per workspace; its agents share the checkout. `TODO.md` has the
stages.
