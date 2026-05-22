//go:build scenario

package scenarios_test

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/repository"
	scenarioEngine "github.com/bszymi/spine/scenariotest/engine"
	"github.com/bszymi/spine/scenariotest/harness"
)

// TestMultiRepoRunLifecycle_AnchorsTASK006 is the scenario-level AC anchor
// for INIT-014/EPIC-004/TASK-006 (multi-repo run lifecycle tests).
//
// Per-repo branch creation, baseline capture, partial-failure rollback,
// step routing resolution, and assignment payload shape are exhaustively
// covered at the unit layer:
//
//   - internal/engine/multi_repo_branch_test.go — primary-only,
//     single-code, multi-code, and 6 partial-failure rollback variants.
//   - internal/engine/step_routing_test.go — explicit / default-spine /
//     opt-in / unknown / inactive / bad-format / multi-code-no-decl
//     resolution rules per ADR-015.
//   - internal/engine/assignment_payload_test.go — workspace_id +
//     commit_baseline omitempty + non-execution-step tolerance.
//   - internal/scenariotest/scenarios/runner_clone_context_test.go —
//     end-to-end runner clone driven by an AssignmentContext.
//
// Those unit tests stub the per-repo git clients with in-memory fakes;
// they prove the engine's logic but not that the surrounding wiring
// (engine → store → real git CLI → on-disk refs) holds together. This
// scenario consolidates them by:
//
//   - standing up two real on-disk code repos (git init + initial
//     commit) alongside the scenario's primary spine repo;
//   - declaring `repositories: [billing, shipping]` on the task and
//     `repository: billing` on the entry step;
//   - calling Orchestrator.StartRun and asserting that every affected
//     repo received the run branch with `git rev-parse refs/heads/<name>`,
//     that runtime.runs.affected_repositories + repository_baselines
//     round-trip through Postgres, and that the entry step execution
//     row carries the explicit RepositoryID;
//   - calling Orchestrator.CleanupRunBranch and asserting the branch
//     is gone from every repo.
//
// Coverage map (TASK-006 deliverable list):
//
//   - "Primary-only task" — covered by unit
//     (TestStartRun_SingleRepoTaskCreatesOneBranch in
//     multi_repo_branch_test.go) and scenario
//     (TestSingleRepoBackcompat_TaskLifecycleNoCatalog in
//     repository_single_repo_backcompat_test.go).
//   - "Single code repo task" — covered by unit
//     (TestStartRun_MultiRepoCapturesPerRepoBaselines, payments-service).
//   - "Multiple code repo task" — covered by unit
//     (TestStartRun_MultiRepoCreatesBranchPerRepo, payments + api-gateway)
//     and pinned at scenario level by THIS test (billing + shipping).
//   - "Branch creation cleanup on failure" — covered exhaustively by
//     unit (6 partial-failure variants in multi_repo_branch_test.go).
//   - "Explicit step routing" — covered by unit (step_routing_test.go's
//     AC_i_ii_iii) and pinned at scenario level by THIS test asserting
//     EntryStep.RepositoryID == "billing".
//   - "Ambiguous step routing" — covered by unit
//     (step_routing_test.go's AC_viii / SpineExceptionPasses).
//   - "Runner clone context" — covered by scenario
//     (runner_clone_context_test.go).
func TestMultiRepoRunLifecycle_AnchorsTASK006(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "multi-repo-run-lifecycle",
		Description: "Multi-repo run lifecycle: branches in every affected repo, payload routing, cleanup symmetry",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupMultiRepoOrchestrator(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				multiRepoRunLifecycleWorkflowYAML,
				"seed multi-repo task workflow",
			),
			seedMultiRepoTaskHierarchy(),
			scenarioEngine.SyncProjections(),
			startMultiRepoRunAndAssertBranches(),
			cleanupMultiRepoRunAndAssertGone(),
		},
	})
}

// multiRepoRunLifecycleWorkflowYAML defines a workflow with:
//   - explicit per-step routing on the entry step (`repository: billing`)
//     so the persisted execution row's RepositoryID anchors the
//     "explicit step routing" coverage point.
//   - implicit (default-spine) routing on the review step so the
//     workflow round-trips both ADR-015 resolution branches.
//
// The terminal commit is intentionally not exercised here — this
// scenario covers run START + branch fanout + cleanup, not merge.
const multiRepoRunLifecycleWorkflowYAML = `id: task-default
name: Multi-Repo Lifecycle Test Workflow
version: "1.0"
status: Active
description: Multi-repo run lifecycle scenario workflow.
applies_to:
  - Task
entry_step: build
steps:
  - id: build
    name: Build in code repo
    repository: billing
    type: manual
    outcomes:
      - id: completed
        name: Build complete
        next_step: review
    timeout: "4h"

  - id: review
    name: Review in spine
    type: review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
        commit:
          status: Completed
    timeout: "24h"
`

