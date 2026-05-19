// Package gitutil holds helpers for interacting with git and with the
// Claude Code worktree layout (.claude/worktrees/<name>).
package gitutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RepoRoot runs `git rev-parse --show-toplevel` and returns the resulting
// path with its trailing newline trimmed. Git's stderr is passed through to
// the process stderr so the caller's user sees `fatal: not a git repository`
// and similar messages.
//
// If stripClaudeWorktree is true and the toplevel sits inside a Claude Code
// worktree (`.claude/worktrees/<name>`), the enclosing repository root is
// returned instead.
func RepoRoot(stripClaudeWorktree bool) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(string(out), "\n")
	if stripClaudeWorktree {
		path = stripClaudeWorktreeSuffix(path)
	}
	return path, nil
}

// AddWorktree creates a new linked worktree at `path` on a freshly-created
// branch `branch`, via `git worktree add -b <branch> <path>`. On success the
// command is silent — git's progress chatter ("Preparing worktree…",
// "HEAD is now at…") is captured and discarded. On failure the captured
// output is written verbatim to the process stderr so the user sees git's
// own error message.
func AddWorktree(path, branch string) error {
	var buf bytes.Buffer
	cmd := exec.Command("git", "worktree", "add", "-b", branch, path)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		os.Stderr.Write(buf.Bytes())
		return err
	}
	return nil
}

// RemoveWorktree removes the linked worktree at path via
// `git worktree remove --force <path>`. --force is used so a worktree
// with local modifications still gets removed. If the path is not a
// registered worktree (already gone), nil is returned — making the
// operation idempotent.
func RemoveWorktree(path string) error {
	var buf bytes.Buffer
	cmd := exec.Command("git", "worktree", "remove", "--force", path)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		out := buf.String()
		if strings.Contains(out, "is not a working tree") {
			return nil
		}
		os.Stderr.Write(buf.Bytes())
		return err
	}
	return nil
}

// PruneWorktrees runs `git worktree prune` to clean up stale registrations
// (worktree entries whose on-disk directory no longer exists).
func PruneWorktrees() error {
	var buf bytes.Buffer
	cmd := exec.Command("git", "worktree", "prune")
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		os.Stderr.Write(buf.Bytes())
		return err
	}
	return nil
}

// DeleteBranch deletes a local branch. Without force, `git branch -d` is
// used, which refuses to delete an unmerged branch; with force, `-D`
// force-deletes. A branch that doesn't exist is treated as success so the
// caller can re-invoke after a partial deletion without surfacing an error.
func DeleteBranch(branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	var buf bytes.Buffer
	cmd := exec.Command("git", "branch", flag, branch)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		out := buf.String()
		if strings.Contains(out, "not found") {
			return nil
		}
		os.Stderr.Write(buf.Bytes())
		return err
	}
	return nil
}

// Worktree is one entry from `git worktree list`.
type Worktree struct {
	Path   string
	Branch string // empty when HEAD is detached
}

// ListWorktrees parses `git worktree list --porcelain` and returns all
// registered worktrees (including the main one).
func ListWorktrees() ([]Worktree, error) {
	out, err := exec.Command("git", "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path != "" {
			wts = append(wts, cur)
		}
		cur = Worktree{}
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return wts, nil
}

// Commit holds the bits of a git commit we display.
type Commit struct {
	Time    time.Time
	Subject string
}

// LastCommit returns the HEAD commit of the repository at repoPath.
func LastCommit(repoPath string) (Commit, error) {
	out, err := exec.Command("git", "-C", repoPath, "log", "-1", "--format=%ct%n%s").Output()
	if err != nil {
		return Commit{}, err
	}
	parts := strings.SplitN(strings.TrimRight(string(out), "\n"), "\n", 2)
	if len(parts) < 2 {
		return Commit{}, fmt.Errorf("unexpected git log output: %q", string(out))
	}
	sec, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Commit{}, fmt.Errorf("parse commit time %q: %w", parts[0], err)
	}
	return Commit{Time: time.Unix(sec, 0), Subject: parts[1]}, nil
}

// CurrentClaudeWorktree returns the path and name of the Claude Code
// worktree that contains the git toplevel of the current working
// directory, or ("", "", nil) if the toplevel isn't shaped like
// .../.claude/worktrees/<name>. An error is returned only if
// `git rev-parse --show-toplevel` itself fails.
func CurrentClaudeWorktree() (path, name string, err error) {
	top, err := RepoRoot(false)
	if err != nil {
		return "", "", err
	}
	parent := filepath.Dir(top)
	if filepath.Base(parent) == "worktrees" && filepath.Base(filepath.Dir(parent)) == ".claude" {
		return top, filepath.Base(top), nil
	}
	return "", "", nil
}

func stripClaudeWorktreeSuffix(path string) string {
	parent := filepath.Dir(path)
	if filepath.Base(parent) == "worktrees" && filepath.Base(filepath.Dir(parent)) == ".claude" {
		return filepath.Dir(filepath.Dir(parent))
	}
	return path
}
