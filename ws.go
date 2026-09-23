package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
// for an agent is the agent's own one-line account of what it is doing — and
// what `herdr agent list` says about the agents in it.
type wsTab struct {
	ID, Label, Status string
	Cwd, Title        string
	Focused           bool  // the one tab the user is looking at, herdr-wide
	Seq               int64 // herdr's state counter for the agent the dot is about
}

// herdrTabs is the workspace's tabs in the order herdr lists them, which is
// the order they sit in along the tab bar: tabs can be dragged about, and a
// tab's `number` is the one it was born with rather than the place it now
// holds, so the list's own order is the only thing that says where a tab is.
// The three lists are asked for separately because that is how herdr keeps
// them: a tab has a label and a place in the bar, a pane has a cwd and a
// title, and an agent has a status and the counter that says when it last
// changed.
func herdrTabs() ([]wsTab, error) {
	ws := os.Getenv("HERDR_WORKSPACE_ID")
	scope := map[string]any{"workspace_id": ws}
	out, err := herdrAsk("tab.list", scope, "tab", "list", "--workspace", ws)
	if err != nil {
		return nil, fmt.Errorf("herdr tab list: %w", err)
	}
	var tabs struct {
		Result struct {
			Tabs []struct {
				ID      string `json:"tab_id"`
				Label   string `json:"label"`
				Status  string `json:"agent_status"`
				Focused bool   `json:"focused"`
			} `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &tabs); err != nil {
		return nil, fmt.Errorf("herdr tab list: %w", err)
	}
	out, err = herdrAsk("pane.list", scope, "pane", "list", "--workspace", ws)
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
	// Not scoped to the workspace: `agent list` takes no --workspace, and a tab
	// id is herdr-wide, so the agents of other workspaces simply match no tab
	// of ours.
	//
	// An answer that doesn't come is no answer rather than an error: the tabs
	// and their panes are what the table is, and a herdr that won't say what
	// its agents are up to costs the dots their state, not the view.
	var agents struct {
		Result struct {
			Agents []struct {
				Tab    string `json:"tab_id"`
				Status string `json:"agent_status"`
				Seq    int64  `json:"state_change_seq"`
			} `json:"agents"`
		} `json:"result"`
	}
	if out, err := herdrAsk("agent.list", nil, "agent", "list"); err == nil {
		_ = json.Unmarshal(out, &agents)
	}
	// ponytail: the first pane herdr lists for the tab stands for it — a split
	// tab is two panes, and the table has one row to say what the tab is.
	first := map[string]int{}
	for i, p := range panes.Result.Panes {
		if _, ok := first[p.Tab]; !ok {
			first[p.Tab] = i
		}
	}
	// The dot is the agents' rather than the first pane's: herdr keeps a status
	// per agent, and which pane of a tab an agent happens to sit in is no
	// reason for the tab to go quiet about it. A tab split between several
	// shows whichever most wants you, and carries that one's counter.
	loudest := map[string]wsTab{}
	for _, a := range agents.Result.Agents {
		if wsRank(a.Status) > wsRank(loudest[a.Tab].Status) {
			loudest[a.Tab] = wsTab{Status: a.Status, Seq: a.Seq}
		}
	}
	var ts []wsTab
	for _, t := range tabs.Result.Tabs {
		// A tab with no agent in it has only herdr's own word to go on, which
		// is what keeps a bare shell a bare shell.
		ld := loudest[t.ID]
		wt := wsTab{ID: t.ID, Label: t.Label, Status: cmp.Or(ld.Status, t.Status), Focused: t.Focused, Seq: ld.Seq}
		if i, ok := first[t.ID]; ok {
			wt.Cwd, wt.Title = panes.Result.Panes[i].Cwd, panes.Result.Panes[i].Title
		}
		ts = append(ts, wt)
	}
	wsAttention(ts)
	return ts, nil
}

// wsRank is how much a state wants you, which is what settles the dot on a tab
// with more than one agent in it: an approval waiting on an answer first, then
// a turn that has ended with something to read, then work still going on, and
// last an agent with nothing to say. Nought is "unknown" — no agent, or one
// herdr can't place — and a state this ccwt hasn't heard of, which is the same
// "·" either way.
func wsRank(status string) int {
	switch status {
	case "blocked":
		return 4
	case "done":
		return 3
	case "working":
		return 2
	case "idle":
		return 1
	}
	return 0
}

// wsWatch is what the ws view remembers about a tab between rounds: herdr's
// state counter for the agent in it as of the last round, and what that counter
// stood at when the tab was last seen being looked at.
type wsWatch struct {
	seq, seen int64
}

// wsWatched is those, by tab — ccwt's own copy of the badge herdr's tui keeps
// rather than a cache of herdr's answer. Herdr's `done` is the server's seen
// state, and a single look spends it for good: an agent that finishes a second
// turn while you are away is plain `idle`, because you were in its tab when it
// finished the first. Herdr's own docs say as much — each tui client tracks
// viewed completions independently — so this is a client doing what herdr
// expects of one.
//
// What it tracks it off is `state_change_seq`, herdr's own counter for when an
// agent last changed state, rather than off watching for the change itself. A
// conductor that watches for it only ever colours the turns it was running for:
// `ccwt ws` re-execs itself on every upgrade, and the tabs that had finished by
// then stayed grey for the rest of their lives. The counter is there to be
// asked, and asking costs the third round trip a round.
//
// It is rebuilt from the tabs each round, which is what keeps closed tabs from
// piling up in it, and it belongs to the frame: the tab list is read there, and
// only the merge request lookup runs anywhere else.
var wsWatched = map[string]wsWatch{}

// wsAttention promotes the tabs with something unread in them to `done`, which
// is the green dot: an agent that has settled, and has moved since the last
// time anyone was looking at its tab. Herdr saying `done` itself is the same
// answer from the other side, and needs no promoting.
//
// The mark comes off when herdr says the tab is the focused one, which is what
// "the user has read it" looks like from out here — the counter as it stands
// then is what the next turn has to beat.
//
// A tui that has just started has no counter to beat for any tab, so everything
// settled reads unread: what a conductor is for is saying which tabs want you,
// and one glance clears a tab that didn't for good, where a grey one that did
// is an answer nobody comes back to.
func wsAttention(tabs []wsTab) {
	next := make(map[string]wsWatch, len(tabs))
	for i, t := range tabs {
		w := wsWatch{seq: t.Seq, seen: wsWatched[t.ID].seen}
		if t.Focused {
			w.seen = t.Seq
		}
		next[t.ID] = w
		// Only a settled tab is coloured: an agent that has quit since leaves
		// its last word on the screen, but "unknown" is a bare shell too, and
		// a green dot on one of those says nothing anybody can act on.
		if t.Status == "idle" && t.Seq > w.seen {
			tabs[i].Status = "done"
		}
	}
	wsWatched = next
}

// wsTable is the ws view's screen, in two sections: where the workspace's
// merge request stands, and under it the tabs working towards it. They are two
// tables rather than one because they answer to different questions — the
// review is the workspace's one outcome, the tabs are the several things going
// on inside it — and a blank line between them is what says so.
//
// The tab table is the header line, then a line per tab, fitted to width the
// way the worktree table is, and alongside them the row each line stands for —
// a tab by its id, carrying the directory it sits in so that `g` has a history
// to show. A `*` leads the tab the tui itself is in, as it leads the worktree
// you are standing in on the list.
//
// Only the tabs are rows: the merge request is something to read, not somewhere
// to go, so it sits in the lines the frame pins above the window (see u.head,
// which is however many lines came back beyond one per row).
//
// A herdr that won't answer is a line saying so rather than an error: the tui
// is parked in a pane for the day, and a hiccup on the socket is no reason to
// take it down.
func wsTable(width int) ([]string, []listRow) {
	head := append(wsMR(width), "")
	tabs, err := herdrTabs()
	if err != nil {
		return append(head, "  TAB", "  "+err.Error()), []listRow{{pid: -1}}
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
	return append(head, tabbed(table)...), rows
}

// mrWindow is how long the ws view goes on showing the same answer about the
// workspace's merge request. Longer than the git scan's window, and for the
// opposite reason: this one is a handful of gitlab round trips rather than a
// local one, and a review doesn't move in the couple of seconds the tab table
// is redrawn in.
const mrWindow = time.Minute

// mrLook is one round's answer: the merge requests, or what went wrong asking
// for them. A nil *mrLook is the first round, still out — which is a different
// thing to say than "there is no merge request", and worth saying for the
// second or two a gitlab lookup takes.
type mrLook struct {
	rows []mrRow
	err  error
}

// wsMRLook is the last round that came back, and whether one is in flight.
//
// The lookup runs in the background rather than through cached, which would run
// it on the frame: the frame is also what takes keystrokes, and gitlab over
// someone's vpn is seconds of not answering the arrow keys. So a section is
// drawn from the last answer, and a fresh round goes off when the window turns
// over — one at a time, so a lookup slower than its own window can't pile up
// behind itself.
var wsMRLook struct {
	sync.Mutex
	stamp   string
	running bool
	look    *mrLook
}

// wsMR is the ws view's first section: where the workspace's merge request
// stands, which is the row `ccwt mr` prints when you run it with no argument —
// the question the whole workspace exists to get a yes to, and the reason its
// first tab is a conductor rather than another agent.
func wsMR(width int) []string {
	wsMRLook.Lock()
	defer wsMRLook.Unlock()
	if stamp := window(mrWindow); wsMRLook.stamp != stamp && !wsMRLook.running {
		wsMRLook.stamp, wsMRLook.running = stamp, true
		go func() {
			look := branchLook()
			wsBadge(look)
			wsMRLook.Lock()
			defer wsMRLook.Unlock()
			wsMRLook.look, wsMRLook.running = look, false
		}()
	}
	return mrSection(wsMRLook.look, width)
}

// branchLook is one round of it: the merge request of the branch the workspace
// is working on, and then the same lookup any other `ccwt mr` argument gets.
//
// glab reports "this branch has no merge request" and "there is no glab here"
// the same way, as a failure, and the first is where every workspace starts —
// so a branch it can't find one for is no rows rather than an error to put on
// the screen. `ccwt mr` in a tab says which of the two it was.
func branchLook() *mrLook {
	u, err := branchMR()
	if err != nil {
		return &mrLook{}
	}
	rows, err := lookThing(u, true)
	return &mrLook{rows, err}
}

// wsBadgeTTL is how long herdr goes on showing the badge without hearing from
// us again: a handful of lookups, so a round that didn't come back doesn't
// blink it off the sidebar, and short enough that a workspace whose ws tui has
// gone stops claiming anything about its merge request.
const wsBadgeTTL = 5 * mrWindow

// wsBadge puts where the merge request stands on the workspace itself, in the
// two marks the worktree list already leads a name with: the "✓" of a branch
// that is in, and until then the "☐" of work pushed and waiting on a reviewer.
// The same two states said the same way, whether you are reading the list or
// the sidebar down the side of it.
//
// herdr has no icon for a client to set, so it goes as display-only workspace
// metadata under an `mr` token, which the spaces panel draws for whoever has
// asked for it in their config:
//
//	[ui.sidebar.spaces]
//	rows = [["state_icon", "workspace", "$mr"], ["branch", "git_status"]]
//
// Nothing tells herdr when this tui stops, so the badge always carries a ttl
// and expires on its own rather than leaving a "✓" on a workspace that has long
// since moved on. A lookup that failed reports nothing at all: glab being down
// is no news about the merge request, and the badge standing another few
// minutes says less than a wrong one would.
func wsBadge(look *mrLook) {
	ws := os.Getenv("HERDR_WORKSPACE_ID")
	if ws == "" || look == nil || look.err != nil {
		return
	}
	glyph := ""
	if len(look.rows) > 0 {
		switch look.rows[0].status {
		case "merged":
			glyph = "✓"
		case "closed", "locked": // nothing left to wait on, and nothing landed: no badge beats either mark
		default:
			glyph = reviewGlyph
		}
	}
	// A workspace with nothing to say loses the token rather than keeping the
	// last thing it said until the ttl runs out.
	params := map[string]any{"workspace_id": ws, "source": "ccwt", "tokens": map[string]any{"mr": nil}}
	token := []string{"--clear-token", "mr"}
	if glyph != "" {
		params["tokens"], params["ttl_ms"] = map[string]any{"mr": glyph}, wsBadgeTTL.Milliseconds()
		token = []string{"--token", "mr=" + glyph, "--ttl-ms", fmt.Sprint(wsBadgeTTL.Milliseconds())}
	}
	// The workspace id goes ahead of the flags: herdr's parser takes a trailing
	// one for the value of whatever option came last and refuses the lot.
	_, _ = herdrAsk("workspace.report_metadata", params, append([]string{"workspace", "report-metadata", ws, "--source", "ccwt"}, token...)...)
}

// mrSection is that answer as lines, laid out in the same columns `ccwt mr`
// uses — the MR cell indented into the gutter the tab table's `*` sits in, so
// that the two sections start at the same column.
//
// A workspace whose branch has never been pushed is where every workspace
// starts and most of one's life is spent, so it gets a quiet line saying so
// rather than a row of column names with nothing under them.
//
// The ref carries the same OSC 8 link the table `ccwt mr` prints does — one
// merge request said one way, wherever you read it. The escape is no width on
// screen but plenty of runes in the string, which is fine here: these lines
// are pinned rather than trimmed, and they arrive already cut to the width
// below.
func mrSection(look *mrLook, width int) []string {
	msg := ""
	switch {
	case look == nil:
		msg = "looking for the merge request…"
	case look.err != nil:
		msg = oneLine(look.err.Error())
	case len(look.rows) == 0:
		msg = "no merge request yet"
	}
	// The cut happens here rather than in the frame: these lines are pinned
	// above the window, and the frame only trims the ones that scroll. A line
	// that wrapped would push the whole frame down one and take its cursor
	// arithmetic with it — and a glab error is a sentence of someone else's
	// writing, as long as they felt like making it.
	if msg != "" {
		if msg = "  " + msg; width > 0 {
			msg = truncate(msg, width)
		}
		return []string{msg}
	}
	table, cols := mrCells(look.rows)
	for _, row := range table {
		row[0] = "  " + row[0]
	}
	fitTable(table, width, cols)
	lines := tabbed(table)
	paintMerged(lines, table, look.rows)
	linkRefs(lines, table, look.rows)
	return lines
}

// wsDot is the AGENT column's mark for a tab's agent status, the way herdr
// marks it in the tab bar: a circle, and the state is the colour of it. The
// status is herdr's, bar the one wsAttention makes: `done` is the green one,
// and whether a finished turn is still green is this tui's own bookkeeping.
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

// askModel feeds one keystroke to the model box: enter takes what's typed as
// the --model the seed prompt's agent runs on, escape leaves the one already in
// force alone, and the rest is the same line editor the prompt uses.
//
// An empty box accepted is how a model is taken back off: the agent runs on the
// harness's default again, which is what it did before anyone pressed ctrl-o.
// The name is not checked against anything — the harness knows its own models,
// and a ccwt that kept a list of them would be wrong by the next release.
func (u *ui) askModel(k string) {
	switch k {
	case "\r", "\x1b[27;2;13~": // shift-↵ too: a model name is one line
		u.modelName, u.model = strings.TrimSpace(u.model.text), entry{}
	case "\x1b", "\x03":
		u.model = entry{}
	default:
		u.model.text, u.model.cur = lineEdit(u.model.text, u.model.cur, k)
	}
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
// The model is whatever ctrl-o last set, appended as a flag rather than woven
// into task_command: the command is the config's, the same for every agent, and
// the model is this one's.
//
// The pane comes out of the create's own answer: with every tab in the one
// directory, there is nothing else that tells the new pane from the tui's own.
func (u *ui) seed(prompt string) error {
	argv, err := taskCommand()
	if err != nil {
		return err
	}
	if u.modelName != "" {
		argv = append(argv, "--model", shellQuote(u.modelName))
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
	arg, err := promptArg(prompt)
	if err != nil {
		return err
	}
	if out, err := exec.Command(herdrBin(), append([]string{"pane", "run", pane}, append(argv, arg)...)...).CombinedOutput(); err != nil {
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
	// Going there is reading it, and it counts from now rather than from the
	// next round: a look and a step straight back is quicker than the poll.
	w := wsWatched[id]
	w.seen = w.seq
	wsWatched[id] = w
	return ""
}

// wsDown is what the list warns of in its corner: the workspaces whose `ws` tab
// has no ccwt running in it, by name, as watchWsDown last found them.
var wsDown struct {
	sync.Mutex
	names []string
}

func wsDownNames() []string {
	wsDown.Lock()
	defer wsDown.Unlock()
	return wsDown.names
}

// wsDownEvents are what herdr says when the `ws` tabs, or the names of the
// workspaces they are in, may have changed.
var wsDownEvents = []string{"tab.created", "tab.renamed", "tab.closed", "workspace.renamed", "workspace.closed"}

// wsDownEvery is how often the list looks anyway. A ccwt that quits hands its
// pane back to the shell, and herdr says nothing when a pane's foreground
// changes hands — nothing at all, in a probe session on herdr 0.9.1 — so this
// is what sees a conductor go.
const wsDownEvery = 10 * time.Second

// wsSettle is how long a look waits after an event: a burst of them is one
// change, and a tab `c` has just named `ws` has had its ccwt typed at a shell
// that may not have finished starting yet. ponytail: package var so tests can
// shorten it.
var wsSettle = 2 * time.Second

// watchWsDown keeps wsDown up to date for as long as ctx lasts: a look when
// herdr says the tabs have moved, and every wsDownEvery besides.
func watchWsDown(ctx context.Context) {
	poke := make(chan struct{}, 1)
	go herdrListen(ctx, poke, wsDownEvents...)
	for {
		select {
		case <-ctx.Done():
			return
		case <-poke:
			select {
			case <-ctx.Done():
				return
			case <-time.After(wsSettle):
			}
		case <-time.After(wsDownEvery):
		}
		names := herdrWsDown()
		wsDown.Lock()
		wsDown.names = names
		wsDown.Unlock()
	}
}

// herdrWsDown is the workspaces with a `ws` tab that has no ccwt running in
// it, by name, in the order herdr's sidebar lists them: a conductor that quit,
// or never came back after herdr did — herdr restores a tab's name but not what
// was running in it. The name is what says a ccwt belongs there, since it is
// the one `c` gives the tab it starts `ccwt ws` in. What is running is what
// herdr says is in the foreground of the tab's panes.
//
// Every workspace herdr has, not just this repo's: a tab named `ws` is ccwt's
// wherever it is.
//
// A pane herdr can't say that about counts as having one, and a herdr that
// won't answer at all as having no such tabs: this is a warning, and one that
// isn't sure is one to leave out.
func herdrWsDown() []string {
	var snap struct {
		Result struct {
			Snapshot struct {
				Workspaces []struct {
					ID    string `json:"workspace_id"`
					Label string `json:"label"`
				} `json:"workspaces"`
				Tabs []struct {
					ID        string `json:"tab_id"`
					Workspace string `json:"workspace_id"`
					Label     string `json:"label"`
				} `json:"tabs"`
				Panes []struct {
					ID  string `json:"pane_id"`
					Tab string `json:"tab_id"`
				} `json:"panes"`
			} `json:"snapshot"`
		} `json:"result"`
	}
	out, err := herdrAsk("session.snapshot", nil, "api", "snapshot")
	if err != nil || json.Unmarshal(out, &snap) != nil {
		return nil
	}
	s := snap.Result.Snapshot
	up := map[string]bool{} // a `ws` tab -> a ccwt is running in it
	for _, t := range s.Tabs {
		if t.Label == "ws" {
			up[t.ID] = false
		}
	}
	for _, p := range s.Panes {
		if running, ok := up[p.Tab]; ok && !running {
			up[p.Tab] = herdrRunsCcwt(p.ID)
		}
	}
	down := map[string]bool{}
	for _, t := range s.Tabs {
		if running, ok := up[t.ID]; ok && !running {
			down[t.Workspace] = true
		}
	}
	var names []string
	for _, w := range s.Workspaces {
		if down[w.ID] {
			names = append(names, w.Label)
		}
	}
	return names
}

// herdrRunsCcwt reports whether a ccwt is in the foreground of a pane, or
// herdr can't say.
func herdrRunsCcwt(pane string) bool {
	out, err := herdrAsk("pane.process_info", map[string]any{"pane_id": pane}, "pane", "process-info", "--pane", pane)
	var resp struct {
		Result struct {
			Info struct {
				Foreground []struct {
					Name string `json:"name"`
				} `json:"foreground_processes"`
			} `json:"process_info"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(out, &resp) != nil {
		return true
	}
	for _, p := range resp.Result.Info.Foreground {
		if p.Name == "ccwt" {
			return true
		}
	}
	return false
}

// wsDownPane is the corner's box: the workspaces herdrWsDown named, one a line,
// under a rule that says what they are missing. As wide as its widest line, as
// the menu is, and as tall as rows leaves room for; nil when there is nothing
// to say, or nowhere to say it.
func wsDownPane(names []string, cols, rows int) []string {
	const title = "ccwt ws not running"
	if len(names) == 0 || rows < 3 {
		return nil
	}
	inner := len(title) + 4 // the rule either side of it
	for _, n := range names {
		inner = max(inner, len([]rune(n))+2)
	}
	inner = min(inner, max(cols-2, 1))
	row := paneRow("", inner)
	var body []string
	for _, n := range names[:min(len(names), rows-2)] {
		body = append(body, row(" "+n))
	}
	return paneBox("", inner, title, body)
}
