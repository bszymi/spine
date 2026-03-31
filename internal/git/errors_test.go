package git_test

import (
	"testing"

	"github.com/bszymi/spine/internal/git"
)

func TestClassifyGitErrorCases(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		wantKind git.GitErrorKind
		wantMsg  string
	}{
		{
			name:     "lock contention",
			stderr:   "Unable to create '/repo/.git/index.lock': File exists",
			wantKind: git.ErrKindTransient,
			wantMsg:  "repository locked",
		},
		{
			name:     "dotlock file",
			stderr:   "fatal: Unable to create '/repo/.git/HEAD.lock': File exists",
			wantKind: git.ErrKindTransient,
			wantMsg:  "repository locked",
		},
		{
			name:     "merge conflict",
			stderr:   "CONFLICT (content): Merge conflict in governance/test.md",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "merge conflict",
		},
		{
			name:     "lowercase conflict",
			stderr:   "conflict: some conflict description",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "merge conflict",
		},
		{
			name:     "bad revision",
			stderr:   "fatal: bad revision 'nonexistent'",
			wantKind: git.ErrKindNotFound,
		},
		{
			name:     "unknown revision",
			stderr:   "fatal: unknown revision or path not in the working tree",
			wantKind: git.ErrKindNotFound,
		},
		{
			name:     "does not exist",
			stderr:   "fatal: path 'missing.md' does not exist in 'HEAD'",
			wantKind: git.ErrKindNotFound,
		},
		{
			name:     "not a git repo",
			stderr:   "fatal: not a git repository (or any of the parent directories): .git",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "not a git repository",
		},
		{
			name:     "connection refused",
			stderr:   "fatal: unable to access 'https://example.com/repo.git/': Connection refused",
			wantKind: git.ErrKindTransient,
			wantMsg:  "network error",
		},
		{
			name:     "could not resolve",
			stderr:   "fatal: unable to access: Could not resolve host: github.com",
			wantKind: git.ErrKindTransient,
			wantMsg:  "network error",
		},
		{
			name:     "timeout",
			stderr:   "fatal: unable to access: Connection timeout after 30s",
			wantKind: git.ErrKindTransient,
			wantMsg:  "network error",
		},
		{
			name:     "push rejected",
			stderr:   "! [rejected] main -> main (non-fast-forward)",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "push rejected",
		},
		{
			name:     "failed to push",
			stderr:   "error: failed to push some refs to 'origin'",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "push rejected",
		},
		{
			name:     "authentication failed",
			stderr:   "fatal: Authentication failed for 'https://github.com/repo.git/'",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "authentication failed",
		},
		{
			name:     "permission denied publickey",
			stderr:   "fatal: Permission denied (publickey)",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "authentication failed",
		},
		{
			name:     "local permission denied is not auth error",
			stderr:   "error: Permission denied: /some/path",
			wantKind: git.ErrKindPermanent,
			wantMsg:  "error: Permission denied: /some/path",
		},
		{
			name:     "unknown error",
			stderr:   "fatal: some completely unknown error message",
			wantKind: git.ErrKindPermanent,
		},
		{
			name:     "not found",
			stderr:   "pathspec 'missing' not found",
			wantKind: git.ErrKindNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := git.ClassifyGitErrorForTest(tt.stderr)
			if err.Kind != tt.wantKind {
				t.Errorf("expected kind %s, got %s", tt.wantKind, err.Kind)
			}
			if tt.wantMsg != "" && err.Message != tt.wantMsg {
				t.Errorf("expected message %q, got %q", tt.wantMsg, err.Message)
			}
		})
	}
}
