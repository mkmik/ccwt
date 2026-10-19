+++
title = "Prompt box"
summary = "The line editor prompts are typed into, as Claude Code's own box behaves"
kind = "ui"
intent = "induced"
research = "done"
refs = ["tui.go"]

[[feature]]
id = "line-editor"
title = "zle and Claude Code keys: words, lines, home and end, Ctrl-W/U/K"
status = "done"

[[feature]]
id = "newline"
title = "shift-↵ breaks the line, ↵ submits"
status = "done"
needs = ["/tui/frame#modify-other-keys"]

[[feature]]
id = "editor"
title = "Ctrl-G finishes the prompt in $EDITOR"
status = "done"

[[feature]]
id = "paste"
title = "A long paste goes in as [Pasted text #1 +N lines]"
status = "done"

[[feature]]
id = "title"
title = "A one-line title box over the prompt, tab and the mouse move between them"
status = "done"

[[feature]]
id = "effort"
title = "The effort scale over the title, drawn as Claude Code draws it"
status = "done"
needs = ["/agent/claude-code#flags"]

[[needs]]
on = "/tui/frame"
why = "Keys and drawing"
+++
