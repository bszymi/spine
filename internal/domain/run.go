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
	RunID                string    `json:"run_id" yaml:"run_id"`
	TaskPath             string    `json:"task_path" yaml:"task_path"`
	Mode                 RunMode   `json:"mode,omitempty" yaml:"mode,omitempty"`
	WorkflowPath         string    `json:"workflow_path" yaml:"workflow_path"`
	WorkflowID           string    `json:"workflow_id" yaml:"workflow_id"`
	WorkflowVersion      string    `json:"workflow_version" yaml:"workflow_version"`             // Git commit SHA
	WorkflowVersionLabel string    `json:"workflow_version_label" yaml:"workflow_version_label"` // semantic version
	Status               RunStatus `json:"status" yaml:"status"`
	CurrentStepID        string    `json:"current_step_id,omitempty" yaml:"current_step_id,omitempty"`
	BranchName           string    `json:"branch_name,omitempty" yaml:"branch_name,omitempty"`
	// AffectedRepositories is the set of repository IDs this run will write
	// to. Standard runs derive it from the Task; planning runs are always
	// primary-repo-only ([PrimaryRepositoryID]).
	AffectedRepositories []string `json:"affected_repositories,omitempty" yaml:"affected_repositories,omitempty"`
	// PrimaryRepository records whether the workspace primary repo (id
	// [PrimaryRepositoryID]) participates in this run. True for every run
	// today (governance writes always go to the primary repo); the explicit
	// flag preserves the option for a future run kind that bypasses it.
	PrimaryRepository bool `json:"primary_repository,omitempty" yaml:"primary_repository,omitempty"`
	// RepositoryBranches optionally records the branch name actually created
	// in each affected repository. Today every repo gets BranchName; the map
	// only fills in once divergent state needs to be tracked (e.g. partial
	// branch-creation cleanup or per-repo recovery).
	RepositoryBranches map[string]string `json:"repository_branches,omitempty" yaml:"repository_branches,omitempty"`
	TraceID            string            `json:"trace_id" yaml:"trace_id"`
	CommitMeta         map[string]string `json:"commit_meta,omitempty" yaml:"commit_meta,omitempty"`
	TimeoutAt          *time.Time        `json:"timeout_at,omitempty" yaml:"timeout_at,omitempty"`
	StartedAt          *time.Time        `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	CompletedAt        *time.Time        `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	CreatedAt          time.Time         `json:"created_at" yaml:"created_at"`
}

// PrimaryRepositoryID is the reserved ID of the workspace primary repository.
// Mirrors repository.PrimaryRepositoryID; defined here so the domain layer can
// reference the sentinel without depending on the repository package (which
// would create an import cycle: repository already imports domain).
const PrimaryRepositoryID = "spine"

// AffectedRepositoriesForTask derives the set of repository IDs a standard
// run will touch from a Task artifact. The primary repo is always first;
// task-declared code repos follow in their declared order, with the primary
// ID and any duplicates stripped. A nil task or a task with no repositories
// field produces [PrimaryRepositoryID].
func AffectedRepositoriesForTask(task *Artifact) []string {
	out := []string{PrimaryRepositoryID}
	if task == nil {
		return out
	}
	seen := map[string]struct{}{PrimaryRepositoryID: {}}
	for _, id := range task.Repositories {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
