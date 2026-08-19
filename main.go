package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
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
	Switch      string `help:"Check the new worktree out on this existing branch instead of creating a new one."`
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
			branch, err := branchName(name)
			if err != nil {
				return "", "", err
			}
			addErr = gitutil.AddWorktree(root, worktreePath, branch)
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
	Force      bool   `short:"D" help:"Remove anyway: delete the branch even when it is not merged, and the worktree even when it has uncommitted changes."`
	KeepBranch bool   `help:"Remove the worktree but keep its branch."`
	Yes        bool   `short:"y" help:"Close the worktree's herdr workspace without asking for confirmation."`
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
	branch, err := branchName(name)
	if err != nil {
		return err
	}

	// A branch checked out in a worktree can only be deleted once that worktree
	// is gone, so an unmerged branch used to leave the worktree removed and the
	// branch stranded behind it. Settle it before anything is touched. A branch
	// that doesn't exist (a worktree made by `new --switch`, say) can't strand.
	if !c.KeepBranch && !c.Force && gitutil.BranchExists(root, branch) && !gitutil.MergedBranches(root)[branch] {
		return fmt.Errorf("%s is not merged: re-run with -D to delete it anyway, or --keep-branch to remove only the worktree", branch)
	}

	// The removal is a `git worktree remove --force`, so uncommitted work in it
	// is gone for good and no branch is left holding it — the same reason to
	// refuse as an unmerged branch, and the same way out of it.
	if !c.Force && gitutil.Dirty(worktreePath) {
		return fmt.Errorf("%s has uncommitted changes: commit them, or re-run with -D to throw them away", name)
	}

	// An agent mid-task is the third way a worktree isn't safe to remove, and
	// the only one git can't see: a branch it made a minute ago and hasn't
	// committed to is both merged and clean. Removing it closes the workspace
	// out from under the agent, so ask first — the same way out as the others.
	if !c.Force && activeIn(worktreePath, herdrBusy()) {
		return fmt.Errorf("%s has an agent working in it: let it finish, or re-run with -D to remove it anyway", name)
	}

	// Under herdr the worktree is usually an open workspace with an agent living
	// in it; close it before the directory it sits in disappears. Losing a
	// workspace you can see is a bigger surprise than losing a directory, so a
	// terminal gets asked first.
	var self string
	if underHerdr() {
		ws, err := herdrWorkspaces(root, worktreePath)
		if err != nil {
			return err
		}
		if len(ws) > 0 && !c.Yes && !confirmTTY(fmt.Sprintf("remove %s and close its herdr workspace?", name)) {
			return nil
		}
		// Ours goes last of all: closing it kills this process, so it has to
		// outlive the removal itself.
		me := os.Getenv("HERDR_WORKSPACE_ID")
		for _, id := range ws {
			if id == me {
				self = id
				continue
			}
			if err := herdrCloseWS(id); err != nil {
				return err
			}
		}
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

	// What the worktree was about, read while it is still there to read it from:
	// the session transcript is keyed by its path and the last commit is in it.
	entry := Removal{Project: root, Name: name, Topic: worktreeTopic(worktreePath), At: time.Now()}

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

	// The worktree is gone by now, so a log that can't be written is not a
	// removal that failed: saying so would have the tui report a refusal for
	// work it actually did. ponytail: dropped rather than reported — the log is
	// a convenience, and the state directory being unwritable will be obvious
	// from `ccwt worklog` coming back empty.
	_ = logRemoval(entry)

	if !c.KeepBranch {
		// Force-delete: the check above is the safety valve, and it is the only
		// one that agrees with the "✓" in `ccwt list`.
		if err := gitutil.DeleteBranch(root, branch); err != nil {
			return err
		}
	}

	if inside {
		if err := (&DotDotCmd{}).Run(); err != nil {
			return err
		}
	}
	if self != "" {
		// Closing the focused workspace hands focus to whatever herdr had open
		// before, which is some unrelated pane; the repo the worktree belonged to
		// is where the work continues. Focus it first, so the close finds our
		// workspace already unfocused and has no focus to move.
		//
		// Only when ours is the workspace being looked at, though: closing one
		// nobody has focused moves no focus at all, and someone who has switched
		// away is somewhere they chose to be — pulling them into the repo root is
		// the removal reaching further than it was asked to.
		//
		// ponytail: a focus that fails is cosmetic, and not worth abandoning the
		// close that `remove` was actually asked for.
		if herdrFocused(self) {
			if ws, err := herdrWorkspaces(root, root); err == nil && len(ws) > 0 {
				_ = herdrFocusWS(ws[0])
			}
		}
		return herdrCloseWS(self)
	}
	return nil
}

// confirmTTY asks question on stderr and reports whether the answer was yes.
// Stderr not being a terminal — a script, a pipe, the tui — is nobody to ask,
// and answers yes: the command was already given, the prompt only exists for
// the surprise of a workspace closing under someone watching it.
func confirmTTY(question string) bool {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	var answer string
	// A read that can't happen — EOF, no stdin — leaves answer empty, which is
	// "no": with a terminal to ask, nothing closes unless someone said so.
	fmt.Scanln(&answer)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
}

type LockCmd struct {
	Name string `arg:"" help:"Worktree name to lock. Use \".\" for the worktree you're currently in."`
}

func (c *LockCmd) Run() error {
	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		return err
	}
	name := c.Name
	if name == "." {
		if _, name, err = gitutil.CurrentClaudeWorktree(); err != nil {
			return err
		}
		if name == "" {
			return errors.New(`lock .: not inside a Claude Code worktree`)
		}
	}
	// A missing worktree needs no check of ours: git's own "not a working tree"
	// error is the clearest thing to say, and LockWorktree passes it through.
	return gitutil.LockWorktree(root, filepath.Join(root, ".claude", "worktrees", name))
}

type DoneCmd struct {
	Force      bool `short:"D" help:"Remove anyway: delete the branch even when it is not merged, and the worktree even when it has uncommitted changes."`
	KeepBranch bool `help:"Remove the worktree but keep its branch."`
}

// Run is `remove .`, which closes the workspace we're standing in as its last
// act. No confirmation: closing the workspace is what "done" asked for, and
// asking again is asking the same question twice.
func (c *DoneCmd) Run() error {
	return (&RemoveCmd{Name: ".", Force: c.Force, KeepBranch: c.KeepBranch, Yes: true}).Run()
}

type GcCmd struct {
	Yes bool `short:"y" help:"Remove without asking for confirmation."`
}

