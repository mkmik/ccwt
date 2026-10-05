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
// used first, less the hidden ones and those herdr has a workspace on; typing
// narrows them; picking one makes a workspace there with ccwt in a `prj` tab,
// or takes over one herdr had there without knowing its repo.
func TestOpenerOffersWhatHerdrHasNot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := t.TempDir()
	repo := func(name string, at int64) string {
		p := filepath.Join(base, name)
		gitInit(t, p)
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
	for _, k := range []string{"R", "E", "C"} {
		o.key(k)
	}
	if got := o.shown(); !slices.Equal(got, []string{recent}) {
		t.Errorf("REC leaves %v, want [%s]", got, recent)
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
	asked := make(chan string, 2)
	go func() {
		// The second open finds the workspace the first one made.
		for _, already := range []bool{false, true} {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			_ = json.Unmarshal(line, &req)
			asked <- fmt.Sprint(req.Method, " ", req.Params["cwd"], " ", req.Params["path"], " ", req.Params["focus"])
			fmt.Fprintf(c, `{"result":{"already_open":%v,"tab":{"tab_id":"w9:t1"},"root_pane":{"pane_id":"w9:p1"}}}`+"\n", already)
			c.Close()
		}
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
	want := "worktree.open " + recent + " " + recent + " true"
	if got := <-asked; got != want {
		t.Errorf("herdr was asked %q, want %q", got, want)
	}
	out, _ := os.ReadFile(log)
	exe, _ := os.Executable()
	ran := "pane run w9:p1 " + exe + "\ntab rename w9:t1 prj\n"
	if string(out) != ran {
		t.Errorf("herdr ran:\n%s\nwant:\n%s", out, ran)
	}

	// A workspace herdr already had there keeps its own shell and tab name.
	if msg := openProject(recent); msg != "opened recent" {
		t.Errorf("reopen: openProject said %q", msg)
	}
	if got := <-asked; got != want {
		t.Errorf("reopen: herdr was asked %q, want %q", got, want)
	}
	if out, _ := os.ReadFile(log); string(out) != ran {
		t.Errorf("reopen: herdr ran:\n%s\nwant only:\n%s", out, ran)
	}
}

// TestHerdrPlaceLastInCategory: a project opened under a category's directory
// goes last under that category's divider, above the next one.
func TestHerdrPlaceLastInCategory(t *testing.T) {
	cats := []Category{{"work", "/u/w"}, {"personal", "/u/p"}, {"deep", "/u/w/deep"}}
	for path, want := range map[string]string{"/u/w/x": "work", "/u/w": "work", "/u/wx/y": "", "/u/p/z": "personal", "/u/w/deep/q": "deep"} {
		if got := categoryOf(cats, path); got != want {
			t.Errorf("categoryOf(%s) = %q, want %q", path, got, want)
		}
	}

	t.Chdir(t.TempDir()) // a unix socket's path has to be short
	l, err := net.Listen("unix", "herdr.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	t.Setenv("HERDR_SOCKET_PATH", "herdr.sock")
	list := `{"result":{"workspaces":[{"workspace_id":"w1","label":"tmp"},{"workspace_id":"w2","label":"------ work"},{"workspace_id":"w3","label":"a"},{"workspace_id":"w4","label":"------ personal"},{"workspace_id":"w5","label":"b"},{"workspace_id":"w9","label":"new"}]}}`
	moved := make(chan string, 1)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			_ = json.Unmarshal(line, &req)
			if req.Method == "workspace.list" {
				fmt.Fprintln(c, list)
			} else {
				moved <- fmt.Sprint(req.Method, " ", req.Params["workspace_id"], " ", req.Params["insert_index"])
				fmt.Fprintln(c, `{"result":{}}`)
			}
			c.Close()
		}
	}()
	for cat, want := range map[string]string{"work": "workspace.move w9 3", "personal": "workspace.move w9 5"} {
		herdrFile("w9", cat)
		if got := <-moved; got != want {
			t.Errorf("%s: herdr was asked %q, want %q", cat, got, want)
		}
	}
}
