package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// opener is `o` under -g: a pane over the list offering the repos ccwt has
// been used in and that nobody hid, most recently used first — ● those herdr
// has a workspace on, which picking switches to, ○ the ones to open again.
//
// A snapshot, like the worklog's: what was open when the pane came up.
type opener struct {
	all      []string          // what's on offer
	ws       map[string]string // the workspace herdr has on each open one
	filter   string            // what's been typed: a substring of the path, case ignored
	sel, top int               // into shown()
	picked   bool              // sel was moved to by hand, so p and P are about it, not the filter
	first, n int               // the screen line the first row came out on, and how many were drawn
	clicked  time.Time
	bare     bool // `ccwt open --here`'s: see pane
}

// openOpener is `o`: the picker, over the list.
func (u *ui) openOpener() {
	if o, err := newOpener(); err != nil {
		u.msg = "open: " + err.Error()
	} else {
		u.opener, u.msg = o, ""
	}
}

// summon is `ccwt open` arriving: `o`, whatever is up. A search is given up as
// esc gives it up, and what only shows something — a pane, a page, the menu —
// is closed; a prompt being written or a question being asked is left alone,
// since what's in it is the user's.
func (u *ui) summon() {
	if !underHerdr() || u.projects == nil || u.opener != nil {
		return
	}
	if u.entry.open || u.model.open || u.trust != "" {
		u.msg = "open: finish the prompt first"
		return
	}
	if u.typing {
		u.edit("\x1b")
	}
	u.detail, u.page, u.menu, u.todo = nil, nil, nil, nil
	u.logOpen, u.log = false, nil
	u.openOpener()
}

// OpenCmd is `o` from anywhere: under herdr, a popup over the focused pane with
// the picker in it (the plugin's `picker` pane, which is `open --here`), so the
// focus comes back to that pane when it closes. Without the plugin's popup it
// is the -g tui's pane, with the picker up in it. The tui says which process it
// is in globalPath, and watchSummon is how it hears.
type OpenCmd struct {
	Here bool `help:"Run the picker in this terminal, which is what the herdr plugin's popup runs: esc closes it, a pick goes to the project's workspace."`
}

func (c *OpenCmd) Run() error {
	if c.Here {
		return pickHere()
	}
	if underHerdr() && openPopup() == nil {
		return nil
	}
	b, _ := os.ReadFile(globalPath())
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	// The file outlives a tui killed outright, and its pid can be anyone's by
	// then: only a ccwt is sent the signal, and every tui shrugs it off.
	if err != nil || !isCcwt(pid) {
		return errors.New("no ccwt -g running")
	}
	if err := summon(pid); err != nil {
		return err
	}
	if msg := focusPid(pid); !strings.HasPrefix(msg, "went to ") {
		return errors.New(msg)
	}
	return nil
}

// openPopup has herdr open the ccwt plugin's `picker` pane as a popup, running
// this same binary — a build of a branch, not just the one on PATH. It fails
// where the plugin isn't linked, or is a version without the pane.
func openPopup() error {
	env := map[string]string{}
	if exe, err := os.Executable(); err == nil {
		env["CCWT_BIN"] = exe
	}
	_, err := herdrAsk("plugin.pane.open",
		map[string]any{"plugin_id": "ccwt", "entrypoint": "picker", "placement": "popup", "focus": true, "env": env},
		"plugin", "pane", "open", "--plugin", "ccwt", "--entrypoint", "picker", "--placement", "popup", "--focus", "--env", "CCWT_BIN="+env["CCWT_BIN"])
	// ponytail: `go run` deletes this binary as soon as it exits, which can be
	// before the popup has started it; a moment's wait covers that. Have the
	// popup say when it's up if the guess is ever short.
	if err == nil && strings.Contains(env["CCWT_BIN"], "go-build") {
		time.Sleep(time.Second)
	}
	return err
}

