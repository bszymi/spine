package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/git/refname"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/worktreefs"
)

// WriteResult contains the result of an artifact write operation.
type WriteResult struct {
	Artifact  *domain.Artifact
	CommitSHA string
}

// Service implements artifact CRUD operations backed by Git.
type Service struct {
	git          git.GitClient
	events       event.EventRouter
	repo         string               // repository root path
	artifactsDir string               // artifacts directory relative to repo (empty or "/" means repo root)
	policy       branchprotect.Policy // branch-protection guard for direct writes; required in production (ADR-009 §3)
	defaultRef   string               // branch to report when no WriteContext is set; defaults to "main"
}

// NewService creates a new Artifact Service.
//
// Production construction MUST follow up with WithPolicy to install the
// branch-protection guard (ADR-009 §3). A Service without a policy is
// fail-closed at the write boundary — writeAndCommit refuses to advance
// any ref until a policy is wired. Tests that do not care about branch
// protection can install a permissive policy via
// branchprotect.New(branchprotect.StaticRules(nil-slice → bootstrap,
// []config.Rule{} → no protection)).
func NewService(gitClient git.GitClient, events event.EventRouter, repoPath string) *Service {
	return &Service{
		git:          gitClient,
		events:       events,
		repo:         repoPath,
		artifactsDir: "/",
		defaultRef:   "main",
	}
}

// WithArtifactsDir sets the artifacts directory for path resolution.
// When set to a non-root value (e.g., "spine"), all artifact paths
// are prefixed with this directory for file I/O and git operations.
func (s *Service) WithArtifactsDir(dir string) {
	s.artifactsDir = dir
}

// ArtifactsDir returns the configured artifacts directory ("/" for the
// repository root). Wiring passes it to Orchestrator.WithArtifactsDir so
// the governed merge resolves a run's TaskPath exactly as this service
// read it at run start (INIT-023 TASK-017). A nil service has none.
func (s *Service) ArtifactsDir() string {
	if s == nil {
		return ""
	}
	return s.artifactsDir
}

// WithPolicy installs the branch-protection policy consulted by every
// ref-advancing helper (ADR-009 §3). Callers in production wire the
// projection-backed policy built in cmd/spine; tests inject a mock or a
// permissive policy. Passing nil is equivalent to never calling
// WithPolicy — the Service remains fail-closed.
func (s *Service) WithPolicy(p branchprotect.Policy) {
	s.policy = p
}

// WithDefaultRef overrides the branch reported to branch-protection when
// no WriteContext is set on the request. The default is "main" — matching
// the bootstrap authoritative-branch name used throughout Spine. Tests
// that check-out a different authoritative branch point this at that
// name so the policy evaluator sees the real target.
func (s *Service) WithDefaultRef(ref string) {
	if ref != "" {
		s.defaultRef = ref
	}
}

// repoRelativePath converts an artifact-relative path to a repo-relative path.
// When artifactsDir is "/" (root), paths pass through unchanged.
// When artifactsDir is "spine", "governance/charter.md" becomes "spine/governance/charter.md".
func (s *Service) repoRelativePath(artifactPath string) string {
	artifactPath = strings.TrimPrefix(artifactPath, "/")
	if s.artifactsDir == "/" || s.artifactsDir == "" {
		return artifactPath
	}
	return filepath.Join(s.artifactsDir, artifactPath)
}

// safePath validates a path against the main repo root (see safePathIn).
func (s *Service) safePath(path string) (string, error) {
	return s.safePathIn(s.repo, path)
}

