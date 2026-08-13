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
	"unicode"
	"unicode/utf8"

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
	var next chan<- struct{}
	var raw *term.State // the terminal as it was, to give back while $EDITOR has it
	if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		raw = state
		defer term.Restore(int(os.Stdin.Fd()), state)
		keys, next = readKeys()
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
	// open is what space does: show the worktree as a workspace, and on a
	// "<new>" row make the worktree that prompt has been waiting for first.
	open := func() error {
		path := u.sel.path
		if !underHerdr() {
			return nil
		}
		switch {
		case u.sel.pending():
			return act("starting…", u.startPending)
		case u.sel.worktree():
			return act("opening "+filepath.Base(path)+"…", func() string { return herdrOpen(path, "") })
		}
		return nil
	}
	// activate is what a double-click does to whatever row it landed on: fold a
	// project section shut, or open a worktree as a workspace. The keyboard keeps
	// the two apart — space only opens, ↵ only folds — so that a fold can't
	// misfire into opening a worktree; a click can't, since it names the row it
	// means.
	activate := func() error {
		if u.sel.worktree() || u.sel.pending() {
			return open()
		}
		u.toggle()
		return nil
	}
	// external is Ctrl-G: hand the prompt being typed to $EDITOR and take back
	// what comes out of it. Claude Code's own prompt box binds that key to the
	// same thing, and a box you type a prompt into is a box you reach for the
	// same reflex in.
	//
	// The tui gives the terminal back for as long as the editor has it — cooked
	// mode, the shell's screen, no mouse reporting — and takes all three again
	// after. Nothing of ours is reading stdin meanwhile: the key that got us here
	// hasn't been acknowledged yet, so readKeys is parked. Only a key can get
	// here, and there are no keys without a terminal, so raw is set.
	external := func() {
		fmt.Print("\x1b[?1006l\x1b[?1000l\x1b[?7h\x1b[?25h\x1b[?1049l")
		term.Restore(int(os.Stdin.Fd()), raw)
		text, err := externalEdit(u.entry.text)
		term.MakeRaw(int(os.Stdin.Fd()))
		fmt.Print("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1000h\x1b[?1006h")

		last = "" // the editor drew over the frame, so redraw all of it
		u.entry.text, u.entry.cur = text, len(text)
		if err != nil {
			u.msg = "editor failed: " + err.Error()
		}
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
			// While either prompt is up every keystroke is text, so these come
			// first: `q` there is a letter, not the quit key.
			case u.entry.open && k == "\x07": // Ctrl-G: finish the prompt in $EDITOR
				external()
			case u.entry.open:
				u.queue(k)
			case u.typing:
				u.edit(k)
			// A removal's page sits on top of the worklog pane that opened it, so
			// it takes the keys first: esc goes back to the log, and the rest of
			// them are a pager's — a session is far longer than a screen.
			case u.page != nil:
				_, rows := termSize()
				switch k {
				case "\x1b":
					u.page = nil
				case "\x03":
					return nil
				case "\x1b[A", "k":
					u.page.top--
				case "\x1b[B", "j":
					u.page.top++
				case "\x1b[5~", "b":
					u.page.top -= max(rows-3, 1)
				case "\x1b[6~", " ":
					u.page.top += max(rows-3, 1)
				case "g":
					u.page.top = 0
				case "G":
					// Past the end, which the frame pulls back to the last screenful
					// — the only place that knows how tall the pane came out.
					u.page.top = len(u.page.lines)
				}
			// The worklog pane is modal the same way, and closes the same way. Its
			// rows walk and open like the list's underneath it.
			case u.logOpen:
				switch k {
				case "\x1b", "l":
					u.logOpen, u.log = false, nil
					u.logSel, u.logTop = 0, 0
				case "\x03":
					return nil
				case "\x1b[A", "k":
					u.logMove(-1)
				case "\x1b[B", "j":
					u.logMove(1)
				case "\r", "\n", " ", "d":
					u.openPage()
				default:
					// A click selects the removal it landed on, and it takes a
					// second one to open it — as on the list underneath.
					if i := u.logAt(mouseRow(k)); i >= 0 && u.logClick(i) {
						u.openPage()
					}
				}
			// The details pane is modal, so it comes next: while it's up, esc
			// closes it and nothing else reaches the list underneath.
			case u.detail != nil:
				switch {
				case k == "\x1b":
					u.detail = nil
				case k == "\x03":
					return nil
				// The pane is where a long prompt is legible in full, so it's
				// where rewriting one belongs. The edit box takes the pane's
				// place rather than stacking on it: one modal at a time.
				case k == "e" && u.sel.task > 0:
					u.entry = newEntry(u.sel, u.detail[4], u.sel.task)
					u.detail = nil
				}
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
			// `n` is what to do next: queue a prompt behind the selected row —
			// a worktree, or another prompt.
			//
			// Behind nothing at all when nothing is selected: work that isn't
			// waiting for anything, so it goes straight on the list as a "<new>"
			// row — a worktree waiting to be made, which `space` then makes. A
			// section header is the same thing under -g, where the header is how
			// you say which project's worktree it is to be; with nothing selected
			// there, there's nothing to say it.
			//
			// Unless a search is in force, where `n` is vim's next match, as it
			// has to be for the pattern you just typed to be walkable at all.
			// `esc` clears the pattern and gives the key back.
			case k == "n" && u.query == "":
				if parent, err := u.queueParent(); err != nil {
					u.msg = "queue: " + err.Error()
				} else {
					u.entry = newEntry(parent, "", 0)
				}
			case k == "n":
				u.seek(u.dir)
			case k == "N":
				u.seek(-u.dir)
			// esc backs out of what the list is holding onto: the pattern, and
			// the selection with it — which is how the per-row keys, and the bar
			// that lists them, go away again.
			case k == "\x1b":
				u.search = search{} // :nohlsearch, near enough
				u.sel = listRow{}
			case k == " ": // opens a worktree, and does nothing on a section header
				if err := open(); err != nil {
					return err
				}
			case k == "\r", k == "\n": // folds a section, and does nothing on a worktree
				u.toggle()
			case k == "d":
				// Nothing selected, or a section header: no cells, no pane.
				u.detail = u.cells[u.sel]
			// `l` is what was here before: the worktrees already removed, which
			// is the only place left that says what they were about.
			case k == "l":
				log, err := u.worklog()
				if err != nil {
					u.msg = "worklog: " + err.Error()
					break
				}
				u.log, u.logOpen = log, true
			// `r` removes whatever the row stands for: the worktree, or — on a
			// queued prompt, "<new>" ones included — the prompt and the chain
			// waiting under it.
			case k == "r" && u.sel.task != 0:
				msg, ok := dropTask(u.sel.task)
				if ok {
					u.dropSelected()
				}
				u.msg = msg
				u.stale()
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
			// Only now does the next keystroke get read — see readKeys.
			next <- struct{}{}
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

	entry entry // the queue prompt, when it's up

	// What the binary on disk looked like when this tui started, and the notice
	// that goes in the bar once it stops looking like that — see checkUpgrade.
	stamp   string
	restart string

	clicked time.Time // when the last click landed on sel — see click

	// The selected worktree's cells in full, snapshotted when the details pane
	// was opened over the list; nil when it isn't open. A snapshot rather than a
	// live read so the values sit still while they're being copied out of, and
	// so a worktree that goes away underneath doesn't blank the pane.
	detail []string

	// The removals the worklog pane is showing, read when it was opened, and
	// whether it's open — which is its own flag rather than a non-nil log,
	// because an empty log is the ordinary state of a fresh install.
	//
	// Its rows select like the list's, since each has a page behind it. By index
	// rather than by identity, unlike the list's selection, because the log is a
	// snapshot taken when the pane opened and nothing re-reads it underneath.
	// first and shown are where the last frame put the pane's rows — the first
	// one's screen line and how many it drew — which is what turns a click into
	// the removal it landed on.
	log            []Removal
	logOpen        bool
	logSel, logTop int
	logFirst       int
	logShown       int

	// The page open over the worklog: one removal in full, the session that was
	// run in it included. nil when there is none.
	page *page

	// The last list read, kept so that moving the selection — which changes
	// nothing but which row is reverse-videoed — doesn't re-run the git and
	// lsof scan behind it (half a second on a big repo, once per keypress).
	// stale() drops it whenever something that would change the list happens;
	// otherwise it's the interval tick that refreshes it.
	body  []string             // rendered table, header line included
	all   []listRow            // one per body line after the header
	cols  int                  // the width body was rendered at
	div   map[string]string    // repo -> its status-bar divergence, same lifetime
	cells map[listRow][]string // row -> its cells in full, same lifetime
}

