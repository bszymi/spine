//go:build scenario

package scenarios_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	spineEngine "github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// TestPartialMergeExternalResolution_HappyPath is the scenario-level AC anchor
// for INIT-022 EPIC-001 TASK-007:
//
//   - committing → partially-merged via `git.code_repo_partial_failure`
//     when the primary merge has landed but a code repo's merge ended in
//     a permanent-failed class (same setup TASK-006 exercises);
//   - while the run is parked, the failed code-repo branch and every
//     other affected branch stay on disk per
//     `architecture/multi-repository-integration.md §4.5`
//     ("While a run is in the partially-merged state, no cleanup has
//     run yet"). The scenario asserts both the failed code-repo branch
//     and the primary's run branch are present on disk at this point —
//     the operator-inspection invariant the AC item 4 calls out.
//   - `Orchestrator.ResolveRepositoryMergeExternally(runID, repoID,
//     reason, targetSHA)` flips the failed outcome to
//     `resolved-externally` with `ResolvedBy` / `ResolutionReason`
//     populated, clears failure metadata, and writes a per-run ledger
//     commit on the primary repo's run branch. The recovery result
//     surfaces `ReadyToResume=true` with no `BlockingRepositories`.
//   - the next scheduler tick opens the resume gate
//     (`codeRepoOutcomesAllowResume` ignores `resolved-externally`),
//     transitions partially-merged → committing, and re-invokes
//     `MergeRunBranch`. The terminal-skip guard in
//     `internal/engine/multi_repo_merge.go` does NOT re-merge billing,
//     and the run lands in `completed` with billing's outcome still
//     `resolved-externally`.
//   - the ledger commit is reachable from `main` after the merge — the
//     audit invariant TASK-006's deliverable named ("audit entries are
//     queryable from primary-repo history"). We grep `git log main` for
//     the trailers documented in `architecture/git-integration.md §5`
//     plus the resolve-specific extras (`Operation:
//     merge_recovery_resolve`, `Resolved-By`, `Target-Commit-SHA`) the
//     orchestrator stamps onto the commit.
//
// Coverage map:
//
//   - The unit layer
//     (`internal/engine/merge_resolution_test.go`,
//     `internal/engine/merge_recovery_test.go::TestMergeRecovery_PartialMergeRetriedAfterResolveExternally`)
//     proves the orchestrator-side state transitions and ledger
//     commit shape against fakes. They do NOT prove that the operator
//     action → scheduler → orchestrator re-entry actually closes the
//     loop end to end against real Postgres + on-disk repos. This
//     scenario consolidates them.
//   - One code repo (billing) is sufficient: the multi-failed-repo
//     blocking-list permutations belong to TASK-008
//     (cancel-from-partially-merged) and the unit-level
//     `TestMergeRecovery_BlockingRepositoriesSurfacedWhenMultipleFailed`
//     already pins the multi-failure surface.
func TestPartialMergeExternalResolution_HappyPath(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "partial-merge-external-resolution-happy-path",
		Description: "Multi-repo run: code repo fails permanently → partially-merged → operator marks resolved-externally → scheduler tick → completed",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			setupExternalResolutionOrchestrator(),
			scenarioEngine.WriteAndCommit(
				"workflows/task-default.yaml",
				partialMergeExternalResolutionWorkflowYAML,
				"seed partial-merge-external-resolution workflow",
			),
			seedExternalResolutionTaskHierarchy(),
			scenarioEngine.SyncProjections(),

			scenarioEngine.StartRun("initiatives/init-904/epics/epic-904/tasks/task-001.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertCurrentStep("review"),

			queueExternalResolutionMergeFailure(),

			// SubmitStepResult("accepted") triggers commit semantics:
			// IngestResult drives the run to committing and immediately
			// invokes MergeRunBranch (run.go's "immediately attempt the
			// merge rather than waiting for the scheduler poll"). Billing's
			// queued failure fires once, primary still merges, and the run
			// lands in partially-merged.
			scenarioEngine.SubmitStepResult("accepted"),

			assertExternalResolutionPartiallyMergedShape(),
			assertFailedBranchPreservedWhileParked(),

			resolveBillingMergeExternally(),
			assertExternalResolutionRecoveryResult(),
			assertBillingOutcomeResolvedExternally(),

			runExternalResolutionSchedulerTick(),
			assertExternalResolutionRunCompleted(),
			assertLedgerCommitOnMain(),
		},
	})
}

