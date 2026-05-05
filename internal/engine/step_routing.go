package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/workflow"
)

// invalid_step_repository categories (per ADR-015 *Validation*). Recorded
// in error detail so callers can distinguish failure modes without
// parsing message strings.
const (
	stepRoutingFailureUnregistered = "unregistered"  // not in catalog
	stepRoutingFailureInactive     = "inactive"      // binding inactive
	stepRoutingFailureNotOptedIn   = "not_opted_in"  // not in task.repositories ∪ {spine}
	stepRoutingFailureBadFormat    = "bad_format"    // doesn't match catalog ID regex
	stepRoutingFailureInternal     = "internal"      // resolver error
)

// StepRoutingFailure is the structured detail attached to invalid
// run-start errors returned by validateStepRouting. Naming the step ID
// and the offending repository in a typed shape (rather than an error
// message) lets gateway responses and metrics surface both fields
// without re-deriving them from prose.
type StepRoutingFailure struct {
	StepID       string `json:"step_id"`
	RepositoryID string `json:"repository_id"`
	Category     string `json:"category"`
}

// resolveStepRepositoryID applies the ADR-015 resolution rule: return
// step.Repository if non-empty, else PrimaryRepositoryID ("spine"). It
// is the single source of truth used by both run-start validation and
// step activation, so the two paths cannot drift.
func resolveStepRepositoryID(step *domain.StepDefinition) string {
	if step != nil && step.Repository != "" {
		return step.Repository
	}
	return domain.PrimaryRepositoryID
}

// resolveStepRepoIDByStepID looks up the step in the workflow definition
// by ID and applies resolveStepRepositoryID. Used by execution-creation
// sites that have a runID/wfDef + stepID rather than a *StepDefinition.
// A missing step ID falls back to PrimaryRepositoryID — the workflow's
// step-list integrity is enforced elsewhere; this function never panics
// on lookup failure.
func resolveStepRepoIDByStepID(wfDef *domain.WorkflowDefinition, stepID string) string {
	if wfDef == nil {
		return domain.PrimaryRepositoryID
	}
	for i := range wfDef.Steps {
		if wfDef.Steps[i].ID == stepID {
			return resolveStepRepositoryID(&wfDef.Steps[i])
		}
	}
	return domain.PrimaryRepositoryID
}

// recordStepRoutingDecision emits a structured log + counter increment
// at the moment a StepExecution row is created with its resolved repo.
// `source` is "explicit" when the step declared `repository: <id>`,
// "default-spine" otherwise — useful for spotting silent regressions
// where a workflow expected to target a code repo defaulted to spine.
func recordStepRoutingDecision(ctx context.Context, runID, stepID string, stepDef *domain.StepDefinition, repoID string) {
	source := "default-spine"
	if stepDef != nil && stepDef.Repository != "" {
		source = "explicit"
		observe.GlobalMetrics.StepRoutingExplicit.Inc()
	} else {
		observe.GlobalMetrics.StepRoutingDefaultSpine.Inc()
	}
	observe.Logger(ctx).Info("step routing decision",
		"run_id", runID,
		"step_id", stepID,
		"repository_id", repoID,
		"source", source,
	)
}

