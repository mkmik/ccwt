+++
title = "Upgrade restart"
summary = "A parked tui restarts itself into a replaced binary once the keys go quiet"
kind = "ui"
intent = "goal"
research = "done"
refs = ["restart_unix.go", "restart_windows.go"]

[[feature]]
id = "notice"
title = "The bar says upgraded to vX, and which ccwt is drawing it"
status = "done"

[[feature]]
id = "exec"
title = "exec the new binary after four quiet seconds, same pid, pane and flags"
status = "done"

[[feature]]
id = "content-stamp"
title = "Key the restart on the binary's content, not its mtime"
status = "done"
note = "So a `git pull && go install` loop only restarts on a real change"
+++
