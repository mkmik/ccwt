package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mkmik/ccwt/internal/gitutil"
)

// TestNewPath covers `ccwt new --path`: the printed path must be absolute and
// must describe the same worktree the nameful form prints — including the
// enclosing-worktree case, where no worktree is created.
func TestNewPath(t *testing.T) {
	initRepo(t)

	name := capture(t, &NewWorktreeBranchCmd{})
	path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
	if !filepath.IsAbs(path) {
		t.Errorf("path %q is not absolute", path)
	}
	if got := filepath.Base(path); got != name {
		t.Errorf("path %q ends in %q, want %q", path, got, name)
	}

	// From inside a worktree, `new` returns the enclosing one; --path must
	// return that same worktree's path rather than creating anything.
	t.Chdir(path)
	if got := capture(t, &NewWorktreeBranchCmd{Path: true}); got != path {
		t.Errorf("enclosing worktree path = %q, want %q", got, path)
	}
}

// TestListMarksCurrent covers both "*" markers — the current worktree's name
// and a dirty worktree's last commit. Each must appear on its row only, and
// only when stdout is a tty.
func TestListMarksCurrent(t *testing.T) {
	initRepo(t)
	here := capture(t, &NewWorktreeBranchCmd{})
	other := capture(t, &NewWorktreeBranchCmd{})
	t.Chdir(capture(t, &NewWorktreeBranchCmd{Name: here, Path: true}))
	if err := os.WriteFile("untracked", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return true }
	for _, line := range strings.Split(capture(t, &ListCmd{}), "\n") {
		switch {
		case line == "" || strings.HasPrefix(line, "  NAME"):
		case strings.HasPrefix(line, "* "+here+" "):
			if !strings.Contains(line, "* init") {
				t.Errorf("dirty worktree missing commit marker: %q", line)
			}
		case strings.HasPrefix(line, "  "+other+" "):
			if strings.Contains(line, "*") {
				t.Errorf("clean worktree has a commit marker: %q", line)
			}
		default:
			t.Errorf("unexpected line %q", line)
		}
	}

	stdoutIsTTY = func() bool { return false }
	if out := capture(t, &ListCmd{}); strings.Contains(out, "*") {
		t.Errorf("non-tty output contains a marker:\n%s", out)
	}
}

// TestRemoveCurrent covers `ccwt remove .` from inside the worktree it names:
// the worktree goes away and the command prints (and asks the wrapper to cd to)
// the repo root, instead of refusing.
func TestRemoveCurrent(t *testing.T) {
	repo := initRepo(t)
	path := capture(t, &NewWorktreeBranchCmd{Name: "gone", Path: true})
	t.Chdir(path)

	cdFile := filepath.Join(t.TempDir(), "cd")
	t.Setenv("CCWT_WRAPPER_CD_FILE", cdFile)

	// EvalSymlinks: on macOS t.TempDir() hands out /var/..., git reports /private/var/...
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := capture(t, &RemoveCmd{Name: ".", Force: true}); got != want {
		t.Errorf("printed %q, want repo root %q", got, want)
	}
	if got, err := os.ReadFile(cdFile); err != nil || string(got) != want {
		t.Errorf("cd request = %q, %v; want %q", got, err, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists (%v)", path, err)
	}
}

// TestListMarksMerged: a branch already contained in main gets the "✓" glyph,
// one with commits of its own doesn't. Tty-only, like the other markers.
func TestListMarksMerged(t *testing.T) {
	initRepo(t)
	capture(t, &NewWorktreeBranchCmd{Name: "merged"})
	ahead := capture(t, &NewWorktreeBranchCmd{Name: "ahead", Path: true})
	git(t, "-C", ahead, "commit", "--allow-empty", "-m", "ahead")

	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	for _, tty := range []bool{true, false} {
		stdoutIsTTY = func() bool { return tty }
		for _, line := range strings.Split(capture(t, &ListCmd{}), "\n") {
			name, rest, _ := strings.Cut(strings.TrimPrefix(line, "  "), " ")
			want := tty && name == "merged"
			if got := strings.Contains(rest, "✓ worktree-"+name); got != want {
				t.Errorf("tty=%v: merged glyph = %v, want %v: %q", tty, got, want, line)
			}
		}
	}
}

// TestListRowsStayPaired: `list` looks each worktree up concurrently, so pin
// the invariant that breaks if a result ever lands on the wrong row — every
// name must still be printed next to its own branch and its own commit.
func TestListRowsStayPaired(t *testing.T) {
	initRepo(t)
	want := map[string]string{}
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
		git(t, "-C", path, "commit", "--allow-empty", "-m", "subject-"+name)
		want[name] = "subject-" + name
	}

	for _, line := range strings.Split(capture(t, &ListCmd{}), "\n") {
		name, rest, _ := strings.Cut(line, " ")
		subject, ok := want[name]
		if !ok {
			continue // header
		}
		if !strings.Contains(rest, subject) || !strings.Contains(rest, "worktree-"+name) {
			t.Errorf("row for %s carries another worktree's data: %q", name, line)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("worktrees missing from the listing: %v", want)
	}
}

// TestRemoveUnmerged: an unmerged branch blocks the removal outright, leaving
// the worktree intact rather than stranding the branch — and --keep-branch
// removes the worktree while keeping the branch.
func TestRemoveUnmerged(t *testing.T) {
	initRepo(t)
	path := capture(t, &NewWorktreeBranchCmd{Name: "ahead", Path: true})
	git(t, "-C", path, "commit", "--allow-empty", "-m", "ahead")

	if err := (&RemoveCmd{Name: "ahead"}).Run(); err == nil {
		t.Error("removing an unmerged worktree succeeded, want refusal")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("refused remove deleted the worktree anyway: %v", err)
	}

	if err := (&RemoveCmd{Name: "ahead", KeepBranch: true}).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists (%v)", path, err)
	}
	if !gitutil.BranchExists("worktree-ahead") {
		t.Error("--keep-branch deleted the branch")
	}
}

