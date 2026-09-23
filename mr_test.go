package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// `ccwt mr` takes whichever of the two things you have in your hand: the
// merge request's url, which says on its own which gitlab and which project,
// or the ticket — a key, or the browse url it sits at the end of. Nothing
// else is either, and must say so rather than be searched for.
func TestMrTakesUrlsAndTickets(t *testing.T) {
	const mrLink = "https://gitlab.example.com/acme/tools/backend/api/-/merge_requests/2596"
	m := mrURL.FindStringSubmatch(mrLink)
	if m == nil {
		t.Fatalf("mrURL didn't match %q", mrLink)
	}
	iid, _ := strconv.Atoi(m[3])
	if m[1] != "gitlab.example.com" || m[2] != "acme/tools/backend/api" || iid != 2596 {
		t.Errorf("mrURL = %q, want the host, the project path and 2596", m[1:])
	}
	// The project path goes into an api path, where its slashes have to stop
	// being slashes.
	if got, want := url.PathEscape(m[2]), "acme%2Ftools%2Fbackend%2Fapi"; got != want {
		t.Errorf("escaped project = %q, want %q", got, want)
	}

	for _, thing := range []string{"PROJ-1234", "https://jira.example.com/browse/PROJ-1234"} {
		if mrURL.MatchString(thing) {
			t.Errorf("%q read as a merge request url", thing)
		}
		if got := ticketKey.FindString(thing); got != "PROJ-1234" {
			t.Errorf("ticketKey(%q) = %q, want PROJ-1234", thing, got)
		}
	}
	for _, thing := range []string{"proj-1234", "just some words", mrLink} {
		if got := ticketKey.FindString(strings.TrimPrefix(thing, "https://")); thing != mrLink && got != "" {
			t.Errorf("ticketKey(%q) = %q, want nothing", thing, got)
		}
	}

	// A pull request's url says which github and which repo, and is its own
	// url whichever of its pages the link went to. Neither kind of url passes
	// for the other.
	p := prURL.FindStringSubmatch("https://github.com/acme/api/pull/42/files#diff-1")
	if p == nil || p[0] != "https://github.com/acme/api/pull/42" || p[1] != "github.com" || p[2] != "acme/api" {
		t.Errorf("prURL = %q, want the pull request's url, github.com and acme/api", p)
	}
	if prURL.MatchString(mrLink) || mrURL.MatchString(p[0]) {
		t.Errorf("a merge request url and a pull request url read as each other")
	}
}

