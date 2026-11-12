package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mkmik/ccwt/internal/gitutil"
)

// dennetMux is dennet as ccwt's mux: its Spaces are the workspaces, their
// Surfaces the tabs, and the Den each Surface shows is the pane. dense draws
// them; den runs the terminals and reports to dennet what is in them.
//
// It is picked inside a den, which is where there is a Space to be in. What
// dennet has nothing for — a popup, which tab a window shows — says so;
// TODO.md's gaps are which.
//
// Everything is in one namespace, the one the den ccwt runs in belongs to:
// dennet's own, "default", for a den of this machine. A Host's dens are in its
// namespace, and ccwt doesn't look there.
type dennetMux struct{}

// dennetSocket is where dennet listens: where `dennet server run` puts its
// socket by default, the first of the two places that has one, the way
// dennet itself looks on a Host.
func dennetSocket() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	socks := []string{filepath.Join(state, "dennet", "dennet.sock")}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		socks = slices.Insert(socks, 0, filepath.Join(rt, "dennet", "dennet.sock"))
	}
	for _, s := range socks {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return socks[len(socks)-1]
}

// dennetDen is the den ccwt runs in, from the socket den hands every program
// it runs: <runtime>/den/<namespace>/<name>.sock. "" outside a den.
func dennetDen() (ns, name string) {
	s := os.Getenv("DEN_SOCKET")
	if s == "" {
		return dennetDefaultNS, ""
	}
	return filepath.Base(filepath.Dir(s)), strings.TrimSuffix(filepath.Base(s), ".sock")
}

const dennetDefaultNS = "default"

// dennetClient talks HTTP over dennet's unix socket. A question gets a few
// seconds; a watch, none.
func dennetClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: dennetTransport}
}

// dennetTransport is the one pool of connections to dennet. One per request
// would leave each request's kept-alive connection open for good, in ccwt and
// in dennet both, until the machine runs out of files.
var dennetTransport = &http.Transport{
	DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", dennetSocket())
	},
}

// dennetURL is the url of resource res (spaces, surfaces, dens) in the
// namespace ccwt is in, with query appended.
func dennetURL(res, query string) string {
	ns, _ := dennetDen()
	u := "http://dennet/apis/den.mkm.pub/v1alpha1/namespaces/" + ns + "/" + res
	if query != "" {
		u += "?" + query
	}
	return u
}

// The parts of dennet's objects ccwt reads.
type (
	dnMeta  struct{ Name, ResourceVersion string }
	dnBadge struct {
		Text string `json:"text"`
		Tone string `json:"tone,omitempty"`
	}
	dnSpace struct {
		Metadata dnMeta
		Spec     struct {
			Parent, Title, WorkingDir string
			Order                     int32
			Badges                    []dnBadge
		}
	}
	dnSurface struct {
		Metadata dnMeta
		Spec     struct {
			Space, Title string
			Order        int32
			Terminal     *struct{ Den string }
		}
	}
	dnDen struct {
		Metadata dnMeta
		Status   struct {
			Phase, Title string
			CWD          string `json:"cwd"`
			PID          int
		}
	}
)

// den is the Den the surface shows, "" for a surface of another kind.
func (s dnSurface) den() string {
	if s.Spec.Terminal == nil {
		return ""
	}
	return s.Spec.Terminal.Den
}

// dennetList is every object of resource res in our namespace.
func dennetList[T any](res string) ([]T, error) {
	var l struct{ Items []T }
	err := dennetDo(http.MethodGet, res, nil, &l)
	return l.Items, err
}

