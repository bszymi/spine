package domain

import (
	"encoding/json"
	"time"
)

// DivergenceStatus represents the lifecycle of a divergence context.
// Per engine-state-machine.md §4.1.
type DivergenceStatus string

const (
	DivergenceStatusPending    DivergenceStatus = "pending"
	DivergenceStatusActive     DivergenceStatus = "active"
	DivergenceStatusConverging DivergenceStatus = "converging"
	DivergenceStatusResolved   DivergenceStatus = "resolved"
	DivergenceStatusFailed     DivergenceStatus = "failed"
)

// BranchStatus represents the lifecycle of a branch within a divergence context.
// Per engine-state-machine.md §5.1.
type BranchStatus string

const (
	BranchStatusPending    BranchStatus = "pending"
	BranchStatusInProgress BranchStatus = "in_progress"
	BranchStatusCompleted  BranchStatus = "completed"
	BranchStatusFailed     BranchStatus = "failed"
)

// DivergenceContext tracks divergence and convergence state within a Run.
type DivergenceContext struct {
	DivergenceID     string           `json:"divergence_id"`
	RunID            string           `json:"run_id"`
	Status           DivergenceStatus `json:"status"`
	DivergenceMode   DivergenceMode   `json:"divergence_mode"`
	DivergenceWindow string           `json:"divergence_window,omitempty"` // "open" or "closed" (exploratory only)
	ConvergenceID    string           `json:"convergence_id,omitempty"`
	TriggeredAt      *time.Time       `json:"triggered_at,omitempty"`
	ResolvedAt       *time.Time       `json:"resolved_at,omitempty"`
}

// Branch tracks an individual execution branch within a divergence context.
type Branch struct {
	BranchID          string          `json:"branch_id"`
	RunID             string          `json:"run_id"`
	DivergenceID      string          `json:"divergence_id"`
	Status            BranchStatus    `json:"status"`
	CurrentStepID     string          `json:"current_step_id,omitempty"`
	Outcome           json.RawMessage `json:"outcome,omitempty"`
	ArtifactsProduced []string        `json:"artifacts_produced"`
	CreatedAt         time.Time       `json:"created_at"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
}

// ConvergenceResult records the outcome of a convergence evaluation.
type ConvergenceResult struct {
	StrategyApplied  ConvergenceStrategy `json:"strategy_applied"`
	SelectedBranch   string              `json:"selected_branch,omitempty"`   // for select_one
	SelectedBranches []string            `json:"selected_branches,omitempty"` // for select_subset
	EvaluationRecord json.RawMessage     `json:"evaluation_record,omitempty"` // evaluator's decision detail
}
