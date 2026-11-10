+++
title = "Acting on dennet"
summary = "Open workspaces, make tabs, type into them, badge and close them, through dennet"
kind = "bridge"
intent = "goal"
research = "done"
refs = ["dennet.go", "TODO.md"]

[[feature]]
id = "open"
title = "openWorktree, openProject, newTab: a Space under the repo's, a Surface and a Den"
status = "done"
needs = ["/dennet/reading#lists"]

[[feature]]
id = "run"
title = "run with den send, on a den of this machine"
status = "done"
note = "Typed, as herdr types it; the agent as the Den's command was the other way"
needs = ["#open"]

[[feature]]
id = "edit"
title = "Rename and move tabs, rename workspaces, close them (the Space, its Surfaces, then their Dens)"
status = "done"
needs = ["/dennet/reading#lists"]

[[feature]]
id = "badge"
title = "The merge request's glyph on Space spec.badges"
status = "done"
note = "ccwt finds its own badge by glyph until a badge has a source"
needs = ["/dennet/reading#lists"]

[[feature]]
id = "procs"
title = "What runs in front in a den, from ps and its status.pid"
status = "done"
note = "This machine only"
needs = ["/dennet/reading#lists"]

[[feature]]
id = "show"
title = "focusTab, focusWorkspace, and showing what is opened: a request on our own Surface"
status = "done"
note = "dense.mkm.pub/show; the dense window just typed into that tab obeys, in dense's ShowRequest"
needs = ["/dennet/reading#lists"]

[[needs]]
on = "/tools/dennet"
why = "What it writes"
+++
