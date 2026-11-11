package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDennet is a dennet on its socket, as it would be found from inside a den
// of namespace "default" named den, holding the objects in lists to begin
// with: spaces, surfaces and dens, each a list of objects in JSON. It keeps
// what it is told — a POST adds (naming from generateName), a merge PATCH
// merges, refusing a stale resourceVersion, a DELETE takes away — and a watch
// gets the objects there as ADDED events, then whatever is sent on that
// resource's channel. Every write is in the log, as "VERB res/name".
//
// The socket goes in rt/ under the directory the test is in: relative, as
// herdrSocket's, since a socket's path has to fit in about a hundred bytes and
// a macOS temp directory is most of that.
func fakeDennet(t *testing.T, den string, lists map[string]string) *fakeAPI {
	t.Helper()
	if err := os.MkdirAll("rt/dennet", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "rt")
	t.Setenv("DEN_SOCKET", "/run/den/default/"+den+".sock")
	t.Setenv("HERDR_ENV", "")
	l, err := net.Listen("unix", "rt/dennet/dennet.sock")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{objs: map[string][]map[string]any{}, events: map[string]chan string{}}
	for _, res := range []string{"spaces", "surfaces", "dens"} {
		f.events[res] = make(chan string, 16)
		var objs []map[string]any
		if err := json.Unmarshal([]byte("["+lists[res]+"]"), &objs); err != nil {
			t.Fatalf("%s: %v", res, err)
		}
		f.objs[res] = objs
	}
	srv := &http.Server{Handler: f, ConnState: func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			f.conns.Add(1)
		}
	}}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close(); dennetTransport.CloseIdleConnections() })
	return f
}

