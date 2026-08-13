package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/kong"
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
// and a dirty worktree's leading removal glyph, where the "*" also has to beat
// the "✓" the merged branch would otherwise get. Each must appear on its row
// only, and only when stdout is a tty.
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
		case line == "" || strings.HasPrefix(line, "    NAME"):
		case strings.HasPrefix(line, "* * "+here+" "):
			if strings.Contains(line, "✓") {
				t.Errorf("dirty worktree marked merged: %q", line)
			}
		case strings.HasPrefix(line, "  ✓ "+other+" "):
			if strings.Contains(line, "*") {
				t.Errorf("clean worktree has a dirty marker: %q", line)
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

// TestListNoHeaders: --no-headers drops the header row and nothing else.
func TestListNoHeaders(t *testing.T) {
	initRepo(t)
	name := capture(t, &NewWorktreeBranchCmd{})

	if out := capture(t, &ListCmd{}); !strings.HasPrefix(out, "NAME") {
		t.Errorf("default output has no header row:\n%s", out)
	}
	out := capture(t, &ListCmd{NoHeaders: true})
	if strings.Contains(out, "NAME") {
		t.Errorf("--no-headers printed a header row:\n%s", out)
	}
	if !strings.HasPrefix(out, name+" ") {
		t.Errorf("--no-headers dropped the worktree row:\n%s", out)
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
			name, _, _ := strings.Cut(strings.TrimLeft(line, "✓* "), " ")
			want := tty && name == "merged"
			if got := strings.HasPrefix(line, "  ✓ "); got != want {
				t.Errorf("tty=%v: merged glyph = %v, want %v: %q", tty, got, want, line)
			}
		}
	}
}

