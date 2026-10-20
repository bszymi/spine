package engine

// INIT-023 TASK-006 review round 1 regressions: decisions serialised with
// merge attempts (a reject during an approved attempt in flight; an
// approve whose write lands after a reject, and one between the reject's
// revocation and its route), merges that can never be evaluated (refused
// permanently instead of left committing), and the lifecycle run's
// starter recorded for the exemption's separation of duties.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// TestGovernanceDecision_RejectWaitsForAnInFlightApprovedMerge: an
// approved attempt sits in MergeBound — the approve's own merge, or a
// scheduler retry carrying an approval recorded earlier — and an admin
// rejects. The reject must not take effect underneath the attempt (before
// the stripe it routed the run back and answered success, and the attempt
// then merged and published the change anyway, leaving the run active
// with its content on main). It waits, sees the merge won and answers
// 409; the run is completed, the approval and its marker stand, and no
// rejected decision is recorded. Mutations: taking no stripe in
// DecideGovernanceMerge fails both subtests, none in MergeRunBranch the
// scheduler one.
func TestGovernanceDecision_RejectWaitsForAnInFlightApprovedMerge(t *testing.T) {
	for _, viaApprove := range []bool{true, false} {
		name := "approve's own merge"
		if !viaApprove {
			name = "scheduler retry carrying the approval"
		}
		t.Run(name, func(t *testing.T) {
			f := newGovFake(t, publishWorkflow(), defaultGuard(), true, govPaths...)
			requireHoldErr(t, f.merge())
			var (
				rejectErr error
				finished  = make(chan struct{})
			)
			f.g.mergeHook = func() {
				go func() {
					defer close(finished)
					_, rejectErr = f.decide(GovernanceReject, gBound, govAdmin, "no: this removes protection")
				}()
				select {
				case <-finished:
					t.Error("the reject completed while the approved merge was in flight")
				case <-time.After(200 * time.Millisecond):
				}
			}
			if viaApprove {
				res, err := f.decide(GovernanceApprove, gBound, govOperator, "")
				if err != nil || res.MergeError != "" || res.RunStatus != domain.RunStatusCompleted {
					t.Fatalf("approve: %v %+v; want the approved merge completed", err, res)
				}
			} else {
				f.run.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] = gBound
				f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorID] = "op-1"
				f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorRole] = "operator"
				f.run.CommitMeta[domain.CommitMetaGovernanceApprovalApprovedAt] = "2026-10-16T00:00:00Z"
				if err := f.merge(); err != nil {
					t.Fatalf("retry: %v", err)
				}
			}
			<-finished
			requireSpineCode(t, rejectErr, domain.ErrConflict)
			if f.run.Status != domain.RunStatusCompleted || len(f.g.pushes) != 1 {
				t.Fatalf("run %s pushes %v; want completed, pushed once", f.run.Status, f.g.pushes)
			}
			if !governanceApprovalMatches(f.run.CommitMeta, gBound) || f.run.CommitMeta[domain.CommitMetaGovernanceApprovalMergeSHA] != gMerge {
				t.Fatalf("approval or marker lost: %v", governanceKeys(f.run.CommitMeta))
			}
			for _, e := range f.eventsOf(domain.EventGovernanceMergeDecided) {
				if strings.Contains(string(e.Payload), `"decision":"rejected"`) {
					t.Fatal("a reject that lost to the merge recorded a rejected decision")
				}
			}
		})
	}
}

// TestGovernanceDecision_LateApproveCannotOutliveAReject: an approve
// passes its checks, then — before its write — a reject in another
// process revokes and routes the run back. The approval must not be
// recorded (before: it was written unconditionally, the approve answered
// 409 "nothing was merged", and the author's unchanged resubmit then
// merged with no hold). With the conditional store write and with the
// read-modify-write fallback alike: 409, no approval, no approved
// decision event, and the resubmitted commit is held again.
func TestGovernanceDecision_LateApproveCannotOutliveAReject(t *testing.T) {
	for _, merger := range []bool{true, false} {
		name := "conditional write"
		if !merger {
			name = "read-modify-write fallback"
		}
		t.Run(name, func(t *testing.T) {
			f := newGovFake(t, publishWorkflow(), defaultGuard(), merger, govPaths...)
			requireHoldErr(t, f.merge())
			once := sync.Once{}
			f.store.beforeWrite = func(string) {
				once.Do(func() {
					for k, v := range governanceApprovalTombstone() {
						f.run.CommitMeta[k] = v
					}
					f.run.Status = domain.RunStatusActive
				})
			}
			_, err := f.decide(GovernanceApprove, gBound, govOperator, "")
			requireSpineCode(t, err, domain.ErrConflict)
			f.store.beforeWrite = nil
			if a := governanceApprovalFrom(f.run.CommitMeta); a != nil {
				t.Fatalf("the late approval survived the reject: %+v", a)
			}
			if n := f.g.callCount("binder:MergeBound"); n != 0 {
				t.Fatalf("MergeBound called %d times", n)
			}
			for _, e := range f.eventsOf(domain.EventGovernanceMergeDecided) {
				if strings.Contains(string(e.Payload), `"decision":"approved"`) {
					t.Fatal("an approval that was not recorded was announced")
				}
			}
			// The author resubmits the same commit unchanged.
			f.run.Status = domain.RunStatusCommitting
			requireHoldErr(t, f.merge())
		})
	}
}

