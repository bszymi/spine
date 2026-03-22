package validation

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// PreconditionTypeCrossArtifactValid is the precondition type that triggers
// cross-artifact validation before step execution.
const PreconditionTypeCrossArtifactValid = "cross_artifact_valid"

// EvaluatePreconditions checks all preconditions for a step.
// Returns a failed ValidationResult if any validation errors are found.
// Warnings are logged but do not block execution.
func EvaluatePreconditions(ctx context.Context, engine *Engine, step domain.StepDefinition, taskPath string) *domain.ValidationResult {
	if len(step.Preconditions) == 0 {
		return &domain.ValidationResult{Status: "passed"}
	}

	log := observe.Logger(ctx)
	var allErrors []domain.ValidationError
	var allWarnings []domain.ValidationError

	for _, precond := range step.Preconditions {
		if precond.Type != PreconditionTypeCrossArtifactValid {
			continue // unknown precondition types are skipped
		}

		// Determine artifact path to validate
		artifactPath := taskPath
		if p := precond.Config["artifact_path"]; p != "" {
			artifactPath = p
		}

		result := engine.Validate(ctx, artifactPath)

		allErrors = append(allErrors, result.Errors...)
		allWarnings = append(allWarnings, result.Warnings...)
	}

	// Log warnings
	for i := range allWarnings {
		log.Warn("precondition warning",
			"rule_id", allWarnings[i].RuleID,
			"message", allWarnings[i].Message,
			"artifact_path", allWarnings[i].ArtifactPath,
		)
	}

	status := "passed"
	if len(allErrors) > 0 {
		status = "failed"
	} else if len(allWarnings) > 0 {
		status = "warnings"
	}

	return &domain.ValidationResult{
		Status:   status,
		Errors:   allErrors,
		Warnings: allWarnings,
	}
}
