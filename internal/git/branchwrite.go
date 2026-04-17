package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WriteScope is the working directory for a single branch-scoped write.
// When the scope was created with a non-empty branch, RepoDir points at a
// private worktree and Cleanup must be called to remove it. When the scope
// is "unbranched" (empty branch), RepoDir is the main repo and Cleanup is
// a no-op.
type WriteScope struct {
	RepoDir string
	Branch  string
	cleanup func()
}

// Cleanup releases the worktree (no-op when using the main repo).
// Safe to call multiple times.
func (s *WriteScope) Cleanup() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
	}
}

// TrailerOrder is the fixed order used when appending trailers to commit
// messages. Preserves the pre-extraction ordering across artifact.Service
// and workflow.Service; extra keys are silently skipped.
var TrailerOrder = []string{"Trace-ID", "Actor-ID", "Run-ID", "Operation", "Workflow-Bypass"}

// EnterBranch prepares a WriteScope for a write operation.
//
// When branch is empty, the scope points at repoDir with a no-op Cleanup.
// Otherwise a private parent dir is allocated and `git worktree add` is run
// inside it; the scope's Cleanup removes both the worktree and its parent.
//
// validator, if non-nil, is called on branch before the worktree is added.
// This lets callers enforce per-package ref-name rules without this package
// taking a dependency on them.
func EnterBranch(ctx context.Context, repoDir, branch string, validator func(string) error) (*WriteScope, error) {
	if branch == "" {
		return &WriteScope{RepoDir: repoDir, cleanup: func() {}}, nil
	}
	if validator != nil {
		if err := validator(branch); err != nil {
			return nil, err
		}
	}

	// Allocate a private parent directory (0700, unique name) and let git
	// create the worktree inside it. The previous pattern was
	// mkdtemp → Remove → "git worktree add" in /tmp — a TOCTOU race where
	// another local user could reinsert a symlink between the Remove and
	// the add. By keeping the parent and pointing git at a fresh child
	// name that never existed, there is no remove-then-recreate window.
	parent, err := os.MkdirTemp("", "spine-wt-parent-*")
	if err != nil {
		return nil, fmt.Errorf("create worktree parent dir: %w", err)
	}
	worktreeDir := filepath.Join(parent, "wt")

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", worktreeDir, branch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(parent)
		return nil, fmt.Errorf("add worktree for branch %s: %s", branch, strings.TrimSpace(string(out)))
	}

	return &WriteScope{
		RepoDir: worktreeDir,
		Branch:  branch,
		cleanup: func() {
			// Cleanup uses a fresh bounded context, not the request ctx —
			// if the request was cancelled (client disconnect, timeout)
			// the worktree must still be removed or we leak it on disk.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			rmCmd := exec.CommandContext(cleanupCtx, "git", "worktree", "remove", "--force", worktreeDir)
			rmCmd.Dir = repoDir
			_, _ = rmCmd.CombinedOutput()
			os.RemoveAll(parent)
		},
	}, nil
}

// StageAndCommit stages path under scope.RepoDir and creates a scoped commit
// (only this file is committed). opts.Trailers are appended to
// opts.Message in TrailerOrder. On commit failure the path is unstaged so the
// index stays clean.
func StageAndCommit(ctx context.Context, scope *WriteScope, path string, opts CommitOpts) (CommitResult, error) {
	msg := opts.Message
	if len(opts.Trailers) > 0 {
		msg += "\n"
		for _, key := range TrailerOrder {
			if val, ok := opts.Trailers[key]; ok {
				msg += "\n" + key + ": " + val
			}
		}
	}

	if err := stageFile(ctx, scope.RepoDir, path); err != nil {
		return CommitResult{}, err
	}

	sha, err := commitFile(ctx, scope.RepoDir, path, msg, opts.Author)
	if err != nil {
		_ = UnstageFile(ctx, scope.RepoDir, path)
		return CommitResult{}, err
	}
	return CommitResult{SHA: sha}, nil
}

// UnstageFile removes path from the index of repoDir (git reset HEAD -- path).
// Used directly by service rollback paths when the file write succeeds but a
// later validation step fails before StageAndCommit is called.
func UnstageFile(ctx context.Context, repoDir, path string) error {
	cmd := exec.CommandContext(ctx, "git", "reset", "HEAD", "--", path)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// CurrentBranch returns the currently-checked-out branch name for repoDir.
// Used by auto-push helpers to know which branch to push.
func CurrentBranch(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func stageFile(ctx context.Context, repoDir, path string) error {
	cmd := exec.CommandContext(ctx, "git", "add", "--", path)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

func commitFile(ctx context.Context, repoDir, path, message string, author Author) (string, error) {
	args := []string{"commit", "-m", message}
	if author.Name != "" && author.Email != "" {
		args = append(args, "--author", fmt.Sprintf("%s <%s>", author.Name, author.Email))
	}
	args = append(args, "--", path)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit %s: %s", path, strings.TrimSpace(string(out)))
	}

	shaCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	shaCmd.Dir = repoDir
	shaOut, err := shaCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %s", strings.TrimSpace(string(shaOut)))
	}
	return strings.TrimSpace(string(shaOut)), nil
}