// cleanPath validates an artifact-relative path lexically and returns its
// repo-relative, slash-form path — the one path every later file and git
// operation uses, so the path that is checked is the path that is staged.
//
// The caller's artifact-relative path is cleaned FIRST, before
// repoRelativePath joins it under artifacts_dir: filepath.Join folds ".."
// away, so "a/../../x.md" under artifacts_dir "spine" would otherwise
// become the clean-looking "x.md" and escape the artifacts directory.
// worktreefs.Clean refuses ".." and the git directory outright (INIT-023
// TASK-004).
func (s *Service) cleanPath(path string) (string, error) {
	// Reject absolute paths before any processing (repoRelativePath would
	// otherwise strip the leading "/").
	if filepath.IsAbs(path) {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("path must be relative: %s", path))
	}
	cleaned, err := worktreefs.Clean(path)
	if err != nil {
		return "", worktreeError(context.Background(), path, "validate path", err)
	}
	rel, err := worktreefs.Clean(s.repoRelativePath(cleaned))
	if err != nil {
		return "", worktreeError(context.Background(), path, "validate path", err)
	}
	return rel, nil
}

// safePathIn validates path for file I/O under root and returns its
// repo-relative, slash-form path. root may be the main repo or an isolated
// worktree directory; it is trusted (symlinks in its own path are followed).
//
// Beyond the lexical rules of cleanPath, every existing component of the
// path is checked from a directory handle opened without following
// symlinks (worktreefs): a symbolic link anywhere in the path — the final
// component included, dangling or not — is refused, as is a non-directory
// parent or a non-regular leaf. A missing leaf or parent is fine (Create).
// The checks are repeated by every worktreefs operation that follows, so
// this is the single validation entry, not the only guard. Refusals are
// ErrInvalidParams whose message names only repo-relative components and
// that satisfy errors.Is(err, worktreefs.ErrUnsafePath).
func (s *Service) safePathIn(root, path string) (string, error) {
	rel, err := s.cleanPath(path)
	if err != nil {
		return "", err
	}
	tree, err := worktreefs.Open(root)
	if err != nil {
		// Log the underlying error server-side; do not leak the repo
		// root path back to the caller.
		slog.Default().Warn("safePath: open root failed", "error", err, "root", root)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}
	defer func() { _ = tree.Close() }()
	if _, err := tree.Lstat(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", worktreeError(context.Background(), path, "validate path", err)
	}
	return rel, nil
}

// worktreeError maps a worktreefs failure onto the domain error contract:
// a refused path is ErrInvalidParams (400) carrying the PathError text,
// which names only repo-relative components; a read over the worktree read
// cap is ErrPayloadTooLarge (413), the status TASK-003 uses for an
// oversized git blob; anything else is logged server-side and returned as
// a generic ErrInternal. The worktreefs error stays the cause, so
// errors.Is(err, worktreefs.ErrUnsafePath) / worktreefs.ErrTooLarge hold.
func worktreeError(ctx context.Context, path, action string, err error) error {
	var pe *worktreefs.PathError
	if errors.As(err, &pe) {
		return domain.NewErrorWithCause(domain.ErrInvalidParams, pe.Error(), err)
	}
	var se *worktreefs.SizeError
	if errors.As(err, &se) {
		return domain.NewErrorWithCause(domain.ErrPayloadTooLarge, se.Error(), err)
	}
	observe.Logger(ctx).Warn("artifact worktree I/O failed",
		"action", action, "path", path, "error", err)
	return domain.NewErrorWithCause(domain.ErrInternal,
		fmt.Sprintf("%s %s failed", action, path), err)
}

// readRepoFile reads an artifact's bytes from the main working tree
// through worktreefs (symlinks refused, read capped).
//
// The read holds the working tree (an in-place scope, INIT-023 TASK-012
// review round 1). In standalone's shared tree a governed sequence — a
// convergence merging a divergence branch, the status cascade, the
// recovery ledger — checks a run branch out while it holds the tree, and
// an unlocked read then returned that branch's unreviewed file, which
// the acceptance and follow-up paths wrote back onto main. Under the lock
// the read waits for the sequence and its scope's entry restore, so it
// sees the home branch's file. A tree with no home branch (an embedder's
// per-request worktree) takes an uncontended lock and reads exactly what
// it read before. The bytes are also the expected original of the update
// that follows (updateFrom), so a change that lands between the two is a
// conflict, never overwritten.
func (s *Service) readRepoFile(ctx context.Context, path string) ([]byte, error) {
	if _, err := s.cleanPath(path); err != nil {
		return nil, err
	}
	scope, err := git.EnterBranch(ctx, s.repo, "", nil)
	if err != nil {
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}
	defer scope.Cleanup()
	rel, err := s.safePath(path)
	if err != nil {
		return nil, err
	}
	tree, err := worktreefs.Open(s.repo)
	if err != nil {
		return nil, worktreeError(ctx, path, "open repository for", err)
	}
	defer func() { _ = tree.Close() }()
	raw, err := tree.ReadFile(rel)
	if err != nil {
		return nil, worktreeError(ctx, path, "read file", err)
	}
	return raw, nil
}

