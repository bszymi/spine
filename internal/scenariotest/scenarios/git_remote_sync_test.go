//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"strings"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// addRemote sets up a bare Git repository as "origin" and stores
// the bare directory path in scenario state for later assertions.
func addRemote() engine.Step {
	return engine.Step{
		Name: "add-bare-remote",
		Action: func(sc *engine.ScenarioContext) error {
			bare := sc.Repo.AddBareRemote(sc.T)
			sc.Set("bare_dir", bare)
			return nil
		},
	}
}

func assertRemoteHeadContains(branch, substring string) engine.Step {
	return engine.Step{
		Name: fmt.Sprintf("assert-remote-%s-contains-%s", branch, substring),
		Action: func(sc *engine.ScenarioContext) error {
			bare := sc.MustGet("bare_dir").(string)
			if !harness.RemoteHeadContains(sc.T, bare, branch, substring) {
				sc.T.Errorf("expected remote %s HEAD to contain %q", branch, substring)
			}
			return nil
		},
	}
}

func assertRemoteBranchExists(branch string) engine.Step {
	return engine.Step{
		Name: fmt.Sprintf("assert-remote-branch-exists-%s", branch),
		Action: func(sc *engine.ScenarioContext) error {
			bare := sc.MustGet("bare_dir").(string)
			if !harness.RemoteBranchExists(sc.T, bare, branch) {
				sc.T.Errorf("expected remote branch %s to exist", branch)
			}
			return nil
		},
	}
}

func assertRemoteBranchGone(branch string) engine.Step {
	return engine.Step{
		Name: fmt.Sprintf("assert-remote-branch-gone-%s", branch),
		Action: func(sc *engine.ScenarioContext) error {
			bare := sc.MustGet("bare_dir").(string)
			if harness.RemoteBranchExists(sc.T, bare, branch) {
				sc.T.Errorf("expected remote branch %s to be deleted", branch)
			}
			return nil
		},
	}
}

// --- Scenario 1: Artifact create on main pushes to origin ---

func TestGitRemoteSync_ArtifactCreateOnMain(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "artifact-create-pushes-to-origin",
		Description: "Artifact created on main is automatically pushed to origin",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []engine.Step{
			addRemote(),
			{
				Name: "create-artifact-on-main",
				Action: func(sc *engine.ScenarioContext) error {
					content := "---\ntype: Governance\ntitle: Remote Sync Test\nstatus: Living Document\nversion: \"0.1\"\n---\n# Remote Sync Test\n"
					_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/remote-sync-test.md", content)
					return err
				},
			},
			assertRemoteHeadContains("main", "Create Governance"),
		},
	})
}

// --- Scenario 2: Planning run start pushes branch to origin ---

