package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// `ccwt mr` for a review on github, which is a pull request: the same table
// and the same columns, asked of github instead. Which of the two a review is
// comes from its url — or, for the branch you're on and for a ticket, from
// where origin points (see onGitHub), which is how the tui's `m` decides it.
//
// It asks graphql rather than rest: where a pull request stands, whether it
// is approved and what its checks did are one round trip there and four
// here. And it asks through `gh api`, which already knows each host's api url
// and its token.
//
// ponytail: a gh process per question, ~0.4s each where glabAPI's shared
// connection is a tenth of that. A lookup is two or three rounds of them, each
// round going out together; borrow gh's token and share the client, as the
// gitlab half does, when that wait starts to show.

// prURL is a pull request url: the host, the repo, and the number after
// /pull/. What matched is the pull request's own url, whatever page of it
// (/files, /checks) the link was to.
var prURL = regexp.MustCompile(`^https?://([^/]+)/([^/]+/[^/]+)/pull/\d+`)

// onGitHub reports whether host's reviews are pull requests, decided as the
// tui's `m` decides it (see forgeCLI). Anything else is taken for a gitlab,
// which is what `ccwt mr` took everything for before there was a choice.
func onGitHub(host string) bool {
	cfg, _ := loadConfig() // a config we can't read names no hosts
	return forgeCLI(host, cfg.Forges) == "gh"
}

// prFields is what a row needs to know about a pull request, asked the same
// way wherever the pull request was found. A check run and a commit status
// are the two things github counts as a check, and a status's name is its
// context.
const prFields = `
fragment pr on PullRequest {
	number title url state isDraft isInMergeQueue mergeStateStatus reviewDecision
	mergeCommit { oid }
	repository { nameWithOwner }
	commits(last: 1) { nodes { commit { statusCheckRollup {
		state
		contexts(first: 100) { nodes {
			... on CheckRun { name conclusion }
			... on StatusContext { name: context state }
		} }
	} } } }
}`

