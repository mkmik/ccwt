package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewPath covers `ccwt new --path`: the printed path must be absolute and
// must describe the same worktree the nameful form prints — including the
// enclosing-worktree case, where no worktree is created.
func TestNewPath(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

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
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	here := capture(t, &NewWorktreeBranchCmd{})
	other := capture(t, &NewWorktreeBranchCmd{})
	t.Chdir(capture(t, &NewWorktreeBranchCmd{Name: here, Path: true}))
	if err := os.WriteFile("untracked", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return true }
	for _, line := range strings.Split(capture(t, &ListCmd{}), "\n") {
		name, rest, _ := strings.Cut(line, " ")
		switch name {
		case "NAME":
		case "*" + here:
			if !strings.Contains(rest, "*init") {
				t.Errorf("dirty worktree missing commit marker: %q", line)
			}
		case other:
			if strings.Contains(rest, "*") {
				t.Errorf("clean worktree has a commit marker: %q", line)
			}
		default:
			t.Errorf("unexpected first column %q in line %q", name, line)
		}
	}

	stdoutIsTTY = func() bool { return false }
	if out := capture(t, &ListCmd{}); strings.Contains(out, "*") {
		t.Errorf("non-tty output contains a marker:\n%s", out)
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
