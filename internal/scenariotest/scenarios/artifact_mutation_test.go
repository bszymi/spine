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

// Standard single-step-plus-review workflow with commit metadata on the
// terminal outcome, so the branch is merged to main on completion.
const mutationWorkflowYAML = `id: task-mutation-test
name: Mutation Test Workflow
version: "1.0"
status: Active
description: Workflow for artifact mutation scenario tests.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    outcomes:
      - id: completed
        name: Done
        next_step: review
    timeout: "4h"

  - id: review
    name: Review
    type: review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
        commit:
          status: Completed
      - id: rejected
        name: Rejected
        next_step: execute
    timeout: "24h"
`

func seedMutationWorkflow() scenarioEngine.Step {
	return seedWorkflow("task-mutation-test", mutationWorkflowYAML)
}

// taskContentWithoutBlockerLink returns Task B content without any blocked_by
// link — used to verify link removal on a run branch propagates to main.
func taskContentWithoutBlockerLink(taskPath, epicPath, initPath string) string {
	return fmt.Sprintf(`---
id: TASK-MUT4-B
type: Task
title: "Task Without Blocker Link"
status: Pending
epic: %s
initiative: %s
links:
  - type: parent
    target: %s
---

# Task Without Blocker Link

The blocked_by link was removed on the run branch.
`, "/"+epicPath, "/"+initPath, "/"+epicPath)
}

// mainConflictContent returns a version of the task artifact committed directly
// to main — used to create a branch/main divergence that forces a merge conflict.
func mainConflictContent(taskPath, epicPath, initPath string) string {
	return fmt.Sprintf(`---
id: TASK-MUT
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
`, "/"+epicPath, "/"+initPath, "/"+epicPath)
}

// updatedTaskContent returns a modified version of the task artifact with an
// updated title — used to distinguish branch from main content in assertions.
func updatedTaskContent(taskPath, epicPath, initPath string) string {
	return fmt.Sprintf(`---
id: TASK-MUT
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
`, "/"+epicPath, "/"+initPath, "/"+epicPath)
}

// TestArtifactMutation_UpdateOnRunBranch verifies that artifact.Update with a
// WriteContext scoped to a run branch modifies only the branch copy —
// the main (HEAD) version is unchanged.
//
// Scenario: Branch-scoped artifact update does not touch main
//
//	Given an artifact on main (created via SeedHierarchy)
//	  And an active standard run whose branch is checked out from main
//	When the artifact is updated on the run branch via WriteContext
//	Then reading from the branch returns the updated content
//	And reading from HEAD still returns the original content
func TestArtifactMutation_UpdateOnRunBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "artifact-update-on-run-branch",
		Description: "artifact.Update with WriteContext modifies branch only; HEAD unchanged",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-MUT1", "EPIC-MUT1", "TASK-MUT"),
			scenarioEngine.SyncProjections(),

			// Start run — branch is created from HEAD.
			scenarioEngine.StartRun("initiatives/init-mut1/epics/epic-mut1/tasks/task-mut.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(), // stores "branch_name" in state

			// Read the original artifact from HEAD before any branch mutation.
			{
				Name: "capture-original-content",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					art, err := sc.Runtime.Artifacts.Read(sc.Ctx, taskPath, "HEAD")
					if err != nil {
						return fmt.Errorf("read original: %w", err)
					}
					sc.Set("original_title", art.Title)
					return nil
				},
			},

			// Update the artifact on the run branch using WriteContext.
			{
				Name: "update-artifact-on-run-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: branchName})
					_, err := sc.Runtime.Artifacts.Update(ctx, taskPath, updatedTaskContent(taskPath, epicPath, initPath))
					if err != nil {
						return fmt.Errorf("update artifact on branch: %w", err)
					}
					return nil
				},
			},

			// Assert: branch has the updated title.
			{
				Name: "assert-branch-has-updated-content",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					art, err := sc.Runtime.Artifacts.Read(sc.Ctx, taskPath, branchName)
					if err != nil {
						return fmt.Errorf("read from branch: %w", err)
					}
					if art.Title != "Updated Task Title" {
						return fmt.Errorf("branch: expected title 'Updated Task Title', got %q", art.Title)
					}
					return nil
				},
			},

			// Assert: HEAD still has the original content — update was branch-scoped.
			{
				Name: "assert-head-retains-original-content",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					originalTitle := sc.MustGet("original_title").(string)

					art, err := sc.Runtime.Artifacts.Read(sc.Ctx, taskPath, "HEAD")
					if err != nil {
						return fmt.Errorf("read from HEAD: %w", err)
					}
					if art.Title != originalTitle {
						return fmt.Errorf("HEAD: expected original title %q, got %q", originalTitle, art.Title)
					}
					return nil
				},
			},
		},
	})
}