// pr is prFields as they come back, cut down to what the row needs.
type pr struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	State        string `json:"state"` // OPEN, CLOSED or MERGED
	Draft        bool   `json:"isDraft"`
	InMergeQueue bool   `json:"isInMergeQueue"`
	MergeState   string `json:"mergeStateStatus"`
	Review       string `json:"reviewDecision"`
	MergeCommit  struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	Repository struct {
		Name string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Checks *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []struct {
							Name       string `json:"name"`
							Conclusion string `json:"conclusion"` // a check run's
							State      string `json:"state"`      // a commit status's
						} `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// row is the pull request as the table shows it, bar ENV, which is a
// question of its own (see prDeployedTo). The ref is github's own spelling of
// one, which has no middle to take out.
func (p pr) row(env string) mrRow {
	return mrRow{
		ref:      fmt.Sprintf("%s#%d", p.Repository.Name, p.Number),
		url:      p.URL,
		title:    p.Title,
		status:   prStatus(p),
		pipeline: prChecks(p),
		env:      env,
	}
}

// prStatus is mrStatus for a pull request, in the same words. mergeStateStatus
// is github's answer to "can it go in", but its "blocked" is every rule the
// branch is protected by at once — so the missing review, the one that is
// somebody else's to give, is picked out of it by name. The rest is spelled
// out the way github spells it ("behind").
func prStatus(p pr) string {
	switch {
	case p.State != "OPEN":
		return strings.ToLower(p.State) // merged, closed
	case p.InMergeQueue:
		return "merge queue" // github's merge train, and as little to do about it
	case p.Draft:
		return "draft"
	}
	switch p.MergeState {
	case "CLEAN", "HAS_HOOKS", "UNSTABLE": // unstable: failing checks nobody requires, which PIPELINE says
		return "can be merged"
	case "DIRTY":
		return "conflict"
	case "BLOCKED":
		switch p.Review {
		case "REVIEW_REQUIRED":
			return "needs approval"
		case "CHANGES_REQUESTED":
			return "changes requested"
		}
	}
	return cmp.Or(words(strings.ToLower(p.MergeState)), "unknown")
}

// prChecks is pipelineNote for a pull request: what the checks on its last
// commit came to, and for a failure which of them failed. One that is in or
// dropped has history rather than checks, as a merge request has.
func prChecks(p pr) string {
	if p.State != "OPEN" || len(p.Commits.Nodes) == 0 || p.Commits.Nodes[0].Commit.Checks == nil {
		return ""
	}
	checks := p.Commits.Nodes[0].Commit.Checks
	switch checks.State {
	case "SUCCESS":
		return "green"
	case "PENDING", "EXPECTED": // expected: a required check that hasn't reported yet
		return "running"
	}
	var failed []string
	for _, c := range checks.Contexts.Nodes {
		switch cmp.Or(c.Conclusion, c.State) {
		case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "STARTUP_FAILURE", "ACTION_REQUIRED":
			failed = append(failed, c.Name)
		}
	}
	return "failed" + jobsNote(failed)
}

// lookPR is lookThing for a pull request url: the pull request, and what its
// repo has deployed, asked together as a merge request's are.
func lookPR(host, repo, url string, askEnvs bool) ([]mrRow, error) {
	var envs []env
	var wg sync.WaitGroup
	wg.Go(func() {
		envs = prEnvironments(host, repo, askEnvs)
	})
	p, err := fetchPR(host, url)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	return []mrRow{p.row(prDeployedTo(host, p, envs, askEnvs))}, nil
}

// prTries is how many times fetchPR asks, a second apart, before it settles
// for "unknown": github has worked it out within three seconds, in practice.
const prTries = 4

// fetchPR is one pull request, by its url. github works out whether one can
// be merged only once somebody asks, and says UNKNOWN until it has — and the
// first ask after either branch moves is one of those, so on a busy base it
// is most of them. It has the answer a second or so later, so an open one
// that came back unknown is asked again rather than reported that way.
func fetchPR(host, url string) (pr, error) {
	for try := 1; ; try++ {
		var got struct {
			Resource pr `json:"resource"`
		}
		err := ghQL(host, &got, `query($url: URI!) { resource(url: $url) { ...pr } }`+prFields, "url="+url)
		p := got.Resource
		switch {
		case err != nil:
			return p, err
		case p.Number == 0: // a url github doesn't know resolves to nothing rather than to an error
			return p, fmt.Errorf("no pull request at %s", url)
		case p.State != "OPEN" || p.MergeState != "UNKNOWN" || try == prTries:
			return p, nil
		}
		time.Sleep(time.Second)
	}
}

// branchPR is branchMR on github: the pull request whose head is the branch
// checked out here, most recently touched first, as the url lookThing takes.
func branchPR(host, repo, branch string) (string, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var got struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					URL string `json:"url"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	}
	const q = `query($owner: String!, $name: String!, $branch: String!) {
		repository(owner: $owner, name: $name) {
			pullRequests(headRefName: $branch, first: 1, orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { url } }
		}
	}`
	if err := ghQL(host, &got, q, "owner="+owner, "name="+name, "branch="+branch); err != nil {
		return "", fmt.Errorf("no pull request for this branch: %w", err)
	}
	hits := got.Repository.PullRequests.Nodes
	if len(hits) == 0 {
		return "", errors.New("no pull request for this branch")
	}
	return hits[0].URL, nil
}

