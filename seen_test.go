package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestHideProject: a hidden project leaves -g's seen projects but keeps its
// row, and the menu offers hiding only on a section header under -g.
func TestHideProject(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := t.TempDir()
	gitInit(t, p)
	if err := markSeen(p, "", time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if got, err := seenProjects(); err != nil || !slices.Equal(got, []string{p}) {
		t.Fatalf("seenProjects = (%v, %v), want [%s]", got, err, p)
	}
	if err := hideProject(p); err != nil {
		t.Fatal(err)
	}
	if err := markSeen(p, "", time.Unix(2000, 0)); err != nil { // a later use doesn't unhide it
		t.Fatal(err)
	}
	if got, err := seenProjects(); err != nil || len(got) != 0 {
		t.Errorf("seenProjects after hiding = (%v, %v), want none", got, err)
	}
	db, err := openTasks()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var uses int
	if err := db.QueryRow(`SELECT uses FROM seen WHERE path = ?`, p).Scan(&uses); err != nil || uses != 2 {
		t.Errorf("the hidden row: uses = %d, %v; want it kept, at 2", uses, err)
	}

	hide := action{hideKey, "hide"}
	for _, c := range []struct {
		sel          listRow
		global, want bool
	}{
		{listRow{project: p}, true, true},
		{listRow{project: p}, false, false},
		{listRow{project: p, path: p + "/w"}, true, false},
	} {
		if got := slices.Contains(menuActions(c.sel, false, c.global, false), hide); got != c.want {
			t.Errorf("menu for %+v, global %v, offers hide = %v, want %v", c.sel, c.global, got, c.want)
		}
	}
}

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

// gitInit makes dir look enough like a main checkout for seenPaths to keep it.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
