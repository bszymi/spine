package validation

import (
	"context"
	"encoding/json"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/store"
)

// Rule evaluates a validation check against an artifact.
type Rule interface {
	ID() string
	Classification() domain.ViolationClassification
	Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError
}

// CatalogSnapshot returns the parsed repository catalog plus a
// reproducibility tag (typically the Git commit SHA the catalog was
// read from). RE-* rules call it on each evaluation so the catalog
// stays consistent with the latest governance commit; the tag is
// emitted in rule logs so a validation failure can be replayed against
// the exact catalog state.
//
// When no snapshot is wired the engine treats the workspace as
// single-repo: only the implicit primary "spine" ID is valid.
type CatalogSnapshot func(ctx context.Context) (*repository.Catalog, string, error)

// GovernedFileResolver reports whether a canonical link target refers
// to a known governed YAML artifact that lives outside the artifact
// projection (e.g., /governance/validation-policies/<name>.yaml,
// /.spine/repositories.yaml — see ADR-013, ADR-014, and
// /governance/artifact-schema.md §5.8 / §5.9). LC-004 consults the
// resolver as a fallback whenever the projection lookup misses, so an
// ADR linking to a validation policy file is not flagged as a dangling
// link if the file exists.
//
// `target` is the canonical, leading-slash path as it appears in the
// artifact's `links[].target` field (LC-005 already enforces the
// leading slash, so callers can rely on it).
//
// When no resolver is wired the engine preserves today's strict
// behavior: any link whose target is not in the projection is
// dangling. The resolver is opt-in plumbing for the validation-policy
// registry that lands with TASK-004 (EPIC-006); workspaces that never
// register a non-projection governed YAML need not wire it.
type GovernedFileResolver func(ctx context.Context, target string) bool

// Option configures Engine construction.
type Option func(*Engine)

// WithCatalogSnapshot wires the catalog source consulted by the RE-*
// repository reference rules.
func WithCatalogSnapshot(snapshot CatalogSnapshot) Option {
	return func(e *Engine) {
		e.catalogSnapshot = snapshot
	}
}

// WithGovernedFileResolver wires the resolver consulted by LC-004 as a
// fallback when a link's target is not in the artifact projection.
// Production wiring lands with TASK-004; until then NoopGovernedFileResolver
// preserves today's behavior (every non-projection target is dangling).
func WithGovernedFileResolver(resolver GovernedFileResolver) Option {
	return func(e *Engine) {
		e.governedFileResolver = resolver
	}
}

// NoopGovernedFileResolver returns the default GovernedFileResolver:
// it never resolves any path, so LC-004 falls through to its
// projection-only behavior. Use this as the explicit production
// placeholder while the policy registry wiring (TASK-004) is pending —
// it makes the seam visible at the callsite without changing behavior.
func NoopGovernedFileResolver() GovernedFileResolver {
	return func(_ context.Context, _ string) bool { return false }
}

// PrimaryOnlyCatalogSnapshot returns a CatalogSnapshot that always
// resolves to the synthesised primary-only catalog. This is the safe
// production default for workspaces that have not committed
// /.spine/repositories.yaml: RE-001 accepts the implicit "spine" ID
// and rejects every other repository reference. Callers that read the
// real catalog from Git should pass their own snapshot via
// WithCatalogSnapshot instead.
func PrimaryOnlyCatalogSnapshot(spec repository.PrimarySpec) CatalogSnapshot {
	cat, err := repository.ParseCatalog(nil, spec)
	return func(_ context.Context) (*repository.Catalog, string, error) {
		return cat, "primary-only", err
	}
}

// Engine runs cross-artifact validation rules against the Projection Store.
type Engine struct {
	store                store.Store
	catalogSnapshot      CatalogSnapshot
	governedFileResolver GovernedFileResolver
	rules                []Rule
}

// NewEngine creates a validation engine with all registered rules.
func NewEngine(st store.Store, opts ...Option) *Engine {
	e := &Engine{store: st}
	for _, opt := range opts {
		opt(e)
	}
	e.rules = append(e.rules, structuralRules()...)
	e.rules = append(e.rules, linkRules(e.governedFileResolver)...)
	e.rules = append(e.rules, statusRules()...)
	e.rules = append(e.rules, scopeRules()...)
	e.rules = append(e.rules, prereqRules()...)
	e.rules = append(e.rules, repositoryRules(e.catalogSnapshot)...)
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
