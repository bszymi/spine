package engine

// INIT-023 wave 4 cross-task review, finding P2-2 and the state-write P3s:
// a failed workflow or step read made governanceHoldTarget answer "no
// publish step", so the hold, its clearing and the retry budget's held test
// picked different steps; the abandonment decided its route after its
// writes; an abandonment could interleave with an approval; a stale hold or
// blocked target stopped the retry budget. Notes: "Cross-task review (wave
// 4)" on TASK-006 and TASK-012.

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// flakyLoader fails LoadWorkflow transiently fails times, then serves wf.
type flakyLoader struct {
	wf    *domain.WorkflowDefinition
	fails int
	calls int
	// okFirst > 0: the first okFirst calls succeed, every later one fails.
	okFirst int
}

func (l *flakyLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	l.calls++
	if l.okFirst > 0 && l.calls > l.okFirst {
		return nil, &git.GitError{Kind: git.ErrKindTransient, Op: "show", Message: "repository locked"}
	}
	if l.fails > 0 {
		l.fails--
		return nil, &git.GitError{Kind: git.ErrKindTransient, Op: "show", Message: "repository locked"}
	}
	return l.wf, nil
}

// TestWave4Review_HoldReadFailureNeverAbandons (P2-2 a): a held run whose
// budget is spent, and whose workflow cannot be read on a tick, is not
// taken for "not held": the abandonment returns the error (the scheduler
// skips the tick) and changes nothing — once or twice in a row.
func TestWave4Review_HoldReadFailureNeverAbandons(t *testing.T) {
	ctx := context.Background()
	for _, fails := range []int{1, 2} {
		f := newW4Fake(t, defaultGuard(), false, w4Modified(".spine/branch-protection.yaml")...)
		requireHoldErr(t, f.merge())
		w4Exhausted(t, f)
		f.orch.wfLoader = &flakyLoader{wf: f.wf, fails: fails}
		abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
		if err == nil || abandoned {
			t.Fatalf("fails=%d: AbandonExhaustedMerge = %v, %v; want an error and nothing abandoned", fails, abandoned, err)
		}
		if f.run.Status != domain.RunStatusCommitting || f.step("publish").Status != domain.StepStatusInProgress ||
			!isGovernanceHold(f.step("publish").ErrorDetail) {
			t.Fatalf("fails=%d: run %s publish %s/%v; want committing and still held", fails, f.run.Status,
				f.step("publish").Status, f.step("publish").ErrorDetail)
		}
		if !strings.HasPrefix(f.primaryOutcome().FailureDetail, "connection reset") {
			t.Fatalf("fails=%d: the outcome was marked exhausted: %q", fails, f.primaryOutcome().FailureDetail)
		}
	}
}

// TestWave4Review_ApprovalClearsTheHoldWhateverTheWorkflowRead (P2-2 b):
// the approval clears the hold from the step that carries it without
// reading the workflow, so a failed read can no longer leave a stale hold
// that keeps the approved merge's transient failures out of the budget.
func TestWave4Review_ApprovalClearsTheHoldWhateverTheWorkflowRead(t *testing.T) {
	ctx := context.Background()
	f := newW4Fake(t, defaultGuard(), false, w4Modified(".spine/branch-protection.yaml")...)
	requireHoldErr(t, f.merge())
	// Every workflow read fails while the decision runs (before, the
	// clear's own read failing was enough to leave the hold behind).
	fl := &flakyLoader{wf: f.wf, fails: 1 << 20}
	f.orch.wfLoader = fl
	f.g.mergeErr = &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "connection reset"}
	if _, err := f.decide(GovernanceApprove, gBound, w4Operator, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	fl.fails = 0
	if isGovernanceHold(f.step("publish").ErrorDetail) {
		t.Fatal("the approval left the hold on the publish step")
	}
	for i := 0; i < 5; i++ {
		_ = f.merge()
	}
	abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
	if err != nil || !abandoned || f.run.Status != domain.RunStatusActive {
		t.Fatalf("AbandonExhaustedMerge = %v, %v, run %s; want the approved merge's failures bounded", abandoned, err, f.run.Status)
	}
}

