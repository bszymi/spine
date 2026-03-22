package workflow

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
)

// BindingResult contains the resolved workflow and its pinned version.
type BindingResult struct {
	Workflow     *domain.WorkflowDefinition
	CommitSHA    string // Git SHA of the workflow file at resolution time
	VersionLabel string // semantic version from the workflow definition
}

// WorkflowProvider provides access to workflow definitions for binding resolution.
type WorkflowProvider interface {
	// ListActiveWorkflows returns all workflow definitions with status Active.
	ListActiveWorkflows(ctx context.Context) ([]*domain.WorkflowDefinition, error)
}

// ResolveBinding resolves the governing workflow for an artifact.
// Per Task-Workflow Binding §4: resolution algorithm.
//
// The algorithm:
//  1. Read artifact type and work_type
//  2. Find all Active workflows where applies_to includes the type
//  3. If work_type is specified, filter to matching work_type selectors
//  4. If exactly one matches, use it
//  5. If zero match, return workflow_not_found error
//  6. If multiple match, return conflict error
func ResolveBinding(ctx context.Context, provider WorkflowProvider, gitClient git.GitClient, artifactType, workType string) (*BindingResult, error) {
	workflows, err := provider.ListActiveWorkflows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}

	// Step 3: Find candidates matching the artifact type
	// Separate general matches from work_type-specific matches
	var generalCandidates []*domain.WorkflowDefinition
	var specificCandidates []*domain.WorkflowDefinition

	for _, wf := range workflows {
		if matchesTypeGeneral(wf, artifactType) {
			generalCandidates = append(generalCandidates, wf)
		}
	}

	// Step 4: If work_type specified, try specific match first
	candidates := generalCandidates
	if workType != "" {
		// In v0.x, work_type filtering is not yet implemented at the
		// applies_to clause level (needs structured applies_to support).
		// For now, all type-matching workflows are candidates.
		// When structured applies_to is implemented, specificCandidates
		// will be filtered here and take precedence over generalCandidates.
		_ = specificCandidates
		candidates = generalCandidates
	}

	// Step 5: No match
	if len(candidates) == 0 {
		return nil, domain.NewError(domain.ErrWorkflowNotFound,
			fmt.Sprintf("no active workflow for type=%s work_type=%s", artifactType, workType))
	}

	// Step 6: Ambiguous — multiple matches
	if len(candidates) > 1 {
		ids := make([]string, len(candidates))
		for i, c := range candidates {
			ids[i] = c.ID
		}
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("ambiguous: %d workflows match type=%s work_type=%s: %v", len(candidates), artifactType, workType, ids))
	}

	// Step 7-9: Single match — pin to Git SHA
	resolved := candidates[0]

	// Use the workflow's existing CommitSHA if set (from projection/provider),
	// otherwise fall back to HEAD
	commitSHA := resolved.CommitSHA
	if commitSHA == "" && gitClient != nil {
		sha, err := gitClient.Head(ctx)
		if err != nil {
			return nil, fmt.Errorf("get HEAD for workflow pinning: %w", err)
		}
		commitSHA = sha
	}

	return &BindingResult{
		Workflow:     resolved,
		CommitSHA:    commitSHA,
		VersionLabel: resolved.Version,
	}, nil
}

// matchesTypeGeneral checks if a workflow's applies_to clause matches the artifact type
// (general match, ignoring work_type).
func matchesTypeGeneral(wf *domain.WorkflowDefinition, artifactType string) bool {
	for _, at := range wf.AppliesTo {
		if at == artifactType {
			return true
		}
	}
	return false
}