// TestArtifactMutation_UpdateMergesToMain verifies the full lifecycle:
// an artifact updated on a run branch is present on main after the branch
// is merged via the commit step.
//
// Scenario: Branch-scoped update reaches main after merge
//
//	Given an artifact on main
//	  And a run branch with an updated version of that artifact
//	When the workflow completes through a commit step
//	Then the merged main contains the updated artifact content
func TestArtifactMutation_UpdateMergesToMain(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "artifact-update-merges-to-main",
		Description: "Artifact updated on run branch is present on main after workflow merge",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-MUT2", "EPIC-MUT2", "TASK-MUT"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-mut2/epics/epic-mut2/tasks/task-mut.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			// Update the task artifact on the run branch.
			{
				Name: "update-artifact-on-run-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: branchName})
					_, err := sc.Runtime.Artifacts.Update(ctx, taskPath, updatedTaskContent(taskPath, epicPath, initPath))
					if err != nil {
						return fmt.Errorf("update artifact on branch: %w", err)
					}
					return nil
				},
			},

			// Progress through the workflow: execute → review → accepted (triggers merge).
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),
			scenarioEngine.SubmitStepResult("accepted"),
			scenarioEngine.AssertRunCompleted(),

			// Assert: main now contains the updated content (branch was merged).
			{
				Name: "assert-main-has-updated-content-after-merge",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					art, err := sc.Runtime.Artifacts.Read(sc.Ctx, taskPath, "HEAD")
					if err != nil {
						return fmt.Errorf("read from HEAD after merge: %w", err)
					}
					if art.Title != "Updated Task Title" {
						return fmt.Errorf("expected 'Updated Task Title' on main after merge, got %q", art.Title)
					}
					if !strings.Contains(art.Title, "Updated") {
						return fmt.Errorf("merged artifact does not reflect branch update")
					}
					return nil
				},
			},

			// Branch is cleaned up after merge.
			scenarioEngine.AssertBranchNotExists(),
		},
	})
}

// TestArtifactMutation_ReadThroughOnRunBranch verifies that artifacts which
// exist on main but were not modified on the run branch are readable via the
// branch ref. Since run branches are created from HEAD, all main artifacts
// are present on the branch; reading an unmodified artifact through the branch
// ref should not produce a 404.
//
// Scenario: Unmodified main artifact is readable via run branch ref
//
//	Given an epic and task hierarchy on main
//	  And an active run for the task
//	When the epic is read using the run branch as ref
//	Then the epic is returned (not a 404) with the main content
func TestArtifactMutation_ReadThroughOnRunBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "artifact-read-through-on-run-branch",
		Description: "Unmodified main artifacts are readable via the run branch ref",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-MUT3", "EPIC-MUT3", "TASK-MUT"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-mut3/epics/epic-mut3/tasks/task-mut.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			// Read the epic (not the task that the run is for) via the branch ref.
			// The epic was never modified on this branch — it comes from main.
			{
				Name: "assert-unmodified-epic-readable-via-branch-ref",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					epicPath := sc.MustGet("epic_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					art, err := sc.Runtime.Artifacts.Read(sc.Ctx, epicPath, branchName)
					if err != nil {
						return fmt.Errorf("read epic from branch ref %q: %w", branchName, err)
					}
					if art.Type != domain.ArtifactTypeEpic {
						return fmt.Errorf("expected Epic, got %s", art.Type)
					}
					return nil
				},
			},
		},
	})
}

