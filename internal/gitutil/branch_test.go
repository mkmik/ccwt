package gitutil

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAddWorktreeOnBranch covers the `ccwt new --switch <branch>` path: a
// worktree whose directory name is unrelated to the branch it checks out.
func TestAddWorktreeOnBranch(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
		{"branch", "foobar"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	wt := filepath.Join(repo, ".claude", "worktrees", "generated-name")
	if err := AddWorktreeOnBranch("", wt, "foobar"); err != nil {
		t.Fatalf("AddWorktreeOnBranch: %v", err)
	}
	out, err := exec.Command("git", "-C", wt, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "foobar\n" {
		t.Errorf("worktree HEAD = %q, want %q", got, "foobar\n")
	}
}

// TestAddWorktreeFromUpstream: a new branch starts from the upstream of the
// branch checked out in the repo, which is ahead of it, and doesn't track it.
func TestAddWorktreeFromUpstream(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	run := func(args ...string) string {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-b", "main")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "upstream")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("reset", "-q", "--hard", "HEAD~")
	run("remote", "add", "origin", repo)
	run("branch", "--set-upstream-to=origin/main")

	wt := filepath.Join(repo, ".claude", "worktrees", "fresh")
	if err := AddWorktree("", wt, "worktree-fresh"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if got, want := run("-C", wt, "rev-parse", "HEAD"), run("rev-parse", "origin/main"); got != want {
		t.Errorf("worktree HEAD = %q, want origin/main %q", got, want)
	}
	if out, err := exec.Command("git", "-C", wt, "rev-parse", "@{u}").CombinedOutput(); err == nil {
		t.Errorf("new branch tracks %s, want no upstream", out)
	}
}
