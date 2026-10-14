package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// SubmitRequest is the external-facing result submission from an actor or API.
type SubmitRequest struct {
	ExecutionID       string   // Step execution ID (assignment ID)
	OutcomeID         string   // Selected outcome
	ArtifactsProduced []string // Paths of artifacts produced
	// ExpectedHeadSHA is the optional run-branch head assertion
	// (StepResult.ExpectedHeadSHA, INIT-023 TASK-005).
	ExpectedHeadSHA string
	// ActorID is the authenticated submitter (INIT-023 TASK-008, audit
	// P3-17). When non-empty, IngestResult refuses (domain.ErrForbidden) a
	// submission for an execution bound to a different actor, before any
	// other check — on a terminal execution too — and writes nothing, so a
	// non-owner can neither complete nor fail another actor's step. Empty
	// means the caller enforces ownership itself (pre-TASK-008); an
	// execution rebound between this method's read and the write is still
	// refused then (the snapshot handoff, see StepResult).
	ActorID string
}

// IngestResult validates an actor's result against step requirements
// and routes it to the orchestrator for step completion. Invalid results
// trigger step failure with appropriate classification.
func (o *Orchestrator) IngestResult(ctx context.Context, req SubmitRequest) (*IngestResponse, error) {
	log := observe.Logger(ctx)

	if req.ExecutionID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "execution_id is required")
	}
	if req.OutcomeID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "outcome_id is required")
	}

	// Load the step execution.
	exec, err := o.store.GetStepExecution(ctx, req.ExecutionID)
	if err != nil {
		return nil, err
	}

	// Ownership (INIT-023 TASK-008, P3-17) comes first: before the
	// terminal answers below (a non-owner learns nothing about the
	// execution, and the gateway refuses a foreign terminal step the same
	// way), before the head resolution (a non-owner costs no git call) and
	// before validateRequiredOutputs (a non-owner cannot fail the owner's
	// step with a submission that omits an output).
	if err := checkSubmitter(exec, req.ActorID); err != nil {
		return nil, err
	}

	// EnforceRunStatus (INIT-023 TASK-007): an execution the engine closed
	// because its run was cancelled or failed answers 409, not the
	// idempotent terminal response below. That response exists so a
	// duplicate submit of the same completed outcome is harmless; applied to
	// a run-terminated execution it would tell a late runner or agent that
	// its work was accepted (an HTTP 200 where SMP's F12 contract answers
	// 409 RUN_NOT_ACTIVE). Ordering contract for TASK-008's terminal guard
	// in this branch: this check stays first.
	if o.entryPolicy.EnforceRunStatus && isRunTerminated(exec) {
		return nil, o.runTerminatedConflict(ctx, exec)
	}

	// Idempotency: if already in a terminal state, return current state.
	// This prevents corrupting completed/failed steps on resubmission.
	if exec.Status.IsTerminal() {
		if exec.Status == domain.StepStatusCompleted && exec.OutcomeID == req.OutcomeID {
			log.Info("duplicate result submission (idempotent)", "execution_id", req.ExecutionID)
		} else {
			log.Info("step already terminal, rejecting submission",
				"execution_id", req.ExecutionID,
				"status", exec.Status,
			)
		}
		return &IngestResponse{
			ExecutionID: req.ExecutionID,
			StepID:      exec.StepID,
			Status:      exec.Status,
			OutcomeID:   exec.OutcomeID,
		}, nil
	}

	// State guard: a result can only be submitted once an actor has
	// taken ownership of the step. The status check mirrors the
	// workflow state machine — `step.submit` is invalid from `waiting`
	// or `blocked`. The actor_id check covers in-flight rows from
	// before the Option B fix that are persisted as `assigned` with
	// `actor_id=""` (the phantom state TASK-004 fixes for new runs);
	// rejecting here forces the operator down the /assign or /claim
	// recovery path instead of validating outputs and fail-with-retry-
	// ing a step nobody claimed. Both checks run BEFORE
	// validateRequiredOutputs so a phantom submission can never
	// mutate the execution into a failed state.
	if exec.Status != domain.StepStatusAssigned && exec.Status != domain.StepStatusInProgress {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot submit result: step %s is in %q state, expected assigned or in_progress",
				req.ExecutionID, exec.Status))
	}
	if exec.ActorID == "" {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot submit result: step %s has no actor bound; call /assign or /claim before submitting",
				req.ExecutionID))
	}

	// Load the workflow to validate against step definition.
	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	// EnforceRunStatus (INIT-023 TASK-007): refuse before the
	// required-outputs failure path below, so a dead run's step is never
	// failed-and-retried by a late submit.
	if err := o.requireActiveRun(run, "submitted"); err != nil {
		return nil, err
	}

	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}

	stepDef := findStepDef(wfDef, exec.StepID)
	if stepDef == nil {
		return nil, domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("step %q not found in workflow", exec.StepID))
	}

	// Validate outcome exists in step definition.
	outcome := findOutcome(stepDef, req.OutcomeID)
	if outcome == nil {
		log.Warn("invalid outcome submitted",
			"execution_id", req.ExecutionID,
			"outcome_id", req.OutcomeID,
			"step_id", exec.StepID,
		)
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("outcome %q not defined for step %s", req.OutcomeID, exec.StepID))
	}

	// A head assertion that cannot apply is refused here, before the
	// required-outputs check below can FAIL the step: a malformed or
	// inapplicable expected_head_sha is a 400 that changes nothing
	// (INIT-023 TASK-005 DC-8). Whether the head still matches is
	// SubmitStepResult's check, made before the step changes.
	if err := o.checkHeadAssertion(run, exec, stepDef, req.ExpectedHeadSHA); err != nil {
		return nil, err
	}

	// Validate required_outputs are present.
	if err := validateRequiredOutputs(stepDef.RequiredOutputs, req.ArtifactsProduced); err != nil {
		// Every head refusal leaves the step exactly as it was, so it
		// wins over the missing-output failure below (codex review,
		// round 2): an expected_head_sha that no longer matches the run
		// branch answers its 409 run_branch_moved, a gate approval of a
		// moved branch its 409 run_branch_moved_since_gate, and an
		// unresolvable head its 503 — none fails the step. captureHead
		// only reads, and with binding off and no assertion it reads
		// nothing; it runs only on this failure path, so a valid
		// submission still resolves the head once, in SubmitStepResult.
		if _, headErr := o.captureHead(ctx, run, exec, stepDef, outcome, wfDef, req.ExpectedHeadSHA); headErr != nil {
			return nil, headErr
		}
		log.Warn("required outputs missing",
			"execution_id", req.ExecutionID,
			"step_id", exec.StepID,
			"error", err,
		)
		// Fail the step with invalid_result classification.
		o.failStepWithClassification(ctx, exec, domain.FailureInvalidResult, err.Error())
		return nil, err
	}

	// Result is valid — submit to orchestrator. The snapshot this method
	// validated against is handed over (expect): SubmitStepResult re-reads
	// the execution and refuses to write if it moved in between.
	validated := stateOf(exec)
	if err := o.SubmitStepResult(ctx, req.ExecutionID, StepResult{
		OutcomeID:         req.OutcomeID,
		ArtifactsProduced: req.ArtifactsProduced,
		ExpectedHeadSHA:   req.ExpectedHeadSHA,
		ActorID:           req.ActorID,
		expect:            &validated,
	}); err != nil {
		if isStepStateChanged(err) {
			return o.lostSubmitRace(ctx, req, err)
		}
		return nil, err
	}

	// Re-read to get final state.
	exec, err = o.store.GetStepExecution(ctx, req.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("re-read step execution: %w", err)
	}

	return &IngestResponse{
		ExecutionID: req.ExecutionID,
		StepID:      exec.StepID,
		Status:      exec.Status,
		OutcomeID:   exec.OutcomeID,
	}, nil
}