func TestGitRemoteSync_PlanningRunBranchPushed(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "planning-run-branch-pushed-to-origin",
		Description: "Planning run branch appears on origin after creation",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			addRemote(),
			seedCreationWorkflow(),
			engine.SyncProjections(),
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			{
				Name: "verify-branch-on-remote",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					bare := sc.MustGet("bare_dir").(string)
					if !harness.RemoteBranchExists(sc.T, bare, run.BranchName) {
						sc.T.Errorf("expected branch %s on remote", run.BranchName)
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 3: Artifact create via WriteContext pushes to branch on origin ---

func TestGitRemoteSync_ArtifactOnBranchPushed(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "artifact-on-branch-pushed-to-origin",
		Description: "Artifact created on run branch is pushed to the branch on origin",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			addRemote(),
			seedCreationWorkflow(),
			engine.SyncProjections(),
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			{
				Name: "create-child-artifact-on-branch",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					content := "---\ntype: Governance\ntitle: Branch Artifact\nstatus: Living Document\nversion: \"0.1\"\n---\n# Branch Artifact\n"
					ctx := artifact.WithWriteContext(sc.Ctx, artifact.WriteContext{Branch: run.BranchName})
					_, err = sc.Runtime.Artifacts.Create(ctx, "governance/branch-artifact.md", content)
					return err
				},
			},
			{
				Name: "verify-commit-on-remote-branch",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					bare := sc.MustGet("bare_dir").(string)
					if !harness.RemoteHeadContains(sc.T, bare, run.BranchName, "Create Governance") {
						sc.T.Errorf("expected branch artifact commit on remote branch %s", run.BranchName)
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 4: Run approval + merge pushes main, deletes branch on origin ---

func TestGitRemoteSync_MergeAndCleanup(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "merge-pushes-main-deletes-branch",
		Description: "After merge, main is pushed and run branch is deleted on origin",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			addRemote(),
			seedCreationWorkflow(),
			engine.SyncProjections(),
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			// Progress through workflow: draft → validate → review → commit → merge
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.SubmitStepResult("valid"),
			engine.SubmitStepResult("approved"),
			engine.AssertRunStatus(domain.RunStatusCommitting),
			engine.MergeRunBranch(),
			engine.AssertRunCompleted(),
			{
				Name: "verify-main-pushed-and-branch-deleted",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					bare := sc.MustGet("bare_dir").(string)
					// Main should contain the merge commit
					if !harness.RemoteHeadContains(sc.T, bare, "main", "Merge run") {
						sc.T.Errorf("expected merge commit on remote main")
					}
					// Branch should be deleted
					if harness.RemoteBranchExists(sc.T, bare, run.BranchName) {
						sc.T.Errorf("expected branch %s deleted from remote", run.BranchName)
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 5: Run cancellation deletes branch on origin ---

func TestGitRemoteSync_CancelDeletesBranch(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "cancel-deletes-branch-on-origin",
		Description: "Cancelling a run deletes its branch on origin",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			addRemote(),
			seedCreationWorkflow(),
			engine.SyncProjections(),
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			{
				Name: "store-branch-name",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					sc.Set("branch_name", run.BranchName)
					return nil
				},
			},
			engine.CancelRun(),
			engine.AssertRunStatus(domain.RunStatusCancelled),
			{
				Name: "verify-branch-deleted-on-remote",
				Action: func(sc *engine.ScenarioContext) error {
					branch := sc.MustGet("branch_name").(string)
					bare := sc.MustGet("bare_dir").(string)
					if harness.RemoteBranchExists(sc.T, bare, branch) {
						sc.T.Errorf("expected branch %s deleted from remote after cancel", branch)
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 6: Auto-push disabled → nothing pushed ---

func TestGitRemoteSync_AutoPushDisabled(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	engine.RunScenario(t, engine.Scenario{
		Name:        "auto-push-disabled-nothing-pushed",
		Description: "When SPINE_GIT_AUTO_PUSH=false, no Git operations are pushed to origin",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
		},
		Steps: []engine.Step{
			addRemote(),
			{
				Name: "create-artifact-with-push-disabled",
				Action: func(sc *engine.ScenarioContext) error {
					content := "---\ntype: Governance\ntitle: No Push Test\nstatus: Living Document\nversion: \"0.1\"\n---\n# No Push Test\n"
					_, err := sc.Runtime.Artifacts.Create(sc.Ctx, "governance/no-push.md", content)
					return err
				},
			},
			{
				Name: "verify-nothing-pushed",
				Action: func(sc *engine.ScenarioContext) error {
					bare := sc.MustGet("bare_dir").(string)
					if harness.RemoteHeadContains(sc.T, bare, "main", "Create Governance") {
						sc.T.Errorf("expected no push when SPINE_GIT_AUTO_PUSH=false")
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 7: Planning run branch name follows convention ---

func TestGitRemoteSync_PlanningRunBranchNaming(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "planning-run-branch-naming",
		Description: "Planning run branch follows spine/plan/<id>-<slug>-<hex> convention",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			addRemote(),
			seedCreationWorkflow(),
			engine.SyncProjections(),
			engine.StartPlanningRun(
				"initiatives/init-099/initiative.md",
				testInitiativeContent,
			),
			{
				Name: "verify-planning-branch-name",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					branch := run.BranchName
					if !strings.HasPrefix(branch, "spine/plan/init-099-") {
						sc.T.Errorf("expected branch to start with spine/plan/init-099-, got %s", branch)
					}
					if !strings.Contains(branch, "initiative") {
						sc.T.Errorf("expected branch to contain 'initiative' slug, got %s", branch)
					}
					return nil
				},
			},
		},
	})
}

// --- Scenario 8: Standard run branch name follows convention ---

const testTaskContent = `---
id: TASK-001
type: Task
title: Test Task for Naming
status: Pending
epic: /initiatives/init-099/epics/epic-001/epic.md
initiative: /initiatives/init-099/initiative.md
created: 2026-01-01
last_updated: 2026-01-01
links:
  - type: parent
    target: /initiatives/init-099/epics/epic-001/epic.md
---
# TASK-001 — Test Task for Naming
`

func TestGitRemoteSync_StandardRunBranchNaming(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "standard-run-branch-naming",
		Description: "Standard run branch follows spine/run/<id>-<slug>-<hex> convention",
		EnvOpts: []harness.EnvOption{
			harness.WithWorkflows(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			addRemote(),
			engine.WriteAndCommit(
				"initiatives/init-099/epics/epic-001/tasks/task-001-naming-test.md",
				testTaskContent,
				"seed task artifact",
			),
			engine.SyncProjections(),
			engine.StartRun("initiatives/init-099/epics/epic-001/tasks/task-001-naming-test.md"),
			{
				Name: "verify-standard-branch-name",
				Action: func(sc *engine.ScenarioContext) error {
					runID := sc.MustGet("run_id").(string)
					run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
					if err != nil {
						return err
					}
					branch := run.BranchName
					if !strings.HasPrefix(branch, "spine/run/task-001-") {
						sc.T.Errorf("expected branch to start with spine/run/task-001-, got %s", branch)
					}
					if !strings.Contains(branch, "naming-test") {
						sc.T.Errorf("expected branch to contain 'naming-test' slug, got %s", branch)
					}
					return nil
				},
			},
		},
	})
}
