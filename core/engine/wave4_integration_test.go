package engine

// INIT-023 wave 4 integration: how TASK-005's merge binding, TASK-006's
// governance-path guard, TASK-008's compare-and-swap writes, TASK-012's
// shared-tree safety and retry budget and TASK-017's status lifecycle
// compose inside one governed merge. Over the fake binder (govFake,
// lcFix); the shared-tree half runs on real git in
// wave4_integration_git_test.go.
//
// The order inside MergeRunBranch is binding → lifecycle → governance gate
// (hold) → merge → verifyBoundMerge → V1 → governance post-merge check and
// marker → push. A permanent refusal is never retried, a hold records no
// attempt, and a failed take-back blocks governed merges whichever check
// refused the merge.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

const w4Task = "tasks/t.md"

var w4Operator = domain.Actor{ActorID: "op-1", Role: domain.RoleOperator}

// newW4Fake is newGovFake (guard on unless guard is nil; push on; the
// merger store) with the status lifecycle on when lifecycle is set: the
// run's TaskPath is a Pending Task at the merge base, the bound commit
// and main before the merge, and the bound diff lists diffs.
func newW4Fake(t *testing.T, guard *GovernanceGuard, lifecycle bool, diffs ...git.FileDiff) *govFake {
	t.Helper()
	f := newGovFake(t, publishWorkflow(), guard, true)
	f.g.diffs[sha40('0')+" "+gBound] = diffs
	f.g.diffs[gBase+" "+gMerge] = diffs
	for _, ref := range []string{sha40('0'), gBound, gBase, gMerge} {
		f.g.files[ref+":"+w4Task] = ulDoc("Task", "Pending")
	}
	if lifecycle {
		f.orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	}
	// Order of the store's commit_meta writes relative to the git calls.
	f.store.beforeWrite = func(kind string) { f.g.record("store:" + kind) }
	return f
}

func w4Modified(paths ...string) []git.FileDiff {
	var out []git.FileDiff
	for _, p := range paths {
		out = append(out, git.FileDiff{Path: p, Status: "modified"})
	}
	return out
}

// callIndex is the position of the first call with prefix at or after
// from, or -1.
func (f *govFake) callIndex(prefix string, from int) int {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	for i := from; i < len(f.g.calls); i++ {
		if strings.HasPrefix(f.g.calls[i], prefix) {
			return i
		}
	}
	return -1
}

func (f *govFake) primaryOutcome() *domain.RepositoryMergeOutcome {
	for i := range f.store.mergeOutcomes {
		if f.store.mergeOutcomes[i].IsPrimaryRepository() {
			return &f.store.mergeOutcomes[i]
		}
	}
	return nil
}

func (f *govFake) heldEvents(postMerge bool) int {
	n := 0
	for _, e := range f.eventsOf(domain.EventGovernanceMergeHeld) {
		if strings.Contains(string(e.Payload), `"post_merge":true`) == postMerge {
			n++
		}
	}
	return n
}

// TestWave4_LifecycleRefusalPrecedesTheGovernanceHold: a bound commit that
// both changes a governance path and changes a status the run does not own
// is refused by the status lifecycle (resolveMergeSource) before the
// governance gate runs — routed back, never held, MergeBases never asked.
func TestWave4_LifecycleRefusalPrecedesTheGovernanceHold(t *testing.T) {
	f := newW4Fake(t, defaultGuard(), true, w4Modified(".spine/branch-protection.yaml", "tasks/u.md")...)
	f.g.files[sha40('0')+":tasks/u.md"] = ulDoc("Task", "Pending")
	f.g.files[gBound+":tasks/u.md"] = ulDoc("Task", "Completed")
	if err := f.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	po := f.primaryOutcome()
	if po == nil || po.FailureClass != domain.MergeFailurePrecondition || !strings.Contains(po.FailureDetail, LifecycleNonOwnedStatusChange) {
		t.Fatalf("outcome = %+v, want the lifecycle refusal", po)
	}
	if f.run.Status != domain.RunStatusActive || f.step("publish").OutcomeID != "merge_failed" {
		t.Fatalf("run %s, publish %q; want the merge_failed route", f.run.Status, f.step("publish").OutcomeID)
	}
	if n := len(f.eventsOf(domain.EventGovernanceMergeHeld)); n != 0 {
		t.Fatalf("%d governance.merge_held events; the lifecycle refuses before the gate", n)
	}
	if n := f.g.callCount("MergeBases "); n != 0 {
		t.Fatalf("MergeBases called %d times; the gate must not run", n)
	}
	if len(f.g.merges) != 0 {
		t.Fatalf("MergeBound called %d times", len(f.g.merges))
	}
	if d := f.step("review").ErrorDetail; d == nil || d.Classification != domain.FailureStatusLifecycle {
		t.Fatalf("last completed step's error detail = %+v, want status_lifecycle_violation", d)
	}
}

