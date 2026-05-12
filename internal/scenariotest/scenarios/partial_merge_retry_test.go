//go:build scenario

package scenarios_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/scheduler"
)

// TestPartialMergeRetry_HappyPath is the scenario-level AC anchor for
// INIT-022 EPIC-001 TASK-006 (and the broader EPIC-005 TASK-003 + TASK-006
// recovery contract documented in
// architecture/error-handling-and-recovery.md §5.4 and
// architecture/engine-state-machine.md §2.2):
//
//   - committing → partially-merged via `git.code_repo_partial_failure`
//     when the primary merge has landed but a code repo's merge ended in
//     a permanent-failed class;
//   - the scheduler's `retryCommittingRuns` resume gate keeps the run
//     parked while the failed code-repo outcome remains `failed` —
//     `codeRepoOutcomesAllowResume` is the gate, removing it makes this
//     scenario fail (regression-bait check assertGatedTickIsNoOp);
//   - `Orchestrator.RetryRepositoryMerge` flips the failed outcome back
//     to `pending` and the recovery result reports `ReadyToResume=true`
//     with no `BlockingRepositories`;
//   - the next scheduler tick picks the run up via
//     `git.retry_partial_merge`, transitions partially-merged →
//     committing, re-attempts MergeRunBranch, the previously-failed
//     repo's merge succeeds, and the run advances to completed with
//     every outcome in `merged`.
//
// Coverage map:
//
//   - The unit layer
//     (`internal/scheduler/recovery_partial_test.go`,
//     `internal/engine/merge_recovery_test.go`,
//     `internal/engine/merge_resolution_test.go`)
//     proves each piece of the chain in isolation against fakes. They do
//     NOT prove that the orchestrator → scheduler → orchestrator
//     re-entry actually closes the loop end to end against real
//     Postgres + on-disk repos. This scenario consolidates them.
//   - The harness wires only one code repo (billing). The two-repo
//     surface is already pinned by `multi_repo_run_lifecycle_test.go`;
//     adding a second code repo here would only duplicate the fanout
//     assertion without making the retry loop more interesting. TASK-007
//     and TASK-008 (the resolve-externally and cancel-from-partial
//     exits) cover the multi-repo blocking-list permutations.
func TestPartialMergeRetry_HappyPath(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "partial-merge-retry-happy-path",
		Description: "Multi-repo run: code repo fails permanently → partially-merged → operator retry → scheduler tick → completed",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupPartialMergeOrchestrator(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				partialMergeRetryWorkflowYAML,
				"seed partial-merge-retry workflow",
			),
			seedPartialMergeTaskHierarchy(),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-903/epics/epic-903/tasks/task-001.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),

			queueBillingMergeFailure(),

			// SubmitStepResult("accepted") triggers commit semantics:
			// IngestResult drives the run to committing and immediately
			// invokes MergeRunBranch (run.go's "immediately attempt the
			// merge rather than waiting for the scheduler poll"). Billing's
			// queued failure fires once, primary still merges, and the run
			// lands in partially-merged.
			scenarioEngine.SubmitStepResult("accepted"),

			assertPartiallyMergedShape(),
			assertGatedTickIsNoOp(),

			requestRetryRepositoryMerge(),
			assertReadyToResume(),

			runSchedulerRetryCycle(),
			assertRunCompletedWithMergedOutcomes(),
		},
	})
}

// partialMergeRetryWorkflowYAML routes the entry step at billing so the
// per-step routing assertion stays plausible (a code-repo step is the
// realistic shape an operator hits a partial merge on), and gives the
// review step a `commit: status: Completed` outcome so SubmitStepResult
// drives MergeRunBranch synchronously. `next_step: end` matches the
// pattern other multi-repo scenarios already use; the workflow does not
// need a separate publish step because the orchestrator's commit-phase
// short-circuit (engine/run.go) calls MergeRunBranch directly.
const partialMergeRetryWorkflowYAML = `id: task-default
name: Partial Merge Retry Test Workflow
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

// partialMergeRetryTaskFrontmatter declares billing as the run's only
// non-primary affected repo. ADR-015 requires the workflow's
// `repository: billing` declaration to match an entry in the task's
// `repositories:` list, so a regression that loosens this validation
// would fail StartRun before the merge path is ever reached.
const partialMergeRetryTaskFrontmatter = `---
id: TASK-001
type: Task
title: "Partial Merge Retry Task"
status: Pending
epic: /initiatives/init-903/epics/epic-903/epic.md
initiative: /initiatives/init-903/initiative.md
repositories:
  - billing
