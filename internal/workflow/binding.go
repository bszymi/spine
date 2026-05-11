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
	return ResolveBindingWithMode(ctx, provider, gitClient, artifactType, workType, "execution")
}

// ResolveBindingWithMode resolves the governing workflow filtered by mode.
// Per ADR-006 §4: planning runs resolve to mode: creation, standard runs to mode: execution.
func ResolveBindingWithMode(ctx context.Context, provider WorkflowProvider, gitClient git.GitClient, artifactType, workType, mode string) (*BindingResult, error) {
	workflows, err := provider.ListActiveWorkflows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}

	// Step 3: Find candidates matching the artifact type and mode.
	// work_type-level filtering is not yet implemented at the
	// applies_to clause (needs structured applies_to support); the
	// workType parameter still flows into the error messages below
	// so the failure surface stays specific.
	var candidates []*domain.WorkflowDefinition

	for _, wf := range workflows {
		if matchesTypeGeneral(wf, artifactType) && matchesMode(wf, mode) {
			candidates = append(candidates, wf)
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

	// Pin to the workflow's source commit.
	// Prefer the CommitSHA already set by the provider (from projection/discovery),
	// fall back to HEAD if not set. In production, gitClient is always provided.
	commitSHA := resolved.CommitSHA
	if commitSHA == "" && gitClient != nil {
		sha, err := gitClient.Head(ctx)
		if err != nil {
			return nil, fmt.Errorf("get HEAD for workflow pinning: %w", err)
		}
		commitSHA = sha
	}
	if commitSHA == "" {
		return nil, domain.NewError(domain.ErrInternal,
			"cannot pin workflow: no commit SHA available")
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

// matchesMode checks if a workflow's mode matches the requested mode.
// Empty mode on the workflow defaults to "execution" (per parser).
func matchesMode(wf *domain.WorkflowDefinition, mode string) bool {
	wfMode := wf.Mode
	if wfMode == "" {
		wfMode = "execution"
	}
	return wfMode == mode
}
