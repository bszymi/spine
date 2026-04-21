//go:build scenario

package scenarios_test

import (
	"fmt"
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

// TestArtifactCreationEntry_SiblingTasksDistinctIDs guards INIT-008 EPIC-005
// TASK-018: two consecutive addArtifactToRun calls for Task children of an
// epic created earlier in the same planning run must produce distinct IDs
// (TASK-001, TASK-002) and distinct paths. Before the fix the allocator
// missed the first sibling because its lowercase filename did not match the
// uppercase NextID regex, so both calls collided on TASK-001.
//
// Scenario: Two siblings added to the same planning run branch get distinct IDs
//   Given an initiative on main and an epic created on the planning-run branch
//   When two addArtifactToRun calls allocate task IDs for children of that epic
//   Then the first returns TASK-001 and the second returns TASK-002
//     And the two paths differ
func TestArtifactCreationEntry_SiblingTasksDistinctIDs(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	const (
		tasksDir = "initiatives/init-001-test/epics/epic-042-sibling-scope/tasks"
		epicPath = "initiatives/init-001-test/epics/epic-042-sibling-scope/epic.md"
	)
	// Inline builder: the production /artifacts/add handler inherits
	// `initiative` from the parent epic's metadata via buildInitialContent;
	// the scenario helper does not, so we include it explicitly here.
	taskBody := func(title string) func(string) string {
		return func(allocated string) string {
			return "---\n" +
				"id: " + allocated + "\n" +
				"type: Task\n" +
				"title: " + title + "\n" +
				"status: Draft\n" +
				"epic: /" + epicPath + "\n" +
				"initiative: /initiatives/init-001-test/initiative.md\n" +
				"created: 2026-01-01\n" +
				"last_updated: 2026-01-01\n" +
				"links:\n" +
				"  - type: parent\n" +
				"    target: /" + epicPath + "\n" +
				"---\n\n" +
				"# " + allocated + " — " + title + "\n"
		}
	}

	engine.RunScenario(t, engine.Scenario{
		Name:        "sibling-tasks-distinct-ids",
		Description: "Two /artifacts/add calls on the same planning run allocate distinct IDs",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// Seed initiative on main so the planning run has a valid parent.
			engine.WriteAndCommit(
				"initiatives/init-001-test/initiative.md",
				"---\nid: INIT-001\ntype: Initiative\ntitle: Test Init\nstatus: Pending\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n",
				"seed initiative",
			),
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// Start a planning run that creates the epic; the epic is
			// committed to the run branch by StartPlanningRun itself.
			engine.StartPlanningRun(
				epicPath,
				"---\nid: EPIC-042\ntype: Epic\ntitle: Sibling Scope\nstatus: Draft\ninitiative: /initiatives/init-001-test/initiative.md\ncreated: 2026-01-01\nlast_updated: 2026-01-01\nlinks:\n  - type: parent\n    target: /initiatives/init-001-test/initiative.md\n---\n# EPIC-042\n",
			),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),

			// Two consecutive adds — the second must see the first sibling
			// already on the branch and advance to TASK-002.
			engine.AddSiblingTaskToRun(tasksDir, "Cascade child one", "first", taskBody("Cascade child one")),
			engine.AddSiblingTaskToRun(tasksDir, "Cascade child two", "second", taskBody("Cascade child two")),

			{
				Name: "assert-distinct-ids-and-paths",
				Action: func(sc *engine.ScenarioContext) error {
					firstID := sc.MustGet("first_id").(string)
					secondID := sc.MustGet("second_id").(string)
					firstPath := sc.MustGet("first_path").(string)
					secondPath := sc.MustGet("second_path").(string)
					if firstID != "TASK-001" {
						return fmt.Errorf("first sibling: expected TASK-001, got %s", firstID)
					}
					if secondID != "TASK-002" {
						return fmt.Errorf("second sibling: expected TASK-002 (duplicate allocation bug), got %s", secondID)
					}
					if firstPath == secondPath {
						return fmt.Errorf("sibling paths must differ, both = %s", firstPath)
					}
					return nil
				},
			},
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
