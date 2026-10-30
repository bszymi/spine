package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WriteScope is the working directory for a single branch-scoped write.
// When the scope was created with a non-empty branch, RepoDir points at a
// private worktree and Cleanup must be called to remove it. When the scope
// is "unbranched" (empty branch), RepoDir is the main repo and the scope
// holds that repository's working tree (INIT-023 TASK-012) until Cleanup,
// which must be called as well.
type WriteScope struct {
	RepoDir string
	Branch  string
	cleanup func()

	// In-place scopes only (empty branch): the tree's ticket when this
	// scope took the tree (nil when it re-entered a sequence the caller
	// already holds), whether the tree has a home branch (then the scope's
	// contexts are detached and bounded), and the bound.
	ticket   *treeTicket
	detached bool
	deadline time.Time
	mu       sync.Mutex
	cancels  []context.CancelFunc
}

// Cleanup releases the worktree, or for an in-place scope the working
// tree. Safe to call multiple times; call it before emitting events about
// the write, so no event is published while the tree is held.
func (s *WriteScope) Cleanup() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
	}
}

// Context returns the context every git call made inside the scope must
// use (INIT-023 TASK-012, design-check DC-5). For an in-place scope that
// took the working tree it carries the tree's ticket, so a locked
// CLIClient call made inside the scope re-enters instead of waiting for
// the scope itself until ctx ends; on a tree with a home branch it is
// also detached from ctx's cancellation and bounded by the scope's
// deadline (treeSequenceTimeout from EnterBranch), so a client disconnect
// cannot stop a staged write half-way. On any other tree ctx's
// cancellation is kept (embedders' per-request worktrees behave as
// before). For a worktree scope, or an in-place scope that re-entered a
// sequence, it is ctx itself. The derived contexts end at Cleanup.
func (s *WriteScope) Context(ctx context.Context) context.Context {
	if s == nil || s.ticket == nil {
		return ctx
	}
	if !s.detached {
		return withTicket(ctx, s.ticket)
	}
	c, cancel := context.WithDeadline(context.WithoutCancel(ctx), s.deadline) //nolint:gosec // G118: cancel is kept in s.cancels and called by Cleanup (endContexts)
	s.mu.Lock()
	s.cancels = append(s.cancels, cancel)
	s.mu.Unlock()
	return withTicket(c, s.ticket)
}