// Run collects the worktrees that are done with — branch already merged, no
// Claude Code session running in them and no agent herdr says is working in
// them — and removes each exactly as `ccwt remove` would, branch included.
func (c *GcCmd) Run() error {
	root, err := gitutil.RepoRoot("", true)
	if err != nil {
		return err
	}
	names, kept, err := gcNames(root)
	if err != nil {
		return err
	}
	if kept != "" {
		fmt.Fprintf(os.Stderr, "%s: collectable, but it is the worktree you are in — keeping it\n", kept)
	}
	if len(names) == 0 {
		fmt.Println("nothing to collect")
		return nil
	}
	for _, name := range names {
		fmt.Println(name)
	}
	if !c.Yes {
		fmt.Printf("remove %d worktree(s) and their branches? [y/N] ", len(names))
		var answer string
		// A read that can't happen — no tty, EOF — leaves answer empty, which
		// is "no": nothing gets removed unless someone said so.
		fmt.Scanln(&answer)
		if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			return nil
		}
	}
	for _, name := range names {
		// Yes: the prompt above already covered the whole batch.
		if err := (&RemoveCmd{Name: name, Yes: true}).remove(root); err != nil {
			return err
		}
	}
	return nil
}

// gcNames is what `gc` would collect in the repo at root, and — as kept — the
// worktree it left behind because it is the one we're standing in: collecting
// that would pull the ground out from under the shell, and `ccwt remove .` from
// somewhere else is the way to actually get rid of it. The tui's gc runs on
// this too, so both agree on what "done with" means.
func gcNames(root string) (names []string, kept string, err error) {
	active := claudeCwds()
	maps.Copy(active, herdrBusy())
	names, err = gcCandidates(root, active)
	if err != nil {
		return nil, "", err
	}
	if _, cur, err := gitutil.CurrentClaudeWorktree(); err == nil && cur != "" {
		if i := slices.Index(names, cur); i >= 0 {
			return slices.Delete(names, i, i+1), cur, nil
		}
	}
	return names, "", nil
}

// gcCandidates names the Claude Code worktrees of the repo at root that are
// safe to reclaim: the "✓" of `ccwt list` (branch contained in main, which is
// also what `remove` requires), no "*" (uncommitted changes, which `remove`
// also refuses) and a "no" in its CLAUDE column. active is the set of cwds of
// running Claude Code processes, as claudeCwds reports them.
//
// A detached worktree is never a candidate: it has no branch to be merged.
func gcCandidates(root string, active map[string]bool) ([]string, error) {
	wts, err := gitutil.ListWorktrees(root)
	if err != nil {
		return nil, gitError(root, err)
	}
	merged := gitutil.MergedBranches(root)
	prefix := filepath.Join(root, ".claude", "worktrees") + string(filepath.Separator)
	var names []string
	for _, wt := range wts {
		if strings.HasPrefix(wt.Path, prefix) && merged[wt.Branch] && !gitutil.Dirty(wt.Path) && !activeIn(wt.Path, active) {
			names = append(names, filepath.Base(wt.Path))
		}
	}
	return names, nil
}

type ListCmd struct {
	Global    bool   `short:"g" help:"List the worktrees of every project listed in $XDG_CONFIG_HOME/ccwt/config.toml, not just this repo's."`
	NoHeaders bool   `help:"Leave out the header row, for feeding the table to cut, awk, or a shell loop."`
	Sort      string `help:"Order the worktrees by \"freshness\" — the last commit or the last thing written to the newest Claude Code session there, whichever is younger — or by \"commit\" alone. Overrides sort in the config file (freshness when neither says)."`
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
	_, _, err = renderList(os.Stdout, tty, width, projects, nil, !c.NoHeaders, c.Sort)
	return err
}

// listRow identifies one body line of the table: a worktree by its path, a
// queued prompt by its task id, a "<new>" one — a prompt whose worktree has
// been removed, waiting for one of its own — by both, or, under -g, a project's
// section header, with neither. All of them carry the project they belong to,
// as the repo's main worktree, which is what an action on the row resolves
// against.
//
// A worktree by path rather than by name because with -g two projects can each
// have a worktree called "fix-tests", and the path is also the only thing an
// action needs. A project by root path rather than by the directory name the
// header shows, for the same reason: two checkouts can share a basename.
// The tui's ps view (`P`) puts its own lines in the same rows: a process by
// its pid, under the worktree it is running in, which is a row like any other
// worktree's. Only pid tells those apart, so the predicates below check it.
type listRow struct {
	project, path string
	task          int64 // a queued prompt's id; 0 for the other rows
	pid           int   // a process in the ps view; 0 for the other rows
}

// worktree reports whether the row is a live worktree — the rows the
// per-worktree keys apply to. Everything else on screen is something you can
// land on but not open, pull or remove. A process in the ps view carries the
// worktree it is running in, so that two worktrees running the same pid — a
// child of one of them, cd'd into the other — stay two rows; the pid is what
// says the row is the process rather than the worktree.
func (r listRow) worktree() bool { return r.pid == 0 && r.path != "" && r.task == 0 }

// pending reports whether the row is a "<new>": a queued prompt with no
// worktree left under it — the path it still carries is the removed one's — and
// so the one row where opening means creating.
func (r listRow) pending() bool { return r.path != "" && r.task > 0 }

// section reports whether the row is a project's section header, the only kind
// of row there is anything to fold. The ps view's lines carry a project too and
// fold into nothing, so a pid — its own, or the -1 of a line that is only a
// line — is what says this isn't one of them.
func (r listRow) section() bool {
	return r.path == "" && r.task == 0 && r.pid == 0 && r.project != ""
}

// process reports whether the row is a process in the ps view — the rows
// `space` goes to the pane of.
func (r listRow) process() bool { return r.pid > 0 }

// cached memoizes f per key, recomputing only when stamp changes. Two kinds of
// stamp are used below: a coarse time window for a lookup that has no cheap way
// to tell whether its answer moved, and a file's mtime for one that has.
//
// The tui re-reads the whole list every couple of seconds, and some of what a
// row is built from costs far more than that cadence is worth: `git status`
// across 51 worktrees of a monorepo is ~3s of cpu a round, some 90% of
// everything the tui spends, to answer "is there unsaved work here?" — which
// nobody needs to the second.
type cached[T any] struct {
	mu sync.Mutex
	m  map[string]stamped[T]
}

type stamped[T any] struct {
	stamp string
	v     T
}

func (c *cached[T]) get(key, stamp string, f func() T) T {
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && e.stamp == stamp {
		return e.v
	}
	v := f()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]stamped[T]{}
	}
	c.m[key] = stamped[T]{stamp, v}
	return v
}

// window names the current stretch of d, so that everything asked within one
// stretch shares an answer. Rows are all built in the same round, so they all
// land in the same stretch and refresh together.
func window(d time.Duration) string {
	return strconv.FormatInt(time.Now().UnixNano()/int64(d), 10)
}

