package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/kong"
	"golang.org/x/term"

	"github.com/mkmik/ccwt/internal/gitutil"
	"github.com/mkmik/ccwt/internal/namegen"
)

// set by goreleaser
var version = "(devel)"

func getVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	// otherwise fallback to the version set by goreleaser
	return version
}

type NewWorktreeNameCmd struct{}

func (c *NewWorktreeNameCmd) Run() error {
	fmt.Println(namegen.Generate())
	return nil
}

type RepoRootCmd struct {
	RootWorktree bool `help:"If the repo root sits inside a Claude Code worktree (.claude/worktrees/<name>), print the enclosing repository root instead."`
}

func (c *RepoRootCmd) Run() error {
	path, err := gitutil.RepoRoot("", c.RootWorktree)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

type DotDotCmd struct{}

func (c *DotDotCmd) Run() error {
	path, err := gitutil.RepoRoot("", true)
	if err != nil {
		// git couldn't resolve the toplevel — most often because the current
		// directory was deleted out from under us (e.g. the worktree was
		// removed while we were sitting in it), so getcwd() fails. The shell
		// still records the old path in $PWD, so fall back to stripping the
		// .claude/worktrees/<name> component from it. We only trust the result
		// when the derived root still exists on disk; otherwise surface git's
		// original error rather than emit a path the shell can't cd into.
		if root, ok := gitutil.ClaudeWorktreeRepoRoot(os.Getenv("PWD")); ok {
			if fi, statErr := os.Stat(root); statErr == nil && fi.IsDir() {
				emitCdRequest(root)
				fmt.Println(root)
				return nil
			}
		}
		return err
	}
	emitCdRequest(path)
	fmt.Println(path)
	return nil
}

type NewWorktreeBranchCmd struct {
	Name        string `arg:"" optional:"" help:"Worktree name (auto-generated if omitted). Reused if a worktree with this name already exists."`
	Switch      string `help:"Check the new worktree out on this existing branch instead of creating a new branch worktree-<name>."`
	ForceCreate bool   `help:"Create a new worktree even when cwd is already inside one (otherwise the enclosing worktree's name is returned instead)."`
	Path        bool   `help:"Print the worktree's absolute path instead of its name."`
}

// out is what `new` prints: the worktree's path with --path, its name
// otherwise. Both always describe the same worktree, including in the
// enclosing-worktree case where nothing is created.
func (c *NewWorktreeBranchCmd) out(path, name string) string {
	if c.Path {
		return path
	}
	return name
}

func (c *NewWorktreeBranchCmd) Run() error {
	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		return err
	}
	path, name, err := c.create(root)
	if err != nil {
		return err
	}
	emitOSC7(path)
	emitCdRequest(path)
	fmt.Println(c.out(path, name))
	return nil
}

// create makes (or reuses) a worktree of the repo at root and reports its path
// and name, without the terminal side effects Run adds — which is what callers
// that aren't a command line (the tui) want. root is a parameter rather than
// the cwd's repo because under `tui -g` the worktree belongs to whichever
// project is selected, not to wherever the tui happens to be running.
func (c *NewWorktreeBranchCmd) create(root string) (string, string, error) {
	if c.Name == "" && c.Switch == "" && !c.ForceCreate {
		path, name, err := gitutil.CurrentClaudeWorktree()
		if err != nil {
			return "", "", err
		}
		if name != "" {
			return path, name, nil
		}
	}

	name := c.Name
	if name == "" {
		name = namegen.Generate()
	}

	parent := filepath.Join(root, ".claude", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", err
	}

	worktreePath := filepath.Join(parent, name)

	_, statErr := os.Stat(worktreePath)
	switch {
	case statErr == nil:
		// Worktree directory already exists; reuse. --switch can't be honoured
		// on a worktree we didn't create, so say so rather than silently
		// landing the user on the wrong branch.
		if c.Switch != "" {
			return "", "", fmt.Errorf("worktree %q already exists: --switch only applies when creating one", name)
		}
	case errors.Is(statErr, os.ErrNotExist):
		var addErr error
		if c.Switch != "" {
			addErr = gitutil.AddWorktreeOnBranch(root, worktreePath, c.Switch)
		} else {
			addErr = gitutil.AddWorktree(root, worktreePath, "worktree-"+name)
		}
		if addErr != nil {
			return "", "", addErr
		}
	default:
		return "", "", statErr
	}

	return worktreePath, name, nil
}

