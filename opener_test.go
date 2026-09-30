package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestOpenerOffersWhatHerdrHasNot: `o` offers the seen projects most recently
// used first, less the hidden ones and those herdr has a workspace on; `/`
// narrows them; picking one makes a workspace there with ccwt in a `prj` tab.
func TestOpenerOffersWhatHerdrHasNot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := t.TempDir()
	repo := func(name string, at int64) string {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := markSeen(p, "", time.Unix(at, 0)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old, open, hidden, recent := repo("old", 1000), repo("open", 3000), repo("hidden", 4000), repo("recent", 2000)
	if err := hideProject(hidden); err != nil {
		t.Fatal(err)
	}
	orig := herdrLabels
	herdrLabels = func() (map[string]string, map[string]int, []int) { return nil, map[string]int{open: 0}, nil }
	t.Cleanup(func() { herdrLabels = orig })

	o, err := newOpener()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{recent, old}; !slices.Equal(o.shown(), want) {
		t.Fatalf("offered %v, want %v", o.shown(), want)
	}
	for _, k := range []string{"/", "R", "E", "C", "\r"} {
		o.key(k)
	}
	if got := o.shown(); !slices.Equal(got, []string{recent}) {
		t.Errorf("/REC leaves %v, want [%s]", got, recent)
	}
	if p, done := o.key("\r"); p != recent || !done {
		t.Errorf("↵ picked (%q, %v), want (%q, true)", p, done, recent)
	}

	t.Chdir(t.TempDir()) // a unix socket's path has to be short
	l, err := net.Listen("unix", "herdr.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	asked := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		_ = json.Unmarshal(line, &req)
		asked <- fmt.Sprint(req.Method, " ", req.Params["cwd"], " ", req.Params["focus"])
		fmt.Fprintln(c, `{"result":{"tab":{"tab_id":"w9:t1"},"root_pane":{"pane_id":"w9:p1"}}}`)
	}()
	log, err := filepath.Abs("herdr.log")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SOCKET_PATH", "herdr.sock")
	t.Setenv("HERDR_BIN_PATH", bin)

	if msg := openProject(recent); msg != "opened recent" {
		t.Errorf("openProject said %q", msg)
	}
	if got, want := <-asked, "workspace.create "+recent+" true"; got != want {
		t.Errorf("herdr was asked %q, want %q", got, want)
	}
	out, _ := os.ReadFile(log)
	exe, _ := os.Executable()
	if want := "pane run w9:p1 " + exe + "\ntab rename w9:t1 prj\n"; string(out) != want {
		t.Errorf("herdr ran:\n%s\nwant:\n%s", out, want)
	}
}
