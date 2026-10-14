package engine

// INIT-023 TASK-005, submit side: the head assertion (AC 4), reserved keys
// (AC 5, engine part), the divergence-start case of AC 7, the moved-head
// policy table (AC 16), head-resolution failures (AC 17), recovery
// re-submits and assertion ordering (AC 27) and the convergence rebind and
// its guard (AC 16 last bullet, AC 28).

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

// stepSnapshot is what a refused submit must leave unchanged.
func stepSnapshot(e *domain.StepExecution) string {
	return fmt.Sprintf("%s|%s|%s", e.Status, e.ActorID, e.OutcomeID)
}

func TestHeadAssertion(t *testing.T) {
	head := sha40('a')
	type setup func(f *bindFix) *domain.StepExecution
	review := func(f *bindFix) *domain.StepExecution { return f.exec("review", domain.StepStatusAssigned) }
	cases := []struct {
		name      string
		bindingOn bool
		nonBinder bool
		expected  string
		setup     setup
		code      domain.ErrorCode
		reason    string
	}{
		{name: "mismatch, binding on", bindingOn: true, expected: sha40('b'), setup: review,
			code: domain.ErrConflict, reason: ReasonRunBranchMoved},
		{name: "mismatch, binding off", expected: sha40('b'), setup: review,
			code: domain.ErrConflict, reason: ReasonRunBranchMoved},
		{name: "7-character prefix", bindingOn: true, expected: head[:7], setup: review,
			code: domain.ErrInvalidParams, reason: ReasonExpectedHeadInvalid},
		{name: "39 hex", bindingOn: true, expected: head[:39], setup: review,
			code: domain.ErrInvalidParams, reason: ReasonExpectedHeadInvalid},
		{name: "uppercase hex", bindingOn: true, expected: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", setup: review,
			code: domain.ErrInvalidParams, reason: ReasonExpectedHeadInvalid},
		{name: "non-hex", bindingOn: true, expected: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", setup: review,
			code: domain.ErrInvalidParams, reason: ReasonExpectedHeadInvalid},
		{name: "option", bindingOn: true, expected: "--upload-pack=x", setup: review,
			code: domain.ErrInvalidParams, reason: ReasonExpectedHeadInvalid},
		{name: "run with no branch", bindingOn: true, expected: head,
			setup: func(f *bindFix) *domain.StepExecution {
				f.run.BranchName = ""
				return f.exec("review", domain.StepStatusAssigned)
			}, code: domain.ErrInvalidParams, reason: ReasonExpectedHeadNotApplicable},
		{name: "divergence-branch step", bindingOn: true, expected: head,
			setup: func(f *bindFix) *domain.StepExecution {
				e := f.exec("review", domain.StepStatusAssigned)
				e.BranchID = "branch-a"
				return e
			}, code: domain.ErrInvalidParams, reason: ReasonExpectedHeadNotApplicable},
		{name: "internal step", bindingOn: true, expected: head,
			setup: func(f *bindFix) *domain.StepExecution { return f.exec("publish", domain.StepStatusInProgress) },
			code:  domain.ErrInvalidParams, reason: ReasonExpectedHeadNotApplicable},
		{name: "operator without the capability, binding off", nonBinder: true, expected: head, setup: review,
			code: domain.ErrUnavailable, reason: ReasonExpectedHeadUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBindFix(t, testBindingWorkflow(), tc.bindingOn, "")
			if tc.nonBinder {
				f.orch.git = &plainGit{}
			}
			f.git.setHead(f.run.BranchName, head)
			e := tc.setup(f)
			before := stepSnapshot(e)
			outcome := "accepted"
			if e.StepID == "publish" {
				outcome = "published"
			}
			err := f.submit(e, outcome, tc.expected)
			requireBindingErr(t, err, tc.code, tc.reason)
			if got := stepSnapshot(f.latestExec(e.StepID)); got != before {
				t.Fatalf("step changed on a refused submit: %s -> %s", before, got)
			}
			if len(f.store.metaWrites) != 0 {
				t.Fatalf("refused submit wrote commit_meta: %v", f.store.metaWrites)
			}
		})
	}

	t.Run("matching assertion is accepted with binding off and records nothing", func(t *testing.T) {
		f := newBindFix(t, testBindingWorkflow(), false, "")
		f.git.setHead(f.run.BranchName, head)
		e := f.exec("review", domain.StepStatusAssigned)
		if err := f.submit(e, "rework", head); err != nil {
			t.Fatalf("submit: %v", err)
		}
		if len(f.store.metaWrites) != 0 {
			t.Fatalf("binding off recorded a binding: %v", f.store.metaWrites)
		}
	})
}

