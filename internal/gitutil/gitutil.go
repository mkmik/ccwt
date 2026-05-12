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

func stripClaudeWorktreeSuffix(path string) string {
	parent := filepath.Dir(path)
	if filepath.Base(parent) == "worktrees" && filepath.Base(filepath.Dir(parent)) == ".claude" {
		return filepath.Dir(filepath.Dir(parent))
	}
	return path
}
