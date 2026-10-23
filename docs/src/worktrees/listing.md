# Listing worktrees

`ccwt list` renders a table of the repo's worktrees with their branch, age, whether an
agent session is currently running in each, and what each one is about:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on the dashboard; it's built and…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
```

## The leading glyphs

Every row leads with two glyphs. The first is a `*` on the worktree you're currently in.
The second says whether the worktree can go: `✓` means its branch is already merged into
`main` (or `master`), so it's safe to `ccwt remove` — a [squash, a rebase or a
cherry-pick](#squash-merges) counts too; `☐` means it isn't merged but every
commit is already on its upstream, so nothing here is waiting on you — it's waiting on a
review or on CI; `*` means it has uncommitted changes, which isn't safe to remove whatever
git makes of the branch or the remote, and `±` means the same on a merged branch — the changes
are the only thing in there that isn't on `main` yet; `✳` beats them all and means an
agent is working in there right now — see [Herdr integration](../herdr/index.md#the--marker).
The glyphs are omitted when stdout isn't a terminal, so piped output stays parseable. The tui
under Herdr puts a third ahead of both, the [agents' dot](../herdr/index.md#agent-dots).

## The order

The freshest worktree is on top. Freshness is the younger of two things: the worktree's
last commit, and the last line written to its newest agent session — so a worktree an
agent has been working in all afternoon without committing sorts above one whose commit is
newer but that nobody has touched since, and one an agent started twenty minutes ago sorts
to the top rather than to the bottom on its zero commits. `--sort=commit` is git's answer
on its own, which is what the list did before there was a choice; the default is
[configurable](../configuration.md#sort) and `--sort` overrides it, on both `ccwt list` and
`ccwt tui`. Under Herdr the worktrees open in a workspace come first, in the order of its
sidebar, and under `-g` so do the sections of the repos it has open; the rest follow as above.
In the tui the rest are folded away behind a `▸ … N more` row, which a click or `↵` opens.

## TOPIC

TOPIC says what the worktree is about, and its glyph says where that came from. `✳` is an
agent session — the newest transcript for that worktree, showing the last recap it produced
(`/recap`, or one the agent wrote on its own), or the first prompt you typed when it never
recapped. `⎇` is the last commit, for a worktree nobody has run a session in.

AGENT and TOPIC's `✳` both read Claude Code's own transcripts, so an agent that files its
sessions somewhere else reads as `no` with its last commit for a topic — the worktree, and
everything else the row says about it, is the same either way.

## Fitting the terminal

The table is sized to your terminal, so it never wraps. BRANCH is kept narrow even when
there's room — it's usually the worktree's own name with a `worktree-` in front — and the
space goes to TOPIC. Branches and commit subjects lose their middle rather than their tail,
since the last word is usually what tells them apart, while a session summary simply stops:

```
    NAME                     BRANCH                   AGE  AGENT  TOPIC
* * dreamy-foraging-hickey   worktree-dreamy-…-hickey 2h   yes    ✳ Goal was a widget on…
  ✓ calm-baking-otter        worktree-calm-b…-otter   1d   no     ⎇ Fix the flux ca… (#41)
```

Which columns are drawn at all is [configurable](../configuration.md#columns) too.

## Squash merges

Squash, rebase and cherry-pick all rewrite commit SHAs, so the branch a PR landed from is
no ancestor of `main` afterwards — which is how most projects merge, and it leaves `ccwt`
refusing to remove worktrees whose work is long since shipped.

If the `git-tree-merged` plugin is on your `PATH`, `ccwt` asks
it instead: it compares tree hashes rather than SHAs, so a branch whose content is on
`main` byte for byte reads as merged however the history was rewritten. Without it `ccwt`
falls back to plain ancestry, which errs the safe way — a missed `✓`, never a wrong one.
