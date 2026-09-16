package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

// `ccwt mr` answers the two questions you have about a review you are waiting
// on — can it go in, and did its pipeline pass — without opening a browser to
// find out. It takes either the merge request itself or the ticket it belongs
// to, and the ticket gets a row per merge request that names it.
//
// It asks `glab` rather than the api directly, for the reason the tui's `m`
// does (see forges): the cli already knows which host is which and which token
// to ask it with, and neither is worth reimplementing here.
//
// ponytail: the ticket half leans on the same cli too — gitlab's own search
// for the issue key — rather than on Jira's "mentioned on" list, which would
// mean a second host, a second token and a second cli to find the same merge
// requests. A merge request naming the ticket only in a commit message or a
// comment is the difference, and it is missed. Ask Jira for its remote links
// when that starts costing something.
type MrCmd struct {
	Thing string `arg:"" optional:"" help:"A merge request url, a Jira issue url, or a Jira key (PROJ-1234). \"-\" reads a list of those from stdin, one per line; left out, it's the merge request of the branch you're on."`
}

// mrURL is a merge request url: host, project path, iid. Gitlab's "/-/" is
// what keeps a project path of any depth apart from the page after it.
var mrURL = regexp.MustCompile(`^https?://([^/]+)/(.+?)/-/merge_requests/(\d+)`)