// TestWave4Review_HoldNotRecordedOnAGuessedStep (P2-2): a hold whose
// target cannot be read is recorded nowhere this time — not on the review
// step — and the merge is still held; the next attempt records it on the
// publish step.
func TestWave4Review_HoldNotRecordedOnAGuessedStep(t *testing.T) {
	f := newW4Fake(t, defaultGuard(), false, w4Modified(".spine/branch-protection.yaml")...)
	f.orch.wfLoader = &flakyLoader{wf: f.wf, fails: 1}
	requireHoldErr(t, f.merge())
	if f.step("review").ErrorDetail != nil || f.step("publish").ErrorDetail != nil || len(f.eventsOf(domain.EventGovernanceMergeHeld)) != 0 {
		t.Fatalf("review %v publish %v events %d; want nothing recorded on a failed read",
			f.step("review").ErrorDetail, f.step("publish").ErrorDetail, len(f.eventsOf(domain.EventGovernanceMergeHeld)))
	}
	requireHoldErr(t, f.merge())
	if !isGovernanceHold(f.step("publish").ErrorDetail) || f.step("review").ErrorDetail != nil {
		t.Fatalf("publish %v review %v; want the hold on the publish step", f.step("publish").ErrorDetail, f.step("review").ErrorDetail)
	}
}

// TestWave4Review_AbandonmentDecidesItsRouteFirst (P3): with the guard
// off, a workflow read that fails during an abandonment no longer fails a
// publish-workflow run outright: nothing is written, the error is
// returned, and the next tick routes it back (merge_failed).
func TestWave4Review_AbandonmentDecidesItsRouteFirst(t *testing.T) {
	ctx := context.Background()
	f := newW4Fake(t, nil, false, w4Modified("src/a.go")...)
	w4Exhausted(t, f)
	f.orch.wfLoader = &flakyLoader{wf: f.wf, fails: 1}
	abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
	if err == nil || abandoned || f.run.Status != domain.RunStatusCommitting {
		t.Fatalf("AbandonExhaustedMerge = %v, %v, run %s; want an error, nothing changed", abandoned, err, f.run.Status)
	}
	if strings.HasPrefix(f.primaryOutcome().FailureDetail, retriesExhaustedMarker) {
		t.Fatal("the outcome was marked before the route was decided")
	}
	abandoned, err = f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
	if err != nil || !abandoned || f.run.Status != domain.RunStatusActive || f.step("publish").OutcomeID != "merge_failed" {
		t.Fatalf("next tick: %v, %v, run %s, publish %q; want the merge_failed route", abandoned, err, f.run.Status, f.step("publish").OutcomeID)
	}

	// A read that fails after the decision (the route's own workflow load)
	// leaves the run committing for the next tick; it never fails it.
	g := newW4Fake(t, nil, false, w4Modified("src/a.go")...)
	w4Exhausted(t, g)
	g.orch.wfLoader = &flakyLoader{wf: g.wf, okFirst: 1}
	if _, err := g.orch.AbandonExhaustedMerge(ctx, g.run.RunID, 3); err == nil || g.run.Status != domain.RunStatusCommitting {
		t.Fatalf("route read failed: err %v, run %s; want an error and the run still committing", err, g.run.Status)
	}
	g.orch.wfLoader = &flakyLoader{wf: g.wf}
	abandoned, err = g.orch.AbandonExhaustedMerge(ctx, g.run.RunID, 3)
	if err != nil || !abandoned || g.run.Status != domain.RunStatusActive || g.step("publish").OutcomeID != "merge_failed" {
		t.Fatalf("after the read recovered: %v, %v, run %s, publish %q; want the merge_failed route", abandoned, err, g.run.Status, g.step("publish").OutcomeID)
	}
}