// TestReservedCommitKeys_Engine is AC 5's engine part: a workflow outcome
// cannot plant binding keys, with binding on or off.
func TestReservedCommitKeys_Engine(t *testing.T) {
	planted := func() *domain.WorkflowDefinition {
		wf := testBindingWorkflow()
		sd := findStepDef(wf, "review")
		for i := range sd.Outcomes {
			if sd.Outcomes[i].ID == "accepted" {
				sd.Outcomes[i].Commit = map[string]string{
					"status": "Completed", "spine.bound_sha": sha40('e'), "Spine.Bound_By": "x",
				}
			}
		}
		return wf
	}
	t.Run("binding on", func(t *testing.T) {
		f := newBindFix(t, planted(), true, "")
		f.git.setHead(f.run.BranchName, sha40('a'))
		e := f.exec("review", domain.StepStatusAssigned)
		_ = f.submit(e, "accepted", "") // the publish merge that follows is not under test
		if len(f.store.metaWrites) == 0 {
			t.Fatal("no commit_meta written")
		}
		meta := f.store.metaWrites[0]
		if meta["status"] != "Completed" || meta[CommitMetaBoundSHA] != sha40('a') ||
			meta[CommitMetaBoundBy] != boundByStep("review", e.ExecutionID) {
			t.Fatalf("commit_meta = %v", meta)
		}
		for k, v := range meta {
			if v == sha40('e') || v == "x" || k == "Spine.Bound_By" {
				t.Fatalf("attacker value stored: %s=%s in %v", k, v, meta)
			}
		}
	})
	t.Run("binding off", func(t *testing.T) {
		f := newBindFix(t, planted(), false, "")
		e := f.exec("review", domain.StepStatusAssigned)
		_ = f.submit(e, "accepted", "")
		if len(f.store.metaWrites) == 0 || fmt.Sprint(f.store.metaWrites[0]) != fmt.Sprint(map[string]string{"status": "Completed"}) {
			t.Fatalf("SetCommitMeta = %v, want exactly {status: Completed}", f.store.metaWrites)
		}
		if f.git.callCount("binder:") != 0 {
			t.Fatalf("binding off called CommitBinder methods: %v", f.git.calls)
		}
	})
}

// TestDivergenceStartCommitMeta is AC 7's divergence note: a step that
// starts a divergence never stores its outcome's commit map; binding off
// makes no SetCommitMeta call at all, binding on writes only the binding.
func TestDivergenceStartCommitMeta(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "wf-div", Path: "workflows/div.yaml", EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Type: domain.StepTypeManual, Diverge: "div-1",
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end", Commit: map[string]string{"status": "X"}}}},
		},
		DivergencePoints: []domain.DivergenceDefinition{{ID: "div-1", Mode: domain.DivergenceModeStructured}},
	}
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("binding=%v", on), func(t *testing.T) {
			f := newBindFix(t, wf, on, "")
			f.git.setHead(f.run.BranchName, sha40('a'))
			f.orch.WithDivergence(&mockDivergenceHandler{divCtx: &domain.DivergenceContext{DivergenceID: "div-1", RunID: bindRunID}})
			e := f.exec("start", domain.StepStatusAssigned)
			if err := f.submit(e, "done", ""); err != nil {
				t.Fatalf("submit: %v", err)
			}
			if !on {
				if len(f.store.metaWrites) != 0 {
					t.Fatalf("binding off: SetCommitMeta called %v", f.store.metaWrites)
				}
				return
			}
			if len(f.store.metaWrites) != 1 {
				t.Fatalf("binding on: %d writes", len(f.store.metaWrites))
			}
			if _, ok := f.store.metaWrites[0]["status"]; ok {
				t.Fatalf("divergence start stored the outcome's commit map: %v", f.store.metaWrites[0])
			}
			if f.store.metaWrites[0][CommitMetaBoundSHA] != sha40('a') {
				t.Fatalf("binding not recorded: %v", f.store.metaWrites[0])
			}
		})
	}
}