// How long the tui may go on showing the last answer git gave about a worktree
// being dirty, or a branch being merged. Both decorate the "can this go?"
// column, and both cost a full worktree scan to ask.
//
// ponytail: a time window, not an fs watcher. Watching would make the glyph
// instant, at the price of a watcher per worktree and an event loop this has no
// other use for. The one-shot commands (remove, prune) call gitutil directly
// and stay exact — a refusal to delete unsaved work must not read a cache.
const gitScanWindow = 30 * time.Second

var (
	dirtyCache  cached[bool]
	pushedCache cached[bool]
	mergedCache cached[map[string]bool]
	commitCache cached[gitutil.Commit]
	topicCache  cached[string]
)

// renderList writes the worktree table to w and returns one listRow per body
// line, in the order the lines were printed, which is what lets `ccwt tui` map
// a screen line back to what's on it, along with every row's cells in full,
// keyed by that row — the same values before fitTable cuts them down, which is
// what the tui's details pane shows.
//
// tty selects the human-reader decorations (markers, ✓); the tui always asks
// for them, plain `list` only when stdout is a terminal. width is the terminal
// the table has to fit inside, or 0 for "as wide as it wants". projects are the
// repos to cover: nil means the repo we're standing in, anything else is the
// configured project list and gets a foldable section per project. collapsed
// holds the roots whose section is folded shut — only the tui has any, since
// there is nothing to unfold them with on the command line. headers draws the
// header row; `list --no-headers` is the only caller that doesn't want it.
// sort is the order to put the worktrees in, or "" for whatever the config says.
func renderList(out io.Writer, tty bool, width int, projects []string, collapsed map[string]bool, headers bool, sort string) ([]listRow, map[listRow][]string, error) {
	global := projects != nil
	if !global {
		// "" is the current directory, which is how git reads "the repo we're in".
		projects = []string{""}
	}
	cols, err := listColumns()
	if err != nil {
		return nil, nil, err
	}
	order, err := sortOrder(sort)
	if err != nil {
		return nil, nil, err
	}

	type row struct {
		project, name, branch, age, claude, topic string
		subject                                   string // the last commit, TOPIC's fallback
		path                                      string // the row's identity, for the tui
		sortTime                                  time.Time
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
	// The "you are here" marker is the last thing that asked the current
	// directory anything, so under -g it goes too: the repos are the configured
	// ones, and where ccwt was started needn't be a repo at all — asking git
	// there only earns a "fatal: not a git repository" on the user's stderr.
	if tty && !global {
		wg.Go(func() { cur, _, _ = gitutil.CurrentClaudeWorktree() })
	}
	// Needed by the second round rather than after it, so it goes in the first.
	var busy map[string]bool
	if tty {
		wg.Go(func() { busy = herdrBusy() })
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
			// "" is "the repo we're in", which names a different repo from one
			// working directory to the next — so the cache key is where that
			// resolves to, not the empty string every such caller passes.
			key := dir
			if key == "" {
				key, _ = os.Getwd()
			}
			wg.Go(func() {
				merged[i] = mergedCache.get(key, window(gitScanWindow), func() map[string]bool {
					return gitutil.MergedBranches(dir)
				})
			})
		}
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
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
			refs = append(refs, ref{roots[i], merged[i], wt})
		}
	}

	// Two glyphs lead the name. First the "* " of the worktree we're running
	// in, then the one saying whether the worktree is safe to remove: a "✓ "
	// when its branch is already merged — a "☐ " when it isn't but everything
	// here is pushed, so the ball is in a reviewer's court and there is nothing
	// to do but wait — or a "* " when the worktree has uncommitted changes,
	// which beats both: whatever git says about the branch or the remote,
	// there's unsaved work here and it isn't safe to delete. An agent
	// working in it beats all three, with the "✳ " of a live session: git sees a
	// fresh branch with nothing on it, which is precisely what an agent that
	// has just started looks like. Both ride at the head of the row because
	// "where am I?" and "can this go?" are the actionable things on the line,
	// and reading down two left-hand strips answers them for the whole table.
	// Human readers only: piped output stays parseable. Unmarked cells get the
	// same two-space gutter so the columns line up.
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
			// A zero time is how "git had no answer" is cached; no real commit
			// has one. AGE is still computed fresh off it every round, so the
			// column keeps counting between the reads.
			commit := commitCache.get(rf.wt.Path, window(gitScanWindow), func() gitutil.Commit {
				c, err := gitutil.LastCommit(rf.wt.Path)
				if err != nil {
					return gitutil.Commit{}
				}
				return c
			})
			if !commit.Time.IsZero() {
				r.age = humanAge(time.Since(commit.Time))
				r.subject = commit.Subject
				r.sortTime = commit.Time
			} else {
				r.age = "?"
				r.subject = "(no commits)"
			}
			session, stamp, wrote := newestTranscript(rf.wt.Path)
			r.topic = topic(transcriptSummary(rf.wt.Path, session, stamp), r.subject)
			// Under freshness the session counts too, and a worktree with no
			// commits at all — an agent that started twenty minutes ago — goes
			// to the top on the transcript alone rather than to the bottom on
			// its zero commit time.
			if order == sortFreshness && wrote.After(r.sortTime) {
				r.sortTime = wrote
			}
			if tty {
				glyph, on := "✓", rf.merged[rf.wt.Branch]
				if !on && pushedCache.get(rf.wt.Path, window(gitScanWindow), func() bool {
					return gitutil.Pushed(rf.wt.Path)
				}) {
					glyph, on = reviewGlyph, true
				}
				if dirtyCache.get(rf.wt.Path, window(gitScanWindow), func() bool {
					return gitutil.Dirty(rf.wt.Path)
				}) {
					glyph, on = "*", true
				}
				if activeIn(rf.wt.Path, busy) {
					glyph, on = sessionGlyph, true
				}
				r.name = marker(rf.wt.Path == cur, "*") + marker(on, glyph) + r.name
			}
		})
	}
	wg.Wait()

	// All that's left of the scan is map lookups against it.
	lsof.Wait()
	for i := range rows {
		rows[i].claude = "no"
		if activeIn(rows[i].path, claudeCwdSet) {
			rows[i].claude = "yes"
		}
	}

	// Stable: worktrees sharing a timestamp keep `git worktree list` order
	// rather than shuffling between runs. Newest-first, which is the order you
	// were working in — across all the projects at once outside -g's sections,
	// and within each section under them.
	slices.SortStableFunc(rows, func(a, b row) int {
		return b.sortTime.Compare(a.sortTime)
	})

	gutter := ""
	if tty {
		gutter = marker(false, "")
	}
	// Everything above builds the whole row; pick is where the hidden columns
	// go, so a column's absence can't change anything but the printing.
	pick := func(cells ...string) []string {
		out := make([]string, len(cols))
		for i, c := range cols {
			out[i] = cells[c.i]
		}
		return out
	}
	var table [][]string
	if headers {
		head := make([]string, len(cols))
		for i, c := range cols {
			head[i] = c.name
			if c.i == 0 {
				head[i] = gutter + gutter + c.name // clear of the two glyphs a name leads with
			}
		}
		table = append(table, head)
	}
	// The queued prompts are read here, on the same refresh as everything else:
	// that is also all the cross-process notification there is: another ccwt
	// writing to the database shows up on the next tick, a second or two later,
	// which is soon enough for work that is by definition waiting on something.
	tasks, err := loadTasks()
	if err != nil {
		return nil, nil, err
	}
	queue := indexTasks(tasks)
	live := make(map[string]bool, len(rows))
	for _, r := range rows {
		live[r.path] = true
	}

	var lines []listRow
	details := map[listRow][]string{}
	add := func(id listRow, cells []string) {
		lines = append(lines, id)
		table = append(table, pick(cells...)) // pick copies, so cells is ours to keep
	}
	// emitTask draws one queued prompt and then whatever is queued behind it:
	// the chain is a tree, and it's drawn as one, indented under the row it is
	// waiting for. name is what the details pane calls the whole chain — the
	// worktree it hangs off, since a prompt has no name of its own.
	var emitTask func(name string, t Task, depth int)
	emitTask = func(name string, t Task, depth int) {
		id := listRow{project: t.Project, task: t.ID}
		cells := taskCells(t, gutter, depth)
		add(id, cells)
		cells[0] = name
		details[id] = cells
		for _, k := range queue.kids[t.ID] {
			emitTask(name, k, depth+1)
		}
	}
	emit := func(r row) {
		id := listRow{project: r.project, path: r.path}
		cells := []string{r.name, r.branch, r.age, r.claude, r.topic}
		add(id, cells)
		// The pane names the worktree rather than repeating the row: the two
		// glyphs the name leads with say where you are and whether it's safe to
		// remove, and neither is something to copy out of a details view.
		cells[0] = filepath.Base(r.path)
		details[id] = cells
		for _, t := range queue.roots[r.path] {
			emitTask(filepath.Base(r.path), t, 0)
		}
	}
	// emitPending draws what is left of a chain whose worktree has been removed:
	// each prompt that was waiting on it stands on its own row now, named
	// "<new>" for the worktree it hasn't got yet, with whatever was queued
	// behind it still under it — drawn like the worktree it is about to become,
	// since that is what opening the row does. Deleting the work is not the same
	// as cancelling what was queued behind it.
	emitPending := func(project string) {
		for _, t := range queue.pending(project, live) {
			// A prompt queued with nothing selected never had a worktree to wait
			// on, so the project stands in as the row's path: the row is a
			// worktree of that project, waiting to be made, which is what the
			// removed one's path says for the rest of them.
			id := listRow{project: project, path: cmp.Or(t.Worktree, project), task: t.ID}
			cells := []string{gutter + gutter + newName, "", humanAge(time.Since(t.Created)), "", t.Prompt}
			add(id, cells)
			cells[0] = newName
			details[id] = cells
			for _, k := range queue.kids[t.ID] {
				emitTask(newName, k, 0)
			}
		}
	}
	if !global {
		for _, r := range rows {
			emit(r)
		}
		emitPending(roots[0])
	} else {
		// Sections in the order the config lists the projects: stable, unlike an
		// order derived from the worktrees, which would shuffle the sections
		// around under the cursor every time someone committed. Two config
		// entries pointing into the same repo share the one section. A project
		// with no worktrees still gets its (0) header — it's a configured
		// project, and in the tui that header is where `x` makes its first
		// worktree; only one git couldn't read at all is left out.
		seen := map[string]bool{}
		for _, root := range roots {
			if root == "" || seen[root] {
				continue
			}
			seen[root] = true
			var mine []row
			for _, r := range rows {
				if r.project == root {
					mine = append(mine, r)
				}
			}
			lines = append(lines, listRow{project: root})
			// In the first column drawn, whichever that is: the header names the
			// project and folds the section, and both have to survive whatever
			// the config left out.
			head := make([]string, len(cols))
			head[0] = section(root, len(mine), collapsed[root])
			table = append(table, head)
			if collapsed[root] {
				continue
			}
			for _, r := range mine {
				emit(r)
			}
			emitPending(root)
		}
	}
	fitTable(table, width, cols)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, r := range table {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return lines, details, w.Flush()
}

