package engine

// INIT-023 TASK-008, audit P3-24: compare-and-swap run and step writes,
// and terminal re-entry (§3a). Committed forms of the design-time PoCs
// TestTASK008_PoC_CompleteRunResurrectsCancelledRun,
// TestTASK008_PoC_SubmitOverwritesTimedOutStep and the design-check PoCs
// TestTASK008_DC_SubmitStepResultReroutesCompletedExecution and
// TestTASK008_DC_IngestRaceReroutesCompletedExecution. Every test names
// the mutation that turns it red.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
)

// AC 6 (PoC CompleteRunResurrectsCancelledRun). A CancelRun commits inside
// CompleteRun's read. HEAD: final status=completed, governed merges=1,
// status writes=[cancelled committing]. Mutation: restoring
// UpdateRunStatus in CompleteRun fails it.
func TestCompleteRun_DoesNotResurrectCancelledRun(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	f := newCasFix(t, casWorkflow(), false)
	ctx := context.Background()
	f.st.afterGetRun = func(runID string, n int) {
		if n == 1 { // CompleteRun's own read has its snapshot
			if err := f.orch.CancelRun(ctx, runID); err != nil {
				t.Errorf("concurrent CancelRun: %v", err)
			}
		}
	}
	err := f.orch.CompleteRun(ctx, f.runID, true)
	requireCode(t, err, domain.ErrConflict, "CompleteRun after a concurrent cancel")

	if got := f.st.run(f.runID).Status; got != domain.RunStatusCancelled {
		t.Fatalf("cancelled run was resurrected: final status=%s, status writes=%v", got, f.st.runWrites)
	}
	if n := f.governedMerges(); n != 0 {
		t.Fatalf("cancelled run was merged %d times", n)
	}
	if evs := f.events(domain.EventRunCompleted); len(evs) != 0 {
		t.Fatalf("run_completed emitted for a cancelled run")
	}
	if f.st.blindRun != 0 {
		t.Fatalf("%d blind UpdateRunStatus writes", f.st.blindRun)
	}
}

// mergeFailGit fails every non-abort merge permanently.
type mergeFailGit struct{ *fakeBinderGit }

func (g mergeFailGit) Merge(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	g.record("Merge " + opts.Source)
	if opts.Strategy == "abort" {
		return git.MergeResult{}, nil
	}
	g.plainMerges = append(g.plainMerges, opts)
	return git.MergeResult{}, &git.GitError{Kind: git.ErrKindPermanent, Op: "merge", Message: "conflict"}
}

// AC 7. A hook moves the run to cancelled between each mutator's read and
// its write. No mutator overwrites it, emits its event or cleans up the
// branch. Mutation: restoring UpdateRunStatus in any mutator fails its row.
func TestRunMutators_LostRaceNeverOverwrites(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	type row struct {
		name   string
		start  domain.RunStatus
		call   func(o *Orchestrator, f *casFix) error
		event  domain.EventType
		errNil bool // failRunOnMergeError logs a lost race and returns nil
	}
	rows := []row{
		{"CompleteRun with commit", domain.RunStatusActive, func(o *Orchestrator, f *casFix) error {
			return o.CompleteRun(context.Background(), f.runID, true)
		}, domain.EventRunCompleted, false},
		{"CompleteRun without commit", domain.RunStatusActive, func(o *Orchestrator, f *casFix) error {
			return o.CompleteRun(context.Background(), f.runID, false)
		}, domain.EventRunCompleted, false},
		{"FailRun", domain.RunStatusActive, func(o *Orchestrator, f *casFix) error {
			return o.FailRun(context.Background(), f.runID, "boom")
		}, domain.EventRunFailed, false},
		{"CancelRun", domain.RunStatusActive, func(o *Orchestrator, f *casFix) error {
			return o.CancelRun(context.Background(), f.runID)
		}, domain.EventRunCancelled, false},
		{"PauseRun", domain.RunStatusActive, func(o *Orchestrator, f *casFix) error {
			return o.PauseRun(context.Background(), f.runID, "blocked")
		}, domain.EventRunPaused, false},
		{"ResumeRun", domain.RunStatusPaused, func(o *Orchestrator, f *casFix) error {
			return o.ResumeRun(context.Background(), f.runID)
		}, domain.EventRunResumed, false},
		{"failRunOnMergeError (permanent merge failure)", domain.RunStatusCommitting, func(o *Orchestrator, f *casFix) error {
			return o.MergeRunBranch(context.Background(), f.runID)
		}, domain.EventRunFailed, true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			wf := casWorkflow()
			f := newCasFix(t, wf, false)
			f.orch.git = mergeFailGit{f.git}
			f.st.runs[f.runID].Status = r.start
			// A completed step whose git_conflict detail a lost merge
			// failure must not write.
			review := f.addExec("review", domain.StepStatusCompleted, "actor-r")
			f.st.afterGetRun = func(runID string, n int) {
				if n == 1 {
					f.st.setRunStatus(runID, domain.RunStatusCancelled)
				}
			}
			err := r.call(f.orch, f)
			if r.errNil {
				if err != nil {
					t.Fatalf("lost merge-failure race: err = %v, want nil", err)
				}
			} else {
				requireCode(t, err, domain.ErrConflict, r.name)
			}
			if got := f.st.run(f.runID).Status; got != domain.RunStatusCancelled {
				t.Fatalf("lost race overwrote the winner: status=%s, writes=%v", got, f.st.runWrites)
			}
			if len(f.st.runWrites) != 0 {
				t.Fatalf("lost race wrote run status %v", f.st.runWrites)
			}
			if evs := f.events(r.event); len(evs) != 0 {
				t.Fatalf("lost race emitted %s", r.event)
			}
			if n := f.git.callCount("DeleteBranch"); n != 0 {
				t.Fatalf("lost race cleaned up the run branch (%d DeleteBranch calls)", n)
			}
			if got := f.st.exec(review.ExecutionID); got.ErrorDetail != nil {
				t.Fatalf("lost merge-failure race wrote git_conflict detail: %+v", got.ErrorDetail)
			}
		})
	}
}

