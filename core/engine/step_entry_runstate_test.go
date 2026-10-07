package engine

// INIT-023 TASK-007 — run-status guards and run-end termination (AC 11,
// 12, 24, 25; audit P3-16).

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
)

// guardWorkflow covers every entry point the run-status guard reaches.
func guardWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "guards", EntryStep: "manual",
		Steps: []domain.StepDefinition{
			{ID: "manual", Name: "Manual", Type: domain.StepTypeManual, RequiredOutputs: []string{"deliverable"},
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "next"}}},
			{ID: "next", Name: "Next", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}},
			{ID: "auto", Name: "Auto", Type: domain.StepTypeAutomated,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeAutomatedOnly},
				Outcomes:  []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "next"}}},
			{ID: "retryable", Name: "Retryable", Type: domain.StepTypeManual, Retry: &domain.RetryConfig{Limit: 3, Backoff: "fixed"},
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "next"}}},
		},
	}
}

type guardFixture struct {
	st   *writeCountingRunStore
	gw   *countingActorAssigner
	orch *Orchestrator
}

func newGuardFixture(status domain.RunStatus, p StepEntryPolicy, execs ...*domain.StepExecution) guardFixture {
	f := guardFixture{
		st: &writeCountingRunStore{mockRunStore: &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", status)}}},
		gw: &countingActorAssigner{},
	}
	f.st.createdSteps = execs
	bs := newClaimBlockingStore()
	bs.actors["worker"] = activeActor("worker", domain.ActorTypeHuman)
	bs.actors["runner"] = activeActor("runner", domain.ActorTypeAutomated)
	f.orch = &Orchestrator{store: f.st, wfLoader: &claimTestWFLoader{wf: guardWorkflow()}, events: &mockEventEmitter{},
		git: &stubGitOperator{}, actors: f.gw, blocking: bs, artifacts: &stubArtifactReader{},
		assignments: newMemAssignmentStore(), policy: branchprotect.NewPermissive(),
		actorSelector: &mockActorSelector{actor: activeActor("runner", domain.ActorTypeAutomated)}}
	f.orch.WithStepEntryPolicy(p)
	return f
}

// TestStepEntry_RunStatusGuards: under EnforceRunStatus every step entry
// point refuses a run that is not active, writes nothing and dispatches
// nothing; under the zero value the same calls behave as today.
// Mutation: deleting requireActiveRun from any one entry point fails that
// sub-test (IngestResult is driven without its required output, so a
// missing guard would fail-and-retry the step before SubmitStepResult's
// guard is reached; ActivateStep is driven on an automated_only step with
// a selector, so a missing guard dispatches runner work).
func TestStepEntry_RunStatusGuards(t *testing.T) {
	type entry struct {
		name string
		exec func() *domain.StepExecution
		call func(ctx context.Context, f guardFixture, strict bool) error
	}
	waiting := func(step string) func() *domain.StepExecution {
		return func() *domain.StepExecution {
			return &domain.StepExecution{ExecutionID: "run-1-" + step + "-1", RunID: "run-1", StepID: step,
				Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}
		}
	}
	bound := func(step string, status domain.StepExecutionStatus) func() *domain.StepExecution {
		return func() *domain.StepExecution {
			now := time.Now()
			return &domain.StepExecution{ExecutionID: "run-1-" + step + "-1", RunID: "run-1", StepID: step,
				Status: status, ActorID: "worker", Attempt: 1, CreatedAt: now, StartedAt: &now}
		}
	}
	entries := []entry{
		{name: "AssignStep", exec: waiting("manual"), call: func(ctx context.Context, f guardFixture, _ bool) error {
			_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "manual", ActorID: "worker"})
			return err
		}},
		{name: "ClaimStep", exec: waiting("manual"), call: func(ctx context.Context, f guardFixture, _ bool) error {
			_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "worker", ExecutionID: "run-1-manual-1"})
			return err
		}},
		{name: "AcknowledgeStep", exec: bound("manual", domain.StepStatusAssigned), call: func(ctx context.Context, f guardFixture, _ bool) error {
			_, err := f.orch.AcknowledgeStep(ctx, AcknowledgeRequest{ActorID: "worker", ExecutionID: "run-1-manual-1"})
			return err
		}},
		// Review round 1 (Deviation 1): the guard sits before the
		// idempotent in_progress answer, so a runner re-acknowledging its
		// own in_progress execution on a dead run is told to stop.
		{name: "AcknowledgeStep re-acknowledging in_progress", exec: bound("manual", domain.StepStatusInProgress), call: func(ctx context.Context, f guardFixture, _ bool) error {
			_, err := f.orch.AcknowledgeStep(ctx, AcknowledgeRequest{ActorID: "worker", ExecutionID: "run-1-manual-1"})
			return err
		}},
		{name: "IngestResult", exec: bound("manual", domain.StepStatusInProgress), call: func(ctx context.Context, f guardFixture, strict bool) error {
			req := SubmitRequest{ExecutionID: "run-1-manual-1", OutcomeID: "done"}
			if !strict {
				req.ArtifactsProduced = []string{"deliverable"}
			}
			_, err := f.orch.IngestResult(ctx, req)
			return err
		}},
		{name: "SubmitStepResult", exec: bound("manual", domain.StepStatusAssigned), call: func(ctx context.Context, f guardFixture, _ bool) error {
			return f.orch.SubmitStepResult(ctx, "run-1-manual-1", StepResult{OutcomeID: "done"})
		}},
		{name: "ActivateStep", exec: waiting("auto"), call: func(ctx context.Context, f guardFixture, _ bool) error {
			return f.orch.ActivateStep(ctx, "run-1", "auto")
		}},
		{name: "RetryStep", exec: func() *domain.StepExecution {
			return &domain.StepExecution{ExecutionID: "run-1-retryable-1", RunID: "run-1", StepID: "retryable",
				Status: domain.StepStatusFailed, ActorID: "worker", Attempt: 1, CreatedAt: time.Now(),
				ErrorDetail: &domain.ErrorDetail{Classification: domain.FailureTransient, Message: "flaky"}}
		}, call: func(ctx context.Context, f guardFixture, _ bool) error {
			return f.orch.RetryStep(ctx, f.st.createdSteps[0])
		}},
	}
	statuses := []domain.RunStatus{
		domain.RunStatusCancelled, domain.RunStatusFailed, domain.RunStatusPaused,
		domain.RunStatusCommitting, domain.RunStatusPartiallyMerged, domain.RunStatusPending,
	}
	ctx := context.Background()
	for _, status := range statuses {
		for _, e := range entries {
			status, e := status, e
			t.Run(string(status)+"/"+e.name+"/enforced", func(t *testing.T) {
				f := newGuardFixture(status, StepEntryPolicy{EnforceRunStatus: true}, e.exec())
				before := snapshotExecs(f.st.mockRunStore)
				err := e.call(ctx, f, true)
				requireCode(t, err, domain.ErrConflict, e.name+" on a "+string(status)+" run")
				if !strings.Contains(err.Error(), string(status)) {
					t.Fatalf("refusal %q must name the run status %q", err.Error(), status)
				}
				requireExecsUnchanged(t, f.st.mockRunStore, before, e.name)
				if f.st.stepWrites != 0 || f.st.stepCreates != 0 {
					t.Fatalf("%s wrote %d / created %d executions on a %s run", e.name, f.st.stepWrites, f.st.stepCreates, status)
				}
				if len(f.gw.delivered) != 0 {
					t.Fatalf("%s dispatched %d assignments for a %s run", e.name, len(f.gw.delivered), status)
				}
			})
			t.Run(string(status)+"/"+e.name+"/zero value", func(t *testing.T) {
				f := newGuardFixture(status, StepEntryPolicy{}, e.exec())
				if err := e.call(ctx, f, false); err != nil {
					t.Fatalf("zero value: %s must behave as before (succeed), got %v", e.name, err)
				}
				switch e.name {
				case "ActivateStep":
					if len(f.gw.delivered) != 1 {
						t.Fatalf("zero value: ActivateStep should dispatch as before, got %d", len(f.gw.delivered))
					}
				case "RetryStep":
					if f.st.stepCreates != 1 {
						t.Fatalf("zero value: RetryStep should create the retry execution, got %d", f.st.stepCreates)
					}
				}
			})
		}
	}
}

