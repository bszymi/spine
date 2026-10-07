package refadmit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

// fakeReader is an in-memory RunReader that counts every call.
type fakeReader struct {
	runs     map[string]*domain.Run
	execs    map[string][]domain.StepExecution
	branches map[string]*domain.Branch
	err      error // when set, every call fails with it

	getRun, listExecs, getBranch int
}

func (f *fakeReader) calls() int { return f.getRun + f.listExecs + f.getBranch }

func (f *fakeReader) GetRun(_ context.Context, id string) (*domain.Run, error) {
	f.getRun++
	if f.err != nil {
		return nil, f.err
	}
	if r, ok := f.runs[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "run not found")
}

func (f *fakeReader) ListStepExecutionsByRun(_ context.Context, id string) ([]domain.StepExecution, error) {
	f.listExecs++
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.StepExecution(nil), f.execs[id]...), nil
}

func (f *fakeReader) GetBranch(_ context.Context, id string) (*domain.Branch, error) {
	f.getBranch++
	if f.err != nil {
		return nil, f.err
	}
	if b, ok := f.branches[id]; ok {
		cp := *b
		return &cp, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "branch not found")
}

// fakeDefs is a StepDefLookup keyed by step ID; a missing step yields a
// nil definition ("cannot resolve").
type fakeDefs struct {
	types map[string]domain.StepType
	calls int
}

func (f *fakeDefs) LookupStepDef(_ context.Context, _, stepID string) (*domain.StepDefinition, *domain.Run) {
	f.calls++
	t, ok := f.types[stepID]
	if !ok {
		return nil, nil
	}
	return &domain.StepDefinition{ID: stepID, Type: t}, nil
}

const (
	fxRun       = "run-0a1b2c3d"
	fxRunBranch = "spine/run/task-001-x-0a1b2c3d"
	fxRunRef    = "refs/heads/" + fxRunBranch
)

func actor(id string, role domain.ActorRole) *domain.Actor {
	return &domain.Actor{ActorID: id, Role: role}
}

// runFixture is TASK-016 AC 3's run: active, current step `execute`,
// execution e1 held by alice (in_progress, repository spine). carol
// holds an assigned execution of a non-current step and dan holds the
// current step ID on a divergence-branch execution.
func runFixture() *fakeReader {
	return &fakeReader{
		runs: map[string]*domain.Run{fxRun: {
			RunID: fxRun, Status: domain.RunStatusActive, CurrentStepID: "execute", BranchName: fxRunBranch,
		}},
		execs: map[string][]domain.StepExecution{fxRun: {
			{ExecutionID: "e0", RunID: fxRun, StepID: "draft", ActorID: "carol", Status: domain.StepStatusAssigned, RepositoryID: "spine"},
			{ExecutionID: "e1", RunID: fxRun, StepID: "execute", ActorID: "alice", Status: domain.StepStatusInProgress, RepositoryID: "spine"},
			// Newer than e1, so a binding that ignored BranchID would
			// take it as the latest attempt of `execute`.
			{ExecutionID: "eb", RunID: fxRun, StepID: "execute", BranchID: "some-branch", ActorID: "dan", Status: domain.StepStatusInProgress, RepositoryID: "spine"},
		}},
	}
}

func defsFixture() *fakeDefs {
	return &fakeDefs{types: map[string]domain.StepType{
		"draft": domain.StepTypeManual, "execute": domain.StepTypeAutomated, "explore": domain.StepTypeManual,
	}}
}

func decide(t *testing.T, rr RunReader, defs StepDefLookup, req Request) Decision {
	t.Helper()
	ev := NewEvaluator(rr)
	if defs != nil {
		ev.WithStepDefs(defs)
	}
	d, err := ev.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide(%+v): unexpected error %v", req, err)
	}
	return d
}

