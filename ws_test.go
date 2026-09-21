package main

import (
	"errors"
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
"agent list") printf '%s' '{"result":{"agents":[]}}' ;;
esac
`
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	t.Setenv("HERDR_TAB_ID", "w1:t1")

	noMR(t)

	lines, rows := wsTable(0)
	// The tabs are the rows; everything above them — the merge request section
	// and the blank line dividing the two — is what the frame pins (u.head).
	head := len(lines) - len(rows)
	if head != 3 || lines[1] != "" || !strings.HasPrefix(lines[2], "  TAB") {
		t.Fatalf("wsTable = %q, want the merge request, a blank line and a header above two tabs", lines)
	}
	for i, want := range []struct{ prefix, has, hasNot string }{
		{"  calm-baking-otter ", "Waiting on your review", "a split"}, // moved in front of ours: the first pane speaks for the tab
		{"* 1 ", "ccwt", "unknown"},                                   // ours, a bare shell: no agent to speak of
	} {
		l := lines[i+head]
		if !strings.HasPrefix(l, want.prefix) || !strings.Contains(l, want.has) || strings.Contains(l, want.hasNot) {
			t.Errorf("line %d = %q, want %q… with %q and without %q", i+head, l, want.prefix, want.has, want.hasNot)
		}
	}
	if !strings.Contains(lines[head], "○") || !strings.Contains(lines[head+1], "·") {
		t.Errorf("lines = %q, want an idle dot and a no-agent dot on them", lines[head:])
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

// noMR pins the ws view's first section to "there isn't one yet", so that a
// test about the tabs underneath doesn't send a real glab out to a real gitlab
// for the section above them.
func noMR(t *testing.T) {
	t.Helper()
	wsMRLook.Lock()
	defer wsMRLook.Unlock()
	wsMRLook.stamp, wsMRLook.look, wsMRLook.running = window(mrWindow), &mrLook{}, false
	t.Cleanup(func() {
		wsMRLook.Lock()
		defer wsMRLook.Unlock()
		wsMRLook.stamp, wsMRLook.look = "", nil
	})
}

// A tab's dot is its agents' status rather than the tab's own: herdr keeps one
// per agent, and a tab is only where they happen to sit. What the one row has
// to say about a split full of them is the one that most wants you — the
// approval waiting on an answer ahead of the agent still working alongside it.
// A tab with no agent in it has nothing but herdr's word for the tab, which is
// what keeps a bare shell a bare shell.
func TestWsTabDotIsTheAgentThatMostWantsYou(t *testing.T) {
	dir := t.TempDir()
	herdr := filepath.Join(dir, "herdr")
	script := `#!/bin/sh
case "$1 $2" in
"tab list") printf '%s' '{"result":{"tabs":[{"tab_id":"w1:t1","label":"1","agent_status":"unknown"},{"tab_id":"w1:t2","label":"2","agent_status":"working"}]}}' ;;
"pane list") printf '%s' '{"result":{"panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","cwd":"/src/ccwt","terminal_title_stripped":"zsh"},{"pane_id":"w1:p2","tab_id":"w1:t2","cwd":"/src/ccwt","terminal_title_stripped":"Writing the tests"},{"pane_id":"w1:p3","tab_id":"w1:t2","cwd":"/src/ccwt","terminal_title_stripped":"May I?"}]}}' ;;
"agent list") printf '%s' '{"result":{"agents":[{"tab_id":"w1:t2","pane_id":"w1:p2","agent":"claude","agent_status":"working","state_change_seq":7},{"tab_id":"w1:t2","pane_id":"w1:p3","agent":"codex","agent_status":"blocked","state_change_seq":9}]}}' ;;
esac
`
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	wsWatched = map[string]wsWatch{}

	tabs, err := herdrTabs()
	if err != nil {
		t.Fatal(err)
	}
	if got := wsDot(tabs[0].Status); got != "·" {
		t.Errorf("a tab of shells = %q, want no agent on it", got)
	}
	if got := wsDot(tabs[1].Status); got != "×" {
		t.Errorf("a split with a blocked agent in it = %q, want the blocked one", got)
	}
}

// wsRounds is a herdr that can be told what one tab of a workspace looks like,
// round by round: the agent's status and the counter herdr bumps every time it
// changes, and whether the tab is the one being looked at. It answers what
// herdrTabs asks and returns the dot the table would draw.
func wsRounds(t *testing.T) func(status string, seq int, focused string) string {
	t.Helper()
	dir := t.TempDir()
	herdr := filepath.Join(dir, "herdr")
	tabs := filepath.Join(dir, "tabs.json")
	agents := filepath.Join(dir, "agents.json")
	script := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
"tab list") cat %s ;;
"pane list") printf '%%s' '{"result":{"panes":[]}}' ;;
"agent list") cat %s ;;
esac
`, tabs, agents)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	wsWatched = map[string]wsWatch{}

	return func(status string, seq int, focused string) string {
		t.Helper()
		tab := fmt.Sprintf(`{"result":{"tabs":[{"tab_id":"w1:t2","label":"2","focused":%s}]}}`, focused)
		agent := fmt.Sprintf(`{"result":{"agents":[{"tab_id":"w1:t2","agent":"claude","agent_status":%q,"state_change_seq":%d}]}}`, status, seq)
		if err := os.WriteFile(tabs, []byte(tab), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(agents, []byte(agent), 0o644); err != nil {
			t.Fatal(err)
		}
		ts, err := herdrTabs()
		if err != nil {
			t.Fatal(err)
		}
		return wsDot(ts[0].Status)
	}
}