// AC 7. Re-evaluation: a cancel that raced a pause still cancels (one
// run_cancelled); a cancel that raced a merge start answers 409 and the run
// stays committing (owner question 2).
func TestCancelRun_ReevaluatesAfterLostRace(t *testing.T) {
	t.Run("raced a pause", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), false)
		f.st.afterGetRun = func(runID string, n int) {
			if n == 1 {
				f.st.setRunStatus(runID, domain.RunStatusPaused)
			}
		}
		if err := f.orch.CancelRun(context.Background(), f.runID); err != nil {
			t.Fatalf("CancelRun: %v", err)
		}
		if got := f.st.run(f.runID).Status; got != domain.RunStatusCancelled {
			t.Fatalf("status = %s, want cancelled", got)
		}
		if n := len(f.events(domain.EventRunCancelled)); n != 1 {
			t.Fatalf("%d run_cancelled events, want 1", n)
		}
	})
	t.Run("raced a merge start", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), false)
		f.st.afterGetRun = func(runID string, n int) {
			if n == 1 {
				f.st.setRunStatus(runID, domain.RunStatusCommitting)
			}
		}
		err := f.orch.CancelRun(context.Background(), f.runID)
		requireCode(t, err, domain.ErrConflict, "cancel that lost to a merge start")
		if got := f.st.run(f.runID).Status; got != domain.RunStatusCommitting {
			t.Fatalf("status = %s, want committing", got)
		}
		if n := len(f.events(domain.EventRunCancelled)); n != 0 {
			t.Fatalf("run_cancelled emitted for a committing run")
		}
	})
}

// D3: re-evaluation is bounded. A writer that moves the run after every
// read makes the cancel give up after runCASAttempts with ErrConflict
// wrapping ErrRunStateChanged, having written nothing.
func TestTransitionRun_GivesUpAfterBoundedAttempts(t *testing.T) {
	f := newCasFix(t, casWorkflow(), false)
	f.st.afterGetRun = func(runID string, n int) {
		if n%2 == 1 {
			f.st.setRunStatus(runID, domain.RunStatusPaused)
		} else {
			f.st.setRunStatus(runID, domain.RunStatusActive)
		}
	}
	err := f.orch.CancelRun(context.Background(), f.runID)
	requireCode(t, err, domain.ErrConflict, "livelocked cancel")
	if !errors.Is(err, ErrRunStateChanged) {
		t.Fatalf("err = %v, want it to wrap ErrRunStateChanged", err)
	}
	if f.st.transitionRuns != runCASAttempts {
		t.Fatalf("%d TransitionRunStatus attempts, want %d", f.st.transitionRuns, runCASAttempts)
	}
	if len(f.st.runWrites) != 0 || len(f.events(domain.EventRunCancelled)) != 0 {
		t.Fatalf("a run that never settled was written (%v) or announced", f.st.runWrites)
	}
}

// AC 24. SMP's TransitionRunStatus answers a missing row with an error,
// not (false, nil): transitionRun returns it wrapped after one attempt.
func TestTransitionRun_StoreErrorIsNotRetried(t *testing.T) {
	errRunNotFound := errors.New("spinecore: run not found")
	f := newCasFix(t, casWorkflow(), false)
	f.st.transitionRunErr = errRunNotFound
	err := f.orch.CancelRun(context.Background(), f.runID)
	if !errors.Is(err, errRunNotFound) {
		t.Fatalf("err = %v, want it to wrap the store's error", err)
	}
	if f.st.transitionRuns != 1 {
		t.Fatalf("%d attempts, want 1 (a store error is not a lost race)", f.st.transitionRuns)
	}
}

// startRunCommon's pending→active is conditional too: a run that left
// pending (recovery activated it) is not written over and no run_started
// is emitted.
func TestStartRunCommon_ActivationIsConditional(t *testing.T) {
	f := newCasFix(t, casWorkflow(), false)
	run := &domain.Run{RunID: "run-2", TaskPath: "tasks/t.md", WorkflowPath: f.wf.Path, WorkflowID: f.wf.ID,
		WorkflowVersion: "v1", Status: domain.RunStatusPending, TraceID: casTrace, CreatedAt: time.Now()}
	f.st.afterList = nil
	// CreateRun stores the run; flip it to active "concurrently" right
	// after the entry step is created (before the activation write).
	orig := f.orch.store
	f.orch.store = &activateRaceStore{RunStore: orig, st: f.st, runID: "run-2"}
	_, err := f.orch.startRunCommon(context.Background(), startRunParams{run: run, wfDef: f.wf})
	requireCode(t, err, domain.ErrConflict, "activation that lost a race")
	if n := len(f.events(domain.EventRunStarted)); n != 0 {
		t.Fatalf("run_started emitted for an activation that did not apply")
	}
	if got := f.st.run("run-2").Status; got != domain.RunStatusCancelled {
		t.Fatalf("status = %s, want the concurrent writer's cancelled", got)
	}
}

// activateRaceStore cancels runID right after its entry step is created.
type activateRaceStore struct {
	RunStore
	st    *casStore
	runID string
}

func (s *activateRaceStore) CreateStepExecution(ctx context.Context, e *domain.StepExecution) error {
	if err := s.RunStore.CreateStepExecution(ctx, e); err != nil {
		return err
	}
	if e.RunID == s.runID {
		s.st.setRunStatus(s.runID, domain.RunStatusCancelled)
	}
	return nil
}

// AC 9 (PoC SubmitOverwritesTimedOutStep), with the capability. The
// timeout scanner fails the step between the submit's read and its write.
// HEAD: the step was overwritten to completed, review created, step_completed
// emitted. Mutation: UpdateStepExecution in place of writeStepExecution.
func TestSubmitStepResult_DoesNotOverwriteTimedOutStep(t *testing.T) {
	timeOut := func(f *casFix, id string) {
		f.st.mutateExec(id, func(e *domain.StepExecution) {
			now := time.Now()
			e.Status = domain.StepStatusFailed // what handleStepTimeout writes
			e.CompletedAt = &now
			e.ErrorDetail = &domain.ErrorDetail{Classification: domain.FailureTimeout, Message: "timed out"}
		})
	}
	check := func(t *testing.T, f *casFix, id string) {
		t.Helper()
		if got := f.st.exec(id).Status; got != domain.StepStatusFailed {
			t.Fatalf("timed-out step overwritten to %s", got)
		}
		if f.st.exec(f.runID+"-review-1") != nil {
			t.Fatalf("outcome of an already-failed step was routed (review execution created)")
		}
		if n := len(f.events(domain.EventStepCompleted)); n != 0 {
			t.Fatalf("step_completed emitted for a step the timeout already failed")
		}
	}
	t.Run("SubmitStepResult", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 {
				timeOut(f, id)
			}
		}
		err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID,
			StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}})
		requireCode(t, err, domain.ErrConflict, "submit that lost to the timeout")
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		check(t, f, e.ExecutionID)
	})
	for _, read := range []int{1, 2} {
		t.Run(fmt.Sprintf("IngestResult, timeout after read %d", read), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), true)
			e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
			f.st.afterGetExec = func(id string, n int) {
				if n == read {
					timeOut(f, id)
				}
			}
			resp, err := f.orch.IngestResult(context.Background(), SubmitRequest{
				ExecutionID: e.ExecutionID, OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}})
			if err != nil || resp.Status != domain.StepStatusFailed {
				t.Fatalf("IngestResult = %+v, %v; want status failed, nil error", resp, err)
			}
			check(t, f, e.ExecutionID)
		})
	}
}