// TestDecide_RunBranch is TASK-016 AC 3 with its design-check rows
// (DC-6 latest attempt, DC-7 repoOrPrimary) and the owner question 4
// review freeze. Mutation checks: see the task's Implementation Notes.
func TestDecide_RunBranch(t *testing.T) {
	alice := actor("alice", domain.RoleContributor)

	t.Run("allowed", func(t *testing.T) {
		for _, st := range []domain.StepExecutionStatus{domain.StepStatusAssigned, domain.StepStatusInProgress} {
			rr := runFixture()
			rr.execs[fxRun][1].Status = st
			d := decide(t, rr, defsFixture(), Request{Ref: fxRunRef, Actor: alice})
			if !d.Allow || d.Reason != ReasonBoundActor || d.ExecutionID != "e1" || d.RunID != fxRun || d.Class != ClassRunBranch {
				t.Errorf("alice with %s execution: %+v, want bound_actor allow on e1", st, d)
			}
		}
		// DC-7: the literal primary spelling, and a legacy "" row.
		d := decide(t, runFixture(), defsFixture(), Request{Ref: fxRunRef, Actor: alice, RepositoryID: "spine"})
		if !d.Allow {
			t.Errorf(`RepositoryID "spine": %+v, want allow`, d)
		}
		rr := runFixture()
		rr.execs[fxRun][1].RepositoryID = ""
		if d := decide(t, rr, defsFixture(), Request{Ref: fxRunRef, Actor: alice, RepositoryID: ""}); !d.Allow {
			t.Errorf(`legacy "" execution repository: %+v, want allow`, d)
		}
		// A reviewer-role actor (above contributor) binds the same way.
		rr = runFixture()
		rr.execs[fxRun][1].ActorID = "rita"
		if d := decide(t, rr, defsFixture(), Request{Ref: fxRunRef, Actor: actor("rita", domain.RoleReviewer)}); !d.Allow {
			t.Errorf("bound reviewer-role actor: %+v, want allow", d)
		}
	})

	t.Run("operators never read the store", func(t *testing.T) {
		for _, role := range []domain.ActorRole{domain.RoleOperator, domain.RoleAdmin} {
			for _, del := range []bool{false, true} {
				rr := runFixture()
				defs := defsFixture()
				d := decide(t, rr, defs, Request{Ref: fxRunRef, Actor: actor("op", role), Delete: del})
				if !d.Allow || d.Reason != ReasonOperator {
					t.Errorf("%s delete=%v: %+v, want operator allow", role, del, d)
				}
				if rr.calls() != 0 || defs.calls != 0 {
					t.Errorf("%s delete=%v: %d reader / %d def calls, want 0", role, del, rr.calls(), defs.calls)
				}
				// Even with no reader and no hook at all.
				if d := decide(t, nil, nil, Request{Ref: fxRunRef, Actor: actor("op", role), Delete: del}); !d.Allow {
					t.Errorf("%s delete=%v with nil reader: %+v, want allow", role, del, d)
				}
			}
		}
	})

	type denyCase struct {
		name   string
		mutate func(rr *fakeReader)
		req    Request
		reason Reason
	}
	denies := []denyCase{
		{name: "another contributor", req: Request{Ref: fxRunRef, Actor: actor("bob", domain.RoleContributor)}, reason: ReasonNotBound},
		{name: "alice as reader", req: Request{Ref: fxRunRef, Actor: actor("alice", domain.RoleReader)}, reason: ReasonNotBound},
		{name: "nil actor", req: Request{Ref: fxRunRef}, reason: ReasonNotBound},
		{name: "empty actor ID", req: Request{Ref: fxRunRef, Actor: actor("", domain.RoleContributor)}, reason: ReasonNotBound},
		{name: "holder of a non-current step", req: Request{Ref: fxRunRef, Actor: actor("carol", domain.RoleContributor)}, reason: ReasonNotBound},
		{name: "holder of the current step on a branch execution", req: Request{Ref: fxRunRef, Actor: actor("dan", domain.RoleContributor)}, reason: ReasonNotBound},
		{name: "same suffix, different slug", req: Request{Ref: "refs/heads/spine/run/task-002-y-0a1b2c3d", Actor: alice}, reason: ReasonUnresolved},
		{name: "planning prefix on a standard run's name", req: Request{Ref: "refs/heads/spine/plan/task-001-x-0a1b2c3d", Actor: alice}, reason: ReasonUnresolved},
		{name: "unknown run", req: Request{Ref: "refs/heads/spine/run/task-001-x-ffffffff", Actor: alice}, reason: ReasonUnresolved},
		{name: "repository mismatch", req: Request{Ref: fxRunRef, Actor: alice, RepositoryID: "payments"}, reason: ReasonNotBound},
		{name: "alice deleting", req: Request{Ref: fxRunRef, Actor: alice, Delete: true}, reason: ReasonDeleteRequiresOperator},
		{name: "run has no current step", mutate: func(rr *fakeReader) { rr.runs[fxRun].CurrentStepID = "" }, req: Request{Ref: fxRunRef, Actor: alice}, reason: ReasonNotBound},
		{name: "run diverged", mutate: func(rr *fakeReader) { rr.runs[fxRun].CurrentStepID = "divergence:" + fxRun + "-div-d" }, req: Request{Ref: fxRunRef, Actor: alice}, reason: ReasonNotBound},
		{name: "open slot (assigned, no actor)", mutate: func(rr *fakeReader) {
			rr.execs[fxRun][1].ActorID = ""
			rr.execs[fxRun][1].Status = domain.StepStatusAssigned
		}, req: Request{Ref: fxRunRef, Actor: actor("", domain.RoleContributor)}, reason: ReasonNotBound},
	}
	for _, st := range []domain.StepExecutionStatus{
		domain.StepStatusWaiting, domain.StepStatusBlocked, domain.StepStatusCompleted,
		domain.StepStatusFailed, domain.StepStatusSkipped,
	} {
		st := st
		denies = append(denies, denyCase{
			name:   "execution " + string(st),
			mutate: func(rr *fakeReader) { rr.execs[fxRun][1].Status = st },
			req:    Request{Ref: fxRunRef, Actor: alice}, reason: ReasonNotBound,
		})
	}
	for _, st := range []domain.RunStatus{
		domain.RunStatusPending, domain.RunStatusPaused, domain.RunStatusCommitting,
		domain.RunStatusPartiallyMerged, domain.RunStatusCompleted, domain.RunStatusFailed,
		domain.RunStatusCancelled,
	} {
		st := st
		denies = append(denies, denyCase{
			name:   "run " + string(st),
			mutate: func(rr *fakeReader) { rr.runs[fxRun].Status = st },
			req:    Request{Ref: fxRunRef, Actor: alice}, reason: ReasonNotBound,
		})
	}
	for _, tc := range denies {
		t.Run("denied/"+tc.name, func(t *testing.T) {
			rr := runFixture()
			if tc.mutate != nil {
				tc.mutate(rr)
			}
			d := decide(t, rr, defsFixture(), tc.req)
			if d.Allow || d.Reason != tc.reason {
				t.Errorf("got %+v, want deny %s", d, tc.reason)
			}
			if msg := d.Message(); !strings.HasPrefix(msg, "ref-admission: ") || strings.Contains(msg, "alice") {
				t.Errorf("message %q: want a ref-admission: line that names no bound actor", msg)
			}
		})
	}

	t.Run("DC-6 latest attempt wins", func(t *testing.T) {
		rr := runFixture()
		rr.execs[fxRun] = append(rr.execs[fxRun], domain.StepExecution{
			ExecutionID: "e2", RunID: fxRun, StepID: "execute", ActorID: "bob", Status: domain.StepStatusAssigned, RepositoryID: "spine",
		})
		if d := decide(t, rr, defsFixture(), Request{Ref: fxRunRef, Actor: alice}); d.Allow {
			t.Errorf("alice on the older live attempt: %+v, want deny", d)
		}
		if d := decide(t, rr, defsFixture(), Request{Ref: fxRunRef, Actor: actor("bob", domain.RoleContributor)}); !d.Allow || d.ExecutionID != "e2" {
			t.Errorf("bob on the newer attempt: %+v, want allow on e2", d)
		}
	})

	t.Run("non-primary repository uses RepositoryBranches", func(t *testing.T) {
		rr := runFixture()
		rr.runs[fxRun].RepositoryBranches = map[string]string{"payments": "spine/run/payments-x-0a1b2c3d"}
		rr.execs[fxRun][1].RepositoryID = "payments"
		d := decide(t, rr, defsFixture(), Request{Ref: "refs/heads/spine/run/payments-x-0a1b2c3d", Actor: alice, RepositoryID: "payments"})
		if !d.Allow {
			t.Errorf("code-repo branch: %+v, want allow", d)
		}
		d = decide(t, rr, defsFixture(), Request{Ref: fxRunRef, Actor: alice, RepositoryID: "payments"})
		if d.Allow || d.Reason != ReasonUnresolved {
			t.Errorf("primary name pushed to a code repo with its own branch: %+v, want unresolved", d)
		}
	})

	t.Run("review freeze (owner question 4)", func(t *testing.T) {
		for _, st := range []domain.StepType{domain.StepTypeReview, domain.StepTypeInternal} {
			defs := defsFixture()
			defs.types["execute"] = st
			d := decide(t, runFixture(), defs, Request{Ref: fxRunRef, Actor: alice})
			if d.Allow || d.Reason != ReasonReviewFrozen {
				t.Errorf("bound actor during a %s step: %+v, want review_frozen", st, d)
			}
			if d := decide(t, runFixture(), defs, Request{Ref: fxRunRef, Actor: actor("op", domain.RoleOperator)}); !d.Allow {
				t.Errorf("operator during a %s step: %+v, want allow", st, d)
			}
		}
		for _, st := range []domain.StepType{domain.StepTypeManual, domain.StepTypeAutomated, domain.StepTypeConvergence} {
			defs := defsFixture()
			defs.types["execute"] = st
			if d := decide(t, runFixture(), defs, Request{Ref: fxRunRef, Actor: alice}); !d.Allow {
				t.Errorf("bound actor during a %s step: %+v, want allow", st, d)
			}
		}
		defs := defsFixture()
		delete(defs.types, "execute")
		if d := decide(t, runFixture(), defs, Request{Ref: fxRunRef, Actor: alice}); d.Allow || d.Reason != ReasonStepUnresolved {
			t.Errorf("unresolvable step definition: %+v, want step_unresolved", d)
		}
		rr := runFixture()
		if d := decide(t, rr, nil, Request{Ref: fxRunRef, Actor: alice}); d.Allow || d.Reason != ReasonResolverUnavailable {
			t.Errorf("no step-definition hook: %+v, want resolver_unavailable", d)
		}
		if rr.calls() != 0 {
			t.Errorf("no step-definition hook: %d reader calls, want 0", rr.calls())
		}
		// The hook is consulted only once the actor is bound.
		defs = defsFixture()
		decide(t, runFixture(), defs, Request{Ref: fxRunRef, Actor: actor("bob", domain.RoleContributor)})
		if defs.calls != 0 {
			t.Errorf("unbound actor: %d step-definition lookups, want 0", defs.calls)
		}
	})
}

