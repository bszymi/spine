//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	scenarioEngine "github.com/bszymi/spine/scenariotest/engine"
	"github.com/bszymi/spine/scenariotest/harness"
)

// TestCancelFromPartiallyMerged_AsymmetricCleanup is the scenario-level
// AC anchor for INIT-022 EPIC-001 TASK-008. It pins the asymmetric
// branch-cleanup contract documented in
// `architecture/multi-repository-integration.md §4.5` and
// `architecture/error-handling-and-recovery.md §5.4`:
//
//   - committing → partially-merged via `git.code_repo_partial_failure`
//     when the primary merge has landed and at least one code repo's
//     merge ended in a permanent-failed class — same setup TASK-006/007
//     exercise, extended to two code repos so one merges and one fails.
//   - while the run is parked, every affected repo's run branch stays
//     on disk per §4.5 ("While a run is in the partially-merged state,
//     no cleanup has run yet"). The scenario asserts the parked-state
//     shape before cancelling.
//   - `Orchestrator.CancelRun` flips the run to `cancelled` and invokes
//     `CleanupRunBranch`, which keys deletion off per-repo merge
//     outcomes:
//   - failed code repo (billing) — branch PRESERVED.
//   - merged code repo (shipping) — branch DELETED.
//   - primary (merged) — branch DELETED, same as any merged repo
//     per §4.5's "any other terminal outcome … gets its run branch
//     deleted" clause.
//
// Coverage map:
//
//   - Unit-level coverage exists at
//     `internal/engine/branch_cleanup_test.go::TestCleanupRunBranch_PreservesFailedBranches`
//     and friends, which prove the per-repo decision against fakes.
//     They do NOT prove that the operator-cancel path drives that
//     cleanup against real on-disk repos in the partially-merged exit.
//     This scenario consolidates them.
//   - Two code repos are required for the AC: a merged + a failed
//     side-by-side is the only configuration that proves the
//     asymmetric (not "all gone" / "none gone") cleanup contract.
//   - The retry exit is owned by TASK-006; the resolve-externally exit
//     by TASK-007. Each scenario pins one exit from partially-merged.
func TestCancelFromPartiallyMerged_AsymmetricCleanup(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "cancel-from-partially-merged-asymmetric-cleanup",
		Description: "Multi-repo run: code repo fails permanently → partially-merged → operator cancel → cleanup deletes merged branches and preserves failed",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupCancelOrchestrator(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				cancelFromPartiallyMergedWorkflowYAML,
				"seed cancel-from-partially-merged workflow",
			),
			seedCancelTaskHierarchy(),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-905/epics/epic-905/tasks/task-001.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),

			queueCancelBillingMergeFailure(),

			// SubmitStepResult("accepted") drives commit semantics:
			// IngestResult transitions the run to committing and
			// immediately invokes MergeRunBranch. Billing's queued failure
			// fires once, shipping merges successfully, primary merges,
			// and the run lands in partially-merged with one failed +
			// one merged code-repo outcome.
			scenarioEngine.SubmitStepResult("accepted"),

			assertCancelPartiallyMergedShape(),
			assertAllBranchesPreservedWhileParked(),

			cancelRunFromPartiallyMerged(),
			assertCancelRunStatusCancelled(),
			assertAsymmetricBranchCleanup(),
		},
	})
}

// cancelFromPartiallyMergedWorkflowYAML mirrors TASK-006/007's workflow
// shape: the entry step is in a code repo, the review step's `accepted`
// outcome carries `commit: status: Completed` so SubmitStepResult drives
// MergeRunBranch synchronously through the orchestrator's
// "immediate merge attempt" short-circuit. Routing the entry step at
// billing keeps the per-step routing assertion plausible (a code-repo
// step is the realistic shape an operator hits a partial merge on).
const cancelFromPartiallyMergedWorkflowYAML = `id: task-default
name: Cancel From Partially-Merged Test Workflow
version: "1.0"
status: Active
description: Multi-repo workflow with a commit step that drives MergeRunBranch.
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

// cancelFromPartiallyMergedTaskFrontmatter declares both billing and
// shipping as the run's affected repos. Two code repos are required for
// the AC's asymmetric-cleanup contract: a merged + a failed side-by-side
// is the only configuration that proves the cleanup decision is
// per-repo, not run-wide. ADR-015 also requires the workflow's
// `repository: billing` declaration to match an entry in this list.
const cancelFromPartiallyMergedTaskFrontmatter = `---
id: TASK-001
type: Task
title: "Cancel From Partially-Merged Task"
status: Pending
epic: /initiatives/init-905/epics/epic-905/epic.md
initiative: /initiatives/init-905/initiative.md
repositories:
  - billing
  - shipping
