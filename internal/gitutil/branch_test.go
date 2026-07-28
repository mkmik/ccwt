package gitutil

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAddWorktreeOnBranch covers the `ccwt new <existing-branch>` path: a
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

	if !BranchExists("foobar") {
		t.Error("BranchExists(foobar) = false, want true")
	}
	if BranchExists("nope") {
		t.Error("BranchExists(nope) = true, want false")
	}

	wt := filepath.Join(repo, ".claude", "worktrees", "generated-name")
	if err := AddWorktreeOnBranch(wt, "foobar"); err != nil {
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
