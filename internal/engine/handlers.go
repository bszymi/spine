package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// EngineMergeActorID is the stable actor identity recorded on step executions
// that the Spine engine advances directly (i.e. type: internal steps routed
// through the merge handler). Auditing and reporting surfaces can match on
// this prefix to distinguish engine-owned advances from runner-submitted ones.
const EngineMergeActorID = "actor-engine-merge"

// InternalHandler is the signature of an engine-owned handler for a workflow
// step whose type is `internal` and execution.mode is `spine_only`. The
// handler is responsible for doing the step's work (e.g. performing the
// authoritative merge) and advancing the step execution to a terminal
// outcome.
type InternalHandler func(ctx context.Context, o *Orchestrator, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition) error

// internalHandlers registers the engine-owned handlers by the name that
// appears in a workflow step's `execution.handler` field. The keys here
// must match internal/workflow/handlers.go's KnownInternalHandlers so the
// parser can validate references at workflow-load time without importing
// this package. Registration is done in init() so Go's static
// initialization cycle check is satisfied — mergeHandler's body
// transitively calls back into the engine's own methods.
var internalHandlers map[string]InternalHandler

func init() {
	internalHandlers = map[string]InternalHandler{
		"merge": mergeHandler,
	}
}

// LookupInternalHandler resolves a handler registered under name, or
// returns (nil, false) if none is registered. Callers treat a missing
// handler as a fatal configuration error — workflow-load validation
// should have rejected the workflow earlier.
func LookupInternalHandler(name string) (InternalHandler, bool) {
	h, ok := internalHandlers[name]
	return h, ok
}

// mergeHandler is the engine-owned handler for publish-style steps whose
// work is "perform the authoritative merge and advance the step to its
// terminal outcome". The handler transitions the run into committing so
// MergeRunBranch's state precondition holds, then delegates to
// MergeRunBranch — which owns the branch-protection check, the commit-
// status cascade, the merge itself, the push, AND advancing the publish
// step's StepExecution to `published` or `merge_failed`.
func mergeHandler(ctx context.Context, o *Orchestrator, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition) error {
	log := observe.Logger(ctx)

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitMergeStarted,
	})
	if err != nil {
		return fmt.Errorf("evaluate merge-start transition: %w", err)
	}
	applied, err := o.store.TransitionRunStatus(ctx, run.RunID, run.Status, result.ToStatus)
	if err != nil {
		return fmt.Errorf("transition run to committing: %w", err)
	}
	if !applied {
		// Concurrent activator already moved the run; reload and continue.
		log.Info("run already transitioned to committing, continuing", "run_id", run.RunID)
	}
	run.Status = result.ToStatus

	return o.MergeRunBranch(ctx, run.RunID)
}

// activateInternalStep advances an internal step's execution through
// waiting → assigned → in_progress with the Spine engine as the actor,
// then invokes the registered handler. The handler is responsible for
// advancing the step to a terminal outcome; this function only
// establishes the state and actor identity for audit.
func (o *Orchestrator) activateInternalStep(ctx context.Context, exec *domain.StepExecution, stepDef *domain.StepDefinition, run *domain.Run) error {
	log := observe.Logger(ctx)

	if stepDef.Execution == nil || stepDef.Execution.Handler == "" {
		return domain.NewError(domain.ErrInternal,
			fmt.Sprintf("internal step %q has no handler configured", stepDef.ID))
	}
	handler, ok := LookupInternalHandler(stepDef.Execution.Handler)
	if !ok {
		return domain.NewError(domain.ErrInternal,
			fmt.Sprintf("internal step %q references unknown handler %q", stepDef.ID, stepDef.Execution.Handler))
	}

	exec.ErrorDetail = nil
	exec.ActorID = EngineMergeActorID

	assignResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	if err != nil {
		return err
	}
	exec.Status = assignResult.ToStatus

	ackResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAcknowledged,
	})
	if err != nil {
		return err
	}
	now := time.Now()
	exec.Status = ackResult.ToStatus
	exec.StartedAt = &now
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return fmt.Errorf("update internal step execution: %w", err)
	}
	o.emitEvent(ctx, domain.EventStepStarted, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-%s-started", run.TraceID[:12], exec.StepID), nil)

	// Emitted immediately before the handler runs so that a wedged
	// internal step can be diagnosed as "handler crashed mid-flight"
	// vs. "handler never fired" — the only two failure modes that
	// leave the step stuck at in_progress with no terminal outcome.
	log.Info("internal handler invoked",
		"workflow_id", run.WorkflowID,
		"run_id", run.RunID,
		"execution_id", exec.ExecutionID,
		"step_id", exec.StepID,
		"handler", stepDef.Execution.Handler,
	)

	if err := handler(ctx, o, run, exec, stepDef); err != nil {
		// Transient failures (e.g. merge retry) propagate up so the
		// scheduler picks them up. The handler does not transition the
		// step to a terminal outcome in that case — it stays
		// in_progress, which the scheduler treats as retriable.
		return err
	}
	return nil
}

