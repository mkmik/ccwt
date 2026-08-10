package main

import (
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
	path, err := gitutil.RepoRoot(c.RootWorktree)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

type DotDotCmd struct{}

func (c *DotDotCmd) Run() error {
	path, err := gitutil.RepoRoot(true)
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
	path, name, err := c.create()
	if err != nil {
		return err
	}
	emitOSC7(path)
	emitCdRequest(path)
	fmt.Println(c.out(path, name))
	return nil
}

// create makes (or reuses) the worktree and reports its path and name, without
// the terminal side effects Run adds — which is what callers that aren't a
// command line (the tui) want.
func (c *NewWorktreeBranchCmd) create() (string, string, error) {
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

	root, err := gitutil.RepoRoot(true)
	if err != nil {
		return "", "", err
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
			addErr = gitutil.AddWorktreeOnBranch(worktreePath, c.Switch)
		} else {
			addErr = gitutil.AddWorktree(worktreePath, "worktree-"+name)
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

	root, err := gitutil.RepoRoot(true)
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
	root, err := gitutil.RepoRoot(true)
	if err != nil {
		return err
	}

	name := c.Name
	if name == "." {
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
	if !c.KeepBranch && !c.Force && gitutil.BranchExists(branch) && !gitutil.MergedBranches()[branch] {
		return fmt.Errorf("%s is not merged: re-run with -D to delete it anyway, or --keep-branch to remove only the worktree", branch)
	}

	// Removing the worktree we're standing in leaves the process in a deleted
	// directory, which makes every subsequent git fork fail — so hop out to the
	// repo root first, and cd the shell there afterwards (same as `ccwt cd ..`).
	cwdTop, _ := gitutil.RepoRoot(false)
	inside := cwdTop == worktreePath
	if inside {
		if err := os.Chdir(root); err != nil {
			return err
		}
	}

	if _, err := os.Stat(worktreePath); err == nil {
		if err := gitutil.RemoveWorktree(worktreePath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := gitutil.PruneWorktrees(); err != nil {
		return err
	}

	if !c.KeepBranch {
		// Force-delete: the check above is the safety valve, and it is the only
		// one that agrees with the "✓" in `ccwt list`.
		if err := gitutil.DeleteBranch(branch); err != nil {
			return err
		}
	}

	if inside {
		return (&DotDotCmd{}).Run()
	}
	return nil
}

type ListCmd struct{}

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
	_, err := renderList(os.Stdout, stdoutIsTTY())
	return err
}

// renderList writes the worktree table to w and returns the worktree names in
// the order their rows were printed, which is what lets `ccwt tui` map a
// screen line back to a worktree. tty selects the human-reader decorations
// (markers, ✓); the tui always asks for them, plain `list` only when stdout is
// a terminal.
func renderList(out io.Writer, tty bool) ([]string, error) {
	root, err := gitutil.RepoRoot(true)
	if err != nil {
		return nil, err
	}
	wts, err := gitutil.ListWorktrees()
	if err != nil {
		return nil, err
	}
	// The worktree we're running in gets a "* " on its name, one on its last
	// commit when it has uncommitted changes, and a "✓ " on its branch when
	// that branch is already merged — but only for human readers: piped
	// output stays parseable. Unmarked cells get the same two-space gutter
	// so the columns line up.
	claudeDir := filepath.Join(root, ".claude", "worktrees") + string(filepath.Separator)
	claudeWts := slices.DeleteFunc(wts, func(wt gitutil.Worktree) bool {
		return !strings.HasPrefix(wt.Path, claudeDir)
	})

	type row struct {
		name, branch, age, claude, subject string
		plain                              string // name without the tty marker
		dirty                              bool
		sortTime                           time.Time
	}

	// Every lookup below shells out to git or lsof, and none of them depend on
	// each other, so they all go out at once: serially they are essentially the
	// whole runtime of the command (~500ms across 21 worktrees on a large repo),
	// in parallel it costs about as much as the slowest single worktree.
	// Each goroutine owns one rows[i], so no locking.
	var (
		wg           sync.WaitGroup
		rows         = make([]row, len(claudeWts))
		cur          string
		merged       map[string]bool
		claudeCwdSet map[string]bool
	)
	wg.Go(func() { claudeCwdSet = claudeCwds() })
	if tty {
		wg.Go(func() { cur, _, _ = gitutil.CurrentClaudeWorktree() })
		wg.Go(func() { merged = gitutil.MergedBranches() })
	}
	for i, wt := range claudeWts {
		wg.Go(func() {
			r := &rows[i]
			r.plain = filepath.Base(wt.Path)
			r.name = r.plain
			r.branch = wt.Branch
			if r.branch == "" {
				r.branch = "(detached)"
			}
			if commit, err := gitutil.LastCommit(wt.Path); err == nil {
				r.age = humanAge(time.Since(commit.Time))
				r.subject = truncate(commit.Subject, 60)
				r.sortTime = commit.Time
			} else {
				r.age = "?"
				r.subject = "(no commits)"
			}
			if tty {
				r.dirty = gitutil.Dirty(wt.Path)
			}
		})
	}
	wg.Wait()

	// The rest is map lookups against results the fan-out had to finish first.
	for i, wt := range claudeWts {
		r := &rows[i]
		r.claude = "no"
		if isClaudeActiveIn(wt.Path, claudeCwdSet) {
			r.claude = "yes"
		}
		if tty {
			r.name = marker(wt.Path == cur, "*") + r.name
			r.branch = marker(merged[wt.Branch], "✓") + r.branch
			r.subject = marker(r.dirty, "*") + r.subject
		}
	}
	// Stable: worktrees sharing a commit timestamp keep `git worktree list`
	// order rather than shuffling between runs.
	slices.SortStableFunc(rows, func(a, b row) int {
		return b.sortTime.Compare(a.sortTime)
	})

	gutter := ""
	if tty {
		gutter = marker(false, "")
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, gutter+"NAME\t"+gutter+"BRANCH\tAGE\tCLAUDE\t"+gutter+"LAST COMMIT")
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.plain
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.name, r.branch, r.age, r.claude, r.subject)
	}
	return names, w.Flush()
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

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-1] + "…"
}

var cli struct {
	NewWorktreeName   NewWorktreeNameCmd   `cmd:"" name:"new-worktree-name" help:"Generate a Claude Code-style worktree name (adjective-verb-noun)."`
	NewWorktreeBranch NewWorktreeBranchCmd `cmd:"" name:"new" help:"Create a new worktree under .claude/worktrees/<name> on a new branch worktree-<name>, and print <name>."`
	Cd                CdCmd                `cmd:"" name:"cd" help:"cd into an existing worktree under .claude/worktrees/<name> (errors if it doesn't exist). Use \"..\" for the enclosing repo root, or \"-\" for the previous directory."`
	List              ListCmd              `cmd:"" name:"list" aliases:"ls" help:"List Claude Code worktrees with branch, age, running-session, and last commit."`
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