type fakeAPI struct {
	conns  atomic.Int32 // connections dennet was ever dialled on
	mu     sync.Mutex
	objs   map[string][]map[string]any
	events map[string]chan string
	log    []string
	rv     int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/apis/den.mkm.pub/v1alpha1/namespaces/default/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	res, name, _ := strings.Cut(rest, "/")
	if r.Method == http.MethodGet && name == "" && r.URL.Query().Get("watch") != "" {
		f.watch(w, r, res)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	refuse := func(code int, msg string) {
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"kind":"Status","message":%q}`, msg)
	}
	i := slices.IndexFunc(f.objs[res], func(o map[string]any) bool { return metaOf(o)["name"] == name })
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == http.MethodGet && name == "":
		_ = json.NewEncoder(w).Encode(map[string]any{"items": f.objs[res]})
		return
	case i < 0 && r.Method != http.MethodPost:
		refuse(http.StatusNotFound, res+" "+name+" not found")
		return
	case r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(f.objs[res][i])
		return
	case r.Method == http.MethodPost:
		m := metaOf(body)
		if m["name"] == nil {
			f.rv++
			m["name"] = fmt.Sprint(m["generateName"], "g", f.rv) // never a fixture's name
		}
		if slices.ContainsFunc(f.objs[res], func(o map[string]any) bool { return metaOf(o)["name"] == m["name"] }) {
			refuse(http.StatusConflict, fmt.Sprint(res, " ", m["name"], " already exists"))
			return
		}
		f.objs[res] = append(f.objs[res], body)
		name = m["name"].(string)
	case r.Method == http.MethodPatch:
		if rv := metaOf(body)["resourceVersion"]; rv != nil && rv != metaOf(f.objs[res][i])["resourceVersion"] {
			refuse(http.StatusConflict, "the object has been modified")
			return
		}
		mergePatch(f.objs[res][i], body)
		body = f.objs[res][i]
	case r.Method == http.MethodDelete:
		body = f.objs[res][i]
		f.objs[res] = slices.Delete(f.objs[res], i, i+1)
	}
	f.rv++
	metaOf(body)["resourceVersion"] = fmt.Sprint(f.rv)
	f.log = append(f.log, r.Method+" "+res+"/"+name)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeAPI) watch(w http.ResponseWriter, r *http.Request, res string) {
	f.mu.Lock()
	for _, o := range f.objs[res] {
		b, _ := json.Marshal(o)
		fmt.Fprintf(w, `{"type":"ADDED","object":%s}`+"\n", b)
	}
	f.mu.Unlock()
	w.(http.Flusher).Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-f.events[res]:
			fmt.Fprintln(w, ev)
			w.(http.Flusher).Flush()
		}
	}
}

// writes is the log so far, and empties it.
func (f *fakeAPI) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.log
	f.log = nil
	return l
}

// get is the object res/name, nil when there is none.
func (f *fakeAPI) get(res, name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.objs[res] {
		if metaOf(o)["name"] == name {
			return o
		}
	}
	return nil
}

func metaOf(o map[string]any) map[string]any {
	if o["metadata"] == nil {
		o["metadata"] = map[string]any{}
	}
	return o["metadata"].(map[string]any)
}

// mergePatch applies a JSON merge patch (RFC 7396) to o.
func mergePatch(o, patch map[string]any) {
	for k, v := range patch {
		switch v := v.(type) {
		case nil:
			delete(o, k)
		case map[string]any:
			sub, ok := o[k].(map[string]any)
			if !ok {
				sub = map[string]any{}
				o[k] = sub
			}
			mergePatch(sub, v)
		default:
			o[k] = v
		}
	}
}

// dennetRepo is a repo's Space with a worktree's Space under it, each with
// tabs: the worktree's first tab runs `ccwt ws` (den wt-1, the one the test
// runs in), its second Claude Code waiting for you, its third Claude Code at
// work. The repo's Space comes second in the list and first in the sidebar.
var dennetRepo = map[string]string{
	"spaces": `{"metadata":{"name":"wt"},"spec":{"parent":"repo","order":5,"title":"fix it","workingDir":"/src/repo/.claude/worktrees/wt"}},
{"metadata":{"name":"repo"},"spec":{"order":1,"title":"repo","workingDir":"/src/repo"}},
{"metadata":{"name":"later"},"spec":{"order":2,"workingDir":"/src/later"}}`,
	"surfaces": `{"metadata":{"name":"wt-2"},"spec":{"space":"wt","order":2,"kind":"terminal","terminal":{"den":"wt-2"}}},
{"metadata":{"name":"wt-1"},"spec":{"space":"wt","order":1,"title":"ws","kind":"terminal","terminal":{"den":"wt-1"}}},
{"metadata":{"name":"wt-3"},"spec":{"space":"wt","order":3,"kind":"terminal","terminal":{"den":"wt-3"}}},
{"metadata":{"name":"repo-1"},"spec":{"space":"repo","order":1,"kind":"terminal","terminal":{"den":"repo-1"}}}`,
	"dens": `{"metadata":{"name":"wt-1"},"status":{"phase":"Running","title":"ccwt","cwd":"/src/repo/.claude/worktrees/wt"}},
{"metadata":{"name":"wt-2"},"status":{"phase":"Running","title":"✳ Fix the bug","cwd":"/src/repo/.claude/worktrees/wt"}},
{"metadata":{"name":"wt-3"},"status":{"phase":"Running","title":"◑ Read the logs","cwd":"/src/repo/.claude/worktrees/wt/sub"}},
{"metadata":{"name":"repo-1"},"status":{"phase":"Running","title":"main","cwd":"/src/repo"}}`,
}

// In a den, ccwt reads its workspace from dennet: the Space the den's tab is
// in, that Space's tabs in order with what their Dens say, the agents Claude
// Code's titles give away, and the sidebar walked as dense draws it.
func TestDennetReads(t *testing.T) {
	t.Chdir(t.TempDir())
	fakeDennet(t, "wt-1", dennetRepo)
	m := mx()
	if _, ok := m.(dennetMux); !ok {
		t.Fatalf("mx() in a den = %T, want dennet", m)
	}
	if ws, tab := m.here(); ws != "wt" || tab != "wt-1" {
		t.Errorf("here() = %q, %q, want wt, wt-1", ws, tab)
	}

	tabs, err := readTabs()
	want := []wsTab{
		{ID: "wt-1", Label: "ws", Cwd: "/src/repo/.claude/worktrees/wt", Title: "ccwt"},
		{ID: "wt-2", Label: "Fix the bug", Status: "idle", Cwd: "/src/repo/.claude/worktrees/wt", Title: "Fix the bug"},
		{ID: "wt-3", Label: "Read the logs", Status: "working", Cwd: "/src/repo/.claude/worktrees/wt/sub", Title: "Read the logs"},
	}
	if err != nil || !slices.Equal(tabs, want) {
		t.Errorf("readTabs() = %v, %v\nwant %v", tabs, err, want)
	}

	// The tab ccwt runs in is its own: an agent there doesn't keep it busy.
	busy := busyCwds(m.agents(true))
	if !busy["/src/repo/.claude/worktrees/wt/sub"] || len(busy) != 1 {
		t.Errorf("busyCwds = %v, want only the working agent's cwd", busy)
	}

	labels, order, seps := m.sidebar()
	if labels["/src/repo/.claude/worktrees/wt"] != "fix it" || labels["/src/later"] != "later" {
		t.Errorf("sidebar labels = %v", labels)
	}
	if order["/src/repo"] != 0 || order["/src/repo/.claude/worktrees/wt"] != 1 || order["/src/later"] != 2 || seps != nil {
		t.Errorf("sidebar order = %v, seps %v, want repo, its worktree, then later", order, seps)
	}
	if ids, err := m.worktreeWorkspaces("/src/repo", "/src/repo/.claude/worktrees/wt"); err != nil || !slices.Equal(ids, []string{"wt"}) {
		t.Errorf("worktreeWorkspaces = %v, %v", ids, err)
	}
	if got := m.repoWorkspaces()["/src/repo"]; got != "repo" {
		t.Errorf("repoWorkspaces()[/src/repo] = %q, want the repo's own Space", got)
	}
	if pane, ws := m.paneAt("/src/repo/.claude/worktrees/wt"); pane != "wt-1" || ws != "wt" {
		t.Errorf("paneAt = %q, %q, want wt-1, wt", pane, ws)
	}
	if p, err := m.place("wt-2"); err != nil || p != (place{"fix it", "Fix the bug", "wt-2"}) {
		t.Errorf("place(wt-2) = %v, %v", p, err)
	}
	s, ok := m.snapshot()
	if !ok || len(s.Workspaces) != 3 || len(s.Tabs) != 4 || len(s.Panes) != 4 || s.Tabs[0].ID != "repo-1" {
		t.Errorf("snapshot() = %+v, %v", s, ok)
	}
}

// Questions to dennet share a connection: a refresh a second, all day, doesn't
// leave a socket open for each.
func TestDennetReusesConnections(t *testing.T) {
	t.Chdir(t.TempDir())
	f := fakeDennet(t, "wt-1", dennetRepo)
	for range 20 {
		if _, err := dennetList[dnSpace]("spaces"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.conns.Load(); n != 1 {
		t.Errorf("20 lists dialled dennet %d times, want once", n)
	}
}

// The ws view hears about its own workspace — its Space, its tabs and the dens
// in them — and not about another's agent at work.
func TestDennetListens(t *testing.T) {
	t.Chdir(t.TempDir())
	events := fakeDennet(t, "wt-1", dennetRepo).events
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poke := make(chan struct{}, 1)
	go mx().listen(ctx, poke, "wt")

	heard := func() bool {
		select {
		case <-poke:
			return true
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}
	if !heard() {
		t.Fatal("no poke for the watches starting")
	}
	for heard() { // the rest of the ADDED events
	}
	events["dens"] <- `{"type":"MODIFIED","object":{"metadata":{"name":"repo-1"},"status":{"title":"◐ elsewhere"}}}`
	if heard() {
		t.Error("another workspace's den poked the ws view")
	}
	events["dens"] <- `{"type":"MODIFIED","object":{"metadata":{"name":"wt-3"},"status":{"title":"✳ done"}}}`
	if !heard() {
		t.Error("a den of ours changed, and the ws view didn't hear")
	}
}

func TestDenAgent(t *testing.T) {
	for title, want := range map[string][2]string{
		"✳ Fix the bug": {"idle", "Fix the bug"},
		"◐ Work":        {"working", "Work"},
		"⠂ Work":        {"working", "Work"},
		"main":          {"", "main"},
		"":              {"", ""},
	} {
		if s, r := denAgent(title); s != want[0] || r != want[1] {
			t.Errorf("denAgent(%q) = %q, %q, want %q", title, s, r, want)
		}
	}
}

// `c` under dennet: a new worktree gets a Space under its repo's — the repo's
// made first, since dennet has none yet — with a tab in it, and `ccwt ws`
// typed into that tab's den, which is then named `ws`.
func TestDennetNewWorkspace(t *testing.T) {
	root, err := filepath.EvalSymlinks(initRepo(t)) // what git calls it
	if err != nil {
		t.Fatal(err)
	}
	api := fakeDennet(t, "elsewhere-1", nil)
	dir := t.TempDir()
	log := filepath.Join(dir, "den.log")
	den := filepath.Join(dir, "den")
	if err := os.WriteFile(den, fmt.Appendf(nil, "#!/bin/sh\necho \"$@\" >> %q\n", log), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEN_BIN_PATH", den)

	msg := (&ui{}).newWorkspace()
	name, ok := strings.CutPrefix(msg, "opened ")
	if !ok {
		t.Fatalf("newWorkspace: %s", msg)
	}
	repo := dnsLabel(filepath.Base(root))
	if sp := api.get("spaces", repo); sp == nil || sp["spec"].(map[string]any)["workingDir"] != root {
		t.Errorf("repo Space = %v, want one on %s", sp, root)
	}
	sp := api.get("spaces", name)
	if sp == nil {
		t.Fatalf("no Space %s", name)
	}
	spec := sp["spec"].(map[string]any)
	if spec["parent"] != repo || spec["title"] != name || !strings.HasSuffix(spec["workingDir"].(string), "/.claude/worktrees/"+name) {
		t.Errorf("worktree Space spec = %v", spec)
	}
	surface := api.get("surfaces", name+"-g3")
	got := api.writes()
	want := []string{"POST spaces/" + repo, "POST spaces/" + name, "POST dens/" + name + "-g3", "POST surfaces/" + name + "-g3", "PATCH surfaces/" + name + "-g3"}
	if !slices.Equal(got, want) {
		t.Errorf("writes = %q\nwant %q", got, want)
	}
	if surface == nil || surface["spec"].(map[string]any)["title"] != "ws" {
		t.Errorf("the tab = %v, want it named ws", surface)
	}
	exe, _ := os.Executable()
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "send -f http+unix://rt%2Fdennet%2Fdennet.sock/apis/den.mkm.pub/v1alpha1/namespaces/default/dens/"+name+"-g3 -- "+exe+" ws") {
		t.Errorf("den calls = %q, want ccwt ws sent to the new tab", b)
	}

	// Opened again, the Space is the one there, and no tab is made.
	if msg := openWorkspace(spec["workingDir"].(string), ""); msg != "opened "+name {
		t.Errorf("reopen: %s", msg)
	}
	if got := api.writes(); got != nil {
		t.Errorf("reopen wrote %q, want nothing", got)
	}
}

// The ws view's own changes under dennet: a tab made, labelled and moved to
// the front; the workspace renamed; ccwt's badge put on, swapped and taken off
// beside someone else's; and the workspace closed — the Space first and its
// dens last, since one of them may be the one ccwt runs in.
func TestDennetActs(t *testing.T) {
	t.Chdir(t.TempDir())
	api := fakeDennet(t, "wt-1", dennetRepo)
	m := mx()

	tab, pane, err := m.newTab("wt", "/src/repo/.claude/worktrees/wt", "ws")
	if err != nil || tab != pane || !strings.HasPrefix(tab, "wt-") {
		t.Fatalf("newTab = %q, %q, %v", tab, pane, err)
	}
	if err := m.moveTab(tab, 0); err != nil {
		t.Fatal(err)
	}
	ts, err := m.tabs("wt")
	if err != nil || len(ts) != 4 || ts[0].ID != tab || ts[0].Label != "ws" || ts[1].ID != "wt-1" {
		t.Errorf("tabs after the move = %v, %v", ts, err)
	}

	// Our own Space is the one being looked at — the window that typed into
	// our den is showing it — and nothing else is.
	if !m.focused("wt") || m.focused("repo") {
		t.Errorf("focused(wt), focused(repo) = %v, %v, want true, false", m.focused("wt"), m.focused("repo"))
	}

	// Going somewhere is a request on our own Surface, for dense to read.
	show := func() map[string]any {
		var ask map[string]any
		a, _ := metaOf(api.get("surfaces", "wt-1"))["annotations"].(map[string]any)
		s, _ := a[dennetShowKey].(string)
		_ = json.Unmarshal([]byte(s), &ask)
		return ask
	}
	if err := m.focusTab("wt-2"); err != nil {
		t.Fatal(err)
	}
	first := show()
	if first["surface"] != "wt-2" || first["space"] != nil || first["at"] == "" {
		t.Errorf("focusTab asked %v, want the tab wt-2", first)
	}
	if err := m.focusWorkspace("repo"); err != nil {
		t.Fatal(err)
	}
	if ask := show(); ask["space"] != "repo" || ask["surface"] != nil || ask["at"] == first["at"] {
		t.Errorf("focusWorkspace asked %v, want the Space repo, at a new time", ask)
	}
	if err := m.renameWorkspace("wt", "fixed"); err != nil {
		t.Fatal(err)
	}

	badges := func() []any { b, _ := api.get("spaces", "wt")["spec"].(map[string]any)["badges"].([]any); return b }
	api.get("spaces", "wt")["spec"].(map[string]any)["badges"] = []any{map[string]any{"text": "jira"}}
	m.badge("wt", reviewGlyph)
	m.badge("wt", reviewGlyph) // the same again writes nothing
	m.badge("wt", "✓")
	if b := badges(); len(b) != 2 || b[0].(map[string]any)["text"] != "jira" || b[1].(map[string]any)["text"] != "✓" {
		t.Errorf("badges = %v, want someone else's and ccwt's ✓", b)
	}
	m.badge("wt", "")
	if b := badges(); len(b) != 1 {
		t.Errorf("badges after clearing = %v, want someone else's alone", b)
	}
	api.writes()

	if err := m.closeWorkspace("wt"); err != nil {
		t.Fatal(err)
	}
	got := api.writes()
	if len(got) != 9 || got[0] != "DELETE spaces/wt" || !strings.HasPrefix(got[4], "DELETE surfaces/") || !strings.HasPrefix(got[5], "DELETE dens/") {
		t.Errorf("close = %q, want the Space, its 4 tabs, then their 4 dens", got)
	}
	if api.get("spaces", "repo") == nil || api.get("dens", "repo-1") == nil {
		t.Error("close took the repo's Space or its den too")
	}
}

func TestForeground(t *testing.T) {
	table := `  100   100   300 /bin/zsh
  300   300   300 /usr/local/bin/ccwt
  301   300   300 git
  200   200   200 -zsh
`
	if p, ok := foreground(table, 100); !ok || p.shell || !slices.Equal(p.names, []string{"ccwt", "git"}) {
		t.Errorf("foreground of a shell running ccwt = %+v, %v", p, ok)
	}
	if p, ok := foreground(table, 200); !ok || !p.shell {
		t.Errorf("foreground of a shell at its prompt = %+v, %v", p, ok)
	}
	if _, ok := foreground(table, 999); ok {
		t.Error("foreground of a program not in the table says something")
	}
}
