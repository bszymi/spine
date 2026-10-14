package gateway_test

// Shared fixtures for run-branch API writes (INIT-023 TASK-014, audit P3-6).
// A write onto a run branch now needs the caller bound to the run's current
// step (authorizeRunBranchWrite → core/refadmit), so a fixture that expects
// a write to land models a run whose current step is held by the caller:
//
//   - the run's branch name carries its run ID's suffix, as every
//     engine-made branch does (spine/<run|plan>/<name>-<suffix> belongs to
//     run-<suffix>); refadmit resolves the branch back to its run by it;
//   - the run-level execution of the current step is in progress for the
//     caller;
//   - the run's workflow declares that step's type (the gateway's
//     store-fallback step lookup reads the workflow projection).

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// fixtureWorkflowPath is the workflow every bound fixture run is pinned to.
const fixtureWorkflowPath = "workflows/fixture-lifecycle.yaml"

// runIDOfBranch is the run an engine-made branch name belongs to: "run-"
// plus the name's suffix after its last '-'.
func runIDOfBranch(branch string) string {
	return "run-" + branch[strings.LastIndexByte(branch, '-')+1:]
}

// stepHeldBy returns the run-level execution of stepID, in progress for
// actorID.
func stepHeldBy(runID, stepID, actorID string) []domain.StepExecution {
	return []domain.StepExecution{{
		ExecutionID: runID + "-" + stepID + "-1", RunID: runID, StepID: stepID,
		Status: domain.StepStatusInProgress, ActorID: actorID, Attempt: 1, CreatedAt: time.Now(),
	}}
}

// fixtureWorkflowProjection is the projection of a workflow with a manual
// draft step and a review step.
func fixtureWorkflowProjection() *store.WorkflowProjection {
	def, _ := json.Marshal(&domain.WorkflowDefinition{
		ID: "fixture-lifecycle", Path: fixtureWorkflowPath, Version: "1.0", EntryStep: "draft",
		Steps: []domain.StepDefinition{
			{ID: "draft", Name: "Draft", Type: domain.StepTypeManual},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview},
		},
	})
	return &store.WorkflowProjection{WorkflowPath: fixtureWorkflowPath, Definition: def}
}