func (s *WriteScope) endContexts() {
	s.mu.Lock()
	cancels := s.cancels
	s.cancels = nil
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// TrailerOrder is the fixed order used when appending trailers to commit
// messages. Preserves the pre-extraction ordering across artifact.Service
// and workflow.Service; extra keys are silently skipped.
var TrailerOrder = []string{
	"Trace-ID",
	"Actor-ID",
	"Run-ID",
	"Operation",
	"Workflow-Bypass",
	"Branch-Protection-Override",
}

// EnterBranch prepares a WriteScope for a write operation.
//
// When branch is empty, the scope points at repoDir and holds repoDir's
// working tree (INIT-023 TASK-012, treelock.go) until Cleanup: the lock
// is taken (re-entrant when ctx carries it, waiting only until ctx ends,
// *TreeWaitError), and when this call took a tree with a home branch the
// tree is first restored to a clean checkout of it (a failure is the
// *TreeRestoreError and no scope is returned). An in-place write in
// standalone's shared tree therefore never commits onto a run branch a
// sequence has checked out, and its staged file is never swept into the
// engine's cascade commit. Use scope.Context(ctx) for the git calls made
// inside the scope. Otherwise a private parent dir is allocated and a worktree for the
// branch is added inside it; the scope's Cleanup removes both the
// worktree and its parent.
//
// validator, if non-nil, is called on branch before the worktree is added.
// This lets callers enforce per-package ref-name rules (and keep their
// typed errors) without this package taking a dependency on them. The
// branch is then always checked against the Branch class of the ref
// grammar (QualifyBranch), validator or not; a refusal is a
// *GitError{Kind: ErrKindPermanent} wrapping ErrInvalidRef.
//
// The worktree is attached in three steps (INIT-023 TASK-002, DC-5):
//
//  1. `git worktree add --detach --end-of-options <dir> <oid>`, with the
//     branch's object id from the exact lookup (show-ref --verify, no
//     DWIM). `worktree add <dir> <n>` looked <n> up by DWIM before
//     attaching, so a refs/<n> or refs/tags/<n> pointing at a blob made
//     it die ("invalid reference") although the branch exists, and with
//     the branch missing it detached onto a same-named tag.
//  2. `git switch --no-guess --end-of-options <n>` inside the worktree,
//     which uses the branch's own object and keeps git's "already
//     checked out / used by worktree" guard.
//  3. the post-condition `git symbolic-ref -q HEAD` == refs/heads/<n>.
//
// On any failure the worktree and its parent are removed and an error is
// returned; a missing branch is a *GitError{Kind: ErrKindNotFound}.
//
// It applies the process default checkout limits (SetDefaultCheckoutLimits;
// none unless the host set them): see EnterBranchWithLimits.
func EnterBranch(ctx context.Context, repoDir, branch string, validator func(string) error) (*WriteScope, error) {
	return EnterBranchWithLimits(ctx, repoDir, branch, validator, CheckoutLimits{})
}

// EnterBranchWithLimits is EnterBranch held to limits over the process
// default (CheckoutLimits.over: a limit set here replaces the default's,
// none is removed). INIT-024 TASK-003, review finding N-1:
//
//   - with a budget, the branch tip's tree is measured right after the
//     exact lookup, before the private parent directory is created; a
//     tree over the budget returns the ErrTreeTooLarge *GitError and
//     nothing is created or written (no temporary directory, no worktree
//     registration). An in-place scope (empty branch) measures the home
//     branch when its entry restore switches back to it;
//   - with a mutate deadline, the worktree is added `--no-checkout` and
//     filled by a `reset --hard` this process runs, so the command that
//     writes the tree is a direct child the deadline's SIGTERM reaches;
//     `worktree add`, the reset and `switch` all run under it.
//
// With neither (the zero limits and no process default) it is exactly
// EnterBranch before this change: no extra git process, the caller's
// context alone.
func EnterBranchWithLimits(ctx context.Context, repoDir, branch string, validator func(string) error, limits CheckoutLimits) (*WriteScope, error) {
	limits = limits.over(DefaultCheckoutLimits())
	if branch == "" {
		return enterInPlace(ctx, repoDir, limits)
	}
	if validator != nil {
		if err := validator(branch); err != nil {
			return nil, err
		}
	}
	short, _, err := checkBranch("worktree", "branch", branch)
	if err != nil {
		return nil, err
	}
	oid, ok, err := lookupBranch(ctx, repoDir, "worktree", short)
	if err != nil {
		return nil, fmt.Errorf("add worktree for branch %s: %w", branch, err)
	}
	if !ok {
		return nil, fmt.Errorf("add worktree for branch %s: %w", branch, branchNotFound("worktree", "branch", branch))
	}
	// The checkout budget, before anything exists on disk. The measure
	// uses the same exact object id the worktree is added at.
	if limits.Budget != nil {
		if err := limits.Budget.checkRevs(ctx, &CLIClient{repoPath: repoDir}, "worktree", oid); err != nil {
			return nil, fmt.Errorf("add worktree for branch %s: %w", branch, err)
		}
	}
	// The mutate deadline bounds the two commands that write the tree.
	parentCtx := ctx
	mctx, cancelDeadline := withMutateDeadline(ctx, limits.MutateTimeout)
	defer cancelDeadline()
	deadlineErr := func(err error) error {
		if mutateDeadlineHit(parentCtx, mctx, limits.MutateTimeout) {
			return fmt.Errorf("add worktree for branch %s: %w", branch, mutateTimeout("worktree", limits.MutateTimeout, err))
		}
		return nil
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

	// With a mutate deadline the checkout is split out of `worktree add`:
	// worktree add writes the tree in a CHILD git (`reset --hard`), which
	// the SIGTERM the deadline sends to worktree add never reaches — it
	// went on writing for as long as it liked (adversarial review). So
	// the worktree is added with --no-checkout and this process runs the
	// `reset --hard` that populates it, as a direct, deadlined child.
	// (The post-checkout hook worktree add would run is not run.) No
	// deadline: the single command, as before.
	addArgs := []string{"worktree", "add", "--detach", "--end-of-options", worktreeDir, oid}
	if limits.MutateTimeout > 0 {
		addArgs = []string{"worktree", "add", "--no-checkout", "--detach", "--end-of-options", worktreeDir, oid}
	}
	cmd := newGitCmd(mctx, addArgs...)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		// A worktree add stopped half-way has registered the worktree:
		// remove the registration as well as the directory.
		if dErr := deadlineErr(err); dErr != nil {
			removeAddedWorktree(repoDir, worktreeDir, parent)
			return nil, dErr
		}
		os.RemoveAll(parent)
		return nil, fmt.Errorf("add worktree for branch %s: %s", branch, strings.TrimSpace(string(out)))
	}

	// removeWorktree undoes the add on a failed attach.
	removeWorktree := func() { removeAddedWorktree(repoDir, worktreeDir, parent) }

	if limits.MutateTimeout > 0 {
		reset := newGitCmd(mctx, "reset", "--hard", "-q")
		reset.Dir = worktreeDir
		if out, err := reset.CombinedOutput(); err != nil {
			removeWorktree()
			if dErr := deadlineErr(err); dErr != nil {
				return nil, dErr
			}
			return nil, fmt.Errorf("add worktree for branch %s: %s", branch, strings.TrimSpace(string(out)))
		}
	}

	sw := newGitCmd(mctx, "switch", "--no-guess", "--end-of-options", short)
	sw.Dir = worktreeDir
	if out, err := sw.CombinedOutput(); err != nil {
		removeWorktree()
		if dErr := deadlineErr(err); dErr != nil {
			return nil, dErr
		}
		return nil, fmt.Errorf("add worktree for branch %s: %s", branch, strings.TrimSpace(string(out)))
	}

	// Post-condition: the worktree must be ON refs/heads/<short>, never
	// detached and never on another ref.
	sym := newGitCmd(ctx, "symbolic-ref", "-q", "HEAD")
	sym.Dir = worktreeDir
	if out, err := sym.Output(); err != nil || strings.TrimSpace(string(out)) != headsPrefix+short {
		removeWorktree()
		return nil, fmt.Errorf("add worktree for branch %s: worktree is not on %s%s", branch, headsPrefix, short)
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
			rmCmd := newGitCmd(cleanupCtx, "worktree", "remove", "--force", worktreeDir)
			rmCmd.Dir = repoDir
			_, _ = rmCmd.CombinedOutput()
			os.RemoveAll(parent)
		},
	}, nil
}