// validateStepRouting walks every step in a workflow definition and
// rejects the run start if any step's resolved target repository is
// (a) malformed, (b) not in the task's opt-in set
// (`task.repositories ∪ {spine}`), (c) not registered in the workspace
// catalog, or (d) registered but inactive. The check covers every step
// — including ones behind divergence branches that may never execute —
// because the workflow definition is pinned at run start and we want
// the misconfiguration surfaced before any branch creation work.
//
// `spine` is implicit: it always passes existence and active checks
// (per ADR-013 §2.1, single-repo workspaces may omit the catalog file
// entirely; per ADR-015 *Validation* the primary repo is always
// considered active for as long as the workspace itself is). It is
// also always in the opt-in set even when `task.repositories` is empty.
func (o *Orchestrator) validateStepRouting(ctx context.Context, wfDef *domain.WorkflowDefinition, art *domain.Artifact) error {
	if wfDef == nil || art == nil {
		return nil
	}

	log := observe.Logger(ctx)

	// Build the task opt-in set: code repos from task + spine.
	optedIn := make(map[string]bool, len(art.Repositories)+1)
	for _, id := range art.Repositories {
		optedIn[id] = true
	}
	optedIn[domain.PrimaryRepositoryID] = true

	for i := range wfDef.Steps {
		step := &wfDef.Steps[i]
		repoID := resolveStepRepositoryID(step)

		// Format check applies to explicit declarations only — implicit
		// `spine` resolution is governed by the constant, not by user
		// input. Reuses workflow.IsValidRepositoryID so the run-start
		// rule and the workflow-load rule share exactly one source of
		// truth (no drift between layers).
		if step.Repository != "" && !workflow.IsValidRepositoryID(step.Repository) {
			return newStepRoutingError(step.ID, step.Repository, stepRoutingFailureBadFormat,
				fmt.Sprintf("step %q repository %q does not match the catalog ID format ^[a-z0-9]+(-[a-z0-9]+)*$ (max %d chars)", step.ID, step.Repository, workflow.StepRepositoryIDMaxLen),
				nil)
		}

		// Opt-in check: the task must declare the target repo (spine is implicit).
		if !optedIn[repoID] {
			return newStepRoutingError(step.ID, repoID, stepRoutingFailureNotOptedIn,
				fmt.Sprintf("step %q targets repository %q which is not in task.repositories ∪ {spine}", step.ID, repoID),
				nil)
		}

		// Spine is always present and active. Code repos must be looked up.
		if repoID == domain.PrimaryRepositoryID {
			continue
		}

		if o.repositories == nil {
			// No resolver wired (e.g., single-repo runtime). The opt-in
			// set above already required `spine`, so any non-spine repo
			// here would have been rejected earlier — defense-in-depth.
			return newStepRoutingError(step.ID, repoID, stepRoutingFailureUnregistered,
				fmt.Sprintf("step %q targets repository %q but no repository resolver is wired", step.ID, repoID),
				nil)
		}
		repo, err := o.repositories.Lookup(ctx, repoID)
		if err != nil {
			category := categorizeStepRoutingError(err)
			log.Warn("step routing validation failed",
				"step_id", step.ID,
				"repository_id", repoID,
				"category", category,
				"error", err,
			)
			return newStepRoutingError(step.ID, repoID, category,
				fmt.Sprintf("step %q targets repository %q which failed catalog/binding resolution: %s", step.ID, repoID, category),
				err)
		}
		// Repository resolved cleanly; Lookup already guards against inactive
		// status (returns ErrRepositoryInactive). The success branch is logged
		// at the engine's structured-log level for observability.
		log.Info("step routing validated",
			"step_id", step.ID,
			"repository_id", repo.ID,
			"status", repo.Status,
		)
	}

	return nil
}

func categorizeStepRoutingError(err error) string {
	switch {
	case errors.Is(err, repository.ErrRepositoryNotFound):
		return stepRoutingFailureUnregistered
	case errors.Is(err, repository.ErrRepositoryUnbound):
		return stepRoutingFailureUnregistered
	case errors.Is(err, repository.ErrRepositoryInactive):
		return stepRoutingFailureInactive
	default:
		return stepRoutingFailureInternal
	}
}

func newStepRoutingError(stepID, repoID, category, message string, cause error) error {
	var spineErr *domain.SpineError
	if cause != nil {
		spineErr = domain.NewErrorWithCause(domain.ErrPrecondition, message, cause)
	} else {
		spineErr = domain.NewError(domain.ErrPrecondition, message)
	}
	spineErr.Detail = StepRoutingFailure{
		StepID:       stepID,
		RepositoryID: repoID,
		Category:     category,
	}
	return spineErr
}