// stale drops the cached list, so the next frame reads it again — except while
// a pane is up, where the list is the frozen backdrop it sits on: a refresh
// would shuffle rows nobody can reach, and re-run the git and lsof scan behind
// a list nobody is reading. The queue prompt counts: typing a sentence takes
// long enough for several ticks, and rows sliding about behind the box you're
// typing into is nothing but distraction.
func (u *ui) stale() {
	if u.detail == nil && !u.entry.open && !u.logOpen {
		u.body = nil
	}
}

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

// edit feeds one keystroke to the search prompt: enter accepts the match,
// escape (and Ctrl-C) abandons the whole search, and anything typeKey takes as
// text re-runs the search as it goes.
func (u *ui) edit(k string) {
	switch k {
	case "\r", "\n":
		u.typing = false
		u.report(u.preview())
	case "\x1b", "\x03":
		u.search, u.sel, u.typing = u.saved, u.anchor, false
	default:
		if q := typeKey(u.query, k); q != u.query {
			u.query = q
			u.preview()
		}
	}
}

// entry is the line a queued prompt is typed into: the text so far and the row
// it will hang off, snapshotted when the prompt opened so that a tick that
// re-reads the list underneath can't move it. Kept apart from the search prompt
// because the two share only the typing: a search runs on every keystroke, a
// queued prompt is recorded once, when it's finished.
type entry struct {
	parent listRow
	text   string
	cur    int // the caret, as a byte offset into text
	open   bool
	id     int64 // the queued prompt being rewritten; 0 when this is a new one
}

// newEntry opens the box on text — empty for a new prompt, the prompt itself
// when a queued one is being rewritten — with the caret at the end of it, which
// is where typing carries on from. A constructor rather than a literal so that
// prefilled text can't be left with the caret sitting in front of it.
func newEntry(parent listRow, text string, id int64) entry {
	return entry{parent: parent, text: text, cur: len(text), open: true, id: id}
}

