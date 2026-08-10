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
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

type TuiCmd struct {
	Interval time.Duration `default:"2s" help:"How often to re-read the worktree list."`
}

func (c *TuiCmd) Run() error {
	if !stdoutIsTTY() {
		return errors.New("tui needs a terminal on stdout (use `ccwt list` when piping)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A resize invalidates the cached frame: what's on screen no longer
	// matches the layout we last painted, so force a full repaint.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	// Raw mode so single keypresses arrive without waiting for a newline. If
	// stdin isn't a terminal we just run without keys: the list still
	// refreshes, and Ctrl-C still works because ISIG stays on. A nil channel
	// blocks forever in the select below, which is exactly what we want.
	var keys <-chan byte
	if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer term.Restore(int(os.Stdin.Fd()), state)
		keys = readKeys()
	}

	// Alternate screen, hidden cursor, no auto-wrap (a wrapped long line would
	// push the rest of the frame down and desync the cursor arithmetic).
	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[?7l")
	defer fmt.Print("\x1b[?7h\x1b[?25h\x1b[?1049l")

	out := bufio.NewWriter(os.Stdout)
	tick := time.NewTicker(c.Interval)
	defer tick.Stop()

	var last, msg string
	redraw := func() error {
		lines, err := frame(msg)
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

	for {
		if err := redraw(); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-winch:
			last = ""
		case <-tick.C:
			msg = ""
		case k := <-keys:
			switch k {
			case 'q', 3: // 3 = Ctrl-C, which raw mode delivers as a byte
				return nil
			case 'p':
				msg = "pulling…"
				if err := redraw(); err != nil {
					return err
				}
				msg = gitPull()
			}
		}
	}
}

// readKeys pumps stdin into a channel.
//
// ponytail: the goroutine is left blocked on Read at exit — it dies with the
// process, and this is the whole program, not a library.
func readKeys() <-chan byte {
	keys := make(chan byte, 8)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				keys <- buf[0]
			}
		}
	}()
	return keys
}

// frame renders the full screen: the worktree table, padding, and a status bar
// pinned to the bottom row.
func frame(msg string) ([]string, error) {
	var buf bytes.Buffer
	if err := renderList(&buf, true); err != nil {
		return nil, err
	}
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		cols, rows = 80, 24
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	body := max(rows-1, 1)
	for len(lines) < body {
		lines = append(lines, "")
	}
	return append(lines[:body], statusBar(cols, msg)), nil
}

// statusBar is one reverse-video line: what the keys do, then either a
// transient message (the result of a pull) or how far the branch has drifted
// from its upstream, cut or padded to exactly the terminal width.
func statusBar(cols int, msg string) string {
	if msg == "" {
		msg = gitDivergence()
	}
	s := " ccwt  q:quit  p:pull │ " + msg + " "
	r := []rune(s)
	if len(r) > cols {
		r = r[:max(cols, 0)]
	}
	return "\x1b[7m" + string(r) + strings.Repeat(" ", max(cols-len(r), 0)) + "\x1b[0m"
}

// gitDivergence reports the branch and how far it has drifted from its
// upstream, e.g. "main ↑2 ↓1". rev-list gives both numbers in one shot and,
// unlike `git status`, doesn't refresh the index — which matters when this
// runs every couple of seconds in a background pane.
func gitDivergence() string {
	branch := gitLine("rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		return ""
	}
	// HEAD...@{u}: left count is commits we have and upstream doesn't (ahead),
	// right count is the reverse (behind). Fails when there's no upstream.
	counts := strings.Fields(gitLine("rev-list", "--left-right", "--count", "HEAD...@{u}"))
	switch {
	case len(counts) != 2:
		return branch + "  (no upstream)"
	case counts[0] == "0" && counts[1] == "0":
		return branch + "  in sync"
	default:
		return fmt.Sprintf("%s  ↑%s ↓%s", branch, counts[0], counts[1])
	}
}

// gitLine runs git and returns its first line, or "" if it failed.
func gitLine(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

// gitPull runs a pull and boils its chatter down to the one line the bar has
// room for: on success the first line says what happened ("Already up to
// date.", "Updating a1b2..c3d4"), on failure the last line is git's reason.
func gitPull() string {
	out, err := exec.Command("git", "pull").CombinedOutput()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err != nil {
		return "pull failed: " + strings.TrimSpace(lines[len(lines)-1])
	}
	return "pull: " + strings.TrimSpace(lines[0])
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