// AC 22. With the capability a submission on an assigned execution
// completes and routes: the auto-acknowledge expects (assigned, actor) and
// the terminal write the acknowledged (in_progress, actor). Mutation:
// re-using the pre-ack snapshot for the terminal write fails it.
func TestSubmitStepResult_AutoAckUnderCapability(t *testing.T) {
	f := newCasFix(t, casWorkflow(), true)
	e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
	if err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID,
		StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
		t.Fatalf("submit from assigned: %v", err)
	}
	if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusCompleted {
		t.Fatalf("execute = %s, want completed", got.Status)
	}
	if f.st.exec(f.runID+"-review-1") == nil {
		t.Fatal("the outcome was not routed")
	}
	var mine []stepTransitionCall
	for _, c := range f.st.transitions {
		if c.executionID == e.ExecutionID {
			mine = append(mine, c)
		}
	}
	want := []stepTransitionCall{
		{e.ExecutionID, domain.StepStatusAssigned, "actor-a", domain.StepStatusInProgress, true},
		{e.ExecutionID, domain.StepStatusInProgress, "actor-a", domain.StepStatusCompleted, true},
	}
	if fmt.Sprint(mine) != fmt.Sprint(want) {
		t.Fatalf("conditional writes = %+v, want %+v", mine, want)
	}
}

// AC 11. A store without the capability gets the blind UpdateStepExecution
// exactly as before: the same writes, no conditional call.
func TestStepWrites_FallbackWithoutCapability(t *testing.T) {
	f := newCasFix(t, casWorkflow(), false)
	e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
	if err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID,
		StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	want := []string{
		e.ExecutionID + " in_progress", // auto-acknowledge
		e.ExecutionID + " completed",   // terminal write
		f.runID + "-review-1 waiting",  // ActivateStep (no actor: stays waiting)
	}
	if fmt.Sprint(f.st.stepUpdates) != fmt.Sprint(want) {
		t.Fatalf("UpdateStepExecution calls = %v, want %v", f.st.stepUpdates, want)
	}
	if len(f.st.transitions) != 0 {
		t.Fatalf("conditional writes on a store without the capability: %+v", f.st.transitions)
	}
}

// AC 21 (design-check PoC SubmitStepResultReroutesCompletedExecution). A
// completed "rejected" review submitted again with "approved". HEAD:
// outcome rewritten rejected -> approved; run completed, merged once.
// Mutation: deleting the terminal check in SubmitStepResult fails it.
func TestSubmitStepResult_NeverReroutesCompletedExecution(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	for _, capability := range []bool{false, true} {
		t.Run(fmt.Sprintf("capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			review := f.addExec("review", domain.StepStatusCompleted, "actor-r")
			f.st.mutateExec(review.ExecutionID, func(e *domain.StepExecution) { e.OutcomeID = "rejected" })
			f.addExec("execute", domain.StepStatusWaiting, "")
			before := len(f.st.steps)

			err := f.orch.SubmitStepResult(context.Background(), review.ExecutionID, StepResult{OutcomeID: "approved"})
			requireCode(t, err, domain.ErrConflict, "re-submit of a completed review")
			if !errors.Is(err, ErrStepStateChanged) {
				t.Fatalf("err = %v, want ErrStepStateChanged", err)
			}
			if got := f.st.exec(review.ExecutionID).OutcomeID; got != "rejected" {
				t.Fatalf("completed review's outcome rewritten: rejected -> %s", got)
			}
			if got := f.st.run(f.runID).Status; got != domain.RunStatusActive || f.governedMerges() != 0 {
				t.Fatalf("rejected review re-routed as approved: run status=%s, governed merges=%d", got, f.governedMerges())
			}
			if n := len(f.events(domain.EventStepCompleted)); n != 0 {
				t.Fatalf("step_completed emitted for a terminal re-submit")
			}
			if len(f.st.steps) != before {
				t.Fatalf("a terminal re-submit created executions")
			}
		})
	}
}

// AC 21 (design-check PoC IngestRaceReroutesCompletedExecution). The
// winning "rejected" submit commits between IngestResult's read and
// SubmitStepResult's re-read; the loser gets the idempotent response.
// HEAD: 200 with the loser's outcome and the run merged. Mutation:
// deleting the terminal check (and the snapshot handoff) fails it.
func TestIngestResult_LostRaceKeepsWinnerOutcome(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	for _, capability := range []bool{false, true} {
		t.Run(fmt.Sprintf("capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			review := f.addExec("review", domain.StepStatusInProgress, "actor-r")
			f.st.afterGetExec = func(id string, n int) {
				if n == 1 { // the concurrent "rejected" submit commits here
					f.st.mutateExec(id, func(e *domain.StepExecution) {
						done := time.Now()
						e.Status, e.OutcomeID, e.CompletedAt = domain.StepStatusCompleted, "rejected", &done
					})
				}
			}
			resp, err := f.orch.IngestResult(context.Background(), SubmitRequest{
				ExecutionID: review.ExecutionID, OutcomeID: "approved"})
			if err != nil || resp.Status != domain.StepStatusCompleted || resp.OutcomeID != "rejected" {
				t.Fatalf("IngestResult = %+v, %v; want completed/rejected with a nil error", resp, err)
			}
			if got := f.st.exec(review.ExecutionID).OutcomeID; got != "rejected" {
				t.Fatalf("lost race overwrote the winner's outcome: rejected -> %s", got)
			}
			if f.governedMerges() != 0 || f.st.run(f.runID).Status != domain.RunStatusActive {
				t.Fatalf("lost race routed its outcome: merges=%d status=%s", f.governedMerges(), f.st.run(f.runID).Status)
			}
			if n := len(f.events(domain.EventStepCompleted)); n != 0 {
				t.Fatalf("the loser emitted step_completed")
			}
		})
	}
}