// queue feeds one keystroke to the queue prompt: enter records what's been
// typed, escape drops it, and everything else is for the line editor — a
// prompt is a sentence, and the word that needs changing is rarely the last
// one. A write that fails
// leaves the prompt up with the text still in it — retyping a paragraph
// because the database was busy is not a reasonable thing to ask.
//
// Emptying an existing prompt and pressing enter is not a delete: `r` on the
// row is, and it says so. Here it's the same as escape.
func (u *ui) queue(k string) {
	switch k {
	case "\r", "\n":
		if u.entry.text == "" { // nothing typed: same as abandoning it
			u.entry = entry{}
			return
		}
		err, done := error(nil), "queued"
		if u.entry.id != 0 {
			err, done = updateTask(u.entry.id, u.entry.text), "saved"
		} else {
			err = addTask(u.entry.parent, u.entry.text)
		}
		if err != nil {
			u.msg = done + " failed: " + err.Error()
			return
		}
		u.entry, u.msg = entry{}, done
		u.stale()
	case "\x1b", "\x03":
		u.entry = entry{}
	default:
		u.entry.text, u.entry.cur = lineEdit(u.entry.text, u.entry.cur, k)
	}
}

// externalEdit round-trips text through $EDITOR: a temporary file with the
// prompt in it, the editor on that file, and back whatever was saved. Anything
// that goes wrong hands back the text it was given — the prompt on screen is
// the one thing here that can't be fetched again.
//
// $VISUAL, then $EDITOR, then vi: the order every unix program that hands you a
// file to fill in has used since crontab(1). Through sh because the variable
// holds a command line rather than a path ("code -w", "emacsclient -nw"), and
// the file as an argument to it rather than pasted into it, so that a temporary
// directory with a space in its name is still one filename.
//
// ponytail: newlines are folded to spaces on the way back, because a prompt is
// one line everywhere the tui draws it — the box, the TOPIC column, the details
// pane — and a bare newline in a frame line would scroll the screen out from
// under the cursor arithmetic every later frame relies on. Teach wrapEdit and
// the table about them if a prompt ever wants paragraphs.
func externalEdit(text string) (string, error) {
	f, err := os.CreateTemp("", "ccwt-*.md") // .md: a prompt is prose
	if err != nil {
		return text, err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return text, err
	}

	editor := cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", f.Name())
	// The tui points the real stderr at /dev/null while it owns the screen, so
	// the editor's complaints go where its screen goes: the terminal on stdout.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stdout
	if err := cmd.Run(); err != nil {
		return text, err
	}

	edited, err := os.ReadFile(f.Name())
	if err != nil {
		return text, err
	}
	return strings.Join(strings.Fields(string(edited)), " "), nil
}

// lineEdit applies one keystroke to a line being typed and says where the caret
// ends up, as a byte offset into the line. Printable text goes in at the caret,
// the arrows and the readline keys move it about, and backspace and delete take
// the rune on either side of it.
//
// By byte rather than by rune because that's how a multi-byte rune arrives —
// its bytes, in order — and putting them in one after another is what makes the
// rune. The caret walks whole runes even so, so backspace can't leave half of
// one behind.
//
// Escape sequences this doesn't name (mouse reports, the function keys) are
// ignored rather than typed: they're several bytes but only the first is under
// 0x20, so they'd otherwise land in the text as garbage.
//
// ponytail: ours rather than bubbles/textarea, which would mean a bubbletea
// rewrite of the frame painter, the mouse handling and the list — a new event
// loop for the box in the middle of it. Worth reaching for if this box ever
// wants ↑/↓ across the wrap, a selection, or undo; not for a caret.
func lineEdit(s string, cur int, k string) (string, int) {
	cur = min(max(cur, 0), len(s))
	back := func() int { _, n := utf8.DecodeLastRuneInString(s[:cur]); return cur - n }
	fwd := func() int { _, n := utf8.DecodeRuneInString(s[cur:]); return cur + n }
	switch k {
	case "\x1b[D", "\x02": // ←, Ctrl-B
		return s, back()
	case "\x1b[C", "\x06": // →, Ctrl-F
		return s, fwd()
	case "\x1b[1;5D", "\x1b[1;3D": // Ctrl-← and Alt-←, as terminals report them
		return s, wordLeft(s[:cur])
	case "\x1b[1;5C", "\x1b[1;3C":
		return s, wordRight(s, cur)
	case "\x1b[H", "\x1b[1~", "\x01": // home, Ctrl-A
		return s, 0
	case "\x1b[F", "\x1b[4~", "\x05": // end, Ctrl-E
		return s, len(s)
	case "\x7f", "\b": // backspace: the rune before the caret
		at := back()
		return s[:at] + s[cur:], at
	case "\x1b[3~", "\x04": // delete, Ctrl-D: the one after it
		return s[:cur] + s[fwd():], cur
	case "\x17": // Ctrl-W: the word before it
		at := wordLeft(s[:cur])
		return s[:at] + s[cur:], at
	case "\x15": // Ctrl-U: all of it before
		return s[cur:], 0
	case "\x0b": // Ctrl-K: all of it after
		return s[:cur], cur
	}
	if len(k) == 1 && k[0] >= ' ' {
		return s[:cur] + k + s[cur:], cur + 1
	}
	return s, cur
}

// wordLeft is where the word at the end of s starts, over any spaces first —
// what Ctrl-W rubs out, and what Ctrl-← steps back to.
func wordLeft(s string) int {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	return len(strings.TrimRightFunc(s, notSpace))
}