// The green dot is a turn that ended with nobody there to read it, and it is
// the ws view's own bookkeeping rather than herdr's `done`: that one is the
// server's seen state, and one look spends it for good — the next turn ends
// plain `idle`, because you were in the tab when the last one did. What this
// one goes on is `state_change_seq`, herdr's counter for when the agent last
// moved: anything past the count the tab last had while you were looking at it
// is something nobody has read. `space` puts it out too, which is this tui
// going there itself.
func TestWsGreenUntilTheTabIsLookedAt(t *testing.T) {
	round := wsRounds(t)
	for _, step := range []struct {
		status      string
		seq         int
		focused     string
		want, whyfy string
	}{
		{"working", 1, "false", "◐", "off working: nothing said yet"},
		{"idle", 2, "false", "✓", "the turn ended with nobody there to read it"},
		{"idle", 2, "false", "✓", "and it stays unread until someone is"},
		{"done", 2, "false", "✓", "herdr's own, while the server still has it"},
		{"idle", 2, "false", "✓", "spent over there, still unread over here"},
		{"idle", 2, "true", "○", "read"},
		{"idle", 2, "false", "○", "and read it stays"},
		{"working", 3, "false", "◐", "off again, so there will be something new"},
		{"idle", 4, "false", "✓", "and here it is"},
		{"idle", 4, "true", "○", "read again"},
		{"working", 5, "true", "◐", "you stay in the tab while it works"},
		{"idle", 6, "true", "○", "and are there when it ends: nothing to say"},
		{"idle", 6, "false", "○", "leaving doesn't make it unread"},
		{"working", 7, "false", "◐", "but the next turn is a new answer"},
		{"idle", 8, "false", "✓", "and this one you weren't there for"},
	} {
		if got := round(step.status, step.seq, step.focused); got != step.want {
			t.Errorf("%s/%d (focused=%s) = %q, want %q — %s", step.status, step.seq, step.focused, got, step.want, step.whyfy)
		}
	}
	if msg := herdrFocusTab("w1:t2"); msg != "" {
		t.Fatalf("herdr tab focus = %q", msg)
	}
	if got := round("idle", 8, "false"); got != "○" { // going there is reading it
		t.Errorf("after space = %q, want a read tab", got)
	}
}

// A conductor that only colours the turns it was running for hardly colours
// anything: `ccwt ws` re-execs itself on every ccwt upgrade, and a tab that had
// finished by then stayed grey for the rest of its life — herdr knew there was
// something waiting in it, and the table said nothing. Off herdr's counter a
// tui that has just come up says so, because it doesn't need to have watched
// the turn end to know it has.
func TestWsGreenSurvivesARestart(t *testing.T) {
	round := wsRounds(t)
	if got := round("working", 5, "false"); got != "◐" {
		t.Fatalf("working = %q", got)
	}
	if got := round("idle", 6, "true"); got != "○" { // read, so this tui is quiet about it
		t.Fatalf("read = %q", got)
	}
	wsWatched = map[string]wsWatch{} // the upgrade's re-exec, and everything it remembered
	if got := round("idle", 6, "false"); got != "✓" {
		t.Errorf("after a restart = %q, want the tab to say it has something in it", got)
	}
}

