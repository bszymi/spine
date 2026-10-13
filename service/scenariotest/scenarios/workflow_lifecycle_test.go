//go:build scenario

package scenarios_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
	spineEngine "github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/service/scenariotest/engine"
	"github.com/bszymi/spine/service/scenariotest/harness"
)

// Minimal, validator-clean workflow body used as the subject of the
// lifecycle run. The id is "new-flow" — the ID under which the run
// governs, writes, and ultimately merges.
const newWorkflowBodyV1 = `id: new-flow
name: New Flow
version: "1.0"
status: Active
description: test workflow written under the lifecycle
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
      required_skills: [planning]
    outcomes:
      - id: done
        name: Done
        next_step: end
`

const newWorkflowBodyV2 = `id: new-flow
name: New Flow
version: "1.1"
status: Active
description: updated body on the same run branch
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
      required_skills: [planning]
    outcomes:
      - id: done
        name: Done
        next_step: end
`

// TestWorkflowLifecycle_GoldenPath exercises INIT-017 end-to-end:
//
//	seeded workflow-lifecycle workflow (via WithWorkflows)
//	→ StartWorkflowPlanningRun (creates the new workflow body on a branch)
//	→ UpdateWorkflowOnBranch (second commit, same run_id, version bump)
//	→ submit "submitted"  → draft → review
//	→ submit "approved"   → branch merges to main, run completed
//	→ the new workflow file exists on main at the v1.1 version
func TestWorkflowLifecycle_GoldenPath(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-lifecycle-golden-path",
		Description: "End-to-end workflow edit through the workflow-lifecycle governance flow (ADR-008)",
		EnvOpts: []harness.EnvOption{
			harness.WithWorkflows(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.SyncProjections(),

			// Start the lifecycle run — writes v1.0 on the branch.
			engine.StartWorkflowPlanningRun("new-flow", newWorkflowBodyV1),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),
			engine.AssertBranchExists(),

			// Stack a second commit on the same run branch via
			// workflow.update with write_context.
			engine.UpdateWorkflowOnBranch("new-flow", newWorkflowBodyV2),

			// Progress: draft → review → approved → merge.
			engine.SubmitStepResult("submitted", "workflow_body"),
			engine.AssertCurrentStep("review"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			// The merged file exists on main after approval.
			engine.AssertFileExists("workflows/new-flow.yaml"),
		},
	})
}

// TestWorkflowLifecycle_NeedsReworkKeepsBranch asserts that the rework
// outcome returns to draft and leaves the branch intact for further edits.
func TestWorkflowLifecycle_NeedsReworkKeepsBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-lifecycle-needs-rework",
		Description: "needs_rework loops to draft; the branch and run remain alive for retry",
		EnvOpts: []harness.EnvOption{
			harness.WithWorkflows(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.SyncProjections(),

			engine.StartWorkflowPlanningRun("new-flow", newWorkflowBodyV1),
			engine.AssertCurrentStep("draft"),
			engine.AssertBranchExists(),

			engine.SubmitStepResult("submitted", "workflow_body"),
			engine.AssertCurrentStep("review"),
			engine.SubmitStepResult("needs_rework"),

			// Rework: back to draft, still Active, branch still present.
			engine.AssertCurrentStep("draft"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertBranchExists(),
		},
	})
}

// taskSpikeBodyV11 is a validator-clean update of the harness-seeded
// task-spike workflow (harness/repo.go seeds version "1.0", status Draft,
// which workflow.Validate refuses — a write must pass it). The update
// planning run's pre-check requires the version bump.
const taskSpikeBodyV11 = `id: task-spike
name: Spike Investigation Workflow
version: "1.1"
status: Active
description: updated through the workflow-lifecycle update run
applies_to:
  - Task
entry_step: investigate
steps:
  - id: investigate
    name: Investigate
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - ai_agent
        - human
      required_skills: [planning]
    outcomes:
      - id: done
        name: Done
        next_step: end
`

// assertMainWorkflowVersion reads workflows/<id>.yaml on main (not the
// working tree) and requires the given version line.
func assertMainWorkflowVersion(id, version string) engine.Step {
	return engine.Step{
		Name: "assert-main-workflow-version-" + id,
		Action: func(sc *engine.ScenarioContext) error {
			b, err := sc.Repo.Git.ReadFile(sc.Ctx, "main", "workflows/"+id+".yaml")
			if err != nil {
				return fmt.Errorf("read main:workflows/%s.yaml: %w", id, err)
			}
			if want := `version: "` + version + `"`; !strings.Contains(string(b), want) {
				return fmt.Errorf("main:workflows/%s.yaml lacks %s:\n%s", id, want, b)
			}
			return nil
		},
	}
}

