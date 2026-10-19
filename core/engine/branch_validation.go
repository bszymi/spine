package engine

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
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

	// A file discovery refused (INIT-023 TASK-003: over the git per-blob
	// read limit, or at a path Spine refuses to store; wave 4 follow-up: a
	// gitlink, not a file) was never read, so the branch cannot be
	// vouched for. Each one fails validation instead of merging
	// unvalidated and then never being projected.
	for _, r := range changes.Refused {
		result.TotalArtifacts++
		result.Failed++
		result.Details = append(result.Details, BranchValidationDetail{
			Path:   refusedPathForReport(r.Path),
			Errors: []string{refusalReport(r)},
		})
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

// refusedPathForReport is the path as reported: itself when safe,
// Go-quoted when it holds characters Spine refuses to store (control,
// line-separator or bidi characters, or invalid UTF-8).
func refusedPathForReport(p string) string {
	if artifact.UnsafePath(p) {
		return strconv.Quote(p)
	}
	return p
}

func refusalReport(r artifact.RefusedFile) string {
	if r.Reason == artifact.RefusedOversized {
		if r.GitAllocLimit > 0 {
			return fmt.Sprintf("not validated: reading it needs a git allocation over the %d-byte git allocation limit", r.GitAllocLimit)
		}
		if r.Size >= 0 {
			return fmt.Sprintf("not validated: %d bytes, over the %d-byte git read limit", r.Size, r.Limit)
		}
		return fmt.Sprintf("not validated: over the %d-byte git read limit", r.Limit)
	}
	if r.Reason == artifact.RefusedNotAFile {
		return "not validated: the path holds a gitlink or a directory, not a file"
	}
	return "not validated: the path holds control, line-separator or bidi characters, or is not valid UTF-8"
}
