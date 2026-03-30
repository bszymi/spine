package domain

import "time"

// RunStatus represents the execution lifecycle status of a Run.
// Per engine-state-machine.md §2.1.
type RunStatus string

const (
	RunStatusPending    RunStatus = "pending"
	RunStatusActive     RunStatus = "active"
	RunStatusPaused     RunStatus = "paused"
	RunStatusCommitting RunStatus = "committing"
	RunStatusCompleted  RunStatus = "completed"
	RunStatusFailed     RunStatus = "failed"
	RunStatusCancelled  RunStatus = "cancelled"
)

// ValidRunStatuses returns all valid Run statuses.
func ValidRunStatuses() []RunStatus {
	return []RunStatus{
		RunStatusPending, RunStatusActive, RunStatusPaused,
		RunStatusCommitting, RunStatusCompleted, RunStatusFailed,
		RunStatusCancelled,
	}
}

// IsTerminal returns true if the Run status is a terminal state.
func (s RunStatus) IsTerminal() bool {
	return s == RunStatusCompleted || s == RunStatusFailed || s == RunStatusCancelled
}

// RunMode distinguishes standard execution runs from planning creation runs.
// Per ADR-006 §1.
type RunMode string

const (
	RunModeStandard RunMode = "standard"
	RunModePlanning RunMode = "planning"
)

// Run represents a workflow execution instance.
type Run struct {
	RunID                string     `json:"run_id" yaml:"run_id"`
	TaskPath             string     `json:"task_path" yaml:"task_path"`
	Mode                 RunMode    `json:"mode,omitempty" yaml:"mode,omitempty"`
	WorkflowPath         string     `json:"workflow_path" yaml:"workflow_path"`
	WorkflowID           string     `json:"workflow_id" yaml:"workflow_id"`
	WorkflowVersion      string     `json:"workflow_version" yaml:"workflow_version"`             // Git commit SHA
	WorkflowVersionLabel string     `json:"workflow_version_label" yaml:"workflow_version_label"` // semantic version
	Status               RunStatus  `json:"status" yaml:"status"`
	CurrentStepID        string     `json:"current_step_id,omitempty" yaml:"current_step_id,omitempty"`
	BranchName           string     `json:"branch_name,omitempty" yaml:"branch_name,omitempty"`
	TraceID              string     `json:"trace_id" yaml:"trace_id"`
	TimeoutAt            *time.Time `json:"timeout_at,omitempty" yaml:"timeout_at,omitempty"`
	StartedAt            *time.Time `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at" yaml:"created_at"`
}
