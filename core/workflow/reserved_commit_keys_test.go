package workflow_test

// INIT-023 TASK-005 AC 5 (parser): a workflow outcome may not declare a
// commit key in the engine's reserved "spine." namespace, in any letter
// case. The engine strips such keys anyway (the check here is for the
// author); see core/engine commitmeta.go.

import (
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

func reservedKeyWorkflow(commit map[string]string) *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf-reserved", Name: "Reserved", Version: "1.0", Status: domain.WorkflowStatusActive,
		AppliesTo: []string{"Task"}, EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "rework", Name: "Rework", NextStep: "execute"},
					{ID: "accepted", Name: "Accepted", NextStep: "end", Commit: commit},
				}},
		},
	}
}

func TestValidate_ReservedCommitKeys(t *testing.T) {
	planted := reservedKeyWorkflow(map[string]string{
		"status": "Completed", "spine.bound_sha": strings.Repeat("e", 40), "Spine.Bound_By": "x",
	})
	res := workflow.Validate(planted)
	var found *domain.ValidationError
	for i := range res.Errors {
		if res.Errors[i].Field == "steps[1].outcomes[1].commit" {
			found = &res.Errors[i]
		}
	}
	if found == nil {
		t.Fatalf("Validate errors %+v lack steps[1].outcomes[1].commit", res.Errors)
	}
	if !strings.Contains(found.Message, "Spine.Bound_By") || !strings.Contains(found.Message, "spine.bound_sha") {
		t.Fatalf("message %q does not name the reserved keys", found.Message)
	}
	clean := workflow.Validate(reservedKeyWorkflow(map[string]string{"status": "Completed", "spinex": "ok"}))
	for _, e := range clean.Errors {
		if strings.HasSuffix(e.Field, ".commit") {
			t.Fatalf("a non-reserved commit key was refused: %+v", e)
		}
	}
}
