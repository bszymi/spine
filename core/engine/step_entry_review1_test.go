package engine

// INIT-023 TASK-007 review round 1 — regression tests for the reviewers'
// findings. Each test names the finding it closes and the mutation that
// turns it red.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// ── Finding: a dead run answers 409 on assign, not 404 ──

// TestStepEntry_AssignOnTerminatedRunAnswersConflict: under the strict
// policy, a run that ended with TerminateStepsOnCancel has no live execution
// left, and AssignStep must still give the documented 409 naming the run
// status ("run R is cancelled; a step can only be assigned on an active
// run"), not 404 "no active execution for step".
// Mutation: looking the execution up before requireActiveRun in AssignStep
// fails it (ErrNotFound).
func TestStepEntry_AssignOnTerminatedRunAnswersConflict(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	for _, end := range []struct {
		name   string
		do     func(context.Context, *Orchestrator) error
		status string
	}{
		{"CancelRun", func(ctx context.Context, o *Orchestrator) error { return o.CancelRun(ctx, "run-1") }, "cancelled"},
		{"FailRun", func(ctx context.Context, o *Orchestrator) error { return o.FailRun(ctx, "run-1", "retries exhausted") }, "failed"},
	} {
		end := end
		t.Run(end.name, func(t *testing.T) {
			f := newGuardFixture(domain.RunStatusActive, StrictStepEntryPolicy(),
				&domain.StepExecution{ExecutionID: "run-1-manual-1", RunID: "run-1", StepID: "manual",
					Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()})
			if err := end.do(ctx, f.orch); err != nil {
				t.Fatalf("%s: %v", end.name, err)
			}
			if e := execByID(f.st.mockRunStore, "run-1-manual-1"); !isRunTerminated(e) {
				t.Fatalf("precondition: the waiting execution must be terminated with the run, got %s %+v", e.Status, e.ErrorDetail)
			}
			before := snapshotExecs(f.st.mockRunStore)
			writes := f.st.stepWrites

			_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "manual", ActorID: "worker"})
			requireCode(t, err, domain.ErrConflict, "AssignStep on a "+end.status+" run")
			for _, want := range []string{end.status, "can only be assigned on an active run"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q must contain %q", err.Error(), want)
				}
			}
			requireExecsUnchanged(t, f.st.mockRunStore, before, "refused assign")
			if f.st.stepWrites != writes {
				t.Fatalf("refused assign wrote %d executions", f.st.stepWrites-writes)
			}
		})
	}
}

// ── Finding: pollers are not offered what SoD would refuse ──

