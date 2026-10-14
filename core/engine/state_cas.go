package engine

// Compare-and-swap engine state writes (INIT-023 TASK-008; audit P3-24).
//
// The defect. Every run-status mutator read the run, judged the trigger
// against that snapshot and wrote the result with the unconditional
// UpdateRunStatus; every step writer did the same with
// UpdateStepExecution. Two writers interleaving on one row therefore
// overwrote each other: a CancelRun that committed inside CompleteRun's
// read was resurrected (`cancelled -> committing`) and merged; a submit
// that raced the timeout scanner overwrote the failed step and routed its
// outcome; two merge attempts both advanced the publish step.
//
// The fix.
//
//   - Run writes (always on). transitionRun judges the trigger against the
//     status it read and writes with TransitionRunStatus(from = that
//     status), which every RunStore already implements (SMP's included).
//     On a lost race it re-reads and re-judges — at most runCASAttempts
//     times — so the trigger is always evaluated against the state that
//     won and never written over it: a cancel that raced a pause still
//     cancels, a cancel that raced a merge start answers the state
//     machine's 409 (run.cancel is invalid from committing). Events and
//     branch cleanup run only after an applied write.
//   - Step writes (opt-in through the store). writeStepExecution writes
//     through the optional StepExecutionTransitioner port when the store
//     implements it — conditional on the row still holding the status and
//     actor the caller read — and through the blind UpdateStepExecution
//     otherwise, exactly as before. A miss writes nothing and returns
//     ErrConflict wrapping ErrStepStateChanged; every caller stops without
//     side effects (no event, no routing, no assignment delivery, no retry
//     execution). The standalone PostgresStore implements the port; SMP's
//     spinecore.RunStore opts in in SMP TASK-011 (owner question 3).
//
// (status, actor) and not status alone: a release (back to waiting)
// followed by an assign to B restores assigned/in_progress, so a stale
// write from A would match a status-only expectation. The actor closes
// that with no schema change; an ABA by the same actor is accepted
// (Decision D2). A terminal no-op transition (completed -> completed)
// leaves both unchanged, which is why SubmitStepResult refuses an
// execution that is terminal when read instead of relying on the CAS
// (§3a, terminalSubmitConflict).

