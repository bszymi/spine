package validation

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func statusRules() []Rule {
	return []Rule{&ruleSC001{}, &ruleSC002{}, &ruleSC003{}, &ruleSC004{}, &ruleSC005{}}
}

// SC-001: Completed Task should have acceptance field.
type ruleSC001 struct{}

func (r *ruleSC001) ID() string { return "SC-001" }
func (r *ruleSC001) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}
	if proj.Status != string(domain.StatusCompleted) {
		return nil
	}

	meta := parseMetadata(proj)
	if meta["acceptance"] == "" {
		return []domain.ValidationError{{
			RuleID: r.ID(), Severity: "warning",
			Message: "completed task is missing acceptance field",
		}}
	}
	return nil
}

// SC-002: Completed Epic should have all child Tasks in terminal state.
type ruleSC002 struct{}

func (r *ruleSC002) ID() string { return "SC-002" }
func (r *ruleSC002) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeEpic {
		return nil
	}
	if proj.Status != string(domain.StatusCompleted) {
		return nil
	}

	return checkChildrenTerminal(ctx, st, proj, domain.ArtifactTypeTask, r.ID())
}

// SC-003: Completed Initiative should have all child Epics in terminal state.
type ruleSC003 struct{}

func (r *ruleSC003) ID() string { return "SC-003" }
func (r *ruleSC003) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeInitiative {
		return nil
	}
	if proj.Status != string(domain.StatusCompleted) {
		return nil
	}

	return checkChildrenTerminal(ctx, st, proj, domain.ArtifactTypeEpic, r.ID())
}

func checkChildrenTerminal(ctx context.Context, st store.Store, parent *store.ArtifactProjection, childType domain.ArtifactType, ruleID string) []domain.ValidationError {
	result, err := st.QueryArtifacts(ctx, store.ArtifactQuery{
		Type:  string(childType),
		Limit: 500,
	})
	if err != nil {
		return nil
	}

	parentPath := "/" + parent.ArtifactPath
	var errors []domain.ValidationError
	for i := range result.Items {
		meta := parseMetadata(&result.Items[i])
		childParent := ""
		switch childType {
		case domain.ArtifactTypeTask:
			childParent = meta["epic"]
		case domain.ArtifactTypeEpic:
			childParent = meta["initiative"]
		}

		if childParent != parentPath {
			continue
		}

		if !isTerminalStatus(result.Items[i].Status) {
			errors = append(errors, domain.ValidationError{
				RuleID:   ruleID,
				Severity: "warning",
				Message:  fmt.Sprintf("parent is Completed but child %s is %s", result.Items[i].ArtifactPath, result.Items[i].Status),
			})
		}
	}
	return errors
}

// SC-004: Superseded artifact should have supersedes/superseded_by link.
type ruleSC004 struct{}

func (r *ruleSC004) ID() string { return "SC-004" }
func (r *ruleSC004) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	if proj.Status != string(domain.StatusSuperseded) {
		return nil
	}

	links := parseLinks(proj)
	for _, link := range links {
		if string(link.Type) == "superseded_by" || string(link.Type) == "supersedes" {
			return nil
		}
	}

	return []domain.ValidationError{{
		RuleID: r.ID(), Severity: "warning",
		Message: "superseded artifact has no supersedes/superseded_by link",
	}}
}

// SC-005: Task with blocked_by links should not be In Progress if blocker is not terminal.
type ruleSC005 struct{}

func (r *ruleSC005) ID() string { return "SC-005" }
func (r *ruleSC005) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}
	if proj.Status != string(domain.StatusInProgress) {
		return nil
	}

	links := parseLinks(proj)
	var errors []domain.ValidationError

	for _, link := range links {
		if string(link.Type) != "blocked_by" {
			continue
		}

		targetPath := strings.TrimPrefix(link.Target, "/")
		blocker, err := st.GetArtifactProjection(ctx, targetPath)
		if err != nil {
			continue
		}

		if !isTerminalStatus(blocker.Status) {
			errors = append(errors, domain.ValidationError{
				RuleID:   r.ID(),
				Severity: "warning",
				Message:  fmt.Sprintf("task is In Progress but blocker %s is %s", link.Target, blocker.Status),
			})
		}
	}
	return errors
}