// Create creates a new artifact, validates it, writes the file, commits to Git,
// and emits an artifact_created event.
func (s *Service) Create(ctx context.Context, path, content string) (*WriteResult, error) {
	return s.writeAndCommit(ctx, path, content, writeOp{
		requireExists: false,
		messageVerb:   "Create",
		operation:     "artifact.create",
		eventType:     domain.EventArtifactCreated,
		logAction:     "artifact created",
	})
}

// writeOp encodes the three axes that distinguish Create from Update: the
// pre-check semantics, the commit-message verb + operation trailer, and the
// emitted event type. Both paths share the remaining skeleton (parse →
// validate → branch protection → cleanPath → enterBranch → safePathIn →
// pre-check → confined write → commit → rollback-on-error → autoPush →
// emit).
type writeOp struct {
	requireExists bool // false: must not exist (Create); true: must exist (Update)
	messageVerb   string
	operation     string
	eventType     domain.EventType
	logAction     string
	// checkOriginal (Update only): the file read inside the write scope
	// must still be original, the bytes the new content was derived from
	// (updateFrom); otherwise the write is a conflict and nothing changes.
	checkOriginal bool
	original      []byte
}

func (s *Service) writeAndCommit(ctx context.Context, path, content string, op writeOp) (*WriteResult, error) {
	log := observe.Logger(ctx)

	artifact, err := Parse(path, []byte(content))
	if err != nil {
		return nil, domain.NewError(domain.ErrValidationFailed, err.Error())
	}

	result := Validate(artifact)
	if result.Status == "failed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"artifact validation failed", result.Errors)
	}

	// Branch-protection check (ADR-009 §3). Run before enterBranch so a
	// denied write never creates a worktree. The Artifact Service writes
	// commits but never merges — every writeAndCommit is an OpDirectWrite.
	// OpGovernedMerge lives on the Orchestrator (EPIC-003 TASK-002).
	bpResult, err := s.checkBranchProtection(ctx, op.operation)
	if err != nil {
		return nil, err
	}

	// Lexical validation before enterBranch, so a refused path never
	// creates a worktree.
	if _, err := s.cleanPath(path); err != nil {
		return nil, err
	}

	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	// The deferred Cleanup covers the error paths; the success path
	// releases the scope explicitly before any event (below).
	defer scope.Cleanup()
	// Every git call inside the scope uses sctx (INIT-023 TASK-012): for
	// an in-place write it carries the shared tree's ticket, so a locked
	// git call re-enters the scope instead of waiting for it, and in
	// standalone's shared tree it is detached from the request (bounded),
	// so a client disconnect cannot leave a write half-staged. Events keep
	// ctx.
	sctx := scope.Context(ctx)

	// All file I/O below goes through worktreefs (INIT-023 TASK-004, audit
	// F5): every component is Lstat-checked from a directory handle, so a
	// symlink committed on the branch — at the leaf or above it, dangling
	// or not, into .git/ or out of the tree — is refused before any
	// filesystem change and before anything is staged.
	repoPath, err := s.safePathIn(scope.RepoDir, path)
	if err != nil {
		return nil, err
	}
	tree, err := worktreefs.Open(scope.RepoDir)
	if err != nil {
		return nil, worktreeError(ctx, path, "open worktree for", err)
	}
	defer func() { _ = tree.Close() }()

	var originalContent []byte
	var originalMode fs.FileMode
	var undo *worktreefs.Undo
	if op.requireExists {
		fi, err := tree.Lstat(repoPath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("artifact not found: %s", path))
		} else if err != nil {
			return nil, worktreeError(ctx, path, "check", err)
		}
		originalMode = fi.Mode().Perm()
		originalContent, err = tree.ReadFile(repoPath)
		if err != nil {
			return nil, worktreeError(ctx, path, "read original", err)
		}
		if op.checkOriginal && !bytes.Equal(originalContent, op.original) {
			return nil, domain.NewError(domain.ErrConflict,
				fmt.Sprintf("artifact %s changed while the update was being prepared; retry", path))
		}
		// Replace never opens the existing file for writing: a fresh
		// O_EXCL temp file is renamed over it, keeping its mode.
		if err := tree.Replace(repoPath, []byte(content)); err != nil {
			return nil, worktreeError(ctx, path, "write file", err)
		}
	} else {
		// O_CREAT|O_EXCL: the final component is never followed.
		undo, err = tree.Create(repoPath, []byte(content), 0o644)
		if errors.Is(err, fs.ErrExist) {
			return nil, domain.NewError(domain.ErrAlreadyExists,
				fmt.Sprintf("artifact already exists at path: %s", path))
		}
		if err != nil {
			return nil, worktreeError(ctx, path, "write file", err)
		}
		defer undo.Release()
	}

	trailers := observe.TrailersFromContext(ctx, op.operation)
	if bpResult.overrideHonoured {
		// ADR-009 §4: API-path overrides carry a Branch-Protection-Override
		// trailer. Git-push overrides do not (handler does not rewrite
		// pushed commits). The trailer is a convenience for `git log`
		// inspection; the governance event below is the authoritative
		// audit record.
		trailers["Branch-Protection-Override"] = "true"
	}
	commitResult, err := git.StageAndCommit(sctx, scope, repoPath, git.CommitOpts{
		Message:  fmt.Sprintf("%s %s: %s", op.messageVerb, artifact.Type, artifact.Title),
		Trailers: trailers,
		Author: git.Author{
			Name:  observe.ActorID(ctx),
			Email: observe.ActorID(ctx) + "@spine.local",
		},
	})
	if err != nil {
		// Rollback. StageAndCommit already unstaged on failure, so we only
		// have to restore file state here: the original bytes and mode for
		// an update; the file and every directory this call created for a
		// create — including a file a failing hook put in its place by a
		// rename, when the path is still untracked in HEAD
		// (claimUntracked). The update restore is WriteFile, not Replace:
		// a failing hook may have deleted the file (or its directory), and
		// WriteFile re-creates it O_EXCL with the original mode, while a
		// link put in its place is still refused, never followed.
		if op.requireExists {
			if rbErr := tree.WriteFile(repoPath, originalContent, originalMode); rbErr != nil {
				log.Warn("artifact rollback: restore failed", "path", path, "error", rbErr)
			}
		} else if rbErr := undo.RollbackClaiming(claimUntracked(sctx, scope.RepoDir, repoPath, path)); rbErr != nil {
			log.Warn("artifact rollback: remove failed", "path", path, "error", rbErr)
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	s.autoPush(sctx, scope.RepoDir)
	// Release the working tree before emitting (INIT-023 TASK-012): an
	// event published while the shared tree is held could deadlock with a
	// queue consumer that needs it. Cleanup is idempotent; the deferred
	// call is then a no-op.
	scope.Cleanup()

	log.Info(op.logAction,
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	s.emitEvent(ctx, op.eventType, artifact, commitResult.SHA)

	if bpResult.overrideHonoured {
		s.emitBranchProtectionOverride(ctx, op.operation, bpResult.branch, bpResult.ruleKinds, commitResult.SHA)
	}

	return &WriteResult{Artifact: artifact, CommitSHA: commitResult.SHA}, nil
}

// claimUntracked returns the worktreefs.Undo.RollbackClaiming callback for
// a create whose commit failed (INIT-023 TASK-004 review round 2). It is
// asked only when the file at rel is a regular file that is no longer the
// one Create made: a failing pre-commit hook rewrote it through a temp file
// and a rename (`sed -i`, formatters). Such a file is this call's leftover
// when rel is untracked in HEAD — StageAndCommit has already unstaged it —
// so it is claimed and removed with the directories Create made, as the
// pre-TASK-004 remove-by-name did, and a retry is not already_exists. A
// path tracked in HEAD (a concurrent writer in the shared standalone tree
// committed it meanwhile) or any git failure keeps the file. The check runs
// on a context detached from the request's cancellation, so a client that
// gave up does not turn the rollback into "keep".
func claimUntracked(ctx context.Context, repoDir, rel, path string) func() bool {
	return func() bool {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		tracked, err := git.TrackedInHEAD(cctx, repoDir, rel)
		if err != nil {
			observe.Logger(ctx).Warn("artifact rollback: cannot tell whether the replaced file is tracked; keeping it",
				"path", path, "error", err)
			return false
		}
		return !tracked
	}
}

// Read reads an artifact from Git at the specified ref (or HEAD if empty).
func (s *Service) Read(ctx context.Context, path, ref string) (*domain.Artifact, error) {
	if ref == "" {
		ref = "HEAD"
	}

	repoPath := s.repoRelativePath(path)
	content, err := s.git.ReadFile(ctx, ref, repoPath)
	if err != nil {
		if gitErr, ok := err.(*git.GitError); ok && gitErr.Kind == git.ErrKindNotFound {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("artifact not found: %s at ref %s", path, ref))
		}
		// A blob over the git client's read limit (INIT-023 TASK-003) is
		// 413, the status an oversized worktree read gets (TASK-004).
		if errors.Is(err, git.ErrBlobTooLarge) {
			return nil, domain.NewErrorWithCause(domain.ErrPayloadTooLarge,
				fmt.Sprintf("artifact %s at ref %s %s", path, ref, ReadLimitPhrase(err)), err)
		}
		// A gitlink, a directory or a file entry recording a tag, a tree
		// or a commit at the path (INIT-023 wave 4 follow-up and its
		// review: the git client refuses it unread) holds no artifact:
		// 404, as for a missing path — not the 500 a rendered listing or
		// commit failing to parse used to be.
		if errors.Is(err, git.ErrNotBlob) {
			return nil, domain.NewErrorWithCause(domain.ErrNotFound,
				fmt.Sprintf("artifact not found: %s at ref %s: %s", path, ref, NotAFilePhrase(err)), err)
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	artifact, err := Parse(path, content)
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("parse artifact %s: %v", path, err))
	}

	return artifact, nil
}

// Update updates an existing artifact, validates the new content, commits to Git,
// and emits an artifact_updated event.
func (s *Service) Update(ctx context.Context, path, content string) (*WriteResult, error) {
	return s.writeAndCommit(ctx, path, content, writeOp{
		requireExists: true,
		messageVerb:   "Update",
		operation:     "artifact.update",
		eventType:     domain.EventArtifactUpdated,
		logAction:     "artifact updated",
	})
}

// updateFrom is Update for a read-modify-write: content was derived from
// original, the file's bytes as readRepoFile read them, and the write
// commits only if the file is still original when the write scope holds
// the tree (INIT-023 TASK-012 review round 1). The acceptance and
// follow-up-link paths use it, so a governed merge that changes the task
// between their read and their write is never reverted by it.
func (s *Service) updateFrom(ctx context.Context, path string, original []byte, content string) (*WriteResult, error) {
	return s.writeAndCommit(ctx, path, content, writeOp{
		requireExists: true,
		messageVerb:   "Update",
		operation:     "artifact.update",
		eventType:     domain.EventArtifactUpdated,
		logAction:     "artifact updated",
		checkOriginal: true,
		original:      original,
	})
}

// List scans the repository for all artifacts.
//
// ref is resolved ONCE to an object id (RefSHA) unless it is "HEAD" or
// already a full object id, and the listing and every per-file read use
// that id (INIT-023 TASK-002, DC-7). Each read of a branch name would
// otherwise pay its own exact branch lookup (one extra git process per
// artifact on AddToRun and /artifacts/add), and a branch that moved
// mid-listing would mix two snapshots.
func (s *Service) List(ctx context.Context, ref string) ([]*domain.Artifact, error) {
	if ref == "" {
		ref = "HEAD"
	}
	if ref != "HEAD" && !git.IsFullObjectName(ref) {
		sha, err := s.git.RefSHA(ctx, ref)
		if err != nil {
			return nil, domain.NewError(domain.ErrGit, err.Error())
		}
		ref = sha
	}

	// Scope listing to the artifacts directory.
	pattern := "*.md"
	if s.artifactsDir != "/" && s.artifactsDir != "" {
		pattern = s.artifactsDir + "/"
	}

	files, err := s.git.ListFiles(ctx, ref, pattern)
	if err != nil {
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	var artifacts []*domain.Artifact
	for _, file := range files {
		// Only include .md files when using directory prefix filter.
		if !strings.HasSuffix(file, ".md") {
			continue
		}
		// Raw listings (INIT-023 TASK-003) now carry paths git used to
		// C-quote — and the quoted form never ended in ".md". A path
		// discovery refuses to project is left out here too, exactly as
		// it was before.
		if UnsafePath(file) {
			continue
		}

		content, err := s.git.ReadFile(ctx, ref, file)
		if err != nil {
			continue // skip unreadable files
		}

		if !IsArtifact(content) {
			continue
		}

		// Strip the artifacts_dir prefix so paths remain artifacts-relative.
		artifactPath := s.stripArtifactsDir(file)
		a, err := Parse(artifactPath, content)
		if err != nil {
			continue // skip unparseable artifacts
		}

		artifacts = append(artifacts, a)
	}

	return artifacts, nil
}

// checkBranchProtection consults the installed branch-protection policy
// (ADR-009 §3) before the Service advances any ref. The Artifact Service
// only produces direct commits — merges are the Orchestrator's job — so
// every write is classified as OpDirectWrite. A nil policy is fail-closed:
// the Service refuses to write until WithPolicy has installed one.
//
// Branch resolution: a non-empty WriteContext.Branch is the destination
// (typically a "spine/run/*" branch that matches no protection rule). A
// missing WriteContext means the write would land on the main repo's
// authoritative branch — s.defaultRef. That is the case the policy exists
// to gate; a contributor who omits WriteContext and targets "main" is
// exactly what "no-direct-write" is for.
//
// Override is threaded from WriteContext.Override (set by the gateway's
// handleArtifact* path from write_context.override in the request body).
// The policy evaluator gates effective use on Actor.Role ≥ operator; a
// contributor who sets the flag sees a distinct "override not authorised"
// reason rather than a silent "rule denies".
//
// Returns (overrideHonoured, err). The caller uses overrideHonoured to
// stamp the commit trailer and emit the governance event (ADR-009 §4);
// neither is produced when the flag was set but did not actually bypass
// a rule (e.g. a branch with no matching rule — the write would have
// succeeded either way).
type branchProtectResult struct {
	overrideHonoured bool
	branch           string
	ruleKinds        []string
}

func (s *Service) checkBranchProtection(ctx context.Context, operation string) (branchProtectResult, error) {
	if s.policy == nil {
		return branchProtectResult{}, domain.NewError(domain.ErrUnavailable,
			"artifact service: branch-protection policy not configured (production must call WithPolicy; tests may install a permissive policy)")
	}

	branch := s.defaultRef
	override := false
	if wc := GetWriteContext(ctx); wc != nil {
		if wc.Branch != "" {
			branch = wc.Branch
		}
		override = wc.Override
	}

	req := branchprotect.Request{
		Branch:   branch,
		Kind:     branchprotect.OpDirectWrite,
		Actor:    actorForRequest(ctx),
		Override: override,
		RunID:    observe.RunID(ctx),
		TraceID:  observe.TraceID(ctx),
	}

	decision, reasons, evalErr := s.policy.Evaluate(ctx, req)
	if evalErr != nil {
		// Policy error is fail-closed — the evaluator itself signals Deny
		// with a non-nil error when the rule source is unreachable, and
		// we surface that as ErrInternal so the gateway renders a 5xx
		// rather than a 403 that implies "this is forbidden by rule".
		observe.Logger(ctx).Error("branch-protection evaluation failed",
			"operation", operation,
			"branch", branch,
			"error", evalErr.Error(),
		)
		return branchProtectResult{}, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("branch-protection evaluation failed: %v", evalErr))
	}

	if decision == branchprotect.DecisionDeny {
		observe.Logger(ctx).Info("branch-protection denied write",
			"operation", operation,
			"branch", branch,
			"reasons", reasonCodes(reasons),
		)
		return branchProtectResult{}, domain.NewErrorWithDetail(domain.ErrForbidden,
			firstDenyMessage(reasons, branch),
			map[string]any{
				"branch":  branch,
				"reasons": reasonsToDetail(reasons),
			})
	}

	honoured, kinds := overrideHonoured(reasons)
	return branchProtectResult{
		overrideHonoured: honoured,
		branch:           branch,
		ruleKinds:        kinds,
	}, nil
}

// overrideHonoured reports whether the policy's allow-decision reasons
// indicate that an override actually bypassed a matching rule, and
// returns the bypassed rule kinds. A write that allowed without invoking
// override (e.g. no matching rule) returns (false, nil) — the caller
// skips the governance event and commit trailer, per ADR-009 §4.
func overrideHonoured(reasons []branchprotect.Reason) (bool, []string) {
	var kinds []string
	for _, r := range reasons {
		if r.Code == branchprotect.ReasonOverrideHonoured {
			kinds = append(kinds, string(r.RuleKind))
		}
	}
	return len(kinds) > 0, kinds
}

// actorForRequest resolves the authenticated actor from ctx for use in a
// branchprotect.Request. Falls back to a zero-role actor identified only
// by observe.ActorID(ctx) when no full actor is bound — the evaluator
// then treats Override requests as unauthorised (correct fail-closed
// behaviour for the unauthenticated / test paths).
func actorForRequest(ctx context.Context) domain.Actor {
	if a := domain.ActorFromContext(ctx); a != nil {
		return *a
	}
	return domain.Actor{ActorID: observe.ActorID(ctx)}
}

func reasonCodes(reasons []branchprotect.Reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r.Code)
	}
	return out
}

