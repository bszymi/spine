package engine

import (
	"context"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/workflow"
)

// BindingResolver adapts workflow.ResolveBinding (a package function) into the
// WorkflowResolver interface consumed by the orchestrator.
type BindingResolver struct {
	provider  workflow.WorkflowProvider
	gitClient git.GitClient
}

// NewBindingResolver creates a WorkflowResolver backed by workflow.ResolveBinding.
func NewBindingResolver(provider workflow.WorkflowProvider, gitClient git.GitClient) *BindingResolver {
	return &BindingResolver{provider: provider, gitClient: gitClient}
}

func (r *BindingResolver) ResolveWorkflow(ctx context.Context, artifactType, workType string) (*workflow.BindingResult, error) {
	return workflow.ResolveBinding(ctx, r.provider, r.gitClient, artifactType, workType)
}

func (r *BindingResolver) ResolveWorkflowForMode(ctx context.Context, artifactType, workType, mode string) (*workflow.BindingResult, error) {
	return workflow.ResolveBindingWithMode(ctx, r.provider, r.gitClient, artifactType, workType, mode)
}