// partialMergeExternalResolutionWorkflowYAML mirrors TASK-006's workflow
// shape: the entry step is in the code repo, the review step's
// `accepted` outcome carries `commit: status: Completed` so
// SubmitStepResult drives MergeRunBranch synchronously through the
// orchestrator's "immediate merge attempt" short-circuit.
const partialMergeExternalResolutionWorkflowYAML = `id: task-default
name: Partial Merge External Resolution Test Workflow
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

// partialMergeExternalResolutionTaskFrontmatter declares billing as the
// run's only non-primary affected repo. ADR-015 requires the workflow's
// `repository: billing` declaration to match an entry in the task's
// `repositories:` list. init-904/epic-904 keeps the artifact paths
// distinct from the partial-merge-retry scenario's init-903/epic-903 so
// the two scenarios stay independent in the same package set.
const partialMergeExternalResolutionTaskFrontmatter = `---
id: TASK-001
type: Task
title: "Partial Merge External Resolution Task"
status: Pending
epic: /initiatives/init-904/epics/epic-904/epic.md
initiative: /initiatives/init-904/initiative.md
repositories:
  - billing
links:
  - type: parent
    target: /initiatives/init-904/epics/epic-904/epic.md
---

# Partial Merge External Resolution Task
`

// setupExternalResolutionOrchestrator wires the billing code repo onto
// the orchestrator and stashes its handle so the failure-injection step
// can queue against the same wrapped client the engine resolves
// through. ParentT anchoring is delegated to harness.WithCodeRepos;
// using sc.T here would orphan the working tree once this step's
// subtest ends.
func setupExternalResolutionOrchestrator() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-external-resolution-orchestrator",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			repos := harness.WithCodeRepos(sc.ParentT, sc.Runtime.Orchestrator, sc.Repo,
				harness.CodeRepoSpec{ID: "billing"},
			)
			sc.Set(stateExternalBillingRepo, repos["billing"])
			return nil
		},
	}
}

// seedExternalResolutionTaskHierarchy seeds initiative + epic + multi-
// repo task in the init-904/epic-904 namespace.
func seedExternalResolutionTaskHierarchy() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "seed-external-resolution-task-hierarchy",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			scenarioEngine.FixtureInitiative(sc, "initiatives/init-904/initiative.md", scenarioEngine.ArtifactOpts{ID: "INIT-904"})
			scenarioEngine.FixtureEpic(sc, "initiatives/init-904/epics/epic-904/epic.md", scenarioEngine.ArtifactOpts{
				ID:   "EPIC-904",
				Init: "/initiatives/init-904/initiative.md",
			})
			taskPath := "initiatives/init-904/epics/epic-904/tasks/task-001.md"
			if _, err := sc.Runtime.Artifacts.Create(sc.Ctx, taskPath, partialMergeExternalResolutionTaskFrontmatter); err != nil {
				return fmt.Errorf("create external-resolution task: %w", err)
			}
			return nil
		},
	}
}

// queueExternalResolutionMergeFailure injects a single-shot permanent
// merge conflict on billing's next merge attempt. Same shape TASK-006
// uses — `Kind: ErrKindPermanent`, `Message: "merge conflict"` — so
// classifyMergeFailure returns MergeFailureConflict (permanent,
// IsTransient()=false) and the per-repo loop sees a terminal-failed
// entry that drives transitionToPartiallyMerged.
func queueExternalResolutionMergeFailure() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "queue-billing-merge-failure",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			billing := sc.MustGet(stateExternalBillingRepo).(*harness.CodeRepo)
			billing.SetNextMergeFailure(&git.GitError{
				Kind:    git.ErrKindPermanent,
				Op:      "merge",
				Message: "merge conflict",
			})
			return nil
		},
	}
}

// assertExternalResolutionPartiallyMergedShape pins the post-failure
// invariants documented in error-handling-and-recovery.md §5.4: the
// run is in partially-merged, the primary outcome has landed
// (status=merged with a non-empty MergeCommitSHA), and billing's
// outcome is failed with FailureClass=merge_conflict. This is the
// pre-resolve state the rest of the scenario operates against.
func assertExternalResolutionPartiallyMergedShape() scenarioEngine.Step {
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
			sc.Set(stateExternalRunBranch, run.BranchName)

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
			return nil
		},
	}
}

// assertFailedBranchPreservedWhileParked pins the
// `multi-repository-integration.md §4.5` invariant: while the run is in
// `partially-merged`, every affected repo's run branch stays on disk so
// the operator can inspect the failed branch alongside the merged work.
// Asserts presence on both billing (the failed code repo) and the
// primary (which has already merged but whose branch carries the
// ledger-commit history that resolve-externally is about to extend).
//
// A regression that fired CleanupRunBranch on the partial transition
// would delete one or both branches and fail this step before the
// resolve call ever happens.
func assertFailedBranchPreservedWhileParked() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-failed-branch-preserved-while-parked",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runBranch := sc.MustGet(stateExternalRunBranch).(string)
			if runBranch == "" {
				return fmt.Errorf("run branch name is empty; partially-merged run must have a branch")
			}
			billing := sc.MustGet(stateExternalBillingRepo).(*harness.CodeRepo)
			if err := assertLocalBranchExists(billing.Dir, runBranch); err != nil {
				return fmt.Errorf("billing branch preservation: %w", err)
			}
			if err := assertLocalBranchExists(sc.Repo.Dir, runBranch); err != nil {
				return fmt.Errorf("primary branch preservation: %w", err)
			}
			return nil
		},
	}
}

// resolveBillingMergeExternally invokes the operator-facing
// resolve-externally surface the same way the gateway HTTP handler
// does: a *domain.Actor is bound to ctx via domain.WithActor before the
// orchestrator call, since the orchestrator rejects unauthenticated
// invocations (ErrUnauthorized). The reason and target SHA are
// captured into state so subsequent assertions can compare them
// against the persisted row and ledger commit.
func resolveBillingMergeExternally() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "resolve-billing-merge-externally",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			actor := &domain.Actor{
				ActorID: externalResolutionActorID,
				Type:    domain.ActorTypeHuman,
				Role:    domain.RoleOperator,
				Status:  domain.ActorStatusActive,
			}
			ctx := domain.WithActor(sc.Ctx, actor)
			res, err := sc.Runtime.Orchestrator.ResolveRepositoryMergeExternally(
				ctx, runID, "billing",
				externalResolutionReason, externalResolutionTargetSHA,
			)
			if err != nil {
				return fmt.Errorf("ResolveRepositoryMergeExternally: %w", err)
			}
			sc.Set(stateExternalRecoveryResult, res)
			return nil
		},
	}
}

// assertExternalResolutionRecoveryResult pins the operator-facing
// MergeRecoveryResult: with only billing failed, clearing it must
// report ReadyToResume=true and an empty BlockingRepositories list,
// and the LedgerCommitSHA must be non-empty (the orchestrator just
// wrote the audit commit and surfaces the SHA so callers can avoid an
// extra git query).
func assertExternalResolutionRecoveryResult() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-recovery-result",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			res := sc.MustGet(stateExternalRecoveryResult).(*spineEngine.MergeRecoveryResult)
			if !res.ReadyToResume {
				return fmt.Errorf("ReadyToResume: got false, want true (only billing was failed and we just resolved it)")
			}
			if len(res.BlockingRepositories) != 0 {
				return fmt.Errorf("BlockingRepositories: got %v, want empty after clearing the only failed outcome", res.BlockingRepositories)
			}
			if res.LedgerCommitSHA == "" {
				return fmt.Errorf("LedgerCommitSHA empty; orchestrator must surface the ledger commit SHA so callers can dereference the audit entry without a separate git query")
			}
			sc.Set(stateExternalLedgerSHA, res.LedgerCommitSHA)
			return nil
		},
	}
}

// assertBillingOutcomeResolvedExternally pins the persisted row shape
// after the resolve. Validate() rejects FailureClass / FailureDetail /
// MergeCommitSHA / MergedAt on resolved-externally rows, so the
// resolve path clears them defensively. ResolvedBy / ResolutionReason
// are set to the operator inputs.
func assertBillingOutcomeResolvedExternally() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-billing-outcome-resolved-externally",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			outcome, err := sc.Runtime.Store.GetRepositoryMergeOutcome(sc.Ctx, runID, "billing")
			if err != nil {
				return fmt.Errorf("get billing outcome after resolve: %w", err)
			}
			if outcome.Status != domain.RepositoryMergeStatusResolvedExternally {
				return fmt.Errorf("billing outcome status: got %s, want resolved-externally", outcome.Status)
			}
			if outcome.ResolvedBy != externalResolutionActorID {
				return fmt.Errorf("billing ResolvedBy: got %q, want %q", outcome.ResolvedBy, externalResolutionActorID)
			}
			if outcome.ResolutionReason != externalResolutionReason {
				return fmt.Errorf("billing ResolutionReason: got %q, want %q", outcome.ResolutionReason, externalResolutionReason)
			}
			if outcome.FailureClass != "" {
				return fmt.Errorf("billing FailureClass after resolve: got %q, want empty (resolve clears failure metadata)", outcome.FailureClass)
			}
			if outcome.FailureDetail != "" {
				return fmt.Errorf("billing FailureDetail after resolve: got %q, want empty", outcome.FailureDetail)
			}
			if outcome.MergeCommitSHA != "" {
				return fmt.Errorf("billing MergeCommitSHA: got %q, want empty (resolved-externally is not a Spine-driven merge)", outcome.MergeCommitSHA)
			}
			return nil
		},
	}
}

// runExternalResolutionSchedulerTick drives one tick of the periodic
// merge-retry loop using the same scheduler.WithCommitRetry wiring
// production uses. With billing's outcome now `resolved-externally`
// (a non-failed terminal status), `codeRepoOutcomesAllowResume`
// returns true, the scheduler transitions partially-merged →
// committing, and `MergeRunBranch` re-walks. The terminal-skip guard
// in `internal/engine/multi_repo_merge.go` skips billing (already
// terminal), the primary re-merges (already-merged → "Already up to
// date"), and the run completes.
func runExternalResolutionSchedulerTick() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "run-scheduler-retry-cycle",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			sched := newScenarioScheduler(sc)
			sched.RunRetryCycle(sc.Ctx)
			return nil
		},
	}
}

// assertExternalResolutionRunCompleted is the terminal contract: the
// run must be in `completed`, billing's outcome must remain
// `resolved-externally` (the scheduler resume MUST NOT overwrite the
// audit pair), and the primary's outcome must remain `merged`. A
// regression that re-merged billing on the resume tick — or that
// flipped billing to `merged` after a no-op call — would fail here
// rather than silently lose the operator's audit pair.
func assertExternalResolutionRunCompleted() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-run-completed",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			run, err := sc.Runtime.Store.GetRun(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("get run: %w", err)
			}
			if run.Status != domain.RunStatusCompleted {
				return fmt.Errorf("run status after resume: got %s, want completed", run.Status)
			}

			outcomes, err := sc.Runtime.Store.ListRepositoryMergeOutcomes(sc.Ctx, runID)
			if err != nil {
				return fmt.Errorf("list outcomes: %w", err)
			}
			if len(outcomes) != 2 {
				return fmt.Errorf("outcomes count: got %d, want 2 (spine + billing)", len(outcomes))
			}
			byRepo := outcomesByRepo(outcomes)

			if got := byRepo[repository.PrimaryRepositoryID].Status; got != domain.RepositoryMergeStatusMerged {
				return fmt.Errorf("primary outcome after resume: got %s, want merged", got)
			}
			if got := byRepo[repository.PrimaryRepositoryID].MergeCommitSHA; got == "" {
				return fmt.Errorf("primary MergeCommitSHA empty after resume")
			}

			billing := byRepo["billing"]
			if billing.Status != domain.RepositoryMergeStatusResolvedExternally {
				return fmt.Errorf("billing outcome after resume: got %s, want resolved-externally (terminal-skip must preserve the audit pair)", billing.Status)
			}
			if billing.ResolvedBy != externalResolutionActorID {
				return fmt.Errorf("billing ResolvedBy after resume: got %q, want %q (resume must not overwrite audit pair)", billing.ResolvedBy, externalResolutionActorID)
			}
			if billing.ResolutionReason != externalResolutionReason {
				return fmt.Errorf("billing ResolutionReason after resume: got %q, want %q (resume must not overwrite audit pair)", billing.ResolutionReason, externalResolutionReason)
			}
			return nil
		},
	}
}

// assertLedgerCommitOnMain verifies the ledger commit is reachable
// from the authoritative branch after the run completes — the audit
// invariant TASK-006's deliverable named ("audit entries are queryable
// from primary-repo history"). The ledger commit landed on the run
// branch BEFORE the second MergeRunBranch ran, so the merge into main
// carries it across.
//
// Pinned trailers come from `git-integration.md §5` (Run-ID, Trace-ID,
// Operation) plus the resolve-specific extras the orchestrator stamps
// (Repository-ID, Resolved-By, Target-Commit-SHA). The reason is
// expected in the body, not the trailers — the unit tests
// (`TestResolveRepositoryMergeExternally_NewlineReasonStaysInBody`)
// already pin the trailer-injection guard; we re-verify here to catch
// a regression that relocated the reason into a trailer.
//
// We grep against `git log main` so this assertion remains stable even
// if cleanup deletes the run branch (which §4.5 says it does on the
// resolved-externally exit).
func assertLedgerCommitOnMain() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-ledger-commit-on-main",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			ledgerSHA := sc.MustGet(stateExternalLedgerSHA).(string)

			// Find the ledger commit by SHA (cheaper and more precise than
			// grepping). %B prints the full message including trailers.
			out, err := gitOutput(sc.Repo.Dir, "log", "main", "--format=%H%n%B%n--END--", "-n", "200")
			if err != nil {
				return fmt.Errorf("git log main: %w", err)
			}
			rec, ok := findCommitRecord(out, ledgerSHA)
			if !ok {
				return fmt.Errorf("ledger commit %s not reachable from main; resolve-externally audit invariant violated", ledgerSHA)
			}

			expectSubject := fmt.Sprintf("Resolve external merge for run %s (billing)", runID)
			if firstLine(rec) != expectSubject {
				return fmt.Errorf("ledger commit subject: got %q, want %q", firstLine(rec), expectSubject)
			}

			wantTrailers := map[string]string{
				"Operation":         "merge_recovery_resolve",
				"Run-ID":            runID,
				"Repository-ID":     "billing",
				"Resolved-By":       externalResolutionActorID,
				"Target-Commit-SHA": externalResolutionTargetSHA,
			}
			for key, want := range wantTrailers {
				got := extractTrailer(rec, key)
				if got != want {
					return fmt.Errorf("ledger trailer %s: got %q, want %q", key, got, want)
				}
			}
			// Trace-ID is non-empty but value is unknown to the test —
			// pin presence, not value.
			if extractTrailer(rec, "Trace-ID") == "" {
				return fmt.Errorf("ledger trailer Trace-ID missing; required by git-integration.md §5")
			}
			// Reason lives in the body, not the trailers — protects against
			// trailer-injection via newline-bearing operator input. A
			// regression that moved the reason into a trailer (e.g.
			// `Resolution-Reason:`) would break the injection guard the
			// unit tests pin and is bait-checked here.
			if extractTrailer(rec, "Resolution-Reason") != "" {
				return fmt.Errorf("Resolution-Reason must NOT be a trailer (injection risk)")
			}
			if !strings.Contains(rec, externalResolutionReason) {
				return fmt.Errorf("ledger body missing reason %q", externalResolutionReason)
			}
			return nil
		},
	}
}

// findCommitRecord returns the message body for the requested SHA in
// the `--format=%H%n%B%n--END--` git-log output. Returns ok=false when
// the SHA does not appear, so the caller can surface "commit not
// reachable" with a clear error rather than silently asserting on an
// empty string.
func findCommitRecord(logOut, sha string) (string, bool) {
	for _, rec := range strings.Split(logOut, "\n--END--\n") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		nl := strings.IndexByte(rec, '\n')
		if nl < 0 {
			continue
		}
		if strings.TrimSpace(rec[:nl]) == sha {
			return strings.TrimRight(rec[nl+1:], "\n"), true
		}
	}
	return "", false
}

// firstLine returns the commit subject (the first line of the message
// body git printed via %B).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// extractTrailer pulls the value of `Key: value` from the commit
// message body. We do not shell out to `git interpret-trailers` here
// because the test only needs exact-match assertions and an extra
// process invocation per assertion would slow the scenario. A trailer
// is identified by key-prefix at the start of a line — sufficient for
// the orchestrator's well-formed output.
func extractTrailer(body, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// gitOutput runs `git -C dir <args...>` and returns the stdout. Failures
// surface the stderr in the error so the scenario log is debuggable
// without re-running git manually.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w (output: %s)", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

// assertLocalBranchExists fails when the named branch is missing from
// the local refs of the given working tree. Used to pin §4.5's
// "branches preserved while in partially-merged" invariant. We use
// `rev-parse --verify --quiet refs/heads/<branch>` rather than `branch
// --list` so the exit code drives the assertion (avoiding a stdout
// parse on test infra with localized git output).
func assertLocalBranchExists(dir, branch string) error {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("branch %q missing in %s: %w", branch, dir, err)
	}
	return nil
}

// State keys — namespaced with `external_` so they cannot collide with
// keys other multi-repo scenarios in this package set on the shared
// ScenarioContext.
const (
	stateExternalBillingRepo    = "external_billing_repo"
	stateExternalRecoveryResult = "external_recovery_result"
	stateExternalLedgerSHA      = "external_ledger_sha"
	stateExternalRunBranch      = "external_run_branch"
)

// Operator-action constants. The actor ID is reused across assertions,
// so pinning it once keeps the comparisons honest. The target SHA is
// a hex string the unit tests' validateSingleLineAuditValue accepts;
// the reason is multi-line-free so trailer-injection is not relevant
// to this scenario (the dedicated unit test
// `TestResolveRepositoryMergeExternally_NewlineReasonStaysInBody`
// already covers the multi-line case).
const (
	externalResolutionActorID   = "actor-op-external-1"
	externalResolutionReason    = "operator merged the conflict in upstream PR #4242"
	externalResolutionTargetSHA = "deadbeefcafebabe1234567890abcdef12345678"
)
