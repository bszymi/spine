//go:build scenario

package scenarios_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// sideFileContent returns content for a secondary file written to a branch or
// main directly — used to test non-conflicting parallel edits.
func sideFileContent(label string) string {
	return fmt.Sprintf("# Side File\n\nThis file was added by %s.\n", label)
}

// mcrUpdatedContent returns an updated version of a task artifact, using a
// parametric taskID to match whatever ID was seeded for the test.
func mcrUpdatedContent(taskID, epicPath, initPath string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Updated Task Title"
status: Pending
epic: %s
initiative: %s
links:
  - type: parent
    target: %s
---

# Updated Task

This description was modified on the run branch.
`, taskID, "/"+epicPath, "/"+initPath, "/"+epicPath)
}

// mcrConflictContent returns a diverging version of a task artifact committed
// directly to main — used to create a conflict with the branch version.
func mcrConflictContent(taskID, epicPath, initPath string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Main Direct Update"
status: Pending
epic: %s
initiative: %s
links:
  - type: parent
    target: %s
---

# Main Direct Update

This version was committed directly to main to produce a merge conflict.
`, taskID, "/"+epicPath, "/"+initPath, "/"+epicPath)
}

// resolvedTaskContent returns a task artifact whose content matches a reconciled
// version — written directly to main to simulate conflict resolution.
func resolvedTaskContent(taskID, epicPath, initPath string) string {
	return fmt.Sprintf(`---
id: %s
type: Task
title: "Resolved Task"
status: Pending
epic: %s
initiative: %s
links:
  - type: parent
    target: %s
---

# Resolved Task

Content reconciled from branch and main versions.
`, taskID, "/"+epicPath, "/"+initPath, "/"+epicPath)
}

// TestMergeConflict_NonConflictingParallelEdit verifies that when the run branch
// and main modify different files, the branch merge succeeds and both sets of
// changes appear on main.
//
// Scenario: Non-conflicting parallel edits on branch and main merge cleanly
//
//	Given a task hierarchy on main
//	  And an active run for the task
//	When a new file is committed on the run branch
//	  And a different new file is committed directly on main
//	When the workflow completes through the commit step
//	Then the run is completed (merge succeeded without conflict)
//	  And the branch file appears on main
//	  And the direct-main file is still on main
func TestMergeConflict_NonConflictingParallelEdit(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "merge-conflict-non-conflicting-parallel-edit",
		Description: "Different files modified on branch and main merge cleanly",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-601", "EPIC-601", "TASK-601"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-601/epics/epic-601/tasks/task-601.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			// Write a new file on the run branch — does not touch any main file.
			scenarioEngine.WriteOnBranch(
				"initiatives/init-601/epics/epic-601/notes/branch-note.md",
				sideFileContent("branch"),
				"add branch-only note",
			),

			// Write a different new file directly to main — no overlap with branch file.
			{
				Name: "commit-different-file-directly-to-main",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					sc.Repo.WriteArtifact(sc.T,
						"initiatives/init-601/epics/epic-601/notes/main-note.md",
						sideFileContent("main"))
					sc.Repo.CommitAll(sc.T, "add main-only note")
					return nil
				},
			},

			// Complete the workflow — commit step triggers the branch merge.
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),
			scenarioEngine.SubmitStepResult("accepted"),

			// The merge must succeed: different files do not conflict.
			scenarioEngine.AssertRunCompleted(),

			// Both files must now be visible on main (HEAD).
			scenarioEngine.AssertFileExists("initiatives/init-601/epics/epic-601/notes/branch-note.md"),
			scenarioEngine.AssertFileExists("initiatives/init-601/epics/epic-601/notes/main-note.md"),
		},
	})
}

