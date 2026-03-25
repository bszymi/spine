package workflow_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

type mockProjectionStore struct {
	projections []workflow.WorkflowProjection
	err         error
}

func (s *mockProjectionStore) ListActiveWorkflowProjections(_ context.Context) ([]workflow.WorkflowProjection, error) {
	return s.projections, s.err
}

func TestProjectionWorkflowProvider_ListActiveWorkflows(t *testing.T) {
	wfDef := domain.WorkflowDefinition{
		ID:        "task-default",
		Name:      "Default Task",
		Version:   "1.0",
		Status:    domain.WorkflowStatusActive,
		AppliesTo: []string{"Task"},
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}},
		},
	}
	defJSON, _ := json.Marshal(wfDef)

	store := &mockProjectionStore{
		projections: []workflow.WorkflowProjection{
			{
				WorkflowPath: "workflows/task-default.yaml",
				WorkflowID:   "task-default",
				Definition:   defJSON,
				SourceCommit: "abc123",
			},
		},
	}

	provider := workflow.NewProjectionWorkflowProvider(store)
	workflows, err := provider.ListActiveWorkflows(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(workflows))
	}
	if workflows[0].ID != "task-default" {
		t.Errorf("expected task-default, got %s", workflows[0].ID)
	}
	if workflows[0].Path != "workflows/task-default.yaml" {
		t.Errorf("expected path workflows/task-default.yaml, got %s", workflows[0].Path)
	}
	if workflows[0].CommitSHA != "abc123" {
		t.Errorf("expected commit abc123, got %s", workflows[0].CommitSHA)
	}
}

func TestProjectionWorkflowProvider_SkipsInvalid(t *testing.T) {
	store := &mockProjectionStore{
		projections: []workflow.WorkflowProjection{
			{
				WorkflowPath: "workflows/bad.yaml",
				Definition:   []byte("not json"),
			},
		},
	}

	provider := workflow.NewProjectionWorkflowProvider(store)
	workflows, err := provider.ListActiveWorkflows(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(workflows) != 0 {
		t.Errorf("expected 0 workflows (invalid skipped), got %d", len(workflows))
	}
}