// removeAddedWorktree removes a worktree EnterBranch added (its
// registration and its directory) and the private parent. Like Cleanup it
// uses a fresh bounded context: a cancelled request must not leak the
// worktree registration.
func removeAddedWorktree(repoDir, worktreeDir, parent string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rmCmd := newGitCmd(cleanupCtx, "worktree", "remove", "--force", worktreeDir)
	rmCmd.Dir = repoDir
	_, _ = rmCmd.CombinedOutput()
	_ = os.RemoveAll(parent)
}

// enterInPlace is EnterBranch with an empty branch. The tree's client
// carries limits, so the entry restore's switch back to the home branch
// is measured and deadlined like any other.
func enterInPlace(ctx context.Context, repoDir string, limits CheckoutLimits) (*WriteScope, error) {
	h, err := acquireTree(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	scope := &WriteScope{RepoDir: repoDir, ticket: h.ticket}
	if !h.fresh {
		scope.cleanup = func() {}
		return scope, nil
	}
	c := &CLIClient{repoPath: repoDir, treeBudget: limits.Budget, mutateTimeout: limits.MutateTimeout}
	if home := homeBranchOf(repoDir); home != "" {
		if _, err := c.restoreHeld(h.ctx, home); err != nil {
			h.release()
			return nil, err
		}
		scope.detached = true
		scope.deadline = time.Now().Add(treeSequenceTimeout)
	} else if _, branch, err := c.settlePendingTakeBacks(h.ctx, "restore_tree"); err != nil {
		// Not restored, but never handed out over a take-back a failed
		// ResetBranch left undone (pendingTakeBack; none on an embedder's
		// tree that never calls ResetBranch).
		h.release()
		return nil, &TreeRestoreError{Branch: branch, Err: err}
	}
	var once sync.Once
	scope.cleanup = func() {
		once.Do(func() {
			scope.endContexts()
			h.release()
		})
	}
	return scope, nil
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
	cmd := newGitCmd(ctx, "reset", "HEAD", "--", path)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// TrackedInHEAD reports whether path (repo-relative, slash-separated, as
// staged) names an entry of the HEAD commit of the repository or worktree
// rooted at repoDir. The match is literal (GIT_LITERAL_PATHSPECS: no glob
// or pathspec magic); a directory entry counts as tracked.
//
// Rollback paths use it to decide whether a file left at a path whose
// create failed to commit may be deleted (INIT-023 TASK-004 review round
// 2), so every doubt is an error: an unborn HEAD or any other git failure
// returns a non-nil error, never "untracked", and the caller keeps the file.
func TrackedInHEAD(ctx context.Context, repoDir, path string) (bool, error) {
	cmd := newGitCmd(ctx, "ls-tree", "-z", "--name-only", "HEAD", "--", path)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git ls-tree HEAD %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return len(out) > 0, nil
}

// CurrentBranch returns the currently-checked-out branch name for repoDir.
// Used by auto-push helpers to know which branch to push.
//
// It reads `git symbolic-ref -q HEAD` and trims "refs/heads/". `rev-parse
// --abbrev-ref HEAD` returned "heads/<n>" when a same-named tag existed,
// which the branch grammar would then push as refs/heads/heads/<n>. A
// detached HEAD (exit status 1) returns "HEAD", the value rev-parse
// returned; an unborn branch now returns its name instead of an error.
func CurrentBranch(ctx context.Context, repoDir string) (string, error) {
	cmd := newGitCmd(ctx, "symbolic-ref", "-q", "HEAD")
	cmd.Dir = repoDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "HEAD", nil
		}
		return "", fmt.Errorf("git symbolic-ref -q HEAD: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), headsPrefix), nil
}

func stageFile(ctx context.Context, repoDir, path string) error {
	cmd := newGitCmd(ctx, "add", "--", path)
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

	cmd := newGitCmd(ctx, args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit %s: %s", path, strings.TrimSpace(string(out)))
	}

	shaCmd := newGitCmd(ctx, "rev-parse", "HEAD")
	shaCmd.Dir = repoDir
	shaOut, err := shaCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %s", strings.TrimSpace(string(shaOut)))
	}
	return strings.TrimSpace(string(shaOut)), nil
}
