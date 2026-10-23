# Where a review stands

`ccwt mr <thing>` (or `ccwt pr`) says where a review stands and what its pipeline did,
without opening a browser: a table with a row per merge request, saying

- where each one `STATUS` stands — `merged`, `can be merged`, `conflict`, `needs approval`,
  `open comments`, `merge train` for one queued on a merge train or waiting on a pipeline to
  join it — GitLab itself calls that `ci still running`, which reads as a pipeline to go and
  wait on when it's the train's own and there is nothing to do about it — or whatever else
  GitLab's own mergeability check says, spelled out (`need rebase`, `ci still running`);
- who has `APPROVED` it, by username (an access token's bot by its name, `ci[bot]`, since
  its username is a hash; the column isn't drawn when nobody has approved any of them);
- what its `PIPELINE` did: `green`, `running`, or `failed: lint, test, …`, naming the first
  few failed jobs;
- and the `ENV` it is running on — the environments whose current deployment carries the
  commit this merge request landed as (`stg`, `stg, prod`), which is empty until it's merged
  *and* released. [`[environments]`](../configuration.md#environments) in the config file
  renames them for the column (`production = "prod"`).

## What to ask about

`<thing>` is either a merge request url, which gets a table of the one row, or a Jira key
(`PROJ-1234`) or its browse url, which gets a row per merge request that names the ticket,
with the title to tell them apart. Left out entirely it's the merge request of the branch
you're standing on; `-` reads a list of things off stdin, one per line, and puts all of
their merge requests in the one table (a merge request that answers to two of them is still
one row).

## On a terminal and in a pipe

On a terminal the `MR` column is a link to the merge request, a `merged` status is green —
the one word either table spends colour on, so that the colour still means something when it
turns up — and a spinner on stderr says the lookups are still going; piped or redirected, the
table comes out as markdown — a rule under the header, the `MR` column as a markdown link,
and nothing shortened to fit a width nothing has — so it lands in a merge request comment, a
ticket or a chat message as a table rather than as columns that only line up in a monospace
font.

A merge request already `merged` or `closed` has no pipeline reported — it's history, and
there's nothing to do about it either way — when none of the rows has one the `PIPELINE`
column isn't drawn at all. `ENV` is always drawn, empty for a merge request that isn't
running anywhere.

## Environments

`--no-environments` (it's on by default) fills `ENV` with `??` instead and sends none of
those lookups — a deployments call per project and a merge-base call per environment — for
when the answer isn't worth the round trips; `??` says nobody asked, which isn't what an
empty cell says. It's read off the project's deployments (newest first, so the first mention
of an environment is what's on it now) and answered by asking GitLab whether the
environment's commit has the merge request's in its history — "on it now", rather than
GitLab's own MR badge, which means "reached it at least once" and survives a rollback.

## GitLab and GitHub

It asks [`glab`](https://gitlab.com/gitlab-org/cli), which already knows the host and the
token; the ticket's merge requests come from GitLab's own search for the key, so one that
mentions the ticket only in a commit message or a comment isn't found.

A review on GitHub is a pull request, and gets the same table from
[`gh`](https://cli.github.com): a pull request url, or — with no argument, or a ticket — a
repo whose `origin` is on GitHub, which is told apart the way the tui's
[`m`](../tui/index.md#opening-the-review) tells it. `STATUS` is in the same words —
`ci still running` while checks nobody requires are still going, which GitHub itself counts
as mergeable — plus `draft`, `changes requested`, `behind`, and `merge queue` for GitHub's
merge train; one GitHub hasn't worked out yet is asked again a second later rather than
shown as `unknown`. `PIPELINE` is the checks on its last commit, and `ENV` the repo's
deployments, the same question asked of GitHub's compare. A ticket is searched for in the
pull requests of whoever owns `origin`'s repo, since all of github.com would be everybody's
tickets.

`ccwt ws` shows this table for its workspace's own branch, [at the top of its
window](../herdr/ws.md#the-review).
