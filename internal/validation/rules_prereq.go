package validation

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func prereqRules() []Rule {
	return []Rule{&rulePC001{}, &rulePC002{}, &rulePC003{}}
}

// PC-001: Artifacts referenced in blocked_by links must exist.
type rulePC001 struct{}

func (r *rulePC001) ID() string { return "PC-001" }
func (r *rulePC001) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	links := parseLinks(proj)
	var errors []domain.ValidationError

	for _, link := range links {
		if string(link.Type) != "blocked_by" {
			continue
		}
		targetPath := strings.TrimPrefix(link.Target, "/")
		if _, err := st.GetArtifactProjection(ctx, targetPath); err != nil {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.ID(),
				Severity: "error",
				Message:  fmt.Sprintf("blocked_by target %s does not exist", link.Target),
			})
		}
	}
	return errors
}

// PC-002: When Task is In Progress, parent Epic must be at least In Progress.
type rulePC002 struct{}

func (r *rulePC002) ID() string { return "PC-002" }
func (r *rulePC002) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}
	if proj.Status != string(domain.StatusInProgress) {
		return nil
	}

	meta := parseMetadata(proj)
	epicPath := meta["epic"]
	if epicPath == "" {
		return nil
	}

	lookupPath := strings.TrimPrefix(epicPath, "/")
	parent, err := st.GetArtifactProjection(ctx, lookupPath)
	if err != nil {
		return nil
	}

	if !isAtLeastInProgress(parent.Status) {
		return []domain.ValidationError{{
			RuleID:   r.ID(),
			Severity: "warning",
			Message:  fmt.Sprintf("task is In Progress but parent epic is %s", parent.Status),
		}}
	}
	return nil
}

// PC-003: When Epic is In Progress, parent Initiative must be at least In Progress.
type rulePC003 struct{}

func (r *rulePC003) ID() string { return "PC-003" }
func (r *rulePC003) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeEpic {
		return nil
	}
	if proj.Status != string(domain.StatusInProgress) {
		return nil
	}

	meta := parseMetadata(proj)
	initPath := meta["initiative"]
	if initPath == "" {
		return nil
	}

	lookupPath := strings.TrimPrefix(initPath, "/")
	parent, err := st.GetArtifactProjection(ctx, lookupPath)
	if err != nil {
		return nil
	}

	if !isAtLeastInProgress(parent.Status) {
		return []domain.ValidationError{{
			RuleID:   r.ID(),
			Severity: "warning",
			Message:  fmt.Sprintf("epic is In Progress but parent initiative is %s", parent.Status),
		}}
	}
	return nil
}

// isAtLeastInProgress returns true if the status is In Progress or beyond.
func isAtLeastInProgress(status string) bool {
	switch domain.ArtifactStatus(status) {
	case domain.StatusInProgress, domain.StatusCompleted, domain.StatusSuperseded:
		return true
	}
	return false
}