// divergenceFixture: run-0a1b2c3d is diverged into run-0a1b2c3d-div-d.
// Branch b1's current step `explore` is held by dave, sibling b2's by
// erin, and alice still holds a run-level execution.
func divergenceFixture() *fakeReader {
	const div = fxRun + "-div-d"
	return &fakeReader{
		runs: map[string]*domain.Run{fxRun: {
			RunID: fxRun, Status: domain.RunStatusActive, CurrentStepID: "divergence:" + div, BranchName: fxRunBranch,
		}},
		execs: map[string][]domain.StepExecution{fxRun: {
			{ExecutionID: "e1", RunID: fxRun, StepID: "execute", ActorID: "alice", Status: domain.StepStatusInProgress, RepositoryID: "spine"},
			{ExecutionID: "x1", RunID: fxRun, StepID: "explore", BranchID: div + "-b1", ActorID: "dave", Status: domain.StepStatusAssigned, RepositoryID: "spine"},
			{ExecutionID: "x2", RunID: fxRun, StepID: "explore", BranchID: div + "-b2", ActorID: "erin", Status: domain.StepStatusInProgress, RepositoryID: "spine"},
			{ExecutionID: "x3", RunID: fxRun, StepID: "explore", BranchID: div + "-b/1", ActorID: "fay", Status: domain.StepStatusInProgress, RepositoryID: "spine"},
		}},
		branches: map[string]*domain.Branch{
			div + "-b1":  {BranchID: div + "-b1", RunID: fxRun, DivergenceID: div, Status: domain.BranchStatusInProgress, CurrentStepID: "explore"},
			div + "-b2":  {BranchID: div + "-b2", RunID: fxRun, DivergenceID: div, Status: domain.BranchStatusInProgress, CurrentStepID: "explore"},
			div + "-b/1": {BranchID: div + "-b/1", RunID: fxRun, DivergenceID: div, Status: domain.BranchStatusPending, CurrentStepID: "explore"},
		},
	}
}

const fxDivRef = "refs/heads/spine/" + fxRun + "/" + fxRun + "-div-d/b1"