// ── AC 12 ──

func terminationExecs() []*domain.StepExecution {
	now := time.Now()
	mk := func(id string, status domain.StepExecutionStatus, actorID string) *domain.StepExecution {
		e := &domain.StepExecution{ExecutionID: id, RunID: "run-1", StepID: "manual", Status: status,
			ActorID: actorID, Attempt: 1, CreatedAt: now}
		if status.IsTerminal() {
			e.CompletedAt = &now
			e.OutcomeID = "done"
		}
		return e
	}
	return []*domain.StepExecution{
		mk("x-waiting", domain.StepStatusWaiting, ""),
		mk("x-assigned", domain.StepStatusAssigned, "worker"),
		mk("x-in-progress", domain.StepStatusInProgress, "worker"),
		mk("x-blocked", domain.StepStatusBlocked, "worker"),
		mk("x-completed", domain.StepStatusCompleted, "done-actor"),
		mk("x-failed", domain.StepStatusFailed, "failed-actor"),
	}
}

var liveTerminationIDs = []string{"x-assigned", "x-blocked", "x-in-progress", "x-waiting"}

// TestStepEntry_TerminateStepsOnRunEnd asserts the quiescent post-state of
// each run-end path; it does not claim atomicity against a concurrent
// bind (Design §10 note).
// Mutation: removing the terminateLiveSteps call from any one of the run-end
// paths fails that sub-test.
func TestStepEntry_TerminateStepsOnRunEnd(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	type path struct {
		name      string
		runStatus domain.RunStatus
		end       func(ctx context.Context, o *Orchestrator, run *domain.Run) error
		cause     string
		endStatus domain.RunStatus
	}
	paths := []path{
		{"CancelRun from active", domain.RunStatusActive, func(ctx context.Context, o *Orchestrator, _ *domain.Run) error {
			return o.CancelRun(ctx, "run-1")
		}, "run cancelled", domain.RunStatusCancelled},
		{"CancelRun from paused", domain.RunStatusPaused, func(ctx context.Context, o *Orchestrator, _ *domain.Run) error {
			return o.CancelRun(ctx, "run-1")
		}, "run cancelled", domain.RunStatusCancelled},
		{"CancelRun from partially-merged", domain.RunStatusPartiallyMerged, func(ctx context.Context, o *Orchestrator, _ *domain.Run) error {
			return o.CancelRun(ctx, "run-1")
		}, "run cancelled", domain.RunStatusCancelled},
		{"FailRun", domain.RunStatusActive, func(ctx context.Context, o *Orchestrator, _ *domain.Run) error {
			return o.FailRun(ctx, "run-1", "retries exhausted")
		}, "run failed: retries exhausted", domain.RunStatusFailed},
		{"failRunOnMergeError", domain.RunStatusCommitting, func(ctx context.Context, o *Orchestrator, run *domain.Run) error {
			return o.failRunOnMergeError(ctx, run, domainErr("merge conflict"))
		}, "run failed: merge failed permanently", domain.RunStatusFailed},
	}
	ctx := context.Background()

	for _, p := range paths {
		p := p
		t.Run(p.name+"/TerminateStepsOnCancel", func(t *testing.T) {
			f := newGuardFixture(p.runStatus, StepEntryPolicy{}, terminationExecs()...)
			canceller := &cancellingAssignments{memAssignmentStore: newMemAssignmentStore()}
			for _, id := range []string{"x-assigned", "x-in-progress"} {
				_ = canceller.CreateAssignment(ctx, &domain.Assignment{AssignmentID: "claim-" + id, RunID: "run-1",
					ExecutionID: id, ActorID: "worker", Status: domain.AssignmentStatusActive, AssignedAt: time.Now()})
			}
			f.orch.assignments = canceller
			f.orch.WithStepEntryPolicy(StepEntryPolicy{TerminateStepsOnCancel: true})

			if err := p.end(ctx, f.orch, f.st.runs["run-1"]); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			if got := f.st.runs["run-1"].Status; got != p.endStatus {
				t.Fatalf("run status = %s, want %s", got, p.endStatus)
			}
			for _, id := range liveTerminationIDs {
				e := execByID(f.st.mockRunStore, id)
				if e.Status != domain.StepStatusSkipped {
					t.Fatalf("%s: status = %s, want skipped", id, e.Status)
				}
				if e.ErrorDetail == nil || e.ErrorDetail.Classification != domain.FailureRunTerminated {
					t.Fatalf("%s: error detail = %+v, want run_terminated", id, e.ErrorDetail)
				}
				if !strings.Contains(e.ErrorDetail.Message, p.cause) {
					t.Fatalf("%s: message %q must name the cause %q", id, e.ErrorDetail.Message, p.cause)
				}
				if e.CompletedAt == nil {
					t.Fatalf("%s: CompletedAt not set", id)
				}
			}
			for id, want := range map[string]domain.StepExecutionStatus{"x-completed": domain.StepStatusCompleted, "x-failed": domain.StepStatusFailed} {
				e := execByID(f.st.mockRunStore, id)
				if e.Status != want || (e.ErrorDetail != nil && e.ErrorDetail.Classification == domain.FailureRunTerminated) {
					t.Fatalf("%s must be untouched by termination, got %s %+v", id, e.Status, e.ErrorDetail)
				}
			}
			got := append([]string(nil), canceller.cancelled...)
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(liveTerminationIDs, ",") {
				t.Fatalf("assignment canceller calls = %v, want one per terminated execution %v", got, liveTerminationIDs)
			}
			for _, id := range []string{"x-assigned", "x-in-progress"} {
				if a := canceller.assignments["claim-"+id]; a.Status != domain.AssignmentStatusCancelled {
					t.Fatalf("assignment of %s = %s, want cancelled", id, a.Status)
				}
			}

			// After termination: acknowledge refuses, and (TerminateStepsOnCancel
			// alone) a late submit takes the idempotent terminal branch without a write.
			_, err := f.orch.AcknowledgeStep(ctx, AcknowledgeRequest{ActorID: "worker", ExecutionID: "x-assigned"})
			requireCode(t, err, domain.ErrConflict, "AcknowledgeStep on a terminated execution")
			writes := f.st.stepWrites
			resp, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: "x-in-progress", OutcomeID: "done"})
			if err != nil || resp.Status != domain.StepStatusSkipped {
				t.Fatalf("late submit (TerminateStepsOnCancel only) = %+v, %v; want status skipped, nil", resp, err)
			}
			if f.st.stepWrites != writes {
				t.Fatalf("late submit wrote %d executions", f.st.stepWrites-writes)
			}
		})
		t.Run(p.name+"/zero value", func(t *testing.T) {
			f := newGuardFixture(p.runStatus, StepEntryPolicy{}, terminationExecs()...)
			before := map[string]domain.StepExecution{}
			for _, id := range liveTerminationIDs {
				before[id] = *execByID(f.st.mockRunStore, id)
			}
			if err := p.end(ctx, f.orch, f.st.runs["run-1"]); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			for _, id := range liveTerminationIDs {
				if got := *execByID(f.st.mockRunStore, id); got.Status != before[id].Status || got.ErrorDetail != nil {
					t.Fatalf("zero value: %s changed to %s %+v", id, got.Status, got.ErrorDetail)
				}
			}
		})
	}
}

