package validation

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/store"
)

// repositoryRules returns the RE-* rules that validate Task
// repository-frontmatter references against the governed catalog.
//
// RE-001 is registered only when a catalog snapshot is wired. Without
// it the rule has no way to distinguish a known multi-repo entry from
// an unknown one, so registering it would silently reject valid
// `repositories: [payments-service]` references in any production path
// that hasn't yet plugged in a loader. The "single-repo with no
// catalog file" case is handled at the snapshot layer: ParseCatalog
// synthesises a primary-only catalog from empty bytes, so a workspace
// without /.spine/repositories.yaml still resolves "spine" correctly
// once the snapshot is wired.
//
// RE-002 and RE-003 are pure shape checks on the projected slice and
// are always registered.
func repositoryRules(snapshot CatalogSnapshot) []Rule {
	rules := []Rule{&ruleRE002{}, &ruleRE003{}}
	if snapshot != nil {
		rules = append(rules, &ruleRE001{snapshot: snapshot})
	}
	return rules
}

// ruleRE001 — Task repository IDs must resolve to an entry in the
// governed catalog. The primary "spine" entry is always present in the
// catalog (synthesised by ParseCatalog when no /.spine/repositories.yaml
// is committed), so an explicit `repositories: [spine]` validates in
// every workspace. Runtime active/inactive state is intentionally not
// consulted here; that is run-start precondition territory (TASK-004).
type ruleRE001 struct {
	snapshot CatalogSnapshot
}

func (r *ruleRE001) ID() string { return "RE-001" }
func (r *ruleRE001) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleRE001) Evaluate(ctx context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}
	if len(proj.Repositories) == 0 {
		return nil
	}

	log := observe.Logger(ctx)

	cat, ref, err := r.snapshot(ctx)
	if err != nil {
		// We can't resolve repository IDs without the catalog. Surface
		// this as a validation error against the task so the failure
		// is visible rather than silently passing.
		log.Error("repository validation: catalog snapshot unavailable",
			"task", proj.ArtifactPath, "err", err)
		return []domain.ValidationError{{
			RuleID:   r.ID(),
			Severity: "error",
			Message:  fmt.Sprintf("task %s: repository catalog unavailable: %v", proj.ArtifactPath, err),
		}}
	}

	var errors []domain.ValidationError
	for _, id := range proj.Repositories {
		if _, ok := cat.Get(id); !ok {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.ID(),
				Severity: "error",
				Message:  fmt.Sprintf("task %s: repository %q not found in catalog", proj.ArtifactPath, id),
			})
		}
	}

	if len(errors) > 0 {
		log.Warn("repository validation failed",
			"rule_id", r.ID(),
			"task", proj.ArtifactPath,
			"catalog_ref", ref,
			"violations", len(errors),
		)
	}
	return errors
}

// ruleRE002 — A Task must not list the same repository ID twice.
type ruleRE002 struct{}

func (r *ruleRE002) ID() string { return "RE-002" }
func (r *ruleRE002) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleRE002) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}
	if len(proj.Repositories) < 2 {
		return nil
	}
	seen := make(map[string]int, len(proj.Repositories))
	var errors []domain.ValidationError
	for i, id := range proj.Repositories {
		if first, dup := seen[id]; dup {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.ID(),
				Severity: "error",
				Message:  fmt.Sprintf("task %s: repository %q duplicated (indices %d and %d)", proj.ArtifactPath, id, first, i),
			})
			continue
		}
		seen[id] = i
	}
	return errors
}

// ruleRE003 — Repository IDs declared on a Task must match the catalog
// ID format (lowercase alphanumeric with single internal hyphens, 64
// chars max). Mirrors repository.IsValidID so a task can never name an
// ID the catalog itself would refuse.
type ruleRE003 struct{}

func (r *ruleRE003) ID() string { return "RE-003" }
func (r *ruleRE003) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleRE003) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}
	var errors []domain.ValidationError
	for _, id := range proj.Repositories {
		if !repository.IsValidID(id) {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.ID(),
				Severity: "error",
				Message:  fmt.Sprintf("task %s: repository id %q is not a valid catalog id", proj.ArtifactPath, id),
			})
		}
	}
	return errors
}
