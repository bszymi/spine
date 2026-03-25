package divergence

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

// EvaluateAndCommit runs convergence evaluation and commits the result.
// This is the single entry point used by the engine orchestrator.
func (s *Service) EvaluateAndCommit(ctx context.Context, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) error {
	// Transition from active → converging before evaluation.
	if divCtx.Status == domain.DivergenceStatusActive {
		result, err := workflow.EvaluateDivergenceTransition(divCtx.Status, workflow.DivergenceTransitionRequest{
			Trigger:           workflow.DivergenceTriggerBranchDone,
			EntryPolicy:       convDef.EntryPolicy,
			BranchesTotal:     1, // caller already checked policy
			BranchesTerminal:  1,
			BranchesCompleted: 1,
			Strategy:          convDef.Strategy,
		})
		if err != nil {
			return fmt.Errorf("transition to converging: %w", err)
		}
		divCtx.Status = result.ToStatus
		if err := s.store.UpdateDivergenceContext(ctx, divCtx); err != nil {
			return fmt.Errorf("update divergence context: %w", err)
		}
	}

	branches, err := s.store.ListBranchesByDivergence(ctx, divCtx.DivergenceID)
	if err != nil {
		return fmt.Errorf("list branches for convergence: %w", err)
	}

	output, err := s.EvaluateConvergence(ctx, ConvergenceInput{
		DivergenceID: divCtx.DivergenceID,
		Branches:     branches,
		Strategy:     convDef.Strategy,
		EntryPolicy:  convDef.EntryPolicy,
		MinBranches:  convDef.MinBranches,
	})
	if err != nil {
		return fmt.Errorf("evaluate convergence: %w", err)
	}

	if err := s.CommitConvergence(ctx, divCtx, output); err != nil {
		return fmt.Errorf("commit convergence: %w", err)
	}

	return nil
}
