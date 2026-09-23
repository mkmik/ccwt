package main

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// askTrust is the question `ccwt ws` asks before any other, when the agents it
// starts are Claude Code and Claude Code has not been told to trust the
// directory they would start in: may it?
//
// Every agent the ws view starts goes in a tab of its own that nobody is
// looking at, and in a directory Claude Code doesn't trust yet each of them
// stops at its own trust dialog — the seed prompt typed and sent, and the agent
// sitting on it in a tab you have to go and find. So the tui asks once, up
// front, and files the answer where that dialog would have. It goes up over
// whatever else the tui opens on — the seed prompt, when `c` has just made the
// workspace — which is what makes it the first question rather than one more.
func (u *ui) askTrust() {
	argv, err := taskCommand()
	if err != nil || len(argv) == 0 || filepath.Base(argv[0]) != "claude" {
		return
	}
	if cwd, err := os.Getwd(); err == nil {
		u.trust = claudeUntrusted(cwd)
	}
}

// answerTrust feeds one keystroke to the trust question: `y` files the answer
// and takes the question down, `n` (or esc) takes it down and leaves Claude Code
// to ask for itself. A write that fails leaves the question up, with the reason
// in the bar, to try again or to say no to.
func (u *ui) answerTrust(k string) {
	switch k {
	case "y":
		if err := trustClaude(u.trust); err != nil {
			u.msg = "trust failed: " + err.Error()
			return
		}
		u.trust, u.msg = "", "trusted "+shortenHome(u.trust)
	case "n", "\x1b", "\x03":
		u.trust = ""
	}
}

// trustPane is the question, in the same kind of window as the details pane.
// It names the directory the answer is filed under, which is the repository
// rather than the worktree the tui stands in: yes is an answer for all of it.
func trustPane(dir string, cols, rows int) []string {
	pad, inner := paneWidth(cols)
	row := paneRow(pad, inner)
	var body []string
	for _, p := range []string{
		"",
		"Claude Code doesn't trust " + shortenHome(dir) + " yet, so every agent started here would stop to ask.",
		"",
		"Trust it? Claude Code will be able to read, edit and run the files in it.",
		"",
	} {
		for _, l := range wrap(p, max(inner-2, 1)) {
			body = append(body, row(" "+l))
		}
	}
	lines := paneBox(pad, inner, "trust", body)
	return append(make([]string, min(2, max(rows-len(lines), 0)/2)), lines...)
}

// claudeConfigPath is Claude Code's own config file, where its trust dialog
// files the answer: ~/.claude.json, or the same name in $CLAUDE_CONFIG_DIR.
func claudeConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(cmp.Or(os.Getenv("CLAUDE_CONFIG_DIR"), home), ".claude.json")
}

// claudeProject is what Claude Code files its trust of dir under: the main
// checkout of the repository dir is in, which every worktree of it shares, or
// dir itself outside git. top is the checkout dir is in — as far up as Claude
// Code goes looking for an answer filed under a directory of its own — and ""
// outside git, where it goes all the way up.
func claudeProject(dir string) (key, top string) {
	top = gitLine(dir, "rev-parse", "--show-toplevel")
	if top == "" {
		return dir, ""
	}
	// A checkout's common dir is its .git, whichever worktree asks. Anything
	// else — a bare repository's, a submodule's — has no main checkout to share.
	if common := gitLine(dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); filepath.Base(common) == ".git" {
		return filepath.Dir(common), top
	}
	return top, top
}

// claudeUntrusted is the directory Claude Code would ask to trust before an
// agent could start in dir, or "" when it wouldn't ask — or there is no telling,
// which comes to the same for the tui: no question.
//
// An answer counts where Claude Code's own check counts one: on the directory
// claudeProject files it under, or on dir or any directory above it up to the
// top of its checkout.
//
// Never the home directory: Claude Code keeps its answer about that one for the
// session only, since everything under it would inherit a written one, and ccwt
// doesn't write down what Claude Code won't.
func claudeUntrusted(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real // getcwd's answer, which is the one Claude Code goes by
	}
	key, top := claudeProject(dir)
	if home, _ := os.UserHomeDir(); key == home {
		return ""
	}
	b, err := os.ReadFile(claudeConfigPath())
	if err != nil {
		return ""
	}
	var cfg struct {
		Projects map[string]struct {
			Trusted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if json.Unmarshal(b, &cfg, claudeJSON) != nil || cfg.Projects[key].Trusted {
		return ""
	}
	for d := dir; ; d = filepath.Dir(d) {
		if cfg.Projects[d].Trusted {
			return ""
		}
		if d == top || d == filepath.Dir(d) {
			return key
		}
	}
}

// claudeJSON is how Claude Code's config is read and written back without
// disturbing what ccwt isn't there to change: numbers to the last digit, a lone
// surrogate that JSON.stringify escaped still escaped, and indented the way
// JSON.stringify indents it.
var claudeJSON = json.JoinOptions(
	jsontext.AllowInvalidUTF8(true),
	jsontext.PreserveRawStrings(true),
	jsontext.WithIndent("  "),
	json.Deterministic(true),
)

// claudeNewProject is the entry Claude Code starts a project it has never met
// with, which is what a project first met in ccwt's trust question gets too.
const claudeNewProject = `{"allowedTools":[],"mcpContextUris":[],"mcpServers":{},"enabledMcpjsonServers":[],"disabledMcpjsonServers":[],"hasClaudeMdExternalIncludesApproved":false,"hasClaudeMdExternalIncludesWarningShown":false}`

// trustClaude files yes for dir in Claude Code's config, the way its own dialog
// does: hasTrustDialogAccepted on the project's entry, the rest of the file as
// it was.
//
// The file is Claude Code's, and it is rewritten all the time — every session
// saves its counters there — so the change goes in under Claude Code's own
// lock, the directory proper-lockfile makes next to the file, and as a new file
// renamed over the old one. A session that saves after this reads the file
// back first, under the same lock, as it does whatever else was written there.
//
// ponytail: a lock left behind by a Claude Code that died holding it is waited
// on for two seconds and then reported, where proper-lockfile takes one over
// once it's ten seconds stale; the next Claude Code to save clears it.
func trustClaude(dir string) error {
	path := claudeConfigPath()
	lock := path + ".lock"
	for i := 0; ; i++ {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i == 40 {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer os.Remove(lock)

	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // through a symlinked dotfile, not over it
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg struct {
		Projects map[string]jsontext.Value `json:"projects"`
		Rest     jsontext.Value            `json:",embed"`
	}
	if err := json.Unmarshal(b, &cfg, claudeJSON); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	entry := cfg.Projects[dir]
	if entry == nil {
		entry = jsontext.Value(claudeNewProject)
	}
	var project struct {
		Trusted bool           `json:"hasTrustDialogAccepted"`
		Rest    jsontext.Value `json:",embed"`
	}
	if err := json.Unmarshal(entry, &project, claudeJSON); err != nil {
		return fmt.Errorf("%s: %s: %w", path, dir, err)
	}
	project.Trusted = true
	if cfg.Projects == nil {
		cfg.Projects = map[string]jsontext.Value{}
	}
	if cfg.Projects[dir], err = json.Marshal(project, claudeJSON); err != nil {
		return err
	}
	if b, err = json.Marshal(cfg, claudeJSON); err != nil {
		return err
	}

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".ccwt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // nothing left to remove once the rename is done
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
