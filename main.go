package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"

	"github.com/mmikulicic/ccwt/internal/gitutil"
	"github.com/mmikulicic/ccwt/internal/namegen"
)

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

type NewWorktreeBranchCmd struct{}

func (c *NewWorktreeBranchCmd) Run() error {
	name := namegen.Generate()

	root, err := gitutil.RepoRoot(true)
	if err != nil {
		return err
	}

	parent := filepath.Join(root, ".claude", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}

	worktreePath := filepath.Join(parent, name)
	if err := gitutil.AddWorktree(worktreePath, "worktree-"+name); err != nil {
		return err
	}

	emitOSC7(worktreePath)
	fmt.Println(name)
	return nil
}

// emitOSC7 writes an OSC 7 escape sequence to stderr telling the terminal
// (iTerm2, Ghostty, WezTerm, …) that the current working directory is now
// `path`. Emitted only when stderr is a TTY so redirecting stderr to a file
// won't fill it with escape codes. Format: ESC ] 7 ; file://<host><path> ST
// where ST is ESC \ . The path is URL-encoded via net/url.
func emitOSC7(path string) {
	fi, err := os.Stderr.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	host, _ := os.Hostname()
	u := url.URL{Scheme: "file", Host: host, Path: path}
	fmt.Fprintf(os.Stderr, "\x1b]7;%s\x1b\\", u.String())
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
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].sortTime.After(rows[j].sortTime)
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

var cli struct {
	NewWorktreeName   NewWorktreeNameCmd   `cmd:"" name:"new-worktree-name" help:"Generate a Claude Code-style worktree name (adjective-verb-noun)."`
	NewWorktreeBranch NewWorktreeBranchCmd `cmd:"" name:"new" help:"Create a new worktree under .claude/worktrees/<name> on a new branch worktree-<name>, and print <name>."`
	List              ListCmd              `cmd:"" name:"list" help:"List Claude Code worktrees with branch, age, running-session, and last commit."`
	RepoRoot          RepoRootCmd          `cmd:"" name:"repo-root" help:"Print the root directory of the current git repository."`
}

func main() {
	ctx := kong.Parse(&cli,
		kong.Name("ccwt"),
		kong.Description("Claude Code worktree helper."),
		kong.UsageOnError(),
	)
	ctx.FatalIfErrorf(ctx.Run())
}