links:
  - type: parent
    target: /initiatives/init-905/epics/epic-905/epic.md
---

# Cancel From Partially-Merged Task
`

// setupCancelOrchestrator wires both code repos onto the orchestrator
// and stashes their handles so the failure-injection step can queue
// against the same wrapped client the engine resolves through. ParentT
// anchoring is delegated to harness.WithCodeRepos; using sc.T here would
// orphan the working trees once this step's subtest ends.
func setupCancelOrchestrator() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-cancel-orchestrator",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator, sc.Repo,
				harness.CodeRepoSpec{ID: "billing"},
				harness.CodeRepoSpec{ID: "shipping"},
			)
			sc.Set(stateCancelBillingRepo, repos["billing"])
			sc.Set(stateCancelShippingRepo, repos["shipping"])
			return nil
		},
	}
}

// seedCancelTaskHierarchy seeds initiative + epic + multi-repo task in
// the init-905/epic-905 namespace so the scenario stays independent of
// other multi-repo scenarios in the same package set (TASK-006 owns
// init-903; TASK-007 owns init-904).
func seedCancelTaskHierarchy() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "seed-cancel-task-hierarchy",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			scenarioEngine.FixtureInitiative(sc, "initiatives/init-905/initiative.md", scenarioEngine.ArtifactOpts{ID: "INIT-905"})
			scenarioEngine.FixtureEpic(sc, "initiatives/init-905/epics/epic-905/epic.md", scenarioEngine.ArtifactOpts{
				ID:   "EPIC-905",
				Init: "/initiatives/init-905/initiative.md",
			})
			taskPath := "initiatives/init-905/epics/epic-905/tasks/task-001.md"
			if _, err := sc.Runtime.Artifacts.Create(sc.Ctx, taskPath, cancelFromPartiallyMergedTaskFrontmatter); err != nil {
				return fmt.Errorf("create cancel-from-partially-merged task: %w", err)
			}
			return nil
		},
	}
}

// queueCancelBillingMergeFailure injects a single-shot permanent merge
// conflict on billing's next merge attempt. Shipping's wrapped client
// is left untouched — its merge will fall through to the underlying CLI
// client and succeed, producing the merged + failed pair the
// asymmetric-cleanup AC requires.
//
// Same shape TASK-006 / TASK-007 use: `Kind: ErrKindPermanent`,
// `Message: "merge conflict"`. classifyMergeFailure returns
// MergeFailureConflict (permanent, IsTransient()=false) so the per-repo
// loop sees a terminal-failed entry and `firstPermanentCodeRepoFailure`
// drives `transitionToPartiallyMerged` after the primary merges.
func queueCancelBillingMergeFailure() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "queue-billing-merge-failure",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			billing := sc.MustGet(stateCancelBillingRepo).(*harness.CodeRepo)
			billing.SetNextMergeFailure(&git.GitError{
				Kind:    git.ErrKindPermanent,
				Op:      "merge",
				Message: "merge conflict",
			})
			return nil
		},
	}
}

// assertCancelPartiallyMergedShape pins the post-failure invariants
// documented in error-handling-and-recovery.md §5.4: the run is in
// partially-merged, the primary outcome has landed merged with a
// non-empty MergeCommitSHA, shipping's outcome is merged (so cleanup
// has a "delete me" candidate), and billing's outcome is failed with
// FailureClass=merge_conflict (so cleanup has a "preserve me"
// candidate). Without this triple, the asymmetric-cleanup assertion
// later would not be meaningful.
func assertCancelPartiallyMergedShape() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-partially-merged-shape",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.Status != domain.RunStatusPartiallyMerged {
				return fmt.Errorf("run status: got %s, want partially-merged", run.Status)
			}
			sc.Set(stateCancelRunBranch, run.BranchName)

			outcomes, err := sc.Runtime.Store.ListRepositoryMergeOutcomes(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list outcomes: %w", err)
			}
			byRepo := outcomesByRepo(outcomes)

			primary, ok := byRepo[repository.PrimaryRepositoryID]
			if !ok {
				return fmt.Errorf("primary outcome missing — partially-merged invariant requires primary already merged")
			}
			if primary.Status != domain.RepositoryMergeStatusMerged {
				return fmt.Errorf("primary outcome status: got %s, want merged", primary.Status)
			}
			if primary.MergeCommitSHA == "" {
				return fmt.Errorf("primary MergeCommitSHA empty on merged status")
			}

			shipping, ok := byRepo["shipping"]
			if !ok {
				return fmt.Errorf("shipping outcome missing — the asymmetric-cleanup AC requires a merged code-repo neighbour")
			}
			if shipping.Status != domain.RepositoryMergeStatusMerged {
				return fmt.Errorf("shipping outcome status: got %s, want merged", shipping.Status)
			}
			if shipping.MergeCommitSHA == "" {
				return fmt.Errorf("shipping MergeCommitSHA empty on merged status")
			}

			billing, ok := byRepo["billing"]
			if !ok {
				return fmt.Errorf("billing outcome missing — failed merge attempt should have recorded a row")
			}
			if billing.Status != domain.RepositoryMergeStatusFailed {
				return fmt.Errorf("billing outcome status: got %s, want failed", billing.Status)
			}
			if billing.FailureClass != domain.MergeFailureConflict {
				return fmt.Errorf("billing failure class: got %q, want %q", billing.FailureClass, domain.MergeFailureConflict)
			}
			return nil
		},
	}
}

// assertAllBranchesPreservedWhileParked pins the
// `multi-repository-integration.md §4.5` invariant: while the run is in
// partially-merged, every affected repo's run branch stays on disk so
// the operator can inspect both the merged work and the failed branch.
// Asserts presence on all three trees — a regression that fired
// CleanupRunBranch on the partial transition would delete one or more
// branches and fail this step before the cancel call ever happens.
func assertAllBranchesPreservedWhileParked() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-all-branches-preserved-while-parked",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runBranch := sc.MustGet(stateCancelRunBranch).(string)
			if runBranch == "" {
				return fmt.Errorf("run branch name is empty; partially-merged run must have a branch")
			}
			billing := sc.MustGet(stateCancelBillingRepo).(*harness.CodeRepo)
			shipping := sc.MustGet(stateCancelShippingRepo).(*harness.CodeRepo)
			if err := assertLocalBranchExists(billing.Dir, runBranch); err != nil {
				return fmt.Errorf("billing branch preservation while parked: %w", err)
			}
			if err := assertLocalBranchExists(shipping.Dir, runBranch); err != nil {
				return fmt.Errorf("shipping branch preservation while parked: %w", err)
			}
			if err := assertLocalBranchExists(sc.Repo.Dir, runBranch); err != nil {
				return fmt.Errorf("primary branch preservation while parked: %w", err)
			}
			return nil
		},
	}
}

// cancelRunFromPartiallyMerged invokes the operator-facing cancel surface
// the same way the gateway HTTP handler does: a *domain.Actor is bound
// to ctx via domain.WithActor before the orchestrator call, since the
// orchestrator authorisation paths assume an authenticated caller.
// CancelRun runs CleanupRunBranch internally, so by the time this step
// returns, the asymmetric-cleanup decision has already been made on
// disk.
func cancelRunFromPartiallyMerged() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "cancel-run-from-partially-merged",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			actor := &domain.Actor{
				ActorID: cancelActorID,
				Type:    domain.ActorTypeHuman,
				Role:    domain.RoleOperator,
				Status:  domain.ActorStatusActive,
			}
			ctx := domain.WithActor(sc.Ctx, actor)
			if err := sc.Runtime.Orchestrator.CancelRun(ctx, runID); err != nil {
				return fmt.Errorf("CancelRun: %w", err)
			}
			return nil
		},
	}
}

// assertCancelRunStatusCancelled pins the run-state side of the cancel
// exit: per `engine-state-machine.md §2.2`, partially-merged →
// cancelled is the operator-cancel transition. A regression in
// `workflow.EvaluateRunTransition` that rejected TriggerCancel from
// partially-merged would surface in CancelRun's error return — but
// CancelRun returns nil and silently leaves the run parked on the
// best-effort cleanup path, so the post-condition assertion is the
// load-bearing check.
func assertCancelRunStatusCancelled() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-status-cancelled",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run after cancel: %w", err)
			}
			if run.Status != domain.RunStatusCancelled {
				return fmt.Errorf("run status after cancel: got %s, want cancelled", run.Status)
			}
			return nil
		},
	}
}

// assertAsymmetricBranchCleanup is the load-bearing check for TASK-008.
// Per `multi-repository-integration.md §4.5`, CleanupRunBranch (called
// by CancelRun) keys deletion off per-repo merge outcomes:
//
//   - failed → preserved (so the operator can resolve against the
//     unmodified ref).
//   - merged / skipped / resolved-externally → deleted.
//
// The scenario sets up exactly this triple:
//
//   - billing (failed) — branch must remain on disk.
//   - shipping (merged) — branch must be gone.
//   - primary (merged) — branch must be gone (same rule as any merged
//     code repo per §4.5; primary is not exempt).
//
// The regression-bait check called out in TASK-008's AC is removing the
// `if outcome.Status == RepositoryMergeStatusFailed` conditional in
// `preservedRepoBranches`: with that gone, billing's outcome flows
// through the "delete" branch and `assertLocalBranchAbsent` fails on
// billing.Dir.
func assertAsymmetricBranchCleanup() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-asymmetric-branch-cleanup",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runBranch := sc.MustGet(stateCancelRunBranch).(string)
			billing := sc.MustGet(stateCancelBillingRepo).(*harness.CodeRepo)
			shipping := sc.MustGet(stateCancelShippingRepo).(*harness.CodeRepo)

			if err := assertLocalBranchExists(billing.Dir, runBranch); err != nil {
				return fmt.Errorf("billing (failed) branch must be preserved by cleanup: %w", err)
			}
			// assertBranchAbsentAt (defined in multi_repo_run_lifecycle_test.go)
			// distinguishes "ref not found" (exit 1, clean) from other git
			// errors (exit 128, ENOENT, etc.) so a teardown regression that
			// removed the working tree cannot masquerade as a successful
			// cleanup. Reused here rather than re-implementing the absence
			// check in this file.
			if err := assertBranchAbsentAt(shipping.Dir, runBranch); err != nil {
				return fmt.Errorf("shipping (merged) branch must be deleted by cleanup: %w", err)
			}
			if err := assertBranchAbsentAt(sc.Repo.Dir, runBranch); err != nil {
				return fmt.Errorf("primary (merged) branch must be deleted by cleanup per §4.5: %w", err)
			}
			return nil
		},
	}
}

// State keys — namespaced with `cancel_` so they cannot collide with
// keys other multi-repo scenarios in this package set on the shared
// ScenarioContext.
const (
	stateCancelBillingRepo  = "cancel_billing_repo"
	stateCancelShippingRepo = "cancel_shipping_repo"
	stateCancelRunBranch    = "cancel_run_branch"
)

// Operator-action constants. The actor ID is unique to this scenario so
// a regression that swapped operator IDs across cancel paths would not
// silently align with another scenario's pin.
const cancelActorID = "actor-op-cancel-1"