links:
  - type: parent
    target: /initiatives/init-903/epics/epic-903/epic.md
---

# Partial Merge Retry Task
`

// setupPartialMergeOrchestrator wires the billing code repo onto the
// orchestrator and stashes its handle so later steps can queue failures
// against the same wrapped client the engine resolves through. ParentT
// anchoring is delegated to harness.WithCodeRepos; using sc.T here would
// orphan the working tree once this step's subtest ends.
func setupPartialMergeOrchestrator() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-partial-merge-orchestrator",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator, sc.Repo,
				harness.CodeRepoSpec{ID: "billing"},
			)
			sc.Set(statePartialBillingRepo, repos["billing"])
			return nil
		},
	}
}

// seedPartialMergeTaskHierarchy seeds initiative + epic + multi-repo task
// in their own (init-903 / epic-903) namespace so the scenario can run
// alongside other multi-repo scenarios in the same package without
// artifact-path collisions.
func seedPartialMergeTaskHierarchy() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "seed-partial-merge-task-hierarchy",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			scenarioEngine.FixtureInitiative(sc, "initiatives/init-903/initiative.md", scenarioEngine.ArtifactOpts{ID: "INIT-903"})
			scenarioEngine.FixtureEpic(sc, "initiatives/init-903/epics/epic-903/epic.md", scenarioEngine.ArtifactOpts{
				ID:   "EPIC-903",
				Init: "/initiatives/init-903/initiative.md",
			})
			taskPath := "initiatives/init-903/epics/epic-903/tasks/task-001.md"
			if _, err := sc.Runtime.Artifacts.Create(sc.Ctx, taskPath, partialMergeRetryTaskFrontmatter); err != nil {
				return fmt.Errorf("create partial-merge task: %w", err)
			}
			return nil
		},
	}
}

// queueBillingMergeFailure injects a single-shot permanent merge
// conflict on billing's next merge attempt. The error shape matters:
// classifyMergeFailure inspects the *git.GitError's Message string for
// "conflict" and returns MergeFailureConflict, which is permanent
// (IsTransient()=false), so the outcome's IsTerminal() returns true and
// the per-repo loop sees a terminal-failed entry that drives
// transitionToPartiallyMerged. A plain error or a transient class would
// instead loop the run through retryMerge in committing — a different
// recovery contract than the one this scenario exercises.
func queueBillingMergeFailure() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "queue-billing-merge-failure",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			billing := sc.MustGet(statePartialBillingRepo).(*harness.CodeRepo)
			billing.SetNextMergeFailure(&git.GitError{
				Kind:    git.ErrKindPermanent,
				Op:      "merge",
				Message: "merge conflict",
			})
			return nil
		},
	}
}

// assertPartiallyMergedShape pins the post-failure invariants documented
// in error-handling-and-recovery.md §5.4: the run is in
// partially-merged, the primary outcome has landed (status=merged with
// a non-empty MergeCommitSHA), and billing's outcome is failed with
// FailureClass=merge_conflict so the scheduler's gate stays closed.
//
// The primary outcome's Attempts counter is captured here so the
// regression-bait check (assertGatedTickIsNoOp) can verify it does NOT
// grow during a gated tick — the strongest signal that
// `codeRepoOutcomesAllowResume` actually short-circuits the retry path.
func assertPartiallyMergedShape() scenarioEngine.Step {
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
				return fmt.Errorf("primary outcome MergeCommitSHA empty on merged status")
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

			sc.Set(statePartialPrimaryAttempts, primary.Attempts)
			sc.Set(statePartialBillingAttempts, billing.Attempts)
			return nil
		},
	}
}

// assertGatedTickIsNoOp is the regression-bait check called out in
// TASK-006's acceptance criteria: removing the
// `codeRepoOutcomesAllowResume` gate from `scheduler.retryCommittingRuns`
// must make this scenario fail. While billing's outcome is still
// `failed`, the gate must keep the run parked: no committing transition,
// no MergeRunBranch invocation, no inflated attempts counter.
//
// Asserting only on `run.Status` (still partially-merged) is not
// sufficient — without the gate the scheduler would transition
// partially-merged → committing, MergeRunBranch would re-merge the
// primary (recordPrimaryMergeOutcome bumps Attempts to 2), then
// transitionToPartiallyMerged would put the run right back. End-state
// status would still be partially-merged. The Attempts delta is the
// observable that pins the gate's actual behavior.
func assertGatedTickIsNoOp() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-gated-tick-is-noop",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			sched := newScenarioScheduler(sc)
			sched.RunRetryCycle(sc.Ctx)

			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.Status != domain.RunStatusPartiallyMerged {
				return fmt.Errorf("run status after gated tick: got %s, want partially-merged (gate must hold while billing outcome is failed)", run.Status)
			}

			outcomes, err := sc.Runtime.Store.ListRepositoryMergeOutcomes(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list outcomes: %w", err)
			}
			byRepo := outcomesByRepo(outcomes)

			wantPrimaryAttempts := sc.MustGet(statePartialPrimaryAttempts).(int)
			if got := byRepo[repository.PrimaryRepositoryID].Attempts; got != wantPrimaryAttempts {
				return fmt.Errorf("primary attempts after gated tick: got %d, want %d (a removed gate would re-invoke MergeRunBranch and bump this)", got, wantPrimaryAttempts)
			}
			wantBillingAttempts := sc.MustGet(statePartialBillingAttempts).(int)
			if got := byRepo["billing"].Attempts; got != wantBillingAttempts {
				return fmt.Errorf("billing attempts after gated tick: got %d, want %d (a removed gate would re-attempt the merge before operator action)", got, wantBillingAttempts)
			}
			return nil
		},
	}
}

// requestRetryRepositoryMerge invokes the operator-facing retry surface
// the same way the gateway HTTP handler does: a *domain.Actor is bound
// to ctx via domain.WithActor before the orchestrator call, since the
// orchestrator rejects unauthenticated invocations
// (ErrUnauthorized). The scenario does not use the gateway path because
// that adds a JSON-encoding hop without exercising any merge-recovery
// behavior the orchestrator does not already enforce.
func requestRetryRepositoryMerge() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "request-retry-repository-merge",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			actor := &domain.Actor{
				ActorID: "actor-op-1",
				Type:    domain.ActorTypeHuman,
				Role:    domain.RoleOperator,
				Status:  domain.ActorStatusActive,
			}
			ctx := domain.WithActor(sc.Ctx, actor)
			res, err := sc.Runtime.Orchestrator.RetryRepositoryMerge(ctx, runID, "billing", "operator manually resolved conflict")
			if err != nil {
				return fmt.Errorf("RetryRepositoryMerge: %w", err)
			}
			sc.Set(statePartialRetryResult, res)
			return nil
		},
	}
}

// assertReadyToResume pins the operator-facing return shape: with only
// billing failed, clearing it must report ReadyToResume=true and an
// empty BlockingRepositories list. The store-side check confirms the
// outcome row is now `pending`, which is the per-repo state the next
// scheduler tick re-attempts a merge against.
func assertReadyToResume() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-ready-to-resume",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			res := sc.MustGet(statePartialRetryResult).(*spineEngine.MergeRecoveryResult)
			if !res.ReadyToResume {
				return fmt.Errorf("ReadyToResume: got false, want true (only billing was failed and we just cleared it)")
			}
			if len(res.BlockingRepositories) != 0 {
				return fmt.Errorf("BlockingRepositories: got %v, want empty after clearing the only failed outcome", res.BlockingRepositories)
			}

			runID := sc.MustGet("run_id").(string)
			outcome, err := sc.Runtime.Store.GetRepositoryMergeOutcome(sc.Ctx, runID, "billing")
			if err != nil {
				return fmt.Errorf("get billing outcome after retry: %w", err)
			}
			if outcome.Status != domain.RepositoryMergeStatusPending {
				return fmt.Errorf("billing outcome after retry: got %s, want pending", outcome.Status)
			}
			if outcome.FailureClass != "" {
				return fmt.Errorf("billing FailureClass after retry: got %q, want empty (RetryRepositoryMerge clears the failure metadata)", outcome.FailureClass)
			}
			return nil
		},
	}
}

// runSchedulerRetryCycle drives one tick of the periodic merge-retry
// loop — the same code path that fires from the orchestrator's
// timeoutInterval ticker in production. With billing's outcome now
// `pending`, the gate opens, the run transitions partially-merged →
// committing, and MergeRunBranch is re-invoked.
func runSchedulerRetryCycle() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "run-scheduler-retry-cycle",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			sched := newScenarioScheduler(sc)
			sched.RunRetryCycle(sc.Ctx)
			return nil
		},
	}
}

// assertRunCompletedWithMergedOutcomes is the terminal contract: the
// run must be in `completed` (so blocked downstream tasks can advance
// per CheckAndEmitBlockingTransition), and every outcome row must be
// `merged` with a non-empty MergeCommitSHA. A regression that flipped
// the outcome to merged without the SHA — or that completed the run
// while billing was still pending — would fail here loudly rather than
// silently produce a half-merged completion.
func assertRunCompletedWithMergedOutcomes() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-completed-with-merged-outcomes",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.Status != domain.RunStatusCompleted {
				return fmt.Errorf("run status after retry tick: got %s, want completed", run.Status)
			}

			outcomes, err := sc.Runtime.Store.ListRepositoryMergeOutcomes(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list outcomes: %w", err)
			}
			if len(outcomes) != 2 {
				return fmt.Errorf("outcomes count: got %d, want 2 (spine + billing)", len(outcomes))
			}
			for i := range outcomes {
				o := outcomes[i]
				if o.Status != domain.RepositoryMergeStatusMerged {
					return fmt.Errorf("repo %s outcome status: got %s, want merged", o.RepositoryID, o.Status)
				}
				if o.MergeCommitSHA == "" {
					return fmt.Errorf("repo %s: empty MergeCommitSHA on merged outcome", o.RepositoryID)
				}
			}
			return nil
		},
	}
}

// newScenarioScheduler constructs a scheduler wired with
// orch.MergeRunBranch as commitRetryFn. The threshold is 0 so a
// just-created run is eligible — production uses a small threshold to
// suppress thrash, but in a deterministic scenario we want the tick to
// act on the run we just created.
func newScenarioScheduler(sc *scenarioEngine.ScenarioContext) *scheduler.Scheduler {
	orch := sc.Runtime.Orchestrator
	retryFn := func(ctx context.Context, runID string) error {
		return orch.MergeRunBranch(ctx, runID)
	}
	return scheduler.New(sc.Runtime.Store, sc.Runtime.Events,
		scheduler.WithCommitRetry(retryFn, 0, 0))
}

// outcomesByRepo collapses the slice into a lookup keyed by repository
// ID. The orchestrator guarantees one row per (run, repo), so a
// duplicate would itself be a regression worth surfacing — but the
// caller checks for missing keys, not duplicates, so we keep this as a
// plain assignment.
func outcomesByRepo(outcomes []domain.RepositoryMergeOutcome) map[string]domain.RepositoryMergeOutcome {
	byRepo := make(map[string]domain.RepositoryMergeOutcome, len(outcomes))
	for i := range outcomes {
		byRepo[outcomes[i].RepositoryID] = outcomes[i]
	}
	return byRepo
}

// State keys — namespaced with `partial_` so they cannot collide with
// keys other multi-repo scenarios in this package set on the shared
// ScenarioContext.
const (
	statePartialBillingRepo     = "partial_billing_repo"
	statePartialPrimaryAttempts = "partial_primary_attempts"
	statePartialBillingAttempts = "partial_billing_attempts"
	statePartialRetryResult     = "partial_retry_result"
)
