package domain

import (
	"encoding/json"
	"time"
)

// EventType represents the type of a domain or operational event.
type EventType string

// Domain events (reconstructible from Git).
const (
	EventArtifactCreated EventType = "artifact_created"
	EventArtifactUpdated EventType = "artifact_updated"
	EventRunStarted          EventType = "run_started"
	EventRunCompleted        EventType = "run_completed"
	EventRunFailed           EventType = "run_failed"
	EventRunCancelled        EventType = "run_cancelled"
	EventRunPaused           EventType = "run_paused"
	EventRunResumed          EventType = "run_resumed"
	EventRunPartiallyMerged  EventType = "run_partially_merged"
	EventStepAssigned    EventType = "step_assigned"
	EventStepStarted     EventType = "step_started"
	EventStepCompleted   EventType = "step_completed"
	EventStepFailed      EventType = "step_failed"
	EventStepTimeout     EventType = "step_timeout"
	EventRetryAttempted  EventType = "retry_attempted"
	EventRunTimeout      EventType = "run_timeout"
)

// Operational events (not reconstructible from Git).
const (
	EventDivergenceStarted    EventType = "divergence_started"
	EventConvergenceCompleted EventType = "convergence_completed"
	EventEngineRecovered      EventType = "engine_recovered"
	EventProjectionSynced     EventType = "projection_synced"
	EventThreadCreated        EventType = "thread_created"
	EventCommentAdded         EventType = "comment_added"
	EventThreadResolved       EventType = "thread_resolved"
	EventValidationPassed     EventType = "validation_passed"
	EventValidationFailed     EventType = "validation_failed"
	EventAssignmentFailed     EventType = "step_assignment_failed"
	EventTaskUnblocked        EventType = "task_unblocked"
	EventTaskReleased         EventType = "task_released"
	// EventBranchProtectionOverride is emitted on every honored branch-
	// protection override (ADR-009 §4). Payload names the branch,
	// operation, rule kinds that the override bypassed, and — on the
	// Spine API path — the resulting commit SHA. commit_sha is null for
	// deletions and for ref pushes that do not produce a new commit in-
	// process.
	EventBranchProtectionOverride EventType = "branch_protection.override"
)

// Event represents a domain or operational event emitted by the system.
type Event struct {
	EventID      string          `json:"event_id" yaml:"event_id"`
	Type         EventType       `json:"type" yaml:"type"`
	Timestamp    time.Time       `json:"timestamp" yaml:"timestamp"`
	ActorID      string          `json:"actor_id,omitempty" yaml:"actor_id,omitempty"`
	RunID        string          `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	ArtifactPath string          `json:"artifact_path,omitempty" yaml:"artifact_path,omitempty"`
	TraceID      string          `json:"trace_id,omitempty" yaml:"trace_id,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty" yaml:"payload,omitempty"`
}
