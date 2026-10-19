+++
title = "Claude Code"
summary = "Its worktree layout, transcripts, trust and flags"
kind = "bridge"
intent = "induced"
research = "done"
refs = ["trust.go", "main.go"]

[[feature]]
id = "layout"
title = ".claude/worktrees/<name> on worktree-<name>, as claude --worktree"
status = "done"

[[feature]]
id = "transcripts"
title = "Read ~/.claude/projects transcripts: AGENT, TOPIC, the worklog's session page"
status = "done"

[[feature]]
id = "trust"
title = "Record trust in ~/.claude.json under Claude Code's own lock"
status = "done"

[[feature]]
id = "prompt-file"
title = "Hand the prompt over in a file, left behind if the start fails"
status = "done"

[[feature]]
id = "flags"
title = "--name, --model and --effort from the prompt box"
status = "done"

[[needs]]
on = "/tools/claude"
why = "The default task_command"
+++
