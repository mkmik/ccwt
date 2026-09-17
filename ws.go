package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mkmik/ccwt/internal/gitutil"
)

// WsCmd is `ccwt ws`: the tui for the first tab of a herdr workspace. It asks
// for a seed prompt, starts an agent on it in a tab of its own, and keeps a
// table of the workspace's tabs to jump between — the workspace's own view of
// what is running in it, where the plain tui is the repo's.
//
// It is the tui in another mode, as the ps view is, rather than a program of
// its own: the window, the selection, the search, the mouse and the prompt box
// are the ones the list already has. Only what the rows are, and so what the
// keys do with one, changes.
type WsCmd struct {
	Interval time.Duration `default:"2s" help:"How often to re-read the workspace's tabs."`
}

func (c *WsCmd) Run() error {
	if !underHerdr() {
		return errors.New("ws runs in a herdr workspace, and this isn't one (no HERDR_ENV)")
	}
	return (&TuiCmd{Interval: c.Interval, ws: true}).Run()
}

// wsTab is one tab of the workspace as the ws view draws it: what `herdr tab
// list` says about the tab, joined with what `herdr pane list` says about the
// first pane in it — where it sits and what its terminal calls itself, which
// for an agent is the agent's own one-line account of what it is doing.
type wsTab struct {
	ID, Label, Status string
	Cwd, Title        string
}

