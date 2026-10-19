+++
title = "TUI frame"
summary = "What every view shares: drawing, keys, mouse, menu, search, panes"
kind = "ui"
intent = "induced"
research = "done"
refs = ["tui.go", "winch_unix.go"]

[[feature]]
id = "repaint"
title = "Repaint in place without flicker, redraw on SIGWINCH"
status = "done"

[[feature]]
id = "menu"
title = "The ☰ menu: the bar's keys, clickable"
status = "done"

[[feature]]
id = "search"
title = "/ and ? as in vim: incremental, regexp, n and N"
status = "done"

[[feature]]
id = "mouse"
title = "Click selects, double-click opens"
status = "done"

[[feature]]
id = "copy"
title = "Drag selects text and copies it: pbcopy, wl-copy, xclip or OSC 52"
status = "done"

[[feature]]
id = "panes"
title = "Windows over the list: details, history, worklog, pages that scroll"
status = "done"

[[feature]]
id = "modify-other-keys"
title = "Ask for modifyOtherKeys, so shift-↵ is its own key under herdr"
status = "done"

[[needs]]
on = "/modules/x-term"
why = "Raw mode and the terminal's size"
+++