// TestRemoveSquashMerged: a branch contained in main, whose stale
// origin/<branch> still points at the pre-squash commits, must still be
// removable. `git branch -d` asks "merged into the upstream?" rather than
// "merged into main?" and refuses — after the worktree is already gone.
func TestRemoveSquashMerged(t *testing.T) {
	repo := initRepo(t)
	path := capture(t, &NewWorktreeBranchCmd{Name: "squashed", Path: true})
	git(t, "-C", path, "commit", "--allow-empty", "-m", "work")

	// The branch as pushed, then the PR squash-merged into main under a
	// different commit and the branch caught up to it. Nobody ran
	// `git fetch --prune`, so the remote-tracking ref is left behind.
	// The remote needs its refspec for git to resolve the upstream at all.
	git(t, "remote", "add", "origin", repo)
	git(t, "update-ref", "refs/remotes/origin/worktree-squashed", "refs/heads/worktree-squashed")
	git(t, "config", "branch.worktree-squashed.remote", "origin")
	git(t, "config", "branch.worktree-squashed.merge", "refs/heads/worktree-squashed")
	git(t, "commit", "--allow-empty", "-m", "squashed work (#1)")
	git(t, "-C", path, "reset", "--hard", "main")

	if err := (&RemoveCmd{Name: "squashed"}).Run(); err != nil {
		t.Fatalf("removing a squash-merged worktree: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists (%v)", path, err)
	}
	if gitutil.BranchExists("worktree-squashed") {
		t.Error("worktree removed but the branch was stranded")
	}
}

// initRepo makes a git repo with one commit on main and chdirs into it.
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)
	git(t, "init", "-b", "main")
	git(t, "config", "core.hooksPath", "/dev/null") // ignore the user's global hooks
	git(t, "commit", "--allow-empty", "-m", "init")
	return repo
}

