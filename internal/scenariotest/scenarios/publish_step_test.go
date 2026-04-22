//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// publishStepWorkflowYAML mirrors the shape of task-default.yaml after
// TASK-015: review.accepted routes to an explicit publish step that the
// Spine engine (not a runner) advances. Used to assert the engine-owned
// merge flow end-to-end.
const publishStepWorkflowYAML = `id: task-default
name: Default Task Workflow
version: "1.0"
status: Active
description: Publish-step scenario workflow.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    required_outputs:
      - deliverable
    outcomes:
      - id: completed
        name: Implementation Complete
        next_step: review
    timeout: "4h"

  - id: review
    name: Review Deliverable
    type: review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: publish
        commit:
          status: Completed
      - id: needs_rework
        name: Needs Rework
        next_step: execute
    timeout: "24h"

  - id: publish
    name: Publish Accepted Outcome
    type: internal
    execution:
      mode: spine_only
      handler: merge
    outcomes:
      - id: published
        name: Outcome Published
        next_step: end
      - id: merge_failed
        name: Merge Failed
        next_step: execute
    retry:
      limit: 3
      backoff: exponential
    timeout: "10m"
    timeout_outcome: merge_failed
`

// assertPublishStepEngineAdvanced verifies that the run's publish step
// was executed by the Spine engine directly: exactly one execution
// exists for the publish step, its ActorID is EngineMergeActorID, and
// its OutcomeID is "published".
func assertPublishStepEngineAdvanced() engine.Step {
	return engine.Step{
		Name: "assert-publish-step-engine-advanced",
		Action: func(sc *engine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list step executions: %w", err)
			}
			var publishExecs []domain.StepExecution
			for i := range execs {
				if execs[i].StepID == "publish" {
					publishExecs = append(publishExecs, execs[i])
				}
			}
			if len(publishExecs) != 1 {
				sc.T.Errorf("expected exactly 1 publish step execution, got %d", len(publishExecs))
				return nil
			}
			pub := publishExecs[0]
			if pub.Status != domain.StepStatusCompleted {
				sc.T.Errorf("publish step: expected status completed, got %s", pub.Status)
			}
			if pub.OutcomeID != "published" {
				sc.T.Errorf("publish step: expected outcome published, got %s", pub.OutcomeID)
			}
			if pub.ActorID != spineEngine.EngineMergeActorID {
				sc.T.Errorf("publish step: expected actor %s, got %s",
					spineEngine.EngineMergeActorID, pub.ActorID)
			}
			return nil
		},
	}
}

// assertNoAssignmentForPublish verifies the runner was never dispatched
// for the publish step: no Assignment row exists for the publish step
// execution. This is the core behavioural property of TASK-015 — the
// engine owns the authoritative merge and does not delegate the publish
// outcome to an external actor.
//
// The auto-assignment ID is fmt.Sprintf("auto-%s-%s", execID, actorID).
// If createAutoAssignmentRecord had run, GetAssignment on that ID would
// succeed. We assert it does not.
func assertNoAssignmentForPublish() engine.Step {
	return engine.Step{
		Name: "assert-no-assignment-for-publish",
		Action: func(sc *engine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			execs, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list step executions: %w", err)
			}
			var publishExecID string
			for i := range execs {
				if execs[i].StepID == "publish" {
					publishExecID = execs[i].ExecutionID
					break
				}
			}
			if publishExecID == "" {
				sc.T.Error("publish step execution not found")
				return nil
			}
			expectedAssignmentID := fmt.Sprintf("auto-%s-%s", publishExecID, spineEngine.EngineMergeActorID)
			a, err := sc.Runtime.Store.GetAssignment(sc.Ctx, expectedAssignmentID)
			if err == nil && a != nil {
				sc.T.Errorf("expected no runner assignment for publish step, found %q (the engine should advance the step directly, not dispatch to a runner)", expectedAssignmentID)
			}
			return nil
		},
	}
}

// TestStandardRun_PublishStepEngineAdvanced exercises the full task-
// default flow through the engine-owned publish step.
//
// Scenario: Publish step is advanced by the engine, not dispatched to a runner
//
//	Given a workflow whose publish step is type: internal with handler: merge
//	  And a hierarchy INIT -> EPIC -> TASK with a deliverable on the run branch
//	When the run completes through execute → review(accepted) → publish
//	Then the run should be completed
//	  And the publish step should have exactly one execution
//	  And its actor should be actor-engine-merge
//	  And its outcome should be "published"
//	  And no runner assignment should have been created for the publish step
func TestStandardRun_PublishStepEngineAdvanced(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "publish-step-engine-advanced",
		Description: "Verify the publish step is advanced by the Spine engine with no runner dispatch",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit(
				"workflows/task-default.yaml",
				publishStepWorkflowYAML,
				"seed task-default workflow with publish step",
			),
			engine.SeedHierarchy("INIT-100", "EPIC-100", "TASK-100"),
			engine.SyncProjections(),

			engine.StartRun("initiatives/init-100/epics/epic-100/tasks/task-100.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertBranchExists(),

			engine.WriteOnBranch(
				"initiatives/init-100/epics/epic-100/tasks/task-100-deliverable.md",
				"# Deliverable\nTask implementation output.\n",
				"Add task deliverable",
			),

			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),

			// Review accepts — engine activates publish, runs merge, advances
			// the step, completes the run. No runner dispatch in between.
			engine.SubmitStepResult("accepted"),
			engine.AssertRunCompleted(),

			// Deliverable landed on main.
			engine.AssertFileExists("initiatives/init-100/epics/epic-100/tasks/task-100-deliverable.md"),

			// The publish step was advanced by the engine with the expected
			// actor and outcome, and no Assignment was ever created for it.
			assertPublishStepEngineAdvanced(),
			assertNoAssignmentForPublish(),

			engine.AssertBranchNotExists(),
		},
	})
}