// dennetDo is one request: method on path (a resource, or resource/name) in
// our namespace, with body as JSON — a merge patch for a PATCH — and the
// answer into out when there is one. A refusal is an error carrying dennet's
// own message, which says what was wrong.
func dennetDo(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, dennetURL(path, ""), r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	resp, err := dennetClient(5 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("dennet: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var st struct{ Message, Reason string }
		_ = json.NewDecoder(resp.Body).Decode(&st)
		return &dennetError{resp.StatusCode, fmt.Sprintf("dennet: %s %s: %s", method, path, cmp.Or(st.Message, resp.Status))}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("dennet: %s %s: %w", method, path, err)
	}
	return nil
}

// dennetError is a request dennet refused, with the http status it said it in.
type dennetError struct {
	code int
	msg  string
}

func (e *dennetError) Error() string { return e.msg }

// dennetWorld is the three lists, each in the order dense draws it: Spaces as
// the sidebar's tree walked top to bottom, Surfaces by their place in the tab
// bar. Dens are by name, to be looked up.
type dennetWorld struct {
	spaces   []dnSpace
	surfaces []dnSurface
	dens     map[string]dnDen
}

// dennetLoad reads what it is asked for: the spaces always, and the surfaces
// and dens when tabs says so. fresh asks dennet itself rather than what its
// watches have brought in, for an action about to write from what it reads.
func dennetLoad(tabs, fresh bool) (dennetWorld, error) {
	var w dennetWorld
	spaces, err := dennetGet[dnSpace]("spaces", fresh)
	if err != nil {
		return w, err
	}
	w.spaces = sidebarWalk(spaces)
	if !tabs {
		return w, nil
	}
	if w.surfaces, err = dennetGet[dnSurface]("surfaces", fresh); err != nil {
		return w, err
	}
	slices.SortStableFunc(w.surfaces, func(a, b dnSurface) int {
		return cmp.Or(cmp.Compare(a.Spec.Order, b.Spec.Order), strings.Compare(a.Metadata.Name, b.Metadata.Name))
	})
	dens, err := dennetGet[dnDen]("dens", fresh)
	if err != nil {
		return w, err
	}
	w.dens = map[string]dnDen{}
	for _, d := range dens {
		w.dens[d.Metadata.Name] = d
	}
	return w, nil
}

// sidebarWalk puts spaces in the order dense's sidebar has them: each Space
// followed by those under it, siblings by order and then name. A Space whose
// parent isn't there is drawn at the top, and so is one a cycle would hide.
func sidebarWalk(spaces []dnSpace) []dnSpace {
	byName := map[string]bool{}
	for _, s := range spaces {
		byName[s.Metadata.Name] = true
	}
	kids := map[string][]dnSpace{}
	for _, s := range spaces {
		p := s.Spec.Parent
		if !byName[p] || p == s.Metadata.Name {
			p = ""
		}
		kids[p] = append(kids[p], s)
	}
	for _, k := range kids {
		slices.SortFunc(k, func(a, b dnSpace) int {
			return cmp.Or(cmp.Compare(a.Spec.Order, b.Spec.Order), strings.Compare(a.Metadata.Name, b.Metadata.Name))
		})
	}
	var out []dnSpace
	seen := map[string]bool{}
	var walk func(parent string)
	walk = func(parent string) {
		for _, s := range kids[parent] {
			if !seen[s.Metadata.Name] {
				seen[s.Metadata.Name] = true
				out = append(out, s)
				walk(s.Metadata.Name)
			}
		}
	}
	walk("")
	for _, s := range spaces { // ponytail: a cycle's members go last, unordered, rather than vanish
		if !seen[s.Metadata.Name] {
			out = append(out, s)
		}
	}
	return out
}

func (w dennetWorld) workspace(s dnSpace) muxWorkspace {
	path := s.Spec.WorkingDir
	repo, ok := gitutil.ClaudeWorktreeRepoRoot(path)
	if !ok {
		repo = path
	}
	ws := muxWorkspace{ID: s.Metadata.Name, Label: cmp.Or(s.Spec.Title, s.Metadata.Name), Path: path, Repo: repo}
	// dennet doesn't know which tab is up front (TODO.md, gap 1): the first
	// one stands in for it.
	for _, t := range w.surfaces {
		if t.Spec.Space == ws.ID {
			ws.Tab = t.Metadata.Name
			break
		}
	}
	return ws
}

func (dennetMux) inside() bool { return os.Getenv("DEN_SOCKET") != "" }

// here is the Surface showing our Den, and its Space: asked once and then
// remembered, since which tab a den is in doesn't change, and the views ask
// on every frame. A dennet that can't say is asked again next time.
func (dennetMux) here() (ws, tab string) {
	_, den := dennetDen()
	if den == "" {
		return "", ""
	}
	if h, ok := dennetHere.Load(os.Getenv("DEN_SOCKET")); ok {
		h := h.([2]string)
		return h[0], h[1]
	}
	surfaces, err := dennetGet[dnSurface]("surfaces", false)
	if err != nil {
		return "", ""
	}
	for _, s := range surfaces {
		if s.den() == den {
			dennetHere.Store(os.Getenv("DEN_SOCKET"), [2]string{s.Spec.Space, s.Metadata.Name})
			return s.Spec.Space, s.Metadata.Name
		}
	}
	return "", ""
}

var dennetHere sync.Map

func (dennetMux) listens() bool { return true }

func (dennetMux) workspaces() ([]muxWorkspace, error) {
	w, err := dennetLoad(true, false)
	if err != nil {
		return nil, err
	}
	var ws []muxWorkspace
	for _, s := range w.spaces {
		ws = append(ws, w.workspace(s))
	}
	return ws, nil
}

// sidebar has no dividers: dense's sidebar is a tree (TODO.md, gap 8).
func (m dennetMux) sidebar() (map[string]string, map[string]int, []int) {
	w, err := dennetLoad(false, false)
	if err != nil {
		return nil, nil, nil
	}
	labels, order := map[string]string{}, map[string]int{}
	for i, s := range w.spaces {
		ws := w.workspace(s)
		if ws.Path == "" {
			continue
		}
		labels[ws.Path] = ws.Label
		for _, p := range []string{ws.Path, ws.Repo} {
			if _, ok := order[p]; !ok {
				order[p] = i
			}
		}
	}
	return labels, order, nil
}

func (dennetMux) worktreeWorkspaces(_, path string) ([]string, error) {
	w, err := dennetLoad(false, false)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range w.spaces {
		if s.Spec.WorkingDir == path {
			ids = append(ids, s.Metadata.Name)
		}
	}
	return ids, nil
}

func (dennetMux) repoWorkspaces() map[string]string {
	w, err := dennetLoad(false, false)
	if err != nil {
		return nil
	}
	ids := map[string]string{}
	for _, s := range w.spaces {
		if ws := w.workspace(s); ws.Repo != "" {
			if _, ok := ids[ws.Repo]; !ok {
				ids[ws.Repo] = ws.ID
			}
		}
	}
	return ids
}

// focused is yes for our own Space only. dennet can't say what dense shows
// (TODO.md, gap 1), but a request to show something goes to the window that
// last typed into our den — and that window is looking at our Space. So
// `remove`, run from here, takes that window to the repo's Space before ours
// closes; every other Space is no, which leaves the focus alone.
func (m dennetMux) focused(ws string) bool {
	here, _ := m.here()
	return ws != "" && ws == here
}

// tabs is the Space's Surfaces, each with what its Den says: where it is, its
// title, and the agent in it if the title gives one away. Never focused and
// never a counter (gap 1): a tab nobody can be seen looking at would be green
// for good.
func (dennetMux) tabs(ws string) ([]wsTab, error) {
	if ws == "" {
		return nil, errors.New("dennet: this den is in no Space's tab")
	}
	w, err := dennetLoad(true, false)
	if err != nil {
		return nil, err
	}
	var ts []wsTab
	for _, s := range w.surfaces {
		if s.Spec.Space != ws {
			continue
		}
		d := w.dens[s.den()]
		status, title := denAgent(d.Status.Title)
		ts = append(ts, wsTab{
			ID: s.Metadata.Name, Label: cmp.Or(s.Spec.Title, title, s.Metadata.Name),
			Status: status, Cwd: d.Status.CWD, Title: title,
		})
	}
	return ts, nil
}

// agents is every Den whose title says an agent is in it (TODO.md, gap 2a),
// placed by the Surface showing it.
func (dennetMux) agents(bool) []muxAgent {
	w, err := dennetLoad(true, false)
	if err != nil {
		return nil
	}
	at := map[string]dnSurface{}
	for _, s := range w.surfaces {
		at[s.den()] = s
	}
	var as []muxAgent
	for name, d := range w.dens {
		status, _ := denAgent(d.Status.Title)
		if status == "" || d.Status.Phase != "Running" {
			continue
		}
		s := at[name]
		as = append(as, muxAgent{Status: status, Cwd: d.Status.CWD, Workspace: s.Spec.Space, Tab: s.Metadata.Name, Pane: name})
	}
	return as
}

// denAgent reads Claude Code's state off a terminal title, which it leads with
// a spinner while it works and with ✳ when it waits for you, and is the title
// without that mark. Any other title is no agent, and stays as it is.
//
// ponytail: Claude Code only, and no "blocked" — the title says nothing of a
// question it is waiting on. A hook that tells dennet is the way to both
// (TODO.md, gap 2b).
func denAgent(title string) (status, rest string) {
	r, n := utf8.DecodeRuneInString(title)
	switch {
	case r == '✳':
		status = "idle"
	case strings.ContainsRune("◐◓◑◒", r), r >= 0x2800 && r <= 0x28ff: // the half circles, or braille dots
		status = "working"
	default:
		return "", title
	}
	return status, strings.TrimSpace(title[n:])
}

func (dennetMux) paneAt(path string) (string, string) {
	w, err := dennetLoad(true, false)
	if err != nil {
		return "", ""
	}
	for _, s := range w.spaces {
		if s.Spec.WorkingDir != path {
			continue
		}
		for _, t := range w.surfaces {
			if t.Spec.Space == s.Metadata.Name && t.den() != "" {
				return t.den(), s.Metadata.Name
			}
		}
	}
	return "", ""
}

func (dennetMux) paneTab(pane string) (string, error) {
	surfaces, err := dennetGet[dnSurface]("surfaces", false)
	if err != nil {
		return "", err
	}
	for _, s := range surfaces {
		if s.den() == pane {
			return s.Metadata.Name, nil
		}
	}
	return "", fmt.Errorf("dennet: no tab shows den %s", pane)
}

func (dennetMux) place(tab string) (place, error) {
	w, err := dennetLoad(true, false)
	if err != nil {
		return place{}, err
	}
	for _, t := range w.surfaces {
		if t.Metadata.Name != tab {
			continue
		}
		label := t.Spec.Space
		for _, s := range w.spaces {
			if s.Metadata.Name == t.Spec.Space {
				label = w.workspace(s).Label
			}
		}
		_, title := denAgent(w.dens[t.den()].Status.Title)
		return place{label, cmp.Or(t.Spec.Title, title, tab), tab}, nil
	}
	return place{}, fmt.Errorf("dennet: no tab %s", tab)
}

func (dennetMux) snapshot() (muxSnapshot, bool) {
	w, err := dennetLoad(true, false)
	if err != nil {
		return muxSnapshot{}, false
	}
	var snap muxSnapshot
	for _, s := range w.spaces {
		snap.Workspaces = append(snap.Workspaces, w.workspace(s))
		for _, t := range w.surfaces {
			if t.Spec.Space == s.Metadata.Name {
				snap.Tabs = append(snap.Tabs, muxTab{ID: t.Metadata.Name, Workspace: s.Metadata.Name, Label: t.Spec.Title})
				if d := t.den(); d != "" {
					snap.Panes = append(snap.Panes, muxPane{ID: d, Tab: t.Metadata.Name})
				}
			}
		}
	}
	return snap, true
}

// procs is what is in the foreground of the den's terminal, from the process
// table: the group the terminal has in front (ps's tpgid, read off the den's
// program), and whether that is the program itself — the shell, waiting at
// its prompt. Dens in our namespace run on this machine; one whose program
// isn't in the table is one this can't say about.
func (dennetMux) procs(pane string) (paneProcs, bool) {
	dens, err := dennetGet[dnDen]("dens", false)
	i := slices.IndexFunc(dens, func(d dnDen) bool { return d.Metadata.Name == pane })
	if err != nil || i < 0 || dens[i].Status.PID == 0 {
		return paneProcs{}, false
	}
	out, err := exec.Command("ps", "-A", "-o", "pid=,pgid=,tpgid=,comm=").Output()
	if err != nil {
		return paneProcs{}, false
	}
	return foreground(string(out), dens[i].Status.PID)
}

// foreground reads ps's pid, pgid, tpgid and comm columns for what is in front
// of the terminal pid's program sits on.
func foreground(table string, pid int) (paneProcs, bool) {
	type row struct {
		pid, pgid, tpgid int
		comm             string
	}
	var rows []row
	tpgid := -1
	for _, l := range strings.Split(table, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		var r row
		if _, err := fmt.Sscan(strings.Join(f[:3], " "), &r.pid, &r.pgid, &r.tpgid); err != nil {
			continue
		}
		r.comm = filepath.Base(strings.Join(f[3:], " "))
		rows = append(rows, r)
		if r.pid == pid {
			tpgid = r.tpgid
		}
	}
	if tpgid <= 0 {
		return paneProcs{}, false
	}
	p := paneProcs{shell: tpgid == pid}
	for _, r := range rows {
		if r.pgid == tpgid {
			p.names = append(p.names, r.comm)
		}
	}
	return p, true
}

// listen hears what dennet's watches bring in on spaces and surfaces — and
// dens too, for the ws view of workspace ws, whose tabs show what their Dens
// say. What the watches have already brought in comes first, as it would
// after a (re)connect.
//
// Only what touches ws pokes the ws view: its own Space, its Surfaces, and the
// Dens they show — which the surfaces are what says. Dens report every few
// seconds while their programs write, so another workspace's agent at work
// would otherwise have this one asking all the time.
func (dennetMux) listen(ctx context.Context, poke chan<- struct{}, ws string) {
	var mu sync.Mutex
	ours := map[string]bool{} // our Surfaces' Dens
	keep := func(res string, obj []byte) bool {
		if ws == "" {
			return true
		}
		switch res {
		case "spaces":
			var s dnSpace
			return json.Unmarshal(obj, &s) != nil || s.Metadata.Name == ws
		case "surfaces":
			var s dnSurface
			if json.Unmarshal(obj, &s) != nil {
				return true
			}
			mu.Lock()
			defer mu.Unlock()
			if s.Spec.Space == ws {
				ours[s.den()] = true
			}
			return s.Spec.Space == ws
		default:
			var d dnDen
			mu.Lock()
			defer mu.Unlock()
			return json.Unmarshal(obj, &d) != nil || ours[d.Metadata.Name]
		}
	}
	res := []string{"spaces", "surfaces"}
	if ws != "" {
		res = append(res, "dens")
	}
	c := dennetCache()
	for _, r := range res {
		defer c.subscribe(r, func(obj json.RawMessage) {
			if keep(r, obj) {
				select {
				case poke <- struct{}{}:
				default:
				}
			}
		})()
	}
	<-ctx.Done()
}

// dennetStore is what dennet's watches have brought in, so that the views,
// which ask on every tick, read it here rather than list everything again
// each time: a list, then a watch from the list's resourceVersion, for each
// resource the moment something first asks about it. A watch that drops
// picks up where it was; one dennet says is too old (410 Gone) lists again.
//
// ponytail: kept as JSON and decoded on every read, which is a few dozen
// small objects; typed stores if that ever shows.
type dennetStore struct {
	ctx    context.Context
	stop   context.CancelFunc
	mu     sync.Mutex
	objs   map[string]map[string]json.RawMessage // resource → name → object
	synced map[string]bool                       // the watch on it is up now
	going  map[string]bool
	subs   map[string]map[*func(json.RawMessage)]bool
}

var (
	dennetCacheMu  sync.Mutex
	dennetCacheNow *dennetStore
)

// dennetCache is this process's store: one set of watches, however many
// views listen.
func dennetCache() *dennetStore {
	dennetCacheMu.Lock()
	defer dennetCacheMu.Unlock()
	if dennetCacheNow == nil {
		ctx, stop := context.WithCancel(context.Background())
		dennetCacheNow = &dennetStore{ctx: ctx, stop: stop, objs: map[string]map[string]json.RawMessage{},
			synced: map[string]bool{}, going: map[string]bool{}, subs: map[string]map[*func(json.RawMessage)]bool{}}
	}
	return dennetCacheNow
}

// dennetCacheReset stops the watches and forgets what they brought in: for a
// test, whose dennet is another than the last test's.
func dennetCacheReset() {
	dennetCacheMu.Lock()
	defer dennetCacheMu.Unlock()
	if dennetCacheNow != nil {
		dennetCacheNow.stop()
		dennetCacheNow = nil
	}
}

// dennetGet is every object of res: what the watch has brought in when it is
// up, a list otherwise — and always, when fresh says so.
func dennetGet[T any](res string, fresh bool) ([]T, error) {
	if !fresh {
		if raws, ok := dennetCache().items(res); ok {
			out := make([]T, len(raws))
			for i, r := range raws {
				if err := json.Unmarshal(r, &out[i]); err != nil {
					return nil, fmt.Errorf("dennet: %s: %w", res, err)
				}
			}
			return out, nil
		}
	}
	return dennetList[T](res)
}

// items is res's objects by name, and whether its watch is up; asking starts
// the watch.
func (c *dennetStore) items(res string) ([]json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.start(res)
	if !c.synced[res] {
		return nil, false
	}
	var out []json.RawMessage
	for _, name := range slices.Sorted(maps.Keys(c.objs[res])) {
		out = append(out, c.objs[res][name])
	}
	return out, true
}

// subscribe has fn told of every object of res the watch brings in — nil
// for "everything, maybe": after a list — starting with those it has
// already, until the returned func is called.
func (c *dennetStore) subscribe(res string, fn func(json.RawMessage)) func() {
	c.mu.Lock()
	c.start(res)
	if c.subs[res] == nil {
		c.subs[res] = map[*func(json.RawMessage)]bool{}
	}
	c.subs[res][&fn] = true
	var have []json.RawMessage
	if c.synced[res] {
		have = append(slices.Collect(maps.Values(c.objs[res])), nil)
	}
	c.mu.Unlock()
	for _, o := range have {
		fn(o)
	}
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.subs[res], &fn)
	}
}