// The ws view's first section is where the workspace's merge request stands,
// in the columns `ccwt mr` prints it in. Most of a workspace's life there isn't
// one — the branch is fresh, nothing is pushed — and that has to read as one
// quiet line rather than as a row of column names with nothing under them, or
// as whatever glab said about not finding one.
func TestWsMRSectionIsOneQuietLineUntilThereIsOne(t *testing.T) {
	for _, tc := range []struct {
		what string
		look *mrLook
	}{
		{"the first lookup still out", nil},
		{"a branch with no merge request", &mrLook{}},
		{"a lookup that failed", &mrLook{err: errors.New("glab api: no\nsuch host")}},
	} {
		got := mrSection(tc.look, 0)
		if len(got) != 1 || strings.Contains(got[0], "MR") || strings.Contains(got[0], "\n") {
			t.Errorf("%s: %q, want one line, no column names and no paragraph", tc.what, got)
		}
		// Pinned lines are the ones the frame doesn't trim, so they arrive
		// already inside the terminal: one that wrapped would push the frame
		// down a line and take the cursor arithmetic with it.
		if got := mrSection(tc.look, 20); len([]rune(got[0])) > 20 {
			t.Errorf("%s at width 20: %q, want it cut to fit", tc.what, got[0])
		}
	}

	row := mrRow{ref: "acme/…/ccwt!42", url: "https://gl/acme/ccwt/-/merge_requests/42", status: "needs approval", pipeline: "green", title: "ws sections"}
	got := mrSection(&mrLook{rows: []mrRow{row}}, 0)
	if len(got) != 2 || !strings.HasPrefix(got[0], "  MR") {
		t.Fatalf("mrSection = %q, want the header and the one row", got)
	}
	// Indented into the gutter the tab table's `*` sits in, so the two sections
	// start at the same column.
	if !strings.HasPrefix(plain(got[1]), "  acme/…/ccwt!42") {
		t.Errorf("the row = %q, want the ref in the tab table's gutter", got[1])
	}
	for _, want := range []string{"needs approval", "green", "ws sections"} {
		if !strings.Contains(got[1], want) {
			t.Errorf("the row = %q, want %q on it", got[1], want)
		}
	}
	// The whole ref links to the merge request, the way `ccwt mr`'s own
	// terminal table does it — and the gutter it is indented by stays outside
	// the link, so what underlines is the ref and nothing else.
	if want := "  " + hyperlink(row.url, "acme/…/ccwt!42"); !strings.HasPrefix(got[1], want) {
		t.Errorf("the row = %q, want it to start with the link %q", got[1], want)
	}
	// The link costs the line no width: the frame pins these lines as they
	// are, and one that wrapped would take the cursor arithmetic with it. So
	// what a mouse drag counts along — and copies — is the table without it.
	bare := mrSection(&mrLook{rows: []mrRow{{ref: row.ref, status: row.status, pipeline: row.pipeline, title: row.title}}}, 0)
	if plain(got[1]) != bare[1] {
		t.Errorf("the row = %q bare of escapes, %q with no link at all", plain(got[1]), bare[1])
	}
}

