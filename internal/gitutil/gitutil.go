// Package gitutil holds helpers for interacting with git and with the
// Claude Code worktree layout (.claude/worktrees/<name>).
package gitutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// git builds a git command run inside dir, or inside the current working
// directory when dir is "" — which is how every repo-scoped helper here takes
// its "which repo?" argument.
func git(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}

// RepoRoot runs `git rev-parse --show-toplevel` and returns the resulting
// path with its trailing newline trimmed. Git's stderr is passed through to
// the process stderr so the caller's user sees `fatal: not a git repository`
// and similar messages.
//
// If stripClaudeWorktree is true and the toplevel sits inside a Claude Code
// worktree (`.claude/worktrees/<name>`), the enclosing repository root is
// returned instead.
func RepoRoot(dir string, stripClaudeWorktree bool) (string, error) {
	cmd := git(dir, "rev-parse", "--show-toplevel")
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

// LockReason is the reason ccwt-created worktrees are locked with, so
// `git worktree prune` never reclaims one whose directory is temporarily
// unavailable, and so removal can tell ccwt's own lock from a hand-made one.
const LockReason = "ccwt"

// AddWorktree creates a new linked worktree at `path` on a freshly-created
// branch `branch`, via `git worktree add -b <branch> <path>`. On success the
// command is silent — git's progress chatter ("Preparing worktree…",
// "HEAD is now at…") is captured and discarded. On failure the captured
// output is written verbatim to the process stderr so the user sees git's
// own error message.
func AddWorktree(dir, path, branch string) error {
	return addWorktree(dir, "-b", branch, path)
}

// AddWorktreeOnBranch creates a new linked worktree at `path` checked out on
// the *existing* branch `branch`, via `git worktree add <path> <branch>`.
// Output handling matches AddWorktree.
func AddWorktreeOnBranch(dir, path, branch string) error {
	return addWorktree(dir, path, branch)
}

func addWorktree(dir string, args ...string) error {
	var buf bytes.Buffer
	args = append([]string{"worktree", "add", "--lock", "--reason", LockReason}, args...)
	cmd := git(dir, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		os.Stderr.Write(buf.Bytes())
		return err
	}
	return nil
}

// LockWorktree leaves the worktree at path locked with ccwt's own reason, the
// same state AddWorktree creates one in — so a worktree made by hand, or one
// whose lock was dropped, gets the protection from `git worktree prune` that
// the rest of ccwt assumes. Already locked by ccwt is a no-op; a lock set with
// any other reason is replaced, since asking for ccwt's lock asks for that.
func LockWorktree(dir, path string) error {
	if lockReason(dir, path) == LockReason {
		return nil
	}
	// ponytail: the unlock's error is ignored — it fails only when the worktree
	// wasn't locked, which is the state we want. It also covers a lock set with
	// no reason at all, which lockReason can't tell apart from unlocked.
	git(dir, "worktree", "unlock", path).Run()
	if out, err := git(dir, "worktree", "lock", "--reason", LockReason, path).CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		return err
	}
	return nil
}

// RemoveWorktree removes the linked worktree at path via
// `git worktree remove --force <path>`. --force is used so a worktree
// with local modifications still gets removed. A worktree locked by ccwt
// (see LockReason) is unlocked first; one locked by hand with a different
// reason is left alone, so git refuses the removal and the user's lock
// does what they meant it to. If the path is not a registered worktree
// (already gone), nil is returned — making the operation idempotent.
func RemoveWorktree(dir, path string) error {
	if lockReason(dir, path) == LockReason {
		if out, err := git(dir, "worktree", "unlock", path).CombinedOutput(); err != nil {
			os.Stderr.Write(out)
			return err
		}
	}

	var buf bytes.Buffer
	cmd := git(dir, "worktree", "remove", "--force", path)
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
// (worktree entries whose on-disk directory no longer exists). Prune skips
// locked worktrees, so ccwt's own locks are released first for entries whose
// directory is already gone — otherwise a hand-deleted worktree could never
// be cleaned up. Locks set by hand with another reason are left in place.
func PruneWorktrees(dir string) error {
	wts, _ := ListWorktrees(dir)
	for _, wt := range wts {
		if wt.LockReason != LockReason {
			continue
		}
		if _, err := os.Stat(wt.Path); errors.Is(err, os.ErrNotExist) {
			git(dir, "worktree", "unlock", wt.Path).Run()
		}
	}

	var buf bytes.Buffer
	cmd := git(dir, "worktree", "prune")
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		os.Stderr.Write(buf.Bytes())
		return err
	}
	return nil
}

// DeleteBranch deletes a local branch with `git branch -D`. The safety valve
// is the caller's: `-d` would apply its own, *different* one — for a branch
// with an upstream it asks "merged into the upstream?", not "merged into
// main?", so a squash-merged branch whose stale origin/<branch> is still
// around (nobody ran `git fetch --prune`) refuses to go, even though it is
// contained in main. A branch that doesn't exist is treated as success so the
// caller can re-invoke after a partial deletion without surfacing an error.
func DeleteBranch(dir, branch string) error {
	var buf bytes.Buffer
	cmd := git(dir, "branch", "-D", branch)
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
	Path       string
	Branch     string // empty when HEAD is detached
	LockReason string // empty when unlocked, or locked without a reason
}

