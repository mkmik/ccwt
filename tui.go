package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/mkmik/ccwt/internal/gitutil"
)

type TuiCmd struct {
	Interval time.Duration `default:"2s" help:"How often to re-read the worktree list."`
	Fetch    time.Duration `default:"1m" help:"How often to fetch origin/main in the background (0 to never)."`
	Global   bool          `short:"g" help:"Show the worktrees of every project listed in $XDG_CONFIG_HOME/ccwt/config.toml, not just this repo's."`
}

func (c *TuiCmd) Run() error {
	if !stdoutIsTTY() {
		return errors.New("tui needs a terminal on stdout (use `ccwt list` when piping)")
	}

	projects, err := projectRoots(c.Global)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The cli helpers pass git's stderr straight through on purpose, so on the
	// command line the user sees git's own reason for a refusal. On the tui's
	// screen that same text lands in the middle of the frame, and in raw mode
	// its newline scrolls everything up a line — the status bar walks off the
	// bottom, and the cursor arithmetic every later frame relies on is wrong.
	// `r` on a worktree git won't remove is enough to do it. So while the tui
	// owns the screen, nothing else may write to it.
	//
	// ponytail: the whole var, rather than a quiet flag threaded through every
	// gitutil helper — this way a new one can't reintroduce the problem. The
	// cost is that the status bar reports a refusal as the bare "exit status
	// 128" it always did; capture the stream instead of dropping it if git's
	// reason is worth the plumbing.
	if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		saved := os.Stderr
		os.Stderr = devnull
		defer func() { os.Stderr = saved; devnull.Close() }()
	}

	go fetchMain(ctx, c.Fetch, projects)

	// Raw mode so single keypresses arrive without waiting for a newline. If
	// stdin isn't a terminal we just run without keys: the list still
	// refreshes, and Ctrl-C still works because ISIG stays on. A nil channel
	// blocks forever in the select below, which is exactly what we want.
	// Mouse reporting is only worth turning on when we're actually reading
	// stdin — otherwise the terminal would write click reports into a stdin
	// nobody drains.
	var keys <-chan string
	if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer term.Restore(int(os.Stdin.Fd()), state)
		keys = readKeys()
		// 1000: report button presses. 1006: report them in SGR form, which
		// unlike the original encoding keeps working past column 223.
		fmt.Print("\x1b[?1000h\x1b[?1006h")
		defer fmt.Print("\x1b[?1006l\x1b[?1000l")
	}

	// Alternate screen, hidden cursor, no auto-wrap (a wrapped long line would
	// push the rest of the frame down and desync the cursor arithmetic).
	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[?7l")
	defer fmt.Print("\x1b[?7h\x1b[?25h\x1b[?1049l")

	out := bufio.NewWriter(os.Stdout)
	tick := time.NewTicker(c.Interval)
	defer tick.Stop()

	u := ui{projects: projects, stamp: selfStamp()}
	var last string
	redraw := func() error {
		lines, err := u.frame()
		if err != nil {
			return err
		}
		if joined := strings.Join(lines, "\n"); joined != last {
			last = joined
			paint(out, lines)
			return out.Flush()
		}
		return nil
	}
	// act runs one of the commands bound to a key: show what's happening,
	// run it, then force a full repaint — the command may have printed over
	// the frame, and a frame that happens to be unchanged wouldn't redraw.
	act := func(pending string, do func() string) error {
		u.msg = pending
		if err := redraw(); err != nil {
			return err
		}
		u.msg = do()
		last = ""
		u.stale() // the command may well have changed the list
		return nil
	}
	open := func() error {
		path := u.sel.path
		if path == "" || !underHerdr() {
			return nil
		}
		return act("opening "+filepath.Base(path)+"…", func() string { return herdrOpen(path, "") })
	}
	// activate is what a double-click does to whatever row it landed on: fold a
	// project section shut, or open a worktree as a workspace. The keyboard keeps
	// the two apart — space only opens, ↵ only folds — so that a fold can't
	// misfire into opening a worktree; a click can't, since it names the row it
	// means.
	activate := func() error {
		if u.sel.path == "" {
			u.toggle()
			return nil
		}
		return open()
	}

	for {
		if err := redraw(); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			u.msg = ""
			u.stale() // the tick is what re-reads the list
			u.checkUpgrade()
		case k := <-keys:
			switch {
			// While the search prompt is up every keystroke is text, so this
			// comes first: `q` there is a letter, not the quit key.
			case u.typing:
				u.edit(k)
			case k == "q", k == "\x03": // \x03 = Ctrl-C, which raw mode delivers as a keystroke
				return nil
			case k == "p":
				if err := act("pulling…", func() string { return gitPull(u.gitDir()) }); err != nil {
					return err
				}
			case k == "x" && underHerdr():
				if err := act("creating…", u.newWorktree); err != nil {
					return err
				}
			case k == "\x1b[A", k == "k":
				u.move(-1)
			case k == "\x1b[B", k == "j":
				u.move(1)
			case k == "/":
				u.prompt(1)
			case k == "?":
				u.prompt(-1)
			case k == "n":
				u.seek(u.dir)
			case k == "N":
				u.seek(-u.dir)
			case k == "\x1b":
				u.search = search{} // :nohlsearch, near enough
			case k == " ": // opens a worktree, and does nothing on a section header
				if err := open(); err != nil {
					return err
				}
			case k == "\r", k == "\n": // folds a section, and does nothing on a worktree
				u.toggle()
			case k == "r":
				if path := u.sel.path; path != "" {
					if err := act("removing "+filepath.Base(path)+"…", func() string {
						msg, ok := removeWorktree(path)
						if ok {
							u.dropSelected()
						}
						return msg
					}); err != nil {
						return err
					}
				}
			default:
				// A click selects the row it landed on; it takes a second one to
				// act on it, as it does in any other list.
				if row := u.at(mouseRow(k)); row != (listRow{}) && u.click(row) {
					if err := activate(); err != nil {
						return err
					}
				}
			}
		}
	}
}