// TestGovernanceDecision_RejectRevokesAgainAfterItsRoute: an approve in
// another process that passed its checks writes its approval between the
// reject's revocation and the reject's route (the run is still committing
// then). The reject's second revocation, after the route, removes it, so
// the unchanged resubmit is held. Mutation: dropping
// revokeAgainAfterRoute merges the resubmit.
func TestGovernanceDecision_RejectRevokesAgainAfterItsRoute(t *testing.T) {
	for _, wf := range []struct {
		name string
		def  func() *domain.WorkflowDefinition
	}{{"publish step", publishWorkflow}, {"no publish step", legacyWorkflow}} {
		t.Run(wf.name, func(t *testing.T) {
			f := newGovFake(t, wf.def(), defaultGuard(), true, govPaths...)
			requireHoldErr(t, f.merge())
			once := sync.Once{}
			f.store.beforeTransition = func(from, _ domain.RunStatus) {
				if from != domain.RunStatusCommitting {
					return
				}
				once.Do(func() {
					f.run.CommitMeta[domain.CommitMetaGovernanceApprovalSHA] = gBound
					f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorID] = "op-2"
					f.run.CommitMeta[domain.CommitMetaGovernanceApprovalActorRole] = "operator"
					f.run.CommitMeta[domain.CommitMetaGovernanceApprovalApprovedAt] = "2026-10-16T00:00:00Z"
				})
			}
			if _, err := f.decide(GovernanceReject, gBound, govOperator, "no"); err != nil {
				t.Fatalf("reject: %v", err)
			}
			f.store.beforeTransition = nil
			if a := governanceApprovalFrom(f.run.CommitMeta); a != nil {
				t.Fatalf("an approval written during the reject survived it: %+v", a)
			}
			f.run.Status = domain.RunStatusCommitting
			requireHoldErr(t, f.merge())
		})
	}
}

// TestGovernanceGuard_UnevaluableMergeIsRefusedNotStuck: a merge whose
// changed paths can never be computed — no merge base (an orphan history
// on the run branch), a diff over the git output limit — used to return
// ErrUnavailable on every tick, with approve and reject refused too and
// cancel not allowed from committing: stuck for good. It is now refused
// permanently on the first attempt (nothing merged), routed like any
// permanent merge failure, and a decision on such a run answers 412.
// AC 29's "no undecidable state". Mutation: dropping the
// unevaluableForever branch in governanceGate leaves the run committing.
func TestGovernanceGuard_UnevaluableMergeIsRefusedNotStuck(t *testing.T) {
	tooLarge := &git.GitError{Kind: git.ErrKindPermanent, Op: "diff", Message: "output exceeds read limit",
		Err: &git.OutputTooLargeError{Op: "diff", Limit: 4096}}
	for _, c := range []struct {
		name  string
		setup func(*govFake)
		want  string
	}{
		{"no merge base", func(f *govFake) { f.g.bases["main "+gBound] = []string{} }, "shares no history"},
		{"diff over the output limit", func(f *govFake) { f.g.diffErr[sha40('0')+" "+gBound] = tooLarge }, "exceeds the git output limit"},
	} {
		for _, wf := range []struct {
			name   string
			def    func() *domain.WorkflowDefinition
			status domain.RunStatus
		}{{"publish step", publishWorkflow, domain.RunStatusActive}, {"no publish step", legacyWorkflow, domain.RunStatusFailed}} {
			t.Run(c.name+", "+wf.name, func(t *testing.T) {
				f := newGovFake(t, wf.def(), defaultGuard(), true, govPaths...)
				c.setup(f)
				_, err := f.decide(GovernanceReject, gBound, govOperator, "x")
				requireSpineCode(t, err, domain.ErrPrecondition)
				if st, err := f.orch.GovernanceMergeStatus(context.Background(), bindRunID); err != nil || !strings.Contains(st.EvaluationError, c.want) {
					t.Fatalf("status: %v %+v", err, st)
				}
				if err := f.merge(); err != nil {
					t.Fatalf("merge: %v; want the permanent-failure route (nil)", err)
				}
				if f.run.Status != wf.status {
					t.Fatalf("run = %s, want %s (not left committing)", f.run.Status, wf.status)
				}
				if wf.status == domain.RunStatusActive && f.step("publish").OutcomeID != "merge_failed" {
					t.Fatalf("publish outcome %q, want merge_failed", f.step("publish").OutcomeID)
				}
				if n := f.g.callCount("binder:MergeBound"); n != 0 {
					t.Fatalf("MergeBound called %d times", n)
				}
				if len(f.eventsOf(domain.EventGovernanceMergeHeld)) != 0 || f.store.holdWrites != 0 {
					t.Fatal("an unevaluable merge was recorded as a hold")
				}
				var detail string
				for _, o := range f.store.mergeOutcomes {
					if o.RepositoryID == domain.PrimaryRepositoryID {
						detail = o.FailureDetail
					}
				}
				if !strings.Contains(detail, "cannot be evaluated") || !strings.Contains(detail, c.want) {
					t.Fatalf("primary outcome detail %q", detail)
				}
			})
		}
	}
}