// TestStepEntry_CandidateListingHidesSoDRefusals mirrors D16 (the run-status
// listing filter) for separation of duties: a poller is not offered a
// waiting review of a run it authored, nor an author step of a run whose
// review it holds, because every claim would answer 403 again on the next
// poll. Only a definite refusal hides a candidate; a check that cannot run
// leaves it listed (the claim answers the fail-closed 503).
// Mutation: deleting the SoD filter in ListStepExecutions fails the
// enforced rows.
func TestStepEntry_CandidateListingHidesSoDRefusals(t *testing.T) {
	ctx := context.Background()
	mk := func(id, runID, stepID string, status domain.StepExecutionStatus, actorID string) *domain.StepExecution {
		return &domain.StepExecution{ExecutionID: id, RunID: runID, StepID: stepID, Status: status,
			ActorID: actorID, Attempt: 1, CreatedAt: time.Now()}
	}
	// run-1: member-1 executed; the review waits.
	// run-2: member-2 completed execute after carol claimed and released it
	//        (carol survives only in the ledger); the review waits.
	// run-3: erin holds the live review while a branch step waits.
	stored := []*domain.StepExecution{
		mk("run-1-execute-1", "run-1", "execute", domain.StepStatusCompleted, "member-1"),
		mk("run-1-review-1", "run-1", "review", domain.StepStatusWaiting, ""),
		mk("run-2-execute-1", "run-2", "execute", domain.StepStatusCompleted, "member-2"),
		mk("run-2-review-1", "run-2", "review", domain.StepStatusWaiting, ""),
		mk("run-3-execute-1", "run-3", "execute", domain.StepStatusCompleted, "member-3"),
		mk("run-3-review-1", "run-3", "review", domain.StepStatusInProgress, "erin"),
		mk("run-3-b1-branch-work-1", "run-3", "branch-work", domain.StepStatusWaiting, ""),
	}
	ledger := func() AssignmentStore {
		m := newMemAssignmentStore()
		m.assignments["claim-run-2-execute-1-carol"] = &domain.Assignment{AssignmentID: "claim-run-2-execute-1-carol",
			RunID: "run-2", ExecutionID: "run-2-execute-1", ActorID: "carol",
			Status: domain.AssignmentStatusCancelled, AssignedAt: time.Now()}
		return m
	}
	noLedger := func() AssignmentStore { return historylessAssignments{inner: newMemAssignmentStore()} }
	newOrch := func(p StepEntryPolicy, as AssignmentStore) *Orchestrator {
		st := &mockRunStore{runs: map[string]*domain.Run{}}
		for _, id := range []string{"run-1", "run-2", "run-3"} {
			st.runs[id] = runFixture(id, domain.RunStatusActive)
		}
		bs := &stepListingBlockingStore{runs: st.runs}
		for _, e := range stored {
			cp := *e
			st.createdSteps = append(st.createdSteps, &cp)
			if !cp.Status.IsTerminal() {
				bs.execs = append(bs.execs, cp)
			}
		}
		o := newStepListingOrch(bs, sodWorkflow())
		o.store = st
		o.assignments = as
		o.WithStepEntryPolicy(p)
		return o
	}
	sod := StepEntryPolicy{SeparationOfDuties: true}
	everything := []string{"run-1-review-1", "run-2-review-1", "run-3-b1-branch-work-1"}
	cases := []struct {
		name   string
		policy StepEntryPolicy
		ledger func() AssignmentStore
		actor  string
		want   []string
	}{
		{"run author is not offered its run's review", sod, ledger, "member-1",
			[]string{"run-2-review-1", "run-3-b1-branch-work-1"}},
		{"released executor (ledger) is not offered its run's review", sod, ledger, "carol",
			[]string{"run-1-review-1", "run-3-b1-branch-work-1"}},
		{"live reviewer is not offered the run's author step", sod, ledger, "erin",
			[]string{"run-1-review-1", "run-2-review-1", "run-3-review-1"}},
		{"independent actor is offered everything", sod, ledger, "dana", everything},
		{"an unreadable ledger leaves the candidate listed", sod, noLedger, "carol", everything},
		{"zero value offers the author its review as today", StepEntryPolicy{}, ledger, "member-1", everything},
		{"zero value offers the live reviewer the branch step as today", StepEntryPolicy{}, ledger, "erin",
			[]string{"run-1-review-1", "run-2-review-1", "run-3-b1-branch-work-1", "run-3-review-1"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			o := newOrch(c.policy, c.ledger())
			items, err := o.ListStepExecutions(ctx, StepExecutionQuery{ActorID: c.actor, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range items {
				got = append(got, it.ExecutionID)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("listing for %s = %v, want %v", c.actor, got, c.want)
			}
		})
	}
}

// ── Finding: concurrent binds of one actor into one run ──

// raceStore is a goroutine-safe run store whose reads return copies (as a
// database does) and whose UpdateStepExecution pauses once on one execution,
// holding that bind between its checks and its write.
type raceStore struct {
	*mockRunStore
	mu      sync.Mutex
	pauseOn string
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func newRaceStore(pauseOn string, execs ...*domain.StepExecution) *raceStore {
	st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
	st.createdSteps = execs
	return &raceStore{mockRunStore: st, pauseOn: pauseOn, reached: make(chan struct{}), release: make(chan struct{})}
}

func (s *raceStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.mockRunStore.GetRun(ctx, runID)
	if r == nil {
		return nil, err
	}
	cp := *r
	return &cp, err
}

func (s *raceStore) GetStepExecution(ctx context.Context, id string) (*domain.StepExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.mockRunStore.GetStepExecution(ctx, id)
	if e == nil {
		return nil, err
	}
	cp := *e
	return &cp, err
}

func (s *raceStore) ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mockRunStore.ListStepExecutionsByRun(ctx, runID)
}

func (s *raceStore) UpdateStepExecution(ctx context.Context, e *domain.StepExecution) error {
	if e.ExecutionID == s.pauseOn {
		s.once.Do(func() {
			close(s.reached)
			<-s.release
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *e
	return s.mockRunStore.UpdateStepExecution(ctx, &cp)
}

func (s *raceStore) boundTo(actorID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.createdSteps {
		if e.ActorID == actorID && !e.Status.IsTerminal() {
			out = append(out, e.StepID)
		}
	}
	sort.Strings(out)
	return out
}

// syncAssignments serialises a memAssignmentStore (including its ledger).
type syncAssignments struct {
	mu    sync.Mutex
	inner *memAssignmentStore
}

func (s *syncAssignments) CreateAssignment(ctx context.Context, a *domain.Assignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.CreateAssignment(ctx, a)
}

func (s *syncAssignments) UpdateAssignmentStatus(ctx context.Context, id string, st domain.AssignmentStatus, at *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.UpdateAssignmentStatus(ctx, id, st, at)
}

func (s *syncAssignments) GetAssignment(ctx context.Context, id string) (*domain.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.GetAssignment(ctx, id)
}

func (s *syncAssignments) ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.ListExpiredAssignments(ctx, before)
}

func (s *syncAssignments) ListAssignmentsByActor(ctx context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.ListAssignmentsByActor(ctx, actorID, status)
}

// syncEmitter is a goroutine-safe event sink.
type syncEmitter struct {
	mu     sync.Mutex
	events []domain.Event
}

func (e *syncEmitter) Emit(_ context.Context, ev domain.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

// TestStepEntry_SoDBindsSerialisePerRun: in a divergence workflow a review
// and an author branch step of one run are live together. Two concurrent
// binds of the same actor — one to each — must not both pass separation of
// duties: the first bind is held between its checks and its write, and the
// second must wait for it and then be refused (403), whichever path either
// takes — AssignStep, ClaimStep or ActivateStep's auto-selection.
// Mutation: making lockSoDBind take no lock (or dropping it from any one
// path) lets the second bind pass its check during the pause and complete,
// and the actor ends up bound to both. Since review round 2 the second bind
// runs its checks without the stripe and waits for it only to re-run
// check 7 and write (sodBindSection): dropping that re-check, or running it
// before the stripe is taken, fails every case the same way.
func TestStepEntry_SoDBindsSerialisePerRun(t *testing.T) {
	ctx := context.Background()
	const review, branch, aiReview = "run-1-review-1", "run-1-b1-branch-work-1", "run-1-ai-review-1"
	type op func(context.Context, *Orchestrator) error
	claim := func(execID string) op {
		return func(ctx context.Context, o *Orchestrator) error {
			_, err := o.ClaimStep(ctx, ClaimRequest{ActorID: "erin", ExecutionID: execID})
			return err
		}
	}
	assign := func(stepID string) op {
		return func(ctx context.Context, o *Orchestrator) error {
			_, err := o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: stepID, ActorID: "erin"})
			return err
		}
	}
	// Each case is discriminating: the first bind leaves no trace the
	// second's check could see until it writes (a claim's ledger row is
	// written before the pause, so "claim the branch step first" is not a
	// case here — the ledger already refuses the review).
	cases := []struct {
		name      string
		first     op
		firstExec string
		second    op
		wantBound string
	}{
		{"claim review, then claim branch step", claim(review), review, claim(branch), "review"},
		{"assign review, then claim branch step", assign("review"), review, claim(branch), "review"},
		{"assign branch step, then assign review", assign("branch-work"), branch, assign("review"), "branch-work"},
		{"auto-select for the AI review, then claim branch step", func(ctx context.Context, o *Orchestrator) error {
			return o.ActivateStep(ctx, "run-1", "ai-review")
		}, aiReview, claim(branch), "ai-review"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			st := newRaceStore(c.firstExec,
				sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
				sodExec(review, "review", domain.StepStatusWaiting, ""),
				&domain.StepExecution{ExecutionID: branch, RunID: "run-1", StepID: "branch-work", BranchID: "b1",
					Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()},
				sodExec(aiReview, "ai-review", domain.StepStatusWaiting, ""))
			o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: sodWorkflow()}, events: &syncEmitter{},
				git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: newClaimBlockingStore(),
				artifacts: &stubArtifactReader{}, assignments: &syncAssignments{inner: newMemAssignmentStore()},
				actorSelector: &mockActorSelector{actor: activeActor("erin", domain.ActorTypeAIAgent)}}
			o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})

			firstDone := make(chan error, 1)
			go func() { firstDone <- c.first(ctx, o) }()
			select {
			case <-st.reached:
			case <-time.After(10 * time.Second):
				t.Fatal("the first bind never reached its write")
			}
			secondDone := make(chan error, 1)
			go func() { secondDone <- c.second(ctx, o) }()
			select {
			case err := <-secondDone:
				close(st.release)
				<-firstDone
				t.Fatalf("the second bind finished (err=%v) while the first was between its check and its write: binds of one run are not serialised; erin bound to %v",
					err, st.boundTo("erin"))
			case <-time.After(300 * time.Millisecond):
			}
			close(st.release)
			if err := <-firstDone; err != nil {
				t.Fatalf("first bind: %v", err)
			}
			err := <-secondDone
			requireCode(t, err, domain.ErrForbidden, "second bind of the same actor into the run")
			if !strings.Contains(err.Error(), "separation of duties") {
				t.Fatalf("refusal %q must be the separation-of-duties one", err.Error())
			}
			if got := st.boundTo("erin"); len(got) != 1 || got[0] != c.wantBound {
				t.Fatalf("erin is bound to %v, want only %q", got, c.wantBound)
			}
		})
	}
}
