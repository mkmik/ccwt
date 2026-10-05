package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// opener is `o` under -g: a pane over the list offering the repos ccwt has
// been used in that herdr has no workspace on, and that nobody hid, most
// recently used first — the ones to pick from when it's time to open one again.
//
// A snapshot, like the worklog's: what was open when the pane came up.
type opener struct {
	all      []string // what's on offer
	filter   string   // what's been typed: a substring of the path, case ignored
	sel, top int      // into shown()
	first, n int      // the screen line the first row came out on, and how many were drawn
	clicked  time.Time
}

// newOpener looks at what there is to offer: the seen projects, less those
// herdr has a workspace on — the main checkout or any worktree of it, the same
// test that puts a repo's section among the open ones in -g's list.
func newOpener() (*opener, error) {
	recent, err := recentProjects()
	if err != nil {
		return nil, err
	}
	_, order, _ := herdrLabels()
	return &opener{all: slices.DeleteFunc(recent, func(p string) bool {
		_, open := order[p]
		return open
	})}, nil
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

// key is the picker's turn at a keystroke: the project picked, if one was, and
// whether the picker is done — picked from, or closed. Typing filters straight
// away, so ↵ is all it takes to open the one on top.
func (o *opener) key(k string) (string, bool) {
	shown := o.shown()
	switch k {
	case "\x1b": // the filter first, as the list's esc drops its pattern
		if o.filter == "" {
			return "", true
		}
		o.filter, o.sel = "", 0
	case "\x1b[A":
		o.sel--
	case "\x1b[B":
		o.sel++
	case "\r", "\n":
		if o.sel < len(shown) {
			return shown[o.sel], true
		}
	default:
		// A click selects, a second one opens: a workspace is a lot to make
		// on a stray click.
		if mouseRow(k) == 0 {
			if f := typeKey(o.filter, k); f != o.filter {
				o.filter, o.sel = f, 0
			}
			break
		}
		i := mouseRow(k) - o.first
		if i < 0 || i >= o.n {
			break
		}
		i += o.top
		double := i == o.sel && time.Since(o.clicked) < doubleClick
		o.sel, o.clicked = i, time.Now()
		if double {
			return shown[i], true
		}
	}
	return "", false
}

// pane draws the picker as the worklog is drawn — a box over the list, rows
// under a line of their own, which here is the filter — and says what the bar
// under it is to read.
func (o *opener) pane(cols, rows int) ([]string, string) {
	pad, inner := paneWidth(cols)
	row := paneRow(pad, inner)
	shown := o.shown()
	fits := logFits(rows)
	o.sel = min(max(o.sel, 0), max(len(shown)-1, 0))
	o.top = max(min(max(o.top, o.sel-fits+1), o.sel), 0)

	body := []string{row(" > " + o.filter)}
	o.n = min(fits, len(shown)-o.top)
	for i, p := range shown[o.top : o.top+o.n] {
		l := row(" " + shortenHome(p))
		if o.top+i == o.sel {
			l = band(l)
		}
		body = append(body, l)
	}
	if len(shown) == 0 {
		body = append(body, row(" nothing to open"))
	}
	lines := paneBox(pad, inner, "open", body)
	margin := min(2, max(rows-len(lines), 0)/2)
	o.first = margin + 3 // the margin, the top rule and the filter, and a mouse counts from 1
	keys := " type:filter  ↑↓:select  ↵:open  esc:close "
	if o.filter != "" {
		keys = " type:filter  ↑↓:select  ↵:open  esc:clear "
	}
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