// AC 21: RecoverCompletedStep routes the stored outcome of a completed
// current step on an active run with no step write, and refuses otherwise.
func TestRecoverCompletedStep(t *testing.T) {
	setup := func(t *testing.T) (*casFix, *domain.StepExecution) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		return f, e
	}
	t.Run("routes the stored outcome", func(t *testing.T) {
		f, e := setup(t)
		if err := f.orch.RecoverCompletedStep(context.Background(), e.ExecutionID); err != nil {
			t.Fatalf("RecoverCompletedStep: %v", err)
		}
		if f.st.exec(f.runID+"-review-1") == nil || f.st.run(f.runID).CurrentStepID != "review" {
			t.Fatalf("stored outcome not routed: current=%s", f.st.run(f.runID).CurrentStepID)
		}
		for _, c := range f.st.transitions {
			if c.executionID == e.ExecutionID {
				t.Fatalf("recovery wrote the completed execution: %+v", c)
			}
		}
		for _, u := range f.st.stepUpdates {
			if strings.HasPrefix(u, e.ExecutionID+" ") {
				t.Fatalf("recovery wrote the completed execution: %s", u)
			}
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(f *casFix, e *domain.StepExecution)
	}{
		{"run not active", func(f *casFix, _ *domain.StepExecution) { f.st.setRunStatus(f.runID, domain.RunStatusPaused) }},
		{"current step differs", func(f *casFix, _ *domain.StepExecution) {
			f.st.mu.Lock()
			f.st.runs[f.runID].CurrentStepID = "review"
			f.st.mu.Unlock()
		}},
		{"a later execution of the same step exists", func(f *casFix, _ *domain.StepExecution) {
			f.addExec("execute", domain.StepStatusWaiting, "") // a retry / rework attempt
		}},
		{"the successor is no longer waiting", func(f *casFix, _ *domain.StepExecution) {
			f.addExec("review", domain.StepStatusAssigned, "actor-r")
			f.st.mu.Lock()
			f.st.runs[f.runID].CurrentStepID = "execute"
			f.st.mu.Unlock()
		}},
		{"more than the successor exists", func(f *casFix, _ *domain.StepExecution) {
			f.addExec("review", domain.StepStatusWaiting, "")
			f.addExec("execute", domain.StepStatusWaiting, "")
			f.st.mu.Lock()
			f.st.runs[f.runID].CurrentStepID = "execute"
			f.st.mu.Unlock()
		}},
		{"execution not completed", func(f *casFix, e *domain.StepExecution) {
			f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.Status = domain.StepStatusFailed })
		}},
	} {
		t.Run("refuses: "+tc.name, func(t *testing.T) {
			f, e := setup(t)
			tc.setup(f, e)
			before := len(f.st.steps)
			err := f.orch.RecoverCompletedStep(context.Background(), e.ExecutionID)
			requireCode(t, err, domain.ErrConflict, tc.name)
			if len(f.st.steps) != before || len(f.events(domain.EventStepCompleted)) != 0 {
				t.Fatalf("a refused recovery routed (%d executions, %d events)", len(f.st.steps), len(f.events(domain.EventStepCompleted)))
			}
		})
	}
}

// Codex review, round 1: routing that stopped between creating the
// successor and moving the run's cursor (a crash) is resumed — the cursor
// moves and the successor is activated — instead of being refused for
// ever. An internal publish successor runs its merge handler. Mutation:
// returning the refusal for every later execution fails both cases.
func TestRecoverCompletedStep_ResumesStrandedSuccessor(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	t.Run("manual successor", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		succ := f.addExec("review", domain.StepStatusWaiting, "")
		f.st.mu.Lock()
		f.st.runs[f.runID].CurrentStepID = "execute" // the crash: the cursor never moved
		f.st.mu.Unlock()
		before := len(f.st.steps)
		if err := f.orch.RecoverCompletedStep(context.Background(), e.ExecutionID); err != nil {
			t.Fatalf("RecoverCompletedStep: %v", err)
		}
		if got := f.st.run(f.runID).CurrentStepID; got != "review" {
			t.Fatalf("cursor = %q, want review", got)
		}
		if len(f.st.steps) != before {
			t.Fatalf("recovery created executions (%d -> %d): the successor must be resumed, not duplicated", before, len(f.st.steps))
		}
		if got := f.st.exec(succ.ExecutionID).Status; got != domain.StepStatusWaiting {
			t.Fatalf("successor = %s, want waiting (activated, no actor)", got)
		}
		for _, c := range f.st.transitions {
			if c.executionID == e.ExecutionID {
				t.Fatalf("recovery wrote the completed execution: %+v", c)
			}
		}
	})
	t.Run("internal publish successor runs its handler", func(t *testing.T) {
		f := newCasFix(t, publishWorkflow(), true)
		f.st.runs[f.runID].BranchName = "" // the merge completes without git
		r := f.addExec("review", domain.StepStatusCompleted, "actor-r")
		f.st.mutateExec(r.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "accepted" })
		pub := f.addExec("publish", domain.StepStatusWaiting, "")
		f.st.mu.Lock()
		f.st.runs[f.runID].CurrentStepID = "review"
		f.st.mu.Unlock()
		if err := f.orch.RecoverCompletedStep(context.Background(), r.ExecutionID); err != nil {
			t.Fatalf("RecoverCompletedStep: %v", err)
		}
		if got := f.st.exec(pub.ExecutionID).Status; got == domain.StepStatusWaiting {
			t.Fatal("the publish step was never activated: the run is stranded")
		}
		if got := f.st.run(f.runID).Status; got != domain.RunStatusCompleted {
			t.Fatalf("run = %s, want completed after the merge handler ran", got)
		}
	})
}

