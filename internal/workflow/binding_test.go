package workflow_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
	"github.com/bszymi/spine/internal/workflow"
)

// mockProvider implements WorkflowProvider for testing.
type mockProvider struct {
	workflows []*domain.WorkflowDefinition
}

func (m *mockProvider) ListActiveWorkflows(ctx context.Context) ([]*domain.WorkflowDefinition, error) {
	return m.workflows, nil
}

func activeWorkflow(id string, appliesTo ...string) *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID:          id,
		Name:        id,
		Version:     "1.0",
		Status:      domain.WorkflowStatusActive,
		Description: "test",
		AppliesTo:   appliesTo,
		EntryStep:   "s1",
		Steps: []domain.StepDefinition{{
			ID: "s1", Name: "Step", Type: domain.StepTypeManual,
			Outcomes: []domain.OutcomeDefinition{{ID: "o1", Name: "Done", NextStep: "end"}},
		}},
		Path:      "workflows/" + id + ".yaml",
		CommitSHA: "abcdef1234567890abcdef1234567890abcdef12", // mock SHA for tests without gitClient
	}
}

func TestResolveBindingSingleMatch(t *testing.T) {
	provider := &mockProvider{
		workflows: []*domain.WorkflowDefinition{
			activeWorkflow("task-exec", "Task"),
		},
	}

	result, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", "")
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	if result.Workflow.ID != "task-exec" {
		t.Errorf("expected task-exec, got %s", result.Workflow.ID)
	}
	if result.VersionLabel != "1.0" {
		t.Errorf("expected version 1.0, got %s", result.VersionLabel)
	}
}

func TestResolveBindingNoMatch(t *testing.T) {
	provider := &mockProvider{
		workflows: []*domain.WorkflowDefinition{
			activeWorkflow("task-exec", "Task"),
		},
	}

	_, err := workflow.ResolveBinding(context.Background(), provider, nil, "Epic", "")
	if err == nil {
		t.Fatal("expected error for no matching workflow")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrWorkflowNotFound {
		t.Errorf("expected workflow_not_found, got %s", spineErr.Code)
	}
}

func TestResolveBindingAmbiguous(t *testing.T) {
	provider := &mockProvider{
		workflows: []*domain.WorkflowDefinition{
			activeWorkflow("wf-a", "Task"),
			activeWorkflow("wf-b", "Task"),
		},
	}

	_, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", "")
	if err == nil {
		t.Fatal("expected error for ambiguous binding")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrConflict {
		t.Errorf("expected conflict, got %s", spineErr.Code)
	}
}

func TestResolveBindingMultipleTypes(t *testing.T) {
	provider := &mockProvider{
		workflows: []*domain.WorkflowDefinition{
			activeWorkflow("task-exec", "Task"),
			activeWorkflow("epic-exec", "Epic"),
		},
	}

	result, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", "")
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	if result.Workflow.ID != "task-exec" {
		t.Errorf("expected task-exec, got %s", result.Workflow.ID)
	}
}

func TestResolveBindingWithWorkType(t *testing.T) {
	provider := &mockProvider{
		workflows: []*domain.WorkflowDefinition{
			activeWorkflow("task-general", "Task"),
		},
	}

	// work_type specified but no specific workflow — should fall back to general
	result, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", "spike")
	if err != nil {
		t.Fatalf("ResolveBinding with work_type fallback: %v", err)
	}
	if result.Workflow.ID != "task-general" {
		t.Errorf("expected task-general, got %s", result.Workflow.ID)
	}
}

func TestResolveBindingWorkTypeNoFallback(t *testing.T) {
	provider := &mockProvider{
		workflows: []*domain.WorkflowDefinition{
			activeWorkflow("epic-exec", "Epic"),
		},
	}

	// No matching type at all
	_, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", "spike")
	if err == nil {
		t.Fatal("expected error when no workflow matches type at all")
	}
}

func TestResolveBindingEmptyWorkflows(t *testing.T) {
	provider := &mockProvider{
		workflows: nil,
	}

	_, err := workflow.ResolveBinding(context.Background(), provider, nil, "Task", "")
	if err == nil {
		t.Fatal("expected error for empty workflow list")
	}
}

func TestResolveBindingWithGitPinning(t *testing.T) {
	provider := &mockProvider{
		workflows: []*domain.WorkflowDefinition{
			activeWorkflow("task-exec", "Task"),
		},
	}

	// Use a real temp git repo for SHA pinning
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)

	result, err := workflow.ResolveBinding(context.Background(), provider, client, "Task", "")
	if err != nil {
		t.Fatalf("ResolveBinding with git: %v", err)
	}
	if result.CommitSHA == "" {
		t.Error("expected non-empty commit SHA")
	}
	if len(result.CommitSHA) != 40 {
		t.Errorf("expected 40-char SHA, got %d chars", len(result.CommitSHA))
	}
}
