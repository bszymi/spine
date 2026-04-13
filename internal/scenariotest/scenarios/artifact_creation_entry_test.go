//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// goldenTaskContent builds a valid task artifact for planning run testing.
func goldenTaskContent(id, title, epicPath string) string {
	return "---\n" +
		"id: " + id + "\n" +
		"type: Task\n" +
		"title: " + title + "\n" +
		"status: Draft\n" +
		"epic: /" + epicPath + "\n" +
		"created: 2026-01-01\n" +
		"last_updated: 2026-01-01\n" +
		"links:\n" +
		"  - type: parent\n" +
		"    target: /" + epicPath + "\n" +
		"---\n\n" +
		"# " + id + " — " + title + "\n"
}

// TestArtifactCreationEntry_TaskGoldenPath validates the full creation flow
// for a task: seed existing artifacts, start planning run, progress through
// creation workflow, and verify artifact lands on main.
//
// Scenario: Create a task via planning run golden path
//   Given a governance environment with orchestrator enabled
//     And an existing initiative, epic, and two tasks (TASK-001, TASK-003)
//     And the artifact-creation workflow is seeded
//     And projections are synced
//   When a planning run is started for TASK-004 (next sequential ID)
//   Then the run should be Active on step "draft"
//   When the draft step completes with outcome "ready_for_review"
//   Then the current step should be "validate"
//   When validation passes with outcome "valid"
//   Then the current step should be "review"
//   When review approves with outcome "approved"
//   Then the run should be completed
//     And the task file should exist on main
func TestArtifactCreationEntry_TaskGoldenPath(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "task-creation-entry-golden-path",
		Description: "Create a task via planning run and verify it lands on main",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// Setup: seed an initiative and epic as parent artifacts.
			engine.WriteAndCommit(
				"initiatives/init-001-test/initiative.md",
				"---\nid: INIT-001\ntype: Initiative\ntitle: Test Init\nstatus: Pending\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n",
				"seed initiative",
			),
			engine.WriteAndCommit(
				"initiatives/init-001-test/epics/epic-001-test/epic.md",
				"---\nid: EPIC-001\ntype: Epic\ntitle: Test Epic\nstatus: Pending\ninitiative: /initiatives/init-001-test/initiative.md\ncreated: 2026-01-01\nlast_updated: 2026-01-01\nlinks:\n  - type: parent\n    target: /initiatives/init-001-test/initiative.md\n---\n# EPIC-001\n",
				"seed epic",
			),
			// Seed existing tasks to test sequential ID allocation.
			engine.WriteAndCommit(
				"initiatives/init-001-test/epics/epic-001-test/tasks/task-001-first.md",
				goldenTaskContent("TASK-001", "First Task", "initiatives/init-001-test/epics/epic-001-test/epic.md"),
				"seed task-001",
			),
			engine.WriteAndCommit(
				"initiatives/init-001-test/epics/epic-001-test/tasks/task-003-third.md",
				goldenTaskContent("TASK-003", "Third Task", "initiatives/init-001-test/epics/epic-001-test/epic.md"),
				"seed task-003",
			),
			// Seed artifact-creation workflow.
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Start the planning run — creating TASK-004 (next after 001, 003).
			engine.StartPlanningRun(
				"initiatives/init-001-test/epics/epic-001-test/tasks/task-004-new-task.md",
				goldenTaskContent("TASK-004", "New Task", "initiatives/init-001-test/epics/epic-001-test/epic.md"),
			),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),

			// Progress through creation workflow.
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.AssertCurrentStep("validate"),

			engine.SubmitStepResult("valid"),
			engine.AssertCurrentStep("review"),

			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			// Verify: task appears on main.
			engine.AssertFileExists("initiatives/init-001-test/epics/epic-001-test/tasks/task-004-new-task.md"),
		},
	})
}

// TestArtifactCreationEntry_EpicGoldenPath validates epic creation.
//
// Scenario: Create an epic via planning run
//   Given a governance environment with orchestrator enabled
//     And an existing initiative as parent
//     And the artifact-creation workflow is seeded
//   When a planning run creates the epic through draft -> validate -> review -> approved
//   Then the run should be completed
//     And the epic file should exist on main
func TestArtifactCreationEntry_EpicGoldenPath(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "epic-creation-entry-golden-path",
		Description: "Create an epic via planning run",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// Setup: seed initiative.
			engine.WriteAndCommit(
				"initiatives/init-001-test/initiative.md",
				"---\nid: INIT-001\ntype: Initiative\ntitle: Test Init\nstatus: Pending\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n",
				"seed initiative",
			),
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Start planning run for a new epic.
			engine.StartPlanningRun(
				"initiatives/init-001-test/epics/epic-001-new/epic.md",
				"---\nid: EPIC-001\ntype: Epic\ntitle: New Epic\nstatus: Draft\ninitiative: /initiatives/init-001-test/initiative.md\ncreated: 2026-01-01\nlast_updated: 2026-01-01\nlinks:\n  - type: parent\n    target: /initiatives/init-001-test/initiative.md\n---\n# EPIC-001 — New Epic\n",
			),
			engine.AssertRunStatus(domain.RunStatusActive),

			// Progress through workflow.
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.SubmitStepResult("valid"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			engine.AssertFileExists("initiatives/init-001-test/epics/epic-001-new/epic.md"),
		},
	})
}

// TestArtifactCreationEntry_FirstArtifactInScope validates that creating
// the first task in an empty epic allocates TASK-001.
//
// Scenario: First task in empty epic allocates TASK-001
//   Given a governance environment with orchestrator enabled
//     And an initiative with an empty epic (no existing tasks)
//     And the artifact-creation workflow is seeded
//   When a planning run creates TASK-001 through the full workflow
//   Then the run should be completed
//     And the task file "task-001-first.md" should exist on main
func TestArtifactCreationEntry_FirstArtifactInScope(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "first-artifact-in-scope",
		Description: "Create first task in empty epic → allocates TASK-001",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit(
				"initiatives/init-001-test/initiative.md",
				"---\nid: INIT-001\ntype: Initiative\ntitle: Test\nstatus: Pending\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n",
				"seed init",
			),
			engine.WriteAndCommit(
				"initiatives/init-001-test/epics/epic-001-empty/epic.md",
				"---\nid: EPIC-001\ntype: Epic\ntitle: Empty Epic\nstatus: Pending\ninitiative: /initiatives/init-001-test/initiative.md\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# EPIC-001\n",
				"seed empty epic",
			),
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Create first task — should be TASK-001.
			engine.StartPlanningRun(
				"initiatives/init-001-test/epics/epic-001-empty/tasks/task-001-first.md",
				goldenTaskContent("TASK-001", "First Ever", "initiatives/init-001-test/epics/epic-001-empty/epic.md"),
			),
			engine.AssertRunStatus(domain.RunStatusActive),

			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.SubmitStepResult("valid"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			engine.AssertFileExists("initiatives/init-001-test/epics/epic-001-empty/tasks/task-001-first.md"),
		},
	})
}
