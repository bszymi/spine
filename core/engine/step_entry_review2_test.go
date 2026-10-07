package engine

// INIT-023 TASK-007 review round 2 — regression tests for the scope of the
// separation-of-duties bind lock. TestStepEntry_SoDBindsSerialisePerRun
// (review round 1) still proves the lock closes the race; these prove it
// covers nothing else, and that a waiter honours its context.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// sameStripeRun returns a run ID other than runID that hashes to runID's
// bind stripe, so a test can show two unrelated runs do not wait on each
// other's slow reads.
func sameStripeRun(t *testing.T, runID string) string {
	t.Helper()
	want := sodStripe(runID)
	for i := 0; i < 100000; i++ {
		id := fmt.Sprintf("run-b-%d", i)
		if id != runID && sodStripe(id) == want {
			return id
		}
	}
	t.Fatalf("no run ID shares %s's stripe", runID)
	return ""
}

// pauseGate holds the first call whose key matches until open() is called.
// A nil gate never pauses.
type pauseGate struct {
	match     string
	once      sync.Once
	reached   chan struct{}
	release   chan struct{}
	closeOnce sync.Once
}

func newPauseGate(match string) *pauseGate {
	return &pauseGate{match: match, reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *pauseGate) at(key string) {
	if g == nil || key != g.match {
		return
	}
	g.once.Do(func() {
		close(g.reached)
		<-g.release
	})
}

func (g *pauseGate) open() { g.closeOnce.Do(func() { close(g.release) }) }

// pausingWFLoader stands in for GitWorkflowLoader's `git show`: it pauses
// on one workflow version.
type pausingWFLoader struct {
	wf   *domain.WorkflowDefinition
	gate *pauseGate
}

func (l *pausingWFLoader) LoadWorkflow(_ context.Context, _, version string) (*domain.WorkflowDefinition, error) {
	l.gate.at(version)
	return l.wf, nil
}

// pausingArtifacts stands in for the precondition evaluator's git read: it
// pauses on one artifact path and otherwise answers a Pending artifact.
type pausingArtifacts struct{ gate *pauseGate }

func (r *pausingArtifacts) Read(_ context.Context, path, _ string) (*domain.Artifact, error) {
	r.gate.at(path)
	return &domain.Artifact{Path: path, Status: domain.StatusPending}, nil
}

// pausingResolver stands in for an embedder's resolver query (SMP's
// workspace_roles lookup): a canonical fixed-table resolver that pauses on
// one actor ID.
type pausingResolver struct {
	gate  *pauseGate
	known map[string]bool
}

func (r *pausingResolver) ResolveEntryActor(_ context.Context, id string) (*domain.Actor, error) {
	r.gate.at(id)
	if !r.known[id] {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return activeActor(id, domain.ActorTypeHuman), nil
}

// lockScopeWorkflow: execute (manual) → review (review). The review carries
// an artifact_status precondition on the run's task, so a bind of it makes
// a precondition read that a test can pause.
func lockScopeWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "lock-scope", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Preconditions: []domain.Precondition{{Type: "artifact_status",
					Config: map[string]string{"status": string(domain.StatusPending)}}},
				Outcomes: []domain.OutcomeDefinition{{ID: "accepted", Name: "Accepted", NextStep: "end"}}},
		},
	}
}

// execActor reads one execution's status and actor under the store's lock.
func (s *raceStore) execActor(id string) (domain.StepExecutionStatus, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.createdSteps {
		if e.ExecutionID == id {
			return e.Status, e.ActorID
		}
	}
	return "", ""
}