// TestArtifactMutation_LinkRemovalMergesToMain verifies that removing a
// blocked_by link from an artifact on a run branch propagates to main after
// the branch is merged via the commit step.
//
// Scenario: blocked_by link removed on branch is absent from main after merge
//
//	Given Task B on main with a blocked_by link to the completed Task A
//	  And an active run for Task B (startable because Task A is Completed)
//	When Task B is updated on the run branch to remove the blocked_by link
//	And the workflow completes through a commit step
//	Then Task B on main has no blocked_by links
func TestArtifactMutation_LinkRemovalMergesToMain(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "artifact-link-removal-merges-to-main",
		Description: "blocked_by link removed on run branch is absent from main after workflow merge",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),

			// Build a hierarchy with Task A (completed blocker) and Task B
			// (blocked_by A). Task B is the run target; StartRun succeeds
			// because its only blocker is already terminal.
			{
				Name: "seed-hierarchy-with-blocker",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					initPath := "initiatives/init-mut4/initiative.md"
					epicPath := "initiatives/init-mut4/epics/epic-mut4/epic.md"
					canonicalInit := "/" + initPath
					canonicalEpic := "/" + epicPath

					scenarioEngine.FixtureInitiative(sc, initPath, scenarioEngine.ArtifactOpts{ID: "INIT-MUT4"})
					scenarioEngine.FixtureEpic(sc, epicPath, scenarioEngine.ArtifactOpts{
						ID:   "EPIC-MUT4",
						Init: canonicalInit,
					})

					// Task A: completed blocker.
					taskAPath := "initiatives/init-mut4/epics/epic-mut4/tasks/task-mut4-a.md"
					scenarioEngine.FixtureTask(sc, taskAPath, scenarioEngine.ArtifactOpts{
						ID:     "TASK-MUT4-A",
						Status: "Completed",
						Epic:   canonicalEpic,
						Init:   canonicalInit,
					})

					// Task B: blocked_by Task A (but blocker is complete, so run is startable).
					taskBPath := "initiatives/init-mut4/epics/epic-mut4/tasks/task-mut4-b.md"
					scenarioEngine.FixtureTask(sc, taskBPath, scenarioEngine.ArtifactOpts{
						ID:     "TASK-MUT4-B",
						Status: "Pending",
						Epic:   canonicalEpic,
						Init:   canonicalInit,
						Links: []scenarioEngine.LinkOpt{
							{Type: "blocked_by", Target: "/" + taskAPath},
						},
					})

					sc.Set("init_path", initPath)
					sc.Set("epic_path", epicPath)
					sc.Set("task_path", taskBPath)
					return nil
				},
			},

			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-mut4/epics/epic-mut4/tasks/task-mut4-b.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			// Update Task B on the branch to remove the blocked_by link.
			{
				Name: "remove-blocked-by-link-on-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: branchName})
					_, err := sc.Runtime.Artifacts.Update(ctx, taskPath, taskContentWithoutBlockerLink(taskPath, epicPath, initPath))
					if err != nil {
						return fmt.Errorf("remove link on branch: %w", err)
					}
					return nil
				},
			},

			// Progress through the workflow and merge.
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),
			scenarioEngine.SubmitStepResult("accepted"),
			scenarioEngine.AssertRunCompleted(),

			// Rebuild projections from the merged HEAD so the store reflects
			// the branch's link removal.
			scenarioEngine.SyncProjections(),

			// Assert: artifact content on main has no blocked_by link.
			{
				Name: "assert-blocked-by-link-absent-in-artifact-after-merge",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					art, err := sc.Runtime.Artifacts.Read(sc.Ctx, taskPath, "HEAD")
					if err != nil {
						return fmt.Errorf("read Task B from HEAD after merge: %w", err)
					}
					for _, link := range art.Links {
						if link.Type == domain.LinkTypeBlockedBy {
							return fmt.Errorf("expected no blocked_by link in artifact on main, got: %s", link.Target)
						}
					}
					return nil
				},
			},

			// Assert: projection also has no blocked_by link (verifies the
			// sync rebuilt the relationship table from the merged content).
			{
				Name: "assert-blocked-by-link-absent-in-projection-after-merge",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					links, err := sc.Runtime.Store.QueryArtifactLinks(sc.Ctx, taskPath)
					if err != nil {
						return fmt.Errorf("query projection links for Task B: %w", err)
					}
					for _, link := range links {
						if link.LinkType == string(domain.LinkTypeBlockedBy) {
							return fmt.Errorf("expected no blocked_by in projection after merge, got: %s → %s", link.SourcePath, link.TargetPath)
						}
					}
					return nil
				},
			},

			scenarioEngine.AssertBranchNotExists(),
		},
	})
}