// ui is the whole model: which row is selected, the rows the last frame listed,
// the window of them it had room to show, the projects the list spans (nil for
// just the repo we're in), which of their sections are folded shut, and the
// transient status-bar message.
//
// The selection is held as the row's identity rather than as an index because
// the list is re-read and re-sorted every interval: an index would silently
// point at a different worktree once one is removed or a commit reorders the
// rows.
type ui struct {
	sel       listRow // selected row, the zero value when nothing is selected yet
	rows      []listRow
	top       int // rows[top] is the first row on screen
	height    int // how many rows the frame has room for
	projects  []string
	collapsed map[string]bool // project root -> section folded shut
	msg       string

	search        // the pattern in force, which n and N walk and the frame picks out
	saved  search // what the open prompt replaced, put back if it's abandoned
	anchor listRow
	typing bool // the prompt is up and taking keystrokes

	// What the binary on disk looked like when this tui started, and the notice
	// that goes in the bar once it stops looking like that — see checkUpgrade.
	stamp   string
	restart string

	clicked time.Time // when the last click landed on sel — see click

	// The last list read, kept so that moving the selection — which changes
	// nothing but which row is reverse-videoed — doesn't re-run the git and
	// lsof scan behind it (half a second on a big repo, once per keypress).
	// stale() drops it whenever something that would change the list happens;
	// otherwise it's the interval tick that refreshes it.
	body []string          // rendered table, header line included
	all  []listRow         // one per body line after the header
	cols int               // the width body was rendered at
	div  map[string]string // repo -> its status-bar divergence, same lifetime
}

// stale drops the cached list, so the next frame reads it again.
func (u *ui) stale() { u.body = nil }

// at returns the row on screen line n, counting the way a mouse report does:
// line 1 is the table header, so line 2 is the first row of the window. The
// zero row comes back for a line with nothing on it — the header, or the blank
// space under a list too short to fill the screen.
func (u *ui) at(n int) listRow {
	i := u.top + n - 2
	if n < 2 || n-2 >= u.height || i >= len(u.rows) {
		return listRow{}
	}
	return u.rows[i]
}

// doubleClick is how long the second click of a double has to arrive within.
// ponytail: a constant, since no terminal tells us the desktop's setting.
const doubleClick = 400 * time.Millisecond

// click selects the row and reports whether it was the second click on it in
// quick succession — the one that acts on it. A single click only moves the
// selection: opening a workspace is not something a stray click on the way past
// should do, and the row you clicked is the one the keys then apply to.
//
// The timing is ours to keep because the terminal doesn't do it: SGR reporting
// has one press event and no notion of a double.
func (u *ui) click(row listRow) bool {
	double := row == u.sel && time.Since(u.clicked) < doubleClick
	u.sel, u.clicked = row, time.Now()
	if double {
		// A third click starts a fresh pair rather than opening the row again.
		u.clicked = time.Time{}
	}
	return double
}