type CdCmd struct {
	Name string `arg:"" help:"Worktree name to cd into (must already exist). Use \"..\" for the enclosing repo root, or \"-\" for the previous directory ($OLDPWD)."`
}

func (c *CdCmd) Run() error {
	if c.Name == ".." {
		return (&DotDotCmd{}).Run()
	}

	if c.Name == "-" {
		// Like the shell's `cd -`: jump to the previous directory. OLDPWD is
		// exported by bash/zsh/fish, so we read it straight from the env.
		// ponytail: no existence check — the wrapper's `builtin cd` validates
		// it and surfaces the canonical error, same as native `cd -`.
		old := os.Getenv("OLDPWD")
		if old == "" {
			return errors.New("cd -: OLDPWD not set")
		}
		emitOSC7(old)
		emitCdRequest(old)
		fmt.Println(old)
		return nil
	}

	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		return err
	}

	worktreePath := filepath.Join(root, ".claude", "worktrees", c.Name)
	if _, err := os.Stat(worktreePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("worktree %q does not exist (use `ccwt new %s` to create it)", c.Name, c.Name)
		}
		return err
	}

	emitOSC7(worktreePath)
	emitCdRequest(worktreePath)
	fmt.Println(c.Name)
	return nil
}

// emitCdRequest writes path to the file named by the CCWT_WRAPPER_CD_FILE
// env var (if set), so the shell wrapper installed by `ccwt init <shell>`
// can `cd` there after this binary exits. No-op when the env var is unset,
// so users not running through the wrapper get unchanged behaviour.
func emitCdRequest(path string) {
	if f := os.Getenv("CCWT_WRAPPER_CD_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(path), 0o600)
	}
}

// emitOSC7 writes an OSC 7 escape sequence to stderr telling the terminal
// (iTerm2, Ghostty, WezTerm, …) that the current working directory is now
// `path`. Emitted only when stderr is a TTY so redirecting stderr to a file
// won't fill it with escape codes. Format: ESC ] 7 ; file://<host><path> ST
// where ST is ESC \ . The path is URL-encoded via net/url.
func emitOSC7(path string) {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	host, _ := os.Hostname()
	u := url.URL{Scheme: "file", Host: host, Path: path}
	fmt.Fprintf(os.Stderr, "\x1b]7;%s\x1b\\", u.String())
}

type RemoveCmd struct {
	Name       string `arg:"" help:"Worktree name to remove. Use \".\" for the worktree you're currently in."`
	Force      bool   `short:"D" help:"Force-delete the branch even when it is not merged."`
	KeepBranch bool   `help:"Remove the worktree but keep its branch."`
}

func (c *RemoveCmd) Run() error {
	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		return err
	}
	return c.remove(root)
}

