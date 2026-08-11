package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

	u := ui{projects: projects}
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
		return nil
	}
	open := func() error {
		path := u.sel
		if path == "" || !underHerdr() {
			return nil
		}
		return act("opening "+filepath.Base(path)+"…", func() string { return herdrOpen(path) })
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
		case k := <-keys:
			switch {
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
			case k == "\r", k == "\n":
				if err := open(); err != nil {
					return err
				}
			case k == "r":
				if path := u.sel; path != "" {
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
				// A click selects the row it landed on and opens it, which is
				// the one thing you'd want a click in this list to do.
				if row := mouseRow(k); row >= 2 && row-2 < len(u.paths) {
					u.sel = u.paths[row-2]
					if err := open(); err != nil {
						return err
					}
				}
			}
		}
	}
}

// ui is the whole model: which worktree is selected, the worktrees the last
// frame showed (so a selection can be resolved back to a row), the projects the
// list spans (nil for just the repo we're in), and the transient status-bar
// message.
//
// A worktree is held by path rather than by index because the list is re-read
// and re-sorted every interval: an index would silently point at a different
// worktree once one is removed or a commit reorders the rows. By path rather
// than by name because under -g two projects can each have a "fix-tests", and
// because the path is what an action needs — the repo is the part of it in
// front of .claude/worktrees/.
type ui struct {
	sel      string // selected worktree path, "" when nothing is selected yet
	paths    []string
	projects []string
	msg      string
}

// move walks the selection by d rows, starting from the top when nothing is
// selected (or when the selected worktree has since disappeared).
func (u *ui) move(d int) {
	if len(u.paths) == 0 {
		return
	}
	i := min(max(slices.Index(u.paths, u.sel)+d, 0), len(u.paths)-1)
	u.sel = u.paths[i]
}

// root is the repo an action applies to: the selected worktree's project, or
// the repo the tui is running in when nothing is selected. Under -g there is no
// such fallback — the repos in view are the configured ones, and the directory
// ccwt happens to have been started in isn't one of them.
func (u *ui) root() (string, error) {
	if root, ok := gitutil.ClaudeWorktreeRepoRoot(u.sel); ok {
		return root, nil
	}
	if u.projects != nil {
		return "", errors.New("no worktree selected")
	}
	return gitutil.RepoRoot("", true)
}

// gitDir is the repo the whole-repo actions work on — the pull key, and the
// branch the status bar reports the drift of. "." outside -g: the directory
// ccwt was started in. Under -g the selected worktree's project instead, and ""
// (do nothing, report nothing) until something is selected, since with a dozen
// repos on screen the current directory is not the one the keys should reach.
func (u *ui) gitDir() string {
	if u.projects == nil {
		return "."
	}
	root, _ := gitutil.ClaudeWorktreeRepoRoot(u.sel)
	return root
}

// dropSelected moves the selection off the selected worktree, as it has to once
// that worktree is removed: down a row, or up one when the removed row was the
// last. The removed path is still in u.paths — the list is only re-read on the
// next frame — so this is the ordinary walk, and when it was the only row the
// selection stays on the dead path and frame clears it.
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
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		cols, rows = 80, 24
	}

	var buf bytes.Buffer
	paths, err := renderList(&buf, true, cols, u.projects)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	body := max(rows-1, 1)

	// The frame doesn't scroll: a list taller than the terminal is simply cut,
	// so only the rows on screen count as selectable. Keeping u.paths to those
	// is what stops a click below the table from opening a worktree nobody can
	// see. ponytail: add scrolling if a repo ever grows more worktrees than a
	// window has lines — which -g makes rather easier to hit.
	u.paths = paths[:min(len(paths), max(body-1, 0))]
	// Row i of the table is line i+1: line 0 is the header.
	i := slices.Index(u.paths, u.sel)
	if i < 0 {
		u.sel = "" // the selected worktree is gone (removed, or scrolled off)
	} else if i+1 < len(lines) {
		lines[i+1] = highlight(lines[i+1], cols)
	}

	for len(lines) < body {
		lines = append(lines, "")
	}
	return append(lines[:body], statusBar(cols, u.msg, u.sel, u.gitDir())), nil
}

// highlight reverse-videos a row edge to edge, so the selection reads as a bar
// across the screen rather than stopping at the last commit subject.
func highlight(line string, cols int) string {
	r := []rune(line)
	if len(r) > cols {
		r = r[:max(cols, 0)]
	}
	return "\x1b[7m" + string(r) + strings.Repeat(" ", max(cols-len(r), 0)) + "\x1b[0m"
}

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

// readKeys pumps stdin into a channel, one read per message: an arrow key or a
// mouse report arrives as a several-byte escape sequence, and the terminal
// hands the whole thing over in a single read. ponytail: a chunk holding two
// keystrokes (only really possible when pasting) counts as one — matching no
// binding, so it's ignored.
//
// ponytail: the goroutine is left blocked on Read at exit — it dies with the
// process, and this is the whole program, not a library.
func readKeys() <-chan string {
	keys := make(chan string, 8)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				keys <- string(buf[:n])
			}
		}
	}()
	return keys
}

// statusBar is one reverse-video line: what the keys do, then either a
// transient message (the result of a pull) or how far the branch has drifted
// from its upstream, cut or padded to exactly the terminal width. The
// per-worktree actions only appear once there's a worktree to apply them to,
// and the herdr ones only when there's a herdr to open a workspace in.
func statusBar(cols int, msg, sel, dir string) string {
	herdr := underHerdr()
	keys := " ccwt  q:quit  p:pull"
	if herdr {
		keys += "  x:new"
	}
	if sel != "" {
		if herdr {
			keys += "  ↵:open"
		}
		keys += "  r:remove"
	}
	if msg == "" {
		msg = gitDivergence(dir)
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
	return herdrOpen(path)
}

// herdrOpen opens the worktree at path as its own herdr workspace, the same way
// the herdr plugin's new-worktree action does — including handing it the
// enclosing repo root as --cwd, since herdr refuses to spawn a workspace from
// inside a linked worktree.
//
// No --label: herdr derives a workspace's name from its root pane's directory,
// which for a worktree is already <name>, and a --label would instead pin a
// custom override — renaming the workspace out from under someone who had
// named it themselves, every time they reopened it.
func herdrOpen(path string) string {
	root, ok := gitutil.ClaudeWorktreeRepoRoot(path)
	if !ok {
		return "open failed: " + path + " is not a Claude Code worktree"
	}
	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}
	out, err := exec.Command(herdr, "worktree", "open",
		"--cwd", root, "--path", path, "--focus").CombinedOutput()
	if err != nil {
		return "open failed: " + lastLine(out, err)
	}
	return "opened " + filepath.Base(path)
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
