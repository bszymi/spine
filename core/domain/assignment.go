package domain

import "time"

// AssignmentStatus represents the lifecycle status of an actor assignment.
// Per runtime-schema.md §4.7.
type AssignmentStatus string

const (
	AssignmentStatusActive    AssignmentStatus = "active"
	AssignmentStatusCompleted AssignmentStatus = "completed"
	AssignmentStatusCancelled AssignmentStatus = "cancelled"
	AssignmentStatusTimedOut  AssignmentStatus = "timed_out"
)

// Assignment tracks an actor's assignment to a step execution.
type Assignment struct {
	AssignmentID string           `json:"assignment_id" yaml:"assignment_id"`
	RunID        string           `json:"run_id" yaml:"run_id"`
	ExecutionID  string           `json:"execution_id" yaml:"execution_id"`
	ActorID      string           `json:"actor_id" yaml:"actor_id"`
	Status       AssignmentStatus `json:"status" yaml:"status"`
	AssignedAt   time.Time        `json:"assigned_at" yaml:"assigned_at"`
	RespondedAt  *time.Time       `json:"responded_at,omitempty" yaml:"responded_at,omitempty"`
	TimeoutAt    *time.Time       `json:"timeout_at,omitempty" yaml:"timeout_at,omitempty"`
}