// TestWave4_HoldThenApprovedMergeRunsEveryCheckBeforeThePush: the
// lifecycle passes, the governance gate holds (no attempt recorded), and
// the approved merge runs MergeBound, V1 (its merge-base check), the
// governance post-merge check (its Diff of the merge) and the consumption
// marker — all before the push.
func TestWave4_HoldThenApprovedMergeRunsEveryCheckBeforeThePush(t *testing.T) {
	f := newW4Fake(t, defaultGuard(), true, w4Modified(".spine/branch-protection.yaml")...)
	requireHoldErr(t, f.merge())
	if po := f.primaryOutcome(); po != nil {
		t.Fatalf("a hold recorded an attempt: %+v", po)
	}
	if len(f.g.merges) != 0 {
		t.Fatal("a held merge merged")
	}
	if _, err := f.decide(GovernanceApprove, gBound, w4Operator, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if f.run.Status != domain.RunStatusCompleted {
		t.Fatalf("run = %s, want completed (outcome %+v)", f.run.Status, f.primaryOutcome())
	}
	merge := f.callIndex("binder:MergeBound ", 0)
	v1 := f.callIndex("MergeBase "+gBase+" "+gBound, merge)
	govDiff := f.callIndex("Diff "+gBase+" "+gMerge, v1)
	marker := f.callIndex("store:merge", govDiff)
	push := f.callIndex("binder:PushCommit ", 0)
	if merge < 0 || v1 < 0 || govDiff < 0 || marker < 0 || push < 0 || !(merge < v1 && v1 < govDiff && govDiff < marker && marker < push) {
		t.Fatalf("order MergeBound %d < V1 %d < governance diff %d < marker %d < push %d violated; calls %v",
			merge, v1, govDiff, marker, push, f.g.calls)
	}
	if got := f.run.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA]; got != gMerge {
		t.Fatalf("consumption marker = %q, want %s", got, gMerge)
	}
}

// TestWave4_V1RefusalPrecedesTheGovernancePostMergeCheck: an approved
// merge whose result V1 refuses is taken back once and routed as a
// lifecycle refusal: no governance post-merge refusal, no consumption
// marker, nothing pushed.
func TestWave4_V1RefusalPrecedesTheGovernancePostMergeCheck(t *testing.T) {
	f := newW4Fake(t, defaultGuard(), true, w4Modified(".spine/branch-protection.yaml", "tasks/u.md")...)
	for _, ref := range []string{sha40('0'), gBound, gBase} {
		f.g.files[ref+":tasks/u.md"] = ulDoc("Task", "Pending")
	}
	f.g.files[gMerge+":tasks/u.md"] = ulDoc("Task", "Cancelled") // the merge result changes it
	requireHoldErr(t, f.merge())
	if _, err := f.decide(GovernanceApprove, gBound, w4Operator, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	po := f.primaryOutcome()
	if po == nil || !strings.Contains(po.FailureDetail, LifecycleMergeResultMismatch) {
		t.Fatalf("outcome = %+v, want merge_result_mismatch", po)
	}
	if len(f.g.pushes) != 0 || len(f.g.resets) != 1 || f.g.resets[0] != "main "+gBase+" "+gMerge {
		t.Fatalf("pushes %v, resets %v; want no push and one take-back to the base", f.g.pushes, f.g.resets)
	}
	if n := f.heldEvents(true); n != 0 {
		t.Fatalf("%d post-merge governance refusals; V1 refused first", n)
	}
	if got := f.run.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA]; got != "" {
		t.Fatalf("consumption marker %q written for a refused merge", got)
	}
	if f.run.Status != domain.RunStatusActive {
		t.Fatalf("run = %s, want the merge_failed route", f.run.Status)
	}
}

