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
func (r *ruleSC001) Classification() domain.ViolationClassification {
	return domain.ViolationStatusConflict
}
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
func (r *ruleSC002) Classification() domain.ViolationClassification {
	return domain.ViolationStatusConflict
}
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
func (r *ruleSC003) Classification() domain.ViolationClassification {
	return domain.ViolationStatusConflict
}
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
	parentPath := "/" + parent.ArtifactPath
	var errors []domain.ValidationError
	// Walk the cursor instead of fetching a fixed Limit:500 page —
	// the store now clamps to ArtifactQueryMaxLimit, and a workspace
	// with more children than that would otherwise have its tail
	// pages silently dropped from this rule.
	cursor := ""
	for {
		result, err := st.QueryArtifacts(ctx, store.ArtifactQuery{
			Type:   string(childType),
			Limit:  store.ArtifactQueryMaxLimit,
			Cursor: cursor,
		})
		if err != nil {
			return nil
		}
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
		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}
	return errors
}

// SC-004: Superseded artifact should have supersedes/superseded_by link.
type ruleSC004 struct{}

func (r *ruleSC004) ID() string { return "SC-004" }
func (r *ruleSC004) Classification() domain.ViolationClassification {
	return domain.ViolationStatusConflict
}
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
func (r *ruleSC005) Classification() domain.ViolationClassification {
	return domain.ViolationStatusConflict
}
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
