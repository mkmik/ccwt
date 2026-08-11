package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLockLifecycle covers the three states removal has to tell apart: a
// worktree ccwt locked (unlock, then remove), one the user locked by hand
// (leave the lock, refuse the removal), and a stale registration whose
// directory is gone (unlock so prune can reclaim it).
func TestLockLifecycle(t *testing.T) {
	// Resolved, because that is the shape git reports paths in and what
	// callers get out of RepoRoot (on macOS /var is a symlink to /private/var).
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	add := func(name string) string {
		t.Helper()
		wt := filepath.Join(repo, ".claude", "worktrees", name)
		if err := AddWorktree("", wt, "worktree-"+name); err != nil {
			t.Fatalf("AddWorktree(%s): %v", name, err)
		}
		if got := lockReason("", wt); got != LockReason {
			t.Fatalf("lockReason(%s) = %q, want %q", name, got, LockReason)
		}
		return wt
	}

	// ccwt's own lock is released and the worktree goes away.
	wt := add("ours")
	if err := RemoveWorktree("", wt); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk: %v", err)
	}

	// A hand-set lock is honoured: the removal fails and the tree survives.
	wt = add("theirs")
	for _, args := range [][]string{
		{"worktree", "unlock", wt},
		{"worktree", "lock", "--reason", "mine", wt},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := RemoveWorktree("", wt); err == nil {
		t.Error("RemoveWorktree on a hand-locked worktree: want error, got nil")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("hand-locked worktree was removed: %v", err)
	}

	// A locked-but-deleted directory is still prunable.
	wt = add("gone")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := PruneWorktrees(""); err != nil {
		t.Fatalf("PruneWorktrees: %v", err)
	}
	if r := lockReason("", wt); r != "" {
		t.Errorf("stale worktree still registered (lock %q)", r)
	}
}
