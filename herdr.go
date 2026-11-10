package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// herdrMux is herdr as ccwt's mux: its cli, or under herdr the socket herdr
// names in every pane it starts. The cli it runs is bin, the one there was
// when it was made, so that a rename put off for a second goes to the herdr
// that was asked for rather than whichever the environment names by then.
type herdrMux struct{ bin string }

// inside: herdr exports HERDR_ENV into every pane it spawns. Outside one there
// is no session for `herdr worktree open` to put a workspace in.
func (herdrMux) inside() bool { return underHerdr() }

func underHerdr() bool { return os.Getenv("HERDR_ENV") != "" }

func (herdrMux) here() (ws, tab string) {
	return os.Getenv("HERDR_WORKSPACE_ID"), os.Getenv("HERDR_TAB_ID")
}

func (herdrMux) listens() bool { return os.Getenv("HERDR_SOCKET_PATH") != "" }

func (herdrMux) listen(ctx context.Context, poke chan<- struct{}, ws string) {
	if ws == "" {
		herdrListen(ctx, poke, nil, wsDownEvents...)
		return
	}
	herdrListen(ctx, poke, wsHeard(ws), wsEvents...)
}

func (herdrMux) agents(poll bool) []muxAgent {
	if poll {
		return herdrAgents(herdrPoll)
	}
	return herdrAgents(herdrAsk)
}

func (herdrMux) tabs(ws string) ([]wsTab, error) { return herdrTabs(ws) }

func (herdrMux) sidebar() (map[string]string, map[string]int, []int) { return herdrLabels() }

func (herdrMux) worktreeWorkspaces(root, path string) ([]string, error) {
	return herdrWorkspaces(root, path)
}

func (herdrMux) repoWorkspaces() map[string]string { return herdrRepoWS() }

func (herdrMux) focused(ws string) bool { return herdrFocused(ws) }

func (herdrMux) paneAt(path string) (string, string) { return herdrPane(path) }

func (herdrMux) paneTab(pane string) (string, error) { return herdrPaneTab(pane) }

func (herdrMux) place(tab string) (place, error) { return herdrPlace(tab) }

func (herdrMux) snapshot() (muxSnapshot, bool) { return herdrSnap() }

func (herdrMux) procs(pane string) (paneProcs, bool) { return herdrProcs(pane) }

func (herdrMux) closeWorkspace(ws string) error { return herdrCloseWS(ws) }

func (herdrMux) focusWorkspace(ws string) error { return herdrFocusWS(ws) }

func (herdrMux) file(ws, cat string) { herdrFile(ws, cat) }