func git(t *testing.T, args ...string) {
	t.Helper()
	args = append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The whole point of paint() is repainting without a screen clear, which is
// what would make `ccwt tui` flicker. It also must not end with a newline: on
// the bottom row, where the status bar lives, that scrolls the screen.
func TestPaintOverwritesWithoutClearing(t *testing.T) {
	var buf bytes.Buffer
	paint(&buf, []string{"one", "two"})
	got := buf.String()
	if want := "\x1b[H" + "one\x1b[K" + "\r\n" + "two\x1b[K" + "\x1b[J"; got != want {
		t.Errorf("paint() = %q, want %q", got, want)
	}
	if strings.Contains(got, "\x1b[2J") {
		t.Error("paint() clears the screen")
	}
}

// The ahead/behind pair is parsed out of rev-list's two-column output, so it
// has to survive both the no-upstream case and a real divergence.
func TestGitDivergence(t *testing.T) {
	initRepo(t)
	if got, want := gitDivergence(), "main  (no upstream)"; got != want {
		t.Errorf("gitDivergence() = %q, want %q", got, want)
	}

	// A local branch as its own upstream is enough to exercise the counting.
	git(t, "branch", "upstream")
	git(t, "config", "branch.main.remote", ".")
	git(t, "config", "branch.main.merge", "refs/heads/upstream")
	if got, want := gitDivergence(), "main  in sync"; got != want {
		t.Errorf("gitDivergence() = %q, want %q", got, want)
	}

	git(t, "commit", "--allow-empty", "-m", "ahead")
	if got, want := gitDivergence(), "main  ↑1 ↓0"; got != want {
		t.Errorf("gitDivergence() = %q, want %q", got, want)
	}
}

// The status bar has to be exactly one screen line wide, whether the message
// is short (pad) or long (cut) — otherwise it wraps and shoves the table up.
func TestStatusBarIsExactlyOneLineWide(t *testing.T) {
	for _, msg := range []string{"", "ok", strings.Repeat("x", 200)} {
		for _, sel := range []string{"", "some-worktree"} {
			bar := statusBar(40, msg, sel)
			bar = strings.TrimSuffix(strings.TrimPrefix(bar, "\x1b[7m"), "\x1b[0m")
			if got := len([]rune(bar)); got != 40 {
				t.Errorf("statusBar(40, %.10q, %q) is %d cols wide, want 40", msg, sel, got)
			}
		}
	}
}

// The selection is kept by name, so it has to survive the list changing under
// it: rows reordering (a commit lands elsewhere) must not move it, and the
// selected worktree disappearing must not wedge the arrows.
func TestSelectionFollowsTheWorktree(t *testing.T) {
	u := ui{names: []string{"alpha", "bravo", "charlie"}}
	u.move(1) // nothing selected yet: start at the top
	u.move(1)
	if u.sel != "bravo" {
		t.Fatalf("after two downs sel = %q, want bravo", u.sel)
	}

	// A commit reorders the list: the selection stays on bravo, and the next
	// arrow moves relative to where bravo sits now.
	u.names = []string{"bravo", "charlie", "alpha"}
	u.move(1)
	if u.sel != "charlie" {
		t.Errorf("after the rows reordered, sel = %q, want charlie", u.sel)
	}

	u.names = []string{"alpha"} // bravo removed out from under us
	u.sel = "bravo"
	u.move(1)
	if u.sel != "alpha" {
		t.Errorf("after the selected worktree vanished, sel = %q, want alpha", u.sel)
	}

	u.names = nil
	u.move(-1) // must not panic on an empty list
}

// A click is turned into a worktree by counting screen lines, so the frame's
// row i must really be names[i] — off by the one header line, and clicking a
// row would open (or `r` would remove) its neighbour.
func TestFrameRowsMatchNames(t *testing.T) {
	initRepo(t)
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
		git(t, "-C", path, "commit", "--allow-empty", "-m", "subject-"+name)
	}

	// The first frame is what fills in the row names — as in the tui, where a
	// frame is always painted before any key is read.
	var u ui
	if _, err := u.frame(); err != nil {
		t.Fatal(err)
	}
	u.move(1) // selects the first row
	lines, err := u.frame()
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range u.names {
		if !strings.Contains(lines[i+1], name) {
			t.Errorf("names[%d] = %q, but line %d is %q", i, name, i+1, lines[i+1])
		}
	}
	if !strings.HasPrefix(lines[1], "\x1b[7m") {
		t.Errorf("selected row %q is not highlighted", lines[1])
	}
	if strings.HasPrefix(lines[2], "\x1b[7m") {
		t.Errorf("unselected row %q is highlighted", lines[2])
	}
}

// Only a left-button press opens a worktree: releases, wheel ticks and plain
// keystrokes all have to fall through to the ordinary key handling.
func TestMouseRow(t *testing.T) {
	for k, want := range map[string]int{
		"\x1b[<0;12;5M":  5,   // left press on screen row 5
		"\x1b[<0;12;5m":  0,   // …its release
		"\x1b[<64;12;5M": 0,   // wheel up
		"\x1b[A":         0,   // arrow up
		"r":              0,   // a key
		"\x1b[<0;12;xM":  0,   // malformed row
		"\x1b[<0;12M":    0,   // truncated
		"\x1b[<0;1;999M": 999, // past the bottom of any screen; the caller bounds-checks
	} {
		if got := mouseRow(k); got != want {
			t.Errorf("mouseRow(%q) = %d, want %d", k, got, want)
		}
	}
}

// The background fetch has to actually move origin/main, and it has to stop
// when the tui does rather than outliving it.
func TestFetchMain(t *testing.T) {
	origin := initRepo(t)
	clone := t.TempDir()
	git(t, "clone", "--quiet", origin, clone)
	git(t, "-C", origin, "commit", "--allow-empty", "-m", "remote work")
	t.Chdir(clone)

	want := gitLine("-C", origin, "rev-parse", "HEAD")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); fetchMain(ctx, time.Hour) }() // an hour: only the first fetch is under test

	for range 100 {
		if gitLine("rev-parse", "origin/main") == want {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := gitLine("rev-parse", "origin/main"); got != want {
		t.Errorf("origin/main = %s after the background fetch, want %s", got, want)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("fetchMain outlived its context")
	}
}

// capture runs cmd with stdout redirected and returns what it printed.
func capture(t *testing.T, cmd interface{ Run() error }) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := cmd.Run()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	return strings.TrimRight(string(out), "\n")
}
