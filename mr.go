package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// `ccwt mr` answers the two questions you have about a review you are waiting
// on — can it go in, and did its pipeline pass — without opening a browser to
// find out. It takes either the merge request itself or the ticket it belongs
// to, and the ticket gets a row per merge request that names it.
//
// It asks the api itself, with glab's token and glab's idea of which host
// (see glabAPI): signing in is still `glab auth login`, but a `glab api` per
// question was a process and a tls handshake per question, and those were
// most of the several seconds this used to take.
//
// ponytail: the ticket half leans on the same cli too — gitlab's own search
// for the issue key — rather than on Jira's "mentioned on" list, which would
// mean a second host, a second token and a second cli to find the same merge
// requests. A merge request naming the ticket only in a commit message or a
// comment is the difference, and it is missed. Ask Jira for its remote links
// when that starts costing something.
//
// A review on github is a pull request, and pr.go is the same questions asked
// of github; `ccwt pr` is this command under that name.
type MrCmd struct {
	Thing        string `arg:"" optional:"" help:"A merge request or pull request url, a Jira issue url, or a Jira key (PROJ-1234). \"-\" reads a list of those from stdin, one per line; left out, it's the review of the branch you're on."`
	Environments bool   `default:"true" negatable:"" help:"Ask what the environments are running — GitLab's environments, GitHub's deployments — for the ENV column. Off (--no-environments), ENV says \"??\" and those lookups — a handful of round trips per review — don't go out."`
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
		wg.Go(func() {
			found[i], errs[i] = lookThing(thing, c.Environments)
		})
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
// url points at, or every one that mentions the ticket it is. askEnvs is
// --environments: false and the ENV column is "??" rather than an answer,
// with nothing asked to fill it.
//
// A url says which forge it is on; a ticket doesn't, so it is searched for on
// the one origin points at.
func lookThing(thing string, askEnvs bool) ([]mrRow, error) {
	if m := mrURL.FindStringSubmatch(thing); m != nil {
		iid, _ := strconv.Atoi(m[3])
		host, project := m[1], url.PathEscape(m[2])
		// The merge request and what the project has deployed go out
		// together: the url already says which project, and neither answer
		// is any use without the other.
		var envs []env
		var wg sync.WaitGroup
		wg.Go(func() {
			envs = environments(host, project, askEnvs)
		})
		got, err := fetchMR(host, project, iid)
		wg.Wait()
		if err != nil {
			return nil, err
		}
		// One of these two asks anything: a merge request that has landed
		// reports no pipeline, and one that hasn't is running nowhere.
		return []mrRow{got.row(pipelineNote(host, got), deployedTo(host, project, got, envs, askEnvs))}, nil
	}
	if m := prURL.FindStringSubmatch(thing); m != nil {
		return lookPR(m[1], m[2], m[0], askEnvs)
	}
	if key := ticketKey.FindString(thing); key != "" {
		if origin := gitLine(".", "remote", "get-url", "origin"); onGitHub(urlHost(origin)) {
			return ticketPRs(urlHost(origin), urlPath(origin), key, askEnvs)
		}
		return ticketMRs(key, askEnvs)
	}
	return nil, fmt.Errorf("%q is not a merge request url, a pull request url or a jira issue", thing)
}

// branchMR is the merge request of the branch checked out here — the one
// whose source branch it is, most recently touched first — and the url it
// answers with goes back through lookThing like any other argument.
//
// The tui's `m` key asks `glab mr view` for the same thing (see forges) and
// this used to as well, but that is a process and two round trips of its own
// to answer what one query answers, and with no argument it is the first
// thing the table waits on. git already knows the branch and the remote.
//
// ponytail: the merge request in the project origin points at. One pushed
// from a fork belongs to the project it targets rather than the one it came
// from, so it isn't found here and the workspace reads as having none. Follow
// source_project_id when someone works that way.
func branchMR() (string, error) {
	origin := gitLine(".", "remote", "get-url", "origin")
	branch := gitLine(".", "rev-parse", "--abbrev-ref", "HEAD")
	project := urlPath(origin)
	if project == "" || branch == "" {
		return "", errors.New("no merge request for this branch: no origin remote or no branch here")
	}
	if onGitHub(urlHost(origin)) {
		return branchPR(urlHost(origin), project, branch)
	}
	query := url.Values{
		"source_branch": {branch},
		"order_by":      {"updated_at"},
		"sort":          {"desc"},
		"per_page":      {"1"},
	}
	var hits []mr
	path := "projects/" + url.PathEscape(project) + "/merge_requests?" + query.Encode()
	if err := glabAPI(urlHost(origin), path, &hits); err != nil {
		return "", fmt.Errorf("no merge request for this branch: %w", err)
	}
	if len(hits) == 0 {
		return "", errors.New("no merge request for this branch")
	}
	return hits[0].WebURL, nil
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
	AutoMerge  string `json:"auto_merge_strategy"`
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
func ticketMRs(key string, askEnvs bool) ([]mrRow, error) {
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
		wg.Go(func() {
			envs[i] = environments("", strconv.Itoa(p), askEnvs)
		})
	}
	for i, h := range hits {
		if settled(h) {
			continue
		}
		wg.Go(func() {
			if got, err := fetchMR("", strconv.Itoa(h.ProjectID), h.IID); err == nil {
				full[i] = got
			} else {
				errs[i] = err
			}
		})
	}
	wg.Wait()

	// The second round: the two columns that are questions about the first
	// round's answers — which jobs a failed pipeline failed, and which of the
	// environments are running what this one landed as.
	rows := make([]mrRow, len(hits))
	for i, m := range full {
		wg.Go(func() {
			project := strconv.Itoa(hits[i].ProjectID)
			rows[i] = m.row(pipelineNote("", m), deployedTo("", project, m, envs[slices.Index(projects, hits[i].ProjectID)], askEnvs))
		})
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
// ponytail: one page of deployments, so an environment nothing has deployed
// to in the last hundred goes unseen. Ask that environment directly when a
// project deploys often enough for it to matter.
//
// A lookup that fails is an empty column rather than an error: nobody asked
// about environments, they asked about a merge request. ask is false when
// --environments is off, and then nothing is asked at all.
func environments(host, project string, ask bool) []env {
	if !ask {
		return nil
	}
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
	envs := make([]env, len(deployed))
	for i, d := range deployed {
		envs[i] = env{d.Environment.Name, d.SHA}
	}
	return currentEnvs(envs)
}

// currentEnvs is what each environment is running, out of successful
// deployments newest first: the first to name an environment is what's on it.
// Names come out as the config's `[environments]` calls them, so two that it
// gives the same name collapse into the one column entry, newest first.
func currentEnvs(deployed []env) []env {
	cfg, _ := loadConfig() // a config we can't read renames nothing
	var envs []env
	for _, d := range deployed {
		name := cmp.Or(cfg.Environments[d.name], d.name)
		if !slices.ContainsFunc(envs, func(e env) bool { return e.name == name }) {
			envs = append(envs, env{name, d.sha})
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
//
// With --environments off the column is "??" rather than empty: an empty cell
// says the merge request is running nowhere, and not having asked is a
// different thing to say.
func deployedTo(host, project string, m mr, envs []env, ask bool) string {
	if !ask {
		return "??"
	}
	sha := cmp.Or(m.SquashSHA, m.MergeSHA)
	if m.State != "merged" || sha == "" {
		return ""
	}
	return runningOn(envs, func(head string) bool { return carries(host, project, head, sha) })
}

// runningOn is the ENV column out of an answer to one question per
// environment — does the commit on it have, in its history, the one the
// review landed as — which has asks the forge. They go out together, since
// each is a round trip of its own.
func runningOn(envs []env, has func(head string) bool) string {
	on := make([]string, len(envs))
	var wg sync.WaitGroup
	for i, e := range envs {
		wg.Go(func() {
			if has(e.sha) {
				on[i] = e.name
			}
		})
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
//
// A merge request queued on a merge train is the one case where gitlab's
// answer is worse than useless: it says "ci still running", which reads as
// something to go and wait on, when the pipeline running is the train's own
// and the merge request is already on its way in.
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
	// On the train, or waiting on a pipeline to join it — either way the
	// train is what it is waiting on and there is nothing to do about it.
	if strings.Contains(m.AutoMerge, "merge_train") {
		return "merge train"
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
	table, cols := mrCells(rows)
	if !stdoutIsTTY() {
		return mrMarkdown(table, rows)
	}
	fitTable(table, width, cols)
	lines := tabbed(table)
	paintMerged(lines, table, rows)
	linkRefs(lines, table, rows)
	return lines
}

// paintMerged puts the one colour these tables use on the one word worth
// spending it on: a merge request that is in, and so has nothing left to ask
// of you. Everything else — the pipeline still running, the approval that
// isn't there — stays the terminal's own foreground, which is what keeps the
// green meaning something when it does turn up.
//
// It goes on where linkRefs's escape does and for the same reason: an escape
// is no width on screen but plenty of characters to a tabwriter, so it waits
// until the columns are padded. The cell is the first "merged" past the ref —
// STATUS is the column after MR, and the rows that aren't merged aren't
// touched at all, so a title with the word in it stays plain. It runs before
// linkRefs, whose own escape would move the offsets out from under it.
//
// The green is the done dot's green, asked for by number: herdr paints its
// theme over the ansi sixteen, so the word and the dot stay the one colour.
func paintMerged(lines []string, table [][]string, rows []mrRow) {
	for i, r := range rows {
		if r.status != "merged" {
			continue
		}
		ref := table[i+1][0]
		j := strings.Index(lines[i+1][len(ref):], r.status)
		if j < 0 {
			continue // the column was cut away
		}
		j += len(ref)
		lines[i+1] = lines[i+1][:j] + "\x1b[92m" + r.status + "\x1b[0m" + lines[i+1][j+len(r.status):]
	}
}

// linkRefs makes the MR column of each row the link to the merge request, so
// the table you read the answer off is also the way to go and look at it. The
// sequence goes on last, once the columns are padded: it takes no room on
// screen, but both fitTable and the tabwriter measure cells in characters and
// would lay the table out around it.
//
// The ws view's section is these same cells indented into its gutter (see
// mrSection), so the leading spaces are left outside the link: what underlines
// is the ref, there as here.
func linkRefs(lines []string, table [][]string, rows []mrRow) {
	for i, r := range rows {
		cell := table[i+1][0]
		ref := strings.TrimLeft(cell, " ")
		if rest, ok := strings.CutPrefix(lines[i+1], cell); ok {
			lines[i+1] = cell[:len(cell)-len(ref)] + hyperlink(r.url, ref) + rest
		}
	}
}

// mrCells is the merge requests as a table: the header and a row each, and
// alongside it how wide each column may get and how it is to be shortened when
// it can't have that. The ws view lays the same cells out in its first section
// (see wsMR), so the two tables say a merge request the same way.
func mrCells(rows []mrRow) ([][]string, []column) {
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
	return table, cols
}

// mrMarkdown is the same table written as markdown, which is what comes out
// when nothing is reading it on a terminal. Off a terminal the table is going
// somewhere — a merge request comment, a ticket, a chat message, a status
// note — and all of those render markdown, where padded columns arrive as a
// wall of spaces and the OSC 8 link arrives as nothing at all. So the MR cell
// becomes the markdown link it was, and the widths stop mattering: nothing is
// cut to fit a terminal that isn't there.
//
// ponytail: no markdown package. A table is pipes and a rule under the header,
// and the escaping below is the whole of what one would do for us.
func mrMarkdown(table [][]string, rows []mrRow) []string {
	lines := make([]string, 0, len(table)+1)
	for i, row := range table {
		cells := make([]string, len(row))
		for j, cell := range row {
			cells[j] = mdCell(cell)
		}
		// The header is the one row that isn't a merge request, so the rows
		// run one behind it — and one with no url is left as its own text.
		if i > 0 && rows[i-1].url != "" {
			cells[0] = "[" + cells[0] + "](" + rows[i-1].url + ")"
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
		if i == 0 {
			lines = append(lines, strings.Repeat("| --- ", len(row))+"|")
		}
	}
	return lines
}

// mdCell is a cell's text as a markdown table can hold it: a pipe in it would
// end the cell early, and a line break would end the row.
func mdCell(s string) string { return strings.ReplaceAll(oneLine(s), "|", `\|`) }

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
// alone. Only the terminal table uses it: off one the link is a markdown one,
// since an escape sequence in a pipe is something for `cut` or `grep` to trip
// over.
func hyperlink(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// gitlab is the client every api call shares, so that they share its
// connections: a merge request is a handful of round trips and a ticket is a
// handful per merge request, and a tls handshake for each of them — which is
// what a `glab api` per call, a process each, was paying — was most of the
// wait. The timeout is the one a process didn't need: a request that never
// answers would otherwise leave the spinner turning for good.
var gitlab = &http.Client{Timeout: 30 * time.Second}

// glabAPI asks one gitlab api path and decodes the answer into v. The host is
// named only when the url said which gitlab it is: left out, it's the one
// gitlabHost guesses, which is the right guess for a ticket, since a ticket
// doesn't say.
//
// The token is glab's own, borrowed from where glab keeps it (see glabToken),
// so signing in is still `glab auth login`. What can't be asked that way goes
// out as `glab api` instead, the way all of it used to: no token of glab's to
// borrow, or a host that doesn't answer at the api url below — a gitlab
// configured with its own api_host, a proxy only glab was told about — and
// glab's own config is what knows better.
func glabAPI(host, path string, v any) error {
	host = cmp.Or(host, gitlabHost())
	token := glabToken(host)
	if token == "" {
		return glabRun(host, path, v)
	}
	req, err := http.NewRequest("GET", "https://"+host+"/api/v4/"+path, nil)
	if err != nil {
		return glabRun(host, path, v)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := gitlab.Do(req)
	if err != nil {
		return glabRun(host, path, v)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("gitlab api %s: %w", path, err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("gitlab api %s: %s", path, apiError(body, resp.Status))
	}
	return json.Unmarshal(body, v)
}

// gitlabHost is which gitlab a call that didn't name one goes to, guessed in
// the order glab guesses it: what GITLAB_HOST says, the host of the repo we
// are standing in, and then the host glab is configured to default to. Read
// as a host however it was written, since the config holds a whole url where
// the remote holds a host.
var gitlabHost = sync.OnceValue(func() string {
	if host := cmp.Or(os.Getenv("GITLAB_HOST"), remoteHost(".")); host != "" {
		return urlHost(host)
	}
	// glab's default host, the one setting in its config that isn't a host's
	// own — so it's the line that starts the file's left margin with "host:".
	for line := range strings.SplitSeq(glabConfig(), "\n") {
		if host, ok := strings.CutPrefix(line, "host:"); ok {
			return urlHost(strings.TrimSpace(host))
		}
	}
	return ""
})

// glabConfig is glab's config file, which is where the token for each host it
// knows is kept. Read once, and "" when there is none to read — which is a
// config with nothing in it, and every lookup falls through to the command.
//
// glab looks for it the same three places: where GLAB_CONFIG_DIR says, under
// XDG_CONFIG_HOME, or in ~/.config.
var glabConfig = sync.OnceValue(func() string {
	home, _ := os.UserHomeDir()
	dir := cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	dir = cmp.Or(os.Getenv("GLAB_CONFIG_DIR"), filepath.Join(dir, "glab-cli"))
	out, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	return string(out)
})

// glabToken is the token glab sends to host, looked for where glab looks: the
// environment first, then its config — the "hosts:" block, the host under it,
// and the "token:" under the host.
//
// Per host, since a token is: one meant for another gitlab is a token handed
// to a host that has no business seeing it. The environment's doesn't say
// which host it is for, so it is taken to be for the host the environment
// otherwise names, or failing that the one we are standing in — that being
// the host an unqualified `glab` command would have spent it on anyway.
//
// Read rather than asked for. `glab config get` answers the same question and
// would save us knowing the shape of someone else's file, but every glab
// command checks for a new glab on its way out, and that check is most of a
// second — several times what the lookups it would be serving cost.
//
// ponytail: this file and these three levels of it, rather than yaml and the
// rest of where glab will look. A token kept somewhere else — a per-repo
// config, a keyring — reads as no token here, and the call goes out as `glab
// api`, which knows all of those places. That is the slow way round, but it
// is the right answer.
func glabToken(host string) string {
	if token := os.Getenv("GITLAB_TOKEN"); token != "" && host == gitlabHost() {
		return token
	}
	return configToken(glabConfig(), host)
}

// configToken is host's token in the text of that config: under "hosts:", the
// host, and "token:" under it. Whether a line is a host or one of a host's
// settings is how far it is indented, which is all the yaml this needs to
// know — and a file it can't find its way around is no token at all, which
// the caller has an answer for.
func configToken(config, host string) string {
	var entry int // how far the host names under "hosts:" are indented
	var inHosts, inHost bool
	for line := range strings.SplitSeq(config, "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, _ := strings.Cut(text, ":")
		switch indent := len(line) - len(strings.TrimLeft(line, " \t")); {
		case indent == 0:
			inHosts, inHost, entry = key == "hosts", false, 0
		case !inHosts: // a setting under some other top-level key
		case entry == 0 || indent == entry:
			entry, inHost = indent, key == host
		case inHost && key == "token":
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

// glabRun asks the same path through the `glab api` command, which is where a
// call goes that can't be made from here. It is the process and the handshake
// that the client above exists to stop paying, so it is the fallback rather
// than the road.
func glabRun(host, path string, v any) error {
	args := []string{"api"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	out, err := exec.Command("glab", append(args, path)...).Output()
	if err != nil {
		return fmt.Errorf("glab api %s: %s", path, cliError(out, err))
	}
	return json.Unmarshal(out, v)
}

// apiError is what gitlab said was wrong with the request: "message" is where
// it says it, "error" is where the parts of it that speak oauth say it, and
// the status line is what's left when the body says neither.
func apiError(body []byte, status string) string {
	var said struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &said) // a body we can't read has nothing to quote
	return cmp.Or(said.Message, said.Error, status)
}

// cliError is what went wrong, as glab said it. It says it twice: once on
// stdout as json, and once on stderr as a wrapped block under an "ERROR"
// banner. The json is the one to quote — it is a line rather than a paragraph,
// and it is the message glab meant — and the block is what's left when there
// is no json, minus the decoration, which says nothing an error message in an
// error message needs to say.
//
// gh says it once, on stderr, as a line: the block with nothing to take off.
func cliError(out []byte, err error) string {
	var said struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &said) == nil && said.Error.Message != "" {
		first, _, _ := strings.Cut(said.Error.Message, "\n") // the rest is advice
		return first
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		for line := range strings.SplitSeq(string(ee.Stderr), "\n") {
			if l := strings.TrimSpace(line); l != "" && l != "ERROR" {
				return l
			}
		}
	}
	return err.Error()
}

func words(s string) string { return strings.ReplaceAll(s, "_", " ") }