// lostSubmitRace answers a submission whose execution moved between this
// call's read and its write (INIT-023 TASK-008, Decision D9): another
// submit, the timeout scanner, a cancel or a rebind won. It re-reads the
// execution and answers exactly as a sequential duplicate would — the
// ownership refusal, TASK-007's run-terminated conflict, or the persisted
// terminal state with a nil error — so a client never sees a
// timing-dependent status. A row that moved but is still live (a rebind)
// keeps the conflict.
func (o *Orchestrator) lostSubmitRace(ctx context.Context, req SubmitRequest, raceErr error) (*IngestResponse, error) {
	exec, err := o.store.GetStepExecution(ctx, req.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("re-read step execution: %w", err)
	}
	if err := checkSubmitter(exec, req.ActorID); err != nil {
		return nil, err
	}
	if o.entryPolicy.EnforceRunStatus && isRunTerminated(exec) {
		return nil, o.runTerminatedConflict(ctx, exec)
	}
	if !exec.Status.IsTerminal() {
		return nil, raceErr
	}
	observe.Logger(ctx).Info("submission lost a race; answering with the persisted state",
		"execution_id", req.ExecutionID, "status", exec.Status, "outcome_id", exec.OutcomeID)
	return &IngestResponse{
		ExecutionID: req.ExecutionID,
		StepID:      exec.StepID,
		Status:      exec.Status,
		OutcomeID:   exec.OutcomeID,
	}, nil
}