// move walks the selection by d rows, starting from the top when nothing is
// selected (or when the selected row has since disappeared). Section headers
// are rows like any other: they're what ↵ folds, so the keyboard has to be able
// to land on one.
func (u *ui) move(d int) {
	if len(u.rows) == 0 {
		return
	}
	i := min(max(slices.Index(u.rows, u.sel)+d, 0), len(u.rows)-1)
	u.sel = u.rows[i]
}

// search is one / (or ?) pattern: the text of it and the direction n repeats
// it in. Kept together because they're saved and restored together — abandoning
// a `?` prompt has to put back the way the old pattern ran, not just its text.
type search struct {
	query string
	dir   int // +1 for /, -1 for ?
}

// prompt opens the search prompt, running in direction d. It remembers both the
// pattern it displaces, so escape can put it back, and the row to search from,
// so that the match tracks the pattern being typed instead of walking further
// down the list with every keystroke.
func (u *ui) prompt(d int) {
	u.saved, u.search = u.search, search{dir: d}
	u.anchor, u.msg, u.typing = u.sel, "", true
}

// edit feeds one keystroke to the prompt: enter accepts the match, escape (and
// Ctrl-C) abandons the whole search, backspace rubs out a rune, and anything
// printable is appended — a multi-byte rune arrives as its bytes, in order,
// which is what this appends. Escape sequences (arrows, mouse reports) are
// ignored: they're several bytes but only the first is under 0x20, so they'd
// otherwise land in the pattern as garbage.
func (u *ui) edit(k string) {
	switch k {
	case "\r", "\n":
		u.typing = false
		u.report(u.preview())
	case "\x1b", "\x03":
		u.search, u.sel, u.typing = u.saved, u.anchor, false
	case "\x7f", "\b":
		if r := []rune(u.query); len(r) > 0 {
			u.query = string(r[:len(r)-1])
			u.preview()
		}
	default:
		if len(k) == 1 && k[0] >= ' ' {
			u.query += k
			u.preview()
		}
	}
}

// preview is the search as run while it's being typed: always from the row the
// prompt opened on, so that deleting a character walks the match back rather
// than leaving the selection wherever the longer pattern had pushed it. A
// pattern that matches nothing leaves the selection on the anchor, as vim does.
func (u *ui) preview() bool {
	u.sel = u.anchor
	return u.query == "" || u.find(u.dir)
}

// seek is n and N: move the selection to the next match in direction d.
func (u *ui) seek(d int) {
	if u.query != "" {
		u.report(u.find(d))
	}
}

// report puts the outcome of a search on the bar — nothing at all when it
// landed on something, and otherwise which of the two ways it failed.
func (u *ui) report(ok bool) {
	switch _, err := regexp.Compile(u.query); {
	case ok:
		u.msg = ""
	case err != nil:
		u.msg = "bad pattern: " + u.query
	default:
		u.msg = "not found: " + u.query
	}
}

// find walks the rows from the selection in direction d, wrapping around the
// ends the way vim's search does, and selects the first one the pattern matches.
// It matches against the line as drawn — name, branch, age and topic at once —
// because that's what the user is looking at; a per-column search would need a
// syntax to say which column.
func (u *ui) find(d int) bool {
	re := u.re()
	if re == nil || len(u.rows) == 0 {
		return false
	}
	// Nothing selected: start just off the end we're heading away from, so the
	// first row probed is the top one either way.
	start := slices.Index(u.rows, u.sel)
	if start < 0 {
		start = -d
	}
	for i := 1; i <= len(u.rows); i++ {
		j := ((start+d*i)%len(u.rows) + len(u.rows)) % len(u.rows)
		// Line 0 of the cached table is the header, so row j is line j+1.
		if j+1 < len(u.body) && re.MatchString(u.body[j+1]) {
			u.sel = u.rows[j]
			return true
		}
	}
	return false
}

