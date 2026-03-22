package validation

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func scopeRules() []Rule {
	return []Rule{&ruleSA001{}, &ruleSA002{}}
}

// SA-001: Task deliverables should reference paths consistent with repository structure.
type ruleSA001 struct{}

func (r *ruleSA001) ID() string { return "SA-001" }
func (r *ruleSA001) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}
	// Scope alignment is a warning-level advisory check.
	// Full implementation would parse deliverables from content and validate paths.
	// For now, check that the task has non-empty content.
	if proj.Content == "" {
		return []domain.ValidationError{{
			RuleID: r.ID(), Severity: "warning",
			Message: "task has no content body",
		}}
	}
	return nil
}

// SA-002: ADR decisions should reference architectural context.
type ruleSA002 struct{}

func (r *ruleSA002) ID() string { return "SA-002" }
func (r *ruleSA002) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeADR {
		return nil
	}
	// Advisory check: ADR should have links to architecture docs.
	links := parseLinks(proj)
	for _, link := range links {
		if string(link.Type) == "related_to" {
			return nil // has at least one related link
		}
	}
	return []domain.ValidationError{{
		RuleID: r.ID(), Severity: "warning",
		Message: "ADR has no related_to links to architectural context",
	}}
}
