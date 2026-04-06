package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// ExecutionCandidateFilter defines parameters for querying execution candidates.
type ExecutionCandidateFilter struct {
	ActorType      string // filter by allowed actor type (empty = all)
	Skills         []string // filter by required skills the actor must have (empty = all)
	IncludeBlocked bool   // include blocked tasks (default: false)
}

// ExecutionCandidate represents a task that is potentially ready for execution.
type ExecutionCandidate struct {
	TaskPath        string   `json:"task_path"`
	TaskID          string   `json:"task_id"`
	Title           string   `json:"title"`
	Status          string   `json:"status"`
	RequiredSkills  []string `json:"required_skills,omitempty"`
	Blocked         bool     `json:"blocked"`
	BlockedBy       []string `json:"blocked_by,omitempty"`
	ResolvedBlockers []string `json:"resolved_blockers,omitempty"`
}

// FindExecutionCandidates returns tasks that are ready for execution based on
// the provided filter. Uses the projection store to find task artifacts and
// the blocking store to check dependency status.
func (o *Orchestrator) FindExecutionCandidates(ctx context.Context, filter ExecutionCandidateFilter) ([]ExecutionCandidate, error) {
	if o.blocking == nil {
		return nil, fmt.Errorf("execution candidate discovery requires blocking store")
	}

	// Query all tasks in actionable status from projections.
	result, err := o.blocking.(candidateQuerier).QueryArtifacts(ctx, store.ArtifactQuery{
		Type:   string(domain.ArtifactTypeTask),
		Status: string(domain.StatusPending),
	})
	if err != nil {
		return nil, fmt.Errorf("query task artifacts: %w", err)
	}

	var candidates []ExecutionCandidate
	for _, proj := range result.Items {
		candidate := ExecutionCandidate{
			TaskPath: proj.ArtifactPath,
			TaskID:   proj.ArtifactID,
			Title:    proj.Title,
			Status:   proj.Status,
		}

		// Extract required skills from metadata if available.
		candidate.RequiredSkills = extractRequiredSkills(proj.Metadata)

		// Check blocking status.
		blockResult, err := o.IsBlocked(ctx, proj.ArtifactPath)
		if err != nil {
			// If blocking check fails, mark as blocked (safe default).
			candidate.Blocked = true
		} else {
			candidate.Blocked = blockResult.Blocked
			candidate.BlockedBy = blockResult.BlockedBy
			candidate.ResolvedBlockers = blockResult.Resolved
		}

		// Apply filters.
		if candidate.Blocked && !filter.IncludeBlocked {
			continue
		}

		candidates = append(candidates, candidate)
	}

	return candidates, nil
}

// candidateQuerier extends BlockingStore with artifact query capability.
type candidateQuerier interface {
	BlockingStore
	QueryArtifacts(ctx context.Context, query store.ArtifactQuery) (*store.ArtifactQueryResult, error)
}

// extractRequiredSkills attempts to extract required_skills from artifact metadata JSONB.
func extractRequiredSkills(metadata []byte) []string {
	if len(metadata) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return nil
	}
	skills, ok := m["required_skills"]
	if !ok {
		return nil
	}
	arr, ok := skills.([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
