package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/mkmik/ccwt/internal/gitutil"
)

// process is one line of `ps`: what is running, and what started it.
type process struct {
	pid, parent int
	cmdline     string
}

// listProcesses returns our own processes with their full command lines, in
// the order ps gives them. `-x` takes in the ones with no controlling
// terminal, which is most of what a multiplexer runs, and without `-a` it
// stays our own: another user's processes are neither in our worktrees nor
// ours to navigate to.
// ponytail: package vars so tests can stand a process table up without one.
var listProcesses = func() ([]process, error) {
	out, err := exec.Command("ps", "-xww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var procs []process
	for line := range strings.SplitSeq(string(out), "\n") {
		pidField, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		ppidField, cmdline, ok := strings.Cut(strings.TrimLeft(rest, " "), " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidField)
		parent, perr := strconv.Atoi(ppidField)
		if err != nil || perr != nil {
			continue
		}
		procs = append(procs, process{pid, parent, cmdline})
	}
	return procs, nil
}

// procCwds is the working directory of every process we can see, by pid — what
// says which worktree a process is running in. The same lsof the CLAUDE column
// asks (claudeCwds), without its command-name prefilter, since here every
// process counts, and restricted to our own uid instead, which is what keeps
// it from walking the whole machine.
//
// Whatever lsof managed to print is used even when it exits non-zero: a
// process that went away mid-scan, or one it couldn't examine, is a normal
// failure and no reason to lose the rest.
var procCwds = func() map[int]string {
	cwds := map[int]string{}
	out, _ := exec.Command("lsof", "-a", "-d", "cwd", "-u", strconv.Itoa(os.Getuid()), "-Fpn").Output()
	pid := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 {
				cwds[pid] = line[1:]
			}
		}
	}
	return cwds
}

