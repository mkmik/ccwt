package main

import (
	"cmp"
	"context"
	"os"
	"slices"
)

// mux is the terminal workspace manager ccwt drives: what opens a workspace on
// a worktree, keeps its tabs and the terminals in them, and says which agents
// are at work where. herdr is one (herdr.go); dennet, with dense drawing it, is
// to be the other (see TODO.md).
//
// The words are ccwt's, and each mux maps them onto its own: a workspace is a
// herdr workspace or a dennet Space, a tab a herdr tab or a Surface, a pane a
// herdr pane or a Den. Ids are whatever the mux calls things, and go back to
// the same mux unread.
//
// Everything ccwt says to one goes through here, so which one it talks to is
// mx's choice and nobody else's.
type mux interface {
	// inside is whether ccwt runs in one of the mux's terminals, which is
	// where there is a session to open workspaces in: outside one, the actions
	// that would are hidden rather than left to fail on every press.
	inside() bool
	// here is the workspace and tab ccwt runs in, "" outside.
	here() (ws, tab string)
	// listens is whether listen can hear anything here.
	listens() bool
	// listen pokes, for as long as ctx lasts, whenever what the ws view of
	// workspace ws shows may have changed — or, ws "", whenever any workspace's
	// tabs or names may have. A poke already waiting stands for the ones after
	// it.
	listen(ctx context.Context, poke chan<- struct{}, ws string)

	// agents is every agent the mux knows of, in every workspace. poll is for
	// a view, which a pause may answer from what it last heard; an action asks.
	agents(poll bool) []muxAgent
	// tabs is workspace ws's tabs in the order they sit along its tab bar, each
	// with what its agents are doing, before wsAttention has had its say.
	tabs(ws string) ([]wsTab, error)
	// sidebar is the names of the workspaces open on worktrees, by path; their
	// places in the sidebar, by path and by repo root; and the places of the
	// dividers between them.
	sidebar() (labels map[string]string, order map[string]int, seps []int)
	// workspaces is every workspace, in sidebar order.
	workspaces() ([]muxWorkspace, error)
	// worktreeWorkspaces is the workspaces open on the worktree at path.
	worktreeWorkspaces(root, path string) ([]string, error)
	// repoWorkspaces is the first workspace on each repo, by the repo's root.
	repoWorkspaces() map[string]string
	// focused is whether workspace ws is the one on screen; no when unsure.
	focused(ws string) bool
	// paneAt is the pane sitting in the worktree at path and its workspace,
	// "" when there is none — waiting a moment for one an open has just made.
	paneAt(path string) (pane, ws string)
	// paneTab is the tab a pane is in.
	paneTab(pane string) (string, error)
	// place is a tab's workspace name, its own name and its address.
	place(tab string) (place, error)
	// snapshot is every workspace, tab and pane, in the mux's order.
	snapshot() (muxSnapshot, bool)
	// procs is what is in the foreground of a pane.
	procs(pane string) (paneProcs, bool)

	// openWorktree opens the worktree at path, of the repo at root, as a
	// workspace of its own, named label when that is not "".
	openWorktree(root, path, label string) error
	// openProject opens a workspace on the repo at path itself.
	openProject(path string) (muxOpened, error)
	// newTab makes a tab in workspace ws, starting in cwd, named label unless
	// that is "", and leaves the focus where it was.
	newTab(ws, cwd, label string) (tab, pane string, err error)
	// run types words, joined by spaces, at the shell in pane, and Enter.
	run(pane string, words ...string) error
	renameTab(tab, name string) error
	moveTab(tab string, at int) error
	focusTab(tab string) error
	renameWorkspace(ws, name string) error
	closeWorkspace(ws string) error
	focusWorkspace(ws string) error
	// file moves workspace ws into category cat's part of the sidebar.
	file(ws, cat string)
	// badge shows glyph on workspace ws for a while, or clears it when "".
	badge(ws, glyph string)
	// popup opens the project picker over the focused pane, with env set.
	popup(env map[string]string) error
}

