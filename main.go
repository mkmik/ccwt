package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
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
	ForceCreate bool   `help:"Create a new worktree even when cwd is already inside one (otherwise the enclosing worktree's name is returned instead)."`
}

func (c *NewWorktreeBranchCmd) Run() error {
	if c.Name == "" && !c.ForceCreate {
		path, name, err := gitutil.CurrentClaudeWorktree()
		if err != nil {
			return err
		}
		if name != "" {
			emitOSC7(path)
			emitCdRequest(path)
			fmt.Println(name)
			return nil
		}
	}

	name := c.Name
	if name == "" {
		name = namegen.Generate()
	}

	root, err := gitutil.RepoRoot(true)
	if err != nil {
		return err
	}

	parent := filepath.Join(root, ".claude", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}

	worktreePath := filepath.Join(parent, name)

	_, statErr := os.Stat(worktreePath)
	switch {
	case statErr == nil:
		// Worktree directory already exists; reuse.
	case errors.Is(statErr, os.ErrNotExist):
		if err := gitutil.AddWorktree(worktreePath, "worktree-"+name); err != nil {
			return err
		}
	default:
		return statErr
	}

	emitOSC7(worktreePath)
	emitCdRequest(worktreePath)
	fmt.Println(name)
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
	Name  string `arg:"" help:"Worktree name to remove."`
	Force bool   `short:"D" help:"Force-delete the branch even when it is not merged."`
}

func (c *RemoveCmd) Run() error {
	root, err := gitutil.RepoRoot(true)
	if err != nil {
		return err
	}

	worktreePath := filepath.Join(root, ".claude", "worktrees", c.Name)
	branch := "worktree-" + c.Name

	cwdTop, _ := gitutil.RepoRoot(false)
	if cwdTop == worktreePath {
		return fmt.Errorf("refusing to remove %s: current directory is inside it (cd elsewhere first)", c.Name)
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

	return gitutil.DeleteBranch(branch, c.Force)
}

type ListCmd struct{}

func (c *ListCmd) Run() error {
	root, err := gitutil.RepoRoot(true)
	if err != nil {
		return err
	}
	wts, err := gitutil.ListWorktrees()
	if err != nil {
		return err
	}
	claudeDir := filepath.Join(root, ".claude", "worktrees") + string(filepath.Separator)
	claudeCwdSet := claudeCwds()

	type row struct {
		name, branch, age, claude, subject string
		sortTime                           time.Time
	}
	var rows []row
	for _, wt := range wts {
		if !strings.HasPrefix(wt.Path, claudeDir) {
			continue
		}
		r := row{
			name:   filepath.Base(wt.Path),
			branch: wt.Branch,
			claude: "no",
		}
		if r.branch == "" {
			r.branch = "(detached)"
		}
		if isClaudeActiveIn(wt.Path, claudeCwdSet) {
			r.claude = "yes"
		}
		if commit, err := gitutil.LastCommit(wt.Path); err == nil {
			r.age = humanAge(time.Since(commit.Time))
			r.subject = truncate(commit.Subject, 60)
			r.sortTime = commit.Time
		} else {
			r.age = "?"
			r.subject = "(no commits)"
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b row) int {
		return b.sortTime.Compare(a.sortTime)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tBRANCH\tAGE\tCLAUDE\tLAST COMMIT")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.name, r.branch, r.age, r.claude, r.subject)
	}
	return w.Flush()
}

// claudeCwds returns the set of working directories of currently-running
// Claude Code processes. We identify a process as "claude" if any of its
// mapped executable (txt) paths look like the claude binary — argv[0] is
// unreliable because the official installer leaves the process showing up
// as its version number (e.g. "2.1.139") rather than "claude".
//
// On any lsof failure we return an empty set rather than erroring out:
// the worst case is that the CLAUDE column reads "no" everywhere.
func claudeCwds() map[string]bool {
	cwds := map[string]bool{}
	out, err := exec.Command("lsof", "-d", "cwd,txt", "-Fcn").Output()
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
	List              ListCmd              `cmd:"" name:"list" help:"List Claude Code worktrees with branch, age, running-session, and last commit."`
	Remove            RemoveCmd            `cmd:"" name:"remove" help:"Delete a worktree under .claude/worktrees/<name> and its branch (merged-only; -D to force unmerged)."`
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