// multiRepoTaskFrontmatter declares both code repos in the task's
// `repositories:` list — required for the workflow's `repository:
// billing` step to validate as opted-in per ADR-015 AC (vi).
const multiRepoTaskFrontmatter = `---
id: TASK-001
type: Task
title: "Multi-Repo Lifecycle Task"
status: Pending
epic: /initiatives/init-901/epics/epic-901/epic.md
initiative: /initiatives/init-901/initiative.md
repositories:
  - billing
  - shipping
links:
  - type: parent
    target: /initiatives/init-901/epics/epic-901/epic.md
---

# Multi-Repo Lifecycle Task
`

// setupMultiRepoOrchestrator wires two real on-disk code repos onto
// sc.Runtime.Orchestrator via harness.WithCodeRepos. Without this step
// the scenario harness's default orchestrator has neither
// RepositoryResolver nor RepositoryGitClients populated and a
// multi-repo run would silently degrade to primary-only.
//
// ParentT-anchoring is delegated to harness.WithCodeRepos: per-step
// subtest tempdirs would be torn down when the step ends, orphaning
// the working trees the run branches must land in during a later step.
//
// State keys set:
//   - "billing_dir"   — filesystem path of the billing code repo
//   - "shipping_dir"  — filesystem path of the shipping code repo
func setupMultiRepoOrchestrator() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-multi-repo-orchestrator",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator, sc.Repo,
				harness.CodeRepoSpec{ID: "billing"},
				harness.CodeRepoSpec{ID: "shipping"},
			)
			sc.Set("billing_dir", repos["billing"].Dir)
			sc.Set("shipping_dir", repos["shipping"].Dir)
			return nil
		},
	}
}

// seedMultiRepoTaskHierarchy seeds the Initiative + Epic via the
// standard fixture builders, then writes the Task artifact directly so
// the frontmatter can carry `repositories: [billing, shipping]` — a
// field the FixtureTask helper does not expose.
func seedMultiRepoTaskHierarchy() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "seed-multi-repo-task-hierarchy",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			scenarioEngine.FixtureInitiative(sc, "initiatives/init-901/initiative.md", scenarioEngine.ArtifactOpts{ID: "INIT-901"})
			scenarioEngine.FixtureEpic(sc, "initiatives/init-901/epics/epic-901/epic.md", scenarioEngine.ArtifactOpts{
				ID:   "EPIC-901",
				Init: "/initiatives/init-901/initiative.md",
			})
			taskPath := "initiatives/init-901/epics/epic-901/tasks/task-001.md"
			if _, err := sc.Runtime.Artifacts.Create(sc.Ctx, taskPath, multiRepoTaskFrontmatter); err != nil {
				return fmt.Errorf("create multi-repo task: %w", err)
			}
			sc.Set("task_path", taskPath)
			return nil
		},
	}
}