// TestMergeConflict_MainUnchangedAfterConflict verifies that when a merge conflict
// occurs, the run fails and the conflicting artifact on main retains the version
// that was committed directly — the branch version is not silently applied.
//
// Scenario: Conflicting parallel edits fail and do not overwrite main
//
//	Given an artifact updated on the run branch
//	  And the same artifact updated directly on main (diverging histories)
//	When the workflow's commit step attempts to merge
//	Then the run transitions to failed
//	  And the main artifact still contains the direct-main version
//	  And the main artifact does NOT contain the branch version
func TestMergeConflict_MainUnchangedAfterConflict(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "merge-conflict-main-unchanged-after-conflict",
		Description: "After a merge conflict the main artifact retains the pre-conflict version",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-602", "EPIC-602", "TASK-602"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-602/epics/epic-602/tasks/task-602.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			// Update the task artifact on the run branch.
			{
				Name: "update-artifact-on-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: branchName})
					_, err := sc.Runtime.Artifacts.Update(ctx, taskPath, mcrUpdatedContent("TASK-602", epicPath, initPath))
					return err
				},
			},

			// Commit a diverging version of the same artifact directly to main,
			// creating a branch/main divergence that will cause a conflict.
			{
				Name: "commit-diverging-update-to-main",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)

					sc.Repo.CheckoutBranch(sc.T, "main")
					sc.Repo.WriteArtifact(sc.T, taskPath, mcrConflictContent("TASK-602", epicPath, initPath))
					sc.Repo.CommitAll(sc.T, "direct main update — creates branch/main divergence")
					return nil
				},
			},

			// Advance through execute step (no merge yet).
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),

			// Submit accepted — triggers CompleteRun → MergeRunBranch → conflict.
			// IngestResult returns nil even on conflict (failRunOnMergeError swallows error).
			scenarioEngine.SubmitStepResult("accepted"),

			// Run must be failed due to the merge conflict.
			scenarioEngine.AssertRunStatus(domain.RunStatusFailed),

			// Main must still have the direct-main version — the branch version
			// ("Updated Task Title") was never silently applied.
			{
				Name: "assert-main-retains-direct-main-content-not-branch-content",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					art, err := sc.Runtime.Artifacts.Read(sc.Ctx, taskPath, "HEAD")
					if err != nil {
						return fmt.Errorf("read artifact from HEAD: %w", err)
					}
					// The direct-main version has title "Main Direct Update".
					if art.Title != "Main Direct Update" {
						return fmt.Errorf("expected main to retain 'Main Direct Update', got %q", art.Title)
					}
					// The branch version title must not appear on main.
					if strings.Contains(art.Title, "Updated") {
						return fmt.Errorf("branch version leaked to main: title contains 'Updated'")
					}
					return nil
				},
			},
		},
	})
}

// TestMergeConflict_ReRunAfterResolution verifies that after a run fails due to
// a merge conflict, starting a new run on the same task succeeds once the
// conflict has been manually resolved on main.
//
// "Manual resolution" here means writing the reconciled content directly to main
// (as a developer would after inspecting both versions). The subsequent run then
// proceeds without encountering a conflict.
//
// Scenario: new run on same task succeeds after conflict is resolved on main
//
//	Given a run that failed due to a merge conflict
//	When the conflicting artifact is resolved directly on main
//	  And a new run is started for the same task
//	Then the new run progresses to completion without encountering a conflict
func TestMergeConflict_ReRunAfterResolution(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "merge-conflict-re-run-after-resolution",
		Description: "A new run on the same task succeeds after the merge conflict is manually resolved",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-603", "EPIC-603", "TASK-603"),
			scenarioEngine.SyncProjections(),

			// --- First run: produces a merge conflict ---

			scenarioEngine.StartRun("initiatives/init-603/epics/epic-603/tasks/task-603.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			// Update the task artifact on the run branch.
			{
				Name: "first-run-update-artifact-on-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: branchName})
					_, err := sc.Runtime.Artifacts.Update(ctx, taskPath, mcrUpdatedContent("TASK-603", epicPath, initPath))
					return err
				},
			},

			// Commit a diverging update directly to main.
			{
				Name: "first-run-diverging-update-on-main",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)

					sc.Repo.CheckoutBranch(sc.T, "main")
					sc.Repo.WriteArtifact(sc.T, taskPath, mcrConflictContent("TASK-603", epicPath, initPath))
					sc.Repo.CommitAll(sc.T, "direct main update — creates branch/main divergence")
					return nil
				},
			},

			// Complete the first run — triggers conflict → run fails.
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),
			scenarioEngine.SubmitStepResult("accepted"),
			scenarioEngine.AssertRunStatus(domain.RunStatusFailed),

			// Save the first run's ID so we can verify independence from the second run.
			{
				Name: "save-first-run-id",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					sc.Set("first_run_id", sc.MustGet("run_id").(string))
					return nil
				},
			},

			// --- Manual conflict resolution: write reconciled content to main ---

			{
				Name: "resolve-conflict-on-main",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)

					sc.Repo.CheckoutBranch(sc.T, "main")
					sc.Repo.WriteArtifact(sc.T, taskPath, resolvedTaskContent("TASK-603", epicPath, initPath))
					sc.Repo.CommitAll(sc.T, "manual conflict resolution: reconcile branch and main versions")
					return nil
				},
			},

			// Sync projections after main is updated.
			scenarioEngine.SyncProjections(),

			// --- Second run: succeeds without conflict ---

			scenarioEngine.StartRun("initiatives/init-603/epics/epic-603/tasks/task-603.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Verify this is a new, distinct run from the failed one.
			{
				Name: "assert-second-run-is-distinct",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					firstID := sc.MustGet("first_run_id").(string)
					secondID := sc.MustGet("run_id").(string)
					if firstID == secondID {
						return fmt.Errorf("expected a new run ID, both runs share ID %q", firstID)
					}
					return nil
				},
			},

			// Submit execute and review steps — no branch/main divergence this time.
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),
			scenarioEngine.SubmitStepResult("accepted"),

			// The second run must complete successfully.
			scenarioEngine.AssertRunCompleted(),
		},
	})
}
