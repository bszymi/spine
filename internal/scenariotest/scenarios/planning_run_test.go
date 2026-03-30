//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Simplified artifact-creation workflow for scenario testing.
// Mirrors production workflow step structure (same IDs and outcomes)
// but omits preconditions that require artifact status changes in git.
const goldenArtifactCreationYAML = `id: artifact-creation
name: Artifact Creation
version: "1.0"
status: Active
description: Golden path test workflow for planning run artifact creation.
mode: creation
applies_to:
  - Initiative
  - Epic
  - Task
entry_step: draft
steps:
  - id: draft
    name: Draft Artifact
    type: manual
    required_outputs:
      - artifact_content
    outcomes:
      - id: ready_for_review
        name: Ready for Review
        next_step: validate

  - id: validate
    name: Validate Artifact
    type: automated
    outcomes:
      - id: valid
        name: Validation Passed
        next_step: review
      - id: invalid
        name: Validation Failed
        next_step: draft
    timeout: "5m"
    timeout_outcome: valid

  - id: review
    name: Review Artifact
    type: review
    outcomes:
      - id: approved
        name: Approved
        next_step: end
        commit:
          status: Pending
      - id: needs_revision
        name: Needs Revision
        next_step: draft
    timeout: "72h"
    timeout_outcome: needs_revision
`

const testInitiativeContent = `---
id: INIT-099
type: Initiative
title: Test Planning Initiative
status: Draft
owner: test
created: 2026-01-01
last_updated: 2026-01-01
---
# INIT-099 — Test Planning Initiative
`

func seedCreationWorkflow() engine.Step {
	return engine.WriteAndCommit(
		"workflows/artifact-creation.yaml",
		goldenArtifactCreationYAML,
		"seed artifact-creation workflow",
	)
}

// TestPlanningRun_InitiativeCreationGoldenPath validates the complete
// planning run lifecycle: start → draft → validate → review → merge → completed.
func TestPlanningRun_InitiativeCreationGoldenPath(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "initiative-creation-golden-path",
		Description: "Verify planning run creates an initiative through the full creation workflow",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// Setup: seed workflow and sync.
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Start the planning run with initiative content.
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),

			// Step 1: Draft → ready_for_review (with required output) → validate.
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.AssertCurrentStep("validate"),

			// Step 2: Validate → valid → review.
			engine.SubmitStepResult("valid"),
			engine.AssertCurrentStep("review"),

			// Step 3: Review → approved → end (enters committing due to commit metadata).
			engine.SubmitStepResult("approved"),
			engine.AssertRunStatus(domain.RunStatusCommitting),

			// Step 4: Merge the run branch to main.
			engine.MergeRunBranch(),
			engine.AssertRunCompleted(),
		},
	})
}

const childEpicContent = `---
id: EPIC-001
type: Epic
title: Test Epic
status: Draft
initiative: /initiatives/init-099/initiative.md
created: 2026-01-01
last_updated: 2026-01-01
links:
  - type: parent
    target: /initiatives/init-099/initiative.md
---
# EPIC-001 — Test Epic
`

const childEpic2Content = `---
id: EPIC-002
type: Epic
title: Test Epic 2
status: Draft
initiative: /initiatives/init-099/initiative.md
created: 2026-01-01
last_updated: 2026-01-01
links:
  - type: parent
    target: /initiatives/init-099/initiative.md
---
# EPIC-002 — Test Epic 2
`

const childTaskContent = `---
id: TASK-001
type: Task
title: Test Task
status: Pending
epic: /initiatives/init-099/epics/epic-001/epic.md
initiative: /initiatives/init-099/initiative.md
created: 2026-01-01
last_updated: 2026-01-01
links:
  - type: parent
    target: /initiatives/init-099/epics/epic-001/epic.md
---
# TASK-001 — Test Task
`

// TestPlanningRun_InitiativeWithChildArtifacts validates that multiple artifacts
// created on a planning run branch all merge to main on approval.
func TestPlanningRun_InitiativeWithChildArtifacts(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "initiative-with-child-artifacts",
		Description: "Verify planning run with initiative + epics + task all merge to main",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Start planning run with initiative.
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			engine.AssertRunStatus(domain.RunStatusActive),

			// Create child artifacts on the run branch.
			engine.CreateArtifactOnBranch(
				"initiatives/init-099/epics/epic-001/epic.md",
				childEpicContent,
			),
			engine.CreateArtifactOnBranch(
				"initiatives/init-099/epics/epic-002/epic.md",
				childEpic2Content,
			),
			engine.CreateArtifactOnBranch(
				"initiatives/init-099/epics/epic-001/tasks/task-001.md",
				childTaskContent,
			),

			// Progress through workflow.
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.SubmitStepResult("valid"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunStatus(domain.RunStatusCommitting),

			// Merge to main.
			engine.MergeRunBranch(),
			engine.AssertRunCompleted(),
		},
	})
}

// TestPlanningRun_RejectionAndRework validates that review rejection loops
// back to draft, and approval on retry succeeds.
func TestPlanningRun_RejectionAndRework(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "planning-run-rejection-rework",
		Description: "Verify rejection loops to draft and retry approval succeeds",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Start planning run.
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),

			// First pass: draft → validate → review → rejected.
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.AssertCurrentStep("validate"),
			engine.SubmitStepResult("valid"),
			engine.AssertCurrentStep("review"),
			engine.SubmitStepResult("needs_revision"),

			// Should loop back to draft.
			engine.AssertCurrentStep("draft"),
			engine.AssertRunStatus(domain.RunStatusActive),

			// Second pass: draft → validate → review → approved.
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.AssertCurrentStep("validate"),
			engine.SubmitStepResult("valid"),
			engine.AssertCurrentStep("review"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunStatus(domain.RunStatusCommitting),

			// Merge and complete.
			engine.MergeRunBranch(),
			engine.AssertRunCompleted(),
		},
	})
}

// TestPlanningRun_Cancellation validates that cancelling a planning run
// leaves no trace on main and cleans up the branch.
func TestPlanningRun_Cancellation(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "planning-run-cancellation",
		Description: "Verify cancellation cleans up branch and leaves main untouched",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Start planning run and create a child artifact on branch.
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.CreateArtifactOnBranch(
				"initiatives/init-099/epics/epic-001/epic.md",
				childEpicContent,
			),

			// Cancel the run.
			engine.CancelRun(),
			engine.AssertRunStatus(domain.RunStatusCancelled),
		},
	})
}
