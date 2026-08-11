package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is ccwt's config file. It exists so `list -g` and `tui -g` know which
// repos to span; the projects are named here rather than discovered so that
// what shows up in a cross-project view is a decision, not whatever happens to
// be on disk.
type Config struct {
	Projects []Project `toml:"projects"`
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