// pickHere is `ccwt open --here`: the picker, the whole of this terminal, until
// esc or a project picked. A pick that fails says why on the bar and stays up.
func pickHere() error {
	if !stdoutIsTTY() {
		return errors.New("open --here needs a terminal")
	}
	o, err := newOpener()
	if err != nil {
		return err
	}
	o.bare = true
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), state)
	// As in the tui: git's complaints would land in the middle of the frame.
	if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		saved := os.Stderr
		os.Stderr = devnull
		defer func() { os.Stderr = saved; devnull.Close() }()
	}
	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1002h\x1b[?1006h")
	defer fmt.Print("\x1b[?1006l\x1b[?1002l\x1b[?7h\x1b[?25h\x1b[?1049l")

	keys, next := readKeys()
	resized, stopResize := watchResize()
	defer stopResize()
	out := bufio.NewWriter(os.Stdout)
	msg := ""
	draw := func() error {
		cols, rows := termSize()
		body := max(rows-1, 1)
		lines, bar := o.pane(cols, body)
		lines = append(lines, make([]string, max(body-len(lines), 0))...)
		paint(out, append(lines[:body], highlight(bar+msg, cols)))
		return out.Flush()
	}
	for {
		if err := draw(); err != nil {
			return err
		}
		var k string
		select {
		case <-resized:
			continue
		case k = <-keys:
		}
		do, p := o.key(k)
		if k == "\x03" {
			do = "close"
		}
		msg = ""
		switch do {
		case "close":
			return nil
		case "open":
			msg = "opening " + filepath.Base(p) + "…"
			if err := draw(); err != nil {
				return err
			}
			if msg = o.open(p); strings.HasPrefix(msg, "opened") || strings.HasPrefix(msg, "switched to ") {
				return nil
			}
		case "pull":
			msg = gitPull(p)
		case "copy":
			copyClip(p)
			msg = "copied to clipboard: " + p
		}
		next <- struct{}{}
	}
}

// isCcwt reports whether pid is a ccwt other than this one.
func isCcwt(pid int) bool {
	procs, err := listProcesses()
	if err != nil || pid == os.Getpid() {
		return false
	}
	for _, p := range procs {
		if p.pid == pid {
			exe, _, _ := strings.Cut(p.cmdline, " ")
			return filepath.Base(exe) == "ccwt"
		}
	}
	return false
}

// globalPath is the file the -g tui writes its pid in, for `ccwt open`.
//
// ponytail: one file, so the last -g started is the one `ccwt open` reaches —
// a second herdr session's -g included. Key it by HERDR_SOCKET_PATH if two at
// once ever matters.
func globalPath() string {
	if dir := stateDir(); dir != "" {
		return filepath.Join(dir, "global.pid")
	}
	return ""
}

// registerGlobal puts this tui in globalPath, and returns what takes it out
// again: on the way out, unless a later -g has written its own since.
func registerGlobal() func() {
	path, pid := globalPath(), strconv.Itoa(os.Getpid())
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o755) != nil || os.WriteFile(path, []byte(pid), 0o644) != nil {
		return func() {}
	}
	return func() {
		if b, _ := os.ReadFile(path); string(b) == pid {
			os.Remove(path)
		}
	}
}

// newOpener looks at what there is to offer: the seen projects, and which of
// them herdr has a workspace on.
func newOpener() (*opener, error) {
	recent, err := recentProjects()
	if err != nil {
		return nil, err
	}
	return &opener{all: recent, ws: herdrRepoWS()}, nil
}

