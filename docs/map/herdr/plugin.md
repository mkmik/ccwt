+++
title = "Herdr plugin"
summary = "herdr actions: New ccwt worktree (ccwt new, then worktree open) and Open a ccwt project (ccwt open)"
kind = "bridge"
intent = "goal"
research = "done"
status = "done"
refs = ["herdr-plugin/"]

[[needs]]
on = "/cli/worktrees#new"
why = "It runs ccwt new"

[[needs]]
on = "/tools/herdr"
why = "It's herdr's plugin"
+++
