package validation

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func linkRules() []Rule {
	return []Rule{&ruleLinkReciprocal{"LC-001", "parent", "contains"},
		&ruleLinkReciprocal{"LC-002", "blocks", "blocked_by"},
		&ruleLinkReciprocal{"LC-003", "supersedes", "superseded_by"},
		&ruleLC004{}, &ruleLC005{}}
}

// ruleLinkReciprocal checks that for each link of sourceType,
// the target has a corresponding link of reciprocalType back.
type ruleLinkReciprocal struct {
	id             string
	sourceType     string
	reciprocalType string
}

func (r *ruleLinkReciprocal) ID() string { return r.id }
func (r *ruleLinkReciprocal) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	links := parseLinks(proj)
	var errors []domain.ValidationError

	for _, link := range links {
		if string(link.Type) != r.sourceType {
			continue
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

// LC-004: Link targets must resolve to existing artifacts.
type ruleLC004 struct{}

func (r *ruleLC004) ID() string { return "LC-004" }
func (r *ruleLC004) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	links := parseLinks(proj)
	var errors []domain.ValidationError

	for _, link := range links {
		targetPath := strings.TrimPrefix(link.Target, "/")
		if _, err := st.GetArtifactProjection(ctx, targetPath); err != nil {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.ID(),
				Severity: "error",
				Message:  fmt.Sprintf("link target %s does not exist", link.Target),
			})
		}
	}
	return errors
}

// LC-005: Link targets must use canonical path format (start with /).
type ruleLC005 struct{}

func (r *ruleLC005) ID() string { return "LC-005" }
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