// TestListMarksWaitingForReview: a branch whose commits are all on its upstream
// gets the "☐" glyph — pushed, nothing left to do here but wait. A branch with
// a commit the upstream hasn't got is still the author's problem and stays bare.
func TestListMarksWaitingForReview(t *testing.T) {
	repo := initRepo(t)
	git(t, "remote", "add", "origin", repo)
	for _, name := range []string{"pushed", "unpushed"} {
		path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
		git(t, "-C", path, "commit", "--allow-empty", "-m", name)
		branch := "worktree-" + name
		// The branch as pushed, then a commit on top of it for "unpushed".
		git(t, "update-ref", "refs/remotes/origin/"+branch, "refs/heads/"+branch)
		git(t, "config", "branch."+branch+".remote", "origin")
		git(t, "config", "branch."+branch+".merge", "refs/heads/"+branch)
		if name == "unpushed" {
			git(t, "-C", path, "commit", "--allow-empty", "-m", "more")
		}
	}

	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return true }
	for _, line := range strings.Split(capture(t, &ListCmd{}), "\n") {
		name, _, _ := strings.Cut(strings.TrimLeft(line, "✓*"+reviewGlyph+" "), " ")
		want := name == "pushed"
		if got := strings.HasPrefix(line, "  "+reviewGlyph+" "); got != want {
			t.Errorf("review glyph on %q = %v, want %v: %q", name, got, want, line)
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
	if !gitutil.BranchExists("", "worktree-ahead") {
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
	if gitutil.BranchExists("", "worktree-squashed") {
		t.Error("worktree removed but the branch was stranded")
	}
}

// TestRemoveMergedUpstream: a branch merged into origin/main is removable even
// when local main hasn't been pulled and so doesn't contain it yet.
func TestRemoveMergedUpstream(t *testing.T) {
	initRepo(t)
	path := capture(t, &NewWorktreeBranchCmd{Name: "upstreamed", Path: true})
	git(t, "-C", path, "commit", "--allow-empty", "-m", "work")

	// The PR merged: origin/main moved on to contain the branch, local main
	// still sits where it was because nobody pulled.
	git(t, "update-ref", "refs/remotes/origin/main", "refs/heads/worktree-upstreamed")

	if err := (&RemoveCmd{Name: "upstreamed"}).Run(); err != nil {
		t.Fatalf("removing a branch merged into origin/main: %v", err)
	}
	if gitutil.BranchExists("", "worktree-upstreamed") {
		t.Error("worktree removed but the branch was stranded")
	}
}

// TestRemoveUnderHerdr: under herdr the worktree is usually an open workspace
// with an agent in it, so `remove` closes it before the directory goes away —
// but only once the checks pass. Uncommitted changes are one of those checks:
// the removal is forced, so they'd be gone for good, and the workspace must
// still be there afterwards.
func TestRemoveUnderHerdr(t *testing.T) {
	initRepo(t)
	path := capture(t, &NewWorktreeBranchCmd{Name: "open", Path: true})
	if err := os.WriteFile(filepath.Join(path, "scratch"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// A herdr that records what it was asked to do and reports the worktree as
	// open in workspace w9.
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\n"+
		"[ \"$1 $2\" = \"worktree list\" ] && printf '{\"result\":{\"worktrees\":"+
		"[{\"path\":\"%s\",\"open_workspace_id\":\"w9\"}]}}'\nexit 0\n", log, path)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")

	if err := (&RemoveCmd{Name: "open"}).Run(); err == nil {
		t.Error("removing a dirty worktree succeeded, want refusal")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("refused remove closed the workspace anyway")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("refused remove deleted the worktree anyway: %v", err)
	}

	if err := (&RemoveCmd{Name: "open", Force: true}).Run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(log); !strings.Contains(string(got), "workspace close w9") {
		t.Errorf("herdr calls = %q, want the workspace closed", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists (%v)", path, err)
	}
}

// TestBranchPrefixFromConfig: branch_prefix names the branch `new` creates, and
// `remove` deletes that same branch rather than the "worktree-" one it never
// made — which would leave the branch stranded.
func TestBranchPrefixFromConfig(t *testing.T) {
	initRepo(t)
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(), []byte("branch_prefix = \"wt/\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	capture(t, &NewWorktreeBranchCmd{Name: "prefixed"})
	if !gitutil.BranchExists("", "wt/prefixed") {
		t.Fatal("new did not use the configured branch prefix")
	}
	if err := (&RemoveCmd{Name: "prefixed"}).Run(); err != nil {
		t.Fatal(err)
	}
	if gitutil.BranchExists("", "wt/prefixed") {
		t.Error("worktree removed but the prefixed branch was stranded")
	}
}

// TestHerdrOpenLabelsNewWorkspacesOnly: herdr lists a worktree workspace under
// its repo by branch, so a new one is opened with a --label to keep the prefix
// out of the sidebar. Reopening passes none: that would rename a workspace the
// user had renamed themselves.
func TestHerdrOpenLabelsNewWorkspacesOnly(t *testing.T) {
	initRepo(t)

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	if err := os.WriteFile(herdr, fmt.Appendf(nil, "#!/bin/sh\necho \"$@\" >> %q\n", log), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)

	msg := (&ui{}).newWorktree()
	name, ok := strings.CutPrefix(msg, "opened ")
	if !ok {
		t.Fatalf("newWorktree: %s", msg)
	}
	if got, _ := os.ReadFile(log); !strings.Contains(string(got), "--label "+name) {
		t.Errorf("herdr calls = %q, want the new workspace labelled %q", got, name)
	}

	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
	if msg := herdrOpen(path, ""); !strings.HasPrefix(msg, "opened ") {
		t.Fatalf("herdrOpen: %s", msg)
	}
	if got, _ := os.ReadFile(log); strings.Contains(string(got), "--label") {
		t.Errorf("herdr calls = %q, want a reopen to leave the workspace name alone", got)
	}
}

// TestDone: `done` is `remove .` plus closing our own workspace — the one
// `remove` leaves alone — and only after the removal went through: a refusal
// leaves both the worktree and the workspace where they were.
func TestDone(t *testing.T) {
	initRepo(t)
	path := capture(t, &NewWorktreeBranchCmd{Name: "mine", Path: true})
	t.Chdir(path)
	if err := os.WriteFile(filepath.Join(path, "scratch"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\n"+
		"[ \"$1 $2\" = \"worktree list\" ] && printf '{\"result\":{\"worktrees\":"+
		"[{\"path\":\"%s\",\"open_workspace_id\":\"w1\"}]}}'\nexit 0\n", log, path)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")

	if err := (&DoneCmd{}).Run(); err == nil {
		t.Error("done on a dirty worktree succeeded, want refusal")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("refused done closed the workspace anyway")
	}

	if err := (&DoneCmd{Force: true}).Run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(log); !strings.Contains(string(got), "workspace close w1") {
		t.Errorf("herdr calls = %q, want our own workspace closed", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists (%v)", path, err)
	}
}

// TestGc: only worktrees that are both merged and idle are collected — an
// unmerged one and one with a session running in it stay put — and what `gc`
// removes, it removes the way `remove` does, branch and all.
func TestGc(t *testing.T) {
	initRepo(t)
	merged := capture(t, &NewWorktreeBranchCmd{Name: "merged", Path: true})
	busy := capture(t, &NewWorktreeBranchCmd{Name: "busy", Path: true})
	ahead := capture(t, &NewWorktreeBranchCmd{Name: "ahead", Path: true})
	git(t, "-C", ahead, "commit", "--allow-empty", "-m", "ahead")

	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		t.Fatal(err)
	}
	// A session sitting in a subdirectory of "busy" still counts as running there.
	got, err := gcCandidates(root, map[string]bool{filepath.Join(busy, "internal"): true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"merged"}) {
		t.Fatalf("candidates = %v, want [merged]", got)
	}

	// No Claude runs in a temp repo, so the full command sees "merged" and
	// "busy" both idle and takes them; "ahead" is unmerged and stays.
	if err := (&GcCmd{Yes: true}).Run(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{merged, busy} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("worktree %s still exists (%v)", path, err)
		}
	}
	if gitutil.BranchExists("", "worktree-merged") {
		t.Error("worktree removed but the branch was stranded")
	}
	if _, err := os.Stat(ahead); err != nil {
		t.Errorf("gc removed the unmerged worktree %s: %v", ahead, err)
	}
}

// TestGcKeepsCurrentWorktree: gc collects everything else, but the worktree the
// process is standing in survives — removing it would leave the shell in a
// deleted directory.
func TestGcKeepsCurrentWorktree(t *testing.T) {
	initRepo(t)
	here := capture(t, &NewWorktreeBranchCmd{Name: "here", Path: true})
	other := capture(t, &NewWorktreeBranchCmd{Name: "other", Path: true})
	t.Chdir(here)

	if err := (&GcCmd{Yes: true}).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(here); err != nil {
		t.Errorf("gc removed the worktree it was run from: %v", err)
	}
	if !gitutil.BranchExists(here, "worktree-here") {
		t.Error("gc deleted the branch of the worktree it was run from")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists (%v)", other, err)
	}
}

// TestAgentWorkingIsNotSafeToRemove: a worktree an agent is working in is off
// limits even though git has nothing against it — the branch is merged (it has
// no commits of its own yet) and the tree is clean. It gets the session glyph
// where the "✓" would go at the head of the row, `remove` refuses it until -D,
// and `gc` walks past it.
func TestAgentWorkingIsNotSafeToRemove(t *testing.T) {
	initRepo(t)
	busy := capture(t, &NewWorktreeBranchCmd{Name: "busy", Path: true})
	idle := capture(t, &NewWorktreeBranchCmd{Name: "idle", Path: true})

	defer func(orig func() map[string]bool) { herdrBusy = orig }(herdrBusy)
	// A subdirectory: where an agent that cd'd deeper into the worktree reports from.
	herdrBusy = func() map[string]bool { return map[string]bool{filepath.Join(busy, "internal"): true} }

	defer func(orig func() bool) { stdoutIsTTY = orig }(stdoutIsTTY)
	stdoutIsTTY = func() bool { return true }
	for _, line := range strings.Split(capture(t, &ListCmd{}), "\n") {
		name, _, _ := strings.Cut(strings.TrimLeft(line, "✓* "+sessionGlyph), " ")
		want := map[string]string{"busy": sessionGlyph, "idle": "✓"}[name]
		if want == "" {
			continue // header, or the blank last line
		}
		if !strings.HasPrefix(line, "  "+want+" ") {
			t.Errorf("%s: want %q leading the row: %q", name, want, line)
		}
	}

	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&RemoveCmd{Name: "busy"}).remove(root); err == nil {
		t.Error("removed a worktree with an agent working in it, want refusal")
	}
	if err := (&GcCmd{Yes: true}).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(busy); err != nil {
		t.Errorf("gc collected the worktree an agent is working in: %v", err)
	}
	if _, err := os.Stat(idle); !os.IsNotExist(err) {
		t.Errorf("gc left the idle worktree %s behind (%v)", idle, err)
	}
	if err := (&RemoveCmd{Name: "busy", Force: true}).remove(root); err != nil {
		t.Errorf("remove -D of a worktree with an agent working in it: %v", err)
	}
}

// initRepo makes a git repo with one commit on main and chdirs into it.
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // ignore the user's own config (branch_prefix)
	t.Setenv("XDG_STATE_HOME", t.TempDir())  // ... and the queued prompts of the machine we're testing on
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
	if got, want := gitDivergence("."), "main  (no upstream)"; got != want {
		t.Errorf("gitDivergence() = %q, want %q", got, want)
	}

	// A local branch as its own upstream is enough to exercise the counting.
	git(t, "branch", "upstream")
	git(t, "config", "branch.main.remote", ".")
	git(t, "config", "branch.main.merge", "refs/heads/upstream")
	if got, want := gitDivergence("."), "main  in sync"; got != want {
		t.Errorf("gitDivergence() = %q, want %q", got, want)
	}

	git(t, "commit", "--allow-empty", "-m", "ahead")
	if got, want := gitDivergence("."), "main  ↑1 ↓0"; got != want {
		t.Errorf("gitDivergence() = %q, want %q", got, want)
	}

	// No repo to ask — which is what -g hands it until a row is selected.
	if got := gitDivergence(""); got != "" {
		t.Errorf("gitDivergence(\"\") = %q, want empty", got)
	}
}

// Under -g the current directory is not one of the repos in view, so the keys
// and the status bar have to reach the selected row's project instead — and
// nothing at all while there's no selection.
func TestGlobalModeIgnoresTheCurrentDirectory(t *testing.T) {
	root := initRepo(t)

	global := ui{projects: []string{root}}
	if got := global.gitDir(); got != "" {
		t.Errorf("gitDir() with nothing selected = %q, want empty", got)
	}
	if _, err := global.root(); err == nil {
		t.Error("root() with nothing selected fell back to the current directory")
	}
	global.sel = listRow{project: root, path: filepath.Join(root, ".claude", "worktrees", "somewhere")}
	if got := global.gitDir(); got != root {
		t.Errorf("gitDir() = %q, want the selected row's project %q", got, root)
	}

	// Outside -g the current directory is still the answer.
	local := ui{}
	if got := local.gitDir(); got != "." {
		t.Errorf("gitDir() outside -g = %q, want \".\"", got)
	}
	if _, err := local.root(); err != nil {
		t.Errorf("root() outside -g: %v", err)
	}
}

// The status bar has to be exactly one screen line wide, whether the message
// is short (pad) or long (cut) — otherwise it wraps and shoves the table up.
func TestStatusBarIsExactlyOneLineWide(t *testing.T) {
	for _, msg := range []string{"", "ok", strings.Repeat("x", 200)} {
		for _, sel := range []listRow{{}, {path: "some-worktree"}, {project: "some-project"}, {task: 1}} {
			for _, searching := range []bool{false, true} {
				bar := statusBar(40, msg, sel, "main  in sync", searching)
				bar = strings.TrimSuffix(strings.TrimPrefix(bar, "\x1b[7m"), "\x1b[0m")
				if got := len([]rune(bar)); got != 40 {
					t.Errorf("statusBar(40, %.10q, %v) is %d cols wide, want 40", msg, sel, got)
				}
			}
		}
	}
}

// The keys that call herdr only work inside a herdr pane, so outside one the
// bar must not advertise them — while the keys that don't need herdr stay.
func TestStatusBarShowsHerdrActionsOnlyUnderHerdr(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{{"", false}, {"1", true}} {
		t.Setenv("HERDR_ENV", tc.env)
		bar := statusBar(200, "", listRow{path: "some-worktree"}, "", false)
		for _, key := range []string{"x:new", "space:open"} {
			if strings.Contains(bar, key) != tc.want {
				t.Errorf("HERDR_ENV=%q: %q in the bar = %v, want %v", tc.env, key, !tc.want, tc.want)
			}
		}
		if !strings.Contains(bar, "r:remove") {
			t.Errorf("HERDR_ENV=%q: r:remove is gone, but it doesn't need herdr", tc.env)
		}
	}
}

// `n` is two keys in one: the next thing to do, and — while a search pattern is
// in force — vim's next match. The bar is the only place that says which, so it
// has to keep up.
func TestStatusBarSaysWhichNIsInForce(t *testing.T) {
	sel := listRow{path: "some-worktree"}
	if bar := statusBar(200, "", sel, "", false); !strings.Contains(bar, "n:queue") || strings.Contains(bar, "n:next") {
		t.Errorf("with no pattern the bar is %q, want n:queue on it", bar)
	}
	if bar := statusBar(200, "", sel, "", true); !strings.Contains(bar, "n:next") || strings.Contains(bar, "n:queue") {
		t.Errorf("with a pattern in force the bar is %q, want n:next on it", bar)
	}
}

// A tui left open for days goes on running the code it started with, so an
// upgrade underneath it has to reach the status bar — but only a real one: an
// unreadable stamp (the file is being replaced right now, or was never
// stattable) must not stick a notice up that nothing can take back down.
func TestUpgradeNoticeNeedsTwoReadableStamps(t *testing.T) {
	var now string
	defer func(old func() string) { selfStamp = old }(selfStamp)
	selfStamp = func() string { return now }

	for _, tc := range []struct {
		name        string
		start, then string
		want        bool
	}{
		{"unchanged", "1@1", "1@1", false},
		{"replaced", "1@1", "2@2", true},
		{"unreadable at startup", "", "2@2", false},
		{"unreadable now", "1@1", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := ui{stamp: tc.start}
			now = tc.then
			u.checkUpgrade()
			if got := u.restart != ""; got != tc.want {
				t.Errorf("after %q -> %q the bar says %q, want a notice = %v", tc.start, tc.then, u.restart, tc.want)
			}
		})
	}

	// And once it's up it stays up: only restarting clears it, so a later tick
	// that reads the new binary as unchanged mustn't wipe the notice.
	u := ui{stamp: "1@1"}
	now = "2@2"
	u.checkUpgrade()
	said := u.restart
	u.stamp, now = "2@2", "2@2"
	u.checkUpgrade()
	if u.restart != said {
		t.Errorf("the notice changed to %q on a later tick, want it to stay %q", u.restart, said)
	}
}

// The notice is no use if nothing shows it. It outlives the transient messages
// — which the next tick wipes — but steps aside while one is on screen.
func TestFrameShowsTheRestartNotice(t *testing.T) {
	initRepo(t)
	defer func(old func() (int, int)) { termSize = old }(termSize)
	termSize = func() (int, int) { return 200, 6 }

	u := ui{restart: "upgraded to v9.9.9 — restart ccwt"}
	bar := func() string {
		lines, err := u.frame()
		if err != nil {
			t.Fatal(err)
		}
		return lines[len(lines)-1]
	}
	if got := bar(); !strings.Contains(got, u.restart) {
		t.Errorf("status bar is %q, want the restart notice on it", got)
	}
	u.msg = "pulling…"
	if got := bar(); !strings.Contains(got, "pulling…") {
		t.Errorf("status bar is %q, want the transient message while it's up", got)
	}
}

// worktreeRows is what a frame hands back for a list of plain worktree rows.
func worktreeRows(paths ...string) []listRow {
	rows := make([]listRow, len(paths))
	for i, p := range paths {
		rows[i] = listRow{path: p}
	}
	return rows
}

// The selection is kept by identity, so it has to survive the list changing
// under it: rows reordering (a commit lands elsewhere) must not move it, and
// the selected worktree disappearing must not wedge the arrows.
func TestSelectionFollowsTheWorktree(t *testing.T) {
	u := ui{rows: worktreeRows("alpha", "bravo", "charlie")}
	u.move(1) // nothing selected yet: start at the top
	u.move(1)
	if u.sel.path != "bravo" {
		t.Fatalf("after two downs sel = %q, want bravo", u.sel.path)
	}

	// A commit reorders the list: the selection stays on bravo, and the next
	// arrow moves relative to where bravo sits now.
	u.rows = worktreeRows("bravo", "charlie", "alpha")
	u.move(1)
	if u.sel.path != "charlie" {
		t.Errorf("after the rows reordered, sel = %q, want charlie", u.sel.path)
	}

	u.rows = worktreeRows("alpha") // bravo removed out from under us
	u.sel = listRow{path: "bravo"}
	u.move(1)
	if u.sel.path != "alpha" {
		t.Errorf("after the selected worktree vanished, sel = %q, want alpha", u.sel.path)
	}

	u.rows = nil
	u.move(-1) // must not panic on an empty list
}

// Removing a worktree must not dump you back at nothing selected: `r` twice in
// a row should remove two worktrees, not one.
func TestSelectionSurvivesRemove(t *testing.T) {
	u := ui{rows: worktreeRows("alpha", "bravo", "charlie"), sel: listRow{path: "bravo"}}
	u.dropSelected()
	if u.sel.path != "charlie" { // the row below
		t.Errorf("after removing a middle row, sel = %q, want charlie", u.sel.path)
	}

	u.rows = worktreeRows("alpha", "charlie")
	u.dropSelected()
	if u.sel.path != "alpha" { // last row: fall back to the one above
		t.Errorf("after removing the last row, sel = %q, want alpha", u.sel.path)
	}

	u.rows = worktreeRows("alpha")
	u.dropSelected() // the only row: nothing left to select, frame clears it
	if u.sel.path != "alpha" {
		t.Errorf("after removing the only row, sel = %q, want alpha", u.sel.path)
	}
}

// A click is turned into a worktree by counting screen lines, so the frame's
// row i must really be paths[i] — off by the one header line, and clicking a
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
	for i, r := range u.rows {
		if !strings.Contains(lines[i+1], filepath.Base(r.path)) {
			t.Errorf("rows[%d] = %q, but line %d is %q", i, r.path, i+1, lines[i+1])
		}
	}
	if !strings.HasPrefix(lines[1], rowBar) {
		t.Errorf("selected row %q is not highlighted", lines[1])
	}
	if strings.HasPrefix(lines[2], rowBar) {
		t.Errorf("unselected row %q is highlighted", lines[2])
	}
}

// Moving the selection changes which row is highlighted and nothing else, so
// it must not re-run the git and lsof scan behind the list — on a big repo
// that's half a second per keypress. Only the interval tick (and anything that
// changes the list) re-reads it.
func TestMoveReusesTheList(t *testing.T) {
	initRepo(t)
	capture(t, &NewWorktreeBranchCmd{Name: "alpha", Path: true})

	var u ui
	if _, err := u.frame(); err != nil {
		t.Fatal(err)
	}
	capture(t, &NewWorktreeBranchCmd{Name: "bravo", Path: true}) // appears only on a re-read

	u.move(1)
	if _, err := u.frame(); err != nil {
		t.Fatal(err)
	}
	if len(u.rows) != 1 {
		t.Errorf("moving re-read the list: %d rows, want the 1 the cache had", len(u.rows))
	}

	u.stale()
	if _, err := u.frame(); err != nil {
		t.Fatal(err)
	}
	if len(u.rows) != 2 {
		t.Errorf("after stale() the list has %d rows, want both worktrees", len(u.rows))
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

// One click selects, two open: a click on the way past a row shouldn't spawn a
// workspace, and the terminal won't tell us which is which, so the timing is
// ours to get right.
func TestClickDoubles(t *testing.T) {
	alpha, bravo := listRow{path: "alpha"}, listRow{path: "bravo"}

	var u ui
	if u.click(alpha) {
		t.Error("the first click on a row acted on it, want it to only select")
	}
	if u.sel != alpha {
		t.Errorf("after one click sel = %q, want alpha", u.sel.path)
	}
	if !u.click(alpha) {
		t.Error("the second click on the same row didn't act on it")
	}
	if u.click(alpha) {
		t.Error("a third click acted again, want it to start a fresh pair")
	}

	// A second click somewhere else is a first click there.
	u.click(bravo)
	if u.click(alpha) {
		t.Error("clicking back to a row acted on it, want it to only select")
	}

	// Two clicks far enough apart are two single clicks.
	u.clicked = time.Now().Add(-2 * doubleClick)
	if u.click(alpha) {
		t.Error("a slow second click acted on the row, want it to only select")
	}
}

// One read of stdin is not one keystroke: hold an arrow down and it repeats
// faster than a frame draws, so the tty hands over several sequences at once.
// Every one of them has to come back out, or the list doesn't move while a key
// is held — the whole chunk matches no binding and is dropped.
func TestSplitKeys(t *testing.T) {
	for in, want := range map[string][]string{
		"j":                          {"j"},
		"jjj":                        {"j", "j", "j"},
		"\x1b[B":                     {"\x1b[B"},
		"\x1b[B\x1b[B\x1b[A":         {"\x1b[B", "\x1b[B", "\x1b[A"},
		"\x1b[<0;12;5M\x1b[<0;12;5m": {"\x1b[<0;12;5M", "\x1b[<0;12;5m"},
		"j\x1b[Bq":                   {"j", "\x1b[B", "q"},
		"\x1b":                       {"\x1b"},  // a bare escape
		"\x1b[":                      {"\x1b["}, // a sequence cut in half by the read
		"":                           nil,
	} {
		if got := splitKeys(in); !slices.Equal(got, want) {
			t.Errorf("splitKeys(%q) = %q, want %q", in, got, want)
		}
	}
}

// A list taller than the terminal has to scroll, not just get cut: -g routinely
// runs to several screenfuls, and rows below the fold were unreachable — the
// arrows stopped at the bottom edge, and folding a section away doesn't help
// when the section header is itself below it.
func TestFrameScrollsToTheSelection(t *testing.T) {
	initRepo(t)
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
		git(t, "-C", path, "commit", "--allow-empty", "-m", "subject-"+name)
	}
	// Four lines: header, two rows, status bar. The third row only exists once
	// the frame scrolls to it.
	defer func(old func() (int, int)) { termSize = old }(termSize)
	termSize = func() (int, int) { return 80, 4 }

	var u ui
	lines, err := u.frame()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 {
		t.Fatalf("frame is %d lines, want 4 — anything taller scrolls the screen", len(lines))
	}
	if len(u.rows) != 3 {
		t.Fatalf("frame listed %d rows, want 3", len(u.rows))
	}

	// Walk to the bottom row. It is below the fold, so the frame has to scroll.
	for range 3 {
		u.move(1)
	}
	want := u.rows[2]
	if u.sel != want {
		t.Fatalf("three downs left the selection on %q, want the last row %q", u.sel.path, want.path)
	}
	if lines, err = u.frame(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 {
		t.Fatalf("scrolled frame is %d lines, want 4", len(lines))
	}
	if u.top != 1 {
		t.Errorf("frame scrolled to row %d, want 1 — just far enough to show the selection", u.top)
	}
	if name := filepath.Base(want.path); !strings.Contains(lines[2], name) {
		t.Errorf("last line of the table is %q, want the selected %q on it", lines[2], name)
	}
	if !strings.HasPrefix(lines[2], rowBar) {
		t.Errorf("the selected row %q is not highlighted", lines[2])
	}

	// A click counts screen lines, so it has to count them through the scroll.
	if got := u.at(3); got != want {
		t.Errorf("clicking the last table line selected %q, want %q", got.path, want.path)
	}
	if got := u.at(1); got != (listRow{}) { // the header
		t.Errorf("clicking the header selected %q, want nothing", got.path)
	}

	// Walking back up scrolls the other way.
	u.move(-1)
	u.move(-1)
	if _, err = u.frame(); err != nil {
		t.Fatal(err)
	}
	if u.top != 0 {
		t.Errorf("after walking back to the top, frame starts at row %d, want 0", u.top)
	}
}

// `/` types a pattern into the bar and moves the selection to the matching row
// as it's typed; n and N walk the rest of the matches, wrapping around the ends.
//
// The rows are ordered newest-commit-first, but three commits a few
// milliseconds apart share a timestamp and the sort is stable, so the order
// here is whatever `git worktree list` gives — hence the assertions below are
// written against `order` rather than against the names.
func TestSearch(t *testing.T) {
	initRepo(t)
	// One fixed commit date, so the three really do share a timestamp: a run
	// that happened to straddle a second boundary sorted the last one to the
	// top instead, and every assertion below is written against the order.
	t.Setenv("GIT_AUTHOR_DATE", "2024-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2024-01-01T00:00:00Z")
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
		git(t, "-C", path, "commit", "--allow-empty", "-m", "subject-"+name)
	}

	var u ui
	if _, err := u.frame(); err != nil { // the frame is what fills in the rows
		t.Fatal(err)
	}
	var order []string
	for _, r := range u.rows {
		order = append(order, filepath.Base(r.path))
	}
	if len(order) != 3 || order[1] != "bravo" {
		t.Fatalf("rows are %v, want the three worktrees with bravo in the middle", order)
	}
	sel := func() string { return filepath.Base(u.sel.path) }
	// type_ feeds the prompt a string the way the tty does — a byte at a time,
	// control bytes (enter, backspace, escape) among them.
	type_ := func(s string) {
		for _, k := range splitKeys(s) {
			u.edit(k)
		}
	}

	// Incremental: the match follows the pattern keystroke by keystroke, with
	// no enter involved — including backwards, when a character is rubbed out.
	// "v" is on bravo's line and on no other (unlike "b", which "subject-" puts
	// on every one of them).
	u.prompt(1)
	type_("V")
	if sel() != "bravo" { // case-insensitive
		t.Errorf("/V selected %q, want bravo out of %v", sel(), order)
	}
	type_("x")
	if u.sel != (listRow{}) {
		t.Errorf("/Vx selected %q, want the selection back where the prompt opened", sel())
	}
	type_("\x7fo\r")
	if u.typing || u.query != "Vo" || sel() != "bravo" || u.msg != "" {
		t.Fatalf("after typing: query %q typing %v sel %q msg %q, want %q false bravo none", u.query, u.typing, sel(), u.msg, "Vo")
	}

	// Every match on screen is picked out, the selected row's out of its bar.
	lines, err := u.frame()
	if err != nil {
		t.Fatal(err)
	}
	row := lines[slices.Index(order, "bravo")+1]
	if !strings.Contains(row, "\x1b[0mvo"+rowBar) {
		t.Errorf("selected row is %q, want the match punched out of the bar", row)
	}

	// n wraps around the list to come back to the only match.
	u.seek(1)
	if sel() != "bravo" || u.msg != "" {
		t.Errorf("n from the only match selected %q (%q), want bravo", sel(), u.msg)
	}

	// A regexp, not a literal: bravo is the only row it matches.
	u.prompt(1)
	type_("br[aeiou]v\r")
	if sel() != "bravo" || u.msg != "" {
		t.Errorf("/br[aeiou]v selected %q (%q), want bravo out of %v", sel(), u.msg, order)
	}
	u.prompt(1)
	type_("br[\r") // …and one that doesn't compile says so
	if !strings.HasPrefix(u.msg, "bad pattern") {
		t.Errorf("an unclosed [ left msg %q, want a bad pattern message", u.msg)
	}

	// ? searches the other way, and n repeats it in that direction: from the
	// middle row, "a" (on every row) runs up the list and wraps.
	u.sel = u.rows[1]
	u.prompt(-1)
	type_("a\r")
	if sel() != order[0] {
		t.Errorf("?a from the middle row selected %q, want %q", sel(), order[0])
	}
	u.seek(u.dir)
	if sel() != order[2] {
		t.Errorf("n after ?a selected %q, want %q — backwards, wrapping around the top", sel(), order[2])
	}
	u.seek(-u.dir)
	if sel() != order[0] {
		t.Errorf("N after ?a selected %q, want %q — the other way", sel(), order[0])
	}

	// A pattern nothing matches leaves the selection where it was and says so.
	was := u.sel
	u.prompt(1)
	type_("zzz\r")
	if u.sel != was || u.msg == "" {
		t.Errorf("/zzz selected %q with msg %q, want %q left alone and a message", sel(), u.msg, filepath.Base(was.path))
	}

	// The bar is the prompt while one is up, ? and all.
	u.prompt(-1)
	type_("al")
	if lines, err = u.frame(); err != nil {
		t.Fatal(err)
	}
	if bar := lines[len(lines)-1]; !strings.Contains(bar, "?al") {
		t.Errorf("status bar is %q, want the ?al prompt on it", bar)
	}

	// Escape abandons the whole search: the pattern it displaced comes back,
	// direction and all, and so does the selection.
	type_("\x1b")
	if u.typing || u.query != "zzz" || u.dir != 1 || u.sel != was {
		t.Errorf("escape left typing %v query %q dir %d sel %q, want the previous search and row back",
			u.typing, u.query, u.dir, sel())
	}
}

// The background fetch has to actually move origin/main, and it has to stop
// when the tui does rather than outliving it. Twice over: nil is the repo the
// tui runs in, and under -g it's the configured projects — from a directory
// that is deliberately not a repo at all.
func TestFetchMain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		global bool
	}{{"cwd", false}, {"global", true}} {
		t.Run(tc.name, func(t *testing.T) {
			origin := initRepo(t)
			clone := t.TempDir()
			git(t, "clone", "--quiet", origin, clone)
			git(t, "-C", origin, "commit", "--allow-empty", "-m", "remote work")

			var dirs []string
			if tc.global {
				dirs = []string{clone}
				t.Chdir(t.TempDir())
			} else {
				t.Chdir(clone)
			}

			want := gitLine(origin, "rev-parse", "HEAD")
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); fetchMain(ctx, time.Hour, dirs) }() // an hour: only the first fetch is under test

			for range 100 {
				if gitLine(clone, "rev-parse", "origin/main") == want {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if got := gitLine(clone, "rev-parse", "origin/main"); got != want {
				t.Errorf("origin/main = %s after the background fetch, want %s", got, want)
			}

			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("fetchMain outlived its context")
			}
		})
	}
}

