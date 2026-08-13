package main

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Task is one queued prompt: work to start once whatever it hangs off is
// finished. It waits either on a worktree (Worktree, its path) or on another
// queued prompt (Parent, its id) — so "do this, then that, then the other" is a
// tree of them, rooted at the worktree the first one is waiting for.
//
// Project is the repo the chain belongs to, kept rather than derived from the
// worktree path because the path outlives the worktree: a chain whose worktree
// has been removed still has to be drawn under the right project.
type Task struct {
	ID       int64
	Project  string
	Worktree string // path of the worktree it waits on, "" when it waits on a task
	Parent   int64  // id of the task it waits on, 0 when it waits on a worktree
	Prompt   string
	Created  time.Time
}

// The task database is shared by every ccwt in the session — the tui of each
// project and the -g one across all of them — so it lives with the rest of the
// state that outlives a process rather than under any one repo.
//
// One connection per process and no coordination beyond these pragmas: WAL so a
// tui re-reading the list doesn't block the one being typed into, busy_timeout
// so a writer that finds the database locked waits for it rather than failing,
// and foreign_keys so a prompt can't end up waiting on one that isn't there.
// ponytail: this is the whole of the locking story, which is what a single user
// with a few tabs open needs; nothing here would survive being a multi-writer
// service.
const taskDSN = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

// deleted is when the prompt was dropped, 0 while it is still queued. Dropping
// is a mark rather than a removal: `r` is one keystroke and the prompt it takes
// away can be a paragraph, so the row stays in the table to be recovered with
// an UPDATE. ponytail: nothing ever prunes them — a text row per dropped prompt
// is not a size problem; add a sweep if a database this small ever becomes one.
const taskSchema = `CREATE TABLE IF NOT EXISTS task (
	id       INTEGER PRIMARY KEY,
	project  TEXT    NOT NULL,
	worktree TEXT    NOT NULL,
	parent   INTEGER REFERENCES task(id) ON DELETE CASCADE,
	prompt   TEXT    NOT NULL,
	created  INTEGER NOT NULL,
	deleted  INTEGER NOT NULL DEFAULT 0
) STRICT`

// tasksPath is $XDG_STATE_HOME/ccwt/tasks.db, falling back to the ~/.local/state
// default XDG defines when the variable is unset. State rather than config: it's
// written by the program, not by hand, and it is not worth backing up.
func tasksPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "ccwt", "tasks.db")
}