// TestDecide_Divergence is TASK-016 AC 4.
func TestDecide_Divergence(t *testing.T) {
	dave := actor("dave", domain.RoleContributor)
	d := decide(t, divergenceFixture(), defsFixture(), Request{Ref: fxDivRef, Actor: dave})
	if !d.Allow || d.Reason != ReasonBoundActor || d.ExecutionID != "x1" || d.Class != ClassDivergenceBranch {
		t.Fatalf("dave on his branch: %+v, want bound_actor allow on x1", d)
	}
	d = decide(t, divergenceFixture(), defsFixture(), Request{Ref: "refs/heads/spine/" + fxRun + "/" + fxRun + "-div-d/b/1", Actor: actor("fay", domain.RoleContributor)})
	if !d.Allow {
		t.Errorf("suffix with an extra '/': %+v, want allow", d)
	}

	cases := []struct {
		name   string
		mutate func(rr *fakeReader)
		req    Request
		reason Reason
	}{
		{name: "run-level actor", req: Request{Ref: fxDivRef, Actor: actor("alice", domain.RoleContributor)}, reason: ReasonNotBound},
		{name: "sibling branch actor", req: Request{Ref: fxDivRef, Actor: actor("erin", domain.RoleContributor)}, reason: ReasonNotBound},
		{name: "branch completed", mutate: func(rr *fakeReader) { rr.branches[fxRun+"-div-d-b1"].Status = domain.BranchStatusCompleted }, req: Request{Ref: fxDivRef, Actor: dave}, reason: ReasonNotBound},
		{name: "branch failed", mutate: func(rr *fakeReader) { rr.branches[fxRun+"-div-d-b1"].Status = domain.BranchStatusFailed }, req: Request{Ref: fxDivRef, Actor: dave}, reason: ReasonNotBound},
		{name: "branch of another run", mutate: func(rr *fakeReader) { rr.branches[fxRun+"-div-d-b1"].RunID = "run-ffffffff" }, req: Request{Ref: fxDivRef, Actor: dave}, reason: ReasonUnresolved},
		{name: "branch of another divergence", mutate: func(rr *fakeReader) { rr.branches[fxRun+"-div-d-b1"].DivergenceID = fxRun + "-div-e" }, req: Request{Ref: fxDivRef, Actor: dave}, reason: ReasonUnresolved},
		{name: "segment not prefixed <run>-div-", req: Request{Ref: "refs/heads/spine/" + fxRun + "/other-div-d/b1", Actor: dave}, reason: ReasonUnresolved},
		{name: "unknown branch", req: Request{Ref: "refs/heads/spine/" + fxRun + "/" + fxRun + "-div-d/b9", Actor: dave}, reason: ReasonUnresolved},
		{name: "branch cursor moved past dave's step", mutate: func(rr *fakeReader) { rr.branches[fxRun+"-div-d-b1"].CurrentStepID = "judge" }, req: Request{Ref: fxDivRef, Actor: dave}, reason: ReasonNotBound},
		{name: "dave deleting", req: Request{Ref: fxDivRef, Actor: dave, Delete: true}, reason: ReasonDeleteRequiresOperator},
		{name: "run not active", mutate: func(rr *fakeReader) { rr.runs[fxRun].Status = domain.RunStatusPaused }, req: Request{Ref: fxDivRef, Actor: dave}, reason: ReasonNotBound},
		{name: "run branch while diverged", req: Request{Ref: fxRunRef, Actor: actor("alice", domain.RoleContributor)}, reason: ReasonNotBound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := divergenceFixture()
			if tc.mutate != nil {
				tc.mutate(rr)
			}
			d := decide(t, rr, defsFixture(), tc.req)
			if d.Allow || d.Reason != tc.reason {
				t.Errorf("got %+v, want deny %s", d, tc.reason)
			}
		})
	}

	t.Run("non-prefixed segment makes no branch read", func(t *testing.T) {
		rr := divergenceFixture()
		decide(t, rr, defsFixture(), Request{Ref: "refs/heads/spine/" + fxRun + "/other-div-d/b1", Actor: dave})
		if rr.getBranch != 0 {
			t.Errorf("GetBranch calls = %d, want 0", rr.getBranch)
		}
	})

	t.Run("review freeze applies to the branch's step", func(t *testing.T) {
		defs := defsFixture()
		defs.types["explore"] = domain.StepTypeReview
		if d := decide(t, divergenceFixture(), defs, Request{Ref: fxDivRef, Actor: dave}); d.Allow || d.Reason != ReasonReviewFrozen {
			t.Errorf("dave during a review step: %+v, want review_frozen", d)
		}
	})

	t.Run("operator", func(t *testing.T) {
		rr := divergenceFixture()
		if d := decide(t, rr, defsFixture(), Request{Ref: fxDivRef, Actor: actor("op", domain.RoleOperator), Delete: true}); !d.Allow {
			t.Errorf("operator delete: %+v, want allow", d)
		}
		if rr.calls() != 0 {
			t.Errorf("operator: %d reader calls, want 0", rr.calls())
		}
	})
}