// under reports whether path is dir or sits inside it.
func under(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// psRow is one process on screen: what it is, and how far under the shell it
// sits, which is the indentation it's drawn with.
type psRow struct {
	pid     int
	depth   int
	cmdline string
}

// psRows is the process tree of one worktree: every process whose working
// directory is in the worktree while its parent's isn't — the shell you
// started there, or whatever took its place — with what each one spawned
// underneath it, down to depth generations (depth < 0 for all of them).
//
// Descendants are taken whole rather than filtered by cwd again: a build that
// cd'd itself into a temp directory is still part of what is running here, and
// a shell that cd'd out of the worktree takes its children with it. The tree
// is what the shell started, not what happens to be pointing at the directory.
func psRows(procs []process, cwds map[int]string, worktree string, depth int) []psRow {
	// In pid order, which ps doesn't print in: the tree is drawn in the order
	// the shells and their children were started, and it comes out the same
	// from one run to the next.
	procs = slices.SortedFunc(slices.Values(procs), func(a, b process) int { return a.pid - b.pid })
	inside := map[int]bool{}
	for _, p := range procs {
		if under(cwds[p.pid], worktree) {
			inside[p.pid] = true
		}
	}
	kids := map[int][]process{}
	for _, p := range procs {
		// ponytail: the only cycle real ppids have is a process parenting
		// itself, which is how the top of the tree reports itself on some
		// systems; anything longer would need the kernel to have gone mad.
		if p.parent != p.pid {
			kids[p.parent] = append(kids[p.parent], p)
		}
	}
	var rows []psRow
	var walk func(p process, d int)
	walk = func(p process, d int) {
		rows = append(rows, psRow{p.pid, d, p.cmdline})
		if depth >= 0 && d >= depth {
			return
		}
		for _, k := range kids[p.pid] {
			walk(k, d+1)
		}
	}
	for _, p := range procs {
		if inside[p.pid] && !inside[p.parent] {
			walk(p, 0)
		}
	}
	return rows
}

// claudeWorktrees is a repo's worktrees under .claude/worktrees/, with the repo
// root they hang off — the set `ccwt list` shows, by path alone.
func claudeWorktrees(dir string) (string, []string, error) {
	wts, err := gitutil.ListWorktrees(dir)
	if err != nil {
		return "", nil, gitError(dir, err)
	}
	if len(wts) == 0 {
		return "", nil, nil
	}
	// The main worktree is the repo root the rest hang off, taken from git
	// rather than from the configured path, which may point inside the repo.
	root := wts[0].Path
	claudeDir := filepath.Join(root, ".claude", "worktrees") + string(filepath.Separator)
	var paths []string
	for _, wt := range wts {
		if strings.HasPrefix(wt.Path, claudeDir) {
			paths = append(paths, wt.Path)
		}
	}
	return root, paths, nil
}

type PsCmd struct {
	Global bool `short:"g" help:"Cover every project listed in $XDG_CONFIG_HOME/ccwt/config.toml, a section per project, not just this repo's worktrees."`
	Depth  int  `short:"d" default:"1" help:"How many generations below each shell to show: 0 for the shells alone, --depth=-1 for everything they started."`
}

// Run prints what is running in each worktree.
func (c *PsCmd) Run() error {
	projects, err := projectRoots(c.Global)
	if err != nil {
		return err
	}
	// Fit the lines to the terminal only when there is one, as `list` does.
	width := 0
	if stdoutIsTTY() {
		width, _, _ = term.GetSize(int(os.Stdout.Fd()))
	}
	lines, _, err := psReport(projects, c.Depth, width)
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// psReport is what is running in each worktree, a line at a time: the projects
// to cover (nil for the repo we're in, as projectRoots gives it), how many
// generations below each shell to draw, and the width to cut the lines to — 0
// leaves them whole, which is what the tui asks for, since it cuts its own.
//
// Worktrees with nothing in them are left out, sections included: the question
// the report answers is what is running where, and a screenful of empty
// headings doesn't help answer it.
//
// The rows alongside are what each line stands for, as renderList's are: a
// process by its pid, a worktree by its path — the tui selects, searches and
// acts on them, and the command line ignores them.
func psReport(projects []string, depth, width int) ([]string, []listRow, error) {
	global := projects != nil
	if !global {
		// "" is the current directory, which is how git reads "the repo we're in".
		projects = []string{""}
	}

	// ps and lsof know nothing about git and git knows nothing about them, so
	// all three go out at once — the scan is the slow one (~200ms) and this is
	// the whole of what the report waits for.
	var (
		wg      sync.WaitGroup
		procs   []process
		procErr error
		cwds    map[int]string
		roots   = make([]string, len(projects))
		trees   = make([][]string, len(projects))
		errs    = make([]error, len(projects))
	)
	wg.Go(func() { procs, procErr = listProcesses() })
	wg.Go(func() { cwds = procCwds() })
	for i, dir := range projects {
		wg.Go(func() { roots[i], trees[i], errs[i] = claudeWorktrees(dir) })
	}
	wg.Wait()
	if procErr != nil {
		return nil, nil, procErr
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}

	type block struct {
		path string
		rows []psRow
	}
	var out []string
	var ids []listRow
	seen := map[string]bool{} // two config entries can point into the same repo
	for i, root := range roots {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		var blocks []block
		for _, wt := range trees[i] {
			if rows := psRows(procs, cwds, wt, depth); len(rows) > 0 {
				blocks = append(blocks, block{wt, rows})
			}
		}
		if len(blocks) == 0 {
			continue
		}
		indent := ""
		if global {
			out = append(out, section(root, len(blocks), false))
			// The project's line, as a row that is only a line: nothing folds
			// in the report — it is what is running, and a project with nothing
			// running in it isn't in it to be folded away.
			ids = append(ids, listRow{project: root, pid: -1})
			indent = "  "
		}
		for _, b := range blocks {
			out = append(out, indent+filepath.Base(b.path))
			ids = append(ids, listRow{project: root, path: b.path})
			pidw := 0
			for _, r := range b.rows {
				pidw = max(pidw, len(strconv.Itoa(r.pid)))
			}
			for _, r := range b.rows {
				line := fmt.Sprintf("%s  %*d  %s%s", indent, pidw, r.pid, psIndent(r.depth), r.cmdline)
				if width > 0 {
					line = truncate(line, width)
				}
				out = append(out, line)
				ids = append(ids, listRow{project: root, path: b.path, pid: r.pid})
			}
		}
	}
	return out, ids, nil
}

// focusPid goes to the pane a process is running in — what `space` does on a
// process in the tui's ps view, as `nav ps` does once it has picked its match.
// It reports what happened, for the status bar.
//
// The pane is an environment variable, and macOS hides the environment of
// everything under /bin: a shell can't answer for itself. So its descendants
// are asked in turn — they inherited the pane it was started in, and the agent
// running under it is an ordinary binary whose environment reads back.
func focusPid(pid int) string {
	procs, err := listProcesses()
	if err != nil {
		return err.Error()
	}
	kids := map[int][]int{}
	for _, p := range procs {
		if p.parent != p.pid { // see psRows on the one cycle real ppids have
			kids[p.parent] = append(kids[p.parent], p.pid)
		}
	}
	for q := []int{pid}; len(q) > 0; q = q[1:] {
		mux, pane := paneOf(processEnv(q[0]))
		if mux == nil {
			q = append(q, kids[q[0]]...)
			continue
		}
		if err := mux.focus(pane); err != nil {
			return err.Error()
		}
		return "went to " + pane
	}
	return fmt.Sprintf("%d: not in a pane (or its environment is unreadable)", pid)
}

// psIndent is what a row of depth d hangs off: nothing for the shell itself,
// and the same arrow a queued prompt is drawn with for what it spawned, since
// on both trees the glyph says the same thing — this line is under that one.
func psIndent(d int) string {
	if d == 0 {
		return ""
	}
	return strings.Repeat("  ", d-1) + taskGlyph + " "
}