// TestArtifactMutation_BranchConflictWithMain verifies that when the same
// artifact is updated both on the run branch and directly on main, the merge
// step returns an error rather than silently overwriting one version.
//
// Scenario: Conflicting updates on branch and main surface as a merge error
//
//	Given an artifact updated on the run branch
//	  And the same artifact updated directly on main (diverging from the common ancestor)
//	When the workflow's commit step attempts to merge the branch
//	Then an error containing "conflict" is returned
func TestArtifactMutation_BranchConflictWithMain(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "artifact-branch-conflict-with-main",
		Description: "Conflicting artifact updates on branch and main produce a merge error",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedMutationWorkflow(),
			scenarioEngine.SeedHierarchy("INIT-MUT5", "EPIC-MUT5", "TASK-MUT"),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-mut5/epics/epic-mut5/tasks/task-mut.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertBranchExists(),

			// Update the artifact on the run branch.
			{
				Name: "update-artifact-on-branch",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)
					branchName := sc.MustGet("branch_name").(string)

					ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: branchName})
					_, err := sc.Runtime.Artifacts.Update(ctx, taskPath, updatedTaskContent(taskPath, epicPath, initPath))
					if err != nil {
						return fmt.Errorf("update on branch: %w", err)
					}
					return nil
				},
			},

			// Commit a diverging version of the same file directly to main.
			// This creates a conflict: both main and the branch have changed
			// the same file from the same common ancestor.
			{
				Name: "commit-conflicting-update-directly-to-main",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					taskPath := sc.MustGet("task_path").(string)
					epicPath := sc.MustGet("epic_path").(string)
					initPath := sc.MustGet("init_path").(string)

					sc.Repo.CheckoutBranch(sc.T, "main")
					sc.Repo.WriteArtifact(sc.T, taskPath, mainConflictContent(taskPath, epicPath, initPath))
					sc.Repo.CommitAll(sc.T, "direct main update — creates branch/main divergence")
					return nil
				},
			},

			// Advance through the execute step — no merge yet.
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),

			// Submit "accepted" — CompleteRun transitions to committing and
			// immediately calls MergeRunBranch. The conflict causes
			// failRunOnMergeError to mark the run as failed. IngestResult itself
			// does NOT return the merge error; it returns nil.
			scenarioEngine.SubmitStepResult("accepted"),

			// Assert: the merge conflict transitioned the run to failed.
			{
				Name: "assert-run-failed-due-to-merge-conflict",
				Action: func(sc *scenarioEngine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return fmt.Errorf("get run: %w", err)
					}
					if run.Status != domain.RunStatusFailed {
						return fmt.Errorf("expected run status 'failed' after merge conflict, got %q", run.Status)
					}
					return nil
				},
			},
		},
	})
}