// TestStepEntry_SoDBindLockCoversOnlyCheckAndWrite: under the strict policy
// (SeparationOfDuties on) a bind pauses in one of its slow reads — the
// workflow load (`git show`), the embedder's actor resolver, or a
// precondition's git read — on run A. A bind into an unrelated run B whose
// ID hashes to the same stripe must not wait for it: it is given a 2 s
// context and must bind before run A's read finishes. Run A's bind then
// completes normally.
// Mutation: taking the stripe before GetRun in AssignStep (round 1) fails
// every assign row; taking it before checkStepEntryActor or before the
// preconditions in ClaimStep fails the claim resolver and precondition rows
// (with sync.Mutex stripes the second bind times out the test; with
// context-aware stripes it gives up with 503).
func TestStepEntry_SoDBindLockCoversOnlyCheckAndWrite(t *testing.T) {
	ctx := context.Background()
	const runA = "run-a"
	runB := sameStripeRun(t, runA)

	type bindOp func(ctx context.Context, o *Orchestrator, runID, actorID string) error
	assignReview := func(ctx context.Context, o *Orchestrator, runID, actorID string) error {
		_, err := o.AssignStep(ctx, AssignRequest{RunID: runID, StepID: "review", ActorID: actorID})
		return err
	}
	claimReview := func(ctx context.Context, o *Orchestrator, runID, actorID string) error {
		_, err := o.ClaimStep(ctx, ClaimRequest{ActorID: actorID, ExecutionID: runID + "-review-1"})
		return err
	}
	const (
		pauseWorkflow     = "workflow load"
		pauseResolver     = "actor resolver"
		pausePrecondition = "precondition read"
	)
	cases := []struct {
		name  string
		pause string
		bind  bindOp
	}{
		{"assign paused loading the workflow", pauseWorkflow, assignReview},
		{"assign paused resolving the actor", pauseResolver, assignReview},
		{"assign paused reading a precondition", pausePrecondition, assignReview},
		// ClaimStep has always loaded the workflow before the stripe; the
		// row pins that it stays outside.
		{"claim paused loading the workflow", pauseWorkflow, claimReview},
		{"claim paused resolving the actor", pauseResolver, claimReview},
		{"claim paused reading a precondition", pausePrecondition, claimReview},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			ms := &mockRunStore{runs: map[string]*domain.Run{}}
			for _, id := range []string{runA, runB} {
				ms.runs[id] = runFixture(id, domain.RunStatusActive)
				ms.createdSteps = append(ms.createdSteps,
					&domain.StepExecution{ExecutionID: id + "-execute-1", RunID: id, StepID: "execute",
						Status: domain.StepStatusCompleted, ActorID: "member-1", Attempt: 1, CreatedAt: time.Now()},
					&domain.StepExecution{ExecutionID: id + "-review-1", RunID: id, StepID: "review",
						Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()})
			}
			// Run A's slow read is told apart by a key only run A uses.
			ms.runs[runA].WorkflowVersion = "slow"
			ms.runs[runA].TaskPath = "tasks/slow.md"
			st := &raceStore{mockRunStore: ms}

			var gate *pauseGate
			loader := &pausingWFLoader{wf: lockScopeWorkflow()}
			reader := &pausingArtifacts{}
			resolver := &pausingResolver{known: map[string]bool{"alice": true, "bob": true}}
			switch c.pause {
			case pauseWorkflow:
				gate = newPauseGate("slow")
				loader.gate = gate
			case pausePrecondition:
				gate = newPauseGate("tasks/slow.md")
				reader.gate = gate
			case pauseResolver:
				gate = newPauseGate("alice")
				resolver.gate = gate
			}
			t.Cleanup(gate.open)

			o := &Orchestrator{store: st, wfLoader: loader, events: &syncEmitter{},
				git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: newClaimBlockingStore(),
				artifacts: reader, assignments: &syncAssignments{inner: newMemAssignmentStore()}}
			p := StrictStepEntryPolicy()
			p.Actors = resolver
			o.WithStepEntryPolicy(p)

			firstDone := make(chan error, 1)
			go func() { firstDone <- c.bind(ctx, o, runA, "alice") }()
			select {
			case <-gate.reached:
			case <-time.After(10 * time.Second):
				t.Fatalf("run A's bind never reached its %s", c.pause)
			}

			sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			secondDone := make(chan error, 1)
			go func() { secondDone <- c.bind(sctx, o, runB, "bob") }()
			select {
			case err := <-secondDone:
				if err != nil {
					gate.open()
					<-firstDone
					t.Fatalf("run B's bind failed while run A's bind was paused in its %s: %v", c.pause, err)
				}
			case <-time.After(5 * time.Second):
				gate.open()
				<-firstDone
				<-secondDone
				t.Fatalf("run B's bind (same stripe) waited for run A's %s: the bind lock covers more than check 7 and the write", c.pause)
			}
			select {
			case err := <-firstDone:
				t.Fatalf("run A's bind finished (err=%v) before its %s was released", err, c.pause)
			default:
			}
			if status, actor := st.execActor(runB + "-review-1"); status != domain.StepStatusAssigned || actor != "bob" {
				t.Fatalf("run B's review = %s/%q, want assigned to bob", status, actor)
			}

			gate.open()
			if err := <-firstDone; err != nil {
				t.Fatalf("run A's bind after its %s: %v", c.pause, err)
			}
			if status, actor := st.execActor(runA + "-review-1"); status != domain.StepStatusAssigned || actor != "alice" {
				t.Fatalf("run A's review = %s/%q, want assigned to alice", status, actor)
			}
		})
	}
}