// ticketKey is a Jira issue, on its own or at the end of a browse url. Only
// tried once the url above hasn't matched, so an issue-shaped fragment of a
// merge request url can't be taken for a ticket.
var ticketKey = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-\d+\b`)

func (c *MrCmd) Run() error {
	stop := spin()
	rows, subject, err := c.look()
	stop()
	if err != nil {
		return err
	}
	// Fit the table to the terminal only when there is one, as `list` does.
	width := 0
	if stdoutIsTTY() {
		width, _, _ = term.GetSize(int(os.Stdout.Fd()))
	}
	for _, line := range mrTable(subject, rows, width) {
		fmt.Println(line)
	}
	return nil
}

// look is every merge request the arguments come to, in one table. What was
// asked about comes back alongside them, since arguments nothing answers to
// have to be said by name.
//
// The lookups go out together, as a ticket's own do: each is a round trip or
// two, and a list piped in is as long as the list is.
func (c *MrCmd) look() ([]mrRow, string, error) {
	things, err := c.things()
	if err != nil {
		return nil, "", err
	}
	found := make([][]mrRow, len(things))
	errs := make([]error, len(things))
	var wg sync.WaitGroup
	for i, thing := range things {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found[i], errs[i] = lookThing(thing)
		}()
	}
	wg.Wait()
	rows := slices.Concat(found...)
	// One merge request can answer to several of them — two tickets it
	// mentions, or a url and the ticket that names it — and is one row. By
	// url, since a shortened ref is no longer unique on its own.
	seen := map[string]bool{}
	rows = slices.DeleteFunc(rows, func(r mrRow) bool {
		key := cmp.Or(r.url, r.ref)
		dup := seen[key]
		seen[key] = true
		return dup
	})
	return rows, strings.Join(things, ", "), errors.Join(errs...)
}

// things is what to look up: the argument as given, the lines on stdin when
// it is "-" — so a list of tickets or urls can be piped in — or, with no
// argument at all, the merge request of the branch you are standing on, which
// is the one you were about to go and paste.
func (c *MrCmd) things() ([]string, error) {
	switch c.Thing {
	case "":
		u, err := branchMR()
		return []string{u}, err
	case "-":
		var things []string
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			if t := strings.TrimSpace(in.Text()); t != "" {
				things = append(things, t)
			}
		}
		if err := in.Err(); err != nil {
			return nil, err
		}
		if len(things) == 0 {
			return nil, errors.New("nothing on stdin to look up")
		}
		return things, nil
	}
	return []string{c.Thing}, nil
}

// lookThing is the merge requests one argument comes to: the single one its
// url points at, or every one that mentions the ticket it is.
func lookThing(thing string) ([]mrRow, error) {
	if m := mrURL.FindStringSubmatch(thing); m != nil {
		iid, _ := strconv.Atoi(m[3])
		host, project := m[1], url.PathEscape(m[2])
		// The merge request and what the project has deployed go out
		// together: the url already says which project, and neither answer
		// is any use without the other.
		var envs []env
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			envs = environments(host, project)
		}()
		got, err := fetchMR(host, project, iid)
		wg.Wait()
		if err != nil {
			return nil, err
		}
		// One of these two asks anything: a merge request that has landed
		// reports no pipeline, and one that hasn't is running nowhere.
		return []mrRow{got.row(pipelineNote(host, got), deployedTo(host, project, got, envs))}, nil
	}
	if key := ticketKey.FindString(thing); key != "" {
		return ticketMRs(key)
	}
	return nil, fmt.Errorf("%q is neither a merge request url nor a jira issue", thing)
}

// branchMR is the merge request of the branch checked out here, as the tui's
// `m` key finds it (see forges): glab already knows the branch, the host and
// the project, and the url it answers with goes back through lookThing like
// any other argument.
func branchMR() (string, error) {
	out, err := exec.Command("glab", forges["glab"].argv...).Output()
	if err != nil {
		return "", fmt.Errorf("no merge request for this branch: %s", glabError(out, err))
	}
	return strings.TrimSpace(string(out)), nil
}

// mrRow is one merge request as the table shows it. One merge request is a
// table of one row: the columns are what the answer is, and a lone answer
// laid out differently is one more shape to read.
//
// The url isn't a column — it's what the ref column links to.
type mrRow struct {
	ref, url, title, status, pipeline, env string
}

// mr is what the api says about a merge request, cut down to what the row
// needs.
type mr struct {
	IID        int    `json:"iid"`
	ProjectID  int    `json:"project_id"`
	State      string `json:"state"`
	Merge      string `json:"merge_status"`
	Detailed   string `json:"detailed_merge_status"`
	Title      string `json:"title"`
	WebURL     string `json:"web_url"`
	SquashSHA  string `json:"squash_commit_sha"`
	MergeSHA   string `json:"merge_commit_sha"`
	References struct {
		Full string `json:"full"`
	} `json:"references"`
	Pipeline *headPipeline `json:"head_pipeline"`
}

// row is the merge request as the table shows it, bar the two columns that
// are answers to further questions: what its pipeline did, and what is
// running its commit.
func (m mr) row(pipeline, env string) mrRow {
	return mrRow{
		ref:      shortRef(cmp.Or(m.References.Full, fmt.Sprintf("!%d", m.IID))),
		url:      m.WebURL,
		title:    m.Title,
		status:   mrStatus(m),
		pipeline: pipeline,
		env:      env,
	}
}

// shortRef takes the middle out of a merge request's reference:
// "acme/tools/backend/api!2523" reads "acme/…/api!2523".
// What tells one project from another is its own name and the group at the
// top; the levels in between are the same for every project in that group, so
// all they do is push the columns that say something off the side.
//
// ponytail: the whole middle, however short it is. A rule that kept a level
// when it happened to be narrow would mean two refs of the same depth lining
// up differently, which is harder to read down a column than one that is
// always the same shape.
func shortRef(ref string) string {
	path, iid, ok := strings.Cut(ref, "!")
	parts := strings.Split(path, "/")
	if !ok || len(parts) < 3 {
		return ref
	}
	return parts[0] + "/…/" + parts[len(parts)-1] + "!" + iid
}

// settled reports whether the merge request is done being one: in, or
// dropped. Nothing about it is going to change, and — the point here — it
// reports no pipeline, which is what makes a search hit enough on its own.
func settled(m mr) bool { return m.State == "merged" || m.State == "closed" }

// headPipeline is the last pipeline run on the merge request, or nil when
// none has been. The search endpoint always answers nil here, which is what
// sends a merge request still going anywhere through a lookup of its own.
type headPipeline struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
}

// fetchMR is one merge request in full. project is whatever addresses it in
// an api path: the escaped path from a url, or the numeric id the search
// hands back.
func fetchMR(host, project string, iid int) (mr, error) {
	var m mr
	err := glabAPI(host, fmt.Sprintf("projects/%s/merge_requests/%d", project, iid), &m)
	return m, err
}

// ticketMRs is every merge request that names the ticket, newest project and
// iid last, each looked up in full so its pipeline comes with it. The lookups
// go out together: a handful of round trips one after another is most of the
// wait, and there is nothing to order them by.
func ticketMRs(key string) ([]mrRow, error) {
	var hits []mr
	if err := glabAPI("", "search?scope=merge_requests&search="+url.QueryEscape(key), &hits); err != nil {
		return nil, err
	}
	slices.SortFunc(hits, func(a, b mr) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.IID, b.IID))
	})
	// What a project has deployed is the project's, not the merge request's,
	// so it is asked once per project however many rows came out of it. The
	// hits are in project order, so the duplicates are already adjacent.
	projects := make([]int, len(hits))
	for i, h := range hits {
		projects[i] = h.ProjectID
	}
	projects = slices.Compact(projects)

	// The first round: what each project is running, and the full merge
	// request for any that is still going somewhere. A settled one needs no
	// second look — the search hit carries every column it has, and the
	// pipeline it doesn't carry is the pipeline it wouldn't report.
	envs := make([][]env, len(projects))
	full := slices.Clone(hits)
	errs := make([]error, len(hits))
	var wg sync.WaitGroup
	for i, p := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			envs[i] = environments("", strconv.Itoa(p))
		}()
	}
	for i, h := range hits {
		if settled(h) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := fetchMR("", strconv.Itoa(h.ProjectID), h.IID); err == nil {
				full[i] = got
			} else {
				errs[i] = err
			}
		}()
	}
	wg.Wait()

	// The second round: the two columns that are questions about the first
	// round's answers — which jobs a failed pipeline failed, and which of the
	// environments are running what this one landed as.
	rows := make([]mrRow, len(hits))
	for i, m := range full {
		wg.Add(1)
		go func() {
			defer wg.Done()
			project := strconv.Itoa(hits[i].ProjectID)
			rows[i] = m.row(pipelineNote("", m), deployedTo("", project, m, envs[slices.Index(projects, hits[i].ProjectID)]))
		}()
	}
	wg.Wait()
	return rows, errors.Join(errs...)
}

// env is one of a project's environments and the commit on it: the sha its
// last successful deployment carried, which is what "has this merge request
// reached it" gets asked against.
type env struct{ name, sha string }

// environments is what a project deploys to and what each one is running,
// read off its deployments newest first: the first time an environment's name
// comes up is its current deployment. The environments endpoint itself won't
// do — its list leaves out last_deployment, so it would be a call per
// environment to find out what is on them.
//
// Names come out as the config's `[environments]` calls them, so two that it
// gives the same name collapse into the one column entry, newest first.
//
// ponytail: one page of deployments, so an environment nothing has deployed
// to in the last hundred goes unseen. Ask that environment directly when a
// project deploys often enough for it to matter.
//
// A lookup that fails is an empty column rather than an error: nobody asked
// about environments, they asked about a merge request.
func environments(host, project string) []env {
	var deployed []struct {
		SHA         string `json:"sha"`
		Environment struct {
			Name string `json:"name"`
		} `json:"environment"`
	}
	path := "projects/" + project + "/deployments?status=success&order_by=id&sort=desc&per_page=100"
	if err := glabAPI(host, path, &deployed); err != nil {
		return nil
	}
	cfg, _ := loadConfig() // a config we can't read renames nothing
	var envs []env
	for _, d := range deployed {
		name := cmp.Or(cfg.Environments[d.Environment.Name], d.Environment.Name)
		if !slices.ContainsFunc(envs, func(e env) bool { return e.name == name }) {
			envs = append(envs, env{name, d.SHA})
		}
	}
	return envs
}

// deployedTo is the ENV column: the environments running the commit this
// merge request landed as. One that hasn't landed is running nowhere, however
// many environments the project has — which is the whole of what the column
// is for, and what makes it worth asking per merge request rather than per
// project.
//
// The commit is the squashed one where there is one, since that is what a
// squash-merging project puts on the target branch, and the merge commit
// otherwise.
func deployedTo(host, project string, m mr, envs []env) string {
	sha := cmp.Or(m.SquashSHA, m.MergeSHA)
	if m.State != "merged" || sha == "" || len(envs) == 0 {
		return ""
	}
	on := make([]string, len(envs))
	var wg sync.WaitGroup
	for i, e := range envs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if carries(host, project, e.sha, sha) {
				on[i] = e.name
			}
		}()
	}
	wg.Wait()
	return strings.Join(slices.DeleteFunc(on, func(name string) bool { return name == "" }), ", ")
}

// carries reports whether head has commit in its history — git calls it an
// ancestor, and gitlab answers it as the merge base of the two being the
// commit itself.
//
// This is "is it on that environment now" rather than gitlab's own badge,
// which is "it reached there at least once": a deployment links the merge
// requests it brought and nothing ever unlinks them, so a rollback leaves the
// badge behind. The history of what is deployed right now can't lie that way.
func carries(host, project, head, commit string) bool {
	var base struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("projects/%s/repository/merge_base?refs[]=%s&refs[]=%s", project, commit, head)
	if err := glabAPI(host, path, &base); err != nil {
		return false
	}
	return base.ID == commit
}

// mrStatus is the one thing you want to know about an open merge request:
// what is holding it. detailed_merge_status is gitlab's own answer — it runs
// the mergeability checks in order and names the first one that failed — so
// this only puts the ones we have a word for into that word, and spells the
// rest out as they come ("need rebase", "ci still running").
func mrStatus(m mr) string {
	switch m.State {
	case "merged", "closed", "locked":
		return m.State
	}
	switch m.Detailed {
	case "mergeable":
		return "can be merged"
	case "conflict", "broken_status":
		return "conflict"
	case "not_approved":
		return "needs approval"
	case "discussions_not_resolved":
		return "open comments"
	}
	// A gitlab too old for detailed_merge_status still has the coarse version
	// of the same question, and "unknown" is what's left when it has neither.
	return cmp.Or(words(m.Detailed), words(m.Merge), "unknown")
}

// pipelineNote is what the head pipeline did, and for a failed one which jobs
// failed. Nothing ran is "", so the line simply doesn't mention a pipeline.
//
// Neither does a merge request that has already gone in or been dropped: its
// pipeline is history, there is nothing to do about it either way, and the
// jobs behind a failed one aren't worth the round trip to name.
func pipelineNote(host string, m mr) string {
	if m.Pipeline == nil || settled(m) {
		return ""
	}
	switch s := m.Pipeline.Status; s {
	case "success":
		return "green"
	case "running", "pending", "created", "preparing", "scheduled", "waiting_for_resource":
		return "running"
	case "failed":
		return "failed" + jobsNote(failedJobs(host, m.ProjectID, m.Pipeline.ID))
	default:
		return words(s) // canceled, skipped, manual
	}
}

// shownJobs is how many failures a line names before giving up and saying
// there are more: a pipeline that breaks early fails dozens of jobs, and the
// answer has to stay one line.
const shownJobs = 3

func jobsNote(jobs []string) string {
	if len(jobs) == 0 {
		return ""
	}
	shown := jobs[:min(len(jobs), shownJobs)]
	for i, j := range shown {
		shown[i] = truncate(j, 28) // job names carry whole argv sometimes
	}
	note := ": " + strings.Join(shown, ", ")
	if len(jobs) > len(shown) {
		note += ", …"
	}
	return note
}

// failedJobs names the jobs that failed in a pipeline. A lookup that fails is
// simply no names: the status already said "failed", which is the part that
// matters, and a second error on top of it helps nobody.
//
// Jobs allowed to fail are left out — the pipeline failed despite them, so
// they are not what to go and look at.
//
// ponytail: this pipeline's own jobs only. A child pipeline's failures sit
// behind a bridge and don't appear here; follow the bridges if a monorepo
// makes that the usual shape.
func failedJobs(host string, project, pipeline int) []string {
	var jobs []struct {
		Name    string `json:"name"`
		Allowed bool   `json:"allow_failure"`
	}
	path := fmt.Sprintf("projects/%d/pipelines/%d/jobs?scope[]=failed&per_page=100", project, pipeline)
	if err := glabAPI(host, path, &jobs); err != nil {
		return nil
	}
	var names []string
	for _, j := range jobs {
		if !j.Allowed {
			names = append(names, j.Name)
		}
	}
	return names
}

// mrTable lays the merge requests out the way `ccwt list` lays out the
// worktrees, one row each: where it stands, and what its pipeline did.
func mrTable(subject string, rows []mrRow, width int) []string {
	if len(rows) == 0 {
		return []string{"no merge requests mention " + subject}
	}
	// TITLE goes last: it is the longest of them and the one that reads
	// fine cut short, so it takes the shortening — and the answers you came
	// for keep their place on the left however long it is.
	cols := []column{
		{name: "MR", cut: elide, max: 44},
		{name: "STATUS"},
		{name: "PIPELINE", cut: truncate, pipe: 60}, // room for the failed jobs, which jobsNote has already bounded
		{name: "ENV", cut: truncate, max: 30},
		{name: "TITLE", cut: truncate, pipe: 50},
	}
	table := [][]string{{"MR", "STATUS", "PIPELINE", "ENV", "TITLE"}}
	for _, r := range rows {
		table = append(table, []string{r.ref, r.status, r.pipeline, r.env, r.title})
	}
	// Merge requests that are all in already have nothing to put under
	// PIPELINE, and a column of blanks under a header is just the header, so
	// it goes — as PROJECT does when the worklog is one project's.
	//
	// ENV stays whether anything fills it or not: a project that deploys
	// nowhere is a thing to see rather than a column to hide.
	const pipelineCol = 2
	if !slices.ContainsFunc(rows, func(r mrRow) bool { return r.pipeline != "" }) {
		cols = slices.Delete(cols, pipelineCol, pipelineCol+1)
		for i, row := range table {
			table[i] = slices.Delete(row, pipelineCol, pipelineCol+1)
		}
	}
	fitTable(table, width, cols)

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	for _, r := range table {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

	// The MR column is the link to the merge request, so the table you read
	// the answer off is also the way to go and look at it. The sequence goes
	// on last, once the columns are padded: it takes no room on screen, but
	// both fitTable and the tabwriter measure cells in characters and would
	// lay the table out around it.
	for i, r := range rows {
		if rest, ok := strings.CutPrefix(lines[i+1], table[i+1][0]); ok {
			lines[i+1] = hyperlink(r.url, table[i+1][0]) + rest
		}
	}
	return lines
}

// spin draws a spinner until the function it returns is called, so that a
// handful of gitlab round trips look like something happening rather than
// like nothing. Just the spinner: what it is waiting on is one thing the
// whole time, and naming it would only be a word to read and discard.
//
// It goes on stderr and rubs itself out afterwards, leaving the
// table on stdout to be piped, redirected or read as if it was never there —
// and it doesn't draw at all when stderr isn't a terminal.
//
// The first frame lands a tick in rather than at once, so a lookup that comes
// straight back doesn't flash a spinner on the way.
//
// ponytail: no progress package. This is the whole of what one would give us
// here — one line, one phase, no percentage to report — and the repo already
// writes its own escape sequences (see emitOSC7).
func spin() (stop func()) {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return func() {}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		tick := time.NewTicker(80 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-done:
				fmt.Fprint(os.Stderr, "\r\x1b[K") // the line, as we found it
				return
			case <-tick.C:
				fmt.Fprintf(os.Stderr, "\r%c", frames[i%len(frames)])
			}
		}
	}()
	return func() { close(done); <-finished }
}

// hyperlink makes text a link to url, the way OSC 8 does it — which terminals
// that know the sequence render as something to click and the rest leave
// alone. Not a terminal at all is the case that matters: an escape sequence
// in a pipe is something for `cut` or `grep` to trip over, so redirected
// output stays the plain text it was.
func hyperlink(url, text string) string {
	if url == "" || !stdoutIsTTY() {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// glabAPI asks one gitlab api path and decodes the answer into v. The host is
// named only when the url said which gitlab it is: left out, glab uses the
// remote of the repo we are standing in, or its own default host — which is
// the right guess for a ticket, since a ticket doesn't say.
func glabAPI(host, path string, v any) error {
	args := []string{"api"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	out, err := exec.Command("glab", append(args, path)...).Output()
	if err != nil {
		return fmt.Errorf("glab api %s: %s", path, glabError(out, err))
	}
	return json.Unmarshal(out, v)
}

// glabError is what went wrong, as glab said it. It says it twice: once on
// stdout as json, and once on stderr as a wrapped block under an "ERROR"
// banner. The json is the one to quote — it is a line rather than a paragraph,
// and it is the message glab meant — and the block is what's left when there
// is no json, minus the decoration, which says nothing an error message in an
// error message needs to say.
func glabError(out []byte, err error) string {
	var said struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &said) == nil && said.Error.Message != "" {
		first, _, _ := strings.Cut(said.Error.Message, "\n") // the rest is advice
		return first
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		for _, line := range strings.Split(string(ee.Stderr), "\n") {
			if l := strings.TrimSpace(line); l != "" && l != "ERROR" {
				return l
			}
		}
	}
	return err.Error()
}

func words(s string) string { return strings.ReplaceAll(s, "_", " ") }
