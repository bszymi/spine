package engine

// Shared fixtures for the INIT-023 TASK-007 step-entry policy tests.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
)

// ListAssignmentsByActor makes memAssignmentStore the engine's optional
// assignmentHistory port (the SoD author ledger). Every status is
// returned, newest first, like the Postgres store.
func (s *memAssignmentStore) ListAssignmentsByActor(_ context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error) {
	var out []domain.Assignment
	for _, a := range s.assignments {
		if a.ActorID != actorID {
			continue
		}
		if status != nil && a.Status != *status {
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssignedAt.After(out[j].AssignedAt) })
	return out, nil
}

// historylessAssignments is an AssignmentStore that does NOT offer
// ListAssignmentsByActor: under SeparationOfDuties the engine must fail
// closed rather than treat "no ledger" as "no author".
type historylessAssignments struct{ inner *memAssignmentStore }

func (h historylessAssignments) CreateAssignment(ctx context.Context, a *domain.Assignment) error {
	return h.inner.CreateAssignment(ctx, a)
}
func (h historylessAssignments) UpdateAssignmentStatus(ctx context.Context, id string, st domain.AssignmentStatus, at *time.Time) error {
	return h.inner.UpdateAssignmentStatus(ctx, id, st, at)
}
func (h historylessAssignments) GetAssignment(ctx context.Context, id string) (*domain.Assignment, error) {
	return h.inner.GetAssignment(ctx, id)
}
func (h historylessAssignments) ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error) {
	return h.inner.ListExpiredAssignments(ctx, before)
}

// failingHistoryAssignments offers the ledger but every read fails.
type failingHistoryAssignments struct{ *memAssignmentStore }

func (f failingHistoryAssignments) ListAssignmentsByActor(context.Context, string, *domain.AssignmentStatus) ([]domain.Assignment, error) {
	return nil, errors.New("assignment ledger unreachable")
}

// cancellingAssignments records CancelActiveAssignmentsForExecution calls
// (the optional activeAssignmentCanceller port).
type cancellingAssignments struct {
	*memAssignmentStore
	cancelled []string
}

func (c *cancellingAssignments) CancelActiveAssignmentsForExecution(_ context.Context, executionID string, at time.Time) (int, error) {
	c.cancelled = append(c.cancelled, executionID)
	n := 0
	for _, a := range c.assignments {
		if a.ExecutionID == executionID && a.Status == domain.AssignmentStatusActive {
			a.Status = domain.AssignmentStatusCancelled
			t := at
			a.RespondedAt = &t
			n++
		}
	}
	return n, nil
}

// countingActorAssigner records every DeliverAssignment (runner dispatch).
type countingActorAssigner struct {
	delivered []actor.AssignmentRequest
}

func (c *countingActorAssigner) DeliverAssignment(_ context.Context, req actor.AssignmentRequest) error {
	c.delivered = append(c.delivered, req)
	return nil
}

func (c *countingActorAssigner) ProcessResult(context.Context, actor.AssignmentRequest, actor.AssignmentResult) error {
	return nil
}

// writeCountingRunStore counts step-execution writes so tests can assert
// that a refused call writes nothing.
type writeCountingRunStore struct {
	*mockRunStore
	stepWrites   int
	stepCreates  int
	statusWrites int
}

func (w *writeCountingRunStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	w.stepWrites++
	return w.mockRunStore.UpdateStepExecution(ctx, exec)
}

func (w *writeCountingRunStore) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	w.stepCreates++
	return w.mockRunStore.CreateStepExecution(ctx, exec)
}

func (w *writeCountingRunStore) UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error {
	w.statusWrites++
	return w.mockRunStore.UpdateRunStatus(ctx, runID, status)
}

// fakeDiscussions is a DiscussionChecker with a switchable answer.
type fakeDiscussions struct{ open bool }

func (f *fakeDiscussions) HasOpenThreads(context.Context, domain.AnchorType, string) (bool, error) {
	return f.open, nil
}

// fakeEntryResolver is a policy Actors resolver driven by a function.
type fakeEntryResolver func(ctx context.Context, actorID string) (*domain.Actor, error)

func (f fakeEntryResolver) ResolveEntryActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	return f(ctx, actorID)
}

// smpHumanResolver answers SMP's `human-<uuid>` handles as active humans
// (who hold no skills) and everything else through the blocking store —
// the shape SMP TASK-011 wires.
func smpHumanResolver(bs *claimBlockingStore) EntryActorResolver {
	return fakeEntryResolver(func(ctx context.Context, actorID string) (*domain.Actor, error) {
		if len(actorID) > len("human-") && actorID[:len("human-")] == "human-" {
			return &domain.Actor{ActorID: actorID, Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}, nil
		}
		if bs != nil {
			return bs.GetActor(ctx, actorID)
		}
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	})
}

// smpShapedPolicy is every check except RequireSkillsOnAssign, plus the
// human-handle resolver (SMP TASK-011's intended adoption).
func smpShapedPolicy(bs *claimBlockingStore) StepEntryPolicy {
	p := StrictStepEntryPolicy()
	p.RequireSkillsOnAssign = false
	p.Actors = smpHumanResolver(bs)
	return p
}

// errCode returns the domain error code carried anywhere in err's chain.
func errCode(err error) domain.ErrorCode {
	var se *domain.SpineError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// requireCode fails the test unless err carries want.
func requireCode(t *testing.T, err error, want domain.ErrorCode, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected %s, got nil error", what, want)
	}
	if got := errCode(err); got != want {
		t.Fatalf("%s: expected %s, got %q (%v)", what, want, got, err)
	}
}

// snapshotExecs copies every execution held by the store, for
// "nothing changed" comparisons.
func snapshotExecs(st *mockRunStore) []domain.StepExecution {
	out := make([]domain.StepExecution, 0, len(st.createdSteps))
	for _, e := range st.createdSteps {
		cp := *e
		if e.EligibleActorIDs != nil {
			cp.EligibleActorIDs = append([]string(nil), e.EligibleActorIDs...)
		}
		if e.ErrorDetail != nil {
			ed := *e.ErrorDetail
			cp.ErrorDetail = &ed
		}
		out = append(out, cp)
	}
	return out
}

func requireExecsUnchanged(t *testing.T, st *mockRunStore, before []domain.StepExecution, what string) {
	t.Helper()
	after := snapshotExecs(st)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s: executions changed\nbefore: %+v\nafter:  %+v", what, before, after)
	}
}

func execByID(st *mockRunStore, id string) *domain.StepExecution {
	for _, e := range st.createdSteps {
		if e.ExecutionID == id {
			return e
		}
	}
	return nil
}

func activeActor(id string, typ domain.ActorType) *domain.Actor {
	return &domain.Actor{ActorID: id, Type: typ, Status: domain.ActorStatusActive}
}

func skillSet(names ...string) []domain.Skill {
	out := make([]domain.Skill, 0, len(names))
	for _, n := range names {
		out = append(out, domain.Skill{Name: n, Status: domain.SkillStatusActive})
	}
	return out
}

func runFixture(runID string, status domain.RunStatus) *domain.Run {
	return &domain.Run{RunID: runID, TaskPath: "tasks/t.md", WorkflowPath: "workflows/w.yaml",
		WorkflowVersion: "v1", Status: status, BranchName: "spine/run/" + runID,
		TraceID: fmt.Sprintf("trace-%s-0123456789", runID)}
}