// mx is the mux this ccwt talks to: dennet in a den, herdr anywhere else —
// herdr in a den too, since the innermost of the two is the one ccwt is in,
// and herdr's cli outside both, as ever. Chosen afresh each time, from the
// environment, so a test can change its mind with t.Setenv.
func mx() mux {
	if os.Getenv("HERDR_ENV") == "" && os.Getenv("DEN_SOCKET") != "" {
		return dennetMux{}
	}
	return herdrMux{bin: herdrBin()}
}

// muxAgent is one agent: where it is — its pane, tab and workspace, the pane's
// cwd and its own — what it is doing, the mux's counter for when that last
// changed, and whether its pane is the one you are in. The tags are herdr's
// `agent list`'s, which decodes straight into it.
type muxAgent struct {
	Status     string `json:"agent_status"`
	Seq        int64  `json:"state_change_seq"`
	Cwd        string `json:"cwd"`
	Foreground string `json:"foreground_cwd"`
	Workspace  string `json:"workspace_id"`
	Tab        string `json:"tab_id"`
	Pane       string `json:"pane_id"`
	Focused    bool   `json:"focused"`
}

// muxWorkspace is one workspace: its id, its name in the sidebar, the
// checkout it is open on ("" for none), the repo that checkout is of, and the
// tab it has up front.
type muxWorkspace struct {
	ID, Label, Path, Repo, Tab string
}

// muxTab and muxPane are a tab and a pane as snapshot has them.
type (
	muxTab  struct{ ID, Workspace, Label string }
	muxPane struct{ ID, Tab string }
)

// muxSnapshot is the part of everything the mux has that the corner looks at:
// the workspaces, tabs and panes, each in the mux's order.
type muxSnapshot struct {
	Workspaces []muxWorkspace
	Tabs       []muxTab
	Panes      []muxPane
}

// muxOpened is what an open made, or found already there.
type muxOpened struct {
	ws, tab, pane string
	already       bool
}

// busyCwds is the set of cwds an agent is mid-task in: "working", or
// "blocked" on a question it is waiting for an answer to. Both mean live work
// that no git check can see — a branch made a minute ago, nothing committed to
// it yet, looks pristine right up until the removal pulls the floor out from
// under the agent. Asking the mux rather than the agent means it holds for
// whatever the mux is running there, not just Claude Code.
//
// Our own tab doesn't count: when the agent itself runs `ccwt done`, it is the
// working agent, and counting it would leave it unable to clean up after
// itself. The tab rather than the whole workspace, because a workspace can hold
// several — `ccwt ws` is a tui in one tab of a workspace with agents in the
// others — and an agent working a tab along is precisely what a removal must
// not pull the floor out from under. For a worktree open as its own single-tab
// workspace the two are the same thing. A pane whose environment names no tab
// falls back to the workspace. No mux, or none running, is nobody working.
func busyCwds(agents []muxAgent) map[string]bool {
	busy := map[string]bool{}
	// A tab id ("w1:t2") is never a workspace id ("w1"), so one comparison does
	// for whichever of the two we were handed.
	ws, tab := mx().here()
	self := cmp.Or(tab, ws)
	for _, a := range agents {
		if a.Status != "working" && a.Status != "blocked" {
			continue
		}
		if self != "" && (a.Tab == self || a.Workspace == self) {
			continue
		}
		// Both cwds: an agent started at the repo root and cd'd into a worktree
		// is only in the pane's cwd as the foreground process's.
		busy[a.Cwd], busy[a.Foreground] = true, true
	}
	delete(busy, "")
	return busy
}

// sidebarFirst puts what the sidebar has a place for ahead of what it hasn't,
// in its order, and says nothing about two paths it has no place for.
func sidebarFirst(order map[string]int, a, b string) int {
	i, aok := order[a]
	j, bok := order[b]
	switch {
	case aok && bok:
		return cmp.Compare(i, j)
	case aok:
		return -1
	case bok:
		return 1
	}
	return 0
}

// paneProcs is what is in the foreground of a pane: the processes by name,
// and whether that is the pane's shell itself, waiting at its prompt.
type paneProcs struct {
	names []string
	shell bool
}

// runsCcwt reports whether a ccwt is in the foreground of a pane, or the mux
// can't say.
func runsCcwt(pane string) bool {
	p, ok := mx().procs(pane)
	return !ok || slices.Contains(p.names, "ccwt")
}