// The glyphs TOPIC is prefixed with, saying where the line came from: Claude
// Code's own asterisk for a session, a branch for a commit subject. The
// session one does second duty at the head of the row, where it marks the
// worktree an agent is working in right now.
//
// taskGlyph is the odd one out: it leads a queued prompt's NAME rather than its
// TOPIC, because on that row the structure is the thing to say — what this is
// waiting for is which row it hangs under.
//
// reviewGlyph is the odd one out twice over: it only leads NAME, and it says
// nothing about where a line came from — an unchecked box for a branch that is
// pushed and quiet, i.e. waiting on a review or on CI, and read against the "✓"
// it sits in the same column as: sign-off pending rather than granted.
// Single-width, like the rest: the leading gutter is two columns and emoji are
// two on their own.
const (
	sessionGlyph = "✳"
	commitGlyph  = "⎇"
	taskGlyph    = "↳"
	reviewGlyph  = "☐"
)

// topic is what the worktree is about in one line: what the last Claude Code
// session there was doing, or — for a worktree nobody has run one in — the last
// commit. Two sources in one column because neither fills it on its own: a
// worktree with a session has a commit subject that only repeats it at best,
// and one without leaves a blank cell.
func topic(summary, subject string) string {
	if summary != "" {
		return sessionGlyph + " " + summary
	}
	return commitGlyph + " " + subject
}

// topicCut shortens a TOPIC cell the way its source wants shortening — the
// glyph says which. A commit subject loses its middle (see listColumns), a
// session summary is a sentence and reads better cut off at the end.
func topicCut(s string, limit int) string {
	if strings.HasPrefix(s, commitGlyph) {
		return elide(s, limit)
	}
	return truncate(s, limit)
}

// section is a project's header line: a disclosure triangle, the repo's
// directory name, and how many worktrees are underneath — the count being the
// only thing left to say about a section once it's folded shut.
func section(root string, n int, collapsed bool) string {
	glyph := "▾"
	if collapsed {
		glyph = "▸"
	}
	return fmt.Sprintf("%s %s (%d)", glyph, filepath.Base(root), n)
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
	name string
	i    int // where its cell sits in the full row
	cut  func(string, int) string
	max  int // always applies; 0 = no cap
	pipe int // extra cap when there's no terminal width to fit to; 0 = no cap
}

