package divergence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// ConvergenceInput contains the branch outcomes for evaluation.
type ConvergenceInput struct {
	DivergenceID string
	Branches     []domain.Branch
	Strategy     domain.ConvergenceStrategy
	EntryPolicy  domain.EntryPolicy
	MinBranches  int
}

// ConvergenceOutput is the result of convergence evaluation.
type ConvergenceOutput struct {
	Result            domain.ConvergenceResult
	SelectedArtifacts []string // artifact paths to merge
}

// CheckEntryPolicy evaluates whether convergence may begin.
func (s *Service) CheckEntryPolicy(ctx context.Context, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) (bool, error) {
	branches, err := s.store.ListBranchesByDivergence(ctx, divCtx.DivergenceID)
	if err != nil {
		return false, fmt.Errorf("list branches: %w", err)
	}

	total := len(branches)
	terminal := 0
	completed := 0
	hasFailed := false
	for i := range branches {
		if branches[i].Status == domain.BranchStatusCompleted || branches[i].Status == domain.BranchStatusFailed {
			terminal++
		}
		if branches[i].Status == domain.BranchStatusCompleted {
			completed++
		}
		if branches[i].Status == domain.BranchStatusFailed {
			hasFailed = true
		}
	}

	req := workflow.DivergenceTransitionRequest{
		Trigger:           workflow.DivergenceTriggerBranchDone,
		EntryPolicy:       convDef.EntryPolicy,
		BranchesTotal:     total,
		BranchesTerminal:  terminal,
		BranchesCompleted: completed,
		MinBranches:       convDef.MinBranches,
		Strategy:          convDef.Strategy,
		BranchFailed:      hasFailed,
	}

	result, err := workflow.EvaluateDivergenceTransition(divCtx.Status, req)
	if err != nil {
		return false, err
	}

	return result.ToStatus == domain.DivergenceStatusConverging, nil
}

// EvaluateConvergence applies the convergence strategy to branch outcomes.
func (s *Service) EvaluateConvergence(ctx context.Context, input ConvergenceInput) (*ConvergenceOutput, error) {
	log := observe.Logger(ctx)

	var completedBranches []domain.Branch
	for i := range input.Branches {
		if input.Branches[i].Status == domain.BranchStatusCompleted {
			completedBranches = append(completedBranches, input.Branches[i])
		}
	}

	output := &ConvergenceOutput{
		Result: domain.ConvergenceResult{
			StrategyApplied: input.Strategy,
		},
	}

	switch input.Strategy {
	case domain.ConvergenceSelectOne:
		if len(completedBranches) == 0 {
			return nil, domain.NewError(domain.ErrConflict, "no completed branches for select_one")
		}
		// Select the first completed branch (evaluator would refine this)
		selected := completedBranches[0]
		output.Result.SelectedBranch = selected.BranchID
		output.SelectedArtifacts = selected.ArtifactsProduced

	case domain.ConvergenceSelectSubset:
		if len(completedBranches) == 0 {
			return nil, domain.NewError(domain.ErrConflict, "no completed branches for select_subset")
		}
		var selectedIDs []string
		for i := range completedBranches {
			selectedIDs = append(selectedIDs, completedBranches[i].BranchID)
			output.SelectedArtifacts = append(output.SelectedArtifacts, completedBranches[i].ArtifactsProduced...)
		}
		output.Result.SelectedBranches = selectedIDs

	case domain.ConvergenceMerge:
		if len(completedBranches) < 2 {
			return nil, domain.NewError(domain.ErrConflict, "merge requires at least 2 completed branches")
		}
		var selectedIDs []string
		for i := range completedBranches {
			selectedIDs = append(selectedIDs, completedBranches[i].BranchID)
			output.SelectedArtifacts = append(output.SelectedArtifacts, completedBranches[i].ArtifactsProduced...)
		}
		output.Result.SelectedBranches = selectedIDs

	case domain.ConvergenceRequireAll:
		// All branches must be completed — no failures or unfinished
		for i := range input.Branches {
			if input.Branches[i].Status == domain.BranchStatusFailed {
				return nil, domain.NewError(domain.ErrConflict,
					fmt.Sprintf("require_all: branch %s failed", input.Branches[i].BranchID))
			}
			if input.Branches[i].Status != domain.BranchStatusCompleted {
				return nil, domain.NewError(domain.ErrConflict,
					fmt.Sprintf("require_all: branch %s is %s (not completed)", input.Branches[i].BranchID, input.Branches[i].Status))
			}
		}
		var selectedIDs []string
		for i := range completedBranches {
			selectedIDs = append(selectedIDs, completedBranches[i].BranchID)
			output.SelectedArtifacts = append(output.SelectedArtifacts, completedBranches[i].ArtifactsProduced...)
		}
		output.Result.SelectedBranches = selectedIDs

	case domain.ConvergenceExperiment:
		// Package all branches as experiment variants
		var selectedIDs []string
		for i := range completedBranches {
			selectedIDs = append(selectedIDs, completedBranches[i].BranchID)
		}
		output.Result.SelectedBranches = selectedIDs

	default:
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("unknown convergence strategy: %s", input.Strategy))
	}

	log.Info("convergence evaluated",
		"divergence_id", input.DivergenceID,
		"strategy", input.Strategy,
		"completed_branches", len(completedBranches),
	)
	return output, nil
}

