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

// Run represents a workflow execution instance.
type Run struct {
	RunID                string     `json:"run_id"`
	TaskPath             string     `json:"task_path"`
	WorkflowPath         string     `json:"workflow_path"`
	WorkflowID           string     `json:"workflow_id"`
	WorkflowVersion      string     `json:"workflow_version"`       // Git commit SHA
	WorkflowVersionLabel string     `json:"workflow_version_label"` // semantic version
	Status               RunStatus  `json:"status"`
	CurrentStepID        string     `json:"current_step_id,omitempty"`
	TraceID              string     `json:"trace_id"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}