// TestDecide_FailClosed is TASK-016 AC 5 with the DC-5 split cap.
func TestDecide_FailClosed(t *testing.T) {
	contributor := actor("alice", domain.RoleContributor)

	t.Run("nil reader", func(t *testing.T) {
		for _, ref := range []string{fxRunRef, fxDivRef} {
			if d := decide(t, nil, defsFixture(), Request{Ref: ref, Actor: contributor}); d.Allow || d.Reason != ReasonResolverUnavailable {
				t.Errorf("%s contributor: %+v, want resolver_unavailable", ref, d)
			}
			if d := decide(t, nil, defsFixture(), Request{Ref: ref, Actor: actor("op", domain.RoleOperator)}); !d.Allow {
				t.Errorf("%s operator: %+v, want allow", ref, d)
			}
		}
	})

	t.Run("store error is returned", func(t *testing.T) {
		boom := errors.New("connection refused")
		for _, ref := range []string{fxRunRef, fxDivRef} {
			rr := runFixture()
			rr.err = boom
			ev := NewEvaluator(rr).WithStepDefs(defsFixture())
			d, err := ev.Decide(context.Background(), Request{Ref: ref, Actor: contributor})
			if !errors.Is(err, boom) || d.Allow {
				t.Errorf("%s: decision %+v err %v, want the store error and no allow", ref, d, err)
			}
			// The evaluator stays refused for the rest of the push.
			d, err = ev.Decide(context.Background(), Request{Ref: ref, Actor: contributor})
			if err != nil || d.Reason != ReasonNotEvaluated {
				t.Errorf("%s after an error: %+v %v, want not_evaluated", ref, d, err)
			}
		}
		divRR := divergenceFixture()
		divRR.branches = nil
		ev := NewEvaluator(&erroringBranches{fakeReader: divRR, err: boom}).WithStepDefs(defsFixture())
		if _, err := ev.Decide(context.Background(), Request{Ref: fxDivRef, Actor: actor("dave", domain.RoleContributor)}); !errors.Is(err, boom) {
			t.Errorf("GetBranch error: %v, want it returned", err)
		}
	})

	t.Run("not_evaluated bounds reads after the first denial", func(t *testing.T) {
		rr := runFixture()
		ev := NewEvaluator(rr).WithStepDefs(defsFixture())
		bob := actor("bob", domain.RoleContributor)
		if d, _ := ev.Decide(context.Background(), Request{Ref: fxRunRef, Actor: bob}); d.Allow {
			t.Fatalf("bob admitted: %+v", d)
		}
		for i := 0; i < 50; i++ {
			ref := fmt.Sprintf("refs/heads/spine/run/foreign-%d-%08x", i, i)
			d, err := ev.Decide(context.Background(), Request{Ref: ref, Actor: bob})
			if err != nil || d.Allow || d.Reason != ReasonNotEvaluated {
				t.Fatalf("ref %d: %+v %v, want not_evaluated", i, d, err)
			}
		}
		if rr.calls() > 2 {
			t.Errorf("reader calls = %d after 51 refs, want <= 2", rr.calls())
		}
		// Ordinary branches still pass, and operator refs are unaffected.
		if d, _ := ev.Decide(context.Background(), Request{Ref: "refs/heads/feature/x", Actor: bob}); !d.Allow {
			t.Errorf("ordinary branch after a denial: %+v, want allow", d)
		}
	})

	t.Run("bound actor's own refs are memoized", func(t *testing.T) {
		rr := runFixture()
		defs := defsFixture()
		ev := NewEvaluator(rr).WithStepDefs(defs)
		for i := 0; i < 5; i++ {
			if d, err := ev.Decide(context.Background(), Request{Ref: fxRunRef, Actor: contributor}); err != nil || !d.Allow {
				t.Fatalf("push %d: %+v %v", i, d, err)
			}
		}
		if rr.getRun != 1 || rr.listExecs != 1 || defs.calls != 1 {
			t.Errorf("reads: GetRun=%d ListStepExecutionsByRun=%d LookupStepDef=%d, want 1 each", rr.getRun, rr.listExecs, defs.calls)
		}
	})

	// Review round 1: the parser does not dedupe refs and the
	// not_evaluated short-circuit only trips after a refusal, so a
	// bound actor repeating their own divergence ref (~8,000 times fit
	// the 1 MiB command section) cost one GetBranch per repetition.
	// Mutation check: dropping findBranch's memo fails the counts.
	t.Run("bound actor's repeated divergence refs are memoized", func(t *testing.T) {
		dave := actor("dave", domain.RoleContributor)
		for _, tc := range []struct {
			name, ref     string
			actor         *domain.Actor
			wantGetBranch int
		}{
			{"one split", fxDivRef, dave, 1},
			{"suffix with a '/'", "refs/heads/spine/" + fxRun + "/" + fxRun + "-div-d/b/1", actor("fay", domain.RoleContributor), 1},
		} {
			rr := divergenceFixture()
			defs := defsFixture()
			ev := NewEvaluator(rr).WithStepDefs(defs)
			for i := 0; i < 1000; i++ {
				if d, err := ev.Decide(context.Background(), Request{Ref: tc.ref, Actor: tc.actor}); err != nil || !d.Allow {
					t.Fatalf("%s, repetition %d: %+v %v, want allow", tc.name, i, d, err)
				}
			}
			if rr.getBranch != tc.wantGetBranch || rr.getRun != 1 || rr.listExecs != 1 || defs.calls != 1 {
				t.Errorf("%s, 1000 repetitions: GetBranch=%d GetRun=%d ListStepExecutionsByRun=%d LookupStepDef=%d, want %d/1/1/1",
					tc.name, rr.getBranch, rr.getRun, rr.listExecs, defs.calls, tc.wantGetBranch)
			}
		}

		// A miss is memoized too: two splits are tried once, then the
		// second split's hit is reused.
		rr := divergenceFixture()
		const nested = fxRun + "-div-d/x"
		rr.branches[nested+"-y"] = &domain.Branch{BranchID: nested + "-y", RunID: fxRun, DivergenceID: nested, Status: domain.BranchStatusInProgress, CurrentStepID: "explore"}
		ev := NewEvaluator(rr)
		for i := 0; i < 3; i++ {
			b, err := ev.findBranch(context.Background(), fxRun, nested+"/y")
			if err != nil || b == nil || b.BranchID != nested+"-y" {
				t.Fatalf("findBranch(%s/y) = %+v, %v", nested, b, err)
			}
			if b, err := ev.findBranch(context.Background(), fxRun, fxRun+"-div-d/zz"); err != nil || b != nil {
				t.Fatalf("findBranch(unknown) = %+v, %v; want nil, nil", b, err)
			}
		}
		if rr.getBranch != 3 {
			t.Errorf("GetBranch calls = %d, want 3 (two splits, one miss, each once)", rr.getBranch)
		}
	})

	t.Run("DC-5 split cap", func(t *testing.T) {
		// A long divergence path under the DC-4 bound: every '/' is a
		// candidate split, every GetBranch misses.
		rr := &fakeReader{runs: map[string]*domain.Run{fxRun: {RunID: fxRun, Status: domain.RunStatusActive, CurrentStepID: "divergence:x"}}}
		path := fxRun + "-div-d" + strings.Repeat("/a", 400)
		ref := "refs/heads/spine/" + fxRun + "/" + path
		if c := Classify(ref); c.Class != ClassDivergenceBranch {
			t.Fatalf("fixture class %s, want divergence_branch (len %d)", c.Class, len(ref))
		}
		d := decide(t, rr, defsFixture(), Request{Ref: ref, Actor: contributor})
		if d.Allow || d.Reason != ReasonUnresolved {
			t.Errorf("got %+v, want unresolved", d)
		}
		if rr.getBranch > maxDivergenceSplits {
			t.Errorf("GetBranch calls = %d, want <= %d", rr.getBranch, maxDivergenceSplits)
		}

		// The AC's literal 10,000-segment path, straight at the
		// resolver (Classify would already make such a ref reserved
		// under DC-4, which is the other half of the bound).
		rr = &fakeReader{}
		ev := NewEvaluator(rr)
		b, err := ev.findBranch(context.Background(), fxRun, fxRun+"-div-d"+strings.Repeat("/a", 10000))
		if b != nil || err != nil {
			t.Errorf("findBranch = %+v, %v; want nil, nil", b, err)
		}
		if rr.getBranch > maxDivergenceSplits {
			t.Errorf("10,000 segments: GetBranch calls = %d, want <= %d", rr.getBranch, maxDivergenceSplits)
		}
		if d := decide(t, rr, defsFixture(), Request{Ref: "refs/heads/spine/" + fxRun + "/" + fxRun + "-div-d" + strings.Repeat("/a", 10000), Actor: contributor}); d.Reason != ReasonOperatorRequired {
			t.Errorf("over-long divergence ref: %+v, want operator_required (reserved)", d)
		}
	})
}

