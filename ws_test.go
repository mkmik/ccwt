package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mkmik/ccwt/internal/gitutil"
)

// `ccwt ws` is the tui of a herdr workspace: its list is the workspace's tabs
// in herdr's order, each joined with the first pane in it — where it sits and
// what its terminal calls itself — and a `*` leads the tab the tui is itself
// in. Herdr's order is the tab bar's, so a tab dragged in front of an older one
// comes first here too, whatever number it was born with. A tab is a row of its
// own, not one of the list's worktrees whatever directory it carries: the keys
// that unmake things must not take it for one — what `r` does with a tab is its
// own thing, see below.
func TestWsTableListsTheWorkspaceTabs(t *testing.T) {
	dir := t.TempDir()
	herdr := filepath.Join(dir, "herdr")
	script := `#!/bin/sh
case "$1 $2" in
"tab list") printf '%s' '{"result":{"tabs":[{"tab_id":"w1:t3","label":"calm-baking-otter","number":3,"agent_status":"idle"},{"tab_id":"w1:t1","label":"1","number":1,"agent_status":"unknown"}]}}' ;;
"pane list") printf '%s' '{"result":{"panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","cwd":"/src/ccwt","terminal_title_stripped":"zsh"},{"pane_id":"w1:p3","tab_id":"w1:t3","cwd":"/src/ccwt/.claude/worktrees/calm-baking-otter","terminal_title_stripped":"Waiting on your review"},{"pane_id":"w1:p4","tab_id":"w1:t3","cwd":"/elsewhere","terminal_title_stripped":"a split"}]}}' ;;
esac
`
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	t.Setenv("HERDR_TAB_ID", "w1:t1")

	lines, rows := wsTable(0)
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "  TAB") {
		t.Fatalf("wsTable = %q, want a header and two tabs", lines)
	}
	for i, want := range []struct{ prefix, has, hasNot string }{
		{"  calm-baking-otter ", "Waiting on your review", "a split"}, // moved in front of ours: the first pane speaks for the tab
		{"* 1 ", "ccwt", "unknown"},                                   // ours, a bare shell: no agent to speak of
	} {
		l := lines[i+1]
		if !strings.HasPrefix(l, want.prefix) || !strings.Contains(l, want.has) || strings.Contains(l, want.hasNot) {
			t.Errorf("line %d = %q, want %q… with %q and without %q", i+1, l, want.prefix, want.has, want.hasNot)
		}
	}
	if !strings.Contains(lines[1], "○") || !strings.Contains(lines[2], "·") {
		t.Errorf("lines = %q, want an idle dot and a no-agent dot on them", lines[1:])
	}
	// The dot is a stand-in until the frame paints it, and the colour has to
	// hand the row back to whatever it was sitting on — the selection band here.
	if got := paintDots("  a  ◐  b", rowBar); !strings.Contains(got, "●") || !strings.HasSuffix(got, rowBar+"  b") {
		t.Errorf("paintDots = %q, want a coloured circle that restores the band", got)
	}
	want := []listRow{{path: "/src/ccwt/.claude/worktrees/calm-baking-otter", tab: "w1:t3"}, {path: "/src/ccwt", tab: "w1:t1"}}
	if !slices.Equal(rows, want) {
		t.Errorf("rows = %v, want %v", rows, want)
	}
	if rows[0].worktree() || rows[0].section() || rows[0].pending() || rows[0].process() {
		t.Errorf("a tab row %v reads as something else", rows[0])
	}

	bar := keyBar(200, "", "", wsActions(rows[0], false))
	for _, key := range []string{"n:agent", "space:go", "g:git"} {
		if !strings.Contains(bar, key) {
			t.Errorf("bar on a tab = %q, want %s on it", bar, key)
		}
	}
	if bar := keyBar(200, "", "", wsActions(rows[1], true)); !strings.Contains(bar, "n:next") || strings.Contains(bar, "n:agent") {
		t.Errorf("bar with a pattern in force = %q, want n:next and no n:agent", bar)
	}
	if bar := keyBar(200, "", "", wsActions(listRow{}, false)); strings.Contains(bar, "space:go") {
		t.Errorf("bar with nothing selected = %q, want nothing to go to", bar)
	}
}

