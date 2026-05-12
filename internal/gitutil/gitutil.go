// Package gitutil holds helpers for interacting with git and with the
// Claude Code worktree layout (.claude/worktrees/<name>).
package gitutil

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func stripClaudeWorktreeSuffix(path string) string {
	parent := filepath.Dir(path)
	if filepath.Base(parent) == "worktrees" && filepath.Base(filepath.Dir(parent)) == ".claude" {
		return filepath.Dir(filepath.Dir(parent))
	}
	return path
}
