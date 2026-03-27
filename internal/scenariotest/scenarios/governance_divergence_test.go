//go:build scenario

package scenarios_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/workflow"
)

// TestDivergence_SelectOneStrategy validates that the select_one
// convergence strategy selects the first completed branch.
func TestDivergence_SelectOneStrategy(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "select-one-strategy",
		Description: "select_one convergence picks the first completed branch",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "evaluate-select-one",
				Action: func(sc *engine.ScenarioContext) error {
					svc := divergence.NewService(nil, nil, nil)
					output, err := svc.EvaluateConvergence(sc.Ctx, divergence.ConvergenceInput{
						DivergenceID: "div-1",
						Branches: []domain.Branch{
							{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"art1.md"}},
							{BranchID: "b2", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"art2.md"}},
						},
						Strategy: domain.ConvergenceSelectOne,
					})
					if err != nil {
						return fmt.Errorf("evaluate convergence: %w", err)
					}
					if output.Result.SelectedBranch != "b1" {
						sc.T.Errorf("expected selected branch b1, got %s", output.Result.SelectedBranch)
					}
					if len(output.SelectedArtifacts) != 1 || output.SelectedArtifacts[0] != "art1.md" {
						sc.T.Errorf("expected [art1.md], got %v", output.SelectedArtifacts)
					}
					return nil
				},
			},
		},
	})
}

// TestDivergence_MergeStrategy validates that merge requires at least
// 2 completed branches and includes all their artifacts.
func TestDivergence_MergeStrategy(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "merge-strategy",
		Description: "merge convergence requires 2+ completed branches and merges artifacts",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "merge-success",
				Action: func(sc *engine.ScenarioContext) error {
					svc := divergence.NewService(nil, nil, nil)
					output, err := svc.EvaluateConvergence(sc.Ctx, divergence.ConvergenceInput{
						DivergenceID: "div-1",
						Branches: []domain.Branch{
							{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a1.md"}},
							{BranchID: "b2", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a2.md"}},
						},
						Strategy: domain.ConvergenceMerge,
					})
					if err != nil {
						return fmt.Errorf("evaluate merge: %w", err)
					}
					if len(output.Result.SelectedBranches) != 2 {
						sc.T.Errorf("expected 2 selected branches, got %d", len(output.Result.SelectedBranches))
					}
					if len(output.SelectedArtifacts) != 2 {
						sc.T.Errorf("expected 2 artifacts, got %d", len(output.SelectedArtifacts))
					}
					return nil
				},
			},
			{
				Name: "merge-rejects-single-branch",
				Action: func(sc *engine.ScenarioContext) error {
					svc := divergence.NewService(nil, nil, nil)
					_, err := svc.EvaluateConvergence(sc.Ctx, divergence.ConvergenceInput{
						DivergenceID: "div-1",
						Branches: []domain.Branch{
							{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a1.md"}},
						},
						Strategy: domain.ConvergenceMerge,
					})
					if err == nil {
						sc.T.Error("expected error for merge with single branch")
					}
					return nil
				},
			},
		},
	})
}

// TestDivergence_RequireAllStrategy validates that require_all fails
// if any branch has failed.
func TestDivergence_RequireAllStrategy(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "require-all-strategy",
		Description: "require_all convergence fails if any branch failed",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "require-all-success",
				Action: func(sc *engine.ScenarioContext) error {
					svc := divergence.NewService(nil, nil, nil)
					output, err := svc.EvaluateConvergence(sc.Ctx, divergence.ConvergenceInput{
						DivergenceID: "div-1",
						Branches: []domain.Branch{
							{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a1.md"}},
							{BranchID: "b2", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a2.md"}},
						},
						Strategy: domain.ConvergenceRequireAll,
					})
					if err != nil {
						return fmt.Errorf("evaluate require_all: %w", err)
					}
					if len(output.Result.SelectedBranches) != 2 {
						sc.T.Errorf("expected 2 selected branches, got %d", len(output.Result.SelectedBranches))
					}
					return nil
				},
			},
			{
				Name: "require-all-rejects-failed-branch",
				Action: func(sc *engine.ScenarioContext) error {
					svc := divergence.NewService(nil, nil, nil)
					_, err := svc.EvaluateConvergence(sc.Ctx, divergence.ConvergenceInput{
						DivergenceID: "div-1",
						Branches: []domain.Branch{
							{BranchID: "b1", Status: domain.BranchStatusCompleted, ArtifactsProduced: []string{"a1.md"}},
							{BranchID: "b2", Status: domain.BranchStatusFailed},
						},
						Strategy: domain.ConvergenceRequireAll,
					})
					if err == nil {
						sc.T.Error("expected error when a branch has failed")
					}
					return nil
				},
			},
		},
	})
}