// start runs res's watch, once. c.mu is held.
func (c *dennetStore) start(res string) {
	if !c.going[res] {
		c.going[res] = true
		go c.reflect(res)
	}
}

func (c *dennetStore) reflect(res string) {
	rv := ""
	for {
		if rv == "" {
			rv = c.relist(res)
		}
		if rv != "" {
			rv = c.watch(res, rv)
		}
		c.mu.Lock()
		c.synced[res] = false
		c.mu.Unlock()
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// relist replaces what there is of res with a list, and is the
// resourceVersion to watch from, "" when dennet couldn't say.
func (c *dennetStore) relist(res string) string {
	var l struct {
		Metadata dnMeta
		Items    []json.RawMessage
	}
	if dennetDo(http.MethodGet, res, nil, &l) != nil {
		return ""
	}
	objs := map[string]json.RawMessage{}
	for _, o := range l.Items {
		objs[dnMetaOf(o).Name] = o
	}
	c.mu.Lock()
	c.objs[res], c.synced[res] = objs, true
	c.mu.Unlock()
	for _, o := range append(l.Items, nil) {
		c.tell(res, o)
	}
	return l.Metadata.ResourceVersion
}

// watch applies res's events from resourceVersion rv on, until dennet hangs
// up or the store stops, and is where to pick up again: "" to list again.
func (c *dennetStore) watch(res, rv string) string {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, dennetURL(res, "watch=1&allowWatchBookmarks=true&resourceVersion="+url.QueryEscape(rv)), nil)
	if err != nil {
		return ""
	}
	resp, err := dennetClient(0).Do(req)
	if err != nil {
		return rv
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	c.mu.Lock()
	c.synced[res] = true
	c.mu.Unlock()
	for dec := json.NewDecoder(resp.Body); ; {
		var ev struct {
			Type   string
			Object json.RawMessage
		}
		if err := dec.Decode(&ev); err != nil {
			return rv
		}
		if ev.Type == "ERROR" { // 410 Gone, mostly
			return ""
		}
		m := dnMetaOf(ev.Object)
		rv = cmp.Or(m.ResourceVersion, rv)
		if ev.Type == "BOOKMARK" {
			continue
		}
		c.mu.Lock()
		if ev.Type == "DELETED" {
			delete(c.objs[res], m.Name)
		} else {
			c.objs[res][m.Name] = ev.Object
		}
		c.mu.Unlock()
		c.tell(res, ev.Object)
	}
}

func (c *dennetStore) tell(res string, obj json.RawMessage) {
	c.mu.Lock()
	fns := slices.Collect(maps.Keys(c.subs[res]))
	c.mu.Unlock()
	for _, fn := range fns {
		(*fn)(obj)
	}
}

func dnMetaOf(obj json.RawMessage) dnMeta {
	var o struct{ Metadata dnMeta }
	_ = json.Unmarshal(obj, &o)
	return o.Metadata
}

// openWorktree gives the worktree at path a Space under its repo's — the
// repo's made too, when dennet has none — with a tab in it, as herdr's open
// does. A Space already on path is the one opened again, given a tab if it has
// none. label names a new one; a Space opened again keeps its name.
func (dennetMux) openWorktree(root, path, label string) error {
	_, _, err := dennetOpen(path, root, cmp.Or(label, filepath.Base(path)))
	return err
}

// openProject gives the repo at path a Space at the top of the sidebar, with
// a tab in it.
func (dennetMux) openProject(path string) (muxOpened, error) {
	o, already, err := dennetOpen(path, "", filepath.Base(path))
	o.already = already
	return o, err
}

// dennetOpen is the Space on path, made under the repo at root's Space (none:
// at the top) when there isn't one, and its first tab, made when there isn't
// one. already says the Space was there. dense is asked to show it, as herdr's
// open is told to focus it; a dense that doesn't listen leaves it in the
// sidebar.
func dennetOpen(path, root, title string) (muxOpened, bool, error) {
	w, err := dennetLoad(true, true)
	if err != nil {
		return muxOpened{}, false, err
	}
	sp, already := w.spaceOn(path)
	if !already {
		parent := ""
		if root != "" {
			p, ok := w.spaceOn(root)
			if !ok {
				// The Space we run in, on no directory, is the repo's from now.
				if p, ok = w.adopt(root); ok {
					if err = dennetDo(http.MethodPatch, "spaces/"+p.Metadata.Name, map[string]any{"spec": map[string]any{"workingDir": root}}, nil); err != nil {
						return muxOpened{}, false, err
					}
				}
			}
			if !ok {
				if p, err = createSpace(w, root, "", filepath.Base(root)); err != nil {
					return muxOpened{}, false, err
				}
				w.spaces = append(w.spaces, p)
			}
			parent = p.Metadata.Name
		}
		if sp, err = createSpace(w, path, parent, title); err != nil {
			return muxOpened{}, false, err
		}
	}
	o := muxOpened{ws: sp.Metadata.Name}
	for _, t := range w.surfaces {
		if t.Spec.Space == o.ws && t.den() != "" {
			o.tab, o.pane = t.Metadata.Name, t.den()
			_ = dennetShow(o.ws, "")
			return o, already, nil
		}
	}
	o.tab, o.pane, err = createTab(o.ws, path, "")
	if err == nil {
		_ = dennetShow("", o.tab)
	}
	return o, already, err
}

// spaceOn is the Space on path, the first in the sidebar when there are more.
func (w dennetWorld) spaceOn(path string) (dnSpace, bool) {
	for _, s := range w.spaces {
		if s.Spec.WorkingDir == path {
			return s, true
		}
	}
	return dnSpace{}, false
}

// adopt is the Space ccwt runs in, when it is on no directory and our den is
// at root: a Space made by hand in dense for the repo, with ccwt started from a
// tab in it. Without this, the repo's first worktree made a second Space on
// root beside it.
func (w dennetWorld) adopt(root string) (dnSpace, bool) {
	ws, _ := dennetMux{}.here()
	_, den := dennetDen()
	if ws == "" || w.dens[den].Status.CWD != root {
		return dnSpace{}, false
	}
	for _, s := range w.spaces {
		if s.Metadata.Name == ws && s.Spec.WorkingDir == "" {
			return s, true
		}
	}
	return dnSpace{}, false
}

// createSpace makes a Space on path, last among parent's: named after the
// directory, as the Spaces copied over from herdr are, unless that name is
// taken or no name at all, when dennet picks one.
func createSpace(w dennetWorld, path, parent, title string) (dnSpace, error) {
	order := int32(0)
	for _, s := range w.spaces {
		if s.Spec.Parent == parent {
			order = max(order, s.Spec.Order+1)
		}
	}
	spec := map[string]any{"title": title, "workingDir": path, "order": order}
	if parent != "" {
		spec["parent"] = parent
	}
	body := map[string]any{"metadata": map[string]any{"generateName": "space-"}, "spec": spec}
	if name := dnsLabel(filepath.Base(path)); name != "" {
		body["metadata"] = map[string]any{"name": name}
	}
	var sp dnSpace
	err := dennetDo(http.MethodPost, "spaces", body, &sp)
	var de *dennetError
	if errors.As(err, &de) && de.code == http.StatusConflict {
		body["metadata"] = map[string]any{"generateName": "space-"}
		err = dennetDo(http.MethodPost, "spaces", body, &sp)
	}
	return sp, err
}

// dnsLabel is name as a Kubernetes name can have it — lower case letters,
// digits and dashes inside — or "" when nothing of it is left.
func dnsLabel(name string) string {
	b := []byte(strings.ToLower(name))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			b[i] = '-'
		}
	}
	return strings.Trim(string(b), "-")
}