// remove is Run against a named repo instead of the cwd's, so `tui -g` can
// remove a worktree of a project it isn't standing in. Every git command below
// therefore names root rather than relying on where the process happens to be.
func (c *RemoveCmd) remove(root string) error {
	name := c.Name
	if name == "." {
		var err error
		if _, name, err = gitutil.CurrentClaudeWorktree(); err != nil {
			return err
		}
		if name == "" {
			return errors.New(`remove .: not inside a Claude Code worktree`)
		}
	}

	worktreePath := filepath.Join(root, ".claude", "worktrees", name)
	branch := "worktree-" + name

	// A branch checked out in a worktree can only be deleted once that worktree
	// is gone, so an unmerged branch used to leave the worktree removed and the
	// branch stranded behind it. Settle it before anything is touched. A branch
	// that doesn't exist (a worktree made by `new --switch`, say) can't strand.
	if !c.KeepBranch && !c.Force && gitutil.BranchExists(root, branch) && !gitutil.MergedBranches(root)[branch] {
		return fmt.Errorf("%s is not merged: re-run with -D to delete it anyway, or --keep-branch to remove only the worktree", branch)
	}

	// Removing the worktree we're standing in leaves the process in a deleted
	// directory, which makes every subsequent git fork fail — so hop out to the
	// repo root first, and cd the shell there afterwards (same as `ccwt cd ..`).
	cwdTop, _ := gitutil.RepoRoot("", false)
	inside := cwdTop == worktreePath
	if inside {
		if err := os.Chdir(root); err != nil {
			return err
		}
	}

	if _, err := os.Stat(worktreePath); err == nil {
		if err := gitutil.RemoveWorktree(root, worktreePath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := gitutil.PruneWorktrees(root); err != nil {
		return err
	}

	if !c.KeepBranch {
		// Force-delete: the check above is the safety valve, and it is the only
		// one that agrees with the "✓" in `ccwt list`.
		if err := gitutil.DeleteBranch(root, branch); err != nil {
			return err
		}
	}

	if inside {
		return (&DotDotCmd{}).Run()
	}
	return nil
}

type ListCmd struct {
	Global bool `short:"g" help:"List the worktrees of every project listed in $XDG_CONFIG_HOME/ccwt/config.toml, not just this repo's."`
}

// marker returns the flag glyph, or the blank gutter of the same width so
// unmarked cells stay aligned. The trailing space keeps the marker from
// gluing itself to the text, which makes the text easier to select.
func marker(on bool, glyph string) string {
	if on {
		return glyph + " "
	}
	return "  "
}

// ponytail: package var so tests can force the tty branch.
var stdoutIsTTY = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

func (c *ListCmd) Run() error {
	projects, err := projectRoots(c.Global)
	if err != nil {
		return err
	}
	tty := stdoutIsTTY()
	// Fit the table to the terminal only when there is one — and GetSize
	// returns 0 when there isn't, which is exactly what "don't fit" means here.
	width := 0
	if tty {
		width, _, _ = term.GetSize(int(os.Stdout.Fd()))
	}
	_, err = renderList(os.Stdout, tty, width, projects)
	return err
}

// renderList writes the worktree table to w and returns the worktree paths in
// the order their rows were printed, which is what lets `ccwt tui` map a screen
// line back to a worktree. A path rather than a name because with -g two
// projects can each have a worktree called "fix-tests", and the path is also
// the only thing an action needs: the repo it belongs to is the part in front
// of .claude/worktrees/.
//
// tty selects the human-reader decorations (markers, ✓); the tui always asks
// for them, plain `list` only when stdout is a terminal. width is the terminal
// the table has to fit inside, or 0 for "as wide as it wants". projects are the
// repos to cover: nil means the repo we're standing in, anything else is the
// configured project list and brings the PROJECT column with it.
func renderList(out io.Writer, tty bool, width int, projects []string) ([]string, error) {
	global := projects != nil
	if !global {
		// "" is the current directory, which is how git reads "the repo we're in".
		projects = []string{""}
	}

	type row struct {
		project, name, branch, age, claude, subject, summary string
		path                                                 string // the row's identity, for the tui
		sortTime                                             time.Time
	}

	// Every lookup below shells out to git or lsof, and within a round none of
	// them depend on each other, so they all go out at once: serially they are
	// essentially the whole runtime of the command (~500ms across 21 worktrees
	// on a large repo), in parallel it costs about as much as the slowest single
	// worktree. Each goroutine owns one slice element, so no locking. There are
	// two rounds — which worktrees exist, then what each one looks like — since
	// the second can't be started until the first says what to look at.
	var (
		wg     sync.WaitGroup
		roots  = make([]string, len(projects))
		lists  = make([][]gitutil.Worktree, len(projects))
		merged = make([]map[string]bool, len(projects))
		errs   = make([]error, len(projects))
		cur    string
	)
	// The lsof scan is the slowest single thing here (~200ms) and nothing in
	// either git round needs it, so it gets its own WaitGroup and runs
	// alongside both rather than delaying the second.
	var lsof sync.WaitGroup
	var claudeCwdSet map[string]bool
	lsof.Go(func() { claudeCwdSet = claudeCwds() })
	if tty {
		wg.Go(func() { cur, _, _ = gitutil.CurrentClaudeWorktree() })
	}
	for i, dir := range projects {
		wg.Go(func() {
			wts, err := gitutil.ListWorktrees(dir)
			if err != nil {
				errs[i] = gitError(dir, err)
				return
			}
			if len(wts) == 0 {
				return
			}
			// The main worktree is the repo root the .claude/worktrees/ hang
			// off. Taking it from git rather than from the configured path
			// keeps working when that path points somewhere inside the repo.
			roots[i] = wts[0].Path
			claudeDir := filepath.Join(roots[i], ".claude", "worktrees") + string(filepath.Separator)
			lists[i] = slices.DeleteFunc(wts, func(wt gitutil.Worktree) bool {
				return !strings.HasPrefix(wt.Path, claudeDir)
			})
		})
		if tty {
			wg.Go(func() { merged[i] = gitutil.MergedBranches(dir) })
		}
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	// One flat list of worktrees, each still knowing which project it came from.
	type ref struct {
		project string
		merged  map[string]bool
		wt      gitutil.Worktree
	}
	var refs []ref
	for i, wts := range lists {
		for _, wt := range wts {
			refs = append(refs, ref{filepath.Base(roots[i]), merged[i], wt})
		}
	}

	// The worktree we're running in gets a "* " on its name, one on its last
	// commit when it has uncommitted changes, and a "✓ " on its branch when
	// that branch is already merged — but only for human readers: piped
	// output stays parseable. Unmarked cells get the same two-space gutter
	// so the columns line up.
	rows := make([]row, len(refs))
	for i, rf := range refs {
		wg.Go(func() {
			r := &rows[i]
			r.path = rf.wt.Path
			r.project = rf.project
			r.name = filepath.Base(rf.wt.Path)
			r.branch = rf.wt.Branch
			if r.branch == "" {
				r.branch = "(detached)"
			}
			if commit, err := gitutil.LastCommit(rf.wt.Path); err == nil {
				r.age = humanAge(time.Since(commit.Time))
				r.subject = commit.Subject
				r.sortTime = commit.Time
			} else {
				r.age = "?"
				r.subject = "(no commits)"
			}
			r.summary = sessionSummary(rf.wt.Path)
			if tty {
				r.name = marker(rf.wt.Path == cur, "*") + r.name
				r.branch = marker(rf.merged[rf.wt.Branch], "✓") + r.branch
				r.subject = marker(gitutil.Dirty(rf.wt.Path), "*") + r.subject
			}
		})
	}
	wg.Wait()

	// All that's left of the scan is map lookups against it.
	lsof.Wait()
	for i := range rows {
		rows[i].claude = "no"
		if isClaudeActiveIn(rows[i].path, claudeCwdSet) {
			rows[i].claude = "yes"
		}
	}

	// Stable: worktrees sharing a commit timestamp keep `git worktree list`
	// order rather than shuffling between runs. Newest-first across all the
	// projects at once, which is the order you were working in.
	slices.SortStableFunc(rows, func(a, b row) int {
		return b.sortTime.Compare(a.sortTime)
	})

	gutter := ""
	if tty {
		gutter = marker(false, "")
	}
	cols := listColumns(global)
	header := []string{gutter + "NAME", gutter + "BRANCH", "AGE", "CLAUDE", gutter + "LAST COMMIT", "SESSION"}
	if global {
		header = append([]string{"PROJECT"}, header...)
	}
	table := [][]string{header}
	paths := make([]string, len(rows))
	for i, r := range rows {
		paths[i] = r.path
		cells := []string{r.name, r.branch, r.age, r.claude, r.subject, r.summary}
		if global {
			cells = append([]string{r.project}, cells...)
		}
		table = append(table, cells)
	}
	fitTable(table, width, cols)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, r := range table {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return paths, w.Flush()
}

// gitError turns a failed git invocation into something worth printing: git
// says "fatal: not a git repository" on its stderr, which exec keeps on the
// ExitError rather than in the "exit status 128" the error itself stringifies
// to. In global mode the project it came from goes in front, since with a
// dozen repos on screen the message alone doesn't say which one is misconfigured.
func gitError(dir string, err error) error {
	msg := err.Error()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		msg = strings.TrimSpace(string(ee.Stderr))
	}
	if dir == "" {
		return errors.New(msg)
	}
	return fmt.Errorf("%s: %s", dir, msg)
}

type column struct {
	cut  func(string, int) string
	max  int // always applies; 0 = no cap
	pipe int // extra cap when there's no terminal width to fit to; 0 = no cap
}

// listColumns says how each column of the table may be shortened when it
// doesn't all fit, and how wide it is allowed to get when it does. AGE and
// CLAUDE are never longer than their own headers, so there is nothing to cut
// there.
//
// BRANCH and LAST COMMIT lose their middle rather than their end: what tells
// "worktree-elegant-…-cook" from "worktree-elegant-…-otter", or one commit
// from the next ("… tui` (#32)"), tends to be the last word.
//
// BRANCH is capped even when there's room for it: it's usually the worktree's
// own name with a "worktree-" bolted on the front, so the widest it ever needs
// to be is much narrower than the widest it could be, and every column it
// gives up goes to SESSION. PROJECT, when -g brings it along, is a repo's
// directory name and stays short on its own.
func listColumns(global bool) []column {
	cols := []column{
		{truncate, 0, 0},
		{elide, 26, 0},
		{nil, 0, 0},
		{nil, 0, 0},
		{elide, 0, 60},
		{truncate, 0, 60},
	}
	if global {
		return append([]column{{truncate, 0, 0}}, cols...) // PROJECT
	}
	return cols
}

// minCol is as narrow as a column ever gets: three characters and an ellipsis,
// under the two-space gutter. Past that a column says nothing at all, so a
// terminal too narrow to give every column that much (~43 columns) spills
// instead — there is no useful table left to render at that size.
const minCol = 6

// fitTable shortens table's cells in place until every row fits within width
// terminal columns — a row that doesn't wraps, and a wrapped row wrecks both
// the alignment and the `tui`'s cursor arithmetic. Characters come off
// whichever shrinkable column is currently the widest, so the space goes to
// the columns that need it and a short one is never cut to subsidise a long
// one. The headers are cut along with everything else, since a header wider
// than its column would only pull the column back open.
//
// width <= 0 means there's no terminal to fit to — piped output, say — and
// the fixed caps stand in for one, so a paragraph-long session summary can't
// run off into the distance.
func fitTable(table [][]string, width int, columns []column) {
	widths := make([]int, len(columns))
	for _, row := range table {
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	for i, c := range columns {
		if c.max > 0 {
			widths[i] = min(widths[i], c.max)
		}
		if width <= 0 && c.pipe > 0 {
			widths[i] = min(widths[i], c.pipe)
		}
	}
	// tabwriter pads every column but the last one by two spaces.
	total := 2 * (len(columns) - 1)
	for _, w := range widths {
		total += w
	}
	for width > 0 && total > width {
		widest := -1
		for i, c := range columns {
			if c.cut == nil || widths[i] <= minCol {
				continue
			}
			if widest < 0 || widths[i] > widths[widest] {
				widest = i
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
		total--
	}

	for _, row := range table {
		for i, c := range columns {
			if c.cut != nil {
				row[i] = c.cut(row[i], widths[i])
			}
		}
	}
}

// claudeCwds returns the set of working directories of currently-running
// Claude Code processes. We identify a process as "claude" if any of its
// mapped executable (txt) paths look like the claude binary — argv[0] is
// unreliable because the official installer leaves the process showing up
// as its version number (e.g. "2.1.139") rather than "claude".
//
// The two -c patterns (OR'd with each other, AND'ed with -d by -a) are only
// a cheap prefilter on that command name: without them lsof enumerates the
// mapped files of every process on the machine, which dominates the runtime
// of `ccwt list` (~200ms vs ~70ms here). The txt check below still decides.
//
// On any lsof failure we return an empty set rather than erroring out:
// the worst case is that the CLAUDE column reads "no" everywhere.
func claudeCwds() map[string]bool {
	cwds := map[string]bool{}
	out, err := exec.Command("lsof", "-a",
		"-c", "/claude/i", // named after the binary…
		"-c", `/^[0-9]+(\.[0-9]+)+$/`, // …or after the version, as the installer leaves it
		"-d", "cwd,txt", "-Fcn").Output()
	if err != nil {
		return cwds
	}
	type procInfo struct {
		cwd      string
		isClaude bool
	}
	procs := map[string]*procInfo{}
	var curPid, curFd string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			curPid = line[1:]
			procs[curPid] = &procInfo{}
		case 'f':
			curFd = line[1:]
		case 'n':
			p := procs[curPid]
			if p == nil {
				continue
			}
			name := line[1:]
			switch curFd {
			case "cwd":
				p.cwd = name
			case "txt":
				if isClaudeBinaryPath(name) {
					p.isClaude = true
				}
			}
		}
	}
	for _, p := range procs {
		if p.isClaude && p.cwd != "" {
			cwds[p.cwd] = true
		}
	}
	return cwds
}

func isClaudeBinaryPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "/claude/versions/") ||
		strings.HasSuffix(lower, "/bin/claude") ||
		strings.Contains(lower, "/claude-code/")
}

func isClaudeActiveIn(worktreePath string, cwds map[string]bool) bool {
	if cwds[worktreePath] {
		return true
	}
	prefix := worktreePath + string(filepath.Separator)
	for cwd := range cwds {
		if strings.HasPrefix(cwd, prefix) {
			return true
		}
	}
	return false
}

// sessionSummary describes in one line what the most recent Claude Code
// session in worktreePath was about, or "" when that worktree has never had
// one. Claude Code keeps its session transcripts as JSONL files under
// ~/.claude/projects/<cwd with every non-alphanumeric character replaced by
// "-">/, one per session, so "most recent" is simply the newest file there.
func sessionSummary(worktreePath string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, worktreePath)

	entries, err := os.ReadDir(filepath.Join(home, ".claude", "projects", slug))
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newest, newestMod = e.Name(), info.ModTime()
		}
	}
	if newest == "" {
		return ""
	}
	return summarizeTranscript(filepath.Join(home, ".claude", "projects", slug, newest))
}

