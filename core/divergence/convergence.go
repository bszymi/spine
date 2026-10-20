package divergence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
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

	observe.AuditLog(ctx, "convergence_evaluated",
		"divergence_id", input.DivergenceID,
		"strategy", string(input.Strategy),
		"selected_branch", output.Result.SelectedBranch,
		"selected_branches_count", len(output.Result.SelectedBranches),
	)

	return output, nil
}

// CommitConvergence finalizes the convergence: merges the selected
// branches into the run branch, updates state, emits the event.
//
// INIT-023 TASK-012 (audit F8). It used to merge with no Target, which
// the git client read as "into whatever is checked out" — in standalone's
// shared working tree that is main between sequences, so divergence-branch
// content (whoever can push the deterministic spine/<run>/<div>/<branch>
// ref authors it) landed on the governed branch while the run was still
// active and its review step still waiting, without the governed merge,
// and with branch protection asked only about the source ref. Now:
//
//  1. Guard (convergenceTargetGuard): the run must be the divergence's
//     own run and have a spine/ run branch, checked before any git or
//     store call. "Converge into main" or "into another run's branch" is
//     unrepresentable.
//  2. Branch protection is consulted for every selected branch against
//     the branch that actually moves (run.BranchName), before the tree is
//     taken (policy evaluation is not git).
//  3. The merges name run.BranchName as Target and, for a shared-tree
//     client (git.WorkingTreeGuard: standalone's CLIClient), run as one
//     sequence that holds the working tree; a failed merge is aborted
//     there before the tree is released, so a conflict no longer leaves
//     the shared tree mid-merge. The abort is never sent to any other
//     client (SMP's adapter would turn it into a push of HEAD).
//  4. The divergence transition, the store write and the
//     convergence_completed event run after the tree is released.
func (s *Service) CommitConvergence(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, output *ConvergenceOutput) error {
	if err := convergenceTargetGuard(run, divCtx); err != nil {
		return err
	}
	log := observe.Logger(ctx)
	now := time.Now()

	selected := s.selectedBranchIDs(output)
	for range selected {
		// Route through branch-protection policy (ADR-009 §3) for audit
		// consistency, naming the branch the merge moves: the run branch
		// (spine/*), which no user rule matches, so the check is a no-op
		// at the decision level and keeps the audit trail symmetric with
		// the MergeRunBranch guard.
		if err := s.evaluateBranchOp(ctx, run.BranchName, branchprotect.OpGovernedMerge, divCtx.RunID); err != nil {
			return err
		}
	}
	if err := s.mergeSelected(ctx, run, divCtx, selected); err != nil {
		return err
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
	payload, _ := json.Marshal(map[string]any{
		"divergence_id":     divCtx.DivergenceID,
		"strategy":          output.Result.StrategyApplied,
		"selected_branch":   output.Result.SelectedBranch,
		"selected_branches": output.Result.SelectedBranches,
	})
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID:   fmt.Sprintf("conv-completed-%s", divCtx.DivergenceID),
		Type:      domain.EventConvergenceCompleted,
		Timestamp: now,
		RunID:     divCtx.RunID,
		Payload:   payload,
	})

	log.Info("convergence committed",
		"divergence_id", divCtx.DivergenceID,
		"strategy", output.Result.StrategyApplied,
	)
	return nil
}

// mergeSelected merges every selected divergence branch into the run
// branch: inside the shared working tree's lock when s.git is a
// git.WorkingTreeGuard (and then a failed merge is aborted before the tree
// is released), directly otherwise. Git only: nothing is emitted or stored.
func (s *Service) mergeSelected(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, selected []string) error {
	merge := func(ctx context.Context, abortOnError bool) error {
		for _, branchID := range selected {
			gitBranch := fmt.Sprintf("spine/%s/%s/%s", divCtx.RunID, divCtx.DivergenceID, extractBranchSuffix(branchID, divCtx.DivergenceID))
			if _, err := s.git.Merge(ctx, git.MergeOpts{
				Source:   gitBranch,
				Target:   run.BranchName,
				Strategy: "merge-commit",
				Message:  fmt.Sprintf("Converge branch %s", branchID),
			}); err != nil {
				if abortOnError {
					if _, abortErr := s.git.Merge(ctx, git.MergeOpts{Source: "--abort", Strategy: "abort"}); abortErr != nil {
						observe.Logger(ctx).Warn("convergence: aborting the failed merge failed; the next sequence's restore aborts it",
							"divergence_id", divCtx.DivergenceID, "branch_id", branchID, "error", abortErr)
					}
				}
				return fmt.Errorf("merge branch %s failed: %w", branchID, err)
			}
		}
		return nil
	}
	if guard, ok := s.git.(git.WorkingTreeGuard); ok {
		return guard.WithWorkingTree(ctx, func(ctx context.Context) error { return merge(ctx, true) })
	}
	return merge(ctx, false)
}

// convergenceTargetGuard is the F8 guard (INIT-023 TASK-012): a
// convergence (and an exploratory branch) needs the divergence's own run,
// whose branch is a Spine run branch. Every run branch the engine creates
// is spine/run/… or spine/plan/… (core/engine/branchname.go), so main, a
// missing branch or another run's branch is refused before anything
// happens.
func convergenceTargetGuard(run *domain.Run, divCtx *domain.DivergenceContext) error {
	switch {
	case divCtx == nil:
		return domain.NewError(domain.ErrPrecondition, "divergence: no divergence context")
	case run == nil:
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("divergence %s: the run is required (its branch is the convergence target)", divCtx.DivergenceID))
	case run.RunID != divCtx.RunID:
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("divergence %s belongs to run %s, not %s", divCtx.DivergenceID, divCtx.RunID, run.RunID))
	case run.BranchName == "":
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("divergence %s: run %s has no branch to converge into", divCtx.DivergenceID, run.RunID))
	case !strings.HasPrefix(run.BranchName, "spine/"):
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("divergence %s: run %s branch %q is not a Spine run branch", divCtx.DivergenceID, run.RunID, run.BranchName))
	}
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
