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
	"slices"
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
	Prompt   string        `help:"Open on the seed prompt with this already typed in it."`
	Title    string        `help:"The seed prompt's title, already typed in."`
}

func (c *WsCmd) Run() error {
	if !underHerdr() {
		return errors.New("ws runs in a herdr workspace, and this isn't one (no HERDR_ENV)")
	}
	return (&TuiCmd{Interval: c.Interval, ws: true, seedPrompt: c.Prompt, seedTitle: c.Title}).Run()
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
	out, err := herdrPoll("tab.list", scope, "tab", "list", "--workspace", ws)
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
	out, err = herdrPoll("pane.list", scope, "pane", "list", "--workspace", ws)
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
	// Not scoped to the workspace: a tab id is herdr-wide, so the agents of
	// other workspaces simply match no tab of ours.
	//
	// An answer that doesn't come is no answer rather than an error: the tabs
	// and their panes are what the table is, and a herdr that won't say what
	// its agents are up to costs the dots their state, not the view.
	agents := herdrAgents(herdrPoll)
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
	for _, a := range agents {
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

// wsWatched is those, by tab (by agent, in the list: see agentDots) — ccwt's
// own copy of the badge herdr's tui keeps rather than a cache of herdr's
// answer. Herdr's `done` is the server's seen state, and a single look spends
// it for good: an agent that finishes a second turn while you are away is
// plain `idle`, because you were in its tab when it finished the first.
// Herdr's own docs say as much — each tui client tracks viewed completions
// independently — so this is a client doing what herdr expects of one.
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

// agentDots is what the list leads a worktree's row with under herdr: the dot
// the ws view would give a tab, by the same rules — the agent that most wants
// you when there are several, and green for a turn that has ended since anyone
// last looked. A worktree with no agent herdr can place is `·`, as a tab of
// shells is.
//
// An agent is in the worktree either of its cwds is in, as for the `✳`
// herdrBusy puts on the row. Each goes through wsAttention on its own, under
// its pane's id, so that two agents in one worktree are two turns to read; a
// tab counts as looked at while one of its agents has the focus.
//
// ponytail: an agent's focus stands in for its tab's — the list asks herdr for
// agents and nothing else — so a tab read from a shell pane beside its agent
// stays green until the agent's pane has had the focus. A herdr-wide `tab list`
// would settle it, at a second round trip a round.
func agentDots(agents []herdrAgent) func(path string) string {
	looked := map[string]bool{} // a tab -> one of its agents has the focus
	for _, a := range agents {
		looked[a.Tab] = looked[a.Tab] || a.Focused
	}
	dots := make([]wsTab, len(agents))
	for i, a := range agents {
		dots[i] = wsTab{ID: a.Pane, Status: a.Status, Seq: a.Seq, Focused: looked[a.Tab]}
	}
	wsAttention(dots)
	return func(path string) string {
		status := ""
		for i, a := range agents {
			if (under(a.Cwd, path) || under(a.Foreground, path)) && wsRank(dots[i].Status) > wsRank(status) {
				status = dots[i].Status
			}
		}
		return wsDot(status)
	}
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
	tabs, err := wsTabs()
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

// wsTabsHeard is herdrTabs' last answer, kept while the ws view listens to
// herdr's events: it asks again only when an event has said something in the
// workspace changed (dirty), or wsTabsEvery has gone by without one — the way
// back from an event that went missing. Only the tui's own loop touches it.
var wsTabsHeard struct {
	on, dirty bool
	tabs      []wsTab
	at        time.Time
}

// wsTabsEvery is how long the ws view trusts what it last heard about its tabs
// while herdr is quiet.
const wsTabsEvery = time.Minute

// wsTabs is herdrTabs, through wsTabsHeard.
func wsTabs() ([]wsTab, error) {
	h := &wsTabsHeard
	if h.on && !h.dirty && time.Since(h.at) < wsTabsEvery {
		return h.tabs, nil
	}
	tabs, err := herdrTabs()
	if err == nil {
		h.tabs, h.at, h.dirty = tabs, time.Now(), false
	}
	return tabs, err
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
		was := lookMerged(wsMRLook.look)
		go func() {
			look := branchLook()
			if lookMerged(look) && !was {
				landed()
			}
			wsBadge(look)
			wsMRLook.Lock()
			defer wsMRLook.Unlock()
			wsMRLook.look, wsMRLook.running = look, false
		}()
	}
	return mrSection(wsMRLook.look, width)
}

func lookMerged(look *mrLook) bool {
	return look != nil && len(look.rows) > 0 && look.rows[0].status == "merged"
}

// landed is the forge's "merged" passed on to everything else that asks the
// same question of git on its own clock: origin/main is fetched now rather than
// at the next background round, and the cached answers drawn from the old one
// are dropped — so `r` turns up within a frame of the ✓, not a minute or so
// after it. Each of them still asks git, and a refusal still comes from git:
// the forge saying merged makes nothing removable on its own.
func landed() {
	fetchOrigin(context.Background(), ".")
	removableCache.forget()
	mergedCache.forget()
}

// branchLook is one round of it: the merge request of the branch the workspace
// is working on, and then the same lookup any other `ccwt mr` argument gets.
//
// glab reports "this branch has no merge request" and "there is no glab here"
// the same way, as a failure, and the first is where every workspace starts —
// so a branch it can't find one for is no rows rather than an error to put on
// the screen. `ccwt mr` in a tab says which of the two it was.
func branchLook() *mrLook {
	u, err := branchMR(".")
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
// marks the worktree list already leads a name with: the "✓" of a branch that
// is in, and until then the "☐" of work pushed and waiting on a reviewer. The
// same states said the same way, whether you are reading the list or the
// sidebar down the side of it — the "±" of a merged branch with uncommitted
// changes too, which is why the checkout this tui stands in is asked about
// once the merge request is in: a "✓" reads as "done with, remove it", and
// removing it would lose them.
//
// While it waits, the box says what its pipeline is doing, since that is
// what decides whether the wait is on you: a box in a box while it runs,
// crossed out once it failed, and filled while it rides a merge train (or
// GitHub's merge queue), where the pipeline is the train's and nothing is on you
// either way. The sidebar has no colour to say it with, so the shape does.
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
			if gitutil.Dirty("") {
				glyph = mergedDirtyGlyph
			}
		case "closed", "locked": // nothing left to wait on, and nothing landed: no badge beats either mark
		default:
			glyph = reviewGlyph
			switch r := look.rows[0]; {
			case r.status == "merge train" || r.status == "merge queue":
				glyph = "■"
			case r.pipeline == "running":
				glyph = "▣"
			case strings.HasPrefix(r.pipeline, "failed"):
				glyph = "☒"
			}
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
	_, _ = herdrPoll("workspace.report_metadata", params, append([]string{"workspace", "report-metadata", ws, "--source", "ccwt"}, token...)...)
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
	linkComments(lines, table, look.rows)
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
		as = append(as, action{"n", "agent"}, action{"T", "todo"})
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
		return err == nil && removeBlocked(root, name, false, herdrPoll) == nil
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
//
// A prompt handed over — a "<new>" row's, see startPending — opens the box
// whatever else is there, with it and its title already typed in, so the effort
// can be picked and a line added before anything runs.
func (u *ui) askSeed(prompt, title string) {
	if tabs, err := herdrTabs(); prompt != "" || err == nil && len(tabs) <= 1 {
		u.entry, u.title = newEntry(listRow{}, prompt, 0), newEntry(listRow{}, title, 0)
	}
}

// askTitle feeds one keystroke to the seed prompt's title line: ↵ or tab go on
// down to the prompt, shift-tab up to the effort scale, escape abandons the
// lot, and the rest is the line editor.
func (u *ui) askTitle(k string) {
	switch k {
	case "\r", "\t", "\x1b[27;2;13~":
		u.title.open = false
	case "\x1b[Z":
		u.title.open, u.effortFocus = !u.ws, u.ws
	case "\x1b", "\x03":
		u.entry, u.title = entry{}, entry{}
	default:
		u.title.text, u.title.cur = lineEdit(u.title.text, u.title.cur, k)
	}
}

// efforts are the cli's --effort levels, lowest first, with the colour Claude
// Code's own scale picks each one out in.
var efforts = []struct{ name, sgr string }{
	{"low", "33"}, {"medium", "32"}, {"high", "34"}, {"xhigh", "35"}, {"max", "1;31"},
}

// effortScale is Claude Code's effort slider, drawn as its /model draws it: 42
// columns, the ▲ under each level at these offsets, and the levels spaced
// out beneath. Nothing is chosen at -1, the harness's default.
const effortScale = "low     medium     high     xhigh      max"

var effortMarks = []int{1, 10, 20, 30, 40}

// askEffort feeds one keystroke to the effort scale: the arrows move along it
// — the first press from the default lands on high, the middle — backspace goes
// back to the default, and ↵, tab or ↓ go on to the title.
func (u *ui) askEffort(k string) {
	i := slices.IndexFunc(efforts, func(e struct{ name, sgr string }) bool { return e.name == u.effort })
	switch k {
	case "\x1b[D", "\x1b[C", "h", "l":
		switch {
		case i < 0:
			i = 2
		case k == "\x1b[D" || k == "h":
			i = max(i-1, 0)
		default:
			i = min(i+1, len(efforts)-1)
		}
		u.effort = efforts[i].name
	case "\x7f", "\b":
		u.effort = ""
	case "\r", "\t", "\x1b[B", "\x1b[27;2;13~":
		u.effortFocus, u.title.open = false, true
	case "\x1b", "\x03":
		u.entry, u.title, u.effortFocus = entry{}, entry{}, false
	}
}

// clickEffort is a click on the effort box: it takes the keys, and a click on
// the track or the levels picks the level nearest it.
func (u *ui) clickEffort(k string) {
	u.effortFocus, u.title.open = true, false
	col, row := mouseAt(k)
	if row-u.effortRow < 2 {
		return
	}
	best := 0
	for i, m := range effortMarks {
		if abs(col-u.effortCol-m) < abs(col-u.effortCol-effortMarks[best]) {
			best = i
		}
	}
	u.effort = efforts[best].name
}

func abs(n int) int { return max(n, -n) }

// effortPane is the effort box: Faster to Smarter, the track with the ▲ on the
// chosen level, the levels under it, dim but for that one. With the keys, the
// chosen level is inverted, the caret the other boxes show.
func (u *ui) effortPane(pad string, inner int) []string {
	lead := max((inner-len(effortScale))/2, 1)
	u.effortCol = len(pad) + 2 + lead // +1 past the │, +1: a mouse counts from 1
	row := paneRow(pad, inner)
	sp := strings.Repeat(" ", lead)
	i := slices.IndexFunc(efforts, func(e struct{ name, sgr string }) bool { return e.name == u.effort })
	track := []rune(strings.Repeat("─", len(effortScale)))
	if i >= 0 {
		track[effortMarks[i]] = '▲'
	}
	head := row(sp + "Faster" + strings.Repeat(" ", len(effortScale)-13) + "Smarter")
	line := row(sp + string(track))
	labels := row(sp + effortScale)
	// The colours go in after the padding is counted: paneRow counts runes, and
	// an escape is runes that take no room.
	at := len([]rune(pad)) + 1 + lead
	// A box narrower than the scale cuts it off: so does the paint.
	end := len([]rune(pad)) + 1 + inner
	paint := func(l string, from, to int, sgr string) string {
		r := []rune(l)
		from, to = min(from, end), min(to, end)
		return string(r[:from]) + "\x1b[" + sgr + "m" + string(r[from:to]) + "\x1b[0m" + string(r[to:])
	}
	if i >= 0 {
		m := at + effortMarks[i]
		line = paint(paint(line, m+1, at+len(effortScale), "2"), m, m+1, "1")
		line = paint(line, at, m, "2")
		from := at + strings.Index(effortScale, efforts[i].name)
		to := from + len(efforts[i].name)
		sgr := efforts[i].sgr
		if u.effortFocus {
			sgr += ";7"
		}
		labels = paint(paint(paint(labels, to, at+len(effortScale), "2"), from, to, sgr), at, from, "2")
	} else {
		line = paint(line, at, at+len(effortScale), "2")
		labels = paint(labels, at, at+len(effortScale), "2")
	}
	title := "effort"
	if u.effortFocus {
		title = "effort ← →"
	}
	return paneBox(pad, inner, title, []string{head, line, labels})
}

// seedPane is the seed prompt's box — or the queue prompt's — with its title
// line in a box of its own above it, and in the ws view the effort scale above
// that. Only the box taking the keys shows a caret.
func (u *ui) seedPane(cols, rows int) []string {
	cur := u.entry.cur
	if u.title.open || u.effortFocus {
		cur = -1
	}
	above := 3
	if u.ws {
		above += 5
	}
	pane := entryPane(u.entry.text, cur, u.entryTitle(), cols, max(rows-above, 1))
	w := min(max(cols*3/5, 24), cols)
	inner := max(w-2, 1)
	pad := strings.Repeat(" ", max(cols-w, 0)/2)
	// One line of it, the one with the caret on: a title is a few words.
	lines, at, col := wrapEdit(u.title.text, max(inner-2, 1), u.title.cur)
	line := paneRow(pad, inner)(" " + lines[at])
	if u.title.open {
		line = invert(line, len(pad)+2+col)
	}
	top := 0
	for top < len(pane) && pane[top] == "" {
		top++
	}
	var effort []string
	u.effortRow = 0
	if u.ws {
		effort = u.effortPane(pad, inner)
		u.effortRow = top + 1
	}
	u.titleRow = top + len(effort) + 1 // +1: a mouse counts from 1
	return slices.Concat(pane[:top], effort, paneBox(pad, inner, "title", []string{line}), pane[top:])
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
	cwd, err := os.Getwd()
	if err != nil {
		return "start failed: " + err.Error()
	}
	name := strings.Join(strings.Fields(u.title.text), " ")
	if err := seed(os.Getenv("HERDR_WORKSPACE_ID"), cwd, u.modelName, u.effort, name, u.entry.expanded()); err != nil {
		return "start failed: " + err.Error()
	}
	u.entry, u.title = entry{}, entry{}
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

// seed starts an agent on prompt in a tab of its own: a tab of workspace ws in
// cwd — the ws view's own, or the one a "<new>" row has just opened — with
// task_command's cli run there on the prompt.
//
// The worktree is the workspace's, not the tab's: the agents of one workspace
// are working on one thing, and a `c` from the list has already made them a
// worktree to work on it in. The tab goes unlabelled for the same reason — it
// would be the same name every time, and herdr names an unlabelled tab after
// what runs in it, which is the agent saying what it is doing.
//
// The title line, when anything is typed in it, names the session (the cli's
// --name) and the workspace, so the agent reads the same in herdr as
// in its own resume list. The label is cosmetic: a rename herdr refuses
// doesn't stop the agent starting.
//
// The model is whatever ctrl-o last set, if anything, appended as a flag rather than woven
// into task_command: the command is the config's, the same for every agent, and
// the model is this one's. The effort the scale was left on goes the same way.
//
// The pane comes out of the create's own answer: with every tab in the one
// directory, there is nothing else that tells the new pane from the tui's own.
func seed(ws, cwd, model, effort, name, prompt string) error {
	argv, err := taskCommand()
	if err != nil {
		return err
	}
	if model != "" {
		argv = append(argv, "--model", shellQuote(model))
	}
	if effort != "" {
		argv = append(argv, "--effort", effort)
	}
	if name != "" {
		argv = append(argv, "--name", shellQuote(name))
	}
	out, err := exec.Command(herdrBin(), "tab", "create", "--workspace", ws,
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
	if name != "" {
		_ = exec.Command(herdrBin(), "workspace", "rename", ws, name).Run()
	}
	arg, err := promptArg(prompt)
	if err != nil {
		return err
	}
	// `--` before it, or a prompt that opens on a dash — a list pasted in, say
	// — is read by the cli as an option it doesn't know.
	if out, err := exec.Command(herdrBin(), append([]string{"pane", "run", pane}, append(argv, "--", arg)...)...).CombinedOutput(); err != nil {
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
// has no ccwt running in it, by name, as watchWsDown last found them — or with
// -g, the projects' own workspaces with no ccwt in any of their panes — and,
// line for line, the first tab of each, which is where a double click on one goes.
var wsDown struct {
	sync.Mutex
	names, tabs []string
	global      bool
}

func wsDownNames() ([]string, bool) {
	wsDown.Lock()
	defer wsDown.Unlock()
	return wsDown.names, wsDown.global
}

// wsDownTab is the first tab of the workspace on line i of the corner's box,
// or "" when there is no such line.
func wsDownTab(i int) string {
	wsDown.Lock()
	defer wsDown.Unlock()
	if i < 0 || i >= len(wsDown.tabs) {
		return ""
	}
	return wsDown.tabs[i]
}

// wsDownEvents are what herdr says when the `ws` tabs, or the names of the
// workspaces they are in, may have changed.
var wsDownEvents = []string{"tab.created", "tab.renamed", "tab.closed", "workspace.renamed", "workspace.closed"}

// wsEvents are what herdr says when what the ws view shows — the tabs, their
// panes, the agents in them — may have changed. An agent's status comes as a
// pane.updated: pane.agent_status_changed is one pane's, by its id, and herdr
// refuses a subscription to it without one (herdr 0.9.5). Output in a pane
// says nothing.
var wsEvents = []string{
	"tab.created", "tab.renamed", "tab.closed", "tab.moved", "tab.focused",
	"pane.created", "pane.updated", "pane.closed", "pane.agent_detected",
	"workspace.renamed",
}

// wsSettleHeard is how long the ws view waits after an event before it asks:
// an agent at work rewrites its pane's title over and over, and a burst of
// those is one change to read.
const wsSettleHeard = 500 * time.Millisecond

// wsHeard is which of herdr's events the ws view of workspace ws re-reads for:
// anything in that workspace, and a tab taking the focus anywhere — which is
// how one of ours loses it, with nothing said about ours (herdr 0.9.5). The
// line saying the subscription has started counts, and so does a line that
// doesn't say where it happened.
func wsHeard(ws string) func(line []byte) bool {
	return func(line []byte) bool {
		var e struct {
			Event string `json:"event"`
			Data  struct {
				Workspace string `json:"workspace_id"`
				Tab       struct {
					Workspace string `json:"workspace_id"`
				} `json:"tab"`
				Pane struct {
					Workspace string `json:"workspace_id"`
				} `json:"pane"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		w := cmp.Or(e.Data.Workspace, e.Data.Tab.Workspace, e.Data.Pane.Workspace)
		return e.Event == "" || e.Event == "tab_focused" || w == "" || w == ws
	}
}

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

// watchWsDown keeps wsDown up to date for as long as ctx lasts, for the
// worktrees of the repos at roots — or, global, for the repos' own workspaces:
// every wsDownEvery, and, listen, when herdr says the tabs have moved.
func watchWsDown(ctx context.Context, roots []string, global, listen bool) {
	poke := make(chan struct{}, 1)
	if listen {
		go herdrListen(ctx, poke, nil, wsDownEvents...)
	}
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
		if herdrPaused() {
			continue // the box goes on saying what it said
		}
		names, tabs := herdrWsDown(roots, global)
		wsDown.Lock()
		wsDown.names, wsDown.tabs, wsDown.global = names, tabs, global
		wsDown.Unlock()
	}
}

// herdrWsDown is the workspaces with a `ws` tab that has no ccwt running in
// it, by name, in the order herdr's sidebar lists them: a conductor that quit,
// or never came back after herdr did — herdr restores a tab's name but not what
// was running in it. The name is what says a ccwt belongs there, since it is
// the one `c` gives the tab it starts `ccwt ws` in. What is running is what
// herdr says is in the foreground of the tab's panes. Alongside the names, the
// first tab of each workspace — the first herdr lists, the one leading its bar.
//
// Only the workspaces open on a worktree of the repos at roots: the list's own,
// the ones herdr's sidebar shows under the repo. Another repo's are for its own
// list to name, and their panes go unasked.
//
// Global, it is the workspaces open on the repos at roots themselves — where
// the -g list's tui lives — with no ccwt in any pane of any tab.
//
// A pane herdr can't say that about counts as having one, and a herdr that
// won't answer at all as having no such tabs: this is a warning, and one that
// isn't sure is one to leave out.
func herdrWsDown(roots []string, global bool) (names, tabs []string) {
	s, ok := herdrSnap()
	if !ok {
		return nil, nil
	}
	ours := map[string]bool{} // a workspace -> it is open on one of our worktrees, or global, one of our repos
	for _, w := range s.Workspaces {
		if global {
			ours[w.ID] = slices.Contains(roots, filepath.Clean(w.Worktree.Path))
		} else {
			root, ok := gitutil.ClaudeWorktreeRepoRoot(w.Worktree.Path)
			ours[w.ID] = ok && slices.Contains(roots, root)
		}
	}
	unit := map[string]string{}  // a tab -> what a ccwt should be running in: the `ws` tab itself, or global, its whole workspace
	first := map[string]string{} // a workspace -> its first tab
	for _, t := range s.Tabs {
		if _, ok := first[t.Workspace]; !ok {
			first[t.Workspace] = t.ID
		}
		switch {
		case !ours[t.Workspace]:
		case global:
			unit[t.ID] = t.Workspace
		case t.Label == "ws":
			unit[t.ID] = t.ID
		}
	}
	up := map[string]bool{} // a unit -> a ccwt is running in it
	for _, u := range unit {
		up[u] = false
	}
	for _, p := range s.Panes {
		if u, ok := unit[p.Tab]; ok && !up[u] {
			up[u] = herdrRunsCcwt(p.ID)
		}
	}
	down := map[string]bool{}
	for _, t := range s.Tabs {
		if u, ok := unit[t.ID]; ok && !up[u] {
			down[t.Workspace] = true
		}
	}
	for _, w := range s.Workspaces {
		if down[w.ID] {
			names, tabs = append(names, w.Label), append(tabs, first[w.ID])
		}
	}
	return names, tabs
}

// herdrSnapshot is the part of herdr's `api snapshot` the corner looks at: the
// workspaces, tabs and panes, each in the order herdr lists them.
type herdrSnapshot struct {
	Workspaces []struct {
		ID       string `json:"workspace_id"`
		Label    string `json:"label"`
		Worktree struct {
			Path string `json:"checkout_path"`
		} `json:"worktree"`
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
}

func herdrSnap() (herdrSnapshot, bool) {
	var snap struct {
		Result struct {
			Snapshot herdrSnapshot `json:"snapshot"`
		} `json:"result"`
	}
	out, err := herdrAsk("session.snapshot", nil, "api", "snapshot")
	if err != nil || json.Unmarshal(out, &snap) != nil {
		return herdrSnapshot{}, false
	}
	return snap.Result.Snapshot, true
}

// paneProcs is what herdr says is in the foreground of a pane: the processes by
// name, and whether that is the pane's shell itself, waiting at its prompt.
type paneProcs struct {
	names []string
	shell bool
}

func herdrProcs(pane string) (paneProcs, bool) {
	out, err := herdrAsk("pane.process_info", map[string]any{"pane_id": pane}, "pane", "process-info", "--pane", pane)
	var resp struct {
		Result struct {
			Info struct {
				Group      int `json:"foreground_process_group_id"`
				Shell      int `json:"shell_pid"`
				Foreground []struct {
					Name string `json:"name"`
				} `json:"foreground_processes"`
			} `json:"process_info"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(out, &resp) != nil {
		return paneProcs{}, false
	}
	i := resp.Result.Info
	p := paneProcs{shell: i.Shell != 0 && i.Group == i.Shell}
	for _, f := range i.Foreground {
		p.names = append(p.names, f.Name)
	}
	return p, true
}

// herdrRunsCcwt reports whether a ccwt is in the foreground of a pane, or
// herdr can't say.
func herdrRunsCcwt(pane string) bool {
	p, ok := herdrProcs(pane)
	return !ok || slices.Contains(p.names, "ccwt")
}

// restartStep is what the corner's ↻ does for one of the workspaces in its box:
// start the tui again in the tab it belongs in — `prj` for a project's own
// workspace under -g, `ws` for a worktree's — making that tab, first in the
// bar, when there is none. skip says why a workspace is being left alone.
type restartStep struct {
	name, ws, cwd string
	tab, pane     string // "" when the tab is to be made
	skip          string
}

// restartTab is the tab the tui is restarted in, and the arguments it is run
// with there.
func restartTab(global bool) (string, []string) {
	if global {
		return "prj", nil
	}
	return "ws", []string{"ws"}
}

// restartPlan is what ↻ would do, from a fresh look at herdr, for the
// workspaces the corner last named.
func restartPlan() ([]restartStep, bool) {
	wsDown.Lock()
	firsts, global := slices.Clone(wsDown.tabs), wsDown.global
	wsDown.Unlock()
	label, _ := restartTab(global)
	s, ok := herdrSnap()
	if !ok {
		return nil, global
	}
	down := map[string]bool{} // a workspace -> the corner named it, by its first tab
	for _, t := range s.Tabs {
		if slices.Contains(firsts, t.ID) {
			down[t.Workspace] = true
		}
	}
	var steps []restartStep
	for _, w := range s.Workspaces {
		if !down[w.ID] {
			continue
		}
		st := restartStep{name: w.Label, ws: w.ID, cwd: w.Worktree.Path}
		for _, t := range s.Tabs {
			if t.Workspace == w.ID && t.Label == label {
				st.tab = t.ID
				break
			}
		}
		if st.tab != "" {
			for _, p := range s.Panes {
				if p.Tab == st.tab {
					st.pane = p.ID
					break
				}
			}
			switch procs, ok := herdrProcs(st.pane); {
			case st.pane == "" || !ok:
				st.skip = "herdr can't say what its `" + label + "` tab is running"
			case !procs.shell:
				st.skip = "its `" + label + "` tab is running " + strings.Join(procs.names, ", ")
			}
		}
		steps = append(steps, st)
	}
	return steps, global
}

// restartText is the plan as the confirmation pane shows it.
func restartText(steps []restartStep, global bool) []string {
	label, args := restartTab(global)
	cmd := strings.Join(append([]string{"ccwt"}, args...), " ")
	if len(steps) == 0 {
		return []string{"Nothing to restart: herdr has none of these workspaces any more."}
	}
	lines := []string{"↻ starts `" + cmd + "` again in these workspaces:", ""}
	for _, st := range steps {
		switch {
		case st.skip != "":
			lines = append(lines, "  "+st.name+": left alone, "+st.skip)
		case st.tab == "":
			lines = append(lines, "  "+st.name+": a new `"+label+"` tab, first in the bar, running "+cmd)
		default:
			lines = append(lines, "  "+st.name+": "+cmd+" in its `"+label+"` tab, at the shell there")
		}
	}
	if !slices.ContainsFunc(steps, func(s restartStep) bool { return s.skip == "" }) {
		return append(lines, "", "Nothing here to do: esc to close.")
	}
	return append(lines, "", "y to go ahead, esc to leave them be.")
}

// restart carries the plan out, and says for the bar how it went.
func restart(steps []restartStep, global bool) string {
	exe, err := os.Executable()
	if err != nil {
		return "restart failed: " + err.Error()
	}
	label, args := restartTab(global)
	n := 0
	for _, st := range steps {
		if st.skip != "" {
			continue
		}
		pane := st.pane
		if st.tab == "" {
			out, err := herdrAsk("tab.create", map[string]any{"workspace_id": st.ws, "cwd": st.cwd, "label": label, "focus": false},
				"tab", "create", "--workspace", st.ws, "--cwd", st.cwd, "--label", label, "--no-focus")
			var resp struct {
				Result struct {
					Tab struct {
						ID string `json:"tab_id"`
					} `json:"tab"`
					Pane struct {
						ID string `json:"pane_id"`
					} `json:"root_pane"`
				} `json:"result"`
			}
			if err != nil || json.Unmarshal(out, &resp) != nil || resp.Result.Pane.ID == "" {
				return fmt.Sprintf("restart failed in %s: herdr tab create: %v", st.name, err)
			}
			pane = resp.Result.Pane.ID
			// Only over the socket: the cli has no `tab move`. Best effort: a
			// tab in the wrong place still has its tui.
			_, _ = herdrAsk("tab.move", map[string]any{"tab_id": resp.Result.Tab.ID, "insert_index": 0}, "tab", "move", resp.Result.Tab.ID, "0")
		}
		if out, err := exec.Command(herdrBin(), append([]string{"pane", "run", pane, exe}, args...)...).CombinedOutput(); err != nil {
			return "restart failed in " + st.name + ": herdr pane run: " + lastLine(out, err)
		}
		n++
	}
	return fmt.Sprintf("restarted %d of %d", n, len(steps))
}

// wsDownPane is the corner's box: the workspaces herdrWsDown named, one a line,
// under a rule that says what they are missing. As wide as its widest line, as
// the menu is, and as tall as rows leaves room for; nil when there is nothing
// to say, or nowhere to say it. The title ends in a ↻ to click, for starting
// them again.
func wsDownPane(names []string, global bool, cols, rows int) []string {
	title := "ccwt ws not running ↻"
	if global {
		title = "ccwt tui not running ↻"
	}
	if len(names) == 0 || rows < 3 {
		return nil
	}
	inner := len([]rune(title)) + 4 // the rule either side of it
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

// todoPicker is `T`: `todography next`, the map's wanted work whose needs are
// met, in a pane over the table — one row per item, the milestone headings left
// out. `c` on a row opens the seed prompt on its url, for an agent to be started
// on once there's more said about it — or, outside the ws view, the queue
// prompt, hanging off parent as `n`'s would. `/` narrows the rows to those
// holding what's typed after it, as the opener's filter does.
//
// A snapshot, like the opener's: what was next when the pane came up.
type todoPicker struct {
	rows     []string // todography's lines, the url first on each
	parent   listRow
	filter   string // what `/` has been given: a substring of the line, case ignored
	typing   bool   // `/` is taking keystrokes, until ↵ or esc
	sel, top int    // into shown()
	first, n int    // the screen line the first row came out on, and how many were drawn
}

// newTodoPicker runs `todography next` in dir, the project's checkout, which is
// where todography looks for its map.
func newTodoPicker(dir string, parent listRow) (*todoPicker, error) {
	cmd := exec.Command("todography", "next")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.New(lastLine(out, err))
	}
	return &todoPicker{rows: todoRows(string(out)), parent: parent}, nil
}

// todoRows is the items of todography's listing: the indented lines, each
// starting with its t8y: url.
func todoRows(out string) []string {
	var rows []string
	for l := range strings.Lines(out) {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "t8y:") {
			rows = append(rows, l)
		}
	}
	return rows
}

// shown is what the filter leaves of the rows.
func (t *todoPicker) shown() []string {
	f := strings.ToLower(t.filter)
	var s []string
	for _, r := range t.rows {
		if strings.Contains(strings.ToLower(r), f) {
			s = append(s, r)
		}
	}
	return s
}

// key is the picker's turn at a keystroke: "close", or "seed" and the url of
// the row `c` was pressed on. While `/` is typing, letters go to the filter, ↵
// hands the keys back and esc drops the filter; otherwise esc drops the filter
// first and closes after.
func (t *todoPicker) key(k string) (string, string) {
	shown := t.shown()
	switch {
	case k == "\x1b[A", !t.typing && k == "k":
		t.sel = max(t.sel-1, 0)
	case k == "\x1b[B", !t.typing && k == "j":
		t.sel = min(t.sel+1, max(len(shown)-1, 0))
	case k == "\x1b" && (t.typing || t.filter != ""):
		t.filter, t.typing, t.sel = "", false, 0
	case t.typing && (k == "\r" || k == "\n"):
		t.typing = false
	case t.typing && mouseRow(k) == 0:
		if f := typeKey(t.filter, k); f != t.filter {
			t.filter, t.sel = f, 0
		}
	case k == "\x1b", k == "T":
		return "close", ""
	case k == "/":
		t.typing = true
	case k == "c":
		if t.sel < len(shown) {
			return "seed", strings.Fields(shown[t.sel])[0]
		}
	default:
		if i := mouseRow(k) - t.first; i >= 0 && i < t.n {
			t.sel = t.top + i
		}
	}
	return "", ""
}

// pane draws the picker as the opener is drawn, the filter on a line of its own
// above the rows once there is one, and says what the bar under it is to read.
func (t *todoPicker) pane(cols, rows int) ([]string, string) {
	pad, inner := paneWidth(cols)
	row := paneRow(pad, inner)
	shown := t.shown()
	var body []string
	if t.typing || t.filter != "" {
		body = append(body, row(" / "+t.filter))
	}
	head := len(body)
	fits := max(rows-2-head, 1) // the two rules, and the filter
	t.sel = min(max(t.sel, 0), max(len(shown)-1, 0))
	t.top = max(min(max(t.top, t.sel-fits+1), t.sel), 0)
	t.n = min(fits, len(shown)-t.top)
	for i, r := range shown[t.top : t.top+t.n] {
		l := row(" " + r)
		if t.top+i == t.sel {
			l = band(l)
		}
		body = append(body, l)
	}
	switch {
	case len(t.rows) == 0:
		body = append(body, row(" nothing next"))
	case len(shown) == 0:
		body = append(body, row(" nothing matches"))
	}
	lines := paneBox(pad, inner, "todography next", body)
	margin := min(2, max(rows-len(lines), 0)/2)
	t.first = margin + 2 + head // the margin, the top rule and the filter, and a mouse counts from 1
	keys := " ↑↓:select  c:agent  /:filter  esc:close "
	switch {
	case t.typing:
		keys = " type:filter  ↵:done  esc:clear "
	case t.filter != "":
		keys = " ↑↓:select  c:agent  /:filter  esc:clear "
	}
	return append(make([]string, margin), lines...), keys
}

// seedTodo is `c` in the todo picker: the seed prompt — or the queue prompt,
// hanging off parent — open on url with the title the url less its scheme, and
// the caret at the end of the prompt. ↵ is still to come, so there's room to say
// more first.
func (u *ui) seedTodo(parent listRow, url string) {
	u.entry, u.title = newEntry(parent, url, 0), newEntry(listRow{}, strings.TrimPrefix(url, "t8y:"), 0)
	u.title.open = false
}
