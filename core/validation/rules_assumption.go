package validation

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// Evidence consistency for Assumption artifacts (ADR-017 §4). An
// Assumption's status says what is recorded about the claim, so a status
// that names evidence should have the evidence: an artifact that links to
// the assumption with supports (Supported) or refutes (Contradicted).
//
// Both rules are warnings. A status may be set in the same change that adds
// the evidence artifact, and the projection of the two is not atomic.
func assumptionRules() []Rule {
	return []Rule{
		&ruleAssumptionEvidence{id: "AL-001", status: domain.StatusSupported, linkType: domain.LinkTypeSupports},
		&ruleAssumptionEvidence{id: "AL-002", status: domain.StatusContradicted, linkType: domain.LinkTypeRefutes},
	}
}

// ruleAssumptionEvidence warns when an Assumption has the given status and
// no artifact links to it with the given link type.
type ruleAssumptionEvidence struct {
	id       string
	status   domain.ArtifactStatus
	linkType domain.LinkType
}

func (r *ruleAssumptionEvidence) ID() string { return r.id }
func (r *ruleAssumptionEvidence) Classification() domain.ViolationClassification {
	return domain.ViolationLinkInconsistency
}

func (r *ruleAssumptionEvidence) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeAssumption ||
		domain.ArtifactStatus(proj.Status) != r.status {
		return nil
	}
	incoming, err := st.QueryArtifactLinksByTarget(ctx, proj.ArtifactPath)
	if err != nil {
		// The rule is advisory: a failed lookup is not evidence of a
		// missing link, so it reports nothing rather than a false warning.
		return nil
	}
	for _, l := range incoming {
		// An assumption cannot be evidence for itself.
		if l.LinkType == string(r.linkType) && !sameArtifactPath(l.SourcePath, proj.ArtifactPath) {
			return nil
		}
	}
	return []domain.ValidationError{{
		RuleID:   r.id,
		Severity: "warning",
		Message: fmt.Sprintf("assumption is %s but no artifact links to it with %q",
			r.status, r.linkType),
	}}
}

func sameArtifactPath(a, b string) bool {
	ab, _ := store.LinkTargetForms(a)
	bb, _ := store.LinkTargetForms(b)
	return ab == bb
}