// IngestResponse is returned after result ingestion.
type IngestResponse struct {
	ExecutionID string                     `json:"execution_id"`
	StepID      string                     `json:"step_id"`
	Status      domain.StepExecutionStatus `json:"status"`
	OutcomeID   string                     `json:"outcome_id"`
}

// validateRequiredOutputs checks that all required output paths are present
// in the artifacts produced by the actor.
func validateRequiredOutputs(required, produced []string) error {
	if len(required) == 0 {
		return nil
	}

	producedSet := make(map[string]bool, len(produced))
	for _, p := range produced {
		producedSet[p] = true
	}

	var missing []string
	for _, r := range required {
		if !producedSet[r] {
			missing = append(missing, r)
		}
	}

	if len(missing) > 0 {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("missing required outputs: %v", missing))
	}
	return nil
}

// FailStep transitions a step to failed with a classification and message.
// This is the public entry point for external callers (scheduler, gateway).
func (o *Orchestrator) FailStep(ctx context.Context, executionID string, classification domain.FailureClassification, message string) error {
	exec, err := o.store.GetStepExecution(ctx, executionID)
	if err != nil {
		return err
	}
	if exec.Status.IsTerminal() {
		return nil // already terminal, nothing to do
	}
	o.failStepWithClassification(ctx, exec, classification, message)
	return nil
}

// failStepWithClassification transitions a step to failed with error detail,
// then evaluates retry eligibility. If retryable, a new execution is scheduled
// with backoff delay. If not, the run is failed.
//
// The write is conditional on the (status, actor) exec was read with
// (INIT-023 TASK-008). A miss means someone else resolved the step first
// (a submit completed it, the timeout scanner failed it, a cancel closed
// it): it is logged and nothing else happens — no RetryStep, so no retry
// execution and no FailRun for a step this call did not fail. FailStep's
// callers (actor-less system paths) still get nil, the same contract as
// "already terminal, nothing to do".
func (o *Orchestrator) failStepWithClassification(ctx context.Context, exec *domain.StepExecution, classification domain.FailureClassification, message string) {
	log := observe.Logger(ctx)

	prior := stateOf(exec)
	exec.Status = domain.StepStatusFailed
	exec.ErrorDetail = &domain.ErrorDetail{
		Classification: classification,
		Message:        message,
		StepID:         exec.StepID,
	}
	if err := o.writeStepExecution(ctx, exec, prior); err != nil {
		if isStepStateChanged(err) {
			log.Info("step resolved concurrently; not failing it",
				"execution_id", exec.ExecutionID, "classification", classification)
			return
		}
		log.Error("failed to update step execution", "error", err)
		return
	}

	// Evaluate retry — creates new execution or fails the run.
	if err := o.RetryStep(ctx, exec); err != nil {
		log.Warn("retry evaluation failed", "error", err)
	}
}