// herdrTabs is the workspace's tabs in the order herdr lists them, which is
// the order they sit in along the tab bar: tabs can be dragged about, and a
// tab's `number` is the one it was born with rather than the place it now
// holds, so the list's own order is the only thing that says where a tab is.
// The two lists are asked for separately because that is how herdr keeps them:
// a tab has a label and an agent status, a pane has a cwd and a title.
func herdrTabs() ([]wsTab, error) {
	ws := os.Getenv("HERDR_WORKSPACE_ID")
	out, err := exec.Command(herdrBin(), "tab", "list", "--workspace", ws).Output()
	if err != nil {
		return nil, fmt.Errorf("herdr tab list: %w", err)
	}
	var tabs struct {
		Result struct {
			Tabs []struct {
				ID     string `json:"tab_id"`
				Label  string `json:"label"`
				Status string `json:"agent_status"`
			} `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &tabs); err != nil {
		return nil, fmt.Errorf("herdr tab list: %w", err)
	}
	out, err = exec.Command(herdrBin(), "pane", "list", "--workspace", ws).Output()
	if err != nil {
		return nil, fmt.Errorf("herdr pane list: %w", err)
	}
	var panes struct {
		Result struct {
			Panes []struct {
				Tab   string `json:"tab_id"`
				Cwd   string `json:"cwd"`
				Title string `json:"terminal_title_stripped"`
			} `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &panes); err != nil {
		return nil, fmt.Errorf("herdr pane list: %w", err)
	}
	// ponytail: the first pane herdr lists for the tab stands for it — a split
	// tab is two panes, and the table has one row to say what the tab is.
	first := map[string]int{}
	for i, p := range panes.Result.Panes {
		if _, ok := first[p.Tab]; !ok {
			first[p.Tab] = i
		}
	}
	var ts []wsTab
	for _, t := range tabs.Result.Tabs {
		wt := wsTab{ID: t.ID, Label: t.Label, Status: t.Status}
		if i, ok := first[t.ID]; ok {
			wt.Cwd, wt.Title = panes.Result.Panes[i].Cwd, panes.Result.Panes[i].Title
		}
		ts = append(ts, wt)
	}
	return ts, nil
}

// wsTable is the ws view's list: the header line, then a line per tab, fitted
// to width the way the worktree table is, and alongside them the row each line
// stands for — a tab by its id, carrying the directory it sits in so that `g`
// has a history to show. A `*` leads the tab the tui itself is in, as it leads
// the worktree you are standing in on the list.
//
// A herdr that won't answer is a line saying so rather than an error: the tui
// is parked in a pane for the day, and a hiccup on the socket is no reason to
// take it down.
func wsTable(width int) ([]string, []listRow) {
	tabs, err := herdrTabs()
	if err != nil {
		return []string{"  TAB", "  " + err.Error()}, []listRow{{pid: -1}}
	}
	cols := []column{{name: "TAB", cut: truncate, max: 30}, {name: "AGENT"}, {name: "DIR", cut: elide, max: 30}, {name: "TITLE", cut: truncate}}
	table := [][]string{{"  TAB", "AGENT", "DIR", "TITLE"}}
	var rows []listRow
	self := os.Getenv("HERDR_TAB_ID")
	for _, t := range tabs {
		mark := "  "
		if t.ID == self {
			mark = "* "
		}
		table = append(table, []string{mark + t.Label, wsDot(t.Status), filepath.Base(t.Cwd), t.Title})
		rows = append(rows, listRow{path: t.Cwd, tab: t.ID})
	}
	fitTable(table, width, cols)

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	for _, r := range table {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	w.Flush()
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n"), rows
}

// wsDot is the AGENT column's mark for a tab's agent status, the way herdr
// marks it in the tab bar: a circle, and the state is the colour of it.
//
// The cell carries herdr's other set, the colourless symbols it draws with
// `ui.status_indicators = "symbols"`, and paintDots turns each one into the
// coloured circle at the end of the frame. The stand-in is what keeps the
// table honest: it is one rune, so fitTable and tabwriter measure the column
// the way it will print, and the search looks at a character rather than at
// the middle of an escape.
func wsDot(status string) string {
	switch status {
	case "blocked":
		return "×"
	case "working":
		return "◐"
	case "done":
		return "✓"
	case "idle":
		return "○"
	}
	return "·" // "unknown": a shell, or a tab with nothing in it yet
}

// wsActions is what the keys do in the ws view. `n` is the seed prompt — a new
// agent in a tab of its own — unless a search is in force, where it is vim's
// next match as it is on the list; a tab is somewhere to go, and `g` shows the
// history of the worktree it sits in.
func wsActions(sel listRow, searching bool) []action {
	as := []action{{"q", "quit"}, {"/", "search"}}
	if searching {
		as = append(as, action{"n", "next"}, action{"N", "prev"})
	} else {
		as = append(as, action{"n", "agent"})
	}
	if sel.tab != "" {
		as = append(as, action{" ", "go"}, action{"g", "git"})
	}
	// `r` is the workspace's own, not the selected row's — see wsRemovable —
	// so it neither needs a selection nor comes and goes as you walk the table.
	if wsRemovable() {
		as = append(as, action{"r", "remove"})
	}
	return as
}

// wsRemovable reports whether this workspace is finished with: the tui is
// standing in a worktree, and `ccwt done` there would go through without -D —
// merged branch, nothing uncommitted, no agent still working in it.
//
// The question is the workspace's, which is to say the directory `ccwt ws` was
// started in, and not any tab's: the tabs are where the work is happening, the
// workspace is the thing that gets closed when it's over. So `r` is on the bar
// or it isn't, the same whichever row is selected.
//
// ponytail: cached for the same stretch as the list's "can this go?" glyph and
// for the same reason — it is a full worktree scan, and the bar asks on every
// frame. A stale yes costs a refusal message, never a removal: `ccwt done`
// asks git again itself.
func wsRemovable() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	return removableCache.get(cwd, window(gitScanWindow), func() bool {
		_, name, err := gitutil.CurrentClaudeWorktree()
		if err != nil || name == "" { // the repo itself, or anywhere else: nothing to remove
			return false
		}
		root, err := gitutil.RepoRoot("", true)
		return err == nil && removeBlocked(root, name, false) == nil
	})
}

var removableCache cached[bool]

// wsDone is `r` in the ws view: `ccwt done`, run where the tui is standing —
// the worktree goes, branch and all, and the workspace closes behind it, this
// tui with it. There is nothing to redraw when it works; when it doesn't, the
// bar says why, as it does for the list's own `r`.
func wsDone() string {
	_, name, _ := gitutil.CurrentClaudeWorktree()
	if err := (&DoneCmd{}).Run(); err != nil {
		return "remove failed: " + err.Error()
	}
	return "removed " + name
}

// askSeed is what `ccwt ws` opens with in a workspace that has no other tab
// yet: the seed prompt, since the first thing to do in a fresh workspace is to
// say what it is for. A workspace with tabs already in it gets the table.
func (u *ui) askSeed() {
	if tabs, err := herdrTabs(); err == nil && len(tabs) <= 1 {
		u.entry = newEntry(listRow{}, "", 0)
	}
}

// startSeed is ↵ on the seed prompt: start the agent, and close the box only
// once it is running. A failure leaves the prompt up with the text still in it
// — retyping a paragraph because herdr was busy is not a reasonable thing to
// ask — and says why in the bar.
func (u *ui) startSeed() string {
	if strings.TrimSpace(u.entry.text) == "" { // nothing typed: same as abandoning it
		u.entry = entry{}
		return ""
	}
	if err := u.seed(u.entry.text); err != nil {
		return "start failed: " + err.Error()
	}
	u.entry = entry{}
	return "started"
}

// seed starts an agent on prompt in a tab of its own: a tab of this workspace
// in the directory the tui is standing in, with task_command's cli run there on
// the prompt.
//
// The worktree is the workspace's, not the tab's: the agents of one workspace
// are working on one thing, and a `c` from the list has already made them a
// worktree to work on it in. The tab goes unlabelled for the same reason — it
// would be the same name every time, and herdr names an unlabelled tab after
// what runs in it, which is the agent saying what it is doing.
//
// The pane comes out of the create's own answer: with every tab in the one
// directory, there is nothing else that tells the new pane from the tui's own.
func (u *ui) seed(prompt string) error {
	argv, err := taskCommand()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	out, err := exec.Command(herdrBin(), "tab", "create", "--workspace", os.Getenv("HERDR_WORKSPACE_ID"),
		"--cwd", cwd, "--no-focus").CombinedOutput()
	if err != nil {
		return fmt.Errorf("herdr tab create: %s", lastLine(out, err))
	}
	var resp struct {
		Result struct {
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &resp)
	pane := resp.Result.Pane.ID
	if pane == "" {
		return errors.New("no pane in the new tab to run the agent in")
	}
	if out, err := exec.Command(herdrBin(), append([]string{"pane", "run", pane}, append(argv, shellQuote(prompt))...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr pane run: %s", lastLine(out, err))
	}
	return nil
}

// herdrFocusTab goes to one tab, which is what `space` does on a row of the ws
// view. It reports for the bar, and has nothing to say when it worked: you are
// looking at the other tab by then.
func herdrFocusTab(id string) string {
	if out, err := exec.Command(herdrBin(), "tab", "focus", id).CombinedOutput(); err != nil {
		return "herdr tab focus: " + lastLine(out, err)
	}
	return ""
}