// Codex review, round 2: resuming the successor never rewinds a cursor a
// live submit moved on in the meantime — with the store's conditional
// cursor move, and with the re-read fallback. Mutation: an unconditional
// UpdateCurrentStep in resumeRoutedSuccessor fails both.
func TestRecoverCompletedStep_DoesNotRewindAMovedCursor(t *testing.T) {
	for _, capability := range []bool{true, false} {
		t.Run(fmt.Sprintf("capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
			f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
			succ := f.addExec("review", domain.StepStatusWaiting, "")
			f.st.mu.Lock()
			f.st.runs[f.runID].CurrentStepID = "execute"
			f.st.mu.Unlock()
			// The live submit finishes its routing after recovery listed the
			// executions: cursor to review, review taken and in progress.
			f.afterNextList(func() {
				f.st.mu.Lock()
				f.st.runs[f.runID].CurrentStepID = "review"
				f.st.mu.Unlock()
				f.st.mutateExec(succ.ExecutionID, func(x *domain.StepExecution) {
					x.Status, x.ActorID = domain.StepStatusInProgress, "actor-r"
				})
			})
			err := f.orch.RecoverCompletedStep(context.Background(), e.ExecutionID)
			requireCode(t, err, domain.ErrConflict, "recovery after the live submit moved the cursor")
			if got := f.st.run(f.runID).CurrentStepID; got != "review" {
				t.Fatalf("cursor = %q, want the live submit's review", got)
			}
			if got := f.st.exec(succ.ExecutionID); got.Status != domain.StepStatusInProgress || got.ActorID != "actor-r" {
				t.Fatalf("successor = %s/%q, want the live submit's in_progress/actor-r", got.Status, got.ActorID)
			}
		})
	}
	// The cursor itself: a live submit that moved it past the successor is
	// not rewound (the conditional move misses).
	f := newCasFix(t, casWorkflow(), true)
	e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
	f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
	f.addExec("review", domain.StepStatusWaiting, "")
	f.st.mu.Lock()
	f.st.runs[f.runID].CurrentStepID = "execute"
	f.st.mu.Unlock()
	f.afterNextList(func() {
		f.st.mu.Lock()
		f.st.runs[f.runID].CurrentStepID = "later-step"
		f.st.mu.Unlock()
	})
	if err := f.orch.RecoverCompletedStep(context.Background(), e.ExecutionID); err == nil {
		t.Fatal("recovery resumed a run whose cursor had moved on")
	}
	if got := f.st.run(f.runID).CurrentStepID; got != "later-step" {
		t.Fatalf("cursor rewound to %q", got)
	}
}

// Codex review, round 3: the step's own actor acknowledging concurrently
// (assigned → in_progress) is not a conflict — not between IngestResult's
// read and SubmitStepResult's, and not between SubmitStepResult's read and
// its auto-acknowledge — so the result is applied, also through the queue
// consumer. A rebind in the same window still is. Mutation: requiring an
// exact snapshot match (or failing on the auto-ack miss) fails it.
func TestSubmit_ConcurrentAcknowledgeIsNotAConflict(t *testing.T) {
	ack := func(f *casFix, id string) {
		f.st.mutateExec(id, func(x *domain.StepExecution) {
			now := time.Now()
			x.Status, x.StartedAt = domain.StepStatusInProgress, &now
		})
	}
	for _, capability := range []bool{false, true} {
		t.Run(fmt.Sprintf("IngestResult, capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
			f.st.afterGetExec = func(id string, n int) {
				if n == 1 {
					ack(f, id)
				}
			}
			resp, err := f.orch.IngestResult(context.Background(), SubmitRequest{ExecutionID: e.ExecutionID,
				OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}, ActorID: "actor-a"})
			if err != nil || resp.Status != domain.StepStatusCompleted {
				t.Fatalf("IngestResult = %+v, %v; want completed", resp, err)
			}
		})
	}
	t.Run("auto-acknowledge lost to the owner's acknowledge (capability)", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 { // SubmitStepResult has its snapshot
				ack(f, id)
			}
		}
		if err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID,
			StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		if got := f.st.exec(e.ExecutionID).Status; got != domain.StepStatusCompleted {
			t.Fatalf("execute = %s, want completed", got)
		}
		if f.st.exec(f.runID+"-review-1") == nil {
			t.Fatal("not routed")
		}
	})
	t.Run("consumer applies its result", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 {
				ack(f, id)
			}
		}
		req := actor.AssignmentRequest{AssignmentID: e.ExecutionID, RunID: f.runID, StepID: "execute", ActorID: "actor-a"}
		if err := NewConsumer(nil, f.orch).executeAndSubmit(context.Background(), discardLogger(), req, staticProvider{outcome: "completed"}); err != nil {
			t.Fatalf("consumer: %v", err)
		}
		if got := f.st.exec(e.ExecutionID).Status; got != domain.StepStatusCompleted {
			t.Fatalf("execute = %s, want completed (the result was dropped)", got)
		}
	})
	t.Run("consumer keeps the error for a rebind", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 {
				f.st.mutateExec(id, func(x *domain.StepExecution) { x.ActorID = "actor-b" })
			}
		}
		req := actor.AssignmentRequest{AssignmentID: e.ExecutionID, RunID: f.runID, StepID: "execute", ActorID: "actor-a"}
		if err := NewConsumer(nil, f.orch).executeAndSubmit(context.Background(), discardLogger(), req, staticProvider{outcome: "completed"}); err == nil {
			t.Fatal("a result for a rebound execution was reported as applied")
		}
		if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusAssigned || got.ActorID != "actor-b" {
			t.Fatalf("execution = %s/%q, want the rebind's", got.Status, got.ActorID)
		}
	})
}

// Codex review, round 4: a redelivered assignment for a step its first
// submit completed but whose routing failed finishes the routing (through
// RecoverCompletedStep) instead of being acknowledged with the run
// stranded; a redelivery for a step that was routed changes nothing.
// Mutation: dropping the RecoverCompletedStep call fails the first case.
func TestConsumer_RedeliveryFinishesIncompleteRouting(t *testing.T) {
	req := func(f *casFix, id string) actor.AssignmentRequest {
		return actor.AssignmentRequest{AssignmentID: id, RunID: f.runID, StepID: "execute", ActorID: "actor-a"}
	}
	t.Run("routing never ran", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		if err := NewConsumer(nil, f.orch).executeAndSubmit(context.Background(), discardLogger(), req(f, e.ExecutionID), staticProvider{outcome: "completed"}); err != nil {
			t.Fatalf("redelivery: %v", err)
		}
		if f.st.exec(f.runID+"-review-1") == nil || f.st.run(f.runID).CurrentStepID != "review" {
			t.Fatal("the completed step's routing was not finished")
		}
	})
	t.Run("already routed", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		f.addExec("review", domain.StepStatusWaiting, "") // routed: the cursor is at review
		before := len(f.st.steps)
		if err := NewConsumer(nil, f.orch).executeAndSubmit(context.Background(), discardLogger(), req(f, e.ExecutionID), staticProvider{outcome: "completed"}); err != nil {
			t.Fatalf("redelivery: %v", err)
		}
		if len(f.st.steps) != before {
			t.Fatal("a routed step was routed again")
		}
	})
}