// TestDivergence_SelectOneRejectsNoBranches validates that select_one
// fails when no branches have completed.
func TestDivergence_SelectOneRejectsNoBranches(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "select-one-no-branches",
		Description: "select_one fails when no branches completed",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-rejection",
				Action: func(sc *engine.ScenarioContext) error {
					svc := divergence.NewService(nil, nil, nil)
					_, err := svc.EvaluateConvergence(sc.Ctx, divergence.ConvergenceInput{
						DivergenceID: "div-1",
						Branches: []domain.Branch{
							{BranchID: "b1", Status: domain.BranchStatusFailed},
						},
						Strategy: domain.ConvergenceSelectOne,
					})
					if err == nil {
						sc.T.Error("expected error for select_one with no completed branches")
					}
					return nil
				},
			},
		},
	})
}

// TestDivergence_EntryPolicyAllTerminal validates that the
// all_branches_terminal entry policy triggers convergence only
// when all branches are in a terminal state.
func TestDivergence_EntryPolicyAllTerminal(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "entry-policy-all-terminal",
		Description: "all_branches_terminal policy triggers only when all branches are done",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-entry-policy",
				Action: func(sc *engine.ScenarioContext) error {
					// Not all terminal — should not trigger.
					notReady := workflow.DivergenceTransitionRequest{
						Trigger:           workflow.DivergenceTriggerBranchDone,
						EntryPolicy:       domain.EntryPolicyAllTerminal,
						BranchesTotal:     3,
						BranchesTerminal:  2,
						BranchesCompleted: 2,
						Strategy:          domain.ConvergenceSelectOne,
					}
					result, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, notReady)
					if err != nil {
						return fmt.Errorf("evaluate not-ready: %w", err)
					}
					if result.ToStatus == domain.DivergenceStatusConverging {
						sc.T.Error("should not converge when not all branches are terminal")
					}

					// All terminal — should trigger convergence.
					ready := notReady
					ready.BranchesTerminal = 3
					ready.BranchesCompleted = 3
					result, err = workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, ready)
					if err != nil {
						return fmt.Errorf("evaluate ready: %w", err)
					}
					if result.ToStatus != domain.DivergenceStatusConverging {
						sc.T.Errorf("expected converging, got %s", result.ToStatus)
					}
					return nil
				},
			},
		},
	})
}

// TestDivergence_EntryPolicyMinimumCompleted validates that the
// minimum_completed_branches policy triggers when enough branches
// have completed.
func TestDivergence_EntryPolicyMinimumCompleted(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "entry-policy-minimum-completed",
		Description: "minimum_completed_branches triggers when min branches are done",
		EnvOpts:     harness.Seeded(),
		Steps: []engine.Step{
			{
				Name: "verify-minimum-policy",
				Action: func(sc *engine.ScenarioContext) error {
					ctx := context.Background()
					_ = ctx

					// Only 1 of 2 minimum completed — should not trigger.
					notEnough := workflow.DivergenceTransitionRequest{
						Trigger:           workflow.DivergenceTriggerBranchDone,
						EntryPolicy:       domain.EntryPolicyMinCompleted,
						BranchesTotal:     3,
						BranchesTerminal:  1,
						BranchesCompleted: 1,
						MinBranches:       2,
						Strategy:          domain.ConvergenceSelectOne,
					}
					result, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, notEnough)
					if err != nil {
						return fmt.Errorf("evaluate not-enough: %w", err)
					}
					if result.ToStatus == domain.DivergenceStatusConverging {
						sc.T.Error("should not converge when minimum not met")
					}

					// 2 of 2 minimum completed — should trigger.
					enough := notEnough
					enough.BranchesTerminal = 2
					enough.BranchesCompleted = 2
					result, err = workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, enough)
					if err != nil {
						return fmt.Errorf("evaluate enough: %w", err)
					}
					if result.ToStatus != domain.DivergenceStatusConverging {
						sc.T.Errorf("expected converging, got %s", result.ToStatus)
					}
					return nil
				},
			},
		},
	})
}