// The ws view's two sections mean the frame pins more than one line: the merge
// request, the blank line under it and the tab table's own header all stay put
// while the tabs scroll underneath, and a click still lands on the tab it was
// aimed at however many lines are pinned above it.
func TestWsFramePinsTheMergeRequestAboveTheTabs(t *testing.T) {
	dir := t.TempDir()
	herdr := filepath.Join(dir, "herdr")
	script := `#!/bin/sh
case "$1 $2" in
"tab list") printf '%s' '{"result":{"tabs":[{"tab_id":"w1:t1","label":"one"},{"tab_id":"w1:t2","label":"two"},{"tab_id":"w1:t3","label":"three"}]}}' ;;
"pane list") printf '%s' '{"result":{"panes":[{"tab_id":"w1:t1","cwd":"/src/a"},{"tab_id":"w1:t2","cwd":"/src/b"},{"tab_id":"w1:t3","cwd":"/src/c"}]}}' ;;
"agent list") printf '%s' '{"result":{"agents":[]}}' ;;
esac
`
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	noMR(t)

	// Six lines: three pinned, two tabs, the bar. The third tab only exists once
	// the frame scrolls to it — which is the whole point of counting the pinned
	// ones, since they are what left room for two rather than three.
	defer func(old func() (int, int)) { termSize = old }(termSize)
	termSize = func() (int, int) { return 80, 6 }

	u := ui{ws: true}
	lines, err := u.frame()
	if err != nil {
		t.Fatal(err)
	}
	if u.head != 3 || len(u.rows) != 3 {
		t.Fatalf("head %d over %d rows, want 3 pinned lines over the three tabs", u.head, len(u.rows))
	}
	if !strings.HasPrefix(lines[2], "  TAB") || !strings.Contains(lines[3], "one") {
		t.Fatalf("frame = %q, want the tab header pinned third and the first tab under it", lines)
	}

	// Walk to the bottom tab: it is below the fold, so the frame scrolls — and
	// the pinned lines don't move with it.
	for range 3 {
		u.move(1)
	}
	if lines, err = u.frame(); err != nil {
		t.Fatal(err)
	}
	if u.top != 1 {
		t.Errorf("frame scrolled to row %d, want 1 — just far enough to show the selection", u.top)
	}
	if !strings.HasPrefix(lines[2], "  TAB") || !strings.Contains(lines[4], "three") {
		t.Errorf("scrolled frame = %q, want the header still pinned and the last tab on screen", lines)
	}
	// A click counts screen lines, so it has to count them past the pinned ones.
	if got := u.at(5); got != u.rows[2] {
		t.Errorf("clicking the last tab line selected %q, want %q", got.tab, u.rows[2].tab)
	}
	for _, n := range []int{1, 2, 3} { // the merge request, the blank, the header
		if got := u.at(n); got != (listRow{}) {
			t.Errorf("clicking pinned line %d selected %q, want nothing", n, got.tab)
		}
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
	noMR(t)
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
	t.Setenv("TMPDIR", dir) // the file the prompt travels in, somewhere it is swept up

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
	if !strings.Contains(string(calls), `pane run w1:p2 claude "$(cat `) {
		t.Errorf("herdr calls = %q, want the cli run in the pane the create named, on the file the prompt went into", calls)
	}
	if got := seededPrompt(t, string(calls)); got != "fix Bob's bug" {
		t.Errorf("the agent would be started on %q, want the whole prompt, apostrophe and all", got)
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
	t.Setenv("TMPDIR", dir) // the file the prompt travels in, somewhere it is swept up

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
	if want := `pane run w1:p2 claude --model 'claude-opus-5' "$(cat `; !strings.Contains(string(calls), want) {
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
	if !strings.Contains(string(calls), `pane run w1:p2 claude "$(cat `) {
		t.Errorf("herdr calls = %q, want no --model once the box was emptied", calls)
	}
	if got := seededPrompt(t, string(calls)); got != "and the docs" {
		t.Errorf("the agent would be started on %q, want the prompt the box was left holding", got)
	}
}

// The workspace wears the list's own two marks: a "✓" once the merge request
// is in, the "☐" of work pushed and waiting on a reviewer until then, and
// nothing at all where there is nothing to wait on — a merge request nobody
// opened yet, or one that was closed without landing. The badge always goes
// with a ttl, because nothing tells herdr when this tui stops; a lookup that
// failed says nothing rather than taking the last answer down with it.
func TestWsBadgeSaysWhereTheMergeRequestStands(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	if err := os.WriteFile(herdr, []byte(fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\n", log)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")

	const call = "workspace report-metadata w1 --source ccwt "
	ttl := " --ttl-ms " + fmt.Sprint(wsBadgeTTL.Milliseconds())
	var want []string
	for _, tc := range []struct{ status, token string }{
		{"merged", "--token mr=✓" + ttl},
		{"needs approval", "--token mr=☐" + ttl},
		{"can be merged", "--token mr=☐" + ttl},
		{"closed", "--clear-token mr"},
	} {
		wsBadge(&mrLook{rows: []mrRow{{status: tc.status}}})
		want = append(want, call+tc.token)
	}
	wsBadge(&mrLook{}) // never pushed: the workspace every one of these starts as
	want = append(want, call+"--clear-token mr")
	wsBadge(&mrLook{err: errors.New("glab is not installed")})

	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(calls), "\n"), "\n"); !slices.Equal(got, want) {
		t.Errorf("herdr calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