// TestMovedHeadPolicyTable is AC 16: step type × outcome kind × head
// state, under both policies.
func TestMovedHeadPolicyTable(t *testing.T) {
	type outcomeKind struct{ name, outcome string }
	steps := map[string][]outcomeKind{
		"review":   {{"advancing", "accepted"}, {"rework", "rework"}, {"end without commit", "reject"}},
		"converge": {{"advancing", "ok"}, {"rework", "rework"}, {"end without commit", "reject"}},
		"execute":  {{"advancing", "done"}, {"rework", "again"}, {"end without commit", "stop"}},
		"validate": {{"advancing", "passed"}, {"rework", "failed"}, {"end without commit", "stop"}},
		"publish":  {{"advancing", "published"}, {"rework", "merge_failed"}},
	}
	const prev, head = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	heads := map[string]struct {
		current string
		bound   bool
	}{
		"moved":       {head, true},
		"not moved":   {prev, true},
		"no previous": {head, false},
	}
	for _, policy := range []MovedHeadPolicy{MovedHeadReject, MovedHeadRebind} {
		for stepID, kinds := range steps {
			for _, k := range kinds {
				for hs, h := range heads {
					name := fmt.Sprintf("%s/%s/%s/%s", policy, stepID, k.name, hs)
					t.Run(name, func(t *testing.T) {
						f := newBindFix(t, testBindingWorkflow(), true, policy)
						f.git.setHead(f.run.BranchName, h.current)
						if h.bound {
							f.bindAt(prev, "execute")
						}
						sd := findStepDef(f.wf, stepID)
						e := &domain.StepExecution{ExecutionID: bindRunID + "-" + stepID + "-2", RunID: bindRunID,
							StepID: stepID, Status: domain.StepStatusInProgress, ActorID: "worker"}
						hb, err := f.orch.captureHead(context.Background(), f.run, e, sd, findOutcome(sd, k.outcome), f.wf, "")
						wantConflict := policy == MovedHeadReject && isGateStep(sd) && k.name == "advancing" && hs == "moved"
						if wantConflict {
							requireBindingErr(t, err, domain.ErrConflict, ReasonRunBranchMovedSinceGate)
							return
						}
						if err != nil {
							t.Fatalf("captureHead: %v", err)
						}
						if stepID == "publish" {
							if hb != nil {
								t.Fatalf("internal step captured: %+v", hb)
							}
							return
						}
						if hb == nil || hb.head != h.current || hb.by != boundByStep(stepID, e.ExecutionID) {
							t.Fatalf("binding = %+v, want the current head %s", hb, h.current)
						}
					})
				}
			}
		}
	}

	// Engine level: a refused approval leaves the review untouched; under
	// rebind it advances and the binding is the current head.
	for _, policy := range []MovedHeadPolicy{MovedHeadReject, MovedHeadRebind} {
		t.Run("engine/"+string(policy), func(t *testing.T) {
			f := newBindFix(t, testBindingWorkflow(), true, policy)
			f.bindAt(prev, "execute")
			f.git.setHead(f.run.BranchName, head)
			e := f.exec("review", domain.StepStatusAssigned)
			err := f.submit(e, "rework", "") // rework never trips
			if err != nil {
				t.Fatalf("rework: %v", err)
			}
			e2 := f.exec("review", domain.StepStatusAssigned)
			f.bindAt(prev, "execute")
			err = f.submit(e2, "accepted", "")
			if policy == MovedHeadReject {
				requireBindingErr(t, err, domain.ErrConflict, ReasonRunBranchMovedSinceGate)
				if got := f.latestExec("review"); got.Status != domain.StepStatusAssigned || got.OutcomeID != "" {
					t.Fatalf("refused approval changed the review: %+v", got)
				}
				if f.run.Status != domain.RunStatusActive {
					t.Fatalf("run status %s", f.run.Status)
				}
				return
			}
			// rebind: the approval went through to the publish merge; the
			// binding recorded at the approval is the current head.
			found := false
			for _, m := range f.store.metaWrites {
				if m[CommitMetaBoundBy] == boundByStep("review", e2.ExecutionID) && m[CommitMetaBoundSHA] == head {
					found = true
				}
			}
			if !found {
				t.Fatalf("rebind: no binding to %s recorded at the approval: %v", head, f.store.metaWrites)
			}
		})
	}
}

