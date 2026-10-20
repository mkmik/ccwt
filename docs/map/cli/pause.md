+++
title = "ccwt pause"
summary = "Quiet what every ccwt asks herdr on its own, to see whether herdr is slow because of it"
aka = ["unpause", "herdrPoll"]
kind = "cli"
intent = "goal"
research = "done"
status = "done"
refs = ["pause.go"]

[[needs]]
on = "/herdr/socket#ask"
why = "herdrPoll stands in front of herdrAsk for the views' questions"

[[needs]]
on = "/herdr/socket#listen"
why = "The subscription hangs up while paused"
+++

A file in the state directory, `paused`: while it is there the views go on
showing herdr's last answer, the badge goes unsent and the corner stops
looking. Keys and commands still reach herdr, and a removal still asks it
whether an agent is working.
