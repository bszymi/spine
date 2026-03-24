package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/workflow"
)

// GitWorkflowLoader loads workflow definitions from Git using the GitClient.
type GitWorkflowLoader struct {
	gitClient git.GitClient
}

// NewGitWorkflowLoader creates a WorkflowLoader backed by Git file reads.
func NewGitWorkflowLoader(gitClient git.GitClient) *GitWorkflowLoader {
	return &GitWorkflowLoader{gitClient: gitClient}
}

func (l *GitWorkflowLoader) LoadWorkflow(ctx context.Context, path, ref string) (*domain.WorkflowDefinition, error) {
	content, err := l.gitClient.ReadFile(ctx, ref, path)
	if err != nil {
		return nil, fmt.Errorf("read workflow from git: %w", err)
	}
	return workflow.Parse(path, content)
}