// TestUntrustedPreviousBindingCountsAsMoved: a binding this run did not
// write (DC-9) never lets a gate approve.
func TestUntrustedPreviousBindingCountsAsMoved(t *testing.T) {
	f := newBindFix(t, testBindingWorkflow(), true, "")
	f.git.setHead(f.run.BranchName, sha40('a'))
	f.run.CommitMeta = map[string]string{CommitMetaBoundSHA: sha40('a'), CommitMetaBoundBy: "step:review:run-deadbeef-review-1"}
	e := f.exec("review", domain.StepStatusAssigned)
	requireBindingErr(t, f.submit(e, "accepted", ""), domain.ErrConflict, ReasonRunBranchMovedSinceGate)
}

// TestHeadResolutionFailure is AC 17.
func TestHeadResolutionFailure(t *testing.T) {
	cases := []struct {
		step, outcome string
		wantErr       bool
	}{
		{"execute", "done", true},    // producer
		{"validate", "failed", true}, // producer, even reworking
		{"review", "accepted", true}, // gate, advancing
		{"review", "rework", false},  // gate, rework: a reviewer can always send work back
	}
	for _, tc := range cases {
		t.Run(tc.step+"/"+tc.outcome, func(t *testing.T) {
			f := newBindFix(t, testBindingWorkflow(), true, "")
			f.bindAt(sha40('1'), "execute")
			reservedBefore := fmt.Sprint(reservedOnly(f.run.CommitMeta))
			f.git.headErr = errors.New("transport down")
			e := f.exec(tc.step, domain.StepStatusAssigned)
			before := stepSnapshot(e)
			err := f.submit(e, tc.outcome, "")
			if tc.wantErr {
				requireBindingErr(t, err, domain.ErrUnavailable, ReasonRunBranchHeadUnavailable)
				if got := stepSnapshot(f.latestExec(tc.step)); got != before {
					t.Fatalf("step changed: %s -> %s", before, got)
				}
				var se *domain.SpineError
				if errors.As(err, &se) && se.Message == "" {
					t.Fatal("empty message")
				}
				return
			}
			if err != nil {
				t.Fatalf("rework submit: %v", err)
			}
			if got := fmt.Sprint(reservedOnly(f.run.CommitMeta)); got != reservedBefore {
				t.Fatalf("reserved keys changed: %s -> %s", reservedBefore, got)
			}
		})
	}
}

