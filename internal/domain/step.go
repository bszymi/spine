package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// StepExecutionStatus represents the execution status of a step.
// Per engine-state-machine.md §3.1.
type StepExecutionStatus string

const (
	StepStatusWaiting    StepExecutionStatus = "waiting"
	StepStatusAssigned   StepExecutionStatus = "assigned"
	StepStatusInProgress StepExecutionStatus = "in_progress"
	StepStatusBlocked    StepExecutionStatus = "blocked"
	StepStatusCompleted  StepExecutionStatus = "completed"
	StepStatusFailed     StepExecutionStatus = "failed"
	StepStatusSkipped    StepExecutionStatus = "skipped"
)

// ValidStepStatuses returns all valid step execution statuses.
func ValidStepStatuses() []StepExecutionStatus {
	return []StepExecutionStatus{
		StepStatusWaiting, StepStatusAssigned, StepStatusInProgress,
		StepStatusBlocked, StepStatusCompleted, StepStatusFailed,
		StepStatusSkipped,
	}
}

// IsTerminal returns true if the step execution status is terminal.
func (s StepExecutionStatus) IsTerminal() bool {
	return s == StepStatusCompleted || s == StepStatusFailed || s == StepStatusSkipped
}

// FailureClassification categorizes why a step failed.
// Per engine-state-machine.md §3.3.1.
type FailureClassification string

const (
	FailureTransient        FailureClassification = "transient_error"
	FailurePermanent        FailureClassification = "permanent_error"
	FailureActorUnavailable FailureClassification = "actor_unavailable"
	FailureInvalidResult    FailureClassification = "invalid_result"
	FailureGitConflict      FailureClassification = "git_conflict"
	FailureTimeout          FailureClassification = "timeout"
)

// IsRetryable returns true if this failure classification allows retry.
func (f FailureClassification) IsRetryable() bool {
	switch f {
	case FailureTransient, FailureActorUnavailable, FailureInvalidResult:
		return true
	default:
		return false
	}
}

// ErrorDetail represents structured failure information stored as JSONB.
type ErrorDetail struct {
	Classification FailureClassification `json:"classification"`
	Message        string                `json:"message"`
	StepID         string                `json:"step_id,omitempty"`
	ActorID        string                `json:"actor_id,omitempty"`
	RuleID         string                `json:"rule_id,omitempty"`
}

// Scan implements the sql.Scanner interface for reading JSONB from PostgreSQL.
func (e *ErrorDetail) Scan(src any) error {
	if src == nil {
		return nil
	}
	b, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("ErrorDetail.Scan: unexpected type %T", src)
	}
	return json.Unmarshal(b, e)
}

// StepExecution represents a single execution attempt of a workflow step.
type StepExecution struct {
	ExecutionID string              `json:"execution_id"`
	RunID       string              `json:"run_id"`
	StepID      string              `json:"step_id"`
	BranchID    string              `json:"branch_id,omitempty"`
	ActorID     string              `json:"actor_id,omitempty"`
	Status      StepExecutionStatus `json:"status"`
	Attempt     int                 `json:"attempt"`
	OutcomeID   string              `json:"outcome_id,omitempty"`
	ErrorDetail *ErrorDetail        `json:"error_detail,omitempty"`
	StartedAt   *time.Time          `json:"started_at,omitempty"`
	CompletedAt *time.Time          `json:"completed_at,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
}