// The table has to fit the terminal at any width: one column too many and
// every row wraps, which wrecks the alignment and, in the tui, the cursor
// arithmetic. Long names, long branches and long commits all compete for the
// same space, so give it all three.
func TestListFitsTerminalWidth(t *testing.T) {
	initRepo(t)
	for _, name := range []string{"exceedingly-verbose-worktree-name-one", "exceedingly-verbose-worktree-name-two"} {
		path := capture(t, &NewWorktreeBranchCmd{Name: name, Path: true})
		git(t, "-C", path, "commit", "--allow-empty", "-m", "a commit subject that runs on well past any sensible column width (#1234)")
	}

	for _, width := range []int{20, 50, 60, 80, 100, 200} {
		var buf bytes.Buffer
		if _, _, err := renderList(&buf, true, width, nil, nil, true); err != nil {
			t.Fatal(err)
		}
		// Every column bottoms out at minCol, so a terminal narrower than that
		// floor gets the floor rather than an ever-thinner table.
		floor := 3*minCol + len("AGE") + len("CLAUDE") + 2*(len(allColumns())-1)
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if got := len([]rune(line)); got > max(width, floor) {
				t.Errorf("width=%d: line is %d columns wide: %q", width, got, line)
			}
		}
	}
}

// The details pane exists because the table cuts: on a terminal too narrow to
// show a long commit subject in the TOPIC column, `d` on the row has to show
// all of it — wrapped, and still inside the width, since a line that wraps
// itself would push the status bar off the bottom.
func TestDetailPaneShowsWhatTheTableCut(t *testing.T) {
	initRepo(t)
	const subject = "a commit subject that runs on well past any sensible column width (#1234)"
	path := capture(t, &NewWorktreeBranchCmd{Name: "exceedingly-verbose-worktree-name", Path: true})
	git(t, "-C", path, "commit", "--allow-empty", "-m", subject)

	defer func(old func() (int, int)) { termSize = old }(termSize)
	termSize = func() (int, int) { return 60, 12 }

	var u ui
	if _, err := u.frame(); err != nil { // the first frame is what fills in the rows
		t.Fatal(err)
	}
	u.move(1)
	u.detail = u.cells[u.sel] // what `d` does
	if u.detail == nil {
		t.Fatalf("no cells for the selected row %q", u.sel.path)
	}
	lines, err := u.frame()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 12 {
		t.Fatalf("pane is %d lines, want the 12 the terminal has", len(lines))
	}
	joined := strings.Join(lines, "\n")
	// The pane is a window drawn over the list, so its own lines are the ones
	// the border runs down — and a wrapped value has that border between its
	// halves: dropping it is what turns the pane back into the text it wrapped.
	pane := func(lines []string) []string {
		var out []string
		for _, l := range lines {
			if strings.ContainsAny(l, "┌│└") {
				out = append(out, l)
			}
		}
		return out
	}
	text := strings.Join(strings.Fields(strings.ReplaceAll(strings.Join(pane(lines), "\n"), "│", "")), " ")
	for _, want := range []string{filepath.Base(path), "worktree-exceedingly-verbose-worktree-name", subject} {
		if !strings.Contains(text, want) {
			t.Errorf("pane doesn't show %q:\n%s", want, joined)
		}
	}
	for _, line := range pane(lines) {
		if got := len([]rune(line)); got > 60 {
			t.Errorf("pane line is %d columns wide: %q", got, line)
		}
	}

	// On a terminal with room to spare the pane sits in from every edge: the
	// gap is what says it's a look at one row of the list rather than a screen
	// of its own. A cramped terminal spends that space on the values instead,
	// which is the 60-column pane above.
	termSize = func() (int, int) { return 100, 24 }
	if lines, err = u.frame(); err != nil {
		t.Fatal(err)
	}
	top := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "┌") })
	if top < 1 {
		t.Fatalf("pane starts on line %d, want it inset from the top:\n%s", top, strings.Join(lines, "\n"))
	}
	for _, line := range pane(lines) {
		if !strings.HasPrefix(line, "  ") || len([]rune(line)) >= 100 {
			t.Errorf("pane line isn't inset from the sides: %q", line)
		}
	}

	// It's a modal over the list, not a screen of its own: the rows the pane
	// doesn't cover are still the list, and they hold still while it's up —
	// the tick's refresh is what would otherwise move them.
	name := filepath.Base(path)[:20] // the row is the cut-down one — that's the point of the pane
	if !strings.Contains(lines[0], "NAME") || !strings.Contains(lines[1], name) {
		t.Errorf("the list isn't drawn behind the pane:\n%s", strings.Join(lines[:top], "\n"))
	}
	u.stale()
	if u.body == nil {
		t.Error("the list behind the pane was dropped, so the next frame re-reads it")
	}
}

