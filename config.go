package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is ccwt's config file. It exists so `list -g` and `tui -g` know which
// repos to span; the projects are named here rather than discovered so that
// what shows up in a cross-project view is a decision, not whatever happens to
// be on disk.
type Config struct {
	Projects []Project `toml:"projects"`
	// BranchPrefix goes in front of a worktree's name to make its branch.
	// Repos that want their branches namespaced ("mkm/") set it here.
	BranchPrefix string `toml:"branch_prefix"`
	// Columns are the table columns `list` and `tui` show, in the order they
	// are named here. Unset (or empty) means all of them.
	Columns []string `toml:"columns"`
	// TaskCommand is what a queued prompt is handed to when its worktree is
	// made: the new pane runs `<task_command> "<the prompt>"`.
	TaskCommand string `toml:"task_command"`
	// Sort is the order `list` and `tui` put the worktrees in; see sortOrders.
	Sort string `toml:"sort"`
	// Forges maps the host of a remote to the cli that knows about its reviews
	// — "gh" or "glab" — for the tui's `g` key. Only a host whose name doesn't
	// say which it is needs a line here, a self-hosted GitLab most of all:
	//
	//	[forges]
	//	"code.example.com" = "glab"
	Forges map[string]string `toml:"forges"`
}

// sortOrders are the orders the worktree list can be in, the first being the
// default. Freshness is the younger of the two things that say something
// happened in a worktree — its last commit, and the last line written to its
// newest session transcript — which is the question a list you are picking your
// next thing to look at off is really asking: an agent that has been working
// for an hour without committing is not stale, and a worktree whose branch was
// last touched in March is, whatever else git has to say about it. Commit is
// git's answer on its own, which is what the list was before there was a
// choice.
const (
	sortFreshness = "freshness"
	sortCommit    = "commit"
)

var sortOrders = []string{sortFreshness, sortCommit}

// sortOrder resolves how to sort: the flag if there is one, else the config,
// else freshness. An unknown name is an error rather than a silent fallback,
// the same way an unknown column is — and it says where it read it, since a
// typo in a hand-written config is otherwise invisible.
func sortOrder(flag string) (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	order := cmp.Or(flag, cfg.Sort, sortOrders[0])
	if !slices.Contains(sortOrders, order) {
		where := "--sort"
		if flag == "" {
			where = configPath() + ": `sort`"
		}
		return "", fmt.Errorf("%s: unknown sort order %q (have %s)", where, order, strings.Join(sortOrders, ", "))
	}
	return order, nil
}

// defaultTaskCommand is what runs a queued prompt when the config says nothing.
// Claude Code, since these are Claude Code's worktrees.
const defaultTaskCommand = "claude"

// taskCommand is the argv a queued prompt is appended to. Split on spaces so
// that the people who want flags ("claude --permission-mode plan") get theirs
// through, the same way $EDITOR is read below.
func taskCommand() ([]string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return strings.Fields(cmp.Or(cfg.TaskCommand, defaultTaskCommand)), nil
}

// defaultBranchPrefix is what a branch is called when the config says nothing:
// worktree-<name>, which is what every ccwt worktree was called before the
// prefix was configurable.
const defaultBranchPrefix = "worktree-"

// branchName is the branch a worktree called name is checked out on. It reads
// the config every time rather than caching: `new` and `remove` each run it
// once per process, and a stale prefix would strand branches under the old one.
func branchName(name string) (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	return cmp.Or(cfg.BranchPrefix, defaultBranchPrefix) + name, nil
}

// Project is one repository the cross-project views cover: the main checkout,
// the one the .claude/worktrees/ live under.
type Project struct {
	Path string `toml:"path"` // may start with ~/
}

// configPath is $XDG_CONFIG_HOME/ccwt/config.toml, falling back to the
// ~/.config default XDG defines when the variable is unset. Not os.UserConfigDir:
// on macOS that answers ~/Library/Application Support, which is not where
// anyone keeps the dotfiles of a command line tool.
func configPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "ccwt", "config.toml")
}

// loadConfig reads the config file. A missing file is not an error — it is
// simply a config with no projects in it, which every caller can report better
// than a bare ENOENT.
func loadConfig() (Config, error) {
	var cfg Config
	path := configPath()
	if path == "" {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// projectRoots returns the repositories a listing should cover: the configured
// projects when global is set, and nil — meaning "the repo we're standing in" —
// otherwise.
func projectRoots(global bool) ([]string, error) {
	if !global {
		return nil, nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if len(cfg.Projects) == 0 {
		return nil, fmt.Errorf("no projects configured in %s:\n\n[[projects]]\npath = \"~/src/some-repo\"\n", configPath())
	}
	roots := make([]string, len(cfg.Projects))
	for i, p := range cfg.Projects {
		roots[i] = expandHome(p.Path)
	}
	return roots, nil
}

type ConfigCmd struct {
	View ConfigViewCmd `cmd:"" help:"Print the config file."`
	Edit ConfigEditCmd `cmd:"" help:"Open the config file in $EDITOR (vi if unset)."`
}

type ConfigViewCmd struct{}

type ConfigEditCmd struct{}

// ensureConfig returns the config path, creating an empty file (and its
// directory) when there isn't one yet, so both subcommands have something to
// show or edit rather than an ENOENT.
func ensureConfig() (string, error) {
	path := configPath()
	if path == "" {
		return "", errors.New("cannot locate a config directory: set XDG_CONFIG_HOME")
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if f != nil {
		f.Close()
	}
	return path, nil
}

func (c *ConfigViewCmd) Run() error {
	path, err := ensureConfig()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	os.Stdout.Write(b)
	return nil
}

func (c *ConfigEditCmd) Run() error {
	path, err := ensureConfig()
	if err != nil {
		return err
	}
	// $EDITOR is split on spaces so that the "code -w" and "emacsclient -nw"
	// people get their flags through; vi is the fallback POSIX guarantees.
	argv := strings.Fields(cmp.Or(os.Getenv("EDITOR"), "vi"))
	cmd := exec.Command(argv[0], append(argv[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// expandHome expands a leading ~/ the way a shell would, since a config file is
// hand-written and nobody wants to spell out their home directory. ponytail: no
// ~user support — that resolves other people's home directories, which is not
// what anyone means in their own config.
func expandHome(path string) string {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, rest)
}

// shortenHome writes a path the way a config file would, which is the way it
// fits on a line: expandHome the other way round.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	rest, ok := strings.CutPrefix(path, home+string(filepath.Separator))
	if !ok {
		return path
	}
	return "~/" + rest
}