// erroringBranches fails GetBranch only.
type erroringBranches struct {
	*fakeReader
	err error
}

func (e *erroringBranches) GetBranch(context.Context, string) (*domain.Branch, error) {
	return nil, e.err
}

// TestDecide_NonRunClasses is TASK-016 AC 6, with the DC-2 invalid
// class and the TASK-002 D12 shadowing rule.
func TestDecide_NonRunClasses(t *testing.T) {
	nonOps := map[string]*domain.Actor{
		"contributor": actor("c", domain.RoleContributor),
		"reviewer":    actor("r", domain.RoleReviewer),
		"reader":      actor("rd", domain.RoleReader),
		"nil":         nil,
	}
	ops := map[string]*domain.Actor{
		"operator": actor("op", domain.RoleOperator),
		"admin":    actor("ad", domain.RoleAdmin),
	}
	refs := map[Class][]string{
		ClassInitBranch: {"refs/heads/spine/init"},
		ClassReserved:   {"refs/heads/spine", "refs/heads/spine/other", "refs/heads/Spine/run/task-001-x-0a1b2c3d"},
		ClassNonBranch:  {"refs/tags/v1.0.0", "refs/tags/main", "refs/notes/commits", "refs/spine/run/x", "refs/remotes/origin/main"},
	}
	for class, list := range refs {
		for _, ref := range list {
			for _, del := range []bool{false, true} {
				for name, a := range nonOps {
					rr := runFixture()
					d := decide(t, rr, defsFixture(), Request{Ref: ref, Actor: a, Delete: del})
					if d.Allow || d.Reason != ReasonOperatorRequired || d.Class != class {
						t.Errorf("%s %s delete=%v: %+v, want operator_required", name, ref, del, d)
					}
					if rr.calls() != 0 {
						t.Errorf("%s %s: %d reader calls, want 0", name, ref, rr.calls())
					}
					if !strings.HasPrefix(d.Message(), "ref-admission: ") {
						t.Errorf("%s %s: message %q", name, ref, d.Message())
					}
				}
				for name, a := range ops {
					if d := decide(t, nil, nil, Request{Ref: ref, Actor: a, Delete: del}); !d.Allow || d.Reason != ReasonOperator {
						t.Errorf("%s %s delete=%v: %+v, want operator allow", name, ref, del, d)
					}
				}
			}
		}
	}

	for name, a := range map[string]*domain.Actor{"nil": nil, "reader": actor("rd", domain.RoleReader), "operator": actor("op", domain.RoleOperator)} {
		if d := decide(t, nil, nil, Request{Ref: "refs/heads/feature/x", Actor: a}); !d.Allow || d.Reason != ReasonOrdinaryBranch {
			t.Errorf("%s ordinary branch: %+v, want ordinary_branch allow", name, d)
		}
	}

	t.Run("invalid refuses every principal", func(t *testing.T) {
		for _, ref := range []string{"refs/heads/spine\x00", "refs/heads/main\x7f", "refs/tags/v1\x01"} {
			for _, a := range []*domain.Actor{nil, actor("c", domain.RoleContributor), actor("op", domain.RoleOperator), actor("ad", domain.RoleAdmin)} {
				for _, del := range []bool{false, true} {
					rr := runFixture()
					d := decide(t, rr, defsFixture(), Request{Ref: ref, Actor: a, Delete: del})
					if d.Allow || d.Reason != ReasonInvalidRef || rr.calls() != 0 {
						t.Errorf("%q actor=%v delete=%v: %+v (%d reads), want invalid_ref with no reads", ref, a, del, d, rr.calls())
					}
					for _, b := range []byte(d.Message()) {
						if b < 0x20 || b == 0x7f {
							t.Errorf("%q: message carries control byte 0x%02x", ref, b)
						}
					}
				}
			}
		}
	})

	t.Run("shadowing refs refused for every principal on create and update", func(t *testing.T) {
		for _, ref := range []string{
			"refs/heads/refs/heads/main", "refs/tags/refs/heads/main", "refs/refs/heads/main",
			"refs/remotes/refs/heads/main", "refs/heads/HEAD", "refs/tags/HEAD",
			"refs/heads/spine/refs/x", "refs/heads/spine/run/x/HEAD",
		} {
			for _, a := range []*domain.Actor{nil, actor("c", domain.RoleContributor), actor("op", domain.RoleOperator), actor("ad", domain.RoleAdmin)} {
				d := decide(t, nil, nil, Request{Ref: ref, Actor: a})
				if d.Allow || d.Reason != ReasonShadowingRef {
					t.Errorf("%s actor=%v: %+v, want shadowing_ref", ref, a, d)
				}
				if !strings.Contains(d.Message(), ref) {
					t.Errorf("%s: message %q should name the ref", ref, d.Message())
				}
			}
		}
		// Deletes fall through to the class rules, so cleanup works.
		if d := decide(t, nil, nil, Request{Ref: "refs/heads/refs/heads/main", Actor: actor("c", domain.RoleContributor), Delete: true}); !d.Allow || d.Reason != ReasonOrdinaryBranch {
			t.Errorf("contributor deleting a shadowing branch: %+v, want ordinary_branch", d)
		}
		if d := decide(t, nil, nil, Request{Ref: "refs/tags/refs/heads/main", Actor: actor("op", domain.RoleOperator), Delete: true}); !d.Allow {
			t.Errorf("operator deleting a shadowing tag: %+v, want allow", d)
		}
		if d := decide(t, nil, nil, Request{Ref: "refs/tags/refs/heads/main", Actor: actor("c", domain.RoleContributor), Delete: true}); d.Allow || d.Reason != ReasonOperatorRequired {
			t.Errorf("contributor deleting a shadowing tag: %+v, want operator_required", d)
		}
	})
}

