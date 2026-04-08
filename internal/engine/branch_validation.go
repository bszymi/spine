package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
)

// BranchValidationResult contains the result of validating all artifacts
// on a planning run's branch.
type BranchValidationResult struct {
	TotalArtifacts int                      `json:"total_artifacts"`
	Passed         int                      `json:"passed"`
	Failed         int                      `json:"failed"`
	Details        []BranchValidationDetail `json:"details,omitempty"`
}

// BranchValidationDetail contains per-artifact validation details.
type BranchValidationDetail struct {
	Path   string   `json:"path"`
	Errors []string `json:"errors"`
}

// ValidateBranch discovers all new artifacts on a planning run's branch
// and validates them individually and as a set.
//
// Uses DiscoverChanges(main, branch) to find all new artifacts,
// then validates each one. Returns a combined result.
//
// The gitClient parameter must support Diff and ReadFile (full git.GitClient).
func ValidateBranch(ctx context.Context, gitClient git.GitClient, run *domain.Run) (*BranchValidationResult, error) {
	log := observe.Logger(ctx)

	if run.Mode != domain.RunModePlanning {
		return nil, fmt.Errorf("ValidateBranch only applies to planning runs")
	}
	if run.BranchName == "" {
		return nil, fmt.Errorf("run %s has no branch", run.RunID)
	}

	// Discover all changes on the branch relative to main.
	changes, err := artifact.DiscoverChanges(ctx, gitClient, "main", run.BranchName)
	if err != nil {
		return nil, fmt.Errorf("discover changes: %w", err)
	}

	result := &BranchValidationResult{}

	// Validate each created artifact.
	for _, a := range changes.Created {
		result.TotalArtifacts++
		validateArtifactForBranch(a, result)
	}

	// Also validate modified artifacts.
	for _, a := range changes.Modified {
		result.TotalArtifacts++
		validateArtifactForBranch(a, result)
	}

	if result.TotalArtifacts == 0 {
		result.Failed = 1
		result.Details = append(result.Details, BranchValidationDetail{
			Path:   "(branch)",
			Errors: []string{"no artifacts found on branch"},
		})
	}

	log.Info("branch validation complete",
		"run_id", run.RunID,
		"branch", run.BranchName,
		"total", result.TotalArtifacts,
		"passed", result.Passed,
		"failed", result.Failed,
	)

	return result, nil
}

func validateArtifactForBranch(a *domain.Artifact, result *BranchValidationResult) {
	vResult := artifact.Validate(a)
	if vResult.Status == "failed" {
		result.Failed++
		var errMsgs []string
		for _, e := range vResult.Errors {
			errMsgs = append(errMsgs, e.Message)
		}
		result.Details = append(result.Details, BranchValidationDetail{
			Path:   a.Path,
			Errors: errMsgs,
		})
	} else {
		result.Passed++
	}
}