// openTasks opens the task database, creating it and its schema the first time
// anything is queued.
//
// The schema statement doubles as what forces the connection open, and so as
// where the pragmas above are applied — which is why the retry is around it.
// Two ccwts opening a database that does not exist yet is the one moment
// busy_timeout doesn't cover: switching a fresh file to WAL is an upgrade from
// a read lock, and SQLite answers those with SQLITE_BUSY on the spot rather
// than waiting, since waiting on an upgrade can deadlock. Once the file is
// there, every writer after it waits its turn properly.
//
// ponytail: a bounded retry rather than a lock file of our own — it is a race
// that can only happen once per machine, and losing it costs 85ms.
func openTasks() (*sql.DB, error) {
	path := tasksPath()
	if path == "" {
		return nil, errors.New("cannot locate a state directory: set XDG_STATE_HOME")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+taskDSN)
	if err != nil {
		return nil, err
	}
	for wait := time.Millisecond; ; wait *= 4 {
		if _, err = db.Exec(taskSchema); err == nil {
			// ponytail: the whole migration story — a database from before the
			// column gets it here, one made by the schema above rejects the
			// statement as a duplicate column, and either way we're done.
			_, _ = db.Exec(`ALTER TABLE task ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0`)
			return db, nil
		}
		if wait > 64*time.Millisecond {
			db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		time.Sleep(wait) // a failed open leaves nothing in the pool: this reconnects
	}
}

// loadTasks reads every queued prompt, oldest first, which is the order a
// chain was typed in and so the order it's drawn in.
//
// No database yet is no tasks: `list` and `tui` read this on every refresh, and
// a state file materialising because someone looked at a table is rude.
func loadTasks() ([]Task, error) {
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
	rows, err := db.Query(`SELECT id, project, worktree, COALESCE(parent, 0), prompt, created FROM task WHERE deleted = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var t Task
		var created int64
		if err := rows.Scan(&t.ID, &t.Project, &t.Worktree, &t.Parent, &t.Prompt, &created); err != nil {
			return nil, err
		}
		t.Created = time.Unix(created, 0)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// newName is what a queued prompt whose worktree is gone calls itself in the
// NAME column: not a worktree yet, but the one it is waiting for is over, so
// this is next — and opening the row is what makes it.
const newName = "<new>"

// taskTree is the queued prompts indexed by what each of them is waiting on,
// which is what it takes to walk a chain down from its worktree without
// rescanning the list at every level.
type taskTree struct {
	roots map[string][]Task // worktree path -> the chains rooted at it
	kids  map[int64][]Task  // task id -> what is queued behind it
}

func indexTasks(tasks []Task) taskTree {
	tt := taskTree{roots: map[string][]Task{}, kids: map[int64][]Task{}}
	for _, t := range tasks {
		if t.Parent != 0 {
			tt.kids[t.Parent] = append(tt.kids[t.Parent], t)
		} else {
			tt.roots[t.Worktree] = append(tt.roots[t.Worktree], t)
		}
	}
	return tt
}

// pending is the prompts of project that were queued on a worktree that no
// longer exists: what the removal of a worktree promotes, from work waiting
// under it to work waiting for a worktree of its own. Oldest first, the order
// they were queued in — a removal mustn't shuffle the rows around under the
// cursor.
func (tt taskTree) pending(project string, live map[string]bool) []Task {
	var ts []Task
	for _, root := range tt.roots {
		for _, t := range root {
			if t.Project == project && !live[t.Worktree] {
				ts = append(ts, t)
			}
		}
	}
	slices.SortFunc(ts, func(a, b Task) int { return cmp.Compare(a.ID, b.ID) })
	return ts
}

// taskCells is a queued prompt as a row of the table: the tree connector in
// NAME, the only column with room for structure, and the prompt itself in TOPIC
// — where a worktree shows what its session or its last commit was about, which
// is the same question. AGE says how long it has been waiting.
func taskCells(t Task, gutter string, depth int) []string {
	name := gutter + gutter + strings.Repeat("  ", depth) + taskGlyph
	return []string{name, "", humanAge(time.Since(t.Created)), "", t.Prompt}
}

// addTask queues prompt behind the given row: behind another queued prompt when
// that's what's selected — a "<new>" row included, so a chain can go on growing
// after the worktree it started under is gone — and behind a worktree
// otherwise.
func addTask(parent listRow, prompt string) error {
	t := Task{Project: parent.project, Prompt: prompt}
	if parent.task > 0 {
		t.Parent = parent.task
	} else {
		t.Worktree = parent.path
	}
	db, err := openTasks()
	if err != nil {
		return err
	}
	defer db.Close()
	var pid any // NULL rather than 0, so the foreign key has nothing to check
	if t.Parent != 0 {
		pid = t.Parent
	}
	_, err = db.Exec(`INSERT INTO task (project, worktree, parent, prompt, created) VALUES (?, ?, ?, ?, ?)`,
		t.Project, t.Worktree, pid, t.Prompt, time.Now().Unix())
	return err
}

// updateTask rewrites a queued prompt in place. Its place in the chain doesn't
// move: what waits on it still waits on it, whatever it now says.
func updateTask(id int64, prompt string) error {
	db, err := openTasks()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE task SET prompt = ? WHERE id = ?`, prompt, id)
	return err
}

// dropTask marks a queued prompt as deleted, and everything queued behind it: a
// chain waits on the prompt above it, so nothing under a dropped one can still
// happen.
//
// Walking the chain by hand is what ON DELETE CASCADE used to do for free — a
// mark isn't a delete, so nothing follows it on its own. Parents are always
// older than their children, so the walk can't loop.
func dropTask(id int64) (string, bool) {
	db, err := openTasks()
	if err != nil {
		return "delete failed: " + err.Error(), false
	}
	defer db.Close()
	if _, err := db.Exec(`WITH RECURSIVE doomed(id) AS (
		SELECT ?
		UNION ALL SELECT task.id FROM task JOIN doomed ON task.parent = doomed.id
	) UPDATE task SET deleted = ? WHERE id IN (SELECT id FROM doomed)`,
		id, time.Now().Unix()); err != nil {
		return "delete failed: " + err.Error(), false
	}
	return "deleted", true
}

// promoteTask turns a queued prompt into the worktree it is about to run in:
// what was queued behind it now waits on that worktree instead, and the prompt
// itself is gone — it isn't waiting any more. A real delete, unlike dropTask's
// mark: this prompt hasn't been thrown away, it has become the worktree, and
// there is nothing here to recover.
//
// One transaction, because the two halves are only safe together: a delete that
// lands without the re-parent takes the rest of the chain with it through the
// schema's cascade.
func promoteTask(id int64, path string) error {
	db, err := openTasks()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE task SET parent = NULL, worktree = ? WHERE parent = ?`, path, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM task WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
