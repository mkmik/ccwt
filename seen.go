package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mkmik/ccwt/internal/gitutil"
)

// The seen table is every repo and worktree ccwt has been used in, and when:
// what an "open recent project" picker needs and nothing else keeps —
// config.toml is only the projects somebody listed, and the worklog only the
// worktrees removed through ccwt.
//
// One row per path. A project's row has path = project; a worktree's names the
// repo it hangs off, so the projects are `WHERE path = project` and a project's
// worktrees are the rest of its rows. uses sits next to first and last for a
// picker that ranks by frecency rather than by recency alone. hidden, on a
// project's row, keeps it out of `-g` without forgetting it was ever used.
//
// ponytail: nothing prunes it, and a row can outlive its directory — a picker
// stats what it offers. A row per worktree ever made is not a size problem.
const seenSchema = `CREATE TABLE IF NOT EXISTS seen (
	path    TEXT    PRIMARY KEY,
	project TEXT    NOT NULL,
	first   INTEGER NOT NULL,
	last    INTEGER NOT NULL,
	uses    INTEGER NOT NULL DEFAULT 1,
	hidden  INTEGER NOT NULL DEFAULT 0
) STRICT`

// markSeen records a use of project and, unless it is "", of its worktree
// worktree — which is a use of the project too.
func markSeen(project, worktree string, at time.Time) error {
	db, err := openTasks()
	if err != nil {
		return err
	}
	defer db.Close()
	for _, path := range []string{project, worktree} {
		if path == "" {
			continue
		}
		if _, err := db.Exec(`INSERT INTO seen (path, project, first, last) VALUES (?, ?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET last = excluded.last, uses = uses + 1`,
			path, project, at.Unix(), at.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// markCwdSeen records the repo, and the Claude Code worktree of it, that ccwt
// is being run in. Outside a repo it does nothing, and it says nothing either
// way: this is bookkeeping on the side of whatever command was asked for.
func markCwdSeen() {
	// Not gitutil.RepoRoot: that passes git's "not a git repository" through,
	// which every `ccwt init` outside a repo would then print.
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return
	}
	top := strings.TrimSpace(string(out))
	project, ok := gitutil.ClaudeWorktreeRepoRoot(top)
	if !ok {
		project, top = top, ""
	}
	_ = markSeen(project, top, time.Now())
}

// seenProjects is the repos ccwt has been used in and nobody hid, oldest first:
// only the ones still a main checkout there, since a row outlives its directory
// and a section git can't read is no use to anyone.
func seenProjects() ([]string, error) {
	db, err := openTasks()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT path FROM seen WHERE path = project AND NOT hidden ORDER BY first, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if fi, err := os.Stat(filepath.Join(p, ".git")); err == nil && fi.IsDir() {
			paths = append(paths, p)
		}
	}
	return paths, rows.Err()
}

// hideProject keeps project out of the seen projects from now on, however much
// ccwt goes on being used there. Its row stays, so the day something wants the
// history — or to unhide it — it's all still there.
func hideProject(project string) error {
	db, err := openTasks()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE seen SET hidden = 1 WHERE path = ?`, project)
	return err
}