// TestStepEntry_SoDBindWaitHonoursContext: while the stripe of run-1 is
// held (standing in for another bind of a run on that stripe, mid-write), a
// bind whose context ends gives up with ErrUnavailable wrapping
// context.DeadlineExceeded, and nothing is written, recorded or delivered.
// Once the stripe is free the same bind succeeds. A zero-value orchestrator
// takes no stripe and binds while it is held.
// Mutation: making the stripe wait ignore ctx (a plain blocking send, i.e.
// sync.Mutex semantics) makes every enforced row wait past its deadline.
func TestStepEntry_SoDBindWaitHonoursContext(t *testing.T) {
	ctx := context.Background()
	type op func(context.Context, *Orchestrator) error
	cases := []struct {
		name      string
		bind      op
		execID    string
		wantActor string
	}{
		{"AssignStep", func(ctx context.Context, o *Orchestrator) error {
			_, err := o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "alice"})
			return err
		}, "run-1-review-1", "alice"},
		{"ClaimStep", func(ctx context.Context, o *Orchestrator) error {
			_, err := o.ClaimStep(ctx, ClaimRequest{ActorID: "alice", ExecutionID: "run-1-review-1"})
			return err
		}, "run-1-review-1", "alice"},
		{"ActivateStep auto-selection", func(ctx context.Context, o *Orchestrator) error {
			return o.ActivateStep(ctx, "run-1", "ai-review")
		}, "run-1-ai-review-1", "ai-1"},
	}
	for _, c := range cases {
		c := c
		for _, enforced := range []bool{true, false} {
			enforced := enforced
			name := c.name + "/SeparationOfDuties"
			p := StepEntryPolicy{SeparationOfDuties: true}
			if !enforced {
				name = c.name + "/zero value takes no stripe"
				p = StepEntryPolicy{}
			}
			t.Run(name, func(t *testing.T) {
				f := newSoDFixture(p,
					sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
					sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""),
					sodExec("run-1-ai-review-1", "ai-review", domain.StepStatusWaiting, ""))
				f.orch.actorSelector = &mockActorSelector{actor: activeActor("ai-1", domain.ActorTypeAIAgent)}

				// Hold run-1's stripe whatever the policy under test: an
				// enforcing orchestrator takes it.
				holder := &Orchestrator{}
				holder.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
				release, err := holder.lockSoDBind(ctx, "run-1")
				if err != nil {
					t.Fatalf("hold the stripe: %v", err)
				}
				t.Cleanup(release)

				before := snapshotExecs(f.st)
				cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- c.bind(cctx, f.orch) }()
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					release()
					<-done
					t.Fatal("the bind waited for the stripe past its context's deadline")
				}

				if !enforced {
					if err != nil {
						t.Fatalf("zero-value bind while the stripe is held: %v", err)
					}
					if e := execByID(f.st, c.execID); e.ActorID != c.wantActor {
						t.Fatalf("zero-value bind left %s with actor %q, want %q", c.execID, e.ActorID, c.wantActor)
					}
					return
				}

				requireCode(t, err, domain.ErrUnavailable, c.name+" while the run's stripe is held")
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("refusal %v must wrap context.DeadlineExceeded", err)
				}
				requireExecsUnchanged(t, f.st, before, "bind that gave up waiting")
				if n := len(f.as.assignments); n != 0 {
					t.Fatalf("bind that gave up waiting recorded %d assignment rows", n)
				}
				if n := len(f.gw.delivered); n != 0 {
					t.Fatalf("bind that gave up waiting delivered %d assignments", n)
				}

				release()
				if err := c.bind(ctx, f.orch); err != nil {
					t.Fatalf("%s once the stripe is free: %v", c.name, err)
				}
				if e := execByID(f.st, c.execID); e.ActorID != c.wantActor {
					t.Fatalf("%s bound %s to %q, want %q", c.name, c.execID, e.ActorID, c.wantActor)
				}
			})
		}
	}
}