// `r` in the ws view is `ccwt done` for the workspace itself: whether it is
// there at all is a property of the directory the tui was started in — the
// workspace's worktree — and not of any tab, so it stays on the bar, the same,
// whichever row the selection happens to be on and with no row selected at all.
// It is offered exactly when `ccwt done` would go through without -D, and
// pressing it removes that worktree, branch included, and closes the workspace
// the tui is sitting in.
func TestWsRemovesTheWorkspaceWhenItIsDoneWith(t *testing.T) {
	initRepo(t)
	// git's own idea of the root: on a mac the temp dir is reached through a
	// symlink, and herdr's answers have to name the path ccwt will ask about.
	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		t.Fatal(err)
	}
	done := capture(t, &NewWorktreeBranchCmd{Name: "done", Path: true})
	dirty := capture(t, &NewWorktreeBranchCmd{Name: "dirty", Path: true})
	if err := os.WriteFile(filepath.Join(dirty, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	// w1 is the workspace the tui is in, open on the "done" worktree; w8 is the
	// repo's own, which is where the focus goes before ours closes under us.
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]q
case "$1 $2" in
"tab list") printf '%%s' '{"result":{"tabs":[{"tab_id":"w1:t1","label":"1","number":1},{"tab_id":"w1:t2","label":"agent","number":2,"agent_status":"idle"}]}}' ;;
"pane list") printf '%%s' '{"result":{"panes":[{"tab_id":"w1:t1","cwd":%[2]q},{"tab_id":"w1:t2","cwd":%[3]q}]}}' ;;
"agent list") printf '%%s' '{"result":{"agents":[]}}' ;;
"worktree list") printf '%%s' '{"result":{"worktrees":[{"path":%[4]q,"open_workspace_id":"w8"},{"path":%[2]q,"open_workspace_id":"w1"}]}}' ;;
"workspace get") printf '%%s' '{"result":{"workspace":{"focused":true}}}' ;;
esac
`, log, done, dirty, root)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	t.Setenv("HERDR_TAB_ID", "w1:t1")

	// A tui in the repo root: nothing to be done with, whatever its tabs are.
	if wsRemovable() {
		t.Error("the repo itself reads as removable")
	}
	t.Chdir(dirty)
	if wsRemovable() {
		t.Error("a workspace with uncommitted work in it reads as done with")
	}

	t.Chdir(done)
	_, rows := wsTable(0)
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want the two tabs", rows)
	}
	// The whole point: the same bar on every row, and on none.
	for _, sel := range []listRow{{}, rows[0], rows[1]} {
		if bar := keyBar(200, "", "", wsActions(sel, false)); !strings.Contains(bar, "r:remove") {
			t.Errorf("bar with %v selected = %q, want r:remove on it", sel, bar)
		}
	}

	if msg := wsDone(); msg != "removed done" {
		t.Fatalf("wsDone: %q", msg)
	}
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Errorf("worktree %s is still there (%v)", done, err)
	}
	if gitutil.BranchExists(root, "worktree-done") {
		t.Error("the branch of the removed worktree is still there")
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "workspace close w1") {
		t.Errorf("herdr calls = %q, want our own workspace closed behind the removal", calls)
	}
	if !strings.Contains(string(calls), "workspace focus w8") {
		t.Errorf("herdr calls = %q, want the repo's workspace focused before ours closes", calls)
	}
}

// The agents to watch out for in the ws view are the workspace's own: one per
// tab, all of them ours. Only the tab this tui is in is exempt, so that an
// agent running `ccwt done` can still clean up after itself.
func TestHerdrBusySeesTheOtherTabsOfOurWorkspace(t *testing.T) {
	dir := t.TempDir()
	herdr := filepath.Join(dir, "herdr")
	script := `#!/bin/sh
