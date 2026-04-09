package artifact

import (
	"context"
	"encoding/json"
	"fmt"
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

// branchScope holds the working directory for branch-scoped operations.
// When a WriteContext specifies a branch, a git worktree is created so that
// file writes and commits happen in isolation — without changing the main
// working tree. This eliminates the race between artifact writes and
// orchestrator operations (MergeRunBranch) that share the same repo.
type branchScope struct {
	repoDir string // directory for file I/O and git commands (worktree or main repo)
	cleanup func() // releases the worktree (no-op when using the main repo)
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
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid repo path: %v", err))
	}

	// Resolve symlinks on the root
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("resolve repo path: %v", err))
	}

	// Resolve the target path — for new files, resolve the parent directory
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid path: %s", path))
	}

	// Resolve symlinks by walking up to the nearest existing ancestor.
	// This prevents escaping via symlinked directories with missing descendants.
	realPath, err := resolveToExistingAncestor(absPath)
	if err != nil {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("resolve path: %v", err))
	}

	if !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) && realPath != realRoot {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("path escapes repository: %s", path))
	}
	return absPath, nil
}

// Create creates a new artifact, validates it, writes the file, commits to Git,
// and emits an artifact_created event.
func (s *Service) Create(ctx context.Context, path, content string) (*WriteResult, error) {
	log := observe.Logger(ctx)

	// Parse and validate content before acquiring a worktree.
	artifact, err := Parse(path, []byte(content))
	if err != nil {
		return nil, domain.NewError(domain.ErrValidationFailed, err.Error())
	}

	result := Validate(artifact)
	if result.Status == "failed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"artifact validation failed", result.Errors)
	}

	// Acquire an isolated worktree for branch-scoped writes (or the main repo).
	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.cleanup()

	// Validate path stays within the working directory.
	fullPath, err := s.safePathIn(scope.repoDir, path)
	if err != nil {
		return nil, err
	}

	// Check for duplicate path
	if _, err := os.Stat(fullPath); err == nil {
		return nil, domain.NewError(domain.ErrAlreadyExists,
			fmt.Sprintf("artifact already exists at path: %s", path))
	}

	// Write file
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("create directory %s: %v", dir, err))
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("write file %s: %v", path, err))
	}

	// Stage and commit using repo-relative path for git operations.
	repoPath := s.repoRelativePath(path)
	trailers := observe.TrailersFromContext(ctx, "artifact.create")

	commitResult, err := s.stageAndCommit(ctx, scope.repoDir, repoPath, git.CommitOpts{
		Message:  fmt.Sprintf("Create %s: %s", artifact.Type, artifact.Title),
		Trailers: trailers,
		Author: git.Author{
			Name:  observe.ActorID(ctx),
			Email: observe.ActorID(ctx) + "@spine.local",
		},
	})
	if err != nil {
		// Clean up the file and unstage on commit failure
		_ = os.Remove(fullPath)
		_ = gitReset(ctx, scope.repoDir, repoPath)
		return nil, err
	}

	s.autoPush(ctx, scope.repoDir)

	log.Info("artifact created",
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	// Emit event
	s.emitEvent(ctx, domain.EventArtifactCreated, artifact, commitResult.SHA)

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
	log := observe.Logger(ctx)

	// Parse and validate new content before acquiring a worktree.
	artifact, err := Parse(path, []byte(content))
	if err != nil {
		return nil, domain.NewError(domain.ErrValidationFailed, err.Error())
	}

	result := Validate(artifact)
	if result.Status == "failed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"artifact validation failed", result.Errors)
	}

	// Acquire an isolated worktree for branch-scoped writes (or the main repo).
	scope, err := s.enterBranch(ctx)
	if err != nil {
		return nil, err
	}
	defer scope.cleanup()

	// Validate path stays within the working directory.
	fullPath, err := s.safePathIn(scope.repoDir, path)
	if err != nil {
		return nil, err
	}

	// Verify artifact exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return nil, domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("artifact not found: %s", path))
	}

	// Save original content for rollback
	originalContent, readErr := os.ReadFile(fullPath)
	if readErr != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("read original %s: %v", path, readErr))
	}

	// Write updated file
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("write file %s: %v", path, err))
	}

	// Stage and commit using repo-relative path for git operations.
	repoPath := s.repoRelativePath(path)
	trailers := observe.TrailersFromContext(ctx, "artifact.update")

	commitResult, err := s.stageAndCommit(ctx, scope.repoDir, repoPath, git.CommitOpts{
		Message:  fmt.Sprintf("Update %s: %s", artifact.Type, artifact.Title),
		Trailers: trailers,
		Author: git.Author{
			Name:  observe.ActorID(ctx),
			Email: observe.ActorID(ctx) + "@spine.local",
		},
	})
	if err != nil {
		// Rollback: restore original content
		_ = os.WriteFile(fullPath, originalContent, 0o644)
		return nil, err
	}

	s.autoPush(ctx, scope.repoDir)

	log.Info("artifact updated",
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	// Emit event
	s.emitEvent(ctx, domain.EventArtifactUpdated, artifact, commitResult.SHA)

	return &WriteResult{Artifact: artifact, CommitSHA: commitResult.SHA}, nil
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
// file I/O and commits target the branch without changing the main working tree.
// Returns a branchScope whose repoDir should be used for all file and git
// operations, and whose cleanup must be deferred.
func (s *Service) enterBranch(ctx context.Context) (*branchScope, error) {
	wc := GetWriteContext(ctx)
	if wc == nil || wc.Branch == "" {
		return &branchScope{repoDir: s.repo, cleanup: func() {}}, nil
	}

	// Create a temporary directory for the worktree.
	worktreeDir, err := os.MkdirTemp("", "spine-wt-*")
	if err != nil {
		return nil, domain.NewError(domain.ErrInternal,
			fmt.Sprintf("create worktree temp dir: %v", err))
	}
	// git worktree add requires the target path to not exist.
	os.Remove(worktreeDir)

	cmd := execCommand(ctx, "git", "worktree", "add", worktreeDir, wc.Branch)
	cmd.Dir = s.repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(worktreeDir)
		return nil, domain.NewError(domain.ErrGit,
			fmt.Sprintf("add worktree for branch %s: %s", wc.Branch, strings.TrimSpace(string(out))))
	}

	return &branchScope{
		repoDir: worktreeDir,
		cleanup: func() {
			rmCmd := execCommand(ctx, "git", "worktree", "remove", "--force", worktreeDir)
			rmCmd.Dir = s.repo
			_, _ = rmCmd.CombinedOutput()
			os.RemoveAll(worktreeDir)
		},
	}, nil
}