import (
	"context"
	"errors"
	"fmt"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// ErrRunStateChanged is the cause of the ErrConflict transitionRun returns
// when every attempt lost the race to another run writer.
var ErrRunStateChanged = errors.New("run changed state concurrently")

// ErrStepStateChanged is the cause of the ErrConflict a step write (or a
// submit) returns when the execution is no longer in the state the caller
// read: another writer won, or the execution was already terminal.
var ErrStepStateChanged = errors.New("step execution changed state concurrently")

// runCASAttempts bounds transitionRun's re-read/re-evaluate loop against a
// livelock between writers that keep moving one run.
const runCASAttempts = 3

// transitionRun reads the run, evaluates req against its CURRENT status
// and writes the result with TransitionRunStatus(from = the status it
// read). On a lost race (applied == false) it re-reads and re-evaluates,
// at most runCASAttempts times, so the trigger is always judged against
// the state that won and never written over it.
//
// It returns the run as read on the winning attempt, the status it
// transitioned from and the transition. `from` is returned separately
// because a fake store may alias the stored row (GetRun returns its own
// pointer), so after the write run.Status may already hold the new
// status.
//
// Errors: a GetRun error is wrapped with %w ("get run: ..."; the embed
// smoketest relies on errors.Is through CancelRun); a failed evaluation
// (for example the run is now cancelled) is the state machine's own
// ErrConflict — the answer a sequential caller gets; a TransitionRunStatus
// error is wrapped and returned at once, never retried (SMP's store
// answers a missing row with an error, not with (false, nil)); losing
// every attempt is ErrConflict wrapping ErrRunStateChanged.
func (o *Orchestrator) transitionRun(ctx context.Context, runID string, req workflow.TransitionRequest) (*domain.Run, domain.RunStatus, *workflow.TransitionResult, error) {
	var lastFrom domain.RunStatus
	for attempt := 1; attempt <= runCASAttempts; attempt++ {
		run, err := o.store.GetRun(ctx, runID)
		if err != nil {
			return nil, "", nil, fmt.Errorf("get run: %w", err)
		}
		from := run.Status
		result, err := workflow.EvaluateRunTransition(from, req)
		if err != nil {
			return nil, "", nil, err
		}
		applied, err := o.store.TransitionRunStatus(ctx, runID, from, result.ToStatus)
		if err != nil {
			return nil, "", nil, fmt.Errorf("update run status: %w", err)
		}
		if applied {
			return run, from, result, nil
		}
		lastFrom = from
		observe.Logger(ctx).Info("run status write lost a race; re-reading",
			"run_id", runID, "trigger", req.Trigger, "from", from, "attempt", attempt)
	}
	return nil, "", nil, domain.NewErrorWithCause(domain.ErrConflict,
		fmt.Sprintf("run %s changed state concurrently (last read %s); %s was not applied", runID, lastFrom, req.Trigger),
		ErrRunStateChanged)
}

// StepExecutionTransitioner is an OPTIONAL RunStore capability (INIT-023
// TASK-008). When the store implements it, every engine step write is
// conditional on the row still holding the status and actor the caller
// read. Stores without it — SMP's spinecore.RunStore until SMP TASK-011,
// cmd/embed-smoketest and the test fakes — keep the blind
// UpdateStepExecution. Detected by type assertion only, so no interface an
// embedder implements gains a method. The standalone *store.PostgresStore
// implements it (asserted at compile time in service/cmd/spine).
type StepExecutionTransitioner interface {
	// TransitionStepExecution writes exec with exactly UpdateStepExecution's
	// column set, only while the row's status equals fromStatus and its
	// actor equals fromActorID (NULL and "" both meaning "no actor").
	// (true, nil) = applied; (false, nil) = the row exists but moved on;
	// a domain ErrNotFound = no such row.
	TransitionStepExecution(ctx context.Context, exec *domain.StepExecution, fromStatus domain.StepExecutionStatus, fromActorID string) (bool, error)
}

// stepState is the (status, actor) expectation a conditional step write
// is judged against: what the caller read before deciding on the write.
type stepState struct {
	status  domain.StepExecutionStatus
	actorID string
}

// stateOf captures e's current (status, actor). Callers take it right
// after their read, before mutating e.
func stateOf(e *domain.StepExecution) stepState {
	return stepState{status: e.Status, actorID: e.ActorID}
}

// stepTransitioner returns the store's conditional-write capability, or
// nil when the store has none.
func (o *Orchestrator) stepTransitioner() StepExecutionTransitioner {
	t, _ := o.store.(StepExecutionTransitioner)
	return t
}

// writeStepExecution persists exec, which the caller read as `prior`.
// With the capability the write is conditional and a miss writes nothing
// and returns ErrConflict wrapping ErrStepStateChanged (callers test it
// with isStepStateChanged and stop without side effects). Without it this
// is o.store.UpdateStepExecution, unchanged. It is the only engine call
// site of UpdateStepExecution.
func (o *Orchestrator) writeStepExecution(ctx context.Context, exec *domain.StepExecution, prior stepState) error {
	t := o.stepTransitioner()
	if t == nil {
		return o.store.UpdateStepExecution(ctx, exec)
	}
	applied, err := t.TransitionStepExecution(ctx, exec, prior.status, prior.actorID)
	if err != nil {
		return err
	}
	if !applied {
		return stepStateChanged(exec.ExecutionID,
			fmt.Sprintf("it is no longer %s for %q", prior.status, prior.actorID))
	}
	return nil
}

// stepStateChanged builds the ErrConflict a lost step race (or a submit on
// an execution that is already terminal) returns.
func stepStateChanged(executionID, why string) error {
	return domain.NewErrorWithCause(domain.ErrConflict,
		fmt.Sprintf("step execution %s changed state concurrently: %s; nothing was written", executionID, why),
		ErrStepStateChanged)
}

// isStepStateChanged reports whether err is a lost step race.
func isStepStateChanged(err error) bool { return errors.Is(err, ErrStepStateChanged) }