case "$1 $2" in
"agent list") printf '%s' '{"result":{"agents":[{"agent_status":"working","cwd":"/src/mine","tab_id":"w1:t1","workspace_id":"w1"},{"agent_status":"working","cwd":"/src/sibling","tab_id":"w1:t2","workspace_id":"w1"}]}}' ;;
esac
`
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	t.Setenv("HERDR_TAB_ID", "w1:t1")

	busy := herdrBusy()
	if busy["/src/mine"] {
		t.Error("our own tab counts as busy: an agent could not run `ccwt done` on its own worktree")
	}
	if !busy["/src/sibling"] {
		t.Error("an agent in another tab of our workspace is invisible: `r` would remove the worktree out from under it")
	}
	// No tab to go by — an older herdr, a pane outside one — and the whole
	// workspace is ours again, which is what this used to do for everyone.
	t.Setenv("HERDR_TAB_ID", "")
	if busy := herdrBusy(); busy["/src/sibling"] {
		t.Error("with no tab in the environment, our own workspace is no longer exempt")
	}
}

// The seed prompt starts an agent in a tab of its own: a tab of this workspace
// in the worktree the tui is standing in — no worktree of its own, and no label
// on the tab — with the configured cli run in the pane the create answered
// with, the whole prompt as one argument. The box closes only once that has
// happened — a herdr that refuses leaves the prompt where it was typed, to try
// again from.
func TestWsSeedStartsAnAgentInANewTab(t *testing.T) {
	initRepo(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	refuse := filepath.Join(dir, "refuse")
	herdr := filepath.Join(dir, "herdr")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]q
case "$1 $2" in
"tab create")
  [ -e %[2]q ] && { echo "no more tabs"; exit 1; }
  printf '%%s' '{"result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}' ;;
esac
`, log, refuse)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")

	u := ui{ws: true, entry: newEntry(listRow{}, "fix Bob's bug", 0)}
	if msg := u.startSeed(); msg != "started" {
		t.Fatalf("startSeed: %s", msg)
	}
	if u.entry.open {
		t.Error("the box is still up after the agent started")
	}
	if entries, _ := os.ReadDir(filepath.Join(".claude", "worktrees")); len(entries) != 0 {
		t.Errorf("worktrees %v were made; the seed runs in the workspace's own", entries)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if want := "tab create --workspace w1 --cwd " + cwd + " --no-focus"; !strings.Contains(string(calls), want) {
		t.Errorf("herdr calls = %q, want %q — a tab of w1 here, unlabelled", calls, want)
	}
	if !strings.Contains(string(calls), `pane run w1:p2 claude 'fix Bob'\''s bug'`) {
		t.Errorf("herdr calls = %q, want the cli run in the pane the create named, the prompt quoted as one argument", calls)
	}

	if err := os.WriteFile(refuse, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u.entry = newEntry(listRow{}, "and then the docs", 0)
	if msg := u.startSeed(); msg != "start failed: herdr tab create: no more tabs" || !u.entry.open || u.entry.text != "and then the docs" {
		t.Errorf("after a refusal: %q, box open %v, text %q; want herdr's reason and the prompt kept", msg, u.entry.open, u.entry.text)
	}
}

// Ctrl-O over the seed prompt names the model the agent is to run on, and the
// name goes to the harness as --model. The box opens on the name already in
// force, so escaping out of a look at it must leave that name alone, and an
// empty box accepted takes the flag back off.
func TestWsSeedRunsOnTheModelTheBoxNames(t *testing.T) {
	initRepo(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]q
case "$1 $2" in
"tab create") printf '%%s' '{"result":{"root_pane":{"pane_id":"w1:p2"}}}' ;;
esac
`, log)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")

	u := ui{ws: true, entry: newEntry(listRow{}, "fix Bob's bug", 0)}
	u.model = newEntry(listRow{}, u.modelName, 0)
	for k := range strings.SplitSeq("claude-opus-5", "") {
		u.askModel(k)
	}
	u.askModel("\r")
	if u.modelName != "claude-opus-5" || u.model.open {
		t.Fatalf("after ↵: model %q, box open %v; want the typed name and the box shut", u.modelName, u.model.open)
	}
	if title := u.entryTitle(); !strings.Contains(title, "claude-opus-5") {
		t.Errorf("the prompt's title = %q, want the model in force on it", title)
	}

	// A look at it, backed out of: the name stands.
	u.model = newEntry(listRow{}, u.modelName, 0)
	u.askModel("\x7f")
	u.askModel("\x1b")
	if u.modelName != "claude-opus-5" {
		t.Errorf("after esc: model %q, want the one that was already in force", u.modelName)
	}

	if msg := u.startSeed(); msg != "started" {
		t.Fatalf("startSeed: %s", msg)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := `pane run w1:p2 claude --model 'claude-opus-5' 'fix Bob'\''s bug'`; !strings.Contains(string(calls), want) {
		t.Errorf("herdr calls = %q, want %q — the model as a flag, the prompt still the last word", calls, want)
	}

	// Emptied and accepted: back to whatever the harness runs by default.
	u.model = newEntry(listRow{}, u.modelName, 0)
	u.askModel("\x15") // ctrl-u
	u.askModel("\r")
	u.entry = newEntry(listRow{}, "and the docs", 0)
	if msg := u.startSeed(); msg != "started" {
		t.Fatalf("startSeed: %s", msg)
	}
	calls, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "pane run w1:p2 claude 'and the docs'") {
		t.Errorf("herdr calls = %q, want no --model once the box was emptied", calls)
	}
}
