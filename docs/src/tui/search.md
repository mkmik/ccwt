# Searching

`/` searches, as in vim or less: type a pattern into the bar and the selection moves to the
match as you type, `enter` accepts it, `esc` puts back the pattern and the row you started
from, and `n`/`N` walk the rest of the matches forwards and back, wrapping around the ends.
`?` searches upwards instead. Every match on screen is picked out, not just the selected
one; a bare `esc` clears them, and clears the selection with them — nothing is selected
again, and the bar drops the keys that only apply to a row.

The pattern is a [regular expression](https://pkg.go.dev/regexp/syntax), always
case-insensitive, matched against the whole line as drawn — name, branch, age and topic
alike, so `/✓` finds the worktrees whose branch is merged and `/yes` the ones with an agent
running in them. One that doesn't compile simply matches nothing, which is what half of one
is while you're still typing it. Under Herdr, where each row leads with [its agents'
dot](../herdr/index.md#agent-dots), what a search sees there is the symbol the dot stands in
for — `×` blocked, `◐` working, `✓` done, `○` idle, `·` none — so `/✓` finds the finished
turns along with the merged branches.
