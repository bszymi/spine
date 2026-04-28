package validation

import (
	"context"
	"encoding/json"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// Rule evaluates a validation check against an artifact.
type Rule interface {
	ID() string
	Classification() domain.ViolationClassification
	Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError
}

// Engine runs cross-artifact validation rules against the Projection Store.
type Engine struct {
	store store.Store
	rules []Rule
}

// NewEngine creates a validation engine with all registered rules.
func NewEngine(st store.Store) *Engine {
	e := &Engine{store: st}
	e.rules = append(e.rules, structuralRules()...)
	e.rules = append(e.rules, linkRules()...)
	e.rules = append(e.rules, statusRules()...)
	e.rules = append(e.rules, scopeRules()...)
	e.rules = append(e.rules, prereqRules()...)
	return e
}

// Validate runs all applicable rules against a single artifact.
func (e *Engine) Validate(ctx context.Context, artifactPath string) domain.ValidationResult {
	proj, err := e.store.GetArtifactProjection(ctx, artifactPath)
	if err != nil {
		return domain.ValidationResult{
			Status: "failed",
			Errors: []domain.ValidationError{
				{RuleID: "engine", Classification: domain.ViolationStructuralError, ArtifactPath: artifactPath, Severity: "error", Message: err.Error()},
			},
		}
	}

	var errors []domain.ValidationError
	var warnings []domain.ValidationError

	for _, rule := range e.rules {
		results := rule.Evaluate(ctx, proj, e.store)
		for i := range results {
			results[i].ArtifactPath = artifactPath
			results[i].Classification = rule.Classification()
			if results[i].Severity == "warning" {
				warnings = append(warnings, results[i])
			} else {
				errors = append(errors, results[i])
			}
		}
	}

	status := "passed"
	if len(errors) > 0 {
		status = "failed"
	} else if len(warnings) > 0 {
		status = "warnings"
	}

	return domain.ValidationResult{
		Status:   status,
		Errors:   errors,
		Warnings: warnings,
	}
}

// ValidateAll runs validation against all projected artifacts.
//
// The store caps QueryArtifacts at ArtifactQueryMaxLimit per call,
// so we walk the cursor here. A single Limit:1000 fetch used to
// silently truncate to that page size and miss validation errors in
// any workspace with more artifacts than the cap; the loop fixes
// that by following HasMore/NextCursor until the projection is
// exhausted.
func (e *Engine) ValidateAll(ctx context.Context) []domain.ValidationResult {
	var results []domain.ValidationResult
	cursor := ""
	for {
		result, err := e.store.QueryArtifacts(ctx, store.ArtifactQuery{
			Limit:  store.ArtifactQueryMaxLimit,
			Cursor: cursor,
		})
		if err != nil {
			return nil
		}
		for i := range result.Items {
			r := e.Validate(ctx, result.Items[i].ArtifactPath)
			results = append(results, r)
		}
		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}
	return results
}

// parseLinks extracts links from the projection's JSONB links field.
func parseLinks(proj *store.ArtifactProjection) []domain.Link {
	if len(proj.Links) == 0 {
		return nil
	}
	var links []domain.Link
	_ = json.Unmarshal(proj.Links, &links)
	return links
}

// parseMetadata extracts metadata from the projection's JSONB metadata field.
func parseMetadata(proj *store.ArtifactProjection) map[string]string {
	if len(proj.Metadata) == 0 {
		return nil
	}
	var meta map[string]string
	_ = json.Unmarshal(proj.Metadata, &meta)
	return meta
}

// isTerminalStatus returns true if the status is a terminal state.
func isTerminalStatus(status string) bool {
	switch domain.ArtifactStatus(status) {
	case domain.StatusCompleted, domain.StatusSuperseded, domain.StatusCancelled,
		domain.StatusRejected, domain.StatusAbandoned, domain.StatusDeprecated:
		return true
	}
	return false
}
