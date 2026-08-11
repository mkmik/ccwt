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
	"testing"
	"time"

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
// and a dirty worktree's branch, where the "*" also has to beat the "✓" the
// branch would otherwise get for being merged. Each must appear on its row
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
		case line == "" || strings.HasPrefix(line, "  NAME"):
		case strings.HasPrefix(line, "* "+here+" "):
			if !strings.Contains(line, "* worktree-") {
				t.Errorf("dirty worktree missing branch marker: %q", line)
			}
			if strings.Contains(line, "✓") {
				t.Errorf("dirty worktree marked merged: %q", line)
			}
		case strings.HasPrefix(line, "  "+other+" "):
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

// initRepo makes a git repo with one commit on main and chdirs into it.
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // ignore the user's own config (branch_prefix)
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
		for _, sel := range []listRow{{}, {path: "some-worktree"}, {project: "some-project"}} {
			bar := statusBar(40, msg, sel, "main  in sync")
			bar = strings.TrimSuffix(strings.TrimPrefix(bar, "\x1b[7m"), "\x1b[0m")
			if got := len([]rune(bar)); got != 40 {
				t.Errorf("statusBar(40, %.10q, %v) is %d cols wide, want 40", msg, sel, got)
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
		bar := statusBar(200, "", listRow{path: "some-worktree"}, "")
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
	if !strings.HasPrefix(lines[1], "\x1b[7m") {
		t.Errorf("selected row %q is not highlighted", lines[1])
	}
	if strings.HasPrefix(lines[2], "\x1b[7m") {
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
	if !strings.HasPrefix(lines[2], "\x1b[7m") {
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
	if !strings.Contains(row, "\x1b[0mvo\x1b[7m") {
		t.Errorf("selected row is %q, want the match punched out of the reverse video", row)
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
		if _, err := renderList(&buf, true, width, nil, nil); err != nil {
			t.Fatal(err)
		}
		// Every column bottoms out at minCol, so a terminal narrower than that
		// floor gets the floor rather than an ever-thinner table.
		floor := 3*minCol + len("AGE") + len("CLAUDE") + 2*(len(listColumns())-1)
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if got := len([]rune(line)); got > max(width, floor) {
				t.Errorf("width=%d: line is %d columns wide: %q", width, got, line)
			}
		}
	}
}

// BRANCH is capped even on a terminal with room to spare, so the space it
// doesn't need goes to TOPIC — which keeps all of it.
func TestFitTableCapsBranch(t *testing.T) {
	cols := listColumns()
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
	got, err := renderList(&buf, false, 0, roots, nil)
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
	got, err = renderList(&buf, false, 0, roots, map[string]bool{gitRoots[0]: true})
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
	_, err = renderList(&buf, true, 0, []string{root}, nil)
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
