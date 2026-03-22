package actor

import "github.com/bszymi/spine/internal/domain"

// AssignmentRequest contains everything an actor needs to execute a step.
// Per Actor Model §5.2.
type AssignmentRequest struct {
	AssignmentID string                `json:"assignment_id"`
	RunID        string                `json:"run_id"`
	TraceID      string                `json:"trace_id"`
	StepID       string                `json:"step_id"`
	StepName     string                `json:"step_name"`
	StepType     domain.StepType       `json:"step_type"`
	ActorID      string                `json:"actor_id"`
	Context      AssignmentContext     `json:"context"`
	Constraints  AssignmentConstraints `json:"constraints"`
}

// AssignmentContext provides the information an actor needs to execute.
type AssignmentContext struct {
	TaskPath        string   `json:"task_path"`
	WorkflowID      string   `json:"workflow_id"`
	RequiredInputs  []string `json:"required_inputs,omitempty"`
	RequiredOutputs []string `json:"required_outputs,omitempty"`
	Instructions    string   `json:"instructions,omitempty"`
}

// AssignmentConstraints define execution boundaries.
type AssignmentConstraints struct {
	Timeout          string   `json:"timeout,omitempty"`
	ExpectedOutcomes []string `json:"expected_outcomes"`
}

// AssignmentResult is the actor's response to a step assignment.
// Per Actor Model §5.3.
type AssignmentResult struct {
	AssignmentID      string   `json:"assignment_id"`
	RunID             string   `json:"run_id"`
	TraceID           string   `json:"trace_id"`
	ActorID           string   `json:"actor_id"`
	OutcomeID         string   `json:"outcome_id"`
	ArtifactsProduced []string `json:"artifacts_produced,omitempty"`
	Summary           string   `json:"summary,omitempty"`
}
