//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestArtifactCreationWorkflow_DraftToPending validates the full artifact
// creation workflow end-to-end: a task starts as Draft, progresses through
// draft → validate → review, and on approval the branch auto-merges to main
// with the artifact status updated to Pending.
func TestArtifactCreationWorkflow_DraftToPending(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	const taskPath = "initiatives/init-001-test/epics/epic-001-test/tasks/task-010-scenario.md"
	const taskContent = `---
id: TASK-010
type: Task
title: Scenario Workflow Task
status: Draft
epic: /initiatives/init-001-test/epics/epic-001-test/epic.md
initiative: /initiatives/init-001-test/initiative.md
created: 2026-01-01
last_updated: 2026-01-01
links:
  - type: parent
    target: /initiatives/init-001-test/epics/epic-001-test/epic.md
---

# TASK-010 — Scenario Workflow Task

A task created to validate the artifact creation workflow transitions.
`

	engine.RunScenario(t, engine.Scenario{
		Name:        "artifact-creation-draft-to-pending",
		Description: "Task created as Draft progresses through creation workflow and lands on main as Pending",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// --- Setup: seed parent artifacts on main ---
			engine.WriteAndCommit(
				"initiatives/init-001-test/initiative.md",
				"---\nid: INIT-001\ntype: Initiative\ntitle: Test Initiative\nstatus: Pending\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001 — Test Initiative\n",
				"seed initiative",
			),
			engine.WriteAndCommit(
				"initiatives/init-001-test/epics/epic-001-test/epic.md",
				"---\nid: EPIC-001\ntype: Epic\ntitle: Test Epic\nstatus: Pending\ninitiative: /initiatives/init-001-test/initiative.md\ncreated: 2026-01-01\nlast_updated: 2026-01-01\nlinks:\n  - type: parent\n    target: /initiatives/init-001-test/initiative.md\n---\n# EPIC-001 — Test Epic\n",
				"seed epic",
			),
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// --- Stage 1: Start planning run — task enters draft step ---
			engine.StartPlanningRun(taskPath, taskContent),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),
			engine.AssertBranchExists(),

			// --- Stage 2: Finish drafting — submit and move to validation ---
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.AssertCurrentStep("validate"),

			// --- Stage 3: Validation passes — move to review ---
			engine.SubmitStepResult("valid"),
			engine.AssertCurrentStep("review"),

			// --- Stage 4: Approve review — auto-merge to main with status Pending ---
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			// --- Verify: artifact exists on main after auto-merge ---
			engine.AssertFileExists(taskPath),

			// --- Verify: branch cleaned up after merge ---
			engine.AssertBranchNotExists(),

			// --- Verify: artifact status rewritten from Draft to Pending ---
			engine.AssertFileContains(taskPath, "status: Pending"),

			// --- Verify: projection reflects Pending status ---
			engine.SyncProjections(),
			engine.AssertProjection(taskPath, "Status", "Pending"),
		},
	})
}