// re is the pattern as a regexp, always case-insensitive: these are worktree
// names and commit subjects, and nobody hunting for one holds shift for it.
// Anything that doesn't compile matches nothing, which is what a half-typed
// "[wip" is on its way through — incremental search has to survive it.
func (u *ui) re() *regexp.Regexp {
	if u.query == "" {
		return nil
	}
	re, err := regexp.Compile("(?i)" + u.query)
	if err != nil {
		return nil
	}
	return re
}

// toggle folds the selected project's section shut, or opens it back up. Only
// section headers fold, and only -g draws any.
func (u *ui) toggle() {
	if u.sel.path != "" || u.sel.project == "" {
		return
	}
	if u.collapsed == nil {
		u.collapsed = map[string]bool{}
	}
	u.collapsed[u.sel.project] = !u.collapsed[u.sel.project]
	u.stale()
}

// root is the repo an action applies to: the selected row's project, or the
// repo the tui is running in when nothing is selected. Outside -g they're all
// the same repo. Under -g there is no such fallback — the repos in view are the
// configured ones, and the directory ccwt was started in isn't one of them.

func (u *ui) root() (string, error) {
	if u.sel.project != "" {
		return u.sel.project, nil
	}
	if u.projects != nil {
		return "", errors.New("no worktree selected")
	}
	return gitutil.RepoRoot("", true)
}

// gitDir is the repo the whole-repo actions work on — the pull key, and the
// branch the status bar reports the drift of. "." outside -g: the directory
// ccwt was started in. Under -g the selected row's project instead, and ""
// (do nothing, report nothing) until something is selected, since with a dozen
// repos on screen the current directory is not the one the keys should reach.
func (u *ui) gitDir() string {
	if u.projects == nil {
		return "."
	}
	return u.sel.project
}

// dropSelected moves the selection off the selected worktree, as it has to once
// that worktree is removed: down a row, or up one when the removed row was the
// last. The removed row is still in u.rows — the list is only re-read on the
// next frame — so this is the ordinary walk, and when it was the only row the
// selection stays on the dead one and frame clears it.
func (u *ui) dropSelected() {
	gone := u.sel
	u.move(1)
	if u.sel == gone {
		u.move(-1)
	}
}

// frame renders the full screen: the worktree table, padding, and a status bar
// pinned to the bottom row.
//
// The frame is sized to the terminal, which is also how a resize gets noticed:
// different size, different frame, so the ordinary diff repaints it. That's
// cheaper than a SIGWINCH handler — and portable, since SIGWINCH doesn't exist
// on Windows. ponytail: costs up to one --interval of staleness after a
// resize; wire up the signal (behind a build tag) if that ever grates.
func (u *ui) frame() ([]string, error) {
	cols, rows := termSize()

	// Re-read only when the cache was dropped or the terminal got wider or
	// narrower, since the table is laid out to the width.
	if u.body == nil || cols != u.cols {
		var buf bytes.Buffer
		listRows, err := renderList(&buf, true, cols, u.projects, u.collapsed, true)
		if err != nil {
			return nil, err
		}
		u.body = strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		u.all, u.cols, u.div = listRows, cols, nil
	}

	body := max(rows-1, 1)

	// The list scrolls, but only as far as it takes to keep the selection on
	// screen — the rows don't move while you're walking around inside the
	// window. -g is what makes this necessary: four projects' worktrees run to
	// several screenfuls, and a frame that simply cut the overflow left the
	// rest not just invisible but unreachable, since the arrows stopped at the
	// bottom edge and the section headers you'd fold to get past it were
	// themselves below it.
	u.rows = u.all
	u.height = max(body-1, 0) // the header takes the frame's first line
	sel := slices.Index(u.rows, u.sel)
	if sel < 0 {
		u.sel = listRow{} // the selected row is gone (removed, or folded away)
	}
	u.top = min(max(u.top, 0), max(len(u.rows)-u.height, 0))
	if sel >= 0 {
		u.top = min(max(u.top, sel-u.height+1), sel)
	}

	// Line 0 of the cached table is the header, so its row i is line i+1. The
	// window is a fresh slice, which is what keeps the highlight below out of
	// the cache.
	last := min(1+u.top+u.height, len(u.body))
	lines := append([]string{u.body[0]}, u.body[min(1+u.top, last):last]...)
	// Every match on screen is picked out, not just the one the selection landed
	// on — vim's hlsearch. In a list of near-identical generated names, a bar
	// across one row doesn't say what about it matched.
	re := u.re()
	for i, line := range lines[1:] {
		bg := ""
		if i == sel-u.top {
			bg = rowBar
		}
		lines[i+1] = draw(line, cols, re, bg)
	}

	for len(lines) < body {
		lines = append(lines, "")
	}
	// While the prompt is up it takes the whole bar, as it does in vim: the key
	// hints have nothing to say about a line you're typing into.
	// The restart notice outlasts the transient messages but yields to them
	// while one is up: they're a second old, and it will still be true after.
	bar := statusBar(cols, cmp.Or(u.msg, u.restart), u.sel, u.divergence())
	if u.typing {
		p := "/"
		if u.dir < 0 {
			p = "?"
		}
		bar = highlight(p+u.query, cols)
	}
	return append(lines[:body], bar), nil
}