// BRANCH is capped even on a terminal with room to spare, so the space it
// doesn't need goes to TOPIC — which keeps all of it.
func TestFitTableCapsBranch(t *testing.T) {
	cols := allColumns()
	long := topic("", "a commit subject that runs on well past any sensible column width (#1234)")
	row := []string{"a-name", "worktree-exceedingly-verbose-branch-name", "2h", "yes", long}
	fitTable([][]string{row}, 500, cols)
	if got := len([]rune(row[1])); got != cols[1].max {
		t.Errorf("BRANCH is %d wide, want %d: %q", got, cols[1].max, row[1])
	}
	if row[4] != long {
		t.Errorf("TOPIC = %q, want it uncut: %q", row[4], long)
	}
}

// Which columns the table draws is the config's call, in the order it names
// them. A name that isn't a column is a typo, and a typo that silently drops a
// column is one you'd never find, so it's an error instead.
func TestConfigColumns(t *testing.T) {
	initRepo(t)
	capture(t, &NewWorktreeBranchCmd{Name: "one", Path: true})
	writeConfig := func(cfg string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(configPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath(), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeConfig("columns = [\"topic\", \"name\"]\n")
	var buf bytes.Buffer
	if _, _, err := renderList(&buf, false, 0, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	head := strings.Split(out, "\n")[0]
	if i, j := strings.Index(head, "TOPIC"), strings.Index(head, "NAME"); i != 0 || j < i {
		t.Errorf("header = %q, want TOPIC then NAME", head)
	}
	for _, gone := range []string{"BRANCH", "CLAUDE", "worktree-one"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q is in a table that asked for topic and name only:\n%s", gone, out)
		}
	}

	writeConfig("columns = [\"nmae\"]\n")
	if _, _, err := renderList(&buf, false, 0, nil, nil, true); err == nil || !strings.Contains(err.Error(), "nmae") {
		t.Errorf("unknown column: err = %v, want one naming it", err)
	}
}

// TOPIC carries two kinds of line, and each is cut the way it wants to be: a
// commit subject keeps its last word, a session summary just stops.
func TestTopicCut(t *testing.T) {
	for _, tc := range []struct{ s, want string }{
		{topic("", "Fix the windows build broken by tui (#32)"), commitGlyph + " Fix the windows bui… (#32)"},
		{topic("Goal was a widget on the dashboard", ""), sessionGlyph + " Goal was a widget on the …"},
	} {
		if got := topicCut(tc.s, 28); got != tc.want {
			t.Errorf("topicCut(%q) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

// Cutting the middle out of a branch or a commit subject has to leave the last
// word — the part that says which branch, or which PR — attached.
func TestElide(t *testing.T) {
	for _, tc := range []struct {
		s     string
		limit int
		want  string
	}{
		{"worktree-elegant-bouncing-cook", 23, "worktree-elegant-…-cook"},
		{"Fix the windows build broken by tui (#32)", 28, "Fix the windows build… (#32)"},
		{"already short", 20, "already short"},
		// No last word worth keeping: fall back to a plain trailing ellipsis.
		{"antidisestablishmentarianism", 10, "antidises…"},
		{"tui: elaboratelyhyphenated-veryverylongfinalword", 20, "tui: elaboratelyhyp…"},
	} {
		if got := elide(tc.s, tc.limit); got != tc.want {
			t.Errorf("elide(%q, %d) = %q, want %q", tc.s, tc.limit, got, tc.want)
		} else if n := len([]rune(got)); n > tc.limit {
			t.Errorf("elide(%q, %d) is %d characters wide", tc.s, tc.limit, n)
		}
	}
}

// TOPIC's session line reads a real Claude Code transcript, so pin what it
// picks out of one: the last recap wins, and a session that never recapped
// falls back to the first prompt the user typed — never to the noise around
// it (hook attachments, tool results, slash commands), and never to more than
// one line.
func TestSummarizeTranscript(t *testing.T) {
	const (
		attachment = `{"type":"attachment","attachment":{"content":"a hook that talks about away_summary"}}`
		meta       = `{"type":"user","isMeta":true,"message":{"role":"user","content":"Caveat: the messages below…"}}`
		slash      = `{"type":"user","message":{"role":"user","content":"<command-name>/recap</command-name>"}}`
		toolResult = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`
		prompt     = `{"type":"user","message":{"role":"user","content":"Proposal\n\n  add --path to new"}}`
		recap1     = `{"type":"system","subtype":"away_summary","content":"Goal was the first thing. (disable recaps in /config)"}`
		recap2     = `{"type":"system","subtype":"away_summary","content":"Goal was the last thing. (disable recaps in /config)"}`
	)

	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"recap wins", []string{attachment, meta, prompt, recap1, toolResult, recap2}, "Goal was the last thing."},
		{"falls back to the first prompt", []string{attachment, meta, slash, prompt, toolResult}, "Proposal add --path to new"},
		{"nothing to say", []string{attachment, meta, slash, toolResult}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := summarizeTranscript(path); got != tc.want {
				t.Errorf("summarizeTranscript() = %q, want %q", got, tc.want)
			}
		})
	}
}

// -g spans the configured projects: each gets a section of its own, holding its
// worktrees, and the identities handed back to the tui point at the right repo
// — which a name alone couldn't, since two projects can each have a worktree
// called "shared".
func TestGlobalListSpansProjects(t *testing.T) {
	// The section's identity is the repo root git reports, which on a mac isn't
	// spelled the way the config path is (/var vs /private/var) — so take it
	// from the worktree, the same way renderList does.
	var roots, gitRoots, worktrees []string
	for range 2 {
		roots = append(roots, initRepo(t))
		path := capture(t, &NewWorktreeBranchCmd{Name: "shared", Path: true})
		worktrees = append(worktrees, path)
		root, _ := gitutil.ClaudeWorktreeRepoRoot(path)
		gitRoots = append(gitRoots, root)
	}

	var buf bytes.Buffer
	got, _, err := renderList(&buf, false, 0, roots, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// A section header for each project, then that project's worktrees.
	want := []listRow{
		{project: gitRoots[0]}, {project: gitRoots[0], path: worktrees[0]},
		{project: gitRoots[1]}, {project: gitRoots[1], path: worktrees[1]},
	}
	if !slices.Equal(got, want) {
		t.Errorf("renderList rows = %v, want %v", got, want)
	}
	out := buf.String()
	for _, root := range roots {
		if header := "▾ " + filepath.Base(root) + " (1)"; !strings.Contains(out, header) {
			t.Errorf("no %q section in:\n%s", header, out)
		}
	}

	// Folded shut, a project keeps its header — turned around, and still saying
	// how much is underneath — and contributes no rows at all.
	buf.Reset()
	got, _, err = renderList(&buf, false, 0, roots, map[string]bool{gitRoots[0]: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := want[2:]; !slices.Equal(got[1:], want) {
		t.Errorf("with the first section folded, rows = %v, want %v", got, want)
	}
	if header := "▸ " + filepath.Base(roots[0]) + " (1)"; !strings.Contains(buf.String(), header) {
		t.Errorf("no %q section in:\n%s", header, buf.String())
	}

	// The tui reads that same list: the first row is a section, ↵ on it folds
	// the section away, and below it are the worktrees its actions resolve a
	// project out of.
	u := ui{projects: roots}
	if _, err := u.frame(); err != nil {
		t.Fatal(err)
	}
	u.move(1)
	if (u.sel != listRow{project: gitRoots[0]}) {
		t.Fatalf("tui selected %v, want the %s section", u.sel, gitRoots[0])
	}
	u.move(1)
	if !slices.Contains(worktrees, u.sel.path) {
		t.Errorf("tui selected %q, want one of %v", u.sel.path, worktrees)
	}

	u.sel = listRow{project: gitRoots[0]}
	u.toggle()
	if _, err := u.frame(); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(u.rows, func(r listRow) bool { return r.path == worktrees[0] }) {
		t.Errorf("folding the %s section left its worktree on screen: %v", gitRoots[0], u.rows)
	}
}

// -g works from anywhere, a directory that isn't a git repository included: it
// must list the configured projects without git muttering "fatal: not a git
// repository" about the one place it wasn't asked about.
func TestGlobalListOutsideAnyRepo(t *testing.T) {
	root := initRepo(t)
	capture(t, &NewWorktreeBranchCmd{Name: "shared"})
	t.Chdir(t.TempDir())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func(orig *os.File) { os.Stderr = orig }(os.Stderr)
	os.Stderr = w

	// tty: the markers are the only thing that ever looked at the current
	// directory, so the complaint only shows up with them turned on.
	var buf bytes.Buffer
	_, _, err = renderList(&buf, true, 0, []string{root}, nil, true)
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if msg, _ := io.ReadAll(r); len(msg) > 0 {
		t.Errorf("-g complained about the current directory: %s", msg)
	}
	if !strings.Contains(buf.String(), "shared") {
		t.Errorf("no worktree listed:\n%s", buf.String())
	}
}

// Under -g the tui acts on rows belonging to projects it isn't standing in, so
// a removal has to name that project's repo throughout: miss one git command
// and it removes the worktree but strands the branch in the other repo.
func TestRemoveFromAnotherProject(t *testing.T) {
	other := initRepo(t)
	path := capture(t, &NewWorktreeBranchCmd{Name: "elsewhere", Path: true})
	initRepo(t) // now standing in an unrelated repo

	if msg, ok := removeWorktree(path); !ok {
		t.Fatalf("removing another project's worktree: %s", msg)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists (%v)", path, err)
	}
	if gitutil.BranchExists(other, "worktree-elsewhere") {
		t.Error("branch left behind in the other project")
	}
}

// Where -g gets its projects: XDG's config location, and a leading ~ expanded
// the way the shell would. With no config file there is nothing to show, so it
// has to say where to write one rather than print an empty table.
func TestProjectRootsFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	if roots, err := projectRoots(false); err != nil || roots != nil {
		t.Errorf("projectRoots(false) = (%v, %v), want (nil, nil): no -g, no config", roots, err)
	}
	if _, err := projectRoots(true); err == nil || !strings.Contains(err.Error(), configPath()) {
		t.Errorf("projectRoots with no config file: err = %v, want one naming %s", err, configPath())
	}

	dir := filepath.Join(home, ".config", "ccwt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[[projects]]\npath = \"~/src/one\"\n\n[[projects]]\npath = \"/srv/two\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := projectRoots(true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(home, "src", "one"), "/srv/two"}; !slices.Equal(got, want) {
		t.Errorf("projectRoots = %v, want %v", got, want)
	}
}

// `config view` on a machine that has never had a config file: it creates an
// empty one rather than failing, and prints what's there once there is
// something to print.
func TestConfigView(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	if out := capture(t, &ConfigViewCmd{}); out != "" {
		t.Errorf("view with no config = %q, want empty", out)
	}
	if _, err := os.Stat(configPath()); err != nil {
		t.Fatalf("view did not create the config file: %v", err)
	}

	cfg := "[[projects]]\npath = \"~/src/one\"\n"
	if err := os.WriteFile(configPath(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := capture(t, &ConfigViewCmd{}); out != strings.TrimRight(cfg, "\n") {
		t.Errorf("view = %q, want %q", out, cfg)
	}
}

// The zsh completion menu is generated from the command table, so it has to
// survive what the help strings contain: an apostrophe ("the one you're in")
// must come out as zsh string syntax instead of ending the string and leaving
// the rest as shell code, and an alias needs a line of its own so `ccwt ls`
// completes too.
func TestZshCommandMenu(t *testing.T) {
	parser, err := kong.New(&cli, kong.Name("ccwt"), kong.Vars{"version": "test"})
	if err != nil {
		t.Fatal(err)
	}
	menu := zshCommandMenu(parser.Model.Node)
	for _, want := range []string{
		"'cd:cd into an existing worktree",
		"'ls:List Claude Code worktrees",
		`'done:Finish with the worktree you'\''re in`,
	} {
		if !strings.Contains(menu, want) {
			t.Errorf("menu is missing %q:\n%s", want, menu)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(menu, "\n"), "\n") {
		if !strings.HasPrefix(line, "        '") || !strings.HasSuffix(line, "'") {
			t.Errorf("%q is not a quoted _describe entry", line)
		}
	}
}

// A bare `ccwt` is the tui, since that's what you reach for most; its flags
// have to work bare too, and naming a subcommand must still pick that one.
func TestDefaultCommandIsTui(t *testing.T) {
	parser, err := kong.New(&cli, kong.Name("ccwt"), kong.Vars{"version": "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "tui"},
		{[]string{"-g"}, "tui"},
		{[]string{"list"}, "list"},
	} {
		ctx, err := parser.Parse(tc.args)
		if err != nil {
			t.Errorf("parse %q: %v", tc.args, err)
			continue
		}
		if got := ctx.Command(); got != tc.want {
			t.Errorf("ccwt %q ran %q, want %q", tc.args, got, tc.want)
		}
	}
}

// renderRows draws the list the way the tui does and hands back its rows along
// with the table they were drawn as.
func renderRows(t *testing.T) ([]listRow, string) {
	t.Helper()
	var buf bytes.Buffer
	rows, _, err := renderList(&buf, false, 0, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return rows, buf.String()
}

// typeInto feeds a prompt to the open entry box one keystroke at a time, then
// presses enter — what the tui does with the keys it reads.
func typeInto(u *ui, prompt string) {
	for _, r := range prompt {
		u.queue(string(r))
	}
	u.queue("\r")
}

// A queued prompt is work waiting on something else to finish, so it is drawn
// under the row it waits on, and a prompt queued behind it under that in turn.
// It outlives what it was waiting for: removing the worktree promotes what was
// waiting on it to a "<new>" row of its own — its turn has come — since
// deleting the work is not the same as cancelling what was queued behind it.
func TestQueuedPromptsHangOffTheirWorktree(t *testing.T) {
	initRepo(t)
	capture(t, &NewWorktreeBranchCmd{Name: "alpha", Path: true})

	render := func() ([]listRow, string) { return renderRows(t) }
	// Queued the way the tui queues it — `n`, the prompt, enter — so that the
	// keystrokes are covered along with the storage.
	var u ui
	queue := func(parent listRow, prompt string) {
		t.Helper()
		u.entry = newEntry(parent, "", 0)
		typeInto(&u, prompt)
		if u.msg != "queued" {
			t.Fatalf("queueing %q: %s", prompt, u.msg)
		}
	}

	rows, _ := render()
	if len(rows) != 1 || !rows[0].worktree() {
		t.Fatalf("rows before anything was queued = %v, want the one worktree", rows)
	}
	queue(rows[0], "then update the docs")

	rows, _ = render()
	if len(rows) != 2 || rows[1].task <= 0 {
		t.Fatalf("rows = %v, want the worktree and a queued prompt under it", rows)
	}
	queue(rows[1], "and then cut a release")

	rows, body := render()
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(rows) != 3 || len(lines) != 3 {
		t.Fatalf("rows = %v, lines = %q, want the worktree and two queued prompts", rows, lines)
	}
	if !strings.Contains(lines[1], "then update the docs") || !strings.Contains(lines[2], "and then cut a release") {
		t.Errorf("the prompts aren't on their rows:\n%s", body)
	}
	// Neither has a worktree of its own, and the NAME column says so rather than
	// standing empty. The worktree they hang off keeps its own name.
	if !strings.Contains(lines[1], queuedName) || !strings.Contains(lines[2], queuedName) {
		t.Errorf("a queued prompt doesn't say %s in NAME:\n%s", queuedName, body)
	}
	if strings.Contains(lines[0], queuedName) {
		t.Errorf("the worktree row says %s:\n%s", queuedName, body)
	}
	// The second waits on the first, so it's drawn a level further in.
	if in, deeper := strings.Index(lines[1], taskGlyph), strings.Index(lines[2], taskGlyph); deeper <= in {
		t.Errorf("the chained prompt is indented %d, want more than the %d of the one it waits on:\n%s", deeper, in, body)
	}

	// rows[0].project rather than the repo path initRepo handed back: on macOS
	// that one is /var/..., and git — which is what the removal talks to —
	// reports /private/var/....
	if err := (&RemoveCmd{Name: "alpha", Force: true}).remove(rows[0].project); err != nil {
		t.Fatal(err)
	}
	rows, body = render()
	if len(rows) != 2 || !rows[0].pending() {
		t.Fatalf("rows after the worktree went = %v, want a <new> row and what waits on it", rows)
	}
	lines = strings.Split(strings.TrimRight(body, "\n"), "\n")
	if !strings.Contains(lines[0], newName) || !strings.Contains(lines[0], "then update the docs") {
		t.Errorf("the promoted prompt isn't a %s row:\n%s", newName, body)
	}
	if !strings.Contains(lines[1], "and then cut a release") {
		t.Errorf("what was queued behind it didn't come along:\n%s", body)
	}

	// `r` on it drops the chain — the one behind it goes with the one it was
	// waiting on.
	if msg, ok := dropTask(rows[0].task); !ok {
		t.Fatalf("dropping the chain: %s", msg)
	}
	if rows, body = render(); len(rows) != 0 {
		t.Errorf("rows after deleting the chain = %v, want none:\n%s", rows, body)
	}

	// Off the list, still in the table: dropping marks the whole chain deleted
	// rather than removing it, so a mistaken `r` can be undone with an UPDATE.
	db, err := openTasks()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var live, dropped int
	if err := db.QueryRow(`SELECT COUNT(*) FILTER (WHERE deleted = 0), COUNT(*) FILTER (WHERE deleted > 0) FROM task`).Scan(&live, &dropped); err != nil {
		t.Fatal(err)
	}
	if live != 0 || dropped != 2 {
		t.Errorf("%d prompts still queued and %d marked deleted, want 0 and both of them", live, dropped)
	}
}

// Opening a "<new>" row is what finally makes its worktree: a fresh one, a
// workspace on it, the prompt running there, and the rest of the chain now
// waiting on that worktree rather than on a prompt that has started.
func TestStartingAPendingPromptMakesItsWorktree(t *testing.T) {
	initRepo(t)
	capture(t, &NewWorktreeBranchCmd{Name: "alpha", Path: true})

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	// A herdr that logs what it was asked and, for `pane list`, answers with a
	// pane sitting in the worktree the `worktree open` before it named — which
	// is the pane the prompt is meant to run in.
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]q
case "$1 $2" in
"pane list")
  p=$(sed -n 's/.*--path \([^ ]*\).*/\1/p' %[1]q | tail -1)
  printf '{"result":{"panes":[{"pane_id":"w1:p1","cwd":"%%s"}]}}' "$p" ;;
"worktree list") printf '{"result":{"worktrees":[]}}' ;;
"agent list")    printf '{"result":{"agents":[]}}' ;;
esac
`, log)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)

	var u ui
	rows, _ := renderRows(t)
	u.entry = newEntry(rows[0], "", 0)
	// An apostrophe in the prompt, since what reaches the pane is a shell line:
	// the whole prompt has to arrive as one argument, quotes and all.
	typeInto(&u, "then update Bob's docs")
	rows, _ = renderRows(t)
	u.entry = newEntry(rows[1], "", 0)
	typeInto(&u, "and then cut a release")

	if err := (&RemoveCmd{Name: "alpha", Force: true}).remove(rows[0].project); err != nil {
		t.Fatal(err)
	}
	// What the tui has when a key is pressed: the rows of the last frame and
	// their cells, which is where startPending reads the prompt from.
	rows, cells, err := renderList(io.Discard, false, 0, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	u = ui{sel: rows[0], cells: cells}

	msg := u.startPending()
	name, ok := strings.CutPrefix(msg, "started ")
	if !ok {
		t.Fatalf("startPending: %s", msg)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), `pane run w1:p1 claude 'then update Bob'\''s docs'`) {
		t.Errorf("herdr calls = %q, want the whole prompt quoted as one argument", calls)
	}

	rows, body := renderRows(t)
	if len(rows) != 2 || !rows[0].worktree() || rows[1].task <= 0 {
		t.Fatalf("rows after starting = %v, want the new worktree and the rest of the chain under it", rows)
	}
	if !strings.Contains(body, name) || strings.Contains(body, newName) {
		t.Errorf("the %s row didn't become the worktree %s:\n%s", newName, name, body)
	}
	if !strings.Contains(body, "and then cut a release") || strings.Contains(body, "Bob's docs") {
		t.Errorf("the started prompt should be gone and the one behind it kept:\n%s", body)
	}
}

// `e` in the details pane rewrites a queued prompt where it stands: the text
// changes and nothing else does — what was waiting on it is still waiting on
// it, and it hasn't become a new prompt at the end of the chain.
func TestEditingAQueuedPromptLeavesItWhereItIs(t *testing.T) {
	initRepo(t)
	capture(t, &NewWorktreeBranchCmd{Name: "alpha", Path: true})

	var u ui
	rows, _ := renderRows(t)
	u.entry = newEntry(rows[0], "", 0)
	typeInto(&u, "port the widget")

	rows, _ = renderRows(t)
	first := rows[1]
	u.entry = newEntry(first, "", 0)
	typeInto(&u, "then write the docs")

	// What `e` opens: the prompt as the details pane shows it, and the id of the
	// row it came from. The word that wants changing is the last one here, but
	// it's edited the way one in the middle would be — caret back a word, the
	// new noun typed in front of the old, the old one deleted from under the
	// caret — since that's the whole point of the box being an editor.
	u.entry = newEntry(first, "port the widget", first.task)
	for _, k := range splitKeys("\x1b[1;5Dsidebar") {
		u.queue(k)
	}
	for range len("widget") {
		u.queue("\x1b[3~")
	}
	u.queue("\r")
	if u.msg != "saved" {
		t.Fatalf("editing: %s", u.msg)
	}

	rows, body := renderRows(t)
	if len(rows) != 3 || rows[1] != first {
		t.Fatalf("rows after the edit = %v, want the same three with %v still second", rows, first)
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if !strings.Contains(lines[1], "port the sidebar") || strings.Contains(body, "port the widget") {
		t.Errorf("the edit didn't land:\n%s", body)
	}
	if !strings.Contains(lines[2], "then write the docs") {
		t.Errorf("the prompt waiting on it moved or lost its text:\n%s", body)
	}
}

// The box a prompt is typed into is a line editor: the caret moves, text goes
// in where it is, and each delete takes the rune on its own side of it. The
// keys arrive the way the tty delivers them — an escape sequence for the
// arrows, a byte for everything else, a byte at a time for a multi-byte rune.
func TestLineEditor(t *testing.T) {
	for _, tc := range []struct {
		start, keys, want string
		cur               int
	}{
		// Typing and backspace at the end: what it did before there was a caret.
		{start: "port the", keys: " widget", want: "port the widget", cur: 15},
		{start: "port the widget", keys: "\x7f\x7f", want: "port the widg", cur: 13},
		// The caret walks runes rather than bytes, so a delete either side of a
		// multi-byte one takes all of it.
		{start: "a é b", keys: "\x1b[D\x1b[D\x7f", want: "a  b", cur: 2},
		{start: "a é b", keys: "\x01\x1b[C\x1b[C\x1b[3~", want: "a  b", cur: 2},
		// Text goes in at the caret, wherever it's been moved to.
		{start: "port the widget", keys: "\x1b[1;5Dnew ", want: "port the new widget", cur: 13},
		{start: "port the widget", keys: "\x01\x1b[1;5C\x1b[1;5Cx", want: "port thex widget", cur: 9},
		{start: "abc", keys: "\x1b[Hx", want: "xabc", cur: 1},
		{start: "abc", keys: "\x01x\x05y", want: "xabcy", cur: 5},
		// Ctrl-W, Ctrl-U, Ctrl-K: the word before the caret, everything before
		// it, everything after it.
		{start: "port the widget", keys: "\x17", want: "port the ", cur: 9},
		{start: "port the widget", keys: "\x1b[1;5D\x15", want: "widget", cur: 0},
		{start: "port the widget", keys: "\x1b[1;5D\x0b", want: "port the ", cur: 9},
		// A multi-byte rune is two keystrokes, and both of its bytes are text.
		{start: "caf", keys: "é", want: "café", cur: 5},
		// Sequences it doesn't know are ignored rather than typed — a mouse
		// report is one — and the ends of the line are the ends of it.
		{start: "abc", keys: "\x1b[<0;12;5M", want: "abc", cur: 3},
		{start: "abc", keys: "\x1b[C\x1b[3~", want: "abc", cur: 3},
		{start: "abc", keys: "\x1b[H\x1b[D\x7f", want: "abc", cur: 0},
	} {
		got, cur := tc.start, len(tc.start)
		for _, k := range splitKeys(tc.keys) {
			got, cur = lineEdit(got, cur, k)
		}
		if got != tc.want || cur != tc.cur {
			t.Errorf("%q + %q = %q with the caret at %d, want %q at %d", tc.start, tc.keys, got, cur, tc.want, tc.cur)
		}
	}
}

// The box scrolls to wherever the caret is rather than to the end of the text:
// on a prompt taller than the box, rewriting the first sentence is exactly what
// the editing keys are for.
func TestEntryPaneFollowsTheCaret(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("word ", 60)) // taller than the box
	for _, cur := range []int{0, len(text) / 2, len(text)} {
		pane := entryPane(text, cur, "edit", 40, 10)
		at := slices.IndexFunc(pane, func(l string) bool { return strings.Contains(l, caretOn) })
		if at < 0 {
			t.Fatalf("the caret at %d isn't in the box at all:\n%s", cur, strings.Join(pane, "\n"))
		}
		// And what's drawn from the caret on is the text from the caret on: it
		// sits on the character it's in front of, not next to it.
		after := strings.TrimRight(caretOff.Replace(pane[at][strings.Index(pane[at], caretOn)+len(caretOn):]), " │")
		if !strings.HasPrefix(text[cur:], after) {
			t.Errorf("the caret at %d has %q drawn from it on, want the start of %q", cur, after, text[cur:])
		}
	}
}

// How the caret is drawn: reverse video on the character it's in front of.
var (
	caretOn  = "\x1b[7m"
	caretOff = strings.NewReplacer(caretOn, "", "\x1b[0m", "")
)

// Moving the caret moves the caret and nothing else. It's drawn on a character
// rather than between two of them because a block of its own would take a
// column, shunting the rest of the line along — and re-wrapping it — with every
// press of an arrow key, which is exactly the shimmer a caret shouldn't have.
func TestTheCaretDoesNotShiftTheText(t *testing.T) {
	const text = "port the widget to the mobile layout, and mind  the  double  spaces"
	var want []string
	for cur := range len(text) + 1 {
		var plain []string
		for _, l := range entryPane(text, cur, "edit", 40, 12) { // the whole text fits
			plain = append(plain, caretOff.Replace(l))
		}
		if want == nil {
			want = plain
		}
		if !slices.Equal(plain, want) {
			t.Fatalf("the caret at %d redrew the text:\n%s\nwant\n%s", cur, strings.Join(plain, "\n"), strings.Join(want, "\n"))
		}

		// And it's on the character it's in front of — on a space of its own
		// past the end of the text.
		joined := strings.Join(entryPane(text, cur, "edit", 40, 12), "\n")
		i := strings.Index(joined, caretOn)
		if i < 0 {
			t.Fatalf("the caret at %d isn't drawn at all:\n%s", cur, joined)
		}
		got, _ := utf8.DecodeRuneInString(joined[i+len(caretOn):])
		wantRune := ' '
		if cur < len(text) {
			wantRune, _ = utf8.DecodeRuneInString(text[cur:])
		}
		if got != wantRune {
			t.Errorf("the caret at %d is on %q, want %q", cur, got, wantRune)
		}
	}
}

// The queue is shared by every ccwt there is — a project's own tui, the `-g`
// one, the next one you open in another tab — so two of them writing at once
// have to both get their prompt in, rather than one coming back with SQLite's
// "database is locked".
func TestQueueTakesConcurrentWriters(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wt := listRow{project: "/repo", path: "/repo/.claude/worktrees/alpha"}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() { // its own connection, as a separate process would have
			if err := addTask(wt, fmt.Sprintf("prompt %d", i)); err != nil {
				t.Errorf("queueing prompt %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	tasks, err := loadTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 8 {
		t.Errorf("%d prompts queued, want all 8: %v", len(tasks), tasks)
	}
}

// A cached lookup must skip the work while the stamp holds and redo it the
// moment the stamp moves — the whole point being that `git status` and the
// transcript reads behind a row stop running on every 2-second refresh.
func TestCachedRecomputesOnlyWhenTheStampMoves(t *testing.T) {
	var c cached[int]
	var calls int
	count := func() int { calls++; return calls }

	if got := c.get("a", "v1", count); got != 1 || calls != 1 {
		t.Fatalf("first get: %d after %d calls, want 1 after 1", got, calls)
	}
	if got := c.get("a", "v1", count); got != 1 || calls != 1 {
		t.Errorf("same stamp: %d after %d calls, want the cached 1 with no new call", got, calls)
	}
	if got := c.get("b", "v1", count); got != 2 || calls != 2 {
		t.Errorf("other key: %d after %d calls, want a fresh 2", got, calls)
	}
	if got := c.get("a", "v2", count); got != 3 || calls != 3 {
		t.Errorf("moved stamp: %d after %d calls, want a fresh 3", got, calls)
	}

	// Rows are built concurrently, so the map behind all this is too.
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { c.get("c", "v1", func() int { return 7 }) })
	}
	wg.Wait()
	if got := c.get("c", "v1", func() int { return 0 }); got != 7 {
		t.Errorf("concurrent gets left %d behind, want 7", got)
	}
}

func TestWindowNamesOneStretchPerDuration(t *testing.T) {
	// Everything asked inside one stretch has to agree, or a refresh would
	// re-run half the scans it was meant to skip.
	if a, b := window(time.Hour), window(time.Hour); a != b {
		t.Errorf("two reads of the same hour gave %q and %q", a, b)
	}
	before := window(time.Microsecond)
	time.Sleep(time.Millisecond)
	if after := window(time.Microsecond); after == before {
		t.Errorf("a microsecond window still reads %q a millisecond later", after)
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
