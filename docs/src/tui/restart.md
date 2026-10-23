# Upgrades and restarts

Something parked in a pane for days goes on running the binary it started from, so the tui
watches that binary and says so when it's replaced — by `go install`, by a package manager,
by a downloaded release:

```
 ☰  q:quit  p:pull  /:search │ upgraded to v0.64.0 — restarting when idle
```

Then it does: once the keys have been quiet for four seconds, with nothing typed and no pane
open, the process replaces itself with the new binary — an `exec`, so it keeps its pid, its
pane and its flags — and the list comes back fresh; the selection and any pattern in force
are what doesn't survive it. The version is the one you're restarting into (asked of the new
binary itself; it's dropped from the line if it won't answer). Windows has no `exec`, so
there the notice reads `restart ccwt` and stays up until you do.

`ccwt restart` sets off the same restart in every tui and `ccwt ws` running, with no new
binary — for the config only read at startup, [`herdr_events`](../configuration.md#herdr_events)
and `-g`'s [`[[projects]]`](../projects.md#the-known-projects). Their bars say
`restart asked — restarting when idle` until they do.