// TestStepEntry_TerminateWithoutRunStatusWarns: TerminateStepsOnCancel is
// only safe together with EnforceRunStatus (termination races a blind
// bind write until TASK-008's CAS), so installing it alone logs one WARN.
func TestStepEntry_TerminateWithoutRunStatusWarns(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o := &Orchestrator{}
	o.WithStepEntryPolicy(StepEntryPolicy{TerminateStepsOnCancel: true})
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("expected exactly one WARN for TerminateStepsOnCancel without EnforceRunStatus, got %d:\n%s", n, buf.String())
	}
	buf.Reset()
	o.WithStepEntryPolicy(StrictStepEntryPolicy())
	o.WithStepEntryPolicy(StepEntryPolicy{})
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("no WARN expected for strict or zero policies:\n%s", buf.String())
	}
}

func domainErr(msg string) error { return domain.NewError(domain.ErrGit, msg) }

// ── AC 24 ──

// TestStepEntry_CandidateListingHidesDeadRuns is the committed form of the
// design-check probe TestT007_CandidateListingOffersCancelledRunSteps.
// Mutation: deleting the run-status filter fails the enforced sub-test.
func TestStepEntry_CandidateListingHidesDeadRuns(t *testing.T) {
	wf := &domain.WorkflowDefinition{ID: "wf", EntryStep: "s", Steps: []domain.StepDefinition{{ID: "s", Name: "S", Type: domain.StepTypeManual}}}
	statuses := []domain.RunStatus{domain.RunStatusActive, domain.RunStatusCancelled, domain.RunStatusFailed,
		domain.RunStatusCompleted, domain.RunStatusCommitting, domain.RunStatusPaused}
	newStore := func() *stepListingBlockingStore {
		bs := &stepListingBlockingStore{runs: map[string]*domain.Run{}}
		for _, s := range statuses {
			runID := "run-" + string(s)
			bs.runs[runID] = &domain.Run{RunID: runID, TaskPath: "t.md", Status: s}
			bs.execs = append(bs.execs, domain.StepExecution{ExecutionID: runID + "-s-1", RunID: runID, StepID: "s",
				Status: domain.StepStatusWaiting, Attempt: 1})
		}
		return bs
	}
	ctx := context.Background()

	o := newStepListingOrch(newStore(), wf)
	o.WithStepEntryPolicy(StepEntryPolicy{EnforceRunStatus: true})
	items, err := o.ListStepExecutions(ctx, StepExecutionQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RunID != "run-active" {
		t.Fatalf("EnforceRunStatus: listing = %+v, want only the active run's execution", items)
	}

	o = newStepListingOrch(newStore(), wf)
	items, err = o.ListStepExecutions(ctx, StepExecutionQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(statuses) {
		t.Fatalf("zero value: listing returned %d executions, want %d (dead runs offered as today)", len(items), len(statuses))
	}
}

// ── AC 25 ──

// TestStepEntry_LateSubmitOnTerminatedExecution is the committed form of
// the design-check probe TestT007_LateSubmitOnTerminatedExecutionIsSuccess.
// Mutation: routing the run-terminated case through the idempotent branch
// fails the enforced sub-test.
func TestStepEntry_LateSubmitOnTerminatedExecution(t *testing.T) {
	now := time.Now()
	terminated := func() *domain.StepExecution {
		return &domain.StepExecution{ExecutionID: "run-1-manual-1", RunID: "run-1", StepID: "manual",
			Status: domain.StepStatusSkipped, ActorID: "runner-1", Attempt: 1, CreatedAt: now, CompletedAt: &now,
			ErrorDetail: &domain.ErrorDetail{Classification: domain.FailureRunTerminated, Message: "run cancelled"}}
	}
	ctx := context.Background()

	t.Run("enforced: terminated execution answers conflict", func(t *testing.T) {
		f := newGuardFixture(domain.RunStatusCancelled, StepEntryPolicy{EnforceRunStatus: true}, terminated())
		_, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: "run-1-manual-1", OutcomeID: "done"})
		requireCode(t, err, domain.ErrConflict, "late submit on a run-terminated execution")
		if !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("refusal %q must name the run status", err.Error())
		}
		if f.st.stepWrites != 0 {
			t.Fatalf("late submit wrote %d executions", f.st.stepWrites)
		}
	})
	t.Run("enforced: duplicate submit of a completed execution stays idempotent", func(t *testing.T) {
		done := &domain.StepExecution{ExecutionID: "run-1-manual-1", RunID: "run-1", StepID: "manual",
			Status: domain.StepStatusCompleted, OutcomeID: "done", ActorID: "worker", Attempt: 1, CreatedAt: now, CompletedAt: &now}
		f := newGuardFixture(domain.RunStatusCompleted, StepEntryPolicy{EnforceRunStatus: true}, done)
		resp, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: "run-1-manual-1", OutcomeID: "done"})
		if err != nil || resp.Status != domain.StepStatusCompleted {
			t.Fatalf("duplicate submit = %+v, %v; want idempotent completed, nil", resp, err)
		}
		if f.st.stepWrites != 0 {
			t.Fatalf("duplicate submit wrote %d executions", f.st.stepWrites)
		}
	})
	t.Run("enforced: a workflow-skipped execution stays idempotent", func(t *testing.T) {
		skipped := terminated()
		skipped.ErrorDetail = nil
		f := newGuardFixture(domain.RunStatusActive, StepEntryPolicy{EnforceRunStatus: true}, skipped)
		resp, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: "run-1-manual-1", OutcomeID: "done"})
		if err != nil || resp.Status != domain.StepStatusSkipped {
			t.Fatalf("submit on a skipped (not run-terminated) execution = %+v, %v", resp, err)
		}
	})
	t.Run("zero value: terminated execution answers skipped as today", func(t *testing.T) {
		f := newGuardFixture(domain.RunStatusCancelled, StepEntryPolicy{}, terminated())
		resp, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: "run-1-manual-1", OutcomeID: "done"})
		if err != nil || resp.Status != domain.StepStatusSkipped {
			t.Fatalf("zero value late submit = %+v, %v; want status skipped, nil", resp, err)
		}
	})
}