// checkUpgrade notices that the binary this process was started from has been
// replaced under it — go install, brew upgrade, a downloaded release — and
// parks a notice in the status bar, since the tui someone leaves open for days
// would otherwise go on running the old code with nothing to say about it.
//
// The notice sticks once shown: the only thing that clears it is the restart.
// A stamp that can't be read (at startup or now) means we can't tell, and the
// bar stays quiet rather than crying upgrade at the window during an install
// where the path is momentarily missing.
func (u *ui) checkUpgrade() {
	if u.restart != "" || u.stamp == "" {
		return
	}
	if s := selfStamp(); s == "" || s == u.stamp {
		return
	}
	u.restart = "upgraded — restart ccwt"
	if v := selfVersion(); v != "" {
		u.restart = "upgraded to " + v + " — restart ccwt"
	}
}

// selfStamp identifies the file behind os.Executable(): its size and mtime,
// which between them change for any upgrade worth restarting for. Empty when
// there's nothing to stat. ponytail: package var so tests can move the binary
// without installing one; a content hash would be surer, but this runs every
// interval and no upgrade lands on the same size and nanosecond.
var selfStamp = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d@%d", fi.Size(), fi.ModTime().UnixNano())
}

// selfVersion asks the new binary what version it is, which is the half of
// "restart me" worth reading: it names what you'd be restarting into. Anything
// that isn't a first line of plausible length is dropped — this is the freshly
// installed file talking, and the bar has one line to give it. The timeout is
// for a binary that's mid-install and does something stranger than fail.
func selfVersion() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return truncate(line, 40)
}

// divergence is gitDivergence for whichever repo the selection points at, kept
// alongside the list so that walking the rows doesn't shell out to git twice
// per keypress. Keyed by repo because under -g each row may have its own.
func (u *ui) divergence() string {
	dir := u.gitDir()
	if d, ok := u.div[dir]; ok {
		return d
	}
	if u.div == nil {
		u.div = map[string]string{}
	}
	u.div[dir] = gitDivergence(dir)
	return u.div[dir]
}

// termSize is the frame's canvas, falling back to a conventional terminal when
// there is nothing to measure. ponytail: package var so tests can pick a size.
var termSize = func() (cols, rows int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80, 24
	}
	return cols, rows
}

// rowBar is the background the selection sits on: gruvbox's selection teal,
// with a light foreground so the row stays legible on it. Reverse video would
// be the portable choice, but a whole row of it is a black slab, and this one
// moves under the arrows. ponytail: truecolor, no 256-color fallback — every
// terminal worth running a TUI in has had it for a decade.
const rowBar = "\x1b[48;2;44;100;118m\x1b[38;2;251;241;199m"

// draw is one line as it goes on the screen: cut to the terminal width, every
// match of re picked out, and — when bar is set — laid on that background edge
// to edge, so the selection reads as a band across the screen rather than
// stopping at the last commit subject.
//
// A match is drawn against whatever it sits on: reverse video on a bare row,
// and a hole punched back out of the band on a barred one. "Off" is a plain
// reset either way, which is a good deal more portable than SGR 27/49.
//
// The escapes go on after the cut, since they aren't printable width — and a
// pattern that can match the empty string is left alone rather than wrapping
// every position in an invisible pair of them.
func draw(line string, cols int, re *regexp.Regexp, bar string) string {
	r := []rune(line)
	if len(r) > cols {
		r = r[:max(cols, 0)]
	}
	on, off := "\x1b[7m", "\x1b[0m"
	if bar != "" {
		on, off = off, bar
	}
	line = string(r)
	if re != nil && !re.MatchString("") {
		line = re.ReplaceAllString(line, on+"$0"+off)
	}
	if bar == "" {
		return line
	}
	return bar + line + strings.Repeat(" ", max(cols-len(r), 0)) + "\x1b[0m"
}