func reasonsToDetail(reasons []branchprotect.Reason) []map[string]string {
	out := make([]map[string]string, len(reasons))
	for i, r := range reasons {
		entry := map[string]string{
			"code":    string(r.Code),
			"message": r.Message,
		}
		if r.RuleKind != "" {
			entry["rule_kind"] = string(r.RuleKind)
		}
		out[i] = entry
	}
	return out
}

func firstDenyMessage(reasons []branchprotect.Reason, branch string) string {
	if len(reasons) == 0 {
		return fmt.Sprintf("branch %q is protected", branch)
	}
	return reasons[0].Message
}

// enterBranch prepares an isolated working directory for branch-scoped writes.
// When a WriteContext specifies a branch, a git worktree is created so that
// file I/O and commits target the branch without changing the main working
// tree. Returns a *git.WriteScope whose RepoDir should be used for all file
// and git operations, and whose Cleanup must be deferred.
func (s *Service) enterBranch(ctx context.Context) (*git.WriteScope, error) {
	wc := GetWriteContext(ctx)
	branch := ""
	if wc != nil {
		branch = wc.Branch
	}
	scope, err := git.EnterBranch(ctx, s.repo, branch, refname.Validate)
	if err != nil {
		// Preserve typed domain error so the gateway surfaces git_error, not
		// internal_error, on the realistic "branch missing or already
		// checked out" failure path. refname.Validate already returns a
		// typed error; passthrough only the untyped worktree failure.
		if _, ok := err.(*domain.SpineError); ok {
			return nil, err
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}
	return scope, nil
}

// stripArtifactsDir removes the artifacts directory prefix from a repo-relative path.
func (s *Service) stripArtifactsDir(repoRelPath string) string {
	if s.artifactsDir == "/" || s.artifactsDir == "" {
		return repoRelPath
	}
	prefix := s.artifactsDir + "/"
	return strings.TrimPrefix(repoRelPath, prefix)
}

// autoPush pushes the current branch to origin after a commit.
// repoDir is the working directory (main repo or worktree) used to determine
// the current branch. The push itself goes through the main git client since
// worktrees share the same object store.
// Push failures are logged as warnings but do not fail the operation.
// Disabled when SPINE_GIT_AUTO_PUSH is set to "false".
func (s *Service) autoPush(ctx context.Context, repoDir string) {
	if strings.EqualFold(os.Getenv("SPINE_GIT_AUTO_PUSH"), "false") {
		return
	}

	log := observe.Logger(ctx)

	branch, err := git.CurrentBranch(ctx, repoDir)
	if err != nil {
		log.Warn("auto-push: failed to determine current branch", "error", err)
		return
	}

	if err := s.git.Push(ctx, "origin", branch); err != nil {
		log.Warn("auto-push: push failed", "branch", branch, "error", err)
	}
}

// emitEvent publishes a domain event for artifact changes.
func (s *Service) emitEvent(ctx context.Context, eventType domain.EventType, a *domain.Artifact, commitSHA string) {
	if s.events == nil {
		return
	}

	eventID, _ := observe.GenerateTraceID()
	evt := domain.Event{
		EventID:      eventID,
		Type:         eventType,
		Timestamp:    time.Now(),
		ActorID:      observe.ActorID(ctx),
		RunID:        observe.RunID(ctx),
		ArtifactPath: a.Path,
		TraceID:      observe.TraceID(ctx),
		Payload: mustJSON(map[string]string{
			"commit_sha":    commitSHA,
			"artifact_id":   a.ID,
			"artifact_type": string(a.Type),
			"status":        string(a.Status),
		}),
	}

	// Fire and forget — event delivery is async, errors logged by EmitLogged.
	event.EmitLogged(ctx, s.events, evt)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// emitBranchProtectionOverride emits the governance event required by
// ADR-009 §4 for every honored override on the Spine write path. The
// payload captures the actor, branch, bypassed rule kinds, operation,
// trace/run identifiers, and the resulting commit SHA — commitSHA is
// empty on the Delete path (not yet exposed on the API), which the event
// renders as null via omitempty.
func (s *Service) emitBranchProtectionOverride(ctx context.Context, operation, branch string, ruleKinds []string, commitSHA string) {
	if s.events == nil {
		return
	}
	// Prefer the authenticated domain.Actor's ID for the governance
	// audit event — observe.ActorID is a free-floating trace field that
	// tests may leave unset, but the policy gate fired because of the
	// actor bound to the request context.
	actorID := observe.ActorID(ctx)
	if a := domain.ActorFromContext(ctx); a != nil && a.ActorID != "" {
		actorID = a.ActorID
	}
	eventID, _ := observe.GenerateTraceID()
	evt := domain.Event{
		EventID:   eventID,
		Type:      domain.EventBranchProtectionOverride,
		Timestamp: time.Now(),
		ActorID:   actorID,
		RunID:     observe.RunID(ctx),
		TraceID:   observe.TraceID(ctx),
		Payload: mustJSON(map[string]any{
			"branch":     branch,
			"rule_kinds": ruleKinds,
			"operation":  operation,
			"commit_sha": nullableSHA(commitSHA),
		}),
	}
	event.EmitLogged(ctx, s.events, evt)
}

// nullableSHA returns nil for an empty SHA so JSON marshals to null,
// matching the Git-path event shape for override-deletes (ADR-009 §4).
func nullableSHA(sha string) any {
	if sha == "" {
		return nil
	}
	return sha
}
