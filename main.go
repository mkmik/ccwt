package main

import (
	"fmt"
	"os"
	"path/filepath"

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

	if err := gitutil.AddWorktree(filepath.Join(parent, name), "worktree-"+name); err != nil {
		return err
	}

	fmt.Println(name)
	return nil
}

var cli struct {
	NewWorktreeName   NewWorktreeNameCmd   `cmd:"" name:"new-worktree-name" help:"Generate a Claude Code-style worktree name (adjective-verb-noun)."`
	NewWorktreeBranch NewWorktreeBranchCmd `cmd:"" name:"new-worktree-branch" help:"Create a new worktree under .claude/worktrees/<name> on a new branch worktree-<name>, and print <name>."`
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
