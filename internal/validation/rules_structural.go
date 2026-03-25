package validation

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func structuralRules() []Rule {
	return []Rule{&ruleSI001{}, &ruleSI002{}, &ruleSI003{}, &ruleSI004{}, &ruleSI005{}}
}

// SI-001: Parent artifact must exist.
type ruleSI001 struct{}

func (r *ruleSI001) ID() string { return "SI-001" }
func (r *ruleSI001) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleSI001) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	meta := parseMetadata(proj)
	var parentPath string

	switch domain.ArtifactType(proj.ArtifactType) {
	case domain.ArtifactTypeTask:
		parentPath = meta["epic"]
	case domain.ArtifactTypeEpic:
		parentPath = meta["initiative"]
	default:
		return nil
	}

	if parentPath == "" {
		return nil // missing parent field handled by schema validation
	}

	// Strip leading / for projection lookup
	lookupPath := strings.TrimPrefix(parentPath, "/")
	if _, err := st.GetArtifactProjection(ctx, lookupPath); err != nil {
		return []domain.ValidationError{{
			RuleID: r.ID(), Severity: "error",
			Message: fmt.Sprintf("parent artifact not found: %s", parentPath),
		}}
	}
	return nil
}

// SI-002: Parent must be correct type.
type ruleSI002 struct{}

func (r *ruleSI002) ID() string { return "SI-002" }
func (r *ruleSI002) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleSI002) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	meta := parseMetadata(proj)
	var parentPath, expectedType string

	switch domain.ArtifactType(proj.ArtifactType) {
	case domain.ArtifactTypeTask:
		parentPath = meta["epic"]
		expectedType = string(domain.ArtifactTypeEpic)
	case domain.ArtifactTypeEpic:
		parentPath = meta["initiative"]
		expectedType = string(domain.ArtifactTypeInitiative)
	default:
		return nil
	}

	if parentPath == "" {
		return nil
	}

	lookupPath := strings.TrimPrefix(parentPath, "/")
	parent, err := st.GetArtifactProjection(ctx, lookupPath)
	if err != nil {
		return nil // SI-001 handles missing parent
	}

	if parent.ArtifactType != expectedType {
		return []domain.ValidationError{{
			RuleID: r.ID(), Severity: "error",
			Message: fmt.Sprintf("parent %s is type %s, expected %s", parentPath, parent.ArtifactType, expectedType),
		}}
	}
	return nil
}

// SI-003: Parent must not be terminal when child is In Progress.
type ruleSI003 struct{}

func (r *ruleSI003) ID() string { return "SI-003" }
func (r *ruleSI003) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleSI003) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if proj.Status != string(domain.StatusInProgress) {
		return nil
	}

	meta := parseMetadata(proj)
	var parentPath string
	switch domain.ArtifactType(proj.ArtifactType) {
	case domain.ArtifactTypeTask:
		parentPath = meta["epic"]
	case domain.ArtifactTypeEpic:
		parentPath = meta["initiative"]
	default:
		return nil
	}

	if parentPath == "" {
		return nil
	}

	lookupPath := strings.TrimPrefix(parentPath, "/")
	parent, err := st.GetArtifactProjection(ctx, lookupPath)
	if err != nil {
		return nil
	}

	if isTerminalStatus(parent.Status) {
		return []domain.ValidationError{{
			RuleID: r.ID(), Severity: "warning",
			Message: fmt.Sprintf("parent %s is %s while child is In Progress", parentPath, parent.Status),
		}}
	}
	return nil
}

// SI-004: Task's initiative must match parent Epic's initiative.
type ruleSI004 struct{}

func (r *ruleSI004) ID() string { return "SI-004" }
func (r *ruleSI004) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleSI004) Evaluate(ctx context.Context, proj *store.ArtifactProjection, st store.Store) []domain.ValidationError {
	if domain.ArtifactType(proj.ArtifactType) != domain.ArtifactTypeTask {
		return nil
	}

	meta := parseMetadata(proj)
	epicPath := meta["epic"]
	taskInitiative := meta["initiative"]
	if epicPath == "" || taskInitiative == "" {
		return nil
	}

	lookupPath := strings.TrimPrefix(epicPath, "/")
	parent, err := st.GetArtifactProjection(ctx, lookupPath)
	if err != nil {
		return nil
	}

	parentMeta := parseMetadata(parent)
	if parentMeta["initiative"] != taskInitiative {
		return []domain.ValidationError{{
			RuleID: r.ID(), Severity: "error",
			Message: fmt.Sprintf("task initiative %s does not match epic's initiative %s", taskInitiative, parentMeta["initiative"]),
		}}
	}
	return nil
}

// SI-005: Artifact type must match repository location convention.
type ruleSI005 struct{}

func (r *ruleSI005) ID() string { return "SI-005" }
func (r *ruleSI005) Classification() domain.ViolationClassification {
	return domain.ViolationStructuralError
}
func (r *ruleSI005) Evaluate(_ context.Context, proj *store.ArtifactProjection, _ store.Store) []domain.ValidationError {
	path := proj.ArtifactPath
	artType := domain.ArtifactType(proj.ArtifactType)

	expectedPrefixes := map[domain.ArtifactType]string{
		domain.ArtifactTypeInitiative:   "initiatives/",
		domain.ArtifactTypeEpic:         "initiatives/",
		domain.ArtifactTypeTask:         "initiatives/",
		domain.ArtifactTypeADR:          "architecture/adr/",
		domain.ArtifactTypeArchitecture: "architecture/",
		domain.ArtifactTypeGovernance:   "governance/",
		domain.ArtifactTypeProduct:      "product/",
	}

	prefix, ok := expectedPrefixes[artType]
	if !ok {
		return nil
	}

	if !strings.HasPrefix(path, prefix) {
		return []domain.ValidationError{{
			RuleID: r.ID(), Severity: "error",
			Message: fmt.Sprintf("artifact type %s at path %s does not match expected prefix %s", artType, path, prefix),
		}}
	}
	return nil
}