// TestDecision_Messages pins the wire catalogue (TASK-016 §1.3 "Wire
// messages") and that no message names who is bound.
func TestDecision_Messages(t *testing.T) {
	ref := Classify
	cases := []struct {
		d    Decision
		want string
	}{
		{Decision{Class: ClassRunBranch, Reason: ReasonNotBound, Ref: ref(fxRunRef)},
			"ref-admission: spine/run/task-001-x-0a1b2c3d is a run branch; only the actor assigned to the run's current step, or an operator, may push it"},
		{Decision{Class: ClassRunBranch, Reason: ReasonUnresolved, Ref: ref(fxRunRef)},
			"ref-admission: spine/run/task-001-x-0a1b2c3d is a run branch; only the actor assigned to the run's current step, or an operator, may push it"},
		{Decision{Class: ClassDivergenceBranch, Reason: ReasonNotBound, Ref: ref(fxDivRef)},
			"ref-admission: spine/run-0a1b2c3d/run-0a1b2c3d-div-d/b1 is a divergence branch; only the actor assigned to that branch's current step, or an operator, may push it"},
		{Decision{Class: ClassRunBranch, Reason: ReasonDeleteRequiresOperator, Ref: ref(fxRunRef)},
			"ref-admission: deleting spine/run/task-001-x-0a1b2c3d requires the operator role"},
		{Decision{Class: ClassInitBranch, Reason: ReasonOperatorRequired, Ref: ref("refs/heads/spine/init")},
			"ref-admission: spine/init is reserved for 'spine init-repo'; operator role required"},
		{Decision{Class: ClassReserved, Reason: ReasonOperatorRequired, Ref: ref("refs/heads/spine")},
			"ref-admission: spine is in the reserved spine namespace; operator role required"},
		{Decision{Class: ClassNonBranch, Reason: ReasonOperatorRequired, Ref: ref("refs/tags/main")},
			"ref-admission: refs/tags/main is not a branch; only operators may push tags or other non-branch refs"},
		{Decision{Class: ClassRunBranch, Reason: ReasonResolverUnavailable, Ref: ref(fxRunRef)},
			"ref-admission: run-branch admission is not configured on this server; operator role required for spine/run/task-001-x-0a1b2c3d"},
		{Decision{Class: ClassRunBranch, Reason: ReasonNotEvaluated, Ref: ref(fxRunRef)},
			"ref-admission: spine/run/task-001-x-0a1b2c3d not evaluated (push already refused)"},
		{Decision{Class: ClassInvalid, Reason: ReasonInvalidRef, Ref: ref("refs/heads/spine\x00")},
			"ref-admission: ref name contains a control byte; push refused"},
		{Decision{Class: ClassRunBranch, Reason: ReasonReviewFrozen, Ref: ref(fxRunRef)},
			"ref-admission: spine/run/task-001-x-0a1b2c3d is frozen while its current step is a review or engine-internal step; operator role required"},
		{Decision{Class: ClassRunBranch, Reason: ReasonStepUnresolved, Ref: ref(fxRunRef)},
			"ref-admission: cannot resolve the current step of spine/run/task-001-x-0a1b2c3d; operator role required"},
		{Decision{Allow: true, Class: ClassRunBranch, Reason: ReasonBoundActor, Ref: ref(fxRunRef)}, ""},
	}
	for _, tc := range cases {
		if got := tc.d.Message(); got != tc.want {
			t.Errorf("%s/%s: Message() = %q, want %q", tc.d.Class, tc.d.Reason, got, tc.want)
		}
	}
	if got, want := EvaluationErrorMessage(fxRunRef), "ref-admission: evaluation error on spine/run/task-001-x-0a1b2c3d"; got != want {
		t.Errorf("EvaluationErrorMessage = %q, want %q", got, want)
	}

	// Echoed names are bounded, and cut on a rune boundary.
	long := "refs/tags/" + strings.Repeat("é", 2000)
	msg := Decision{Class: ClassNonBranch, Reason: ReasonOperatorRequired, Ref: Classify(long)}.Message()
	if len(msg) > maxEcho+200 {
		t.Errorf("message length %d, want it bounded near %d", len(msg), maxEcho)
	}
	if !strings.Contains(msg, "...") || !strings.HasSuffix(strings.Split(msg, "...")[0], "é") {
		t.Errorf("truncated echo should end on a whole rune before '...': %q", msg[:80])
	}
}