// allColumns says how each column of the table may be shortened when it
// doesn't all fit, and how wide it is allowed to get when it does. AGE and
// CLAUDE are never longer than their own headers, so there is nothing to cut
// there.
//
// BRANCH, and a TOPIC showing a commit, lose their middle rather than their
// end: what tells "worktree-elegant-…-cook" from "worktree-elegant-…-otter",
// or one commit from the next ("… tui` (#32)"), tends to be the last word.
//
// BRANCH is capped even when there's room for it — it's usually the worktree's
// own name with a "worktree-" bolted on the front, so the widest it ever needs
// to be is much narrower than the widest it could be, and everything it gives
// up goes to TOPIC.
func allColumns() []column {
	cols := []column{
		{name: "NAME", cut: truncate},
		{name: "BRANCH", cut: elide, max: 26},
		{name: "AGE"},
		{name: "CLAUDE"},
		{name: "TOPIC", cut: topicCut, pipe: 60},
	}
	for i := range cols {
		cols[i].i = i
	}
	return cols
}

// listColumns is the columns to draw: the config's, in the order it names
// them, or all of them when it says nothing. A name that isn't a column is an
// error rather than a silently missing column — the config is hand-written,
// and a typo there is otherwise invisible.
func listColumns() ([]column, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	all := allColumns()
	if len(cfg.Columns) == 0 {
		return all, nil
	}
	cols := make([]column, 0, len(cfg.Columns))
	for _, name := range cfg.Columns {
		i := slices.IndexFunc(all, func(c column) bool { return strings.EqualFold(c.name, name) })
		if i < 0 {
			names := make([]string, len(all))
			for j, c := range all {
				names[j] = strings.ToLower(c.name)
			}
			return nil, fmt.Errorf("%s: unknown column %q in `columns` (have %s)", configPath(), name, strings.Join(names, ", "))
		}
		cols = append(cols, all[i])
	}
	return cols, nil
}

// oneLine is a value as a row of the table can hold it. A queued prompt is
// typed with whatever line breaks its author wanted, and the table is the one
// place they can't survive: a cell with a break in it wraps the row, which
// wrecks the alignment and the tui's cursor arithmetic alike. Each becomes the
// space it stands in for; the prompt itself keeps them.
func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }

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
			row[i] = oneLine(cell)
			widths[i] = max(widths[i], utf8.RuneCountInString(row[i]))
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

// activeIn reports whether any of cwds is the worktree at worktreePath or sits
// inside it — an agent that cd'd into a subdirectory is still working here.
// The cwds are whatever the caller counts as active: running Claude Code
// processes (claudeCwds), agents herdr says are mid-task (herdrBusy), or both.
func activeIn(worktreePath string, cwds map[string]bool) bool {
	for cwd := range cwds {
		if under(cwd, worktreePath) {
			return true
		}
	}
	return false
}

// newestTranscript is the most recent Claude Code session run in worktreePath —
// the file it was recorded in, a stamp that changes whenever that file does, and
// when it last changed — or the zero values when that worktree has never had
// one. Claude Code keeps its session transcripts as JSONL files under
// ~/.claude/projects/<cwd with every non-alphanumeric character replaced by
// "-">/, one per session, so "most recent" is simply the newest file there.
//
// Keyed by the path and kept under the home directory, which is why a removed
// worktree still has one to show: the transcript outlives the tree it was about.
//
// ponytail: the mtime stands in for "when the agent last said something" — a
// transcript is appended to as the session runs, so the file moves whenever
// anything does. Parse the last entry's timestamp out of it if the distinction
// between the agent writing and the user typing ever matters; for freshness it
// doesn't, both mean work is happening here.
func newestTranscript(worktreePath string) (path, stamp string, mod time.Time) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", time.Time{}
	}
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, worktreePath)

	dir := filepath.Join(home, ".claude", "projects", slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", time.Time{}
	}
	var newest string
	var newestMod time.Time
	var newestSize int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newest, newestMod, newestSize = e.Name(), info.ModTime(), info.Size()
		}
	}
	if newest == "" {
		return "", "", time.Time{}
	}
	return filepath.Join(dir, newest), fmt.Sprintf("%s %d %d", newest, newestMod.UnixNano(), newestSize), newestMod
}

// sessionSummary describes in one line what the most recent Claude Code
// session in worktreePath was about, or "" when that worktree has never had
// one.
func sessionSummary(worktreePath string) string {
	path, stamp, _ := newestTranscript(worktreePath)
	return transcriptSummary(worktreePath, path, stamp)
}

// transcriptSummary is sessionSummary for a caller that has already asked
// newestTranscript — the list, which wants the mtime as well and shouldn't scan
// the directory twice to get both.
func transcriptSummary(worktreePath, path, stamp string) string {
	if path == "" {
		return ""
	}
	// A transcript runs to megabytes and the tui asks every row for one every
	// couple of seconds; reading and scanning files that haven't moved is the
	// bulk of what the tui process itself burns. The directory listing already
	// knows whether one moved, so keep the last answer until it does.
	return topicCache.get(worktreePath, stamp, func() string { return summarizeTranscript(path) })
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

// transcriptLines is a whole session transcript as something to read: what was
// asked, what Claude said back, and the tools it reached for in between, in the
// order they happened. Who said what is the indentation — a prompt flush left
// behind a "›", the answer set in under it — since a pane has no colours to
// spare and a transcript is mostly the answer.
//
// Tool results are left out, and they are the bulk of the megabytes: what a
// session did reads perfectly well from the prompts, the prose and the calls,
// and the output of a `git status` from last Tuesday is not what anyone is
// scrolling back for. Parsing every line in full is fine here, unlike
// summarizeTranscript's hot path — this runs once, on the keystroke that opens
// the page.
func transcriptLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var out []string
	// One turn is one paragraph: a blank line in front of it, and its own text
	// broken at its newlines — a frame line holding one would scroll the screen
	// out from under the cursor arithmetic every later frame relies on. The
	// control characters go for the same reason: each is one rune to the width
	// arithmetic and something else entirely on the screen.
	say := func(prefix, text string) {
		text = strings.Map(func(r rune) rune {
			switch {
			case r == '\n':
				return r
			case r == '\t':
				return ' '
			case r < ' ', r == 0x7f:
				return -1
			}
			return r
		}, text)
		if strings.TrimSpace(text) == "" {
			return
		}
		out = append(out, "")
		for _, l := range strings.Split(strings.Trim(text, "\n"), "\n") {
			out = append(out, prefix+l)
		}
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		var e struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch {
		case e.Type == "user" && !e.IsMeta:
			// A typed prompt is a bare JSON string, the same way summarizeTranscript
			// finds the first one: everything else the session feeds back as a user
			// turn is an array, and slash commands are a "<command-name>…" blob.
			var text string
			if json.Unmarshal(e.Message.Content, &text) == nil && !strings.HasPrefix(text, "<") {
				say("› ", text)
			}
		case e.Type == "assistant":
			var blocks []struct {
				Type  string         `json:"type"`
				Text  string         `json:"text"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			}
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					say("  ", b.Text)
				case "tool_use":
					say("  ⚒ ", b.Name+toolArg(b.Input))
				}
			}
		}
	}
	return out
}

// toolArg is the one argument of a tool call worth a line of the page: the
// command a Bash ran, the file a Read read. First of the usual names that's
// there wins, flattened and cut — the page is a read of what happened, not a
// transcript of how to do it again. Written from ~, since a tool called in a
// worktree names its files by the whole way there and half of that is the home
// directory the reader is sitting in.
func toolArg(input map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "prompt", "description"} {
		if s, ok := input[k].(string); ok && strings.TrimSpace(s) != "" {
			return ": " + truncate(shortenHome(strings.Join(strings.Fields(s), " ")), 100)
		}
	}
	return ""
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

// zshCompletionSnippet rides along with the zsh integration, so the one
// `source <(ccwt init zsh)` people already have installs completion too. %s is
// the command menu, generated from kong's own model so it can't drift from the
// commands. ponytail: only the commands are completed, not their flags — add
// flags when someone misses them.
const zshCompletionSnippet = `
# Completion for the function above. compdef only exists once compinit has run,
# so source this after it (nothing breaks if you don't, you just get no menu).
_ccwt() {
    local -a cmds=(
%s    )
    local context state line
    typeset -A opt_args
    _arguments -C '1:command:->cmd' '*::arg:->arg'
    case $state in
        cmd) _describe -t commands 'ccwt command' cmds ;;
        arg)
            # ponytail: the commands taking a worktree name, spelled out, with
            # the literals each one also accepts. A fourth one goes here.
            case $words[1] in
                cd) _ccwt_worktrees .. - ;;
                remove|lock) _ccwt_worktrees . ;;
            esac
            ;;
    esac
}