// startMultiRepoRunAndAssertBranches calls Orchestrator.StartRun and
// asserts the run-startup invariants TASK-006 promises:
//
//   - Run.AffectedRepositories is exactly [spine, billing, shipping].
//   - Run.RepositoryBaselines has a non-empty SHA for every entry.
//   - Branch named Run.BranchName exists in every repo's working tree
//     (verified via `git rev-parse refs/heads/<name>` on disk).
//   - Postgres roundtrip preserves AffectedRepositories +
//     RepositoryBaselines.
//   - The entry step execution row's RepositoryID matches the
//     workflow's explicit `repository: billing` declaration.
//
// State keys set:
//   - "run_id"      — the started run's ID (used by the cleanup step)
//   - "branch_name" — the run branch name (asserted to be deleted later)
func startMultiRepoRunAndAssertBranches() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "start-run-assert-branches",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			taskPath := sc.MustGet("task_path").(string)
			result, err := sc.Runtime.Orchestrator.StartRun(sc.Ctx, taskPath)
			if err != nil {
				return fmt.Errorf("StartRun: %w", err)
			}

			run := result.Run
			wantAffected := []string{repository.PrimaryRepositoryID, "billing", "shipping"}
			if !sameStringSlice(run.AffectedRepositories, wantAffected) {
				return fmt.Errorf("AffectedRepositories: got %v, want %v", run.AffectedRepositories, wantAffected)
			}
			for _, id := range wantAffected {
				if sha := run.RepositoryBaselines[id]; sha == "" {
					return fmt.Errorf("RepositoryBaselines[%q]: empty (got map %v)", id, run.RepositoryBaselines)
				}
			}

			// Branch must exist on disk in every affected repo. The
			// scenario's primary repo lives at sc.Repo.Dir; the code
			// repos live at the dirs the setup step stashed.
			repoDirs := map[string]string{
				repository.PrimaryRepositoryID: sc.Repo.Dir,
				"billing":                      sc.MustGet("billing_dir").(string),
				"shipping":                     sc.MustGet("shipping_dir").(string),
			}
			for repoID, dir := range repoDirs {
				if err := assertBranchExistsAt(dir, run.BranchName); err != nil {
					return fmt.Errorf("repo %s: %w", repoID, err)
				}
			}

			// Postgres roundtrip — the run row was just persisted by
			// CreateRun. Re-read it through the public store API and
			// confirm both multi-repo fields survive the schema layer.
			persisted, err := sc.Runtime.Store.GetRun(sc.Ctx, run.RunID)
			if err != nil {
				return fmt.Errorf("GetRun: %w", err)
			}
			if !sameStringSlice(persisted.AffectedRepositories, wantAffected) {
				return fmt.Errorf("persisted AffectedRepositories: got %v, want %v",
					persisted.AffectedRepositories, wantAffected)
			}
			for _, id := range wantAffected {
				if persisted.RepositoryBaselines[id] != run.RepositoryBaselines[id] {
					return fmt.Errorf("persisted RepositoryBaselines[%q]: got %q, want %q",
						id, persisted.RepositoryBaselines[id], run.RepositoryBaselines[id])
				}
			}

			// Explicit step routing: the entry step declares
			// `repository: billing`, so the persisted execution row's
			// RepositoryID must be `billing`. A regression that defaults
			// to `spine` (the implicit fallback) would silently route
			// the runner to the wrong repo. Re-read the row through the
			// public store API rather than asserting on the in-memory
			// return — runners consume the persisted record, and a
			// CreateStepExecution / scan-path regression that defaulted
			// the column would otherwise pass with the in-memory check
			// alone.
			persistedExec, err := sc.Runtime.Store.GetStepExecution(sc.Ctx, result.EntryStep.ExecutionID)
			if err != nil {
				return fmt.Errorf("GetStepExecution: %w", err)
			}
			if got := persistedExec.RepositoryID; got != "billing" {
				return fmt.Errorf("persisted EntryStep.RepositoryID: got %q, want %q (explicit routing)", got, "billing")
			}

			sc.Set("run_id", run.RunID)
			sc.Set("branch_name", run.BranchName)
			return nil
		},
	}
}

// cleanupMultiRepoRunAndAssertGone calls Orchestrator.CleanupRunBranch
// and asserts the run branch is removed from every affected repo. With
// no merge outcomes recorded (the run never progressed past assignment),
// preservedRepoBranches returns an empty set, so every repo's branch
// gets deleted — the symmetric counterpart of branch creation.
func cleanupMultiRepoRunAndAssertGone() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "cleanup-and-assert-branches-gone",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			branchName := sc.MustGet("branch_name").(string)
			if err := sc.Runtime.Orchestrator.CleanupRunBranch(sc.Ctx, runID); err != nil {
				return fmt.Errorf("CleanupRunBranch: %w", err)
			}
			repoDirs := map[string]string{
				repository.PrimaryRepositoryID: sc.Repo.Dir,
				"billing":                      sc.MustGet("billing_dir").(string),
				"shipping":                     sc.MustGet("shipping_dir").(string),
			}
			for repoID, dir := range repoDirs {
				if err := assertBranchAbsentAt(dir, branchName); err != nil {
					return fmt.Errorf("repo %s post-cleanup: %w", repoID, err)
				}
			}
			return nil
		},
	}
}

// assertBranchExistsAt runs `git rev-parse --verify refs/heads/<name>`
// in repoDir and returns an error if the ref is missing.
func assertBranchExistsAt(repoDir, branchName string) error {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "refs/heads/"+branchName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("branch %q not found in %s: %v\n%s", branchName, filepath.Base(repoDir), err, out)
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("branch %q resolved to empty SHA in %s", branchName, filepath.Base(repoDir))
	}
	return nil
}

// assertBranchAbsentAt is the inverse of assertBranchExistsAt. A clean
// "branch deleted" outcome is exit 1 from `git rev-parse --verify
// --quiet refs/heads/<name>`. Exit 0 means the ref still exists (test
// fails). Any other exit (128 = repo missing / not a git dir, ENOENT =
// git binary unavailable) is propagated as an error so a teardown
// regression that removed the temp dir cannot masquerade as a
// successful cleanup.
func assertBranchAbsentAt(repoDir, branchName string) error {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branchName)
	err := cmd.Run()
	if err == nil {
		return fmt.Errorf("branch %q unexpectedly still exists in %s", branchName, filepath.Base(repoDir))
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return nil
	}
	return fmt.Errorf("git rev-parse --verify --quiet refs/heads/%s in %s: %w",
		branchName, filepath.Base(repoDir), err)
}

// sameStringSlice compares two string slices for element-wise equality.
// Used instead of reflect.DeepEqual to avoid the import for a one-call
// site and to keep the failure message explicit on length-or-content
// mismatch.
func sameStringSlice(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

