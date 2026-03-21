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

// Service implements artifact CRUD operations backed by Git.
type Service struct {
	git    git.GitClient
	events event.EventRouter
	repo   string // repository root path
}

// NewService creates a new Artifact Service.
func NewService(gitClient git.GitClient, events event.EventRouter, repoPath string) *Service {
	return &Service{
		git:    gitClient,
		events: events,
		repo:   repoPath,
	}
}

// safePath validates and resolves a path, ensuring it stays within the repo root.
// Resolves symlinks to prevent escaping via symlinked directories.
func (s *Service) safePath(path string) (string, error) {
	fullPath := filepath.Join(s.repo, path)
	absRepo, err := filepath.Abs(s.repo)
	if err != nil {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("invalid repo path: %v", err))
	}

	// Resolve symlinks on the repo root
	realRepo, err := filepath.EvalSymlinks(absRepo)
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

	if !strings.HasPrefix(realPath, realRepo+string(filepath.Separator)) && realPath != realRepo {
		return "", domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("path escapes repository: %s", path))
	}
	return absPath, nil
}

// Create creates a new artifact, validates it, writes the file, commits to Git,
// and emits an artifact_created event.
func (s *Service) Create(ctx context.Context, path, content string) (*domain.Artifact, error) {
	log := observe.Logger(ctx)

	// Validate path stays within repo
	fullPath, err := s.safePath(path)
	if err != nil {
		return nil, err
	}

	// Check for duplicate path
	if _, err := os.Stat(fullPath); err == nil {
		return nil, domain.NewError(domain.ErrAlreadyExists,
			fmt.Sprintf("artifact already exists at path: %s", path))
	}

	// Parse and validate
	artifact, err := Parse(path, []byte(content))
	if err != nil {
		return nil, domain.NewError(domain.ErrValidationFailed, err.Error())
	}

	result := Validate(artifact)
	if result.Status == "failed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"artifact validation failed", result.Errors)
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

	// Stage and commit
	trailers := observe.TrailersFromContext(ctx, "artifact.create")

	commitResult, err := s.stageAndCommit(ctx, path, git.CommitOpts{
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
		_ = gitReset(ctx, s.repo, path)
		return nil, err
	}

	log.Info("artifact created",
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	// Emit event
	s.emitEvent(ctx, domain.EventArtifactCreated, artifact, commitResult.SHA)

	return artifact, nil
}

// Read reads an artifact from Git at the specified ref (or HEAD if empty).
func (s *Service) Read(ctx context.Context, path, ref string) (*domain.Artifact, error) {
	if ref == "" {
		ref = "HEAD"
	}

	content, err := s.git.ReadFile(ctx, ref, path)
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
func (s *Service) Update(ctx context.Context, path, content string) (*domain.Artifact, error) {
	log := observe.Logger(ctx)

	// Validate path stays within repo
	fullPath, err := s.safePath(path)
	if err != nil {
		return nil, err
	}

	// Verify artifact exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return nil, domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("artifact not found: %s", path))
	}

	// Parse and validate new content
	artifact, err := Parse(path, []byte(content))
	if err != nil {
		return nil, domain.NewError(domain.ErrValidationFailed, err.Error())
	}

	result := Validate(artifact)
	if result.Status == "failed" {
		return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed,
			"artifact validation failed", result.Errors)
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

	// Stage and commit
	trailers := observe.TrailersFromContext(ctx, "artifact.update")

	commitResult, err := s.stageAndCommit(ctx, path, git.CommitOpts{
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

	log.Info("artifact updated",
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	// Emit event
	s.emitEvent(ctx, domain.EventArtifactUpdated, artifact, commitResult.SHA)

	return artifact, nil
}

// List scans the repository for all artifacts.
func (s *Service) List(ctx context.Context, ref string) ([]*domain.Artifact, error) {
	if ref == "" {
		ref = "HEAD"
	}

	files, err := s.git.ListFiles(ctx, ref, "*.md")
	if err != nil {
		return nil, domain.NewError(domain.ErrGit, err.Error())
	}

	var artifacts []*domain.Artifact
	for _, file := range files {
		content, err := s.git.ReadFile(ctx, ref, file)
		if err != nil {
			continue // skip unreadable files
		}

		if !IsArtifact(content) {
			continue
		}

		a, err := Parse(file, content)
		if err != nil {
			continue // skip unparseable artifacts
		}

		artifacts = append(artifacts, a)
	}

	return artifacts, nil
}

// stageAndCommit stages a file and creates a scoped Git commit (only this file).
func (s *Service) stageAndCommit(ctx context.Context, path string, opts git.CommitOpts) (git.CommitResult, error) {
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
	if err := gitAdd(ctx, s.repo, path); err != nil {
		return git.CommitResult{}, domain.NewError(domain.ErrGit,
			fmt.Sprintf("stage file %s: %v", path, err))
	}

	// Commit only this specific file (scoped via pathspec)
	sha, err := gitCommitPath(ctx, s.repo, path, msg, opts.Author)
	if err != nil {
		// Unstage the file to leave a clean index on failure
		_ = gitReset(ctx, s.repo, path)
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

// gitAdd stages a file using the git CLI directly.
// Uses -- separator and GIT_LITERAL_PATHSPECS to prevent path injection.
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