// branchWorkflow has two steps that run on a divergence branch.
func branchWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "wf-branch", Path: "workflows/wf-branch.yaml", EntryStep: "split",
		Steps: []domain.StepDefinition{
			{ID: "split", Name: "Split", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}},
			{ID: "draft", Name: "Draft", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "polish"}}},
			{ID: "polish", Name: "Polish", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}},
		},
	}
}

// Codex review, round 5: a completed divergence-branch execution whose
// branch routing did not finish is recovered on the BRANCH cursor — the
// run's cursor is divergence:<id> — and one that was routed is refused.
// Mutation: dropping the branch dispatch in RecoverCompletedStep (the
// run-cursor check refuses every branch execution) fails the first two.
func TestRecoverCompletedStep_DivergenceBranch(t *testing.T) {
	setup := func(t *testing.T, branchCursor string) (*casFix, *domain.StepExecution) {
		f := newCasFix(t, branchWorkflow(), true)
		f.st.branches["b1"] = &domain.Branch{BranchID: "b1", RunID: f.runID, DivergenceID: "div-1",
			Status: domain.BranchStatusInProgress, CurrentStepID: branchCursor}
		created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		done := created.Add(time.Second)
		e := &domain.StepExecution{ExecutionID: f.runID + "-b1-draft-1", RunID: f.runID, StepID: "draft", BranchID: "b1",
			Status: domain.StepStatusCompleted, ActorID: "actor-a", OutcomeID: "done", Attempt: 1, CreatedAt: created, CompletedAt: &done}
		f.st.steps[e.ExecutionID] = copyExec(e)
		f.st.runs[f.runID].CurrentStepID = "divergence:div-1"
		return f, e
	}
	for _, tc := range []struct{ name, cursor string }{
		{"routing never ran", "draft"},
		{"branch cursor moved, next execution never created", "polish"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e := setup(t, tc.cursor)
			if err := f.orch.RecoverCompletedStep(context.Background(), e.ExecutionID); err != nil {
				t.Fatalf("RecoverCompletedStep: %v", err)
			}
			if f.st.exec(f.runID+"-b1-polish-1") == nil {
				t.Fatal("the branch's next execution was not created")
			}
			b, _ := f.st.GetBranch(context.Background(), "b1")
			if b.CurrentStepID != "polish" {
				t.Fatalf("branch cursor = %q, want polish", b.CurrentStepID)
			}
			if got := f.st.run(f.runID).CurrentStepID; got != "divergence:div-1" {
				t.Fatalf("run cursor = %q, want it left at the divergence", got)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		setup func(f *casFix)
	}{
		{"routed: the next execution exists", func(f *casFix) {
			f.st.steps[f.runID+"-b1-polish-1"] = &domain.StepExecution{ExecutionID: f.runID + "-b1-polish-1", RunID: f.runID,
				StepID: "polish", BranchID: "b1", Status: domain.StepStatusWaiting, Attempt: 1,
				CreatedAt: time.Date(2026, 10, 1, 12, 0, 5, 0, time.UTC)}
		}},
		{"branch completed", func(f *casFix) { f.st.branches["b1"].Status = domain.BranchStatusCompleted }},
		{"run not at this divergence", func(f *casFix) { f.st.runs[f.runID].CurrentStepID = "divergence:div-2" }},
	} {
		t.Run("refuses: "+tc.name, func(t *testing.T) {
			f, e := setup(t, "polish")
			tc.setup(f)
			before := len(f.st.steps)
			err := f.orch.RecoverCompletedStep(context.Background(), e.ExecutionID)
			requireCode(t, err, domain.ErrConflict, tc.name)
			if len(f.st.steps) != before {
				t.Fatal("a refused branch recovery created executions")
			}
		})
	}
}

// Codex review, round 5: a stale delivery whose execution was rebound and
// then resolved by the new owner has nothing left to submit (nil, not a
// retryable 403); one whose execution is still live keeps the refusal.
func TestConsumer_StaleDeliveryAfterRebind(t *testing.T) {
	req := func(f *casFix, id string) actor.AssignmentRequest {
		return actor.AssignmentRequest{AssignmentID: id, RunID: f.runID, StepID: "execute", ActorID: "actor-a"}
	}
	f := newCasFix(t, casWorkflow(), true)
	e := f.addExec("execute", domain.StepStatusCompleted, "actor-b") // rebound to b, b completed it
	f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
	f.addExec("review", domain.StepStatusWaiting, "")
	if err := NewConsumer(nil, f.orch).executeAndSubmit(context.Background(), discardLogger(), req(f, e.ExecutionID), staticProvider{outcome: "completed"}); err != nil {
		t.Fatalf("stale delivery for a resolved execution = %v, want nil", err)
	}
	g := newCasFix(t, casWorkflow(), true)
	live := g.addExec("execute", domain.StepStatusInProgress, "actor-b")
	err := NewConsumer(nil, g.orch).executeAndSubmit(context.Background(), discardLogger(), req(g, live.ExecutionID), staticProvider{outcome: "completed"})
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrForbidden {
		t.Fatalf("stale delivery for a live rebound execution = %v, want forbidden", err)
	}
}

// failingCancelAssignments fails every UpdateAssignmentStatus.
type failingCancelAssignments struct{ *memAssignmentStore }

func (failingCancelAssignments) UpdateAssignmentStatus(context.Context, string, domain.AssignmentStatus, *time.Time) error {
	return errors.New("assignment store unavailable")
}

// Codex review, round 2: with the conditional store the step is written
// before the assignment row is cancelled; when the cancel fails the step is
// put back, so no unbound execution sits next to a still-active row.
// Without the capability the original order (row first) leaves the step
// untouched. Mutation: dropping the compensation fails the first case.
func TestReleaseStep_FailedCancelLeavesConsistentState(t *testing.T) {
	for _, capability := range []bool{true, false} {
		t.Run(fmt.Sprintf("capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
			f.asg.assignments["claim-a"] = &domain.Assignment{AssignmentID: "claim-a", RunID: f.runID,
				ExecutionID: e.ExecutionID, ActorID: "actor-a", Status: domain.AssignmentStatusActive}
			f.orch.assignments = failingCancelAssignments{f.asg}
			before := f.st.exec(e.ExecutionID)
			err := f.orch.ReleaseStep(context.Background(), ReleaseRequest{ActorID: "actor-a", AssignmentID: "claim-a"})
			if err == nil {
				t.Fatal("release succeeded although the assignment could not be cancelled")
			}
			got := f.st.exec(e.ExecutionID)
			if got.Status != before.Status || got.ActorID != before.ActorID {
				t.Fatalf("execution = %s/%q, want it back at %s/%q", got.Status, got.ActorID, before.Status, before.ActorID)
			}
			if got := f.asg.assignments["claim-a"].Status; got != domain.AssignmentStatusActive {
				t.Fatalf("assignment row = %s, want active", got)
			}
			if len(f.events(domain.EventTaskReleased)) != 0 {
				t.Fatal("a failed release emitted its event")
			}
		})
	}
}

// AC 10. Every engine step writer, with the capability, stops without side
// effects when it loses its race. Each row's hook commits a concurrent
// writer's change right after the writer's read.
func TestStepWriters_LostRaceHasNoSideEffects(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")
	ctx := context.Background()
	rebind := func(f *casFix, id, actorID string) {
		f.st.mutateExec(id, func(e *domain.StepExecution) {
			e.Status, e.ActorID = domain.StepStatusAssigned, actorID
		})
	}

	t.Run("SubmitStepResult auto-acknowledge", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 {
				rebind(f, id, "actor-b")
			}
		}
		err := f.orch.SubmitStepResult(ctx, e.ExecutionID, StepResult{OutcomeID: "completed"})
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if got := f.st.exec(e.ExecutionID); got.ActorID != "actor-b" || got.Status != domain.StepStatusAssigned {
			t.Fatalf("execution = %s/%s, want the winner's assigned/actor-b", got.Status, got.ActorID)
		}
		if len(f.events(domain.EventStepStarted)) != 0 || len(f.events(domain.EventStepCompleted)) != 0 {
			t.Fatal("a lost auto-acknowledge emitted events")
		}
	})

	t.Run("failStepWithClassification", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 { // the owner's valid submit completes the step
				f.st.mutateExec(id, func(x *domain.StepExecution) {
					x.Status, x.OutcomeID = domain.StepStatusCompleted, "completed"
				})
			}
		}
		_, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: e.ExecutionID, OutcomeID: "completed"})
		requireCode(t, err, domain.ErrInvalidParams, "missing required output")
		if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusCompleted || got.ErrorDetail != nil {
			t.Fatalf("execution = %s (%+v), want the winner's completed row", got.Status, got.ErrorDetail)
		}
		if f.st.exec(f.runID+"-execute-2") != nil {
			t.Fatal("a retry execution was created for a step someone else resolved")
		}
		if got := f.st.run(f.runID).Status; got != domain.RunStatusActive {
			t.Fatalf("run = %s, want active", got)
		}
	})

	t.Run("ActivateStep no-actor path", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusWaiting, "")
		f.afterNextList(func() {
			rebind(f, e.ExecutionID, "actor-x") // a claim won
		})
		err := f.orch.ActivateStep(ctx, f.runID, "execute")
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if got := f.st.exec(e.ExecutionID); got.ActorID != "actor-x" {
			t.Fatalf("lost activation overwrote the claim: actor=%q", got.ActorID)
		}
	})

	t.Run("ActivateStep auto-assign", func(t *testing.T) {
		wf := casWorkflow()
		wf.Steps[0].Execution = &domain.ExecutionConfig{Mode: domain.ExecModeAutomatedOnly}
		f := newCasFix(t, wf, true)
		f.orch.actorSelector = &mockActorSelector{actor: &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated}}
		e := f.addExec("execute", domain.StepStatusWaiting, "")
		f.afterNextList(func() {
			rebind(f, e.ExecutionID, "actor-x")
		})
		err := f.orch.ActivateStep(ctx, f.runID, "execute")
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if len(f.actors.delivered) != 0 {
			t.Fatalf("a lost activation delivered %d assignments", len(f.actors.delivered))
		}
		if len(f.events(domain.EventStepAssigned)) != 0 {
			t.Fatal("a lost activation emitted step_assigned")
		}
		if len(f.asg.assignments) != 0 {
			t.Fatalf("a lost activation created %d assignment rows", len(f.asg.assignments))
		}
		if got := f.st.exec(e.ExecutionID); got.ActorID != "actor-x" {
			t.Fatalf("lost activation overwrote the claim: actor=%q", got.ActorID)
		}
	})

	t.Run("ActivateStep precondition persist", func(t *testing.T) {
		wf := casWorkflow()
		wf.Steps[0].Preconditions = []domain.Precondition{{Type: "cross_artifact_valid"}}
		f := newCasFix(t, wf, true)
		f.orch.validator = failingValidator{}
		e := f.addExec("execute", domain.StepStatusWaiting, "")
		f.afterNextList(func() {
			rebind(f, e.ExecutionID, "actor-x")
		})
		err := f.orch.ActivateStep(ctx, f.runID, "execute")
		requireCode(t, err, domain.ErrPrecondition, "failed precondition")
		if got := f.st.exec(e.ExecutionID); got.ActorID != "actor-x" || got.ErrorDetail != nil {
			t.Fatalf("lost precondition persist overwrote the row: actor=%q detail=%+v", got.ActorID, got.ErrorDetail)
		}
	})

	t.Run("activateInternalStep", func(t *testing.T) {
		f := newCasFix(t, publishWorkflow(), true)
		f.st.runs[f.runID].BranchName = ""
		e := f.addExec("publish", domain.StepStatusWaiting, "")
		f.afterNextList(func() { // a concurrent activation advanced it first
			f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) {
				x.Status, x.ActorID = domain.StepStatusInProgress, EngineMergeActorID
			})
		})
		err := f.orch.ActivateStep(ctx, f.runID, "publish")
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if got := f.st.run(f.runID).Status; got != domain.RunStatusActive {
			t.Fatalf("the merge handler ran on a lost activation: run = %s", got)
		}
		if len(f.events(domain.EventStepStarted)) != 0 {
			t.Fatal("a lost activation emitted step_started")
		}
	})

	t.Run("advancePublishStepIfAny", func(t *testing.T) {
		f := newCasFix(t, publishWorkflow(), true)
		f.st.setRunStatus(f.runID, domain.RunStatusCommitting)
		e := f.addExec("publish", domain.StepStatusInProgress, EngineMergeActorID)
		f.afterNextList(func() { // a sibling merge attempt advanced the step
			f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) {
				x.Status, x.OutcomeID = domain.StepStatusCompleted, "published"
			})
		})
		if err := f.orch.advancePublishStepIfAny(ctx, f.st.run(f.runID), "merge_failed"); err != nil {
			t.Fatalf("advancePublishStepIfAny = %v, want nil (a sibling won)", err)
		}
		if got := f.st.exec(e.ExecutionID).OutcomeID; got != "published" {
			t.Fatalf("publish outcome overwritten: %s", got)
		}
		if len(f.events(domain.EventStepCompleted)) != 0 {
			t.Fatal("the loser emitted step_completed")
		}
		if got := f.st.run(f.runID).Status; got != domain.RunStatusCommitting {
			t.Fatalf("the loser routed the run back: %s", got)
		}
	})

	t.Run("AssignStep", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusWaiting, "")
		f.afterNextList(func() {
			rebind(f, e.ExecutionID, "actor-x")
		})
		_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: f.runID, StepID: "execute", ActorID: "actor-a"})
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if got := f.st.exec(e.ExecutionID).ActorID; got != "actor-x" {
			t.Fatalf("lost assign overwrote the winner: %q", got)
		}
	})

	t.Run("AcknowledgeStep", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 { // released meanwhile
				f.st.mutateExec(id, func(x *domain.StepExecution) { x.Status, x.ActorID = domain.StepStatusWaiting, "" })
			}
		}
		_, err := f.orch.AcknowledgeStep(ctx, AcknowledgeRequest{ActorID: "actor-a", ExecutionID: e.ExecutionID})
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusWaiting {
			t.Fatalf("lost acknowledge overwrote the release: %s", got.Status)
		}
		if len(f.events(domain.EventStepStarted)) != 0 {
			t.Fatal("a lost acknowledge emitted step_started")
		}
	})

	t.Run("ReleaseStep", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.asg.assignments["claim-a"] = &domain.Assignment{AssignmentID: "claim-a", RunID: f.runID,
			ExecutionID: e.ExecutionID, ActorID: "actor-a", Status: domain.AssignmentStatusActive}
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 { // the owner's submit completed it meanwhile
				f.st.mutateExec(id, func(x *domain.StepExecution) { x.Status = domain.StepStatusCompleted })
			}
		}
		err := f.orch.ReleaseStep(ctx, ReleaseRequest{ActorID: "actor-a", AssignmentID: "claim-a"})
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusCompleted || got.ActorID != "actor-a" {
			t.Fatalf("lost release overwrote the row: %s/%q", got.Status, got.ActorID)
		}
		if got := f.asg.assignments["claim-a"].Status; got != domain.AssignmentStatusActive {
			t.Fatalf("lost release cancelled the assignment row (%s)", got)
		}
		if len(f.events(domain.EventTaskReleased)) != 0 {
			t.Fatal("a lost release emitted its event")
		}
	})

	t.Run("ClaimStep", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusWaiting, "")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 { // an operator assign won (no assignment row)
				rebind(f, id, "actor-x")
			}
		}
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-a", ExecutionID: e.ExecutionID})
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("err = %v, want ErrStepStateChanged", err)
		}
		if got := f.st.exec(e.ExecutionID).ActorID; got != "actor-x" {
			t.Fatalf("lost claim overwrote the assign: %q", got)
		}
		row := f.asg.assignments["claim-"+e.ExecutionID+"-actor-a"]
		if row == nil || row.Status != domain.AssignmentStatusCancelled {
			t.Fatalf("the lost claim's assignment row = %+v, want cancelled", row)
		}
		if len(f.events(domain.EventStepAssigned)) != 0 {
			t.Fatal("a lost claim emitted step_assigned")
		}
	})

	t.Run("recordGitConflictOnStep", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("review", domain.StepStatusCompleted, "actor-r")
		f.afterNextList(func() { // a concurrent writer changed the row
			f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.ActorID = "actor-z" })
		})
		f.orch.recordGitConflictOnStep(ctx, f.st.run(f.runID), errors.New("merge conflict"))
		if got := f.st.exec(e.ExecutionID); got.ErrorDetail != nil || got.ActorID != "actor-z" {
			t.Fatalf("lost git-conflict write overwrote the row: %+v", got)
		}
	})

	t.Run("terminateLiveSteps (TASK-007 CancelRun)", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		f.orch.entryPolicy = StepEntryPolicy{TerminateStepsOnCancel: true}
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		f.afterNextList(func() { // the owner's submit completed it inside the cancel
			f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) {
				x.Status, x.OutcomeID = domain.StepStatusCompleted, "completed"
			})
		})
		if err := f.orch.CancelRun(ctx, f.runID); err != nil {
			t.Fatalf("CancelRun: %v", err)
		}
		if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusCompleted || got.ErrorDetail != nil {
			t.Fatalf("terminate overwrote a completed step: %s %+v", got.Status, got.ErrorDetail)
		}
	})
}

