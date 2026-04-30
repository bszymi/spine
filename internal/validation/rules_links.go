package validation

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func linkRules(governedFileResolver GovernedFileResolver) []Rule {
	return []Rule{&ruleLinkReciprocal{"LC-001", "parent", "contains"},
		&ruleLinkReciprocal{"LC-002", "blocks", "blocked_by"},
		&ruleLinkReciprocal{"LC-003", "supersedes", "superseded_by"},
		&ruleLC004{governedFileResolver: governedFileResolver}, &ruleLC005{}}
}

// ruleLinkReciprocal checks that for each link of sourceType,
// the target has a corresponding link of reciprocalType back.
type ruleLinkReciprocal struct {
	id             string
	sourceType     string
	reciprocalType string
}

func (r *ruleLinkReciprocal) ID() string { return r.id }
func (r *ruleLinkReciprocal) Classification() domain.ViolationClassification {
	return domain.ViolationLinkInconsistency
}
func (r *ruleLinkReciprocal) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	links := parseLinks(proj)
	var errors []domain.ValidationError

	for _, link := range links {
		if string(link.Type) != r.sourceType {
			continue
		}

		// For parent↔contains, the relationship may be inferred from file hierarchy
		// per the spec: "the relationship must be inferable from file hierarchy"
		if r.sourceType == "parent" && r.reciprocalType == "contains" {
			targetPath := strings.TrimPrefix(link.Target, "/")
			if isInferredParentChild(proj.ArtifactPath, targetPath) {
				continue // hierarchy implies the relationship
			}
		}

		targetPath := strings.TrimPrefix(link.Target, "/")
		targetLinks, err := st.QueryArtifactLinks(ctx, targetPath)
		if err != nil {
			continue
		}

		found := false
		for _, tl := range targetLinks {
			if tl.LinkType == r.reciprocalType && (tl.TargetPath == proj.ArtifactPath || tl.TargetPath == "/"+proj.ArtifactPath) {
				found = true
				break
			}
		}

		if !found {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.id,
				Severity: "warning",
				Message:  fmt.Sprintf("link %s → %s has no reciprocal %s link back", r.sourceType, link.Target, r.reciprocalType),
			})
		}
	}
	return errors
}

// isInferredParentChild returns true if the child path is within the parent path's
// directory hierarchy, indicating an implicit parent-child relationship.
func isInferredParentChild(childPath, parentPath string) bool {
	// Parent is typically at initiatives/INIT-XXX/epics/EPIC-XXX/epic.md
	// Child is at initiatives/INIT-XXX/epics/EPIC-XXX/tasks/TASK-XXX.md
	// Or parent is initiatives/INIT-XXX/initiative.md and child is under that dir
	parentDir := parentPath
	if idx := strings.LastIndex(parentPath, "/"); idx > 0 {
		parentDir = parentPath[:idx]
	}
	return strings.HasPrefix(childPath, parentDir+"/")
}

// LC-004: Link targets must resolve to existing artifacts.
//
// The artifact projection covers Markdown front-matter artifacts only.
// Pure-YAML governed artifacts (validation policies, the repository
// catalog) live outside the projection but are still legitimate link
// targets — see ADR-013 / ADR-014. When a GovernedFileResolver is
// wired, LC-004 consults it as a fallback before flagging a missed
// projection lookup as dangling.
type ruleLC004 struct {
	governedFileResolver GovernedFileResolver
}

func (r *ruleLC004) ID() string { return "LC-004" }
func (r *ruleLC004) Classification() domain.ViolationClassification {
	return domain.ViolationLinkInconsistency
}
func (r *ruleLC004) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	links := parseLinks(proj)
	var errors []domain.ValidationError

	for _, link := range links {
		targetPath := strings.TrimPrefix(link.Target, "/")
		if _, err := st.GetArtifactProjection(ctx, targetPath); err == nil {
			continue
		}
		// The resolver contract requires a canonical, leading-slash
		// target. LC-005 will flag non-canonical paths separately;
		// don't hand them to a resolver that can rely on the
		// canonical-path invariant. Empty targets fall through to
		// the dangling-link error here as well.
		if r.governedFileResolver != nil && strings.HasPrefix(link.Target, "/") &&
			r.governedFileResolver(ctx, link.Target) {
			continue
		}
		errors = append(errors, domain.ValidationError{
			RuleID:   r.ID(),
			Severity: "error",
			Message:  fmt.Sprintf("link target %s does not exist", link.Target),
		})
	}
	return errors
}

// LC-005: Link targets must use canonical path format (start with /).
type ruleLC005 struct{}

func (r *ruleLC005) ID() string { return "LC-005" }
func (r *ruleLC005) Classification() domain.ViolationClassification {
	return domain.ViolationLinkInconsistency
}
func (r *ruleLC005) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	links := parseLinks(proj)
	var errors []domain.ValidationError

	for _, link := range links {
		if !strings.HasPrefix(link.Target, "/") {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.ID(),
				Severity: "error",
				Message:  fmt.Sprintf("link target %q is not a canonical path (must start with /)", link.Target),
			})
		}
	}
	return errors
}
