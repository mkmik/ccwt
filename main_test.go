package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
		bar := statusBar(40, msg)
		bar = strings.TrimSuffix(strings.TrimPrefix(bar, "\x1b[7m"), "\x1b[0m")
		if got := len([]rune(bar)); got != 40 {
			t.Errorf("statusBar(40, %.10q) is %d cols wide, want 40", msg, got)
		}
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