// TestDecideBoundToRun covers the owner question 6 predicate behind
// divergence.create_branch.
func TestDecideBoundToRun(t *testing.T) {
	ctx := context.Background()
	call := func(rr RunReader, a *domain.Actor, runID string) Decision {
		t.Helper()
		d, err := NewEvaluator(rr).DecideBoundToRun(ctx, a, runID)
		if err != nil {
			t.Fatalf("DecideBoundToRun: %v", err)
		}
		return d
	}

	if d := call(runFixture(), actor("alice", domain.RoleContributor), fxRun); !d.Allow || d.ExecutionID != "e1" {
		t.Errorf("run-level holder: %+v, want allow on e1", d)
	}
	if d := call(divergenceFixture(), actor("dave", domain.RoleContributor), fxRun); !d.Allow || d.ExecutionID != "x1" {
		t.Errorf("divergence-branch holder: %+v, want allow on x1", d)
	}
	rr := runFixture()
	if d := call(rr, actor("op", domain.RoleOperator), fxRun); !d.Allow || d.Reason != ReasonOperator || rr.calls() != 0 {
		t.Errorf("operator: %+v (%d reads), want operator allow with no reads", d, rr.calls())
	}
	if d := call(nil, actor("op", domain.RoleAdmin), fxRun); !d.Allow {
		t.Errorf("admin with nil reader: %+v, want allow", d)
	}

	denies := []struct {
		name   string
		rr     *fakeReader
		a      *domain.Actor
		runID  string
		reason Reason
	}{
		{"stranger", runFixture(), actor("bob", domain.RoleContributor), fxRun, ReasonNotBound},
		{"holder of a non-current step", runFixture(), actor("carol", domain.RoleContributor), fxRun, ReasonNotBound},
		{"current step on a branch execution of no open branch", runFixture(), actor("dan", domain.RoleContributor), fxRun, ReasonNotBound},
		{"run-level holder while diverged", divergenceFixture(), actor("alice", domain.RoleContributor), fxRun, ReasonNotBound},
		{"reader role", runFixture(), actor("alice", domain.RoleReader), fxRun, ReasonNotBound},
		{"nil actor", runFixture(), nil, fxRun, ReasonNotBound},
		{"unknown run", runFixture(), actor("alice", domain.RoleContributor), "run-ffffffff", ReasonUnresolved},
	}
	for _, tc := range denies {
		if d := call(tc.rr, tc.a, tc.runID); d.Allow || d.Reason != tc.reason {
			t.Errorf("%s: %+v, want deny %s", tc.name, d, tc.reason)
		}
	}
	paused := runFixture()
	paused.runs[fxRun].Status = domain.RunStatusPaused
	if d := call(paused, actor("alice", domain.RoleContributor), fxRun); d.Allow {
		t.Errorf("paused run: %+v, want deny", d)
	}
	completedBranch := divergenceFixture()
	completedBranch.branches[fxRun+"-div-d-b1"].Status = domain.BranchStatusCompleted
	if d := call(completedBranch, actor("dave", domain.RoleContributor), fxRun); d.Allow {
		t.Errorf("holder on a completed branch: %+v, want deny", d)
	}
	newer := runFixture()
	newer.execs[fxRun] = append(newer.execs[fxRun], domain.StepExecution{ExecutionID: "e2", RunID: fxRun, StepID: "execute", ActorID: "bob", Status: domain.StepStatusAssigned})
	if d := call(newer, actor("alice", domain.RoleContributor), fxRun); d.Allow {
		t.Errorf("older live attempt: %+v, want deny (latest attempt wins)", d)
	}
	if d := call(nil, actor("alice", domain.RoleContributor), fxRun); d.Allow || d.Reason != ReasonResolverUnavailable {
		t.Errorf("nil reader: %+v, want resolver_unavailable", d)
	}
	boom := errors.New("down")
	errRR := runFixture()
	errRR.err = boom
	if _, err := NewEvaluator(errRR).DecideBoundToRun(ctx, actor("alice", domain.RoleContributor), fxRun); !errors.Is(err, boom) {
		t.Errorf("store error: %v, want it returned", err)
	}

	// The branch-cursor rule checks the branch record, not only the
	// execution (review round 1). Mutation check: dropping
	// `b.RunID == run.RunID` or `b.CurrentStepID == k.step` admits dave.
	for name, mutate := range map[string]func(b *domain.Branch){
		"branch record of another run":               func(b *domain.Branch) { b.RunID = "run-ffffffff" },
		"branch cursor moved past the holder's step": func(b *domain.Branch) { b.CurrentStepID = "judge" },
	} {
		rr := divergenceFixture()
		mutate(rr.branches[fxRun+"-div-d-b1"])
		if d := call(rr, actor("dave", domain.RoleContributor), fxRun); d.Allow {
			t.Errorf("%s: %+v, want deny", name, d)
		}
	}
}

func TestFrozenStepType(t *testing.T) {
	for st, want := range map[domain.StepType]bool{
		domain.StepTypeReview: true, domain.StepTypeInternal: true,
		domain.StepTypeManual: false, domain.StepTypeAutomated: false, domain.StepTypeConvergence: false, "": false,
	} {
		if got := FrozenStepType(st); got != want {
			t.Errorf("FrozenStepType(%q) = %v, want %v", st, got, want)
		}
	}
}
