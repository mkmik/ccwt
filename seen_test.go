package main

import (
	"testing"
	"time"
)

// TestMarkSeen: a second use moves last and counts, and leaves first alone;
// a worktree's use is its project's too.
func TestMarkSeen(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t0, t1 := time.Unix(1000, 0), time.Unix(2000, 0)
	if err := markSeen("/p", "", t0); err != nil {
		t.Fatal(err)
	}
	if err := markSeen("/p", "/p/.claude/worktrees/w", t1); err != nil {
		t.Fatal(err)
	}
	db, err := openTasks()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for path, want := range map[string][4]any{
		"/p":                     {"/p", int64(1000), int64(2000), int64(2)},
		"/p/.claude/worktrees/w": {"/p", int64(2000), int64(2000), int64(1)},
	} {
		var project string
		var first, last, uses int64
		if err := db.QueryRow(`SELECT project, first, last, uses FROM seen WHERE path = ?`, path).Scan(&project, &first, &last, &uses); err != nil {
			t.Fatal(path, err)
		}
		if got := [4]any{project, first, last, uses}; got != want {
			t.Errorf("%s: got %v, want %v", path, got, want)
		}
	}
}
