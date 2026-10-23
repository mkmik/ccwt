//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// A queued prompt's trip the whole way, as it is lived, with the real binary
// in terminals of its own: `n` in the list queues it, space on its "<new>" row
// makes the worktree and has herdr run `ccwt ws` there on it; that box takes an
// edit and ↵, which starts one agent on the edited prompt; and then a restart
// — `ccwt restart` here, an upgrade in the wild, the same re-exec either way —
// after which the workspace's table is up.
//
// The box used to come back, with the queued prompt in it as it was before the
// edit: the restart ran the tui again on the flags it had been started with,
// and ↵ there started a second agent on the same work.
func TestAQueuedPromptStartsOneAgentThroughARestart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds ccwt and waits out a restart")
	}
	src, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	initRepo(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "ccwt")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	// Not claude, so there is no trust question in front of the box.
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "ccwt", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`task_command = "agent"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// herdr: the workspace a `worktree open` makes is w1, its first pane
	// sitting in the worktree it opened, and it has a second tab once an agent
	// has been given one. The ws view lists the tabs; the list asks the rest
	// and makes do with silence.
	log, agent := filepath.Join(dir, "calls"), filepath.Join(dir, "agent")
	herdr := filepath.Join(dir, "herdr")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %[1]q
case "$1 $2" in
"pane list")
  p=$(sed -n 's/.*--path \([^ ]*\).*/\1/p' %[1]q | tail -1)
  printf '{"result":{"panes":[{"pane_id":"w1:p1","tab_id":"w1:t1","cwd":"%%s","workspace_id":"w1"}]}}' "$p" ;;
"pane get")
  printf '%%s' '{"result":{"pane":{"tab_id":"w1:t1"}}}' ;;
"tab create")
  touch %[2]q
  printf '%%s' '{"result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}' ;;
"tab list")
  if [ -e %[2]q ]; then
    printf '%%s' '{"result":{"tabs":[{"tab_id":"w1:t1","label":"ws"},{"tab_id":"w1:t2","agent_status":"working"}]}}'
  else
    printf '%%s' '{"result":{"tabs":[{"tab_id":"w1:t1","label":"ws"}]}}'
  fi ;;
esac
`, log, agent)
	if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	t.Setenv("TMPDIR", dir)
	calls := func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}

	const prompt = "then update Bob's docs"
	list := runTty(t, exec.Command(bin, "tui", "--interval=100ms", "--fetch=0"))
	list.waitFor(t, "the list", func() bool { return strings.Contains(plain(list.raw()), "q:quit") })
	// n with nothing selected: a prompt that waits on nothing, so a "<new>"
	// row from the start. The box opens on its title; tab goes on to the prompt.
	list.write(t, "ndocs pass\t"+prompt+"\r")
	list.waitFor(t, "the queued prompt's row", func() bool { return strings.Contains(plain(list.raw()), newName) })
	list.write(t, "j ")
	list.waitFor(t, "`ccwt ws` run in the new workspace", func() bool { return strings.Contains(calls(), "pane run w1:p1 ") })
	if !strings.Contains(calls(), "pane run w1:p1 "+bin+" ws --prompt=") {
		t.Fatalf("herdr calls = %q, want the ccwt just built run there on the prompt", calls())
	}

	// What herdr would do with that: type the line at the shell of a pane in
	// the worktree it opened.
	_, line, _ := strings.Cut(calls(), "pane run w1:p1 ")
	line, _, _ = strings.Cut(line, "\n")
	_, wt, _ := strings.Cut(calls(), "--path ")
	wt, _, _ = strings.Cut(wt, " ")
	if !strings.Contains(wt, "/.claude/worktrees/") {
		t.Fatalf("herdr opened %q, want a new worktree", wt)
	}
	ws := exec.Command("/bin/sh", "-c", line)
	ws.Dir = wt
	tty := runTty(t, ws)

	tty.waitFor(t, "the box, with the queued prompt in it", func() bool { return strings.Contains(plain(tty.raw()), prompt) })
	// ↵ off the title, a word more on the prompt, ↵ to start it.
	tty.write(t, "\r too\r")
	tty.waitFor(t, "the agent to start", func() bool { return strings.Contains(calls(), "pane run w1:p2 ") })
	if got := seededPrompt(t, calls()); got != prompt+" too" {
		t.Errorf("the agent was started on %q, want the edited prompt", got)
	}

	if out, err := exec.Command(bin, "restart").CombinedOutput(); err != nil {
		t.Fatalf("ccwt restart: %v\n%s", err, out)
	}
	// The restarted process enters the alternate screen again, and its first
	// frame is whole once the bar, its last line, is in: the table's keys or
	// the box's.
	var frame string
	tty.waitFor(t, "the restarted tui's first frame", func() bool {
		raw := tty.raw()
		if strings.Count(raw, altScreen) < 2 {
			return false
		}
		frame = plain(raw[strings.LastIndex(raw, altScreen):])
		return strings.Contains(frame, "q:quit") || strings.Contains(frame, "esc:cancel")
	})
	if strings.Contains(frame, prompt) || !strings.Contains(frame, "AGENT") {
		t.Errorf("the restarted tui shows this, want the workspace's table and no queued prompt:\n%s", frame)
	}
	// Two ↵s, which in the box would have started a second agent on the
	// queued prompt; then a row and space to go to it, which says when the
	// keys before them have been dealt with.
	tty.write(t, "\r\rj ")
	tty.waitFor(t, "space to go to a tab", func() bool { return strings.Contains(calls(), "tab focus") })
	if n := strings.Count(calls(), "tab create"); n != 1 {
		t.Errorf("%d agents started, want 1", n)
	}
}

// tty is a program running in a terminal of its own, and everything it has
// written there.
type tty struct {
	f   *os.File
	mu  sync.Mutex
	out bytes.Buffer
}

// runTty starts cmd in a 100x30 terminal, as a tui runs in a herdr pane, and
// stops it when the test is done.
func runTty(t *testing.T, cmd *exec.Cmd) *tty {
	t.Helper()
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	tt := &tty{f: f}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			tt.mu.Lock()
			tt.out.Write(buf[:n])
			tt.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		f.Close()
		<-done
	})
	return tt
}

func (tt *tty) write(t *testing.T, keys string) {
	t.Helper()
	if _, err := tt.f.Write([]byte(keys)); err != nil {
		t.Fatal(err)
	}
}

// raw is everything written so far, escapes and all.
func (tt *tty) raw() string {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.out.String()
}

// altScreen is what a tui starting up writes first: it takes the alternate
// screen.
const altScreen = "\x1b[?1049h"

// waitFor waits up to ten seconds for ok, and fails the test on what the
// terminal says when it doesn't come.
func (tt *tty) waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("no sign of %s; the terminal says:\n%s", what, plain(tt.raw()))
}