// stageAndCommit stages a file and creates a scoped Git commit (only this file).
// repoDir is the working directory — either the main repo or a worktree.
func (s *Service) stageAndCommit(ctx context.Context, repoDir, path string, opts git.CommitOpts) (git.CommitResult, error) {
	// Build the full commit message with trailers
	msg := opts.Message
	if len(opts.Trailers) > 0 {
		msg += "\n"
		for _, key := range []string{"Trace-ID", "Actor-ID", "Run-ID", "Operation"} {
			if val, ok := opts.Trailers[key]; ok {
				msg += "\n" + key + ": " + val
			}
		}
	}

	// Stage the file
	if err := gitAdd(ctx, repoDir, path); err != nil {
		return git.CommitResult{}, domain.NewError(domain.ErrGit,
			fmt.Sprintf("stage file %s: %v", path, err))
	}

	// Commit only this specific file (scoped via pathspec)
	sha, err := gitCommitPath(ctx, repoDir, path, msg, opts.Author)
	if err != nil {
		// Unstage the file to leave a clean index on failure
		_ = gitReset(ctx, repoDir, path)
		return git.CommitResult{}, domain.NewError(domain.ErrGit, err.Error())
	}

	return git.CommitResult{SHA: sha}, nil
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

	branch, err := gitCurrentBranch(ctx, repoDir)
	if err != nil {
		log.Warn("auto-push: failed to determine current branch", "error", err)
		return
	}

	if err := s.git.Push(ctx, "origin", branch); err != nil {
		log.Warn("auto-push: push failed", "branch", branch, "error", err)
	}
}

// gitCurrentBranch returns the name of the currently checked-out branch.
func gitCurrentBranch(ctx context.Context, repoDir string) (string, error) {
	cmd := execCommand(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: %s", string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitAdd(ctx context.Context, repoDir, path string) error {
	cmd := execCommand(ctx, "git", "add", "--", path)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add %s: %s", path, string(out))
	}
	return nil
}

// gitCommitPath commits only the specified file, not the entire index.
// Uses -- separator and pathspec to scope the commit.
func gitCommitPath(ctx context.Context, repoDir, path, message string, author git.Author) (string, error) {
	args := []string{"commit", "-m", message}
	if author.Name != "" && author.Email != "" {
		args = append(args, "--author", fmt.Sprintf("%s <%s>", author.Name, author.Email))
	}
	args = append(args, "--", path)

	cmd := execCommand(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git commit %s: %s", path, string(out))
	}

	// Get the commit SHA
	shaCmd := execCommand(ctx, "git", "rev-parse", "HEAD")
	shaCmd.Dir = repoDir
	shaOut, err := shaCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %s", string(shaOut))
	}
	return strings.TrimSpace(string(shaOut)), nil
}

// gitReset unstages a file from the Git index.
func gitReset(ctx context.Context, repoDir, path string) error {
	cmd := execCommand(ctx, "git", "reset", "HEAD", "--", path)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git reset %s: %s", path, string(out))
	}
	return nil
}

// execCommand is a variable for testing.
var execCommand = execCommandDefault

func execCommandDefault(ctx context.Context, name string, args ...string) *execCmd {
	return newExecCmd(ctx, name, args...)
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
