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
	var candidates []*domain.WorkflowDefinition
	for _, wf := range workflows {
		if matchesType(wf, artifactType, workType) {
			candidates = append(candidates, wf)
		}
	}

	// Step 4: If work_type specified but no specific match, fall back to general
	if workType != "" && len(candidates) == 0 {
		for _, wf := range workflows {
			if matchesType(wf, artifactType, "") {
				candidates = append(candidates, wf)
			}
		}
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

	commitSHA := ""
	if gitClient != nil && resolved.Path != "" {
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

// matchesType checks if a workflow's applies_to clause matches the given type and work_type.
func matchesType(wf *domain.WorkflowDefinition, artifactType, workType string) bool {
	for _, at := range wf.AppliesTo {
		// Simple string form: "Task"
		if at == artifactType && workType == "" {
			return true
		}
		if at == artifactType && workType != "" {
			// General match — only if no work_type-specific workflows exist
			// This is handled by the fallback in ResolveBinding
			return true
		}
	}
	return false
}
