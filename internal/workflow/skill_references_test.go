package workflow_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workflow"
)

type fakeRefStore struct {
	projections []store.WorkflowProjection
	err         error
}

func (f *fakeRefStore) ListActiveWorkflowProjections(_ context.Context) ([]store.WorkflowProjection, error) {
	return f.projections, f.err
}

func makeWorkflowDef(skills ...string) []byte {
	wf := domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{
				ID:   "execute",
				Name: "Execute",
				Execution: &domain.ExecutionConfig{
					RequiredSkills: skills,
				},
			},
		},
	}
	data, _ := json.Marshal(wf)
	return data
}

func TestFindWorkflowsReferencingSkill_Found(t *testing.T) {
	st := &fakeRefStore{
		projections: []store.WorkflowProjection{
			{WorkflowID: "wf-1", WorkflowPath: "workflows/task-default.yaml", Definition: makeWorkflowDef("execution", "review")},
			{WorkflowID: "wf-2", WorkflowPath: "workflows/adr.yaml", Definition: makeWorkflowDef("planning")},
		},
	}

	refs, err := workflow.FindWorkflowsReferencingSkill(context.Background(), "execution", st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 reference, got %d", len(refs))
	}
	if refs[0].WorkflowID != "wf-1" {
		t.Errorf("expected wf-1, got %s", refs[0].WorkflowID)
	}
}

func TestFindWorkflowsReferencingSkill_NotFound(t *testing.T) {
	st := &fakeRefStore{
		projections: []store.WorkflowProjection{
			{WorkflowID: "wf-1", WorkflowPath: "workflows/task-default.yaml", Definition: makeWorkflowDef("execution")},
		},
	}

	refs, err := workflow.FindWorkflowsReferencingSkill(context.Background(), "nonexistent", st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 references, got %d", len(refs))
	}
}

func TestFindWorkflowsReferencingSkill_MultipleWorkflows(t *testing.T) {
	st := &fakeRefStore{
		projections: []store.WorkflowProjection{
			{WorkflowID: "wf-1", WorkflowPath: "workflows/a.yaml", Definition: makeWorkflowDef("review")},
			{WorkflowID: "wf-2", WorkflowPath: "workflows/b.yaml", Definition: makeWorkflowDef("review", "planning")},
			{WorkflowID: "wf-3", WorkflowPath: "workflows/c.yaml", Definition: makeWorkflowDef("execution")},
		},
	}

	refs, err := workflow.FindWorkflowsReferencingSkill(context.Background(), "review", st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("expected 2 references, got %d", len(refs))
	}
}

func TestFindWorkflowsReferencingSkill_EmptyDefinition(t *testing.T) {
	st := &fakeRefStore{
		projections: []store.WorkflowProjection{
			{WorkflowID: "wf-1", WorkflowPath: "workflows/empty.yaml", Definition: nil},
		},
	}

	refs, err := workflow.FindWorkflowsReferencingSkill(context.Background(), "anything", st)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 references, got %d", len(refs))
	}
}