// workspaces is herdr's `workspace list`, from the cli whatever the
// environment: nav ws is a command typed once, not a round.
func (m herdrMux) workspaces() ([]muxWorkspace, error) {
	out, err := exec.Command(m.bin, "workspace", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr workspace list: %w", err)
	}
	var resp struct {
		Result struct {
			Workspaces []struct {
				ID       string `json:"workspace_id"`
				Label    string `json:"label"`
				Tab      string `json:"active_tab_id"`
				Worktree struct {
					Path string `json:"checkout_path"`
					Repo string `json:"repo_root"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("herdr workspace list: %w", err)
	}
	var ws []muxWorkspace
	for _, w := range resp.Result.Workspaces {
		ws = append(ws, muxWorkspace{ID: w.ID, Label: w.Label, Path: w.Worktree.Path, Repo: w.Worktree.Repo, Tab: w.Tab})
	}
	return ws, nil
}

// openWorktree is what the herdr plugin's new-worktree action does — including
// handing herdr the enclosing repo root as --cwd, since herdr refuses to spawn
// a workspace from inside a linked worktree.
func (m herdrMux) openWorktree(root, path, label string) error {
	args := []string{"worktree", "open", "--cwd", root, "--path", path, "--focus"}
	if label != "" {
		args = append(args, "--label", label)
	}
	if out, err := exec.Command(m.bin, args...).CombinedOutput(); err != nil {
		return errors.New(lastLine(out, err))
	}
	return nil
}

func (herdrMux) openProject(path string) (muxOpened, error) {
	out, err := herdrAsk("worktree.open", map[string]any{"cwd": path, "path": path, "focus": true},
		"worktree", "open", "--cwd", path, "--path", path, "--focus")
	var resp struct {
		Result struct {
			AlreadyOpen bool `json:"already_open"`
			Workspace   struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			Tab struct {
				ID string `json:"tab_id"`
			} `json:"tab"`
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(out, &resp) != nil || resp.Result.Pane.ID == "" {
		return muxOpened{}, fmt.Errorf("herdr worktree open: %v", err)
	}
	r := resp.Result
	return muxOpened{ws: r.Workspace.ID, tab: r.Tab.ID, pane: r.Pane.ID, already: r.AlreadyOpen}, nil
}

// newTab is the tab, and the pane herdr's answer names in it: with every tab
// of a workspace in the one directory, nothing else tells the new pane from
// the others.
func (herdrMux) newTab(ws, cwd, label string) (string, string, error) {
	args := []string{"tab", "create", "--workspace", ws, "--cwd", cwd}
	params := map[string]any{"workspace_id": ws, "cwd": cwd, "focus": false}
	if label != "" {
		args = append(args, "--label", label)
		params["label"] = label
	}
	out, err := herdrAsk("tab.create", params, append(args, "--no-focus")...)
	if err != nil {
		return "", "", fmt.Errorf("herdr tab create: %s", lastLine(out, err))
	}
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
	if json.Unmarshal(out, &resp) != nil || resp.Result.Pane.ID == "" {
		return "", "", errors.New("herdr tab create: no pane in the new tab")
	}
	return resp.Result.Tab.ID, resp.Result.Pane.ID, nil
}

// run is `herdr pane run`, which joins its words with spaces and types the
// line at the pane's shell prompt: the cli's own, with no method on the socket
// to call instead.
func (m herdrMux) run(pane string, words ...string) error {
	if out, err := exec.Command(m.bin, append([]string{"pane", "run", pane}, words...)...).CombinedOutput(); err != nil {
		return errors.New(lastLine(out, err))
	}
	return nil
}

// renameTab and renameWorkspace are best effort, and say nothing: a name
// herdr refuses is cosmetic.
func (m herdrMux) renameTab(tab, name string) error {
	_ = exec.Command(m.bin, "tab", "rename", tab, name).Run()
	return nil
}

func (m herdrMux) renameWorkspace(ws, name string) error {
	_ = exec.Command(m.bin, "workspace", "rename", ws, name).Run()
	return nil
}

// moveTab goes over the socket only: the cli has no `tab move`.
func (herdrMux) moveTab(tab string, at int) error {
	_, err := herdrAsk("tab.move", map[string]any{"tab_id": tab, "insert_index": at}, "tab", "move", tab, fmt.Sprint(at))
	return err
}

func (m herdrMux) focusTab(tab string) error {
	if out, err := exec.Command(m.bin, "tab", "focus", tab).CombinedOutput(); err != nil {
		return errors.New("herdr tab focus: " + lastLine(out, err))
	}
	return nil
}

// badge: herdr has no icon for a client to set, so it goes as display-only
// workspace metadata under an `mr` token, which the spaces panel draws for
// whoever has asked for it in their config:
//
//	[ui.sidebar.spaces]
//	rows = [["state_icon", "workspace", "$mr"], ["branch", "git_status"]]
//
// Nothing tells herdr when this tui stops, so the badge always carries a ttl
// and expires on its own rather than leaving a "✓" on a workspace that has long
// since moved on. A workspace with nothing to say loses the token rather than
// keeping the last thing it said until the ttl runs out.
func (herdrMux) badge(ws, glyph string) {
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

// popup has herdr open the ccwt plugin's `picker` pane as a popup. It fails
// where the plugin isn't linked, or is a version without the pane.
func (herdrMux) popup(env map[string]string) error {
	_, err := herdrAsk("plugin.pane.open",
		map[string]any{"plugin_id": "ccwt", "entrypoint": "picker", "placement": "popup", "focus": true, "env": env},
		"plugin", "pane", "open", "--plugin", "ccwt", "--entrypoint", "picker", "--placement", "popup", "--focus", "--env", "CCWT_BIN="+env["CCWT_BIN"])
	return err
}

// herdrPane is the pane sitting in the worktree at path, which after a
// `worktree open` is the one it just made — where `ccwt ws` is to run — and the
// workspace it is in, which is where a queued prompt's tab goes. "" when herdr
// hasn't got one, which is the same answer as an open that failed, and is
// handled as one.
//
// Retried for a second, since the pane is spawned by the open we have just
// returned from and takes a moment to show up in the list: giving up on the
// first look would drop the prompt on the floor on a loaded machine.
func herdrPane(path string) (pane, ws string) {
	for wait := 50 * time.Millisecond; ; wait *= 2 {
		out, err := exec.Command(herdrBin(), "pane", "list").Output()
		if err == nil {
			var resp struct {
				Result struct {
					Panes []struct {
						ID        string `json:"pane_id"`
						Cwd       string `json:"cwd"`
						Workspace string `json:"workspace_id"`
					} `json:"panes"`
				} `json:"result"`
			}
			if err := json.Unmarshal(out, &resp); err == nil {
				for _, p := range resp.Result.Panes {
					if p.Cwd == path {
						return p.ID, p.Workspace
					}
				}
			}
		}
		if wait > time.Second {
			return "", ""
		}
		time.Sleep(wait)
	}
}

// herdrBin is the herdr to talk to: plugin actions are handed one in
// HERDR_BIN_PATH, everyone else has it on PATH.
func herdrBin() string {
	if bin := os.Getenv("HERDR_BIN_PATH"); bin != "" {
		return bin
	}
	return "herdr"
}

// herdrAsk puts one request to herdr and returns what `herdr <args>` would have
// printed for it: the socket's own {"id":…,"result":…} line, since the cli is a
// client of that socket that prints what it hears back. Under herdr the request
// goes over the socket itself, which herdr names in HERDR_SOCKET_PATH for every
// pane it starts: a tui in each workspace, re-reading its tabs every couple of
// seconds, is otherwise a herdr process started for every one of those
// questions, and a ping on the socket ahead of each. Anywhere else it is the
// cli, which finds a running herdr without being told where.
//
// method and params are the socket's words for the request, args the cli's.
func herdrAsk(method string, params map[string]any, args ...string) ([]byte, error) {
	sock := os.Getenv("HERDR_SOCKET_PATH")
	if sock == "" {
		return exec.Command(herdrBin(), args...).Output()
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	// A request with nothing in it still sends an object, which herdr insists
	// on: v2 writes a nil map as {} where v1 wrote null.
	req, err := jsonv2.Marshal(map[string]any{"id": "ccwt", "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(append(req, '\n')); err != nil {
		return nil, err
	}
	out, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	// A refusal comes back as a line too, which the cli turns into its exit
	// status: this is where it stops passing for an answer.
	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if jsonv2.Unmarshal(out, &resp) == nil && resp.Error != nil {
		return nil, errors.New(resp.Error.Message)
	}
	return out, nil
}

// herdrListen holds a subscription to herdr's events of these types open over
// the socket for as long as ctx lasts, and pokes each time a line comes down
// it that keep, when there is one, keeps: the answer that says the subscription
// has started — whatever happened before it went unheard, so that is worth a
// look too — and then one per event. A poke already waiting stands for the ones after it: whoever is
// listening looks again, and one look sees all of them.
//
// Herdr going away — a restart, `herdr update --handoff` — ends the
// connection, and it is made again every couple of seconds until herdr is back.
// A pause ends it too, a subscription being something herdr keeps up for us,
// and it is made again within a couple of seconds of `ccwt unpause`.
func herdrListen(ctx context.Context, poke chan<- struct{}, keep func(line []byte) bool, types ...string) {
	subs := make([]map[string]any, len(types))
	for i, t := range types {
		subs[i] = map[string]any{"type": t}
	}
	req, _ := jsonv2.Marshal(map[string]any{"id": "ccwt", "method": "events.subscribe", "params": map[string]any{"subscriptions": subs}})
	for ctx.Err() == nil {
		if !herdrPaused() {
			herdrHear(ctx, req, poke, keep)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// herdrHear is one connection's worth of herdrListen: the subscription req
// made, and a poke for every line down it keep keeps, until herdr hangs up, ctx
// ends or ccwt is paused.
func herdrHear(ctx context.Context, req []byte, poke chan<- struct{}, keep func([]byte) bool) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", os.Getenv("HERDR_SOCKET_PATH"))
	if err != nil {
		return
	}
	defer c.Close()
	defer context.AfterFunc(ctx, func() { c.Close() })()
	if _, err := c.Write(append(req, '\n')); err != nil {
		return
	}
	for r := bufio.NewReader(c); ; {
		// A read waiting on herdr's next event comes back every couple of
		// seconds to see whether ccwt has been paused meanwhile.
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadBytes('\n')
		if errors.Is(err, os.ErrDeadlineExceeded) && !herdrPaused() {
			continue
		}
		if err != nil {
			return
		}
		if keep != nil && !keep(line) {
			continue
		}
		select {
		case poke <- struct{}{}:
		default:
		}
	}
}

// herdrAgents is every agent herdr has, in every workspace: `agent list` takes
// no workspace to narrow it to. No herdr, or none running, or one that won't
// say, is no agents. ask is herdrAsk where an action rests on the answer — a
// removal must not go by one a pause has left standing — and herdrPoll where a
// view only shows it.
//
// ponytail: package var so tests can fake the herdr answer.
var herdrAgents = func(ask herdrAsker) []muxAgent {
	var resp struct {
		Result struct {
			Agents []muxAgent `json:"agents"`
		} `json:"result"`
	}
	if out, err := ask("agent.list", nil, "agent", "list"); err == nil {
		_ = json.Unmarshal(out, &resp)
	}
	return resp.Result.Agents
}

// herdrWorkspaces are the ids of the workspaces herdr has open on the worktree
// at path — usually one, none for a worktree nobody is sitting in. Closing one
// is what ends the agent living in it, which is what has to happen before the
// directory it sits in disappears.
//
// ponytail: package var so tests can fake the herdr answer.
var herdrWorkspaces = func(root, path string) ([]string, error) {
	out, err := exec.Command(herdrBin(), "worktree", "list", "--cwd", root).Output()
	if err != nil {
		return nil, fmt.Errorf("herdr worktree list: %w", err)
	}
	var resp struct {
		Result struct {
			Worktrees []struct {
				Path      string `json:"path"`
				Workspace string `json:"open_workspace_id"`
			} `json:"worktrees"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("herdr worktree list: %w", err)
	}
	var ids []string
	for _, wt := range resp.Result.Worktrees {
		if wt.Path == path && wt.Workspace != "" {
			ids = append(ids, wt.Workspace)
		}
	}
	return ids, nil
}

// herdrLabels is what herdr calls the workspaces it has open on worktrees,
// keyed by the worktree's path: the name in its sidebar, which starts out as
// the worktree's own and is usually renamed to what the work is. No herdr, or
// none running, is no names.
//
// order is where each sits in that sidebar, keyed by the same paths, and by
// each repo's root too: the place of the repo's first workspace, main checkout
// or not, which is where its section goes under -g.
//
// seps are the places in it of the workspaces named "------…": nothing's
// workspace, only a divider in the sidebar, which the list draws too.
//
// ponytail: package var so tests can fake the herdr answer.
var herdrLabels = func() (labels map[string]string, order map[string]int, seps []int) {
	out, err := herdrPoll("workspace.list", nil, "workspace", "list")
	if err != nil {
		return nil, nil, nil
	}
	var resp struct {
		Result struct {
			Workspaces []struct {
				Label    string `json:"label"`
				Worktree struct {
					Path string `json:"checkout_path"`
					Repo string `json:"repo_root"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, nil, nil
	}
	labels, order = map[string]string{}, map[string]int{}
	for i, w := range resp.Result.Workspaces {
		if strings.HasPrefix(w.Label, herdrSep) {
			seps = append(seps, i)
			continue
		}
		labels[w.Worktree.Path] = w.Label
		for _, p := range []string{w.Worktree.Path, w.Worktree.Repo} {
			if _, ok := order[p]; !ok && p != "" {
				order[p] = i
			}
		}
	}
	return labels, order, seps
}

// herdrSep is what the name of a workspace that is only a divider starts with.
const herdrSep = "------"

// herdrCloseWS closes one workspace. Closing our own kills this process, so
// callers leave that one for last.
//
// ponytail: package var so tests can fake the herdr answer.
var herdrCloseWS = func(id string) error {
	if out, err := exec.Command(herdrBin(), "workspace", "close", id).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr workspace close %s: %s", id, lastLine(out, err))
	}
	return nil
}

// herdrFocused reports whether the workspace with this id is the one herdr has
// focused — the one on screen. A herdr that can't say is no, which leaves the
// focus alone: the only caller moves it, and moving it on a guess is what there
// is to avoid.
func herdrFocused(id string) bool {
	out, err := exec.Command(herdrBin(), "workspace", "get", id).Output()
	if err != nil {
		return false
	}
	var resp struct {
		Result struct {
			Workspace struct {
				Focused bool `json:"focused"`
			} `json:"workspace"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return false
	}
	return resp.Result.Workspace.Focused
}

// herdrFocusWS focuses one workspace, which is how the removal of the workspace
// we're standing in says where to land afterwards.
func herdrFocusWS(id string) error {
	if out, err := exec.Command(herdrBin(), "workspace", "focus", id).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr workspace focus %s: %s", id, lastLine(out, err))
	}
	return nil
}

// herdrTabs is the workspace's tabs in the order herdr lists them, which is
// the order they sit in along the tab bar: tabs can be dragged about, and a
// tab's `number` is the one it was born with rather than the place it now
// holds, so the list's own order is the only thing that says where a tab is.
// The three lists are asked for separately because that is how herdr keeps
// them: a tab has a label and a place in the bar, a pane has a cwd and a
// title, and an agent has a status and the counter that says when it last
// changed.
func herdrTabs(ws string) ([]wsTab, error) {
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
	return ts, nil
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

// herdrSnap is the part of herdr's `api snapshot` the corner looks at.
func herdrSnap() (muxSnapshot, bool) {
	var snap struct {
		Result struct {
			Snapshot struct {
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
			} `json:"snapshot"`
		} `json:"result"`
	}
	out, err := herdrAsk("session.snapshot", nil, "api", "snapshot")
	if err != nil || json.Unmarshal(out, &snap) != nil {
		return muxSnapshot{}, false
	}
	var s muxSnapshot
	for _, w := range snap.Result.Snapshot.Workspaces {
		s.Workspaces = append(s.Workspaces, muxWorkspace{ID: w.ID, Label: w.Label, Path: w.Worktree.Path})
	}
	for _, t := range snap.Result.Snapshot.Tabs {
		s.Tabs = append(s.Tabs, muxTab{ID: t.ID, Workspace: t.Workspace, Label: t.Label})
	}
	for _, p := range snap.Result.Snapshot.Panes {
		s.Panes = append(s.Panes, muxPane{ID: p.ID, Tab: p.Tab})
	}
	return s, true
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

// herdrFocusPane goes to one pane. `agent focus` is the only thing herdr
// focuses a pane itself by, so a pane running no agent is reached through its
// tab instead — which lands on the tab's own focused pane, close enough when
// the tab is split and exact when it isn't.
func herdrFocusPane(pane string) error {
	if err := exec.Command(herdrBin(), "agent", "focus", pane).Run(); err == nil {
		return nil
	}
	tab, err := herdrPaneTab(pane)
	if err != nil {
		return err
	}
	if out, err := exec.Command(herdrBin(), "tab", "focus", tab).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr tab focus %s: %s", tab, lastLine(out, err))
	}
	return nil
}

// herdrPaneTab is the tab a pane sits in, which is what herdr can be told to
// go to.
func herdrPaneTab(pane string) (string, error) {
	out, err := exec.Command(herdrBin(), "pane", "get", pane).Output()
	if err != nil {
		return "", fmt.Errorf("herdr pane get %s: %w", pane, err)
	}
	var resp struct {
		Result struct {
			Pane struct {
				Tab string `json:"tab_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.Result.Pane.Tab == "" {
		return "", fmt.Errorf("herdr pane get %s: no tab in the answer", pane)
	}
	return resp.Result.Pane.Tab, nil
}

// herdrWherePane is a pane's place, for --dry-run: its tab's, the tab being
// where going there lands you.
func herdrWherePane(pane string) (place, error) {
	tab, err := herdrPaneTab(pane)
	if err != nil {
		return place{}, err
	}
	return herdrPlace(tab)
}

// herdrPlace is a tab's place: the name of the workspace it is in, its own
// name, and its id — the address `herdr tab focus` takes. Names, not ids, are
// what a dry run is read for; the id is there to be pasted into herdr.
func herdrPlace(tab string) (place, error) {
	var tabResp struct {
		Result struct {
			Tab struct {
				Label     string `json:"label"`
				Workspace string `json:"workspace_id"`
			} `json:"tab"`
		} `json:"result"`
	}
	out, err := exec.Command(herdrBin(), "tab", "get", tab).Output()
	if err != nil {
		return place{}, fmt.Errorf("herdr tab get %s: %w", tab, err)
	}
	if err := json.Unmarshal(out, &tabResp); err != nil {
		return place{}, fmt.Errorf("herdr tab get %s: %w", tab, err)
	}
	var wsResp struct {
		Result struct {
			Workspace struct {
				Label string `json:"label"`
			} `json:"workspace"`
		} `json:"result"`
	}
	out, err = exec.Command(herdrBin(), "workspace", "get", tabResp.Result.Tab.Workspace).Output()
	if err != nil {
		return place{}, fmt.Errorf("herdr workspace get %s: %w", tabResp.Result.Tab.Workspace, err)
	}
	if err := json.Unmarshal(out, &wsResp); err != nil {
		return place{}, fmt.Errorf("herdr workspace get %s: %w", tabResp.Result.Tab.Workspace, err)
	}
	return place{wsResp.Result.Workspace.Label, tabResp.Result.Tab.Label, tab}, nil
}

// herdrRepoWS is the first workspace in herdr's sidebar on each repo — the
// main checkout or any worktree of it, the same test that puts a repo's section
// among the open ones in -g's list — keyed by the repo's root.
//
// ponytail: package var so tests can fake the herdr answer.
var herdrRepoWS = func() map[string]string {
	out, err := herdrAsk("workspace.list", nil, "workspace", "list")
	var resp struct {
		Result struct {
			Workspaces []struct {
				ID       string `json:"workspace_id"`
				Worktree struct {
					Repo string `json:"repo_root"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(out, &resp) != nil {
		return nil
	}
	ws := map[string]string{}
	for _, w := range resp.Result.Workspaces {
		if _, ok := ws[w.Worktree.Repo]; !ok && w.Worktree.Repo != "" {
			ws[w.Worktree.Repo] = w.ID
		}
	}
	return ws
}

// herdrFile moves the workspace id to the end of category cat's section of
// herdr's sidebar: just above the divider after "------ cat", or last when
// that is the last divider. No such divider leaves it where it is.
func herdrFile(id, cat string) {
	out, err := herdrAsk("workspace.list", nil, "workspace", "list")
	var resp struct {
		Result struct {
			Workspaces []struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(out, &resp) != nil {
		return
	}
	// Counted without the workspace itself, since that is how herdr counts
	// where it lands.
	at, in := -1, false
	i := 0
	for _, w := range resp.Result.Workspaces {
		if w.ID == id {
			continue
		}
		if rest, ok := strings.CutPrefix(w.Label, herdrSep); ok {
			if in {
				break
			}
			in = strings.TrimSpace(strings.TrimLeft(rest, "-")) == cat
		}
		i++
		if in {
			at = i
		}
	}
	if at < 0 {
		return
	}
	_, _ = herdrAsk("workspace.move", map[string]any{"workspace_id": id, "insert_index": at},
		"workspace", "move", id, fmt.Sprint(at)) // ponytail: herdr 0.9.1 has no cli for it, only the socket
}

// herdrAsker is how a question is put to herdr: herdrAsk or herdrPoll, for a
// question an action and a view both ask, each in its own way.
type herdrAsker func(method string, params map[string]any, args ...string) ([]byte, error)

// herdrPoll is herdrAsk for what ccwt asks and tells herdr on its own, to keep
// a view up to date, rather than because a key was pressed or a command run.
// Paused, herdr hears none of it: the answer is the last one herdr gave the same
// question, so a view stands still rather than going blank, and errHerdrPaused
// for a question it was never asked.
func herdrPoll(method string, params map[string]any, args ...string) ([]byte, error) {
	key := fmt.Sprintf("%q", args) // the cli's words for the request say all the socket's do
	if herdrPaused() {
		if out, ok := herdrHeard.Load(key); ok {
			return out.([]byte), nil
		}
		return nil, errHerdrPaused
	}
	out, err := herdrAsk(method, params, args...)
	if err == nil {
		herdrHeard.Store(key, out)
	}
	return out, err
}

// herdrHeard is herdrPoll's last answer to each question it has put.
var herdrHeard sync.Map
