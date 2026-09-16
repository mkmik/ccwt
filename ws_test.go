package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// `ccwt ws` is the tui of a herdr workspace: its list is the workspace's tabs
// in herdr's order, each joined with the first pane in it — where it sits and
// what its terminal calls itself — and a `*` leads the tab the tui is itself
// in. A tab is a row to go to, not a worktree to remove, whatever directory it
// carries: the keys that unmake things must not take it for one.
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
		{"* 1 ", "ccwt", "unknown"},                                   // ours, a bare shell: no agent to speak of
		{"  calm-baking-otter ", "Waiting on your review", "a split"}, // the first pane speaks for the tab
	} {
		l := lines[i+1]
		if !strings.HasPrefix(l, want.prefix) || !strings.Contains(l, want.has) || strings.Contains(l, want.hasNot) {
			t.Errorf("line %d = %q, want %q… with %q and without %q", i+1, l, want.prefix, want.has, want.hasNot)
		}
	}
	if !strings.Contains(lines[2], "idle") {
		t.Errorf("line 2 = %q, want the agent's status on it", lines[2])
	}
	want := []listRow{{path: "/src/ccwt", tab: "w1:t1"}, {path: "/src/ccwt/.claude/worktrees/calm-baking-otter", tab: "w1:t3"}}
	if !slices.Equal(rows, want) {
		t.Errorf("rows = %v, want %v", rows, want)
	}
	if rows[1].worktree() || rows[1].section() || rows[1].pending() || rows[1].process() {
		t.Errorf("a tab row %v reads as something else", rows[1])
	}

	bar := keyBar(200, "", "", wsActions(rows[1], false))
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

// The seed prompt is a "<new>" row's space in a tab: a fresh worktree of the
// repo, a tab of this workspace sitting in it and named after it, and the
// configured cli run in the pane the create answered with, the whole prompt as
// one argument. The box closes only once that has happened — a herdr that
// refuses leaves the prompt where it was typed, to try again from.
func TestWsSeedStartsAnAgentInANewTab(t *testing.T) {
	root := initRepo(t)
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
	msg := u.startSeed()
	name, ok := strings.CutPrefix(msg, "started ")
	if !ok {
		t.Fatalf("startSeed: %s", msg)
	}
	if u.entry.open {
		t.Error("the box is still up after the agent started")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", name)); err != nil {
		t.Errorf("no worktree %s was made: %v", name, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	created := regexp.MustCompile(`tab create --workspace w1 --cwd (\S+) --label (\S+) --no-focus`).FindStringSubmatch(string(calls))
	if created == nil || filepath.Base(created[1]) != name || created[2] != name {
		t.Errorf("herdr calls = %q, want a tab of w1 in the worktree %s, named after it", calls, name)
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
