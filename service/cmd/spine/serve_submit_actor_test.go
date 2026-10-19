package main

// INIT-023 TASK-008 AC 4 (a), adapter half: resultAdapter forwards the
// gateway's authenticated submitter into engine.SubmitRequest.ActorID, so
// the engine refuses a foreign one.

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/testutil"
)

func TestResultAdapter_PassesActorID(t *testing.T) {
	wf := &domain.WorkflowDefinition{ID: "wf", EntryStep: "review", Steps: []domain.StepDefinition{
		{ID: "review", Name: "Review", Type: domain.StepTypeReview,
			Outcomes: []domain.OutcomeDefinition{{ID: "accepted", Name: "Accepted", NextStep: "end"}}},
	}}
	st := &headSubmitStore{
		run: domain.Run{RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-0123456789ab"},
		exec: domain.StepExecution{ExecutionID: "run-1-review-1", RunID: "run-1", StepID: "review",
			Status: domain.StepStatusAssigned, ActorID: "alice", Attempt: 1},
	}
	orch, err := engine.New(headWF{wf}, st, headNoop{}, headNoop{}, headNoop{}, git.NewCLIClient(testutil.NewTempRepo(t)), headWF{wf})
	if err != nil {
		t.Fatal(err)
	}
	a := &resultAdapter{orch: orch}
	_, err = a.IngestResult(context.Background(), gateway.ResultSubmission{
		ExecutionID: "run-1-review-1", OutcomeID: "accepted", ActorID: "mallory",
	})
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrForbidden {
		t.Fatalf("foreign submitter through the adapter: err = %v, want forbidden", err)
	}
}