// TestRecoveryResubmitAndAssertionOrdering is AC 27.
func TestRecoveryResubmitAndAssertionOrdering(t *testing.T) {
	t.Run("recovery re-submit of a completed gate", func(t *testing.T) {
		f := newBindFix(t, legacyWorkflow(), true, "")
		f.bindAt(sha40('1'), "review")
		f.run.CommitMeta["spine.head.review"] = sha40('1')
		reservedBefore := fmt.Sprint(reservedOnly(f.run.CommitMeta))
		f.git.setHead(f.run.BranchName, sha40('2')) // moved since: must not matter
		e := f.exec("review", domain.StepStatusCompleted)
		e.OutcomeID = "needs_rework"
		if err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID, StepResult{OutcomeID: "needs_rework"}); err != nil {
			t.Fatalf("recovery re-submit: %v", err)
		}
		if n := f.git.callCount("binder:ResolveBranchHead"); n != 0 {
			t.Fatalf("recovery re-submit resolved the head %d times", n)
		}
		if got := fmt.Sprint(reservedOnly(f.run.CommitMeta)); got != reservedBefore {
			t.Fatalf("reserved keys changed: %s -> %s", reservedBefore, got)
		}
	})
	t.Run("malformed assertion before required outputs", func(t *testing.T) {
		wf := testBindingWorkflow()
		findStepDef(wf, "review").RequiredOutputs = []string{"report.md"}
		f := newBindFix(t, wf, true, "")
		e := f.exec("review", domain.StepStatusAssigned)
		_, err := f.orch.IngestResult(context.Background(), SubmitRequest{
			ExecutionID: e.ExecutionID, OutcomeID: "accepted", ExpectedHeadSHA: "abc1234",
		})
		requireBindingErr(t, err, domain.ErrInvalidParams, ReasonExpectedHeadInvalid)
		if got := f.latestExec("review"); got.Status == domain.StepStatusFailed || got.Status != domain.StepStatusAssigned {
			t.Fatalf("review status = %s; a malformed assertion must not fail the step", got.Status)
		}
		// Control: without the assertion the missing output fails the step.
		f2 := newBindFix(t, wf, true, "")
		e2 := f2.exec("review", domain.StepStatusAssigned)
		if _, err := f2.orch.IngestResult(context.Background(), SubmitRequest{ExecutionID: e2.ExecutionID, OutcomeID: "accepted"}); err == nil {
			t.Fatal("control: missing output accepted")
		}
	})
	// Codex review, round 2: a head refusal wins over the missing-output
	// failure, which would otherwise fail the step for a submit the head
	// check refuses.
	t.Run("head refusals before required outputs", func(t *testing.T) {
		wf := testBindingWorkflow()
		findStepDef(wf, "review").RequiredOutputs = []string{"report.md"}
		for _, tc := range []struct {
			name      string
			bindingOn bool
			setup     func(f *bindFix)
			outcome   string
			expected  string
			code      domain.ErrorCode
			reason    string
		}{
			{"stale assertion, binding on", true, func(f *bindFix) { f.git.setHead(f.run.BranchName, sha40('a')) },
				"rework", sha40('b'), domain.ErrConflict, ReasonRunBranchMoved},
			{"stale assertion, binding off", false, func(f *bindFix) { f.git.setHead(f.run.BranchName, sha40('a')) },
				"accepted", sha40('b'), domain.ErrConflict, ReasonRunBranchMoved},
			{"approval of a moved branch", true, func(f *bindFix) {
				f.bindAt(sha40('1'), "execute")
				f.git.setHead(f.run.BranchName, sha40('2'))
			}, "accepted", "", domain.ErrConflict, ReasonRunBranchMovedSinceGate},
			{"head unresolvable", true, func(f *bindFix) { f.git.headErr = errors.New("transport down") },
				"accepted", "", domain.ErrUnavailable, ReasonRunBranchHeadUnavailable},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newBindFix(t, wf, tc.bindingOn, "")
				tc.setup(f)
				e := f.exec("review", domain.StepStatusAssigned)
				before := stepSnapshot(e)
				_, err := f.orch.IngestResult(context.Background(), SubmitRequest{
					ExecutionID: e.ExecutionID, OutcomeID: tc.outcome, ExpectedHeadSHA: tc.expected,
				})
				requireBindingErr(t, err, tc.code, tc.reason)
				if got := stepSnapshot(f.latestExec("review")); got != before {
					t.Fatalf("step changed %s -> %s; a head refusal must not fail the step", before, got)
				}
			})
		}
		// Controls: a matching assertion, or binding off without one (SMP
		// today: no head read at all), still fails the step for the
		// missing output.
		for _, on := range []bool{true, false} {
			f := newBindFix(t, wf, on, "")
			f.git.setHead(f.run.BranchName, sha40('a'))
			expected := ""
			if on {
				f.bindAt(sha40('a'), "execute")
				expected = sha40('a')
			}
			e := f.exec("review", domain.StepStatusAssigned)
			_, err := f.orch.IngestResult(context.Background(), SubmitRequest{ExecutionID: e.ExecutionID, OutcomeID: "accepted", ExpectedHeadSHA: expected})
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != domain.ErrInvalidParams {
				t.Fatalf("binding=%v: err = %v, want the missing-output ErrInvalidParams", on, err)
			}
			if got := f.latestExec("review"); got.Status != domain.StepStatusFailed {
				t.Fatalf("binding=%v: review status = %s, want failed for the missing output", on, got.Status)
			}
			if !on && f.git.callCount("binder:ResolveBranchHead") != 0 {
				t.Fatal("binding off without an assertion resolved the head")
			}
		}
	})
	t.Run("assertion reaches SubmitStepResult through IngestResult", func(t *testing.T) {
		f := newBindFix(t, testBindingWorkflow(), true, "")
		f.git.setHead(f.run.BranchName, sha40('a'))
		e := f.exec("review", domain.StepStatusAssigned)
		_, err := f.orch.IngestResult(context.Background(), SubmitRequest{
			ExecutionID: e.ExecutionID, OutcomeID: "rework", ExpectedHeadSHA: sha40('b'),
		})
		requireBindingErr(t, err, domain.ErrConflict, ReasonRunBranchMoved)
	})
}

// headMovingConvergence moves the run branch during EvaluateAndCommit,
// as a real convergence merge does.
type headMovingConvergence struct {
	g      *fakeBinderGit
	branch string
	to     string
}

func (c *headMovingConvergence) CheckEntryPolicy(context.Context, *domain.DivergenceContext, domain.ConvergenceDefinition) (bool, error) {
	return true, nil
}