// TestWave4_V1TakesBackToTheBaseNeverAFirstParent: V1 refuses a
// fast-forward to a bound merge commit. verifyBoundMerge accepted it, so
// main held exactly BaseSHA before it: the take-back restores BaseSHA,
// never the bound commit's first parent (run-branch content), which
// TASK-005's takeBackRefusedMerge would pick.
func TestWave4_V1TakesBackToTheBaseNeverAFirstParent(t *testing.T) {
	f, _ := lcFix(t, true)
	// The author already set the target status: no cascade, so the merge
	// source is the bound commit, here a merge commit whose first parent
	// descends from main.
	f.git.files[ulBound+":"+ulTask] = ulDoc("Task", "Completed")
	runWork := sha40('e')
	f.git.parents[ulBound] = []string{runWork, sha40('f')}
	f.git.mergeRes = git.MergeResult{SHA: ulBound, BaseSHA: ulMain, FastForward: true}
	f.git.diffs[ulMain+" "+ulBound] = []git.FileDiff{{Path: "tasks/u.md", Status: "modified"}}
	f.git.files[ulMain+":tasks/u.md"] = ulDoc("Task", "Pending")
	f.git.files[ulBound+":tasks/u.md"] = ulDoc("Task", "Completed") // a status change V1 refuses
	if err := f.merge(); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	po := f.primaryOutcome()
	if po == nil || !strings.Contains(po.FailureDetail, "status_lifecycle_violation") {
		t.Fatalf("outcome = %+v, want a lifecycle refusal", po)
	}
	if len(f.git.resets) != 1 || f.git.resets[0] != "main "+ulMain+" "+ulBound {
		t.Fatalf("take-backs %v, want exactly [main %s %s] (never the first parent %s)", f.git.resets, ulMain, ulBound, runWork)
	}
	if len(f.git.pushes) != 0 {
		t.Fatalf("pushed %v", f.git.pushes)
	}
}

// TestWave4_FailedTakeBackBlocksGovernedMergesWhicheverCheckRefused: with
// the guard on, a refused merge whose take-back failed is a governance
// block whether TASK-005's verifyBoundMerge, TASK-017's V1 or the guard
// itself refused it: every governed merge of the process is refused as
// unavailable before it merges, until the local main no longer contains
// the refused merge. With the guard off nothing changes.
func TestWave4_FailedTakeBackBlocksGovernedMergesWhicheverCheckRefused(t *testing.T) {
	locked := &git.GitError{Kind: git.ErrKindTransient, Op: "reset_branch", Message: "repository locked"}
	for _, c := range []struct {
		name      string
		guard     bool
		lifecycle bool
		set       func(f *govFake)
	}{
		{"binding verification refuses", true, false, func(f *govFake) {
			f.g.parents[gMerge] = []string{gBase, sha40('9')} // not [base, source]
		}},
		{"V1 refuses", true, true, func(f *govFake) {
			f.g.diffs[gBase+" "+gMerge] = w4Modified("tasks/u.md")
			f.g.diffs[sha40('0')+" "+gBound] = w4Modified("tasks/u.md")
			for _, ref := range []string{sha40('0'), gBound, gBase} {
				f.g.files[ref+":tasks/u.md"] = ulDoc("Task", "Pending")
			}
			f.g.files[gMerge+":tasks/u.md"] = ulDoc("Task", "Cancelled")
		}},
		{"guard off: binding verification refuses, no block", false, false, func(f *govFake) {
			f.g.parents[gMerge] = []string{gBase, sha40('9')}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var guard *GovernanceGuard
			if c.guard {
				guard = defaultGuard()
			}
			f := newW4Fake(t, guard, c.lifecycle, w4Modified("src/a.go")...)
			c.set(f)
			f.g.resetErr = locked
			f.g.setHead("main", gMerge) // MergeBound moved the local main
			_ = f.merge()
			if len(f.g.resets) != 1 || len(f.g.pushes) != 0 {
				t.Fatalf("resets %v, pushes %v; want one failed take-back and no push", f.g.resets, f.g.pushes)
			}
			if f.run.Status != domain.RunStatusActive {
				t.Fatalf("run = %s, want the refusal's merge_failed route", f.run.Status)
			}
			key := f.run.CommitMeta[CommitMetaGovernanceBlockedTarget]
			if !c.guard {
				if key != "" {
					t.Fatalf("guard off recorded a block: %q", key)
				}
			} else if key != gMerge+" "+gBase {
				t.Fatalf("blocked-target key = %q, want %q", key, gMerge+" "+gBase)
			}

			run2 := &domain.Run{RunID: "run-other999", TraceID: "trace-ffffffffffffffff", Status: domain.RunStatusCommitting,
				TaskPath: w4Task, WorkflowPath: f.wf.Path, WorkflowID: f.wf.ID, WorkflowVersion: "v1",
				BranchName: "spine/run/run-other999", CurrentStepID: "publish",
				CommitMeta: map[string]string{CommitMetaBoundSHA: sha40('7'), CommitMetaBoundBy: boundByStep("review", "run-other999-review-1")}}
			f.store.runs[run2.RunID] = run2
			f.g.setHead(run2.BranchName, sha40('7'))
			f.g.files[sha40('7')+":"+w4Task] = ulDoc("Task", "Pending")
			err := f.orch.MergeRunBranch(context.Background(), run2.RunID)
			if !c.guard {
				if errors.Is(err, errGovernanceTargetBlocked) || len(f.g.merges) != 2 {
					t.Fatalf("guard off: err %v, merges %d; want the other run to merge", err, len(f.g.merges))
				}
				return
			}
			requireSpineCode(t, err, domain.ErrUnavailable)
			if !errors.Is(err, errGovernanceTargetBlocked) || len(f.g.merges) != 1 {
				t.Fatalf("other run: err %v, merges %d; want blocked before merging", err, len(f.g.merges))
			}
			// The take-back works again: the other run's gate completes it,
			// clears the block and merges.
			f.g.resetErr = nil
			_ = f.orch.MergeRunBranch(context.Background(), run2.RunID)
			if len(f.g.merges) != 2 || f.g.resets[len(f.g.resets)-2] != "main "+gBase+" "+gMerge {
				t.Fatalf("merges %d, resets %v; want the take-back completed, then the merge", len(f.g.merges), f.g.resets)
			}
			f.orch.governance.mu.Lock()
			n := len(f.orch.governance.blocks)
			f.orch.governance.mu.Unlock()
			if n != 0 {
				t.Fatalf("in-process blocks left: %d", n)
			}
		})
	}
}