// summarizeTranscript boils one session transcript down to a single line: the
// last recap the session produced — /recap, and the summaries Claude Code
// writes on its own, both land as an "away_summary" entry — or, for a session
// that never produced one, the first thing the user typed.
//
// Transcripts run to megabytes, so full JSON parsing is kept off the hot path:
// only lines that mention away_summary are parsed looking for a recap (the
// check below still decides), and the hunt for the first prompt stops as soon
// as it finds one, a handful of lines in.
func summarizeTranscript(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var recap, first string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.Contains(line, "away_summary") {
			var e struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(line), &e) == nil && e.Type == "system" && e.Subtype == "away_summary" {
				recap = e.Content
			}
			continue
		}
		if first != "" {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &e) != nil || e.Type != "user" || e.IsMeta {
			continue
		}
		// A typed prompt is a bare JSON string. Everything else the session
		// feeds back as a "user" turn — tool results, pasted attachments —
		// arrives as an array, and slash commands as a "<command-name>…" blob.
		var text string
		if json.Unmarshal(e.Message.Content, &text) == nil && !strings.HasPrefix(text, "<") {
			first = text
		}
	}
	if recap == "" {
		recap = first
	}
	// Flatten: a recap carries the UI's own nudge on the end, and a first
	// prompt can be paragraphs of text — either way a table cell gets one line.
	recap = strings.Join(strings.Fields(recap), " ")
	return strings.TrimSpace(strings.TrimSuffix(recap, "(disable recaps in /config)"))
}

