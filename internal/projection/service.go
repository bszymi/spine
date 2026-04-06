package projection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
	"gopkg.in/yaml.v3"
)

// Service implements the Projection Service — syncs Git state to PostgreSQL.
type Service struct {
	git          git.GitClient
	store        store.Store
	events       event.EventRouter
	pollInterval time.Duration
}

// NewService creates a new Projection Service.
func NewService(gitClient git.GitClient, s store.Store, events event.EventRouter, pollInterval time.Duration) *Service {
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	return &Service{
		git:          gitClient,
		store:        s,
		events:       events,
		pollInterval: pollInterval,
	}
}

// FullRebuild scans the entire repository at HEAD and rebuilds all projections.
// Per Data Model §4.1.
func (s *Service) FullRebuild(ctx context.Context) error {
	log := observe.Logger(ctx)
	log.Info("starting full projection rebuild")

	head, err := s.git.Head(ctx)
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}

	// Mark as rebuilding but keep previous commit until rebuild succeeds
	existingState, _ := s.store.GetSyncState(ctx)
	prevCommit := ""
	if existingState != nil {
		prevCommit = existingState.LastSyncedCommit
	}
	_ = s.store.UpdateSyncState(ctx, &store.SyncState{
		LastSyncedCommit: prevCommit,
		Status:           "rebuilding",
	})

	// Clear existing projections
	if err := s.store.DeleteAllProjections(ctx); err != nil {
		return fmt.Errorf("delete projections: %w", err)
	}

	// Discover all artifacts
	result, err := artifact.DiscoverAll(ctx, s.git, head)
	if err != nil {
		return fmt.Errorf("discover artifacts: %w", err)
	}

	// Count discovery errors as projection failures
	var projErrors int
	projErrors += len(result.Errors)

	// Project artifacts
	for _, a := range result.Artifacts {
		if err := s.projectArtifact(ctx, a, head); err != nil {
			log.Warn("failed to project artifact",
				"path", a.Path,
				"error", err,
			)
			projErrors++
			continue
		}
	}

	// Project workflows
	for _, wfPath := range result.Workflows {
		if err := s.projectWorkflow(ctx, wfPath, head); err != nil {
			log.Warn("failed to project workflow",
				"path", wfPath,
				"error", err,
			)
			projErrors++
		}
	}

	// Update sync state — only mark idle if no errors
	if projErrors > 0 {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: head, // for rebuild, keep HEAD to allow retry at same commit
			Status:           "error",
			ErrorDetail:      fmt.Sprintf("%d projection errors during rebuild", projErrors),
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	} else {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: head,
			Status:           "idle",
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	}

	log.Info("full rebuild complete",
		"artifacts", len(result.Artifacts),
		"workflows", len(result.Workflows),
		"skipped", len(result.Skipped),
		"errors", len(result.Errors),
		"commit", head,
	)

	observe.GlobalMetrics.ProjectionSyncs.Inc()
	return nil
}

