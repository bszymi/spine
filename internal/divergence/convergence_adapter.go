package divergence

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
)

// EvaluateAndCommit runs convergence evaluation and commits the result.
// This is the single entry point used by the engine orchestrator.
func (s *Service) EvaluateAndCommit(ctx context.Context, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) error {
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