func (dennetMux) newTab(ws, cwd, label string) (string, string, error) {
	return createTab(ws, cwd, label)
}

// createTab makes a Den in cwd and a Surface showing it, last in Space ws's
// tab bar; dennet starts the den. The Den is the one dense makes for its own
// new tab — a login shell, the environment den gives it plus the locale, the
// ssh agent and the temp directory — and the Surface takes the Den's name.
func createTab(ws, cwd, label string) (tab, pane string, err error) {
	surfaces, err := dennetList[dnSurface]("surfaces")
	if err != nil {
		return "", "", err
	}
	order := int32(1)
	for _, t := range surfaces {
		if t.Spec.Space == ws {
			order = max(order, t.Spec.Order+1)
		}
	}
	var d dnDen
	if err := dennetDo(http.MethodPost, "dens", map[string]any{
		"metadata": map[string]any{"generateName": ws + "-"},
		"spec": map[string]any{
			"workingDir": cwd,
			"inheritEnv": []string{"LANG", "LC_*", "SSH_AUTH_SOCK", "TMPDIR"},
		},
	}, &d); err != nil {
		return "", "", err
	}
	spec := map[string]any{"space": ws, "order": order, "kind": "terminal", "terminal": map[string]any{"den": d.Metadata.Name}}
	if label != "" {
		spec["title"] = label
	}
	if err := dennetDo(http.MethodPost, "surfaces", map[string]any{
		"metadata": map[string]any{"name": d.Metadata.Name}, "spec": spec,
	}, nil); err != nil {
		_ = dennetDo(http.MethodDelete, "dens/"+d.Metadata.Name, nil, nil) // a den no tab shows is one nobody would find
		return "", "", err
	}
	return d.Metadata.Name, d.Metadata.Name, nil
}