// IncrementalSync updates projections based on changes since the last sync.
// Per Data Model §4.2.
func (s *Service) IncrementalSync(ctx context.Context) error {
	log := observe.Logger(ctx)

	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}

	if state == nil {
		// No sync state — do a full rebuild
		return s.FullRebuild(ctx)
	}

	head, err := s.git.Head(ctx)
	if err != nil {
		return fmt.Errorf("get HEAD: %w", err)
	}

	// Retry if previous sync failed, even at the same commit
	if head == state.LastSyncedCommit && state.Status == "idle" {
		return nil // already up to date and healthy
	}

	// Update sync state to syncing
	_ = s.store.UpdateSyncState(ctx, &store.SyncState{
		LastSyncedCommit: state.LastSyncedCommit,
		Status:           "syncing",
	})

	changeset, err := artifact.DiscoverChanges(ctx, s.git, state.LastSyncedCommit, head)
	if err != nil {
		return fmt.Errorf("discover changes: %w", err)
	}

	var syncErrors int

	// Process created artifacts
	for _, a := range changeset.Created {
		if err := s.projectArtifact(ctx, a, head); err != nil {
			log.Warn("failed to project created artifact",
				"path", a.Path,
				"error", err,
			)
			syncErrors++
		}
	}

	// Process modified artifacts
	for _, a := range changeset.Modified {
		if err := s.projectArtifact(ctx, a, head); err != nil {
			log.Warn("failed to project modified artifact",
				"path", a.Path,
				"error", err,
			)
			syncErrors++
		}
	}

	// Process deleted artifacts
	for _, path := range changeset.Deleted {
		if err := s.store.DeleteArtifactProjection(ctx, path); err != nil {
			log.Warn("failed to delete projection",
				"path", path,
				"error", err,
			)
			syncErrors++
		}
		if err := s.store.DeleteArtifactLinks(ctx, path); err != nil {
			log.Warn("failed to delete links",
				"path", path,
				"error", err,
			)
			syncErrors++
		}
	}

	// Sync workflow changes
	diffs, _ := s.git.Diff(ctx, state.LastSyncedCommit, head)
	for _, diff := range diffs {
		if !artifact.IsWorkflowPath(diff.Path) && (diff.OldPath == "" || !artifact.IsWorkflowPath(diff.OldPath)) {
			continue
		}
		switch diff.Status {
		case "added", "modified":
			if err := s.projectWorkflow(ctx, diff.Path, head); err != nil {
				log.Warn("failed to project workflow", "path", diff.Path, "error", err)
				syncErrors++
			}
		case "deleted":
			if err := s.store.DeleteWorkflowProjection(ctx, diff.Path); err != nil {
				log.Warn("failed to delete workflow", "path", diff.Path, "error", err)
				syncErrors++
			}
		case "renamed":
			if diff.OldPath != "" {
				_ = s.store.DeleteWorkflowProjection(ctx, diff.OldPath)
			}
			if artifact.IsWorkflowPath(diff.Path) {
				if err := s.projectWorkflow(ctx, diff.Path, head); err != nil {
					log.Warn("failed to project renamed workflow", "path", diff.Path, "error", err)
					syncErrors++
				}
			}
		}
	}

	// Update sync state — only advance commit if no errors
	if syncErrors > 0 {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: state.LastSyncedCommit, // don't advance
			Status:           "error",
			ErrorDetail:      fmt.Sprintf("%d sync errors", syncErrors),
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	} else {
		if err := s.store.UpdateSyncState(ctx, &store.SyncState{
			LastSyncedCommit: head,
			Status:           "idle",
		}); err != nil {
			return fmt.Errorf("update sync state: %w", err)
		}
	}

	log.Info("incremental sync complete",
		"created", len(changeset.Created),
		"modified", len(changeset.Modified),
		"deleted", len(changeset.Deleted),
		"errors", syncErrors,
		"from", state.LastSyncedCommit[:8],
		"to", head[:8],
	)

	observe.GlobalMetrics.ProjectionSyncs.Inc()

	// Emit projection_synced event (if event router configured).
	if s.events == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"from_commit": state.LastSyncedCommit[:8],
		"to_commit":   head[:8],
		"created":     len(changeset.Created),
		"modified":    len(changeset.Modified),
		"deleted":     len(changeset.Deleted),
		"errors":      syncErrors,
	})
	if err := s.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("sync-%s", head[:12]),
		Type:      domain.EventProjectionSynced,
		Timestamp: time.Now(),
		Payload:   payload,
	}); err != nil {
		log.Warn("failed to emit projection_synced event", "error", err)
	}

	return nil
}

// StartSyncLoop runs incremental sync on a polling interval.
// Stops when the context is cancelled.
func (s *Service) StartSyncLoop(ctx context.Context) {
	log := observe.Logger(ctx)
	log.Info("starting projection sync loop", "interval", s.pollInterval)

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.IncrementalSync(ctx); err != nil {
				log.Error("incremental sync failed", "error", err)
			}
		case <-ctx.Done():
			log.Info("projection sync loop stopped")
			return
		}
	}
}

// RegisterEventHandlers subscribes to artifact events for real-time sync.
func (s *Service) RegisterEventHandlers(ctx context.Context) error {
	if s.events == nil {
		return nil
	}

	// React to artifact changes by triggering incremental sync
	handler := func(ctx context.Context, evt domain.Event) error {
		return s.IncrementalSync(ctx)
	}

	if err := s.events.Subscribe(ctx, domain.EventArtifactCreated, handler); err != nil {
		return err
	}
	return s.events.Subscribe(ctx, domain.EventArtifactUpdated, handler)
}