func (c *headMovingConvergence) EvaluateAndCommit(context.Context, *domain.Run, *domain.DivergenceContext, domain.ConvergenceDefinition) error {
	c.g.setHead(c.branch, c.to)
	return nil
}

func convergenceFix(t *testing.T) (*bindFix, *domain.Branch) {
	t.Helper()
	wf := testBindingWorkflow()
	wf.ConvergencePoints = []domain.ConvergenceDefinition{{ID: "conv-1", EvaluationStep: "converge"}}
	f := newBindFix(t, wf, true, "")
	branch := &domain.Branch{BranchID: "branch-a", RunID: bindRunID, DivergenceID: "div-1",
		Status: domain.BranchStatusInProgress, CurrentStepID: "execute"}
	f.store.branches = []*domain.Branch{branch}
	f.store.divergenceContexts = map[string]*domain.DivergenceContext{
		"div-1": {DivergenceID: "div-1", RunID: bindRunID, ConvergenceID: "conv-1", Status: domain.DivergenceStatusActive},
	}
	return f, branch
}

// TestConvergenceRebind is AC 16's convergence bullet and AC 28.
func TestConvergenceRebind(t *testing.T) {
	const bound, converged, sneak = "1111111111111111111111111111111111111111", "3333333333333333333333333333333333333333", "2222222222222222222222222222222222222222"
	t.Run("rebinds to the converged head from the bound head", func(t *testing.T) {
		f, _ := convergenceFix(t)
		f.bindAt(bound, "execute")
		f.git.setHead(f.run.BranchName, bound)
		f.orch.WithConvergence(&headMovingConvergence{g: f.git, branch: f.run.BranchName, to: converged})
		be := f.exec("execute", domain.StepStatusAssigned)
		be.BranchID = "branch-a"
		if err := f.submit(be, "stop", ""); err != nil { // branch-terminal outcome triggers convergence
			t.Fatalf("branch submit: %v", err)
		}
		if f.run.CommitMeta[CommitMetaBoundSHA] != converged ||
			f.run.CommitMeta[CommitMetaBoundBy] != boundByConvergence(bindRunID, "div-1") {
			t.Fatalf("binding after convergence = %v", f.run.CommitMeta)
		}
		gate := f.latestExec("converge")
		if gate == nil {
			t.Fatal("evaluation step not created")
		}
		gate.Status, gate.ActorID = domain.StepStatusAssigned, "worker"
		if err := f.submit(gate, "ok", ""); err != nil {
			t.Fatalf("evaluation gate advancing submit after convergence: %v", err)
		}
	})
	t.Run("a push to the main run branch during the divergence blocks the rebind", func(t *testing.T) {
		f, _ := convergenceFix(t)
		f.bindAt(bound, "execute")
		f.git.setHead(f.run.BranchName, sneak) // pushed while the divergence was open
		f.orch.WithConvergence(&headMovingConvergence{g: f.git, branch: f.run.BranchName, to: converged})
		be := f.exec("execute", domain.StepStatusAssigned)
		be.BranchID = "branch-a"
		if err := f.submit(be, "stop", ""); err != nil {
			t.Fatalf("branch submit: %v", err)
		}
		if f.run.CommitMeta[CommitMetaBoundSHA] != bound || f.run.CommitMeta[CommitMetaBoundBy] != boundByStep("execute", bindRunID+"-execute-1") {
			t.Fatalf("binding changed: %v", f.run.CommitMeta)
		}
		gate := f.latestExec("converge")
		gate.Status, gate.ActorID = domain.StepStatusAssigned, "worker"
		requireBindingErr(t, f.submit(gate, "ok", ""), domain.ErrConflict, ReasonRunBranchMovedSinceGate)
	})
	t.Run("a convergence result that does not descend from the bound head is not bound", func(t *testing.T) {
		f, _ := convergenceFix(t)
		f.bindAt(bound, "execute")
		f.git.setHead(f.run.BranchName, bound)
		f.git.notAncestor[bound+">"+converged] = true
		f.orch.WithConvergence(&headMovingConvergence{g: f.git, branch: f.run.BranchName, to: converged})
		be := f.exec("execute", domain.StepStatusAssigned)
		be.BranchID = "branch-a"
		if err := f.submit(be, "stop", ""); err != nil {
			t.Fatalf("branch submit: %v", err)
		}
		if f.run.CommitMeta[CommitMetaBoundSHA] != bound {
			t.Fatalf("binding changed: %v", f.run.CommitMeta)
		}
	})
}
