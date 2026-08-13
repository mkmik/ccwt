package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/mkmik/ccwt/internal/gitutil"
)

// Removal is one line of the worklog: a worktree that was removed, and what it
// was about. The topic is copied in rather than looked up, because there is
// nothing left to look it up from — the transcript is keyed by the worktree's
// path and the last commit was in the worktree.
//
// No branch: `remove` deletes it, and a kept one is the name with the
// configured prefix on the front, which branchName already knows how to spell.
type Removal struct {
	Project string
	Name    string
	Topic   string
	At      time.Time
}

// It lives in the task database rather than a file of its own: same state
// directory, same lifetime, and the pragmas openTasks sets — WAL, busy_timeout
// — are exactly what a log written by whichever ccwt happens to be removing a
// worktree needs.
const worklogSchema = `CREATE TABLE IF NOT EXISTS worklog (
	id      INTEGER PRIMARY KEY,
	project TEXT    NOT NULL,
	name    TEXT    NOT NULL,
	topic   TEXT    NOT NULL,
	removed INTEGER NOT NULL
) STRICT`

// How many removals a log shows when nothing says otherwise; -n's default says
// it again, since a kong tag can't read a constant. ponytail: one number for
// the pane and the command line — "recently" is a screenful either way, and
// nothing prunes the table, so the limit is the whole of the size story.
const worklogLimit = 20

// logRemoval records a worktree on its way out.
func logRemoval(r Removal) error {
	db, err := openTasks()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO worklog (project, name, topic, removed) VALUES (?, ?, ?, ?)`,
		r.Project, r.Name, r.Topic, r.At.Unix())
	return err
}

// loadWorklog reads the last limit removals of the given projects, newest
// first — the order the question is asked in, since what you were working on
// yesterday is further down the same list.
//
// No database yet is no log, the same way it is no tasks: looking at a log
// nobody has written to shouldn't leave a state file behind.
func loadWorklog(projects []string, limit int) ([]Removal, error) {
	if path := tasksPath(); path == "" {
		return nil, nil
	} else if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	db, err := openTasks()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	args := make([]any, 0, len(projects)+1)
	for _, p := range projects {
		args = append(args, p)
	}
	args = append(args, limit)
	q := fmt.Sprintf(`SELECT project, name, topic, removed FROM worklog WHERE project IN (?%s) ORDER BY id DESC LIMIT ?`,
		strings.Repeat(", ?", max(len(projects)-1, 0)))
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var log []Removal
	for rows.Next() {
		var r Removal
		var at int64
		if err := rows.Scan(&r.Project, &r.Name, &r.Topic, &at); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		log = append(log, r)
	}
	return log, rows.Err()
}

// worklogProjects is the repos a log covers, as the log keys them: the ones in
// view, or — when that is nil, which is how "the repo we're standing in" is
// spelled everywhere else — that repo.
//
// Each goes through git rather than being used as written, because an entry was
// filed under git's own spelling of the root and the config's needn't match it:
// a path through a symlink resolves, and one naming a subdirectory of the repo
// comes back as the repo. A log that quietly matched nothing would look exactly
// like a log with nothing in it.
func worklogProjects(projects []string) ([]string, error) {
	if projects == nil {
		projects = []string{""} // "": the repo the process is standing in
	}
	roots := make([]string, len(projects))
	for i, dir := range projects {
		root, err := gitutil.RepoRoot(dir, true)
		if err != nil {
			return nil, gitError(dir, err)
		}
		roots[i] = root
	}
	return roots, nil
}

// worktreeTopic is the TOPIC of `ccwt list` for one worktree, read on the spot
// rather than through the caches the table uses: it is asked once, at removal,
// and a stale answer would be logged forever.
func worktreeTopic(path string) string {
	subject := "(no commits)"
	if c, err := gitutil.LastCommit(path); err == nil {
		subject = c.Subject
	}
	return topic(sessionSummary(path), subject)
}

// worklogTable lays the log out the way `ccwt list` lays out the worktrees, and
// answers the same questions about work that isn't there any more: when it went
// (AGE), what it was called, and what it was about. Fitted to width, or to the
// fixed caps when width is 0 and there is no terminal to fit to.
//
// PROJECT joins them only when the entries span more than one repo, which is
// what -g does: a worktree name on its own doesn't say which one it was in.
func worklogTable(log []Removal, width int) []string {
	if len(log) == 0 {
		return []string{"no worktrees removed yet"}
	}
	cols := []column{{name: "AGE"}, {name: "NAME", cut: truncate}, {name: "TOPIC", cut: topicCut, pipe: 60}}
	span := slices.ContainsFunc(log, func(r Removal) bool { return r.Project != log[0].Project })
	if span {
		cols = slices.Insert(cols, 1, column{name: "PROJECT", cut: truncate, max: 20})
	}

	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = c.name
	}
	table := [][]string{head}
	for _, r := range log {
		row := []string{humanAge(time.Since(r.At)), r.Name, r.Topic}
		if span {
			row = slices.Insert(row, 1, filepath.Base(r.Project))
		}
		table = append(table, row)
	}
	fitTable(table, width, cols)

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	for _, r := range table {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

type WorklogCmd struct {
	Global bool `short:"g" help:"Show the removals of every project listed in $XDG_CONFIG_HOME/ccwt/config.toml, not just this repo's."`
	Limit  int  `short:"n" default:"20" help:"How many removals to show, newest first."`
}

func (c *WorklogCmd) Run() error {
	projects, err := projectRoots(c.Global)
	if err != nil {
		return err
	}
	if projects, err = worklogProjects(projects); err != nil {
		return err
	}
	log, err := loadWorklog(projects, c.Limit)
	if err != nil {
		return err
	}
	// Fit the table to the terminal only when there is one, as `list` does.
	width := 0
	if stdoutIsTTY() {
		width, _, _ = term.GetSize(int(os.Stdout.Fd()))
	}
	for _, line := range worklogTable(log, width) {
		fmt.Println(line)
	}
	return nil
}