// CommitConvergence finalizes the convergence: merges selected artifacts, updates state, emits event.
func (s *Service) CommitConvergence(ctx context.Context, divCtx *domain.DivergenceContext, output *ConvergenceOutput) error {
	log := observe.Logger(ctx)
	now := time.Now()

	// Merge selected branch artifacts to the task branch
	for _, branchID := range s.selectedBranchIDs(output) {
		gitBranch := fmt.Sprintf("spine/%s/%s/%s", divCtx.RunID, divCtx.DivergenceID, extractBranchSuffix(branchID, divCtx.DivergenceID))
		if _, err := s.git.Merge(ctx, git.MergeOpts{
			Source:   gitBranch,
			Strategy: "merge-commit",
			Message:  fmt.Sprintf("Converge branch %s", branchID),
		}); err != nil {
			return fmt.Errorf("merge branch %s failed: %w", branchID, err)
		}
	}

	// Transition to resolved
	result, err := workflow.EvaluateDivergenceTransition(divCtx.Status, workflow.DivergenceTransitionRequest{
		Trigger: workflow.DivergenceTriggerEvalDone,
	})
	if err != nil {
		return err
	}

	divCtx.Status = result.ToStatus
	divCtx.ResolvedAt = &now
	if err := s.store.UpdateDivergenceContext(ctx, divCtx); err != nil {
		return fmt.Errorf("update divergence context: %w", err)
	}

	// Emit convergence_completed event
	evalRecord, _ := json.Marshal(output.Result)
	payload, _ := json.Marshal(map[string]any{
		"divergence_id":     divCtx.DivergenceID,
		"strategy":          output.Result.StrategyApplied,
		"selected_branch":   output.Result.SelectedBranch,
		"selected_branches": output.Result.SelectedBranches,
	})
	if err := s.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("conv-completed-%s", divCtx.DivergenceID),
		Type:      domain.EventConvergenceCompleted,
		Timestamp: now,
		RunID:     divCtx.RunID,
		Payload:   payload,
	}); err != nil {
		log.Warn("failed to emit convergence_completed event", "error", err)
	}

	_ = evalRecord // stored in event payload
	log.Info("convergence committed",
		"divergence_id", divCtx.DivergenceID,
		"strategy", output.Result.StrategyApplied,
	)
	return nil
}

func (s *Service) selectedBranchIDs(output *ConvergenceOutput) []string {
	if output.Result.SelectedBranch != "" {
		return []string{output.Result.SelectedBranch}
	}
	return output.Result.SelectedBranches
}

// extractBranchSuffix extracts the branch suffix from a full branch ID.
// E.g., "run-1-div-div-1-a" with divergence "run-1-div-div-1" → "a"
func extractBranchSuffix(branchID, divergenceID string) string {
	prefix := divergenceID + "-"
	if len(branchID) > len(prefix) {
		return branchID[len(prefix):]
	}
	return branchID
}