// highlight is draw for the lines that are a bar in their own right — the
// status line, which has no rows and no matches on it. It stays reverse video:
// it's a fixture at the bottom of the screen, not something the cursor lands on.
func highlight(line string, cols int) string { return draw(line, cols, nil, "\x1b[7m") }

// mouseRow returns the 1-based screen row of a left-button press in the SGR
// mouse report k (ESC [ < button ; col ; row M), or 0 when k is anything else
// — a release (which ends in "m"), a wheel tick, or an ordinary keystroke.
func mouseRow(k string) int {
	rest, ok := strings.CutPrefix(k, "\x1b[<")
	if !ok || !strings.HasSuffix(rest, "M") {
		return 0
	}
	f := strings.Split(strings.TrimSuffix(rest, "M"), ";")
	if len(f) != 3 || f[0] != "0" {
		return 0
	}
	row, err := strconv.Atoi(f[2])
	if err != nil {
		return 0
	}
	return row
}

// readKeys pumps stdin into a channel, one keystroke per message.
//
// ponytail: the goroutine is left blocked on Read at exit — it dies with the
// process, and this is the whole program, not a library.
func readKeys() <-chan string {
	keys := make(chan string, 64)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			for _, k := range splitKeys(string(buf[:n])) {
				keys <- k
			}
		}
	}()
	return keys
}

// splitKeys cuts one read into the keystrokes it holds: a CSI sequence
// (ESC [ … final byte) is one keystroke, any other byte is one.
//
// A read is not a keystroke. Hold an arrow key down and it repeats faster than
// a frame takes to draw, so the tty hands over several sequences at once — and
// a chunk of three "ESC [ B"s matches no binding, so before this the list
// didn't move at all while a key was held. -g made it worse rather than caused
// it: four repos of worktrees take longer to re-read, so the window in which
// keystrokes pile up is wider.
func splitKeys(s string) []string {
	var keys []string
	for len(s) > 0 {
		n := 1
		if strings.HasPrefix(s, "\x1b[") {
			// Parameter bytes run until the final byte, 0x40–0x7e: "\x1b[B" for
			// an arrow, "\x1b[<0;12;5M" for a mouse report.
			for n = 2; n < len(s) && (s[n] < 0x40 || s[n] > 0x7e); n++ {
			}
			n = min(n+1, len(s))
		}
		keys = append(keys, s[:n])
		s = s[n:]
	}
	return keys
}

// statusBar is one reverse-video line: what the keys do, then either a
// transient message (the result of a pull) or div, how far the branch has
// drifted from its upstream, cut or padded to exactly the terminal width. The
// per-worktree actions only appear once there's a worktree to apply them to,
// and the herdr ones only when there's a herdr to open a workspace in. A
// section header has nothing to open and nothing to remove — ↵ folds it, and
// that's all it takes.
func statusBar(cols int, msg string, sel listRow, div string) string {
	herdr := underHerdr()
	keys := " ccwt  q:quit  p:pull  /:search"
	if herdr {
		keys += "  x:new"
	}
	switch {
	case sel.path != "":
		if herdr {
			keys += "  space:open"
		}
		keys += "  r:remove"
	case sel.project != "":
		keys += "  ↵:fold"
	}
	if msg == "" {
		msg = div
	}
	return highlight(keys+" │ "+msg+" ", cols)
}