// TestLockGovernanceRun pins the stripe's contract: re-entrant on the same
// call chain, exclusive across chains, a waiter gives up with its context
// (ErrUnavailable), the release is idempotent, and a guard-off
// orchestrator takes nothing (the embedded zero value never waits).
func TestLockGovernanceRun(t *testing.T) {
	ctx := context.Background()
	held, unlock, err := lockGovernanceRun(ctx, "run-lock-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, inner, err := lockGovernanceRun(held, "run-lock-a"); err != nil {
		t.Fatalf("re-entrant take: %v", err)
	} else {
		inner()
	}
	wait, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, _, err := lockGovernanceRun(wait, "run-lock-a"); err == nil {
		t.Fatal("a second chain took a held stripe")
	} else {
		requireSpineCode(t, err, domain.ErrUnavailable)
	}

	// A guard-off orchestrator merges while the stripe is held elsewhere.
	f := newGovFake(t, publishWorkflow(), nil, true, govPaths...)
	f.run.RunID = "run-lock-a"
	f.store.runs = map[string]*domain.Run{"run-lock-a": f.run}
	done := make(chan error, 1)
	go func() { done <- f.orch.MergeRunBranch(ctx, "run-lock-a") }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a guard-off MergeRunBranch waited for the governance stripe")
	}

	unlock()
	unlock() // idempotent
	_, again, err := lockGovernanceRun(ctx, "run-lock-a")
	if err != nil {
		t.Fatalf("stripe not released: %v", err)
	}
	again()
}

// TestStartWorkflowPlanningRun_RecordsTheStarter: with the guard and the
// lifecycle exemption on, a workflow-lifecycle run records the actor who
// started it (and wrote its body) under CommitMetaGovernanceStartedBy;
// with either off, or without an actor, nothing is written. Both starters
// are covered: create (POST /workflows) and update (PUT /workflows/{id},
// review round 1 finding 2's attack shape; review round 2 gap — the update
// rows used to be declared but ran the create path).
func TestStartWorkflowPlanningRun_RecordsTheStarter(t *testing.T) {
	on := &governanceGuard{cfg: GovernanceGuard{Enabled: true, ExemptWorkflowLifecycleTaskPath: true}}
	actor := &domain.Actor{ActorID: "rev-1", Role: domain.RoleReviewer}
	for _, c := range []struct {
		name   string
		guard  *governanceGuard
		actor  *domain.Actor
		update bool
		want   string
	}{
		{"create, guard and exemption on", on, actor, false, "rev-1"},
		{"exemption off", &governanceGuard{cfg: GovernanceGuard{Enabled: true}}, actor, false, ""},
		{"guard off", nil, actor, false, ""},
		{"no actor", on, nil, false, ""},
		{"update, guard and exemption on", on, actor, true, "rev-1"},
		{"update, exemption off", &governanceGuard{cfg: GovernanceGuard{Enabled: true}}, actor, true, ""},
		{"update, guard off", nil, actor, true, ""},
		{"update, no actor", on, nil, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := &mockRunStore{}
			w := &mockWorkflowWriter{}
			var g GitOperator = &stubGitOperator{}
			if c.update {
				g = &workflowReadGit{files: map[string]string{"workflows/new-workflow.yaml": validWorkflowBody}}
			}
			orch := workflowPlanningOrchestrator(w, workflowPlanningResolver(), st, &mockEventEmitter{}, g)
			orch.governance = c.guard
			ctx := context.Background()
			if c.actor != nil {
				ctx = domain.WithActor(ctx, c.actor)
			}
			var res *StartRunResult
			var err error
			if c.update {
				res, err = orch.StartWorkflowUpdatePlanningRun(ctx, "new-workflow", bumpedWorkflowBody)
			} else {
				res, err = orch.StartWorkflowPlanningRun(ctx, "new-workflow", validWorkflowBody)
			}
			if err != nil {
				t.Fatal(err)
			}
			wantCreate, wantUpdate := 1, 0
			if c.update {
				wantCreate, wantUpdate = 0, 1
			}
			if w.createCalls != wantCreate || w.updateCalls != wantUpdate {
				t.Fatalf("writer: create=%d update=%d, want %d/%d", w.createCalls, w.updateCalls, wantCreate, wantUpdate)
			}
			got := st.runs[res.Run.RunID].CommitMeta[CommitMetaGovernanceStartedBy]
			if got != c.want {
				t.Fatalf("started_by = %q, want %q", got, c.want)
			}
		})
	}
}