# The worktrees of the repo the shell is standing in, preceded by whatever
# literals the caller passed. Nothing to offer outside a repo, and the error
# git prints there is not the completer's to show.
_ccwt_worktrees() {
    local root
    root=$(command ccwt repo-root --root-worktree 2>/dev/null) || return
    local -a names
    names=("$@" $root/.claude/worktrees/*(N/:t))
    _describe -t worktrees worktree names
}

if (( $+functions[compdef] )); then
    compdef _ccwt ccwt
fi
`

func (c *InitCmd) Run(kctx *kong.Context) error {
	switch c.Shell {
	case "bash", "zsh":
		fmt.Print(posixInitSnippet)
		if c.Shell == "zsh" {
			fmt.Printf(zshCompletionSnippet, zshCommandMenu(kctx.Model.Node))
		}
	case "fish":
		fmt.Print(fishInitSnippet)
	}
	return nil
}

// zshCommandMenu renders the children of n as _describe entries, one
// 'name:description' per line, aliases included so `ccwt ls` completes too.
func zshCommandMenu(n *kong.Node) string {
	var b strings.Builder
	for _, child := range n.Children {
		if child.Hidden {
			continue
		}
		for _, name := range slices.Concat([]string{child.Name}, child.Aliases) {
			fmt.Fprintf(&b, "        %s\n", zshQuote(name+":"+zshDescription(child.Help)))
		}
	}
	return b.String()
}

// zshDescription cuts a command's help down to what fits on a menu line: the
// first sentence, capped. The help texts here are paragraphs — the rest of one
// belongs in `ccwt --help`, not next to a completion candidate.
func zshDescription(help string) string {
	if i := strings.Index(help, ". "); i >= 0 {
		help = help[:i]
	}
	return truncate(strings.TrimSuffix(help, "."), 60)
}

// zshQuote wraps s in the single quotes zsh takes literally. The help texts are
// full of apostrophes ("the one you're in"), and each one would otherwise end
// the string and leave the rest as shell syntax.
func zshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

// NavCmd is navigation: find the thing you have in mind and put the cursor in
// it. What you have in mind is either something running ("the agent I set on
// the parser") or somewhere you work ("the mTLS one"), so those are the two
// ways to search and there is no third. Both take a fuzzy pattern.
type NavCmd struct {
	Ps NavPsCmd `cmd:"" name:"ps" help:"Go to the pane a process is running in: the best fuzzy match on the command lines of your running processes."`
	Ws NavWsCmd `cmd:"" name:"ws" help:"Go to a workspace: the best fuzzy match on its title, or on the name of the worktree it has open."`
}

type NavPsCmd struct {
	Pattern string `arg:"" help:"Fuzzy pattern, matched against the full command line of each of your processes."`
	DryRun  bool   `help:"Don't go anywhere: print where it would have gone — the workspace's name, the tab's name and the tab's address, a line each."`
}

// Run goes to the pane of the best-matching process that is in one. Every
// candidate is tried, best first, rather than only the winner: the tightest
// match on the machine may well be a daemon nobody is sitting in front of, and
// a pattern typed at a navigation command means "the one I can go to".
func (c *NavPsCmd) Run() error {
	// Our own processes with their full command lines — the pattern is typed
	// against what `ps` shows, and another user's pane isn't ours to go to.
	procs, err := listProcesses()
	if err != nil {
		return err
	}
	parents := map[int]int{}
	for _, p := range procs {
		parents[p.pid] = p.parent
	}
	// Ourselves and everything that spawned us: `ccwt nav ps claude` carries
	// the pattern on its own command line, and so does the shell that ran it,
	// and the agent above that is the pane you are already sitting in. pgrep
	// leaves its own ancestors out of a match for the same reason.
	ours := map[int]bool{}
	for pid := os.Getpid(); pid > 1 && !ours[pid]; pid = parents[pid] {
		ours[pid] = true
	}
	type match struct {
		pid     int
		score   int
		cmdline string
	}
	var matches []match
	for _, p := range procs {
		if ours[p.pid] {
			continue
		}
		if score, ok := fuzzyScore(p.cmdline, c.Pattern); ok {
			matches = append(matches, match{p.pid, score, p.cmdline})
		}
	}
	slices.SortFunc(matches, func(a, b match) int { return b.score - a.score })
	for _, m := range matches {
		mux, pane := paneOf(processEnv(m.pid))
		if mux == nil {
			continue
		}
		if c.DryRun {
			where, err := mux.where(pane)
			if err != nil {
				return err
			}
			where.print()
			return nil
		}
		if err := mux.focus(pane); err != nil {
			return err
		}
		fmt.Println(pane, truncate(m.cmdline, 100))
		return nil
	}
	if len(matches) == 0 {
		return fmt.Errorf("%s: no process matches", c.Pattern)
	}
	return fmt.Errorf("%s: nothing that matches is in a pane (or its environment is unreadable)", c.Pattern)
}

type NavWsCmd struct {
	Query  string `arg:"" help:"Fuzzy pattern, matched against each workspace's title and against the name of the worktree it has open."`
	DryRun bool   `help:"Don't go anywhere: print where it would have gone — the workspace's name, the tab's name and the tab's address, a line each."`
}

// Run goes to the workspace whose title — or whose worktree's name — matches
// best. Both, because a workspace ccwt opens starts out named after its
// worktree ("frolicking-tumbling-duckling") and is usually renamed to what the
// work is ("mTLS subresource"), and either is a thing you'd type.
//
// ponytail: herdr only, a workspace being a herdr thing. `nav ps` is the half
// that spans multiplexers.
func (c *NavWsCmd) Run() error {
	out, err := exec.Command(herdrBin(), "workspace", "list").Output()
	if err != nil {
		return fmt.Errorf("herdr workspace list: %w", err)
	}
	var resp struct {
		Result struct {
			Workspaces []struct {
				ID       string `json:"workspace_id"`
				Label    string `json:"label"`
				Tab      string `json:"active_tab_id"`
				Worktree struct {
					Path string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("herdr workspace list: %w", err)
	}
	var best, bestLabel, bestTab string
	bestScore := 0
	for _, w := range resp.Result.Workspaces {
		for _, name := range []string{w.Label, filepath.Base(w.Worktree.Path)} {
			if name == "" || name == "." { // a workspace holding no worktree
				continue
			}
			if score, ok := fuzzyScore(name, c.Query); ok && (best == "" || score > bestScore) {
				best, bestLabel, bestTab, bestScore = w.ID, w.Label, w.Tab, score
			}
		}
	}
	if best == "" {
		return fmt.Errorf("%s: no workspace matches", c.Query)
	}
	if c.DryRun {
		// The tab the workspace is on: focusing a workspace lands you in it,
		// so it is what the pane half of nav would have printed too.
		where, err := herdrPlace(bestTab)
		if err != nil {
			return err
		}
		where.print()
		return nil
	}
	if err := herdrFocusWS(best); err != nil {
		return err
	}
	fmt.Println(best, bestLabel)
	return nil
}

// fuzzyScore matches pattern against s the way a fuzzy finder does: every
// character of the pattern present in order, not necessarily adjacent, case
// ignored. The score ranks matches against each other and means nothing on its
// own — higher is better, tight matches beating spread-out ones and early ones
// beating late. ok is false when the pattern isn't in s at all.
//
// ponytail: two passes, as fzf's simple matcher does — the whole pattern
// spelled out somewhere later on can still lose to a scattered run that starts
// earlier ("claude" against "c…l…a…u…d…e claude"), which a full dynamic
// program wouldn't. Bytes rather than runes, both sides lowercased: the same
// answer as runes for ascii, and a harmless one for the rest.
func fuzzyScore(s, pattern string) (int, bool) {
	hay, needle := strings.ToLower(s), strings.ToLower(pattern)
	if needle == "" {
		return 0, true
	}
	// Typed in one piece, which is how most patterns are typed: the tightest
	// run there can be, so no pass over the rest can beat it.
	if i := strings.Index(hay, needle); i >= 0 {
		return -8*(len(needle)-1) - i, true
	}
	// Forwards for where the pattern first finishes, then backwards from there
	// for the latest start that still fits it all in — the forward pass takes
	// the first character it sees and would otherwise call a run loose that
	// isn't.
	end, n := -1, 0
	for i := range len(hay) {
		if hay[i] == needle[n] {
			if n++; n == len(needle) {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return 0, false
	}
	start, n := end, len(needle)-1
	for i := end; i >= 0; i-- {
		if hay[i] == needle[n] {
			if n == 0 {
				start = i
				break
			}
			n--
		}
	}
	// Characters strewn over a whole command line are a coincidence, not a
	// match: a long enough line has almost any pattern in it somewhere, and
	// navigation that jumps to a coincidence on a typo is worse than
	// navigation that says it found nothing. Eight times the pattern still
	// leaves room for the initials of a name ("fld" for
	// frolicking-tumbling-duckling).
	if end-start > 8*len(needle) {
		return 0, false
	}
	return -8*(end-start) - start, true
}

// multiplexer is a terminal multiplexer ccwt can navigate: the variable it
// exports into every pane it spawns, how it is told to go to one, and how it
// says where one is. Panes are found through that variable rather than by
// asking the multiplexer, because everything running in a pane inherits it —
// which pane a process sits in is written on the process itself.
type multiplexer struct {
	env   string
	focus func(pane string) error
	where func(pane string) (place, error)
}

// place is where a pane is, in the three lines --dry-run prints: the workspace
// holding it, the tab holding it, and the address that gets you there — which
// is the tab's, since a tab is what both halves of nav land you on.
type place struct{ workspace, tab, address string }

func (p place) print() {
	fmt.Println(p.workspace)
	fmt.Println(p.tab)
	fmt.Println(p.address)
}

// ponytail: the first variable present wins. One multiplexer nested in another
// (herdr in tmux) leaves both set and the environment can't say which is the
// inner one; ask the multiplexers themselves if that ever matters.
var multiplexers = []multiplexer{
	{"HERDR_PANE_ID", herdrFocusPane, herdrWherePane},
	{"TMUX_PANE", func(pane string) error {
		// The window as well as the pane: a pane in another window is reached
		// only by selecting both. Then the client, which is the part that
		// can't be done from outside tmux — no client to switch, no failure.
		for _, args := range [][]string{{"select-window", "-t", pane}, {"select-pane", "-t", pane}} {
			if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("tmux %s: %s", strings.Join(args, " "), lastLine(out, err))
			}
		}
		_ = exec.Command("tmux", "switch-client", "-t", pane).Run()
		return nil
	}, func(pane string) (place, error) {
		// tmux's own three: the session is the workspace, the window is the
		// tab, and the pane addresses itself.
		out, err := exec.Command("tmux", "display-message", "-p", "-t", pane,
			"#{session_name}\n#{window_name}").Output()
		if err != nil {
			return place{}, fmt.Errorf("tmux display-message -t %s: %w", pane, err)
		}
		session, window, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return place{session, window, pane}, nil
	}},
}

// paneOf is the multiplexer pane an environment belongs to, and which
// multiplexer's pane it is. A nil multiplexer is an environment outside all of
// them.
func paneOf(env []string) (*multiplexer, string) {
	for i := range multiplexers {
		for _, kv := range env {
			if pane, ok := strings.CutPrefix(kv, multiplexers[i].env+"="); ok && pane != "" {
				return &multiplexers[i], pane
			}
		}
	}
	return nil, ""
}

// processEnv is another process's environment as KEY=VALUE strings, or nil
// when it can't be read: a process of another user, or one whose binary macOS
// protects — SIP hides the environment of everything under /bin and /usr/bin,
// shells included, so what answers there is the agent rather than the shell it
// was started from.
//
// ponytail: splitting the BSD ps output on spaces mangles values containing
// one, which costs nothing here — no pane id has a space in it.
func processEnv(pid int) []string {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil { // linux
		return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	}
	out, err := exec.Command("ps", "-Eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output() // bsd
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// herdrFocusPane goes to one pane. `agent focus` is the only thing herdr
// focuses a pane itself by, so a pane running no agent is reached through its
// tab instead — which lands on the tab's own focused pane, close enough when
// the tab is split and exact when it isn't.
func herdrFocusPane(pane string) error {
	if err := exec.Command(herdrBin(), "agent", "focus", pane).Run(); err == nil {
		return nil
	}
	tab, err := herdrPaneTab(pane)
	if err != nil {
		return err
	}
	if out, err := exec.Command(herdrBin(), "tab", "focus", tab).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr tab focus %s: %s", tab, lastLine(out, err))
	}
	return nil
}

// herdrPaneTab is the tab a pane sits in, which is what herdr can be told to
// go to.
func herdrPaneTab(pane string) (string, error) {
	out, err := exec.Command(herdrBin(), "pane", "get", pane).Output()
	if err != nil {
		return "", fmt.Errorf("herdr pane get %s: %w", pane, err)
	}
	var resp struct {
		Result struct {
			Pane struct {
				Tab string `json:"tab_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.Result.Pane.Tab == "" {
		return "", fmt.Errorf("herdr pane get %s: no tab in the answer", pane)
	}
	return resp.Result.Pane.Tab, nil
}

// herdrWherePane is a pane's place, for --dry-run: its tab's, the tab being
// where going there lands you.
func herdrWherePane(pane string) (place, error) {
	tab, err := herdrPaneTab(pane)
	if err != nil {
		return place{}, err
	}
	return herdrPlace(tab)
}

// herdrPlace is a tab's place: the name of the workspace it is in, its own
// name, and its id — the address `herdr tab focus` takes. Names, not ids, are
// what a dry run is read for; the id is there to be pasted into herdr.
func herdrPlace(tab string) (place, error) {
	var tabResp struct {
		Result struct {
			Tab struct {
				Label     string `json:"label"`
				Workspace string `json:"workspace_id"`
			} `json:"tab"`
		} `json:"result"`
	}
	out, err := exec.Command(herdrBin(), "tab", "get", tab).Output()
	if err != nil {
		return place{}, fmt.Errorf("herdr tab get %s: %w", tab, err)
	}
	if err := json.Unmarshal(out, &tabResp); err != nil {
		return place{}, fmt.Errorf("herdr tab get %s: %w", tab, err)
	}
	var wsResp struct {
		Result struct {
			Workspace struct {
				Label string `json:"label"`
			} `json:"workspace"`
		} `json:"result"`
	}
	out, err = exec.Command(herdrBin(), "workspace", "get", tabResp.Result.Tab.Workspace).Output()
	if err != nil {
		return place{}, fmt.Errorf("herdr workspace get %s: %w", tabResp.Result.Tab.Workspace, err)
	}
	if err := json.Unmarshal(out, &wsResp); err != nil {
		return place{}, fmt.Errorf("herdr workspace get %s: %w", tabResp.Result.Tab.Workspace, err)
	}
	return place{wsResp.Result.Workspace.Label, tabResp.Result.Tab.Label, tab}, nil
}

var cli struct {
	NewWorktreeName   NewWorktreeNameCmd   `cmd:"" name:"new-worktree-name" help:"Generate a Claude Code-style worktree name (adjective-verb-noun)."`
	NewWorktreeBranch NewWorktreeBranchCmd `cmd:"" name:"new" help:"Create a new worktree under .claude/worktrees/<name> on a new branch worktree-<name> (branch_prefix in the config file renames the \"worktree-\" part), and print <name>."`
	Cd                CdCmd                `cmd:"" name:"cd" help:"cd into an existing worktree under .claude/worktrees/<name> (errors if it doesn't exist). Use \"..\" for the enclosing repo root, or \"-\" for the previous directory."`
	List              ListCmd              `cmd:"" name:"list" aliases:"ls" help:"List Claude Code worktrees with branch, age, running-session, and what each one is about: the last Claude Code session there, or its last commit."`
	Tui               TuiCmd               `cmd:"" name:"tui" default:"withargs" help:"Show the worktree list full-screen, refreshing as things change. q quits, p runs git pull, arrows select a worktree, r removes it, P shows what is running in each worktree instead (space goes to a process's pane, esc goes back). Under herdr, x creates a worktree and opens it, and space (or a click) opens the selected one."`
	Remove            RemoveCmd            `cmd:"" name:"remove" help:"Delete a worktree under .claude/worktrees/<name> and its branch (merged, clean and no agent working in it; -D to remove anyway, --keep-branch to remove only the worktree). Under herdr its workspace is closed too, after asking on a terminal (-y skips the question). Use \".\" for the current worktree; removing it cds to the repo root."`
	Done              DoneCmd              `cmd:"" name:"done" help:"Finish with the worktree you're in: remove it and its branch (same checks and flags as \"remove .\"), then under herdr close the workspace you're sitting in."`
	Lock              LockCmd              `cmd:"" name:"lock" help:"Lock a worktree the way \"ccwt new\" does, so \"git worktree prune\" can't reclaim it while its directory is unavailable. Use \".\" for the worktree you're currently in."`
	Ps                PsCmd                `cmd:"" name:"ps" help:"Show what is running in each worktree, as a process tree per worktree: the shell you started there and, by default, what it is running right now. --depth goes further down, -g spans every configured project."`
	Worklog           WorklogCmd           `cmd:"" name:"worklog" help:"Show the worktrees that have been removed, newest first, with what each one was about — what you were working on lately, after the worktree itself is gone."`
	Gc                GcCmd                `cmd:"" name:"gc" help:"Remove every worktree whose branch is already merged, with nothing uncommitted, no agent working in it and no Claude Code session running in it, branches included — except the one you're in. Prints what it found and asks first, unless -y."`
	Nav               NavCmd               `cmd:"" name:"nav" help:"Go to where something is: \"nav ps <pattern>\" to the pane a process is running in, \"nav ws <pattern>\" to a workspace, by its title or by the name of the worktree it has open. Both patterns are fuzzy."`
	RepoRoot          RepoRootCmd          `cmd:"" name:"repo-root" help:"Print the root directory of the current git repository."`
	DotDot            DotDotCmd            `cmd:"" name:".." help:"Print the enclosing repo root, stripping any .claude/worktrees/<name> suffix (shorthand for repo-root --root-worktree)."`
	Init              InitCmd              `cmd:"" name:"init" help:"Emit a shell integration snippet to source from your rc file (e.g. source <(ccwt init zsh), or for fish: ccwt init fish | source)."`
	Config            ConfigCmd            `cmd:"" name:"config" help:"View or edit the config file listing the projects that -g spans, creating an empty one if there isn't any."`

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
