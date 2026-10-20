package domain

import (
	"encoding/json"
	"time"
)

// EventType represents the type of a domain or operational event.
type EventType string

// Domain events (reconstructible from Git).
const (
	EventArtifactCreated    EventType = "artifact_created"
	EventArtifactUpdated    EventType = "artifact_updated"
	EventRunStarted         EventType = "run_started"
	EventRunCompleted       EventType = "run_completed"
	EventRunFailed          EventType = "run_failed"
	EventRunCancelled       EventType = "run_cancelled"
	EventRunPaused          EventType = "run_paused"
	EventRunResumed         EventType = "run_resumed"
	EventRunPartiallyMerged EventType = "run_partially_merged"
	EventStepAssigned       EventType = "step_assigned"
	EventStepStarted        EventType = "step_started"
	EventStepCompleted      EventType = "step_completed"
	EventStepFailed         EventType = "step_failed"
	EventStepTimeout        EventType = "step_timeout"
	EventRetryAttempted     EventType = "retry_attempted"
	EventRunTimeout         EventType = "run_timeout"
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

	// EventRunBranchCleanupFailed is emitted when post-run branch
	// cleanup hits a git error on a specific repository. The merge
	// outcome itself is unaffected — a failed cleanup is best-effort
	// observable state, not a regression of the merge result. Payload:
	// run_id, repository_id, branch, scope ("local"|"remote"), error.
	// Operational tier (not reconstructible from Git history) so the
	// retry budget treats it as low-priority delivery — losing one
	// cleanup notification does not invalidate any merged commit.
	EventRunBranchCleanupFailed EventType = "run_branch_cleanup_failed"

	// EventRunRepositoryMergeResolved is emitted when an operator
	// marks a failed per-repo merge outcome as resolved-externally
	// (EPIC-005 TASK-006). Payload: repository_id, resolved_by,
	// reason. Operational tier — the audit trail is the event log
	// itself; a missed delivery does not change the recorded outcome
	// row, which is the source of truth the scheduler consults.
	EventRunRepositoryMergeResolved EventType = "run_repository_merge_resolved"

	// EventRunRepositoryMergeRetryRequested is emitted when an
	// operator requests a retry of a failed per-repo merge outcome
	// (EPIC-005 TASK-006). Payload: repository_id, requested_by,
	// reason. Operational tier — same rationale as
	// EventRunRepositoryMergeResolved: the outcome row is the source
	// of truth, the event records who asked and why.
	EventRunRepositoryMergeRetryRequested EventType = "run_repository_merge_retry_requested"

	// EventProjectionWorkflowRejected is emitted when the projection
	// refuses a workflow file because of its content (strict parse,
	// projection round trip, scope, absence at head, or — opt-in —
	// workflow.Validate). Payload: workflow_path, commit, check, action
	// (kept | not_projected | removed), kept_source_commit, quarantined,
	// error. INIT-023 TASK-011; architecture/event-schemas.md.
	EventProjectionWorkflowRejected EventType = "projection_workflow_rejected"

	// EventProjectionFileSkipped is emitted once per file the projection
	// declined to project because the file is over the git client's
	// per-blob read limit (reason "oversized") or sits at a path Spine
	// refuses to store (reason "unsafe_path": invalid UTF-8, control
	// characters, U+2028/U+2029, bidi controls). Payload: path (Go-quoted
	// for unsafe_path), reason, size_bytes and limit_bytes (null for
	// unsafe_path; size_bytes -1 when the read overran the limit), commit.
	// The event id is deterministic per (commit, reason, path). INIT-023
	// TASK-003; architecture/event-schemas.md §4.15.
	EventProjectionFileSkipped EventType = "projection_file_skipped"

	// Governance-path guard events (ADR-016; INIT-023 TASK-006, audit F3).
	// architecture/event-schemas.md §4.16-§4.18.
	//
	// EventGovernanceMergeHeld is emitted when a governed merge changes
	// governance paths and waits for an operator decision (the run stays
	// in committing), and when a merge's post-merge assertion refuses it
	// (post_merge: true). Payload: run_id, task_path, bound_sha, paths
	// (capped), path_count, exempt_paths, stale_approval_sha, post_merge.
	// The event id is deterministic per bound commit and hold episode, so
	// re-evaluation on every scheduler tick emits it once.
	EventGovernanceMergeHeld EventType = "governance.merge_held"
	// EventGovernanceMergeDecided is emitted when an operator approves or
	// rejects a held merge. Payload: run_id, bound_sha, decision
	// (approved | rejected), actor_id, actor_role, reason, paths,
	// path_count.
	EventGovernanceMergeDecided EventType = "governance.merge_decided"
	// EventBranchProtectionRemovalRefused is emitted when the projection
	// refuses to install a branch-protection config that removes
	// protection from the authoritative branch without a database record
	// of an operator act that changed the config; the previous rules stay
	// installed. Payload: branch, installed_kinds, candidate_kinds,
	// head_commit, installed_source_commit, records_checked.
	EventBranchProtectionRemovalRefused EventType = "branch_protection.removal_refused"
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