// gitDivergence reports dir's branch and how far it has drifted from its
// upstream, e.g. "main ↑2 ↓1". rev-list gives both numbers in one shot and,
// unlike `git status`, doesn't refresh the index — which matters when this
// runs every couple of seconds in a background pane.
func gitDivergence(dir string) string {
	branch := gitLine(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		return ""
	}
	// HEAD...@{u}: left count is commits we have and upstream doesn't (ahead),
	// right count is the reverse (behind). Fails when there's no upstream.
	counts := strings.Fields(gitLine(dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"))
	switch {
	case len(counts) != 2:
		return branch + "  (no upstream)"
	case counts[0] == "0" && counts[1] == "0":
		return branch + "  in sync"
	default:
		return fmt.Sprintf("%s  ↑%s ↓%s", branch, counts[0], counts[1])
	}
}

// gitLine runs git in dir and returns its first line, or "" if it failed — as
// an empty dir does, which is how "there is no repo to ask" reaches the bar.
func gitLine(dir string, args ...string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

// fetchMain refreshes origin/main in the background, so the ahead/behind counts
// in the status bar drift towards what's actually on the remote instead of
// showing whatever the last hand-run fetch left behind.
//
// Just the one branch: a bare `git fetch` walks every ref the remote has, and
// this runs unattended in a pane. Failures are dropped — offline simply means
// the counts stay put, and the one-line bar has better things to say than that
// a background fetch didn't work. The sleep comes after the fetch rather than
// from a ticker, which is also what keeps a slow fetch off the next one's heels.
//
// dirs are the repos to fetch — the configured projects under -g, and nil for
// the one the tui is running in.
//
// ponytail: "main" literally, so a master-branch repo fetches nothing at all.
// Ask git for the remote's HEAD if that ever comes up.
func fetchMain(ctx context.Context, every time.Duration, dirs []string) {
	if every <= 0 {
		return
	}
	if dirs == nil {
		dirs = []string{"."}
	}
	for {
		// origin main, not origin main:refs/remotes/origin/main: with the
		// refspec a clone already has, git updates the remote-tracking branch
		// on its own, and without forcing it past a rewritten history.
		for _, dir := range dirs {
			cmd := exec.CommandContext(ctx, "git", "fetch", "--quiet", "origin", "main")
			cmd.Dir = dir
			_ = cmd.Run()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// gitPull runs a pull in dir and boils its chatter down to the one line the bar
// has room for: on success the first line says what happened ("Already up to
// date.", "Updating a1b2..c3d4"), on failure the last line is git's reason.
func gitPull(dir string) string {
	if dir == "" {
		return "no worktree selected"
	}
	cmd := exec.Command("git", "pull")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err != nil {
		return "pull failed: " + strings.TrimSpace(lines[len(lines)-1])
	}
	return "pull: " + strings.TrimSpace(lines[0])
}

// underHerdr reports whether the tui is running inside a herdr pane — herdr
// exports HERDR_ENV into every pane it spawns. Outside one there is no session
// for `herdr worktree open` to put a workspace in, so the actions that call it
// are hidden and their keys ignored rather than left to fail on every press.
func underHerdr() bool { return os.Getenv("HERDR_ENV") != "" }

// newWorktree is the herdr plugin's new-worktree action from the inside: create
// a worktree of the selected row's project, open it. --force-create in spirit,
// since the tui is usually run from inside the repo and sometimes from inside a
// worktree, and either way what "new" should do there is make a fresh one.
func (u *ui) newWorktree() string {
	root, err := u.root()
	if err != nil {
		return "new failed: " + err.Error()
	}
	path, _, err := (&NewWorktreeBranchCmd{ForceCreate: true}).create(root)
	if err != nil {
		return "new failed: " + err.Error()
	}
	return herdrOpen(path, filepath.Base(path))
}

// herdrOpen opens the worktree at path as its own herdr workspace, the same way
// the herdr plugin's new-worktree action does — including handing it the
// enclosing repo root as --cwd, since herdr refuses to spawn a workspace from
// inside a linked worktree.
//
// label names the workspace, and belongs only to the call that brings it into
// existence: herdr shows a worktree workspace under its repo by branch, so
// without one the sidebar reads "worktree-<name>" (or whatever branch_prefix
// says) instead of <name>. On a reopen it is "" — a --label there would pin a
// custom name over whatever the user had renamed the workspace to since.
func herdrOpen(path, label string) string {
	root, ok := gitutil.ClaudeWorktreeRepoRoot(path)
	if !ok {
		return "open failed: " + path + " is not a Claude Code worktree"
	}
	args := []string{"worktree", "open", "--cwd", root, "--path", path, "--focus"}
	if label != "" {
		args = append(args, "--label", label)
	}
	out, err := exec.Command(herdrBin(), args...).CombinedOutput()
	if err != nil {
		return "open failed: " + lastLine(out, err)
	}
	return "opened " + filepath.Base(path)
}

// herdrBin is the herdr to talk to: plugin actions are handed one in
// HERDR_BIN_PATH, everyone else has it on PATH.
func herdrBin() string {
	if bin := os.Getenv("HERDR_BIN_PATH"); bin != "" {
		return bin
	}
	return "herdr"
}

// herdrBusy is the set of cwds herdr has an agent mid-task in: "working", or
// "blocked" on a question it is waiting for an answer to. Both mean live work
// that no git check can see — a branch made a minute ago, nothing committed to
// it yet, looks pristine right up until the removal pulls the floor out from
// under the agent. Asking herdr rather than the agent means it holds for
// whatever herdr is running there, not just Claude Code.
//
// Our own workspace doesn't count: when the agent itself runs `ccwt done`, it
// is the working agent, and counting it would leave it unable to clean up
// after itself. No herdr, or none running, is nobody working.
//
// ponytail: package var so tests can fake the herdr answer.
var herdrBusy = func() map[string]bool {
	busy := map[string]bool{}
	out, err := exec.Command(herdrBin(), "agent", "list").Output()
	if err != nil {
		return busy
	}
	var resp struct {
		Result struct {
			Agents []struct {
				Status     string `json:"agent_status"`
				Cwd        string `json:"cwd"`
				Foreground string `json:"foreground_cwd"`
				Workspace  string `json:"workspace_id"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return busy
	}
	self := os.Getenv("HERDR_WORKSPACE_ID")
	for _, a := range resp.Result.Agents {
		if a.Status != "working" && a.Status != "blocked" {
			continue
		}
		if self != "" && a.Workspace == self {
			continue
		}
		// Both cwds: an agent started at the repo root and cd'd into a worktree
		// is only in the pane's cwd as the foreground process's.
		busy[a.Cwd], busy[a.Foreground] = true, true
	}
	delete(busy, "")
	return busy
}

// herdrClose closes the workspace herdr has open on the worktree at path —
// which is what ends the agent running in it — so that removing the directory
// doesn't leave a workspace sitting in a hole. A worktree nobody has open is a
// no-op.
//
// Our own workspace is left alone: closing it would kill this process before it
// got as far as removing the worktree. `remove .` from inside its own workspace
// keeps doing what it did before — cd the shell out to the repo root.
func herdrClose(root, path string) error {
	out, err := exec.Command(herdrBin(), "worktree", "list", "--cwd", root).Output()
	if err != nil {
		return fmt.Errorf("herdr worktree list: %w", err)
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
		return fmt.Errorf("herdr worktree list: %w", err)
	}
	for _, wt := range resp.Result.Worktrees {
		if wt.Path != path || wt.Workspace == "" || wt.Workspace == os.Getenv("HERDR_WORKSPACE_ID") {
			continue
		}
		if out, err := exec.Command(herdrBin(), "workspace", "close", wt.Workspace).CombinedOutput(); err != nil {
			return fmt.Errorf("herdr workspace close %s: %s", wt.Workspace, lastLine(out, err))
		}
	}
	return nil
}

// removeWorktree is `ccwt remove <name>` against the worktree's own project,
// refusal and all: an unmerged branch comes back as the same error it prints on
// the command line, so the tui never deletes something the cli wouldn't.
func removeWorktree(path string) (string, bool) {
	root, ok := gitutil.ClaudeWorktreeRepoRoot(path)
	if !ok {
		return "remove failed: " + path + " is not a Claude Code worktree", false
	}
	name := filepath.Base(path)
	if err := (&RemoveCmd{Name: name}).remove(root); err != nil {
		return "remove failed: " + err.Error(), false
	}
	return "removed " + name, true
}

// lastLine picks the line of a failed command's output most likely to say why,
// falling back to the exit status when it printed nothing at all.
func lastLine(out []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return err.Error()
}

// paint draws the frame at the top of the screen. It never clears first:
// erasing the screen and then filling it leaves a blank gap for one frame,
// which is exactly the flicker we're avoiding. Instead each line overwrites the
// old one and erases whatever trailed it (ESC[K), and ESC[J drops any leftover
// lines below a now-shorter frame. No newline after the last line — on the
// bottom row that would scroll the screen.
func paint(w io.Writer, lines []string) {
	io.WriteString(w, "\x1b[H")
	for i, line := range lines {
		if i > 0 {
			io.WriteString(w, "\r\n")
		}
		io.WriteString(w, line)
		io.WriteString(w, "\x1b[K")
	}
	io.WriteString(w, "\x1b[J")
}