// "-" takes the list off stdin, a thing per line, so a list of tickets or
// urls from somewhere else lands in one table. Blank lines and the whitespace
// around a line aren't things to look up.
func TestMrTakesAListOnStdin(t *testing.T) {
	in, err := os.CreateTemp(t.TempDir(), "things")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString("PROJ-1234\n\n  https://gl.example/g/p/-/merge_requests/7  \n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	defer func(orig *os.File) { os.Stdin = orig }(os.Stdin)
	os.Stdin = in

	got, err := (&MrCmd{Thing: "-"}).things()
	want := []string{"PROJ-1234", "https://gl.example/g/p/-/merge_requests/7"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("things() = %q, %v, want %q", got, err, want)
	}
	// An argument that is a thing is that one thing, whatever stdin holds.
	if got, _ := (&MrCmd{Thing: "PROJ-9"}).things(); !slices.Equal(got, []string{"PROJ-9"}) {
		t.Errorf("things() of an argument = %q, want just it", got)
	}
}

// glab says what went wrong twice: once as json on stdout, once as a wrapped
// block under a banner on stderr. Quote the json when it's there, and when it
// isn't, quote the block without its decoration.
func TestMrQuotesGlabsOwnError(t *testing.T) {
	stdout := []byte(`{"error":{"message":"none of the git remotes match\n\nGITLAB_HOST is set to gl.example"}}`)
	stderr := []byte("\n   ERROR  \n\n  None of the git remotes match. Try adding one.\n")
	failed := &exec.ExitError{Stderr: stderr, ProcessState: &os.ProcessState{}}

	if got, want := cliError(stdout, failed), "none of the git remotes match"; got != want {
		t.Errorf("cliError = %q, want %q", got, want)
	}
	if got, want := cliError(nil, failed), "None of the git remotes match. Try adding one."; got != want {
		t.Errorf("cliError with no json = %q, want %q", got, want)
	}
	if got, want := cliError(nil, errors.New("exec: \"glab\": not found")), "exec: \"glab\": not found"; got != want {
		t.Errorf("cliError with no glab at all = %q, want %q", got, want)
	}
	// gh says it once, on stderr. The json it prints is github's own error,
	// which isn't glab's shape and mustn't be taken for it.
	ghFailed := &exec.ExitError{Stderr: []byte("gh: Not Found (HTTP 404)\n"), ProcessState: &os.ProcessState{}}
	if got, want := cliError([]byte(`{"message":"Not Found","status":"404"}`), ghFailed), "gh: Not Found (HTTP 404)"; got != want {
		t.Errorf("cliError of gh = %q, want %q", got, want)
	}
}

// ENV is the environments running what this merge request landed as, so a
// merge request that hasn't landed is on none of them however many the
// project has. It doesn't take asking gitlab to know that, and mustn't: the
// project's environment list is not this column.
func TestMrNotMergedIsOnNoEnvironment(t *testing.T) {
	deployed := []env{{"staging", "5ee7ab1"}, {"production", "0b4279d"}}
	for _, tc := range []struct {
		m    mr
		envs []env
		why  string
	}{
		{mr{State: "opened", SquashSHA: "56cb2be"}, deployed, "hasn't landed yet"},
		{mr{State: "closed", MergeSHA: "8edbede"}, deployed, "never landed"},
		{mr{State: "merged"}, deployed, "landed as no commit it named"},
		{mr{State: "merged", SquashSHA: "56cb2be"}, nil, "landed, but nothing deploys"},
	} {
		// None of these asks gitlab anything, which is the point: an answer
		// that is "none" whatever is deployed is not a question.
		if got := deployedTo("", "1", tc.m, tc.envs, true); got != "" {
			t.Errorf("deployedTo of one that %s = %q, want nothing", tc.why, got)
		}
	}
	// A pull request the same, without asking github.
	for _, p := range []pr{{State: "OPEN"}, {State: "CLOSED"}, {State: "MERGED"}} {
		if got := prDeployedTo("", p, deployed, true); got != "" {
			t.Errorf("prDeployedTo of one %s with no merge commit = %q, want nothing", p.State, got)
		}
	}
}

// --no-gitlab-environments doesn't leave the column empty: empty is "running
// nowhere", and not having looked is a different answer. Nothing is asked to
// arrive at it, which is the point of turning it off.
func TestMrEnvironmentsOffSaysUnknown(t *testing.T) {
	m := mr{State: "merged", SquashSHA: "56cb2be"}
	if got := deployedTo("", "1", m, []env{{"staging", "5ee7ab1"}}, false); got != "??" {
		t.Errorf("deployedTo with environments off = %q, want %q", got, "??")
	}
	if got := environments("", "1", false); got != nil {
		t.Errorf("environments with them off = %v, want none asked for", got)
	}
	p := pr{State: "MERGED"}
	p.MergeCommit.OID = "56cb2be"
	if got := prDeployedTo("", p, []env{{"staging", "5ee7ab1"}}, false); got != "??" {
		t.Errorf("prDeployedTo with environments off = %q, want %q", got, "??")
	}
	if got := prEnvironments("", "acme/api", false); got != nil {
		t.Errorf("prEnvironments with them off = %v, want none asked for", got)
	}
}

// A reference loses its middle: the group at the top and the project's own
// name are what tell it apart, and the levels between them are the same for
// everything in that group. Anything already short enough to have no middle
// is left alone.
func TestMrRefLosesItsMiddle(t *testing.T) {
	for _, tc := range [][2]string{
		{"acme/tools/backend/api!2523", "acme/…/api!2523"},
		{"acme/tools/deploy!280", "acme/…/deploy!280"},
		{"group/project!7", "group/project!7"}, // no middle to lose
		{"!7", "!7"},                           // a project that named itself nothing
		{"not a reference", "not a reference"},
	} {
		if got := shortRef(tc[0]); got != tc[1] {
			t.Errorf("shortRef(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

// The ENV column calls an environment what the config calls it. A typo in the
// toml key would rename nothing and say nothing, so the binding is what this
// checks, rather than the lookup that uses it.
func TestMrEnvironmentsAreNamedByTheConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := filepath.Join(home, ".config", "ccwt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[environments]\nproduction = \"prod\"\nstaging = \"stg\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Environments["production"] != "prod" || got.Environments["staging"] != "stg" {
		t.Errorf("config environments = %v, want production renamed to prod and staging to stg", got.Environments)
	}
	// An environment the config says nothing about keeps gitlab's name for it.
	if name := cmp.Or(got.Environments["review/mkm"], "review/mkm"); name != "review/mkm" {
		t.Errorf("unnamed environment = %q, want it left alone", name)
	}
}

// The spinner is stderr's business and a terminal's: off one it draws nothing
// and stops without being waited for.
func TestMrSpinnerIsQuietOffATerminal(t *testing.T) {
	spin()() // it hangs or it doesn't
}

// A row says where the merge request stands and what its pipeline did. The
// standing is gitlab's own mergeability check put into the words you'd use
// about it, and a status we have no word for comes through as gitlab spells
// it rather than as a guess.
func TestMrStatusSaysWhatBlocksTheMerge(t *testing.T) {
	for _, tc := range []struct {
		m    mr
		want string
	}{
		{mr{State: "merged", Detailed: "not_open"}, "merged"},
		{mr{State: "closed", Detailed: "not_open"}, "closed"},
		{mr{State: "opened", Detailed: "mergeable"}, "can be merged"},
		{mr{State: "opened", Detailed: "broken_status"}, "conflict"},
		{mr{State: "opened", Detailed: "not_approved"}, "needs approval"},
		{mr{State: "opened", Detailed: "discussions_not_resolved"}, "open comments"},
		{mr{State: "opened", Detailed: "need_rebase"}, "need rebase"},
		{mr{State: "opened", Detailed: "ci_still_running"}, "ci still running"},
		// On a merge train the pipeline being waited on is the train's own,
		// so the train is what to say rather than gitlab's "ci still running".
		{mr{State: "opened", Detailed: "ci_still_running", AutoMerge: "merge_train"}, "merge train"},
		{mr{State: "opened", Detailed: "ci_still_running", AutoMerge: "add_to_merge_train_when_checks_pass"}, "merge train"},
		{mr{State: "opened", Detailed: "ci_still_running", AutoMerge: "merge_when_checks_pass"}, "ci still running"},
		{mr{State: "opened", Merge: "cannot_be_merged"}, "cannot be merged"}, // a gitlab too old to be detailed
		{mr{State: "opened"}, "unknown"},
	} {
		if got := mrStatus(tc.m); got != tc.want {
			t.Errorf("mrStatus(%+v) = %q, want %q", tc.m, got, tc.want)
		}
	}

	// A merge request nothing has run on has no pipeline to report, and nor
	// does one already in or already dropped: its pipeline is history, and
	// history isn't what you asked.
	done := mr{State: "merged", Detailed: "not_open", Pipeline: &headPipeline{ID: 1, Status: "success"}}
	if got := pipelineNote("", done); got != "" {
		t.Errorf("pipelineNote(merged) = %q, want nothing to say", got)
	}
	done.State = "opened"
	if got, want := pipelineNote("", done), "green"; got != want {
		t.Errorf("pipelineNote(opened) = %q, want %q", got, want)
	}

	// A broken pipeline fails more jobs than fit on a line, so the line names
	// a few and says there are more.
	jobs := []string{"unit", "build: [./cmd/a-long-binary-name-and-then-some]", "e2e", "lint", "vet"}
	got := "failed" + jobsNote(jobs)
	for _, want := range []string{"failed: unit, ", "e2e, …"} {
		if !strings.Contains(got, want) {
			t.Errorf("jobsNote = %q, want %q in it", got, want)
		}
	}
	if strings.Contains(got, "lint") || len(got) > 80 {
		t.Errorf("jobsNote = %q, want the long name cut and the tail dropped", got)
	}
	if got, want := "failed"+jobsNote(nil), "failed"; got != want {
		t.Errorf("jobsNote(nil) = %q, want %q", got, want)
	}
}

// A pull request's standing is said in the merge request's words. github's
// "blocked" is every protection rule at once, so the missing review — the one
// that is somebody else's to give — is picked out of it by name, and the rest
// comes through as github spells it.
func TestPrStatusSaysWhatBlocksTheMerge(t *testing.T) {
	for _, tc := range []struct {
		p    pr
		want string
	}{
		{pr{State: "MERGED", MergeState: "UNKNOWN"}, "merged"},
		{pr{State: "CLOSED", MergeState: "DIRTY"}, "closed"},
		{pr{State: "OPEN", MergeState: "CLEAN"}, "can be merged"},
		{pr{State: "OPEN", MergeState: "UNSTABLE"}, "can be merged"}, // failing checks nobody requires
		{pr{State: "OPEN", MergeState: "DIRTY"}, "conflict"},
		{pr{State: "OPEN", MergeState: "BLOCKED", Review: "REVIEW_REQUIRED"}, "needs approval"},
		{pr{State: "OPEN", MergeState: "BLOCKED", Review: "CHANGES_REQUESTED"}, "changes requested"},
		{pr{State: "OPEN", MergeState: "BLOCKED", Review: "APPROVED"}, "blocked"}, // its checks, which PIPELINE says
		{pr{State: "OPEN", MergeState: "BEHIND"}, "behind"},
		{pr{State: "OPEN", MergeState: "BLOCKED", Draft: true}, "draft"},
		{pr{State: "OPEN", MergeState: "BLOCKED", InMergeQueue: true}, "merge queue"},
		{pr{State: "OPEN"}, "unknown"},
	} {
		if got := prStatus(tc.p); got != tc.want {
			t.Errorf("prStatus(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

// A row is read off what the api answers, so the answer is what this starts
// from: a check run and a commit status are both checks, the ones that failed
// are named, and the ones that passed, were skipped or are still going aren't.
// Once it's in, its checks are history and PIPELINE says nothing.
func TestPrRowIsReadOffTheApi(t *testing.T) {
	const answer = `{
		"number": 42, "title": "a widget", "url": "https://github.com/acme/api/pull/42",
		"state": "OPEN", "isDraft": false, "isInMergeQueue": false,
		"mergeStateStatus": "BLOCKED", "reviewDecision": "REVIEW_REQUIRED",
		"mergeCommit": null, "repository": {"nameWithOwner": "acme/api"},
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE", "contexts": {"nodes": [
			{"name": "lint", "conclusion": "FAILURE"},
			{"name": "unit", "conclusion": "SUCCESS"},
			{"name": "docs", "conclusion": "SKIPPED"},
			{"name": "e2e", "conclusion": null},
			{"name": "ci/jenkins", "state": "ERROR"}
		]}}}}]}
	}`
	var p pr
	if err := json.Unmarshal([]byte(answer), &p); err != nil {
		t.Fatal(err)
	}
	want := mrRow{ref: "acme/api#42", url: "https://github.com/acme/api/pull/42", title: "a widget", status: "needs approval", pipeline: "failed: lint, ci/jenkins", env: "prod"}
	if got := p.row("prod"); got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}

	p.State = "MERGED"
	if got := prChecks(p); got != "" {
		t.Errorf("prChecks(merged) = %q, want nothing to say", got)
	}
	// Nothing ran is nothing to say either.
	p.State, p.Commits.Nodes = "OPEN", nil
	if got := prChecks(p); got != "" {
		t.Errorf("prChecks with no checks = %q, want nothing to say", got)
	}
}

// The table is a row per merge request, whether the argument named a ticket's
// worth of them or just the one — and a ticket no merge request mentions says
// so instead.
func TestMrTableIsARowPerMergeRequest(t *testing.T) {
	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return true } // the columns table; markdown has its own test
	rows := []mrRow{
		{ref: "acme/tools/backend/api!2596", url: "https://gitlab.example.com/acme/tools/backend/api/-/merge_requests/2596", title: "add a widget to the dashboard", status: "merged"},
		{ref: "acme/tools/backend/api!2670", title: "fix the flux capacitor", status: "needs approval", pipeline: "failed: e2e", env: "staging, production"},
	}
	lines := mrTable("PROJ-1234", rows, 0)
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "MR") || !strings.Contains(lines[0], "PIPELINE") {
		t.Fatalf("mrTable = %q, want a header and two rows", lines)
	}
	// TITLE is last: it's the long one, so it's the one that gets cut.
	if !strings.HasSuffix(lines[0], "TITLE") {
		t.Errorf("header = %q, want TITLE last", lines[0])
	}
	for i, want := range []string{"!2596", "!2670"} {
		if !strings.Contains(lines[i+1], want) {
			t.Errorf("line %d = %q, want %q on it", i+1, lines[i+1], want)
		}
	}
	if !strings.Contains(lines[2], "needs approval") || !strings.Contains(lines[2], "failed: e2e") {
		t.Errorf("line 2 = %q, want the status and the failing job on it", lines[2])
	}
	if !strings.Contains(lines[0], "ENV") || !strings.Contains(lines[2], "staging, production") {
		t.Errorf("header %q and line 2 %q, want the environments running it named", lines[0], lines[2])
	}
	if got := mrTable("PROJ-9", nil, 0); len(got) != 1 || !strings.Contains(got[0], "PROJ-9") {
		t.Errorf("empty mrTable = %q, want one line naming the ticket", got)
	}

	// One merge request is a table of one row — the same shape, not a second
	// one to read. Being in already, it has no PIPELINE to report and that
	// column goes; ENV stays, empty, because deploying nowhere is worth
	// seeing.
	lines = mrTable("PROJ-1234", []mrRow{{ref: "api!2596", title: "a widget", status: "merged"}}, 0)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "MR") || !strings.Contains(lines[1], "merged") {
		t.Fatalf("mrTable of one = %q, want a header and its row", lines)
	}
	if got, want := strings.Fields(lines[0]), []string{"MR", "STATUS", "ENV", "TITLE"}; !slices.Equal(got, want) {
		t.Errorf("header = %q, want %q", got, want)
	}
}

// On a terminal the MR column is a link to the merge request, and the columns
// still line up: the sequence around the text is no wider than the text.
func TestMrTableLinksTheMergeRequest(t *testing.T) {
	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return true }
	rows := []mrRow{
		{ref: "api!2596", url: "https://gitlab.example.com/acme/tools/backend/api/-/merge_requests/2596", title: "a widget", status: "merged"},
		{ref: "deploy!280", title: "a fix", status: "merged"}, // no url: nothing to link to
	}
	linked := mrTable("PROJ-9", rows, 0)

	if want := "\x1b]8;;" + rows[0].url + "\x1b\\api!2596\x1b]8;;\x1b\\"; !strings.HasPrefix(linked[1], want) {
		t.Errorf("line 1 = %q, want it to start with the link %q", linked[1], want)
	}
	if strings.Contains(linked[2], "\x1b]8;;") {
		t.Errorf("line 2 = %q, want no link on a row with no url", linked[2])
	}
	// Taking the sequences back out gives exactly the table of the same rows
	// with nothing to link to, so the link cost the layout nothing.
	bare := slices.Clone(rows)
	bare[0].url = ""
	plain := mrTable("PROJ-9", bare, 0)
	strip := strings.NewReplacer("\x1b]8;;"+rows[0].url+"\x1b\\", "", "\x1b]8;;\x1b\\", "")
	for i := range linked {
		if got := strip.Replace(linked[i]); got != plain[i] {
			t.Errorf("line %d = %q with links, %q without: the link took up room", i, got, plain[i])
		}
	}
}

// The table spends colour on one word, so that the word shows: a merge
// request that is in is green, and everything still asking something of you is
// the terminal's own foreground.
func TestMrTablePaintsMergedGreen(t *testing.T) {
	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return true }
	rows := []mrRow{
		{ref: "api!1", title: "a widget", status: "merged"},
		{ref: "api!2", title: "unmerged branches are not merged", status: "needs approval", pipeline: "green"},
	}
	lines := mrTable("PROJ-9", rows, 0)
	if want := "\x1b[92mmerged\x1b[0m"; !strings.Contains(lines[1], want) {
		t.Errorf("line 1 = %q, want %q on it", lines[1], want)
	}
	// The word in a title is a word in a title: the paint follows the status,
	// not the text, so nothing else on the screen goes green.
	if strings.Contains(lines[2], "\x1b") {
		t.Errorf("line 2 = %q, want nothing painted on a row that is not in yet", lines[2])
	}
	// And it costs the columns nothing — an escape is no width on screen but
	// plenty of characters to the tabwriter, so a table painted before it was
	// laid out is one whose painted rows have shifted right of the rest.
	if got, want := strings.Index(plain(lines[1]), "a widget"), strings.Index(lines[2], "unmerged"); got != want {
		t.Errorf("TITLE starts at %d on the painted row, %d on the plain one: the colour took up room", got, want)
	}
}

// Off a terminal — piped, redirected, pasted into a comment — the table is
// markdown, since that is what reads it there: a rule under the header, the
// MR column as a markdown link, and a pipe in a title escaped rather than
// left to end the cell early. Nothing is shortened to fit a width nothing
// has.
func TestMrTableIsMarkdownOffATerminal(t *testing.T) {
	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return false }
	const long = "fix the flux capacitor, which had been running backwards since the rewrite"
	rows := []mrRow{
		{ref: "acme/…/api!2596", url: "https://gitlab.example.com/acme/tools/backend/api/-/merge_requests/2596", title: "add a | to the table", status: "merged", env: "prod"},
		{ref: "acme/…/deploy!280", title: long, status: "needs approval", pipeline: "failed: e2e"},
	}

	want := []string{
		"| MR | STATUS | PIPELINE | ENV | TITLE |",
		"| --- | --- | --- | --- | --- |",
		`| [acme/…/api!2596](` + rows[0].url + `) | merged |  | prod | add a \| to the table |`,
		"| acme/…/deploy!280 | needs approval | failed: e2e |  | " + long + " |",
	}
	if got := mrTable("PROJ-1234", rows, 0); !slices.Equal(got, want) {
		t.Errorf("markdown mrTable =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// And the terminal's own escapes stay on the terminal.
	if got := strings.Join(mrTable("PROJ-1234", rows, 0), ""); strings.Contains(got, "\x1b") {
		t.Errorf("markdown mrTable = %q, want no escapes in it", got)
	}
}

// The token comes out of glab's config rather than out of `glab config get`,
// so the shape of that file is ours to get right: the token belongs to the
// host it is indented under, and a host the file doesn't hold one for has to
// come back empty — sending another gitlab's token is worse than asking glab.
func TestGlabConfigToken(t *testing.T) {
	const config = `# What protocol to use when performing Git operations.
git_protocol: ssh
host: gitlab.com
hosts:
    gitlab.com:
        api_host: gitlab.com
        # Your GitLab access token.
        token:
    code.example.com:
        token: glpat-thetokenitself
        user: someone
last_seen_version: v1.118.0
`
	for _, tc := range []struct{ host, want string }{
		{"code.example.com", "glpat-thetokenitself"},
		{"gitlab.com", ""},        // known, but has no token under it
		{"other.example", ""},     // not in the file at all
		{"user", ""},              // a setting under a host is not a host
		{"last_seen_version", ""}, // nor is a key outside the hosts block
	} {
		if got := configToken(config, tc.host); got != tc.want {
			t.Errorf("configToken(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
	if got := configToken("", "code.example.com"); got != "" {
		t.Errorf("configToken of no config = %q, want nothing", got)
	}
}
