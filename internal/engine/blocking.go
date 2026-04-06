package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// BlockingStore provides projection queries needed for dependency blocking detection.
type BlockingStore interface {
	QueryArtifactLinks(ctx context.Context, sourcePath string) ([]store.ArtifactLink, error)
	QueryArtifactLinksByTarget(ctx context.Context, targetPath string) ([]store.ArtifactLink, error)
	GetArtifactProjection(ctx context.Context, artifactPath string) (*store.ArtifactProjection, error)
}

// BlockingResult describes whether a task is blocked and by which tasks.
type BlockingResult struct {
	Blocked   bool
	BlockedBy []string // paths of blocking tasks that are not yet terminal
	Resolved  []string // paths of blocking tasks that are terminal
	HasCycle  bool     // true if circular dependency detected
	CyclePath []string // the cycle if detected
}

// IsBlocked checks whether a task is blocked by resolving its blocked_by links
// from the projection store and checking the status of each blocker.
func (o *Orchestrator) IsBlocked(ctx context.Context, taskPath string) (*BlockingResult, error) {
	if o.blocking == nil {
		return &BlockingResult{Blocked: false}, nil
	}
	return o.isBlockedWithVisited(ctx, taskPath, nil)
}

func (o *Orchestrator) isBlockedWithVisited(ctx context.Context, taskPath string, visited []string) (*BlockingResult, error) {
	// Circular dependency detection
	for _, v := range visited {
		if v == taskPath {
			return &BlockingResult{
				Blocked:   true,
				HasCycle:  true,
				CyclePath: append(visited, taskPath),
			}, nil
		}
	}

	links, err := o.blocking.QueryArtifactLinks(ctx, taskPath)
	if err != nil {
		return nil, fmt.Errorf("query links for %s: %w", taskPath, err)
	}

	result := &BlockingResult{}
	for _, link := range links {
		if link.LinkType != "blocked_by" {
			continue
		}
		targetPath := strings.TrimPrefix(link.TargetPath, "/")
		proj, err := o.blocking.GetArtifactProjection(ctx, targetPath)
		if err != nil {
			// If we can't find the blocker, treat it as blocking (safe default)
			result.BlockedBy = append(result.BlockedBy, link.TargetPath)
			continue
		}

		if isTerminalArtifactStatus(proj.Status) {
			result.Resolved = append(result.Resolved, link.TargetPath)
		} else {
			result.BlockedBy = append(result.BlockedBy, link.TargetPath)
		}
	}

	result.Blocked = len(result.BlockedBy) > 0
	return result, nil
}

// CheckAndEmitBlockingTransition checks if tasks blocked by the completed task
// have become unblocked, and emits an event for each. Call this when a task
// completes to re-evaluate its dependents.
func (o *Orchestrator) CheckAndEmitBlockingTransition(ctx context.Context, completedTaskPath string) {
	if o.blocking == nil {
		return
	}
	log := observe.Logger(ctx)

	// Find tasks that are blocked by the completed task
	allLinks, err := o.blocking.QueryArtifactLinksByTarget(ctx, completedTaskPath)
	if err != nil {
		log.Warn("failed to query dependents for blocking transition", "task", completedTaskPath, "error", err)
		return
	}

	for _, link := range allLinks {
		if link.LinkType != "blocked_by" {
			continue
		}
		result, err := o.IsBlocked(ctx, link.SourcePath)
		if err != nil {
			log.Warn("failed to check blocking status", "task", link.SourcePath, "error", err)
			continue
		}

		if !result.Blocked {
			payload, _ := json.Marshal(map[string]any{
				"task_path":   link.SourcePath,
				"resolved_by": completedTaskPath,
			})
			o.emitEvent(ctx, domain.EventTaskUnblocked, "", "",
				fmt.Sprintf("evt-unblocked-%s", link.SourcePath), payload)
			log.Info("task unblocked", "task", link.SourcePath, "resolved_by", completedTaskPath)
		}
	}
}

// updateExecutionProjection updates a task's execution projection if the store supports it.
func (o *Orchestrator) updateExecutionProjection(ctx context.Context, taskPath string, update func(proj *store.ExecutionProjection)) {
	if o.blocking == nil {
		return
	}
	type execProjStore interface {
		GetExecutionProjection(ctx context.Context, taskPath string) (*store.ExecutionProjection, error)
		UpsertExecutionProjection(ctx context.Context, proj *store.ExecutionProjection) error
	}
	eps, ok := o.blocking.(execProjStore)
	if !ok {
		return
	}
	proj, err := eps.GetExecutionProjection(ctx, taskPath)
	if err != nil {
		return // projection doesn't exist yet — will be created by projection sync
	}
	update(proj)
	if err := eps.UpsertExecutionProjection(ctx, proj); err != nil {
		observe.Logger(ctx).Warn("failed to update execution projection", "task", taskPath, "error", err)
	}
}

// isTerminalArtifactStatus checks if an artifact status represents a terminal state.
func isTerminalArtifactStatus(status string) bool {
	switch domain.ArtifactStatus(status) {
	case domain.StatusCompleted, domain.StatusCancelled, domain.StatusRejected,
		domain.StatusAbandoned, domain.StatusSuperseded:
		return true
	}
	return false
}
