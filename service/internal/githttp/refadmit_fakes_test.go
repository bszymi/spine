package githttp

import (
	"context"

	"github.com/bszymi/spine/core/domain"
)

// admReader is an in-memory refadmit.RunReader for the pre-receive
// admission tests (INIT-023 TASK-016).
type admReader struct {
	runs  map[string]*domain.Run
	execs map[string][]domain.StepExecution
	calls int
}

func (f *admReader) GetRun(_ context.Context, id string) (*domain.Run, error) {
	f.calls++
	if r, ok := f.runs[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "run not found")
}

func (f *admReader) ListStepExecutionsByRun(_ context.Context, id string) ([]domain.StepExecution, error) {
	f.calls++
	return append([]domain.StepExecution(nil), f.execs[id]...), nil
}

func (f *admReader) GetBranch(_ context.Context, _ string) (*domain.Branch, error) {
	f.calls++
	return nil, domain.NewError(domain.ErrNotFound, "branch not found")
}

// admStepDefs resolves every step to a manual (unfrozen) step.
type admStepDefs struct{}

func (admStepDefs) LookupStepDef(_ context.Context, _, stepID string) (*domain.StepDefinition, *domain.Run) {
	return &domain.StepDefinition{ID: stepID, Type: domain.StepTypeManual}, nil
}

// boundRun returns a reader holding one active run whose branch is
// "spine/run/<slug>-<hex>" (run ID "run-<hex>"), with its current step
// `execute` in progress for actorID.
func boundRun(actorID, slug, hex string) *admReader {
	runID := "run-" + hex
	return &admReader{
		runs: map[string]*domain.Run{runID: {
			RunID: runID, Status: domain.RunStatusActive, CurrentStepID: "execute",
			BranchName: "spine/run/" + slug + "-" + hex,
		}},
		execs: map[string][]domain.StepExecution{runID: {{
			ExecutionID: "exec-1", RunID: runID, StepID: "execute", ActorID: actorID,
			Status: domain.StepStatusInProgress, RepositoryID: domain.PrimaryRepositoryID,
		}}},
	}
}