type InitCmd struct {
	Shell string `arg:"" enum:"bash,zsh,fish" help:"Shell to emit the integration snippet for (bash, zsh, or fish)."`
}

const posixInitSnippet = `# Source from your rc file with:
#   source <(ccwt init zsh)    # or: ccwt init bash
ccwt() {
    local _ccwt_cd_file _ccwt_rc
    _ccwt_cd_file=$(mktemp) || return $?
    CCWT_WRAPPER_CD_FILE="$_ccwt_cd_file" command ccwt "$@"
    _ccwt_rc=$?
    if [ -s "$_ccwt_cd_file" ]; then
        builtin cd -- "$(cat -- "$_ccwt_cd_file")"
    fi
    rm -f -- "$_ccwt_cd_file"
    return $_ccwt_rc
}
`

const fishInitSnippet = `# Source from config.fish with:
#   ccwt init fish | source
function ccwt
    set -l _ccwt_cd_file (mktemp); or return $status
    set -lx CCWT_WRAPPER_CD_FILE $_ccwt_cd_file
    command ccwt $argv
    set -l _ccwt_rc $status
    if test -s $_ccwt_cd_file
        builtin cd (cat $_ccwt_cd_file)
    end
    rm -f -- $_ccwt_cd_file
    return $_ccwt_rc
end
`

