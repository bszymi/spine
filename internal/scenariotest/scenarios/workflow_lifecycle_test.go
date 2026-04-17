//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
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