// run is `den send`: the words, joined by spaces, typed into the den and then
// Enter, as herdr's pane run does. A tab made a moment ago has a den still
// starting, with no socket to type into yet, so a send that fails is tried
// again for a few seconds. den finds the den's socket from the Den's name, on
// this machine.
//
// ponytail: tried again on any failure, a refusal included, which costs a
// few seconds before the bar says so.
func (dennetMux) run(pane string, words ...string) error {
	src := "http+unix://" + url.PathEscape(dennetSocket()) + strings.TrimPrefix(dennetURL("dens/"+pane, ""), "http://dennet")
	var out []byte
	var err error
	for wait := 50 * time.Millisecond; ; wait *= 2 {
		out, err = exec.Command(denBin(), append([]string{"send", "-f", src, "--"}, words...)...).CombinedOutput()
		if err == nil || wait > 4*time.Second {
			break
		}
		time.Sleep(wait)
	}
	if err != nil {
		return errors.New("den send: " + lastLine(out, err))
	}
	return nil
}

// denBin is the den to run: the one on PATH, or DEN_BIN_PATH's.
func denBin() string { return cmp.Or(os.Getenv("DEN_BIN_PATH"), "den") }

func (dennetMux) renameTab(tab, name string) error {
	return dennetDo(http.MethodPatch, "surfaces/"+tab, map[string]any{"spec": map[string]any{"title": name}}, nil)
}

