package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// SubmitRequest is the external-facing result submission from an actor or API.
type SubmitRequest struct {
	ExecutionID       string   // Step execution ID (assignment ID)
	OutcomeID         string   // Selected outcome
	ArtifactsProduced []string // Paths of artifacts produced
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

	// Load the workflow to validate against step definition.
	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
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

	// Validate required_outputs are present.
	if err := validateRequiredOutputs(stepDef.RequiredOutputs, req.ArtifactsProduced); err != nil {
		log.Warn("required outputs missing",
			"execution_id", req.ExecutionID,
			"step_id", exec.StepID,
			"error", err,
		)
		// Fail the step with invalid_result classification.
		o.failStepWithClassification(ctx, exec, domain.FailureInvalidResult, err.Error())
		return nil, err
	}

	// Result is valid — submit to orchestrator.
	if err := o.SubmitStepResult(ctx, req.ExecutionID, StepResult{
		OutcomeID:         req.OutcomeID,
		ArtifactsProduced: req.ArtifactsProduced,
	}); err != nil {
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
func (o *Orchestrator) failStepWithClassification(ctx context.Context, exec *domain.StepExecution, classification domain.FailureClassification, message string) {
	log := observe.Logger(ctx)

	exec.Status = domain.StepStatusFailed
	exec.ErrorDetail = &domain.ErrorDetail{
		Classification: classification,
		Message:        message,
		StepID:         exec.StepID,
	}
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		log.Error("failed to update step execution", "error", err)
		return
	}

	// Evaluate retry — creates new execution or fails the run.
	if err := o.RetryStep(ctx, exec); err != nil {
		log.Warn("retry evaluation failed", "error", err)
	}
}
