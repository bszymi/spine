package gateway

// INIT-023 TASK-014 internal pins: the canonical workflow-path guard
// (AC 17) and authorizeRunBranchWrite's dev-mode rows (AC 8). A nil actor
// reaches a handler only in dev mode, where authorize admits everything;
// there is no identity to bind, so the binding is skipped — but the
// review/internal freeze and the fail-closed step lookup still apply.

import (
	"context"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
)

func TestIsWorkflowTarget_Canonical(t *testing.T) {
	for p, want := range map[string]bool{
		"workflows/x.yaml":               true,
		"/workflows/x.yaml":              true,
		"./workflows/x.yaml":             true,
		"governance/../workflows/x.yaml": true,
		"workflows//x.yaml":              true,
		"workflows":                      true,
		"Workflows/x.yaml":               true,
		"workflows-guide.md":             false,
		"docs/workflows/x.md":            false,
		"governance/charter.md":          false,
	} {
		if got := isWorkflowTarget(p); got != want {
			t.Errorf("isWorkflowTarget(%q) = %v, want %v", p, got, want)
		}
	}
}

type devStepDefs struct{ types map[string]domain.StepType }

func (d devStepDefs) AssignStep(context.Context, engine.AssignRequest) (*engine.AssignResult, error) {
	return nil, domain.NewError(domain.ErrUnavailable, "not used")
}

func (d devStepDefs) LookupStepDef(_ context.Context, _, stepID string) (*domain.StepDefinition, *domain.Run) {
	t, ok := d.types[stepID]
	if !ok {
		return nil, nil
	}
	return &domain.StepDefinition{ID: stepID, Type: t}, nil
}

func TestAuthorizeRunBranchWrite_DevModeNilActor(t *testing.T) {
	defs := devStepDefs{types: map[string]domain.StepType{
		"draft": domain.StepTypeManual, "review": domain.StepTypeReview,
		"publish": domain.StepTypeInternal, "converge": domain.StepTypeConvergence,
	}}
	// No store: the binding is never consulted for a nil actor.
	s := &Server{stepAssigner: defs}
	cases := []struct {
		cursor string
		want   domain.ErrorCode // "" = allowed
	}{
		{"draft", ""},
		{"converge", ""},
		{"", ""},
		{"divergence:run-1-div-1", ""},
		{"review", domain.ErrConflict},
		{"publish", domain.ErrConflict},
		{"unknown-step", domain.ErrConflict},
	}
	for _, c := range cases {
		run := &domain.Run{RunID: "run-1", Status: domain.RunStatusActive, CurrentStepID: c.cursor, BranchName: "spine/plan/x-1"}
		err := s.authorizeRunBranchWrite(context.Background(), run)
		if c.want == "" {
			if err != nil {
				t.Errorf("cursor %q: %v, want allowed", c.cursor, err)
			}
			continue
		}
		se, ok := err.(*domain.SpineError)
		if !ok || se.Code != c.want {
			t.Errorf("cursor %q: %v, want %s", c.cursor, err, c.want)
		}
	}

	// No step assigner at all: fail closed (503) once there is a cursor.
	none := &Server{}
	err := none.authorizeRunBranchWrite(context.Background(), &domain.Run{RunID: "run-1", CurrentStepID: "draft"})
	if se, ok := err.(*domain.SpineError); !ok || se.Code != domain.ErrUnavailable {
		t.Errorf("no assigner: %v, want unavailable", err)
	}
}