// w4Exhausted records a primary outcome whose transient retries are spent
// (attempts 4 > 3), attempted now.
func w4Exhausted(t *testing.T, f *govFake) {
	t.Helper()
	now := time.Now().Add(time.Second)
	if err := f.store.UpsertRepositoryMergeOutcome(context.Background(), &domain.RepositoryMergeOutcome{
		RunID: f.run.RunID, RepositoryID: domain.PrimaryRepositoryID, Status: domain.RepositoryMergeStatusFailed,
		SourceBranch: f.run.BranchName, TargetBranch: "main", Attempts: 4, LastAttemptedAt: &now,
		FailureClass: domain.MergeFailureNetwork, FailureDetail: "connection reset",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWave4_RetryBudgetNeverAbandonsAHeldOrBlockedRun: TASK-012's
// exhaustion handler leaves in committing a run held for an operator
// decision (transient failures before the gate on a held run's ticks are
// counted, the hold itself never is) and a run whose refused merge is
// still blocked on the local main. Once an approval clears the hold the
// budget applies again; with the guard off it always does.
func TestWave4_RetryBudgetNeverAbandonsAHeldOrBlockedRun(t *testing.T) {
	ctx := context.Background()
	t.Run("held", func(t *testing.T) {
		f := newW4Fake(t, defaultGuard(), false, w4Modified(".spine/branch-protection.yaml")...)
		requireHoldErr(t, f.merge())
		w4Exhausted(t, f)
		abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
		if err != nil || abandoned || f.run.Status != domain.RunStatusCommitting {
			t.Fatalf("AbandonExhaustedMerge = %v, %v, run %s; want a held run left in committing", abandoned, err, f.run.Status)
		}
		// The approval clears the hold; its merge fails transiently, and
		// the budget (spent before the hold) applies again.
		f.g.mergeErr = &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "connection reset"}
		if _, err := f.decide(GovernanceApprove, gBound, w4Operator, ""); err != nil {
			t.Fatalf("approve: %v", err)
		}
		abandoned, err = f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
		if err != nil || !abandoned || f.run.Status != domain.RunStatusActive {
			t.Fatalf("after the approval: AbandonExhaustedMerge = %v, %v, run %s; want abandoned", abandoned, err, f.run.Status)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		f := newW4Fake(t, defaultGuard(), false, w4Modified("src/a.go")...)
		f.run.CommitMeta[CommitMetaGovernanceBlockedTarget] = gMerge + " " + gBase
		w4Exhausted(t, f)
		abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
		if err != nil || abandoned || f.run.Status != domain.RunStatusCommitting {
			t.Fatalf("AbandonExhaustedMerge = %v, %v, run %s; want a blocked run left in committing", abandoned, err, f.run.Status)
		}
	})
	t.Run("guard off", func(t *testing.T) {
		f := newW4Fake(t, nil, false, w4Modified("src/a.go")...)
		f.run.CommitMeta[CommitMetaGovernanceBlockedTarget] = gMerge + " " + gBase
		w4Exhausted(t, f)
		abandoned, err := f.orch.AbandonExhaustedMerge(ctx, f.run.RunID, 3)
		if err != nil || !abandoned {
			t.Fatalf("AbandonExhaustedMerge = %v, %v; want abandoned with the guard off", abandoned, err)
		}
	})
}

// TestWave4_TransientLifecycleFailureIsACountedAttempt: a retryable
// failure judging the bound commit is recorded as a failed, transient
// primary outcome (TASK-012's cross-task requirement), so the retry
// budget abandons it after maxRetries retries instead of retrying it every
// tick forever; MergeRunBranch still returns the error (TASK-017 AC 9).
func TestWave4_TransientLifecycleFailureIsACountedAttempt(t *testing.T) {
	f, g := lcFix(t, false)
	g.readErr[ulBound+":"+ulTask] = &git.GitError{Kind: git.ErrKindTransient, Op: "read_file", Message: "repository locked"}
	for i := 1; i <= 4; i++ {
		if err := f.merge(); err == nil {
			t.Fatalf("attempt %d: MergeRunBranch returned nil", i)
		}
		po := f.primaryOutcome()
		if po == nil || po.Attempts != i || po.Status != domain.RepositoryMergeStatusFailed || !po.FailureClass.IsTransient() {
			t.Fatalf("attempt %d: outcome %+v, want failed, transient, attempts %d", i, po, i)
		}
		if f.run.Status != domain.RunStatusCommitting || len(f.git.merges) != 0 {
			t.Fatalf("attempt %d: run %s, merges %d", i, f.run.Status, len(f.git.merges))
		}
	}
	abandoned, err := f.orch.AbandonExhaustedMerge(context.Background(), f.run.RunID, 3)
	if err != nil || !abandoned {
		t.Fatalf("AbandonExhaustedMerge = %v, %v; want abandoned after 3 retries", abandoned, err)
	}
	requireMergeFailedRoute(t, f, false)
}

// casGovStore adds TASK-008's step compare-and-swap capability to
// govStore, with a hook that moves the row before the next step write —
// conditional or blind — so a write from a stale copy is visible.
type casGovStore struct {
	*govStore
	before func()
}

func (s *casGovStore) fireBefore() {
	if h := s.before; h != nil {
		s.before = nil
		h()
	}
}

func (s *casGovStore) UpdateStepExecution(ctx context.Context, e *domain.StepExecution) error {
	s.fireBefore()
	return s.govStore.UpdateStepExecution(ctx, e)
}

func (s *casGovStore) TransitionStepExecution(ctx context.Context, e *domain.StepExecution, from domain.StepExecutionStatus, fromActor string) (bool, error) {
	s.fireBefore()
	cur, err := s.GetStepExecution(ctx, e.ExecutionID)
	if err != nil {
		return false, err
	}
	if cur.Status != from || cur.ActorID != fromActor {
		return false, nil
	}
	cp := *e
	return true, s.govStore.UpdateStepExecution(ctx, &cp)
}

// TestWave4_GovernanceStepWritesAreConditional: the hold record and its
// clearing on approval go through TASK-008's step compare-and-swap, so a
// stale copy of a step that another attempt finished meanwhile is never
// written back over it.
func TestWave4_GovernanceStepWritesAreConditional(t *testing.T) {
	t.Run("hold", func(t *testing.T) {
		f := newW4Fake(t, defaultGuard(), false, w4Modified(".spine/branch-protection.yaml")...)
		cs := &casGovStore{govStore: f.store}
		f.orch.store = cs
		publish := f.step("publish")
		cs.before = func() {
			publish.Status = domain.StepStatusCompleted
			publish.OutcomeID = "published"
		}
		requireHoldErr(t, f.merge())
		if p := f.step("publish"); p.Status != domain.StepStatusCompleted || p.ErrorDetail != nil {
			t.Fatalf("publish = %s with %+v; the hold overwrote a step that moved", p.Status, p.ErrorDetail)
		}
	})
	t.Run("clear", func(t *testing.T) {
		f := newW4Fake(t, defaultGuard(), false, w4Modified(".spine/branch-protection.yaml")...)
		requireHoldErr(t, f.merge())
		cs := &casGovStore{govStore: f.store}
		f.orch.store = cs
		cs.before = func() { f.step("publish").ActorID = "someone-else" }
		_, _ = f.decide(GovernanceApprove, gBound, w4Operator, "")
		if p := f.step("publish"); p.ActorID != "someone-else" {
			t.Fatalf("publish actor = %q; the clear overwrote a step that moved", p.ActorID)
		}
	})
}

// TestWave4_GovernanceDecisionMirrorsTheLifecycle: with the status
// lifecycle on, the decision API computes the governance facts from the
// lifecycle's merge source, as the gate does. A standard run whose branch
// adds a Draft task gets no cascade from the lifecycle (its additions keep
// their authored status), but would from TASK-005's cascade; computed from
// that commit the decision found nothing to approve and answered 412 for a
// held merge.
func TestWave4_GovernanceDecisionMirrorsTheLifecycle(t *testing.T) {
	diffs := []git.FileDiff{
		{Path: ".spine/branch-protection.yaml", Status: "modified"},
		{Path: w4Task, Status: "modified"},
		{Path: "tasks/new.md", Status: "added"},
	}
	f := newW4Fake(t, defaultGuard(), true, diffs...)
	f.run.CommitMeta["status"] = "Completed"
	f.g.files[gBound+":"+w4Task] = ulDoc("Task", "Completed") // the author set it
	f.g.files[gMerge+":"+w4Task] = ulDoc("Task", "Completed")
	f.g.files[gBound+":tasks/new.md"] = ulDoc("Task", "Draft")
	f.g.files[gMerge+":tasks/new.md"] = ulDoc("Task", "Draft")
	f.g.ontoSHA = sha40('c') // TASK-005's cascade would rewrite tasks/new.md
	f.g.parents[sha40('c')] = []string{gBound}
	requireHoldErr(t, f.merge())
	if len(f.g.ontoCalls) != 0 {
		t.Fatalf("the lifecycle built a cascade commit: %v", f.g.ontoCalls)
	}
	if _, err := f.decide(GovernanceApprove, gBound, w4Operator, ""); err != nil {
		t.Fatalf("approve of a held merge: %v", err)
	}
	if f.run.Status != domain.RunStatusCompleted {
		t.Fatalf("run = %s, want completed (outcome %+v)", f.run.Status, f.primaryOutcome())
	}

	t.Run("the lifecycle now refuses: nothing to decide", func(t *testing.T) {
		g := newW4Fake(t, defaultGuard(), true, w4Modified(".spine/branch-protection.yaml", "tasks/u.md")...)
		g.g.files[sha40('0')+":tasks/u.md"] = ulDoc("Task", "Pending")
		g.g.files[gBound+":tasks/u.md"] = ulDoc("Task", "Pending")
		requireHoldErr(t, g.merge())
		g.g.files[gBound+":tasks/u.md"] = ulDoc("Task", "Completed")
		_, err := g.decide(GovernanceApprove, gBound, w4Operator, "")
		requireSpineCode(t, err, domain.ErrPrecondition)
		if !errors.Is(err, errLifecycleRefusesMerge) {
			t.Fatalf("approve = %v, want the lifecycle's refusal", err)
		}
		if g.run.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] != "" {
			t.Fatal("an approval was recorded")
		}
		st, err := g.orch.GovernanceMergeStatus(context.Background(), g.run.RunID)
		if err != nil || st.EvaluationError != "the status lifecycle refuses the merge" {
			t.Fatalf("status = %+v, %v", st, err)
		}
	})
}