// advancePublishStepIfAny finds the in-progress internal step whose
// handler is the merge handler (i.e. the run's publish step, if the
// current workflow has one) and advances it to outcomeID. A workflow
// without a publish step — any of the seven workflows that attach
// commit: metadata directly to a terminal outcome — has no step to
// advance; this is a no-op on them, which is how MergeRunBranch remains
// backward-compatible with the pre-TASK-015 merge flow.
//
// On outcome "published": the merge has succeeded and completeAfterMerge
// has already transitioned the run to completed. The step execution is
// simply marked completed for audit; routeStepOutcome is not invoked
// because the run has already terminated.
//
// On outcome "merge_failed": the merge has permanently failed. The run
// is transitioned committing → active via TriggerGitMergeFailedRouteBack,
// the step execution is marked completed with outcome merge_failed, and
// routeStepOutcome routes the run to the outcome's next_step (typically
// "execute" so the actor can retry).
func (o *Orchestrator) advancePublishStepIfAny(ctx context.Context, run *domain.Run, outcomeID string) error {
	log := observe.Logger(ctx)

	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow for publish step advance: %w", err)
	}

	stepExec, stepDef := o.findActiveMergeStep(ctx, run, wfDef)
	if stepExec == nil {
		// No publish step in this workflow — nothing to advance. Also
		// covers the concurrent-advance case: a sibling merge attempt
		// has already moved the step to a terminal state, so
		// findActiveMergeStep (which filters on non-terminal) returns
		// nothing.
		return nil
	}

	outcome := findOutcome(stepDef, outcomeID)
	if outcome == nil {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("outcome %q not defined on publish step %q", outcomeID, stepDef.ID))
	}

	submitResult, err := workflow.EvaluateStepTransition(stepExec.Status, workflow.StepTransitionRequest{
		Trigger:   workflow.StepTriggerSubmit,
		OutcomeID: outcomeID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	stepExec.Status = submitResult.ToStatus
	stepExec.OutcomeID = outcomeID
	stepExec.CompletedAt = &now
	if err := o.store.UpdateStepExecution(ctx, stepExec); err != nil {
		return fmt.Errorf("update publish step execution: %w", err)
	}

	o.emitEvent(ctx, domain.EventStepCompleted, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-%s-completed", run.TraceID[:12], stepExec.StepID), nil)

	if outcomeID == "merge_failed" {
		routeResult, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
			Trigger: workflow.TriggerGitMergeFailedRouteBack,
		})
		if err != nil {
			return fmt.Errorf("evaluate merge_failed route-back transition: %w", err)
		}
		// Compare-and-swap: concurrent merge attempts may race — for
		// example a faster attempt can succeed and transition the run
		// to completed while this (slower) attempt is still observing
		// a failure. In that case we must not reopen the run by
		// writing it back to active. TransitionRunStatus returns
		// !applied when another transition has already landed; we
		// log and skip the step routing because the run has already
		// terminated successfully.
		applied, err := o.store.TransitionRunStatus(ctx, run.RunID, run.Status, routeResult.ToStatus)
		if err != nil {
			return fmt.Errorf("route run back to active: %w", err)
		}
		if !applied {
			log.Info("publish step merge_failed, but run already transitioned (concurrent merge success); skipping route-back",
				"run_id", run.RunID)
			return nil
		}
		run.Status = routeResult.ToStatus

		log.Info("publish step merge_failed, routing run back",
			"run_id", run.RunID, "next_step", outcome.NextStep)

		return o.routeStepOutcome(ctx, stepDef, outcome, stepExec, run, wfDef, now)
	}

	log.Info("publish step advanced",
		"run_id", run.RunID, "step_id", stepExec.StepID, "outcome", outcomeID)
	return nil
}

// findActiveMergeStep returns the in-progress (or assigned) step execution
// that belongs to this run's internal step with handler=merge, along with
// the step definition. Returns (nil, nil) when no such step exists in the
// workflow or no execution for it is currently active.
func (o *Orchestrator) findActiveMergeStep(ctx context.Context, run *domain.Run, wfDef *domain.WorkflowDefinition) (*domain.StepExecution, *domain.StepDefinition) {
	log := observe.Logger(ctx)

	var mergeStepDef *domain.StepDefinition
	for i := range wfDef.Steps {
		s := &wfDef.Steps[i]
		if s.Type == domain.StepTypeInternal && s.Execution != nil && s.Execution.Handler == "merge" {
			mergeStepDef = s
			break
		}
	}
	if mergeStepDef == nil {
		return nil, nil
	}

	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		log.Warn("failed to list step executions while finding publish step",
			"run_id", run.RunID, "error", err)
		return nil, nil
	}
	for i := range execs {
		e := &execs[i]
		if e.StepID == mergeStepDef.ID && !e.Status.IsTerminal() {
			return e, mergeStepDef
		}
	}
	return nil, nil
}