// ticketPRs is ticketMRs on github: every pull request that names the ticket,
// by repo and number. github's search answers each one in full, bar whether an
// open one can be merged when it hadn't worked that out yet (see fetchPR) —
// so those get a second look, alongside what each repo has deployed, which is
// asked once per repo.
//
// ponytail: searched among the repos of whoever owns the one you're in, since
// the whole of github.com is everybody's tickets. A ticket worked on under two
// owners shows the one owner's half; name the owners in the config when that
// happens.
func ticketPRs(host, repo, key string, askEnvs bool) ([]mrRow, error) {
	owner, _, _ := strings.Cut(repo, "/")
	var got struct {
		Search struct {
			Nodes []pr `json:"nodes"`
		} `json:"search"`
	}
	const q = `query($q: String!) { search(query: $q, type: ISSUE, first: 100) { nodes { ...pr } } }`
	if err := ghQL(host, &got, q+prFields, fmt.Sprintf("q=%q type:pr user:%s", key, owner)); err != nil {
		return nil, err
	}
	prs := got.Search.Nodes
	slices.SortFunc(prs, func(a, b pr) int {
		return cmp.Or(strings.Compare(a.Repository.Name, b.Repository.Name), cmp.Compare(a.Number, b.Number))
	})
	repos := make([]string, len(prs))
	for i, p := range prs {
		repos[i] = p.Repository.Name
	}
	repos = slices.Compact(repos)

	envs := make([][]env, len(repos))
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Go(func() {
			envs[i] = prEnvironments(host, r, askEnvs)
		})
	}
	for i, p := range prs {
		if p.State != "OPEN" || p.MergeState != "UNKNOWN" {
			continue
		}
		wg.Go(func() {
			if got, err := fetchPR(host, p.URL); err == nil {
				prs[i] = got // failing that, the search's row stands, unknown and all
			}
		})
	}
	wg.Wait()
	rows := make([]mrRow, len(prs))
	for i, p := range prs {
		wg.Go(func() {
			rows[i] = p.row(prDeployedTo(host, p, envs[slices.Index(repos, p.Repository.Name)], askEnvs))
		})
	}
	wg.Wait()
	return rows, nil
}

// prEnvironments is environments on github: the repo's deployments newest
// first, and the first successful one to name an environment is what's on it
// now. github calls a deployment that succeeded active, until a newer one to
// the same environment succeeds and it goes inactive — so active is what
// gitlab's status=success is here.
//
// The hundred newest, and a lookup that fails is an empty column rather than
// an error, both as there.
func prEnvironments(host, repo string, ask bool) []env {
	if !ask {
		return nil
	}
	owner, name, _ := strings.Cut(repo, "/")
	var got struct {
		Repository struct {
			Deployments struct {
				Nodes []struct {
					Environment string `json:"environment"`
					State       string `json:"state"`
					SHA         string `json:"commitOid"`
				} `json:"nodes"`
			} `json:"deployments"`
		} `json:"repository"`
	}
	const q = `query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			deployments(first: 100, orderBy: {field: CREATED_AT, direction: DESC}) { nodes { environment state commitOid } }
		}
	}`
	if err := ghQL(host, &got, q, "owner="+owner, "name="+name); err != nil {
		return nil
	}
	var deployed []env
	for _, d := range got.Repository.Deployments.Nodes {
		if d.State == "ACTIVE" || d.State == "SUCCESS" {
			deployed = append(deployed, env{d.Environment, d.SHA})
		}
	}
	return currentEnvs(deployed)
}

// prDeployedTo is deployedTo for a pull request: the environments running the
// commit it landed as, which github calls its merge commit however it went in
// — squashed and rebased ones included.
//
// Whether an environment's commit has it in its history is github's compare,
// asked with the environment's commit as the base: "behind" is a commit it
// already has, and asked that way round a yes comes back with no commits and
// no diff in it.
func prDeployedTo(host string, p pr, envs []env, ask bool) string {
	if !ask {
		return "??"
	}
	sha := p.MergeCommit.OID
	if p.State != "MERGED" || sha == "" {
		return ""
	}
	return runningOn(envs, func(head string) bool {
		var compared struct {
			Status string `json:"status"`
		}
		err := ghAPI(host, &compared, "repos/"+p.Repository.Name+"/compare/"+head+"..."+sha)
		return err == nil && (compared.Status == "behind" || compared.Status == "identical")
	})
}

// ghAPI asks github one api path through `gh api`, and decodes what it prints
// into v. gh says what went wrong the way glab does when it has no json to say
// it in — a line on stderr — so cliError reads it for both.
func ghAPI(host string, v any, args ...string) error {
	out, err := exec.Command("gh", append([]string{"api", "--hostname", host}, args...)...).Output()
	if err != nil {
		return errors.New(cliError(out, err))
	}
	return json.Unmarshal(out, v)
}

// ghQL is a graphql query through ghAPI: its variables as name=value, every
// one of them a string, and the data in the answer decoded into v.
func ghQL(host string, v any, query string, vars ...string) error {
	args := []string{"graphql", "--jq", ".data", "-f", "query=" + query}
	for _, kv := range vars {
		args = append(args, "-f", kv)
	}
	return ghAPI(host, v, args...)
}