// TestWorkflowLifecycle_UpdateExisting covers INIT-023 TASK-014 AC 13: an
// EXISTING workflow is edited through an update lifecycle run — the
// governed path a reviewer's PUT /workflows/{id} now takes instead of
// committing straight to main (audit F7).
func TestWorkflowLifecycle_UpdateExisting(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-lifecycle-update-existing",
		Description: "Update an existing workflow through the workflow-lifecycle planning run (ADR-008 §1, INIT-023 TASK-014)",
		EnvOpts: []harness.EnvOption{
			harness.WithWorkflows(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.SyncProjections(),
			assertMainWorkflowVersion("task-spike", "1.0"),

			engine.StartWorkflowUpdatePlanningRun("task-spike", taskSpikeBodyV11),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),
			engine.AssertBranchExists(),
			// main is untouched until the lifecycle review approves.
			assertMainWorkflowVersion("task-spike", "1.0"),

			engine.SubmitStepResult("submitted", "workflow_body"),
			engine.AssertCurrentStep("review"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			assertMainWorkflowVersion("task-spike", "1.1"),
		},
	})
}

// installStrictStepEntryPolicy installs `spine serve`'s default step-entry
// policy (INIT-023 TASK-007) on the scenario orchestrator.
func installStrictStepEntryPolicy() engine.Step {
	return engine.Step{
		Name: "install-strict-step-entry-policy",
		Action: func(sc *engine.ScenarioContext) error {
			sc.Runtime.Orchestrator.WithStepEntryPolicy(spineEngine.StrictStepEntryPolicy())
			return nil
		},
	}
}

// claimCurrentStepRefused expects actorID's claim of the current step to be
// refused with code.
func claimCurrentStepRefused(actorID string, code domain.ErrorCode) engine.Step {
	return engine.Step{
		Name: "claim-step-refused-" + actorID,
		Action: func(sc *engine.ScenarioContext) error {
			execID := sc.MustGet("current_execution_id").(string)
			_, err := sc.Runtime.Orchestrator.ClaimStep(sc.Ctx, spineEngine.ClaimRequest{ActorID: actorID, ExecutionID: execID})
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != code {
				return fmt.Errorf("claim by %s: %v, want %s", actorID, err, code)
			}
			return nil
		},
	}
}

// TestWorkflowLifecycle_UpdateExisting_DraftAuthorCannotReview covers
// INIT-023 TASK-014 AC 19, the F7 "no second reviewer" clause end to end
// under `spine serve`'s default step-entry policy: the reviewer who drafted
// the update cannot claim its review (TASK-007 separation of duties), a
// second reviewer can, and approval puts the bumped version on main.
func TestWorkflowLifecycle_UpdateExisting_DraftAuthorCannotReview(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	engine.RunScenario(t, engine.Scenario{
		Name:        "workflow-lifecycle-update-second-reviewer",
		Description: "An update run's draft author cannot review it; a second reviewer approves (INIT-023 TASK-014 AC 19)",
		EnvOpts: []harness.EnvOption{
			harness.WithWorkflows(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.SyncProjections(),
			registerSkill("skill-wf-author", "workflow_authoring", "governance"),
			registerSkill("skill-wf-review", "workflow_review", "governance"),
			registerActor("rev-a", domain.ActorTypeHuman, domain.RoleReviewer),
			registerActor("rev-b", domain.ActorTypeHuman, domain.RoleReviewer),
			assignSkillToActor("rev-a", "skill-wf-author"),
			assignSkillToActor("rev-a", "skill-wf-review"),
			assignSkillToActor("rev-b", "skill-wf-author"),
			assignSkillToActor("rev-b", "skill-wf-review"),
			installStrictStepEntryPolicy(),

			engine.StartWorkflowUpdatePlanningRun("task-spike", taskSpikeBodyV11),
			engine.AssertCurrentStep("draft"),
			claimCurrentStep("rev-a"),
			engine.SubmitStepResult("submitted", "workflow_body"),
			engine.AssertCurrentStep("review"),

			// The draft author is refused the review (403) ...
			claimCurrentStepRefused("rev-a", domain.ErrForbidden),
			// ... a different reviewer claims and approves.
			claimCurrentStep("rev-b"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			assertMainWorkflowVersion("task-spike", "1.1"),
		},
	})
}