// wordRight is the far end of the word after cur, the same way round.
func wordRight(s string, cur int) int {
	rest := strings.TrimLeftFunc(s[cur:], unicode.IsSpace)
	return len(s) - len(strings.TrimLeftFunc(rest, notSpace))
}

func notSpace(r rune) bool { return !unicode.IsSpace(r) }

// typeKey applies one keystroke to a line with no caret to move — the search
// prompt, where the text only ever grows and shrinks at its end.
func typeKey(s, k string) string {
	s, _ = lineEdit(s, len(s), k)
	return s
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
	if !u.sel.section() {
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

// queueParent is the row a new prompt hangs off: the selected worktree, or the
// selected prompt when a chain is being extended — and with nothing selected,
// no row at all, only the project it belongs to, which is what makes it a
// "<new>" row of its own rather than something waiting on a row above it.
//
// Under -g that project has to be selected, its section header being enough:
// with a dozen repos on screen there is otherwise nothing to say which one's
// worktree the prompt is asking for.
func (u *ui) queueParent() (listRow, error) {
	if u.sel.worktree() || u.sel.task != 0 {
		return u.sel, nil
	}
	root, err := u.root()
	if err != nil {
		return listRow{}, errors.New("select a project first")
	}
	return listRow{project: root}, nil
}

// worklog is the removals of the projects in view, newest first: the repo the
// tui is running in, or all the configured ones under -g — the same span the
// list itself covers, since the pane is a look back at that list.
func (u *ui) worklog() ([]Removal, error) {
	projects, err := worklogProjects(u.projects)
	if err != nil {
		return nil, err
	}
	return loadWorklog(projects, worklogLimit)
}

// logMove walks the worklog pane's selection by d rows, stopping at either end
// rather than wrapping — the same walk as the list's, minus the identity, since
// the log doesn't move under the selection.
func (u *ui) logMove(d int) {
	u.logSel = min(max(u.logSel+d, 0), max(len(u.log)-1, 0))
}

// logAt is the removal on screen line n, counting the way a mouse report does,
// and -1 for a line that isn't one of the pane's rows — its border, its header,
// or the list still showing around it.
func (u *ui) logAt(n int) int {
	if n < u.logFirst || n >= u.logFirst+u.logShown {
		return -1
	}
	return u.logTop + n - u.logFirst
}

// logClick selects removal i and reports whether it was the second click on it
// in quick succession — the one that opens its page. The list's clock, since
// only one of the two lists is ever taking clicks.
func (u *ui) logClick(i int) bool {
	double := i == u.logSel && time.Since(u.clicked) < doubleClick
	u.logSel, u.clicked = i, time.Now()
	if double {
		u.clicked = time.Time{} // a third click starts a fresh pair
	}
	return double
}

// openPage opens the selected removal's page over the worklog. An empty log has
// nothing to open, which is the ordinary state of a fresh install.
func (u *ui) openPage() {
	if u.logSel >= 0 && u.logSel < len(u.log) {
		u.page = removalPage(u.log[u.logSel])
	}
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
		listRows, cells, err := renderList(&buf, true, cols, u.projects, u.collapsed, true)
		if err != nil {
			return nil, err
		}
		u.body = strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		u.all, u.cols, u.div, u.cells = listRows, cols, nil, cells
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

	// The details pane is a modal: it lands on top of the list, which goes on
	// being drawn — and, since stale() leaves the cache alone while it's up,
	// stands still — behind it. The pane's own lines are the only ones it
	// replaces; the rest of the screen is the list you're still in.
	if u.detail != nil {
		for i, l := range detailPane(u.detail, cols, body) {
			if l != "" && i < body {
				lines[i] = l
			}
		}
		keys := " esc:close "
		if u.sel.task > 0 { // a queued prompt: its text is ours to rewrite
			keys = " e:edit  esc:close "
		}
		return append(lines[:body], highlight(keys, cols)), nil
	}

	// A removal's page is over the worklog pane that opened it, so it goes on
	// first. It fills the frame rather than sitting in it: what's in it is a
	// whole session, and a window the size of the details pane would be a
	// keyhole to read one through.
	if u.page != nil {
		for i, l := range pagePane(u.page, cols, body) {
			if l != "" && i < body {
				lines[i] = l
			}
		}
		return append(lines[:body], highlight(" ↑↓:scroll  space/b:page  g/G:ends  esc:back ", cols)), nil
	}

	// The worklog is a modal of the same kind, over the same standing list: what
	// was removed, under what is still there. Its rows carry a selection like the
	// list's, each having a page of its own behind it, so the window follows that
	// selection the same way the list's does.
	if u.logOpen {
		fits := logFits(body)
		u.logSel = min(max(u.logSel, 0), max(len(u.log)-1, 0))
		u.logTop = min(max(u.logTop, 0), max(len(u.log)-fits, 0))
		u.logTop = min(max(u.logTop, u.logSel-fits+1), u.logSel)
		pane, first := logPane(u.log, u.logSel, u.logTop, cols, body)
		u.logFirst, u.logShown = first+1, min(fits, len(u.log)) // +1: a mouse counts from 1
		for i, l := range pane {
			if l != "" && i < body {
				lines[i] = l
			}
		}
		keys := " esc:close "
		if len(u.log) > 0 {
			keys = " ↑↓:select  ↵:details  esc:close "
		}
		return append(lines[:body], highlight(keys, cols)), nil
	}

	// The queue prompt is a modal of the same kind, and for the same reason: the
	// row the prompt will hang off is one of the ones still showing around it.
	// A search goes in the bar because it acts on the list as you type it; this
	// doesn't act on anything until it's finished, and it's a sentence rather
	// than a pattern, so it gets the room to be read back.
	if u.entry.open {
		for i, l := range entryPane(u.entry.text, u.entry.cur, u.entryTitle(), cols, body) {
			if l != "" && i < body {
				lines[i] = l
			}
		}
		keys := " ↵:queue  ctrl-g:$EDITOR  esc:cancel "
		if u.entry.id != 0 {
			keys = " ↵:save  ctrl-g:$EDITOR  esc:cancel "
		}
		// The bar is the only line left to say why the editor didn't open.
		return append(lines[:body], highlight(keys+u.msg, cols)), nil
	}

	// While the search prompt is up it takes the whole bar, as it does in vim:
	// the key hints have nothing to say about a line you're typing into.
	// The restart notice outlasts the transient messages but yields to them
	// while one is up: they're a second old, and it will still be true after.
	bar := statusBar(cols, cmp.Or(u.msg, u.restart), u.sel, u.divergence(), u.query != "", u.projects != nil)
	if u.typing {
		p := "/"
		if u.dir < 0 {
			p = "?"
		}
		bar = highlight(p+u.query, cols)
	}
	return append(lines[:body], bar), nil
}

// entryTitle is what the queue prompt's top rule says it is queueing behind:
// the worktree, or — behind another prompt — that prompt, since one worktree's
// chain can have several links and "after dreamy-foraging-hickey" wouldn't say
// which. Empty until the list has been drawn once, which in the tui it always
// has: the prompt opens on a row that was on screen.
func (u *ui) entryTitle() string {
	cells := u.cells[u.entry.parent]
	switch {
	case u.entry.id != 0:
		return "edit" // rewriting one, not queueing behind it
	case u.entry.parent.path == "" && u.entry.parent.task == 0:
		return newName // behind nothing: it is the row it will become
	case len(cells) == 0:
		return ""
	case u.entry.parent.task > 0:
		return cells[4] // TOPIC: the prompt this one waits on
	}
	return cells[0] // NAME: the worktree
}

// detailPane is the details modal: the selected worktree's cells laid out one
// per line and wrapped to the width, rather than as the row the table had to
// cut them down to fit. Every column the table knows about, not just the ones
// the config draws — hiding a column from the table is about what the list is
// for, and the pane is where you go for the value itself.
//
// It's drawn as a window over the list rather than a screen of its own, so it
// reads as a look at one row of the list you're still in. The lines it returns
// are the window and nothing else — the frame lays them over the rows, and
// leaves the rest of them showing. The margin gives way first on a small
// terminal, then the border stays: the values are the point, the framing isn't.
// Its top edge names the worktree.
//
// The border is the price of that: a drag-select that overshoots the value
// picks up a "│", and copying a value out is what the pane is for. Landing it
// a cell out from the text keeps an ordinary drag clear of it.
//
// ponytail: no scrolling — five values fit any terminal worth running the tui
// in. Give it a window like the list's if the pane grows. And the margin is
// blank rather than the list showing through it: splicing rows that carry the
// selection band and the search highlights back in around the border is a lot
// of code for four cells either side.
func detailPane(cells []string, cols, rows int) []string {
	all := allColumns()
	label := 0
	for _, c := range all {
		label = max(label, len(c.name))
	}
	pad, inner := paneWidth(cols)
	row := paneRow(pad, inner)

	var body []string
	for _, c := range all {
		head := fmt.Sprintf(" %-*s  ", label, c.name)
		for _, l := range wrap(cells[c.i], max(inner-len(head)-1, 1)) {
			body = append(body, row(head+l))
			head = strings.Repeat(" ", len(head))
		}
	}

	lines := paneBox(pad, inner, cells[0], body)
	// Vertical margin only out of what's left over — the values come first.
	return append(make([]string, min(2, max(rows-len(lines), 0)/2)), lines...)
}

// logPane is the worklog modal: the removals as the table worklogTable makes
// of them, in a window over the list, fitted to the pane rather than to the
// terminal. Same border and margin as the details pane, since it is the same
// kind of look aside from the list.
//
// The rows select like the list's — the band on sel, the window starting at top
// — because each of them has a page behind it. first is the line the topmost
// row came out on, which is what turns a click on the pane back into the
// removal it hit; the caller keeps top inside the log, this only slices.
func logPane(log []Removal, sel, top, cols, rows int) (lines []string, first int) {
	pad, inner := paneWidth(cols)
	row := paneRow(pad, inner)

	// Line 0 of the table is its header, which stays put while the rest scrolls
	// — and is the whole of it when the log is empty, where it is the line that
	// says so.
	table := worklogTable(log, max(inner-2, 1))
	head, rest := table[0], table[1:]
	fits := logFits(rows)
	top = min(max(top, 0), max(len(rest)-fits, 0))
	body := []string{row(" " + head)}
	for i, l := range rest[top:min(top+fits, len(rest))] {
		if l = row(" " + l); top+i == sel {
			l = band(l)
		}
		body = append(body, l)
	}

	lines = paneBox(pad, inner, "worklog", body)
	margin := min(2, max(rows-len(lines), 0)/2)
	// The rows start after the margin, the top rule and the header.
	return append(make([]string, margin), lines...), margin + 2
}

// logFits is how many removals a worklog pane rows lines tall has room for: its
// two rules and the table's header take a line each. The frame needs the same
// number to keep the selection inside the window, so there is one of it.
func logFits(rows int) int { return max(rows-3, 1) }

// page is a read of something far longer than a pane: a title, the text as it
// was built, and a window onto it. The worklog's is the one there is — what was
// recorded about a removed worktree, and then the whole Claude Code session
// that was run in it.
//
// The text is kept as it was built and wrapped again whenever the terminal
// changes width, so that top counts lines of the screen rather than lines of
// the source: paging by a screenful, and stopping at the end, are otherwise
// arithmetic nobody can do.
type page struct {
	title string
	text  []string // as built, before it was fitted to any width
	lines []string // text wrapped to width, which is what the window slices
	width int      // what lines was wrapped to
	top   int      // lines[top] is the first line showing
}

// window is the slice of the page showing in a pane width columns wide and rows
// tall, re-wrapping first if the width has changed since the last frame. It
// pulls top back to the last screenful, which is what stops the end of a
// session scrolling off the bottom — and is why the keys can scroll by any
// amount and leave the clamping here.
func (p *page) window(width, rows int) []string {
	if p.lines == nil || width != p.width {
		p.lines, p.width = nil, width
		for _, l := range p.text {
			p.lines = append(p.lines, wrapIndent(l, width)...)
		}
	}
	p.top = min(max(p.top, 0), max(len(p.lines)-rows, 0))
	return p.lines[p.top:min(p.top+rows, len(p.lines))]
}

// pagePane draws the page in the same border and margin as the other panes — it
// is the same kind of look aside from the list — but as tall as the frame, a
// session being no more readable through a keyhole than a book is.
func pagePane(p *page, cols, rows int) []string {
	pad, inner := paneWidth(cols)
	row := paneRow(pad, inner)

	var body []string
	for _, l := range p.window(max(inner-2, 1), max(rows-2, 1)) { // the two rules
		body = append(body, row(" "+l))
	}
	return paneBox(pad, inner, p.title, body)
}

// removalPage is everything left to know about a removed worktree: the line the
// worklog kept — when it went, what it was called, what it was about — and then
// the last Claude Code session that ran in it, which survives the removal
// because Claude Code files transcripts under the home directory rather than in
// the tree they were about.
//
// The path is rebuilt rather than recorded: it is where `new` puts a worktree,
// where `remove` took this one from, and what the transcript is keyed by.
//
// ponytail: the newest session, not every session the worktree ever had — one
// worktree is one piece of work, and the run that finished it is the last one.
// Read the whole directory if that stops being true.
func removalPage(r Removal) *page {
	path := filepath.Join(r.Project, ".claude", "worktrees", r.Name)
	transcript, _ := newestTranscript(path)
	text := []string{
		"NAME     " + r.Name,
		"PROJECT  " + r.Project,
		"REMOVED  " + humanAge(time.Since(r.At)) + " ago, " + r.At.Format("2006-01-02 15:04"),
		"PATH     " + path,
		"TOPIC    " + r.Topic,
		"SESSION  " + cmp.Or(shortenHome(transcript), "none"),
	}
	if lines := transcriptLines(transcript); len(lines) > 0 {
		text = append(text, lines...)
	} else {
		text = append(text, "", "  (no Claude Code session was recorded in this worktree)")
	}
	return &page{title: r.Name, text: text}
}

// paneWidth is where a pane's edges fall on a terminal cols wide: a few cells
// of margin, where there is room for them, and the interior left between the
// borders. Shared, so that the panes line up with one another — they are the
// same window onto different things.
func paneWidth(cols int) (pad string, inner int) {
	mx := min(4, max(cols-64, 0)/2)
	return strings.Repeat(" ", mx), max(cols-2*mx-2, 1)
}

// band lays the selection colour across the interior of a pane line — between
// the borders, which paneRow has already padded the text out to, so that the
// row lights up without the box around it moving.
func band(line string) string {
	i, j := strings.Index(line, "│"), strings.LastIndex(line, "│")
	if i < 0 || i == j {
		return line
	}
	i += len("│")
	return line[:i] + rowBar + line[i:j] + "\x1b[0m" + line[j:]
}

// paneRow draws one line of a pane's interior: s inside the border, cut or
// padded to the pane's width. Shared by the panes so their edges line up.
func paneRow(pad string, inner int) func(string) string {
	return func(s string) string {
		r := []rune(s)
		if len(r) > inner {
			r = r[:inner]
		}
		return pad + "│" + string(r) + strings.Repeat(" ", inner-len(r)) + "│"
	}
}

// paneBox puts the rules on the top and bottom of a pane's body, with title in
// the top one.
func paneBox(pad string, inner int, title string, body []string) []string {
	t := []rune("─ " + title + " ")
	if len(t) > inner {
		t = t[:inner]
	}
	lines := append([]string{pad + "┌" + string(t) + strings.Repeat("─", inner-len(t)) + "┐"}, body...)
	return append(lines, pad+"└"+strings.Repeat("─", inner)+"┘")
}

// entryPane is the queue prompt: the text as it's typed, in a window over the
// middle of the list. A window rather than a line in the status bar because
// what you type here isn't a pattern acting on the list as it goes — it's a
// sentence, worth the room to read back before committing to it, and the row
// it will hang off is one of the ones still showing around the box.
//
// Three fifths of the terminal, centred, and that size whatever is in it: a box
// that grew as you typed would shift the list behind it line by line. Text past
// what it holds scrolls, keeping the caret in view.
func entryPane(text string, cur int, title string, cols, rows int) []string {
	w := min(max(cols*3/5, 24), cols)
	h := min(max(rows*3/5, 3), rows)
	inner := max(w-2, 1)
	pad := strings.Repeat(" ", max(cols-w, 0)/2)
	row := paneRow(pad, inner)

	lines, at, col := wrapEdit(text, max(inner-2, 1), min(max(cur, 0), len(text)))

	// The window follows the caret rather than the end of the text: a prompt
	// too tall for the box is exactly the one worth rewriting the middle of.
	//
	// ponytail: which leaves the caret on the bottom edge for as long as the
	// text above it is taller than the box, a window with no memory of where it
	// was last having nothing to scroll from. Keep a top in the entry if that
	// ever grates.
	visible := max(h-2, 0)
	top := max(at-visible+1, 0)
	body := make([]string, 0, visible)
	for _, l := range lines[top:] {
		if len(body) == visible {
			break
		}
		body = append(body, row(" "+l))
	}
	for len(body) < visible {
		body = append(body, row(""))
	}

	// The caret is drawn rather than left to the terminal's own: the tui hides
	// that one, and moving it would mean tracking where on the screen the text
	// happens to have wrapped to. It goes on the character it's in front of, in
	// reverse video, the way draw() picks out a match — a block between the
	// characters would take a column of its own, shunting the rest of the line
	// along and re-wrapping it every time the caret moved. Past the end of the
	// text there's a space under it, which is the block it used to be.
	//
	// The offset is into the line as it will be printed: the margin, the border,
	// and the space the text is inset by.
	if i := at - top; i >= 0 && i < len(body) {
		body[i] = invert(body[i], len(pad)+2+col)
	}

	return append(make([]string, max(rows-visible-2, 0)/2), paneBox(pad, inner, title, body)...)
}

// invert puts the ith rune of line in reverse video, and hands back the rest of
// it untouched — same width, same characters, one of them lit up.
func invert(line string, i int) string {
	r := []rune(line)
	if i < 0 || i >= len(r) {
		return line
	}
	return string(r[:i]) + "\x1b[7m" + string(r[i]) + "\x1b[0m" + string(r[i+1:])
}

// wrapEdit lays out a line being typed: the text in lines of at most width
// runes, and where the caret — a byte offset into it — has come to rest, as a
// line and a column of that line.
//
// Every character is kept, spaces included, unlike the details pane's wrap():
// a box you type into can't collapse a run of spaces without the caret landing
// somewhere the keys didn't put it. The breaks go where wrap() puts them — at a
// space, or mid-word when a single word is wider than the box.
//
// The caret is placed after the break rather than before it, so that a caret in
// front of a word that has just been pushed onto the next line goes with the
// word rather than staying behind on the line it left.
func wrapEdit(s string, width, cur int) (lines []string, row, col int) {
	var line []rune
	flush := func() { lines, line = append(lines, string(line)), nil }
	space := true // the text starts a word the way a space would
	for i, r := range s {
		// A word that would run off the end starts the next line — and if it's
		// wider than a line, it runs on and gets broken by the full check.
		pushed := space && r != ' ' && len(line) > 0 && len(line)+wordWidth(s[i:]) > width
		if len(line) == width || pushed {
			flush()
		}
		if i == cur {
			row, col = len(lines), len(line)
		}
		line, space = append(line, r), r == ' '
	}
	if cur >= len(s) { // the end of the text, which the loop never reaches
		row, col = len(lines), len(line)
	}
	if len(line) > 0 || len(lines) == 0 {
		flush()
	}
	return lines, row, col
}

// wordWidth is how many runes the word at the start of s takes.
func wordWidth(s string) int {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	return utf8.RuneCountInString(s)
}

// wrap breaks s into lines of at most width runes: at a space where there is
// one, and mid-word when a single word — a long branch name, a path — is wider
// than the pane, since a pane that cut it would be no better than the table.
// Always at least one line, so an empty value still gets its label.
func wrap(s string, width int) []string {
	var lines []string
	var line []rune
	flush := func() { lines, line = append(lines, string(line)), nil }
	for _, word := range strings.Fields(s) {
		w := []rune(word)
		if len(line) > 0 && len(line)+1+len(w) > width {
			flush()
		}
		if len(line) > 0 {
			line = append(line, ' ')
		}
		for len(line)+len(w) > width {
			n := width - len(line)
			line = append(line, w[:n]...)
			w = w[n:]
			flush()
		}
		line = append(line, w...)
	}
	if len(line) > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

// wrapIndent lays a line of a page out at width columns, putting its leading
// spaces back on every line the wrap makes of it. The indent is who is talking
// — a prompt flush left, Claude's answer set in under it — and a wrap that lost
// it would leave a page of text with nothing to say which was which.
//
// Through wrapEdit rather than wrap because a transcript is not all prose: the
// spacing inside a line is the shape of the code or the diff sitting in it, and
// collapsing runs of spaces would flatten every one of them.
func wrapIndent(s string, width int) []string {
	body := strings.TrimLeft(s, " ")
	indent := s[:len(s)-len(body)]
	lines, _, _ := wrapEdit(body, max(width-len(indent), 1), 0)
	for i, l := range lines {
		lines[i] = indent + l
	}
	return lines
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

// readKeys pumps stdin into a channel, one keystroke per message, and waits to
// be asked for the next one: the caller sends on the second channel once it has
// finished with the one it took.
//
// The acknowledgement is what makes Ctrl-G possible. A tty wakes whichever
// reader is blocked on it, so a Read of ours left pending while $EDITOR has the
// terminal would eat the keystrokes meant for the editor. Lockstep means that
// while a key is being handled there is no Read of ours to eat them, and the
// keys typed meanwhile wait in the tty's own buffer instead of this channel's.
//
// ponytail: the goroutine is left blocked at exit — on the Read, or on an
// acknowledgement that never comes — and dies with the process, which is the
// whole program, not a library.
func readKeys() (<-chan string, chan<- struct{}) {
	keys, next := make(chan string), make(chan struct{})
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			for _, k := range splitKeys(string(buf[:n])) {
				keys <- k
				<-next
			}
		}
	}()
	return keys, next
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
//
// searching says a pattern is in force, which is the one thing on this line
// that changes what a key does rather than just whether it applies: `n` walks
// the matches then, and queues a prompt the rest of the time. The bar is where
// that's visible, so it says which.
func statusBar(cols int, msg string, sel listRow, div string, searching, global bool) string {
	herdr := underHerdr()
	keys := " ccwt  q:quit  p:pull  /:search  l:log"
	// `n` queues behind whatever is selected, or as a "<new>" row of its own when
	// that's nothing — which under -g takes a project selected to say whose, so
	// there it's the one state the key has nothing to do in.
	queue := "  n:queue"
	if searching {
		keys += "  n:next  N:prev"
		queue = ""
	} else if global && sel.project == "" {
		queue = ""
	}
	if herdr {
		keys += "  x:new"
	}
	switch {
	case sel.worktree():
		if herdr {
			keys += "  space:open"
		}
		keys += queue + "  d:details  r:remove"
	case sel.pending(): // a queued prompt whose worktree space would make
		if herdr {
			keys += "  space:start"
		}
		keys += queue + "  d:details  r:delete"
	case sel.task != 0: // a queued prompt
		keys += queue + "  d:details  r:delete"
	case sel.project != "":
		keys += queue + "  ↵:fold"
	default: // nothing selected: the queue key is all that's left
		keys += queue
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

// startPending is what opening a "<new>" row does: make the worktree its prompt
// has been waiting for, hand it whatever was queued behind that prompt, open it
// as a workspace, and set the prompt running there. That is the whole of what
// the row stood for, so the prompt itself goes — from here on the row is a
// worktree like any other.
//
// The prompt comes from the row's cells rather than a re-read of the database:
// it is on screen, and it is the same value the details pane and the edit box
// take.
func (u *ui) startPending() string {
	cells := u.cells[u.sel]
	if len(cells) < 5 {
		return "start failed: no prompt on that row"
	}
	prompt := cells[4]
	argv, err := taskCommand()
	if err != nil {
		return "start failed: " + err.Error()
	}
	root, err := u.root()
	if err != nil {
		return "start failed: " + err.Error()
	}
	path, _, err := (&NewWorktreeBranchCmd{ForceCreate: true}).create(root)
	if err != nil {
		return "start failed: " + err.Error()
	}
	msg := herdrOpen(path, filepath.Base(path))
	pane := herdrPane(path)
	if pane == "" {
		return msg // no pane to run in: whatever herdrOpen said went wrong
	}
	if out, err := exec.Command(herdrBin(), append([]string{"pane", "run", pane}, append(argv, shellQuote(prompt))...)...).CombinedOutput(); err != nil {
		return "start failed: " + lastLine(out, err)
	}
	// Last, once the prompt is actually running: a promote is a delete, and
	// until this point every failure above is one the row survives — the "<new>"
	// row is still there, with the prompt still on it, to try again from.
	if err := promoteTask(u.sel.task, path); err != nil {
		return "started, but the queue still lists it: " + err.Error()
	}
	// The row the prompt was on has just become this worktree, so the selection
	// follows it there rather than being cleared by the next frame.
	u.sel = listRow{project: u.sel.project, path: path}
	return "started " + filepath.Base(path)
}

// shellQuote wraps s in single quotes for `herdr pane run`, which joins its
// COMMAND words with spaces and types the line at the pane's shell prompt.
// Unquoted, a multi-word prompt arrives at `claude` as several argv entries and
// only the first of them is taken as the prompt. ponytail: single quotes and
// the '\'' trick, the one escape that needs no table of shell metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// herdrPane is the pane sitting in the worktree at path, which after a
// `worktree open` is the one it just made — where the queued prompt is to run.
// "" when herdr hasn't got one, which is the same answer as an open that
// failed, and is handled as one.
//
// Retried for a second, since the pane is spawned by the open we have just
// returned from and takes a moment to show up in the list: giving up on the
// first look would drop the prompt on the floor on a loaded machine.
func herdrPane(path string) string {
	for wait := 50 * time.Millisecond; ; wait *= 2 {
		out, err := exec.Command(herdrBin(), "pane", "list").Output()
		if err == nil {
			var resp struct {
				Result struct {
					Panes []struct {
						ID  string `json:"pane_id"`
						Cwd string `json:"cwd"`
					} `json:"panes"`
				} `json:"result"`
			}
			if err := json.Unmarshal(out, &resp); err == nil {
				for _, p := range resp.Result.Panes {
					if p.Cwd == path {
						return p.ID
					}
				}
			}
		}
		if wait > time.Second {
			return ""
		}
		time.Sleep(wait)
	}
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