// TestWave4Review_DecisionIsAnAttemptInFlight (P3): a decision registers
// with mergeAttemptGate before it records anything. During an abandonment
// it is refused with nothing recorded; while it runs, no abandonment
// starts.
func TestWave4Review_DecisionIsAnAttemptInFlight(t *testing.T) {
	f := newW4Fake(t, defaultGuard(), false, w4Modified(".spine/branch-protection.yaml")...)
	requireHoldErr(t, f.merge())
	w4Exhausted(t, f)
	end, ok := mergeAttemptGate.beginAbandon(f.run.RunID)
	if !ok {
		t.Fatal("setup: beginAbandon")
	}
	_, err := f.decide(GovernanceApprove, gBound, w4Operator, "")
	end()
	requireSpineCode(t, err, domain.ErrUnavailable)
	if k := governanceKeys(f.run.CommitMeta); k[domain.CommitMetaGovernanceApprovalSHA] != "" || !isGovernanceHold(f.step("publish").ErrorDetail) {
		t.Fatalf("approval %v hold %v; want nothing recorded or cleared", k, f.step("publish").ErrorDetail)
	}
	var during bool
	f.store.beforeWrite = func(kind string) {
		if kind == "merge-if" {
			end, ok := mergeAttemptGate.beginAbandon(f.run.RunID)
			end()
			during = during || ok
		}
	}
	f.g.mergeErr = &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "connection reset"}
	if _, err := f.decide(GovernanceApprove, gBound, w4Operator, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if during {
		t.Fatal("an abandonment started while the decision was recording the approval")
	}
	if len(f.g.merges) != 1 {
		t.Fatalf("merges %d, want the approved attempt", len(f.g.merges))
	}
}

// TestWave4Review_StaleBlockedTargetDoesNotStopTheBudget (P3): a run's
// recorded blocked target whose merge is no longer in the local main's
// history (another run's gate took it back) is stale: the key is cleared
// and the budget applies. One still on main keeps the run committing.
func TestWave4Review_StaleBlockedTargetDoesNotStopTheBudget(t *testing.T) {
	ctx := context.Background()
	for _, onMain := range []bool{true, false} {
		f := newW4Fake(t, defaultGuard(), false, w4Modified("src/a.go")...)
		f.run.CommitMeta[CommitMetaGovernanceBlockedTarget] = gMerge + " " + gBase
		f.g.setHead("main", gBase)
		f.g.notAncestor[gMerge+">"+gBase] = !onMain
		w4Exhausted(t, f)
		abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
		if err != nil || abandoned == onMain {
			t.Fatalf("on main %v: AbandonExhaustedMerge = %v, %v", onMain, abandoned, err)
		}
		if got := f.run.CommitMeta[CommitMetaGovernanceBlockedTarget]; (got == "") == onMain {
			t.Fatalf("on main %v: blocked target %q", onMain, got)
		}
	}
}

// TestWave4Review_StaleHoldIsClearedWhenTheGateAllows (P3): a held run
// whose commits reach main another way (an operator lands the run branch)
// is no longer held; the next attempt the gate allows clears the hold, so
// its transient failures are bounded like any other run's.
func TestWave4Review_StaleHoldIsClearedWhenTheGateAllows(t *testing.T) {
	var w *lateErrCLI
	g := newGovRepo(t, govRepoOpts{
		runFiles: map[string]string{"workflows/task-default.yaml": govReviewLess},
		guard:    defaultGuard(),
		wrap:     func(c *git.CLIClient) GitOperator { w = &lateErrCLI{CLIClient: c}; return w },
	})
	g.toMerge()
	g.requireHeld("initial")
	g.git("merge", "-q", "--no-ff", "-m", "operator lands the run branch directly", g.branch)
	w.locked = true
	for i := 0; i < 5; i++ {
		_ = g.orch.MergeRunBranch(context.Background(), govRunID)
	}
	if isGovernanceHold(g.latest("publish").ErrorDetail) {
		t.Fatal("the hold is still recorded after allowed attempts")
	}
	abandoned, err := g.orch.AbandonExhaustedMerge(context.Background(), govRunID, 3)
	if err != nil || !abandoned {
		t.Fatalf("AbandonExhaustedMerge = %v, %v; want the run abandoned after its transient failures", abandoned, err)
	}
}
