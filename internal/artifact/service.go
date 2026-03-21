package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Create creates a new artifact, validates it, writes the file, commits to Git,
// and emits an artifact_created event.
func (s *Service) Create(ctx context.Context, path, content string) (*domain.Artifact, error) {
	log := observe.Logger(ctx)

	// Check for duplicate path
	fullPath := filepath.Join(s.repo, path)
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
		// Clean up the file on commit failure
		os.Remove(fullPath)
		return nil, err
	}

	log.Info("artifact created",
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	// Emit event
	s.emitEvent(ctx, domain.EventArtifactCreated, path, commitResult.SHA)

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

	// Verify artifact exists
	fullPath := filepath.Join(s.repo, path)
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
		return nil, err
	}

	log.Info("artifact updated",
		"path", path,
		"type", artifact.Type,
		"commit", commitResult.SHA,
	)

	// Emit event
	s.emitEvent(ctx, domain.EventArtifactUpdated, path, commitResult.SHA)

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

// stageAndCommit stages a file and creates a Git commit.
func (s *Service) stageAndCommit(ctx context.Context, path string, opts git.CommitOpts) (git.CommitResult, error) {
	// Stage the file using git add via the CLI
	stageClient, ok := s.git.(*git.CLIClient)
	if !ok {
		return git.CommitResult{}, domain.NewError(domain.ErrInternal, "git client does not support staging")
	}

	// We need to run git add directly
	_ = stageClient // use the same client for commit
	// For now, stage via exec since GitClient interface doesn't have Add
	if err := gitAdd(ctx, s.repo, path); err != nil {
		return git.CommitResult{}, domain.NewError(domain.ErrGit,
			fmt.Sprintf("stage file %s: %v", path, err))
	}

	return s.git.Commit(ctx, opts)
}

// gitAdd stages a file using the git CLI directly.
func gitAdd(ctx context.Context, repoDir, path string) error {
	cmd := execCommand(ctx, "git", "add", path)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add %s: %s", path, string(out))
	}
	return nil
}

// execCommand is a variable for testing.
var execCommand = execCommandDefault

func execCommandDefault(ctx context.Context, name string, args ...string) *execCmd {
	return newExecCmd(ctx, name, args...)
}

// emitEvent publishes a domain event for artifact changes.
func (s *Service) emitEvent(ctx context.Context, eventType domain.EventType, artifactPath, commitSHA string) {
	if s.events == nil {
		return
	}

	traceID, _ := observe.GenerateTraceID()
	evt := domain.Event{
		EventID:      traceID,
		Type:         eventType,
		Timestamp:    time.Now(),
		ActorID:      observe.ActorID(ctx),
		ArtifactPath: artifactPath,
		TraceID:      observe.TraceID(ctx),
		Payload:      mustJSON(map[string]string{"commit_sha": commitSHA}),
	}

	// Fire and forget — event delivery is async
	s.events.Emit(ctx, evt)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