// open is what picking p does: switch to herdr's workspace on it, or open one.
func (o *opener) open(p string) string {
	id, ok := o.ws[p]
	if !ok {
		return openProject(p)
	}
	if err := herdrFocusWS(id); err != nil {
		return err.Error()
	}
	return "switched to " + filepath.Base(p)
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

// shown is what the filter leaves of it.
func (o *opener) shown() []string {
	f := strings.ToLower(o.filter)
	var s []string
	for _, p := range o.all {
		if strings.Contains(strings.ToLower(shortenHome(p)), f) {
			s = append(s, p)
		}
	}
	return s
}

// key is the picker's turn at a keystroke: what to do — "open" or "close",
// which are the picker done, "pull" or "copy", which leave it up — and the
// project to do it to. Typing filters straight away, so ↵ is all it takes to
// open the one on top; once ↑↓ or a click has picked a row, p pulls it and P
// copies its path instead, and anything else typed goes back to the filter.
func (o *opener) key(k string) (string, string) {
	shown := o.shown()
	switch {
	case k == "\x1b": // the filter first, as the list's esc drops its pattern
		if o.filter == "" {
			return "close", ""
		}
		o.filter, o.sel, o.picked = "", 0, false
	case k == "\x1b[A":
		o.sel, o.picked = max(o.sel-1, 0), true
	case k == "\x1b[B":
		o.sel, o.picked = min(o.sel+1, max(len(shown)-1, 0)), true
	case k == "\r", k == "\n":
		if o.sel < len(shown) {
			return "open", shown[o.sel]
		}
	case o.picked && (k == "p" || k == "P") && o.sel < len(shown):
		return map[string]string{"p": "pull", "P": "copy"}[k], shown[o.sel]
	case mouseRow(k) == 0:
		if f := typeKey(o.filter, k); f != o.filter {
			o.filter, o.sel, o.picked = f, 0, false
		}
	// A click selects, a second one opens: a workspace is a lot to make on a
	// stray click.
	default:
		i := mouseRow(k) - o.first
		if i < 0 || i >= o.n {
			break
		}
		i += o.top
		double := i == o.sel && time.Since(o.clicked) < doubleClick
		o.sel, o.clicked, o.picked = i, time.Now(), true
		if double {
			return "open", shown[i]
		}
	}
	return "", ""
}

// pane draws the picker as the worklog is drawn — a box over the list, rows
// under a line of their own, which here is the filter — and says what the bar
// under it is to read.
//
// bare is the picker with the whole terminal to itself, a popup herdr has
// already put a border round: no box of its own, rows from the second line.
func (o *opener) pane(cols, rows int) ([]string, string) {
	pad, inner := paneWidth(cols)
	row, sel := paneRow(pad, inner), band
	fits := logFits(rows)
	if o.bare {
		row = func(s string) string { return s }
		sel = func(s string) string { return draw(s, cols, nil, rowBar) }
		fits = max(rows-1, 1)
	}
	shown := o.shown()
	o.sel = min(max(o.sel, 0), max(len(shown)-1, 0))
	o.top = max(min(max(o.top, o.sel-fits+1), o.sel), 0)

	body := []string{row(" > " + o.filter)}
	o.n = min(fits, len(shown)-o.top)
	for i, p := range shown[o.top : o.top+o.n] {
		mark := "○"
		if _, ok := o.ws[p]; ok {
			mark = "●"
		}
		l := row(" " + mark + " " + shortenHome(p))
		if o.top+i == o.sel {
			l = sel(l)
		}
		body = append(body, l)
	}
	if len(shown) == 0 {
		body = append(body, row(" nothing to open"))
	}
	keys := " type:filter  ↑↓:select  ↵:open  esc:close "
	if o.filter != "" {
		keys = " type:filter  ↑↓:select  ↵:open  esc:clear "
	}
	if o.picked && len(shown) > 0 {
		keys = " ↑↓:select  ↵:open  p:pull  P:copy path  type:filter "
	}
	if o.bare {
		o.first = 2 // past the filter, and a mouse counts from 1
		return body, keys
	}
	lines := paneBox(pad, inner, "open", body)
	margin := min(2, max(rows-len(lines), 0)/2)
	o.first = margin + 3 // the margin, the top rule and the filter, and a mouse counts from 1
	return append(make([]string, margin), lines...), keys
}

// openProject opens the repo at path in a workspace of its own and runs ccwt
// in its first tab, named `prj` — where ↻ under -g looks for it, and starts it
// again, should it ever stop.
//
// A worktree open rather than a workspace create: only the former tells herdr
// which repo the workspace is on, and without that herdrLabels can't see it —
// the repo would stay behind -g's "…" and `o` would go on offering it. A
// workspace already there on path, made some other way, is taken over rather
// than doubled, and gets that repo from then on; it has its own shell in its
// first pane, so nothing is started there.
func openProject(path string) string {
	exe, err := os.Executable()
	if err != nil {
		return "open failed: " + err.Error()
	}
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
		return fmt.Sprintf("open failed: herdr worktree open: %v", err)
	}
	if resp.Result.AlreadyOpen {
		return "opened " + filepath.Base(path)
	}
	// Best effort: a workspace in the wrong place is still open.
	if cfg, err := loadConfig(); err == nil {
		if cat := categoryOf(cfg.Categories, path); cat != "" {
			herdrFile(resp.Result.Workspace.ID, cat)
		}
	}
	if out, err := exec.Command(herdrBin(), "pane", "run", resp.Result.Pane.ID, exe).CombinedOutput(); err != nil {
		return "opened, but ccwt did not start: " + lastLine(out, err)
	}
	// Best effort, as `c`'s `ws`: a tab with the wrong name still has its tui.
	_ = exec.Command(herdrBin(), "tab", "rename", resp.Result.Tab.ID, "prj").Run()
	return "opened " + filepath.Base(path)
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