func (c *InitCmd) Run() error {
	switch c.Shell {
	case "bash", "zsh":
		fmt.Print(posixInitSnippet)
	case "fish":
		fmt.Print(fishInitSnippet)
	}
	return nil
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// truncate cuts s to limit characters, ellipsis included. It counts runes
// rather than bytes: session summaries are prose, and cutting one mid-rune
// would print a replacement character.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// elide cuts s to limit characters too, but takes the characters out of the
// middle so the last word survives: "worktree-elegant-…-cook". It falls back
// to truncate when there is no last word to save, or when saving it would
// leave nothing recognisable at the front.
func elide(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	// The tail is the last word together with the separator in front of it, so
	// that the join reads the way the original did: "-cook", " (#32)".
	if i := strings.LastIndexAny(s, " -_/"); i >= 0 {
		tail := []rune(s[i:])
		head := limit - 1 - len(tail)
		if head >= 3 && len(tail) >= 2 && len(tail) < limit/2 {
			return string(r[:head]) + "…" + string(tail)
		}
	}
	return truncate(s, limit)
}

var cli struct {
	NewWorktreeName   NewWorktreeNameCmd   `cmd:"" name:"new-worktree-name" help:"Generate a Claude Code-style worktree name (adjective-verb-noun)."`
	NewWorktreeBranch NewWorktreeBranchCmd `cmd:"" name:"new" help:"Create a new worktree under .claude/worktrees/<name> on a new branch worktree-<name>, and print <name>."`
	Cd                CdCmd                `cmd:"" name:"cd" help:"cd into an existing worktree under .claude/worktrees/<name> (errors if it doesn't exist). Use \"..\" for the enclosing repo root, or \"-\" for the previous directory."`
	List              ListCmd              `cmd:"" name:"list" aliases:"ls" help:"List Claude Code worktrees with branch, age, running-session, last commit, and what the last Claude Code session there was about."`
	Tui               TuiCmd               `cmd:"" name:"tui" help:"Show the worktree list full-screen, refreshing as things change. q quits, p runs git pull, arrows select a worktree, r removes it. Under herdr, x creates a worktree and opens it, and enter (or a click) opens the selected one."`
	Remove            RemoveCmd            `cmd:"" name:"remove" help:"Delete a worktree under .claude/worktrees/<name> and its branch (merged-only; -D to force unmerged, --keep-branch to remove only the worktree). Use \".\" for the current worktree; removing it cds to the repo root."`
	RepoRoot          RepoRootCmd          `cmd:"" name:"repo-root" help:"Print the root directory of the current git repository."`
	DotDot            DotDotCmd            `cmd:"" name:".." help:"Print the enclosing repo root, stripping any .claude/worktrees/<name> suffix (shorthand for repo-root --root-worktree)."`
	Init              InitCmd              `cmd:"" name:"init" help:"Emit a shell integration snippet to source from your rc file (e.g. source <(ccwt init zsh), or for fish: ccwt init fish | source)."`

	Version kong.VersionFlag `name:"version" help:"Print version information and quit"`
}

func main() {
	ctx := kong.Parse(&cli,
		kong.Name("ccwt"),
		kong.Description("Claude Code worktree helper."),
		kong.UsageOnError(),
		kong.Vars{
			"version": getVersion(),
		},
	)
	ctx.FatalIfErrorf(ctx.Run())
}
