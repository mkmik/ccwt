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
	filter   string   // `/`'s: a substring of the path, case ignored
	typing   bool     // the filter is taking the keystrokes
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
// whether the picker is done — picked from, or closed.
func (o *opener) key(k string) (string, bool) {
	if o.typing {
		switch k {
		case "\r", "\n":
			o.typing = false
		case "\x1b":
			o.typing, o.filter = false, ""
		default:
			o.filter = typeKey(o.filter, k)
		}
		o.sel = 0
		return "", false
	}
	shown := o.shown()
	switch k {
	case "\x1b": // the filter first, as the list's esc drops its pattern
		if o.filter == "" {
			return "", true
		}
		o.filter, o.sel = "", 0
	case "/":
		o.typing = true
	case "\x1b[A", "k":
		o.sel--
	case "\x1b[B", "j":
		o.sel++
	case "\r", "\n", " ":
		if o.sel < len(shown) {
			return shown[o.sel], true
		}
	default:
		// A click selects, a second one opens: a workspace is a lot to make
		// on a stray click.
		i := mouseRow(k) - o.first
		if mouseRow(k) == 0 || i < 0 || i >= o.n {
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

	head := ""
	if o.typing || o.filter != "" {
		head = "/" + o.filter
	}
	body := []string{row(" " + head)}
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
	keys := " ↑↓:select  ↵:open  /:filter  esc:close "
	if o.typing {
		keys = " ↵:done  esc:clear "
	}
	return append(make([]string, margin), lines...), keys
}

// openProject opens the repo at path in a workspace of its own and runs ccwt
// in its first tab, named `prj` — where ↻ under -g looks for it, and starts it
// again, should it ever stop.
func openProject(path string) string {
	exe, err := os.Executable()
	if err != nil {
		return "open failed: " + err.Error()
	}
	out, err := herdrAsk("workspace.create", map[string]any{"cwd": path, "focus": true},
		"workspace", "create", "--cwd", path, "--focus")
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
		return fmt.Sprintf("open failed: herdr workspace create: %v", err)
	}
	if out, err := exec.Command(herdrBin(), "pane", "run", resp.Result.Pane.ID, exe).CombinedOutput(); err != nil {
		return "opened, but ccwt did not start: " + lastLine(out, err)
	}
	// Best effort, as `c`'s `ws`: a tab with the wrong name still has its tui.
	_ = exec.Command(herdrBin(), "tab", "rename", resp.Result.Tab.ID, "prj").Run()
	return "opened " + filepath.Base(path)
}
