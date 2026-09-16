package main

import (
	"cmp"
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

	if got, want := glabError(stdout, failed), "none of the git remotes match"; got != want {
		t.Errorf("glabError = %q, want %q", got, want)
	}
	if got, want := glabError(nil, failed), "None of the git remotes match. Try adding one."; got != want {
		t.Errorf("glabError with no json = %q, want %q", got, want)
	}
	if got, want := glabError(nil, errors.New("exec: \"glab\": not found")), "exec: \"glab\": not found"; got != want {
		t.Errorf("glabError with no glab at all = %q, want %q", got, want)
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
		if got := deployedTo("", "1", tc.m, tc.envs); got != "" {
			t.Errorf("deployedTo of one that %s = %q, want nothing", tc.why, got)
		}
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

// The table is a row per merge request, whether the argument named a ticket's
// worth of them or just the one — and a ticket no merge request mentions says
// so instead.
func TestMrTableIsARowPerMergeRequest(t *testing.T) {
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
// still line up: the sequence around the text is no wider than the text. Off
// one — a pipe, a file — the escape would be someone else's problem, so the
// table stays as it reads.
func TestMrTableLinksTheMergeRequest(t *testing.T) {
	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	rows := []mrRow{
		{ref: "api!2596", url: "https://gitlab.example.com/acme/tools/backend/api/-/merge_requests/2596", title: "a widget", status: "merged"},
		{ref: "deploy!280", title: "a fix", status: "merged"}, // no url: nothing to link to
	}

	stdoutIsTTY = func() bool { return true }
	linked := mrTable("PROJ-9", rows, 0)
	stdoutIsTTY = func() bool { return false }
	plain := mrTable("PROJ-9", rows, 0)

	if want := "\x1b]8;;" + rows[0].url + "\x1b\\api!2596\x1b]8;;\x1b\\"; !strings.HasPrefix(linked[1], want) {
		t.Errorf("line 1 = %q, want it to start with the link %q", linked[1], want)
	}
	if strings.Contains(linked[2], "\x1b") {
		t.Errorf("line 2 = %q, want no link on a row with no url", linked[2])
	}
	if got := strings.Join(plain, ""); strings.Contains(got, "\x1b") {
		t.Errorf("mrTable off a terminal = %q, want no escapes in it", plain)
	}
	// Taking the sequences back out gives exactly the table without them, so
	// the link cost the layout nothing.
	strip := strings.NewReplacer("\x1b]8;;"+rows[0].url+"\x1b\\", "", "\x1b]8;;\x1b\\", "")
	for i := range linked {
		if got := strip.Replace(linked[i]); got != plain[i] {
			t.Errorf("line %d = %q with links, %q without: the link took up room", i, got, plain[i])
		}
	}
}