// AC 13. After TASK-007's cancel (which closes live executions), a submit
// that read the execution before the cancel leaves no completed step routed
// on the cancelled run.
func TestSubmitRacingTerminatingCancel(t *testing.T) {
	f := newCasFix(t, casWorkflow(), true)
	f.orch.entryPolicy = StepEntryPolicy{TerminateStepsOnCancel: true}
	e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
	f.st.afterGetExec = func(_ string, n int) {
		if n == 1 { // the submit has its snapshot; the cancel lands now
			if err := f.orch.CancelRun(context.Background(), f.runID); err != nil {
				t.Errorf("CancelRun: %v", err)
			}
		}
	}
	err := f.orch.SubmitStepResult(context.Background(), e.ExecutionID,
		StepResult{OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}})
	if !errors.Is(err, ErrStepStateChanged) {
		t.Fatalf("err = %v, want ErrStepStateChanged", err)
	}
	got := f.st.exec(e.ExecutionID)
	if got.Status != domain.StepStatusSkipped || !isRunTerminated(got) {
		t.Fatalf("execution = %s, want skipped/run_terminated", got.Status)
	}
	if f.st.exec(f.runID+"-review-1") != nil || len(f.events(domain.EventStepCompleted)) != 0 {
		t.Fatal("a completed step was routed on the cancelled run")
	}
	if got := f.st.run(f.runID).Status; got != domain.RunStatusCancelled {
		t.Fatalf("run = %s, want cancelled", got)
	}
}

type failingValidator struct{}

func (failingValidator) Validate(context.Context, string) domain.ValidationResult {
	return domain.ValidationResult{Status: "failed", Errors: []domain.ValidationError{{RuleID: "r1", Message: "broken"}}}
}