// projectArtifact upserts an artifact projection and its links.
func (s *Service) projectArtifact(ctx context.Context, a *domain.Artifact, commitSHA string) error {
	metadata, err := json.Marshal(a.Metadata)
	if err != nil {
		return fmt.Errorf("marshal artifact metadata: %w", err)
	}
	linksJSON, err := json.Marshal(a.Links)
	if err != nil {
		return fmt.Errorf("marshal artifact links: %w", err)
	}
	contentHash := hashContent(a.Content)

	proj := &store.ArtifactProjection{
		ArtifactPath: a.Path,
		ArtifactID:   a.ID,
		ArtifactType: string(a.Type),
		Title:        a.Title,
		Status:       string(a.Status),
		Metadata:     metadata,
		Content:      a.Content,
		Links:        linksJSON,
		SourceCommit: commitSHA,
		ContentHash:  contentHash,
	}

	if err := s.store.UpsertArtifactProjection(ctx, proj); err != nil {
		return fmt.Errorf("upsert artifact %s: %w", a.Path, err)
	}

	// Denormalize links
	var links []store.ArtifactLink
	for _, link := range a.Links {
		links = append(links, store.ArtifactLink{
			SourcePath: a.Path,
			TargetPath: link.Target,
			LinkType:   string(link.Type),
		})
	}

	if err := s.store.UpsertArtifactLinks(ctx, a.Path, links, commitSHA); err != nil {
		return fmt.Errorf("upsert links for %s: %w", a.Path, err)
	}

	// For Task artifacts, also upsert the execution projection.
	if a.Type == domain.ArtifactTypeTask {
		blocked, blockedBy := s.resolveBlockingStatus(ctx, a.Path, a.Links)
		execProj := &store.ExecutionProjection{
			TaskPath:         a.Path,
			TaskID:           a.ID,
			Title:            a.Title,
			Status:           string(a.Status),
			Blocked:          blocked,
			BlockedBy:        blockedBy,
			AssignmentStatus: "unassigned",
		}
		if err := s.store.UpsertExecutionProjection(ctx, execProj); err != nil {
			observe.Logger(ctx).Warn("failed to upsert execution projection", "path", a.Path, "error", err)
		}
	}

	return nil
}

// resolveBlockingStatus checks blocked_by links and returns blocking state.
func (s *Service) resolveBlockingStatus(ctx context.Context, taskPath string, links []domain.Link) (bool, []string) {
	var blockedBy []string
	for _, link := range links {
		if link.Type != domain.LinkTypeBlockedBy {
			continue
		}
		// Check if the blocker is in a terminal status.
		blocker, err := s.store.GetArtifactProjection(ctx, link.Target)
		if err != nil {
			// Can't find blocker — treat as blocking (safe default).
			blockedBy = append(blockedBy, link.Target)
			continue
		}
		if !isTerminalProjectionStatus(blocker.Status) {
			blockedBy = append(blockedBy, link.Target)
		}
	}
	return len(blockedBy) > 0, blockedBy
}

func isTerminalProjectionStatus(status string) bool {
	switch domain.ArtifactStatus(status) {
	case domain.StatusCompleted, domain.StatusCancelled, domain.StatusRejected,
		domain.StatusAbandoned, domain.StatusSuperseded:
		return true
	}
	return false
}

// projectWorkflow reads and projects a workflow YAML file.
func (s *Service) projectWorkflow(ctx context.Context, wfPath, commitSHA string) error {
	content, err := s.git.ReadFile(ctx, commitSHA, wfPath)
	if err != nil {
		return fmt.Errorf("read workflow %s: %w", wfPath, err)
	}

	// Parse minimal fields for projection
	var wf struct {
		ID        string   `yaml:"id" json:"id"`
		Name      string   `yaml:"name" json:"name"`
		Version   string   `yaml:"version" json:"version"`
		Status    string   `yaml:"status" json:"status"`
		AppliesTo []string `yaml:"applies_to" json:"applies_to"`
	}

	if err := yaml.Unmarshal(content, &wf); err != nil {
		return fmt.Errorf("parse workflow %s: %w", wfPath, err)
	}

	appliesTo, err := json.Marshal(wf.AppliesTo)
	if err != nil {
		return fmt.Errorf("marshal workflow applies_to: %w", err)
	}

	// Convert YAML to JSON for the JSONB definition column
	var rawDef any
	if err := yaml.Unmarshal(content, &rawDef); err != nil {
		return fmt.Errorf("parse workflow raw definition: %w", err)
	}
	definition, err := json.Marshal(rawDef)
	if err != nil {
		return fmt.Errorf("marshal workflow definition: %w", err)
	}

	proj := &store.WorkflowProjection{
		WorkflowPath: wfPath,
		WorkflowID:   wf.ID,
		Name:         wf.Name,
		Version:      wf.Version,
		Status:       wf.Status,
		AppliesTo:    appliesTo,
		Definition:   definition,
		SourceCommit: commitSHA,
	}

	return s.store.UpsertWorkflowProjection(ctx, proj)
}

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}
