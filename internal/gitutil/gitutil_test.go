package gitutil

import "testing"

func TestClaudeWorktreeRepoRoot(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantRoot string
		wantOK   bool
	}{
		{"worktree root", "/repo/.claude/worktrees/foo-bar", "/repo", true},
		{"worktree subdir", "/repo/.claude/worktrees/foo-bar/src/pkg", "/repo", true},
		{"nested repo path", "/Users/me/w/proj/.claude/worktrees/calm-cat", "/Users/me/w/proj", true},
		{"not a worktree", "/repo/src/pkg", "", false},
		{"plain repo root", "/repo", "", false},
		{"claude dir but not worktrees", "/repo/.claude/hooks", "", false},
		{"worktrees with no name", "/repo/.claude/worktrees", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRoot, gotOK := ClaudeWorktreeRepoRoot(tt.path)
			if gotRoot != tt.wantRoot || gotOK != tt.wantOK {
				t.Errorf("ClaudeWorktreeRepoRoot(%q) = (%q, %v), want (%q, %v)",
					tt.path, gotRoot, gotOK, tt.wantRoot, tt.wantOK)
			}
		})
	}
}