// ListWorktrees parses `git worktree list --porcelain` and returns all
// registered worktrees. The main worktree — the repository root the linked
// ones hang off — is always the first entry, which is git's own ordering.
func ListWorktrees(dir string) ([]Worktree, error) {
	out, err := git(dir, "worktree", "list", "--porcelain").Output()
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
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			flush()
			cur.Path = rest
		} else if rest, ok := strings.CutPrefix(line, "branch "); ok {
			cur.Branch = strings.TrimPrefix(rest, "refs/heads/")
		} else if rest, ok := strings.CutPrefix(line, "locked "); ok {
			cur.LockReason = rest
		}
	}
	flush()
	return wts, nil
}

// lockReason returns the reason the worktree at path is locked with, or ""
// when it is unlocked, unregistered, or git could not be asked.
func lockReason(dir, path string) string {
	wts, err := ListWorktrees(dir)
	if err != nil {
		return ""
	}
	for _, wt := range wts {
		if wt.Path == path {
			return wt.LockReason
		}
	}
	return ""
}

// Commit holds the bits of a git commit we display.
type Commit struct {
	Time    time.Time
	Subject string
}

// LastCommit returns the HEAD commit of the repository at repoPath.
func LastCommit(repoPath string) (Commit, error) {
	out, err := git(repoPath, "log", "-1", "--format=%ct%n%s").Output()
	if err != nil {
		return Commit{}, err
	}
	timeStr, subject, ok := strings.Cut(strings.TrimRight(string(out), "\n"), "\n")
	if !ok {
		return Commit{}, fmt.Errorf("unexpected git log output: %q", string(out))
	}
	sec, err := strconv.ParseInt(timeStr, 10, 64)
	if err != nil {
		return Commit{}, fmt.Errorf("parse commit time %q: %w", timeStr, err)
	}
	return Commit{Time: time.Unix(sec, 0), Subject: subject}, nil
}

// MergedBranches returns the set of local branches already contained in the
// repo's main branch ("main", or "master" when there is no "main") — local or
// remote-tracking, whichever contains them. A branch merged into origin/main
// is safe to delete even when nobody has pulled main yet, and a local-only
// merge is safe even when it hasn't been pushed, so take the union of the two.
// A git failure yields an empty set: this only decorates a listing.
func MergedBranches(dir string) map[string]bool {
	merged := map[string]bool{}
	for _, base := range []string{"main", "master"} {
		var found bool
		for _, ref := range []string{base, "origin/" + base} {
			out, err := git(dir, "branch", "--merged", ref, "--format=%(refname:short)").Output()
			if err != nil {
				continue // ref doesn't resolve: no remote, or the other base name
			}
			found = true
			for b := range strings.SplitSeq(string(out), "\n") {
				if b != "" {
					merged[b] = true
				}
			}
		}
		if found {
			break
		}
	}
	return merged
}

// BranchExists reports whether refs/heads/<branch> resolves.
func BranchExists(dir, branch string) bool {
	return git(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// Pushed reports whether the branch at repoPath has an upstream that already
// contains every commit it does — work that has left your hands and is waiting
// on someone else. No upstream, or commits the remote hasn't got, reads as not
// pushed, and so does a git failure: this only decorates a listing.
//
// The upstream is whatever the branch is tracking, stale ref and all: the
// answer is "have I pushed?", which is about the last push, not the last fetch.
func Pushed(repoPath string) bool {
	out, err := git(repoPath, "rev-list", "--count", "@{upstream}..HEAD").Output()
	return err == nil && strings.TrimSpace(string(out)) == "0"
}

// Dirty reports whether the worktree at repoPath has uncommitted changes,
// untracked files included. A git failure reads as clean: this only decorates
// a listing, and is not worth failing it over.
func Dirty(repoPath string) bool {
	out, err := git(repoPath, "status", "--porcelain").Output()
	return err == nil && len(out) > 0
}

// CurrentClaudeWorktree returns the path and name of the Claude Code
// worktree that contains the git toplevel of the current working
// directory, or ("", "", nil) if the toplevel isn't shaped like
// .../.claude/worktrees/<name>. An error is returned only if
// `git rev-parse --show-toplevel` itself fails.
func CurrentClaudeWorktree() (path, name string, err error) {
	top, err := RepoRoot("", false)
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

// ClaudeWorktreeRepoRoot derives the enclosing repository root for a path that
// lives inside a Claude Code worktree (.../.claude/worktrees/<name>, or any
// descendant of it) using pure string manipulation — no git invocation and no
// filesystem access. It returns the path up to, but not including, the
// .claude directory and true; or ("", false) when path is not inside such a
// worktree.
//
// Unlike stripClaudeWorktreeSuffix, which only strips an exact
// .claude/worktrees/<name> suffix, this locates the component anywhere in the
// path. It exists for the case where the worktree directory has been deleted
// while it was the current directory: git can no longer read the cwd (getcwd
// fails), so RepoRoot errors out, but the shell still records the old path in
// $PWD and that string is enough to compute where to escape to.
func ClaudeWorktreeRepoRoot(path string) (string, bool) {
	sep := string(filepath.Separator)
	segs := strings.Split(path, sep)
	for i := 0; i+2 < len(segs); i++ {
		if segs[i] == ".claude" && segs[i+1] == "worktrees" && segs[i+2] != "" {
			return strings.Join(segs[:i], sep), true
		}
	}
	return "", false
}
