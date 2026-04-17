package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
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
	repo         string // repository root path
	artifactsDir string // artifacts directory relative to repo (empty or "/" means repo root)
}

// NewService creates a new Artifact Service.
func NewService(gitClient git.GitClient, events event.EventRouter, repoPath string) *Service {
	return &Service{
		git:          gitClient,
		events:       events,
		repo:         repoPath,
		artifactsDir: "/",
	}
}

// WithArtifactsDir sets the artifacts directory for path resolution.
// When set to a non-root value (e.g., "spine"), all artifact paths
// are prefixed with this directory for file I/O and git operations.
func (s *Service) WithArtifactsDir(dir string) {
	s.artifactsDir = dir
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

// safePath validates and resolves a path against the main repo root.
func (s *Service) safePath(path string) (string, error) {
	return s.safePathIn(s.repo, path)
}

// safePathIn validates and resolves a path, ensuring it stays within root.
// The input path is artifact-relative; it is first converted to a repo-relative
// path via repoRelativePath before being joined with root.
// root may be the main repo or an isolated worktree directory.
func (s *Service) safePathIn(root, path string) (string, error) {
	// Reject absolute paths before any processing.
	if filepath.IsAbs(path) {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("path must be relative: %s", path))
	}

	repoPath := s.repoRelativePath(path)
	fullPath := filepath.Join(root, repoPath)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		// Log the underlying error server-side; do not leak the repo
		// root path back to the caller.
		slog.Default().Warn("safePath: abs(root) failed", "error", err, "root", root)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	// Resolve symlinks on the root
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		slog.Default().Warn("safePath: evalSymlinks(root) failed", "error", err, "root", absRoot)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	// Resolve the target path — for new files, resolve the parent directory
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		slog.Default().Warn("safePath: abs(fullPath) failed", "error", err, "path", path)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	// Resolve symlinks by walking up to the nearest existing ancestor.
	// This prevents escaping via symlinked directories with missing descendants.
	realPath, err := resolveToExistingAncestor(absPath)
	if err != nil {
		slog.Default().Warn("safePath: resolveToExistingAncestor failed", "error", err, "path", path)
		return "", domain.NewError(domain.ErrInvalidParams, "invalid path")
	}

	if !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) && realPath != realRoot {
		return "", domain.NewError(domain.ErrInvalidParams, "path escapes repository")
	}
	return absPath, nil
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
// emitted event type. Both paths share the remaining 10-step skeleton
// (parse → validate → enterBranch → safePath → pre-check → write → commit
// → rollback-on-error → autoPush → emit).
type writeOp struct {
	requireExists bool // false: must not exist (Create); true: must exist (Update)
	messageVerb   string
	operation     string
	eventType     domain.EventType
	logAction     string
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

	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.Cleanup()

	fullPath, err := s.safePathIn(scope.RepoDir, path)
	if err != nil {
		return nil, err
	}

	var originalContent []byte
	if op.requireExists {
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("artifact not found: %s", path))
		}
		var readErr error
		originalContent, readErr = os.ReadFile(fullPath)
		if readErr != nil {
			return nil, domain.NewError(domain.ErrInternal,
				fmt.Sprintf("read original %s: %v", path, readErr))
		}
	} else {
		if _, err := os.Stat(fullPath); err == nil {
			return nil, domain.NewError(domain.ErrAlreadyExists,
				fmt.Sprintf("artifact already exists at path: %s", path))
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return nil, domain.NewError(domain.ErrInternal,
				fmt.Sprintf("create directory %s: %v", filepath.Dir(fullPath), err))
		}
	}

	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("write file %s: %v", path, err))
	}

	repoPath := s.repoRelativePath(path)
	commitResult, err := git.StageAndCommit(ctx, scope, repoPath, git.CommitOpts{
		Message:  fmt.Sprintf("%s %s: %s", op.messageVerb, artifact.Type, artifact.Title),
		Trailers: observe.TrailersFromContext(ctx, op.operation),
		Author: git.Author{
			Name:  observe.ActorID(ctx),
			Email: observe.ActorID(ctx) + "@spine.local",
		},
	})
	if err != nil {
		// Rollback. StageAndCommit already unstaged on failure, so we only
		// have to restore file state here.
		if op.requireExists {
			_ = os.WriteFile(fullPath, originalContent, 0o644) //nolint:gosec // G703: fullPath came from safePath above; 0644 required for git tracking
		} else {
			_ = os.Remove(fullPath)
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	s.autoPush(ctx, scope.RepoDir)

	log.Info(op.logAction,
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	s.emitEvent(ctx, op.eventType, artifact, commitResult.SHA)

	return &WriteResult{Artifact: artifact, CommitSHA: commitResult.SHA}, nil
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

// List scans the repository for all artifacts.
func (s *Service) List(ctx context.Context, ref string) ([]*domain.Artifact, error) {
	if ref == "" {
		ref = "HEAD"
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
	scope, err := git.EnterBranch(ctx, s.repo, branch, validateGitRefName)
	if err != nil {
		// Preserve typed domain error so the gateway surfaces git_error, not
		// internal_error, on the realistic "branch missing or already
		// checked out" failure path. validateGitRefName already returns a
		// typed error; passthrough only the untyped worktree failure.
		if _, ok := err.(*domain.SpineError); ok {
			return nil, err
		}
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}
	return scope, nil
}

// resolveToExistingAncestor resolves symlinks by walking up the path
// to the nearest existing ancestor, then appending the remaining components.
// This prevents symlink escapes via missing subdirectories.
func resolveToExistingAncestor(absPath string) (string, error) {
	// Try the full path first
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved, nil
	}

	// Walk up to find the nearest existing ancestor
	current := absPath
	var remainder []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root without finding an existing path
			return absPath, nil
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent

		if ancestorReal, err := filepath.EvalSymlinks(current); err == nil {
			// Found an existing ancestor — reconstruct the path
			resolved := ancestorReal
			for _, part := range remainder {
				resolved = filepath.Join(resolved, part)
			}
			return resolved, nil
		}
	}
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

	// Fire and forget — event delivery is async, but log failures
	if err := s.events.Emit(ctx, evt); err != nil {
		log := observe.Logger(ctx)
		log.Warn("failed to emit event",
			"event_type", eventType,
			"artifact_path", a.Path,
			"error", err,
		)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
