+++
title = "Herdr plugin"
summary = "A New ccwt worktree action in herdr: ccwt new, then worktree open"
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