func (dennetMux) renameWorkspace(ws, name string) error {
	return dennetDo(http.MethodPatch, "spaces/"+ws, map[string]any{"spec": map[string]any{"title": name}}, nil)
}

// moveTab puts tab at place at (from 0) in its Space's tab bar, numbering the
// tabs again from 1 and writing the ones whose number changed.
func (dennetMux) moveTab(tab string, at int) error {
	w, err := dennetLoad(true, true)
	if err != nil {
		return err
	}
	var ws string
	for _, t := range w.surfaces {
		if t.Metadata.Name == tab {
			ws = t.Spec.Space
		}
	}
	var bar []dnSurface
	var moved dnSurface
	for _, t := range w.surfaces {
		switch {
		case t.Metadata.Name == tab:
			moved = t
		case t.Spec.Space == ws:
			bar = append(bar, t)
		}
	}
	if ws == "" {
		return fmt.Errorf("dennet: no tab %s", tab)
	}
	bar = slices.Insert(bar, min(max(at, 0), len(bar)), moved)
	for i, t := range bar {
		if t.Spec.Order != int32(i+1) {
			if err := dennetDo(http.MethodPatch, "surfaces/"+t.Metadata.Name, map[string]any{"spec": map[string]any{"order": i + 1}}, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// closeWorkspace deletes the Space, then its Surfaces, then their Dens, which
// is what stops them — in that order because closing our own workspace stops
// this ccwt with its den, and everything else has to have gone by then.
// dennet deletes nothing for us (TODO.md, gap 5). What is gone already is
// fine, so a close that stopped halfway can be done again. A Space under this
// one is left, and dense draws it at the top.
func (dennetMux) closeWorkspace(ws string) error {
	surfaces, err := dennetList[dnSurface]("surfaces")
	if err != nil {
		return err
	}
	gone := []string{"spaces/" + ws}
	var dens []string
	for _, t := range surfaces {
		if t.Spec.Space == ws {
			gone = append(gone, "surfaces/"+t.Metadata.Name)
			if d := t.den(); d != "" {
				dens = append(dens, "dens/"+d)
			}
		}
	}
	for _, path := range append(gone, dens...) {
		var de *dennetError
		if err := dennetDo(http.MethodDelete, path, nil, nil); err != nil && !(errors.As(err, &de) && de.code == http.StatusNotFound) {
			return err
		}
	}
	return nil
}

// file leaves the Space where it is: dense's sidebar is a tree, with no
// dividers to file it under (TODO.md, gap 8).
func (dennetMux) file(_, _ string) {}

// ccwtBadges are the badges ccwt puts on a workspace, which are how it tells
// its own from anyone else's: dennet's badges have no owner (TODO.md, gap 4).
var ccwtBadges = []string{"✓", mergedDirtyGlyph, reviewGlyph, "■", "▣", "☒", unknownGlyph}

// badge swaps ccwt's badge on the Space for glyph, or takes it off for "", and
// writes nothing when that changes nothing — it is asked once a minute. A
// badge has no lifetime here, so one a ccwt that has stopped left behind
// stays until the next one replaces it. The Space's resourceVersion goes with
// the write, so a badge someone else added meanwhile isn't written over: that
// write fails, and the next minute's tries again.
func (dennetMux) badge(ws, glyph string) {
	var sp dnSpace
	if dennetDo(http.MethodGet, "spaces/"+ws, nil, &sp) != nil {
		return
	}
	badges := slices.DeleteFunc(slices.Clone(sp.Spec.Badges), func(b dnBadge) bool { return slices.Contains(ccwtBadges, b.Text) })
	if glyph != "" {
		badges = append(badges, dnBadge{Text: glyph})
	}
	if slices.Equal(badges, sp.Spec.Badges) {
		return
	}
	_ = dennetDo(http.MethodPatch, "spaces/"+ws, map[string]any{
		"metadata": map[string]any{"resourceVersion": sp.Metadata.ResourceVersion},
		"spec":     map[string]any{"badges": badges},
	}, nil)
}

func (dennetMux) focusTab(tab string) error { return dennetShow("", tab) }

func (dennetMux) focusWorkspace(ws string) error { return dennetShow(ws, "") }

// dennetShowKey is the annotation dense reads a request to show something from.
const dennetShowKey = "dense.mkm.pub/show"

// dennetShow asks dense to show a Space, or a tab of one: the request goes on
// our own Surface, and the dense window that was just typed into it — the one
// the user asked from — does what it says (TODO.md, gap 1). Each dense window
// keeps its own selection; nothing in dennet says what any of them shows. The
// time makes every request a change, even one asking what the last did.
func dennetShow(space, surface string) error {
	_, here := dennetMux{}.here()
	if here == "" {
		return errors.New("this den has no tab in dennet to ask dense from")
	}
	ask, err := json.Marshal(struct {
		Space   string `json:"space,omitempty"`
		Surface string `json:"surface,omitempty"`
		At      string `json:"at"`
	}{space, surface, time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	return dennetDo(http.MethodPatch, "surfaces/"+here, map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{dennetShowKey: string(ask)}},
	}, nil)
}

// popup: dense has no popups (gap 6), so `ccwt open` shows the picker here.
func (dennetMux) popup(map[string]string) error { return errors.New("dense has no popups") }
