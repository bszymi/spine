package artifact_test

import (
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
)

func validInitiative() *domain.Artifact {
	return &domain.Artifact{
		Path:     "initiatives/INIT-001/initiative.md",
		ID:       "INIT-001",
		Type:     domain.ArtifactTypeInitiative,
		Title:    "Foundations",
		Status:   domain.StatusInProgress,
		Metadata: map[string]string{"created": "2026-03-04"},
	}
}

func validTask() *domain.Artifact {
	return &domain.Artifact{
		Path:   "tasks/TASK-001.md",
		ID:     "TASK-001",
		Type:   domain.ArtifactTypeTask,
		Title:  "Test Task",
		Status: domain.StatusPending,
		Metadata: map[string]string{
			"epic":       "/path/to/epic.md",
			"initiative": "/path/to/init.md",
		},
	}
}

func validADR() *domain.Artifact {
	return &domain.Artifact{
		Path:   "architecture/adr/ADR-001.md",
		ID:     "ADR-001",
		Type:   domain.ArtifactTypeADR,
		Title:  "Test ADR",
		Status: domain.StatusAccepted,
		Metadata: map[string]string{
			"date":            "2026-03-11",
			"decision_makers": "Spine Architecture",
		},
	}
}

func validGovernance() *domain.Artifact {
	return &domain.Artifact{
		Path:   "governance/test.md",
		Type:   domain.ArtifactTypeGovernance,
		Title:  "Test Doc",
		Status: domain.StatusLivingDocument,
	}
}

// ── Valid artifacts pass ──

func TestValidateValidInitiative(t *testing.T) {
	r := artifact.Validate(validInitiative())
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

func TestValidateValidTask(t *testing.T) {
	r := artifact.Validate(validTask())
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

func TestValidateValidADR(t *testing.T) {
	r := artifact.Validate(validADR())
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

func TestValidateValidGovernance(t *testing.T) {
	r := artifact.Validate(validGovernance())
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

func TestValidateValidEpic(t *testing.T) {
	a := &domain.Artifact{
		Path:     "epics/EPIC-001/epic.md",
		ID:       "EPIC-001",
		Type:     domain.ArtifactTypeEpic,
		Title:    "Test Epic",
		Status:   domain.StatusPending,
		Metadata: map[string]string{"initiative": "/path/to/init.md"},
	}
	r := artifact.Validate(a)
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

func TestValidateValidArchitecture(t *testing.T) {
	a := &domain.Artifact{
		Path:   "architecture/test.md",
		Type:   domain.ArtifactTypeArchitecture,
		Title:  "Test Doc",
		Status: domain.StatusStable,
	}
	r := artifact.Validate(a)
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

func TestValidateValidProduct(t *testing.T) {
	a := &domain.Artifact{
		Path:   "product/test.md",
		Type:   domain.ArtifactTypeProduct,
		Title:  "Test Doc",
		Status: domain.StatusLivingDocument,
	}
	r := artifact.Validate(a)
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

// ── Required field validation ──

func TestValidateMissingType(t *testing.T) {
	a := validGovernance()
	a.Type = ""
	r := artifact.Validate(a)
	assertHasError(t, r, "type", "required field missing")
}

func TestValidateMissingTitle(t *testing.T) {
	a := validGovernance()
	a.Title = ""
	r := artifact.Validate(a)
	assertHasError(t, r, "title", "required field missing")
}

func TestValidateMissingStatus(t *testing.T) {
	a := validGovernance()
	a.Status = ""
	r := artifact.Validate(a)
	assertHasError(t, r, "status", "required field missing")
}

func TestValidateUnknownType(t *testing.T) {
	a := validGovernance()
	a.Type = "Goverance"
	r := artifact.Validate(a)
	assertHasError(t, r, "type", "unknown artifact type")
}

// ── Status enum validation (§6) ──

func TestValidateInvalidStatusForTask(t *testing.T) {
	a := validTask()
	a.Status = domain.StatusInProgress // not valid for Task
	r := artifact.Validate(a)
	assertHasError(t, r, "status", "invalid status")
}

func TestValidateInvalidStatusForADR(t *testing.T) {
	a := validADR()
	a.Status = domain.StatusPending // not valid for ADR
	r := artifact.Validate(a)
	assertHasError(t, r, "status", "invalid status")
}

func TestValidateInvalidStatusForGovernance(t *testing.T) {
	a := validGovernance()
	a.Status = domain.StatusDraft // not valid for Governance
	r := artifact.Validate(a)
	assertHasError(t, r, "status", "invalid status")
}

// ── Type-specific required fields (§5) ──

func TestValidateInitiativeMissingCreated(t *testing.T) {
	a := validInitiative()
	delete(a.Metadata, "created")
	r := artifact.Validate(a)
	assertHasError(t, r, "created", "required for Initiative")
}

func TestValidateInitiativeMissingID(t *testing.T) {
	a := validInitiative()
	a.ID = ""
	r := artifact.Validate(a)
	assertHasError(t, r, "id", "required for Initiative")
}

func TestValidateEpicMissingInitiative(t *testing.T) {
	a := &domain.Artifact{
		Path:     "epics/EPIC-001/epic.md",
		ID:       "EPIC-001",
		Type:     domain.ArtifactTypeEpic,
		Title:    "Epic",
		Status:   domain.StatusPending,
		Metadata: map[string]string{},
	}
	r := artifact.Validate(a)
	assertHasError(t, r, "initiative", "required for Epic")
}

func TestValidateTaskMissingEpic(t *testing.T) {
	a := validTask()
	delete(a.Metadata, "epic")
	r := artifact.Validate(a)
	assertHasError(t, r, "epic", "required for Task")
}

func TestValidateTaskMissingInitiative(t *testing.T) {
	a := validTask()
	delete(a.Metadata, "initiative")
	r := artifact.Validate(a)
	assertHasError(t, r, "initiative", "required for Task")
}

func TestValidateADRMissingDate(t *testing.T) {
	a := validADR()
	delete(a.Metadata, "date")
	r := artifact.Validate(a)
	assertHasError(t, r, "date", "required for ADR")
}

func TestValidateADRMissingDecisionMakers(t *testing.T) {
	a := validADR()
	delete(a.Metadata, "decision_makers")
	r := artifact.Validate(a)
	assertHasError(t, r, "decision_makers", "required for ADR")
}

// ── ID format validation (naming-conventions.md §2) ──

func TestValidateValidIDFormats(t *testing.T) {
	tests := []struct {
		name string
		a    *domain.Artifact
	}{
		{"INIT-001", &domain.Artifact{Path: "test.md", ID: "INIT-001", Type: domain.ArtifactTypeInitiative, Title: "T", Status: domain.StatusDraft, Metadata: map[string]string{"created": "2026-01-01"}}},
		{"EPIC-099", &domain.Artifact{Path: "test.md", ID: "EPIC-099", Type: domain.ArtifactTypeEpic, Title: "T", Status: domain.StatusDraft, Metadata: map[string]string{"initiative": "/i.md"}}},
		{"TASK-123", &domain.Artifact{Path: "test.md", ID: "TASK-123", Type: domain.ArtifactTypeTask, Title: "T", Status: domain.StatusPending, Metadata: map[string]string{"epic": "/e.md", "initiative": "/i.md"}}},
		{"ADR-001", &domain.Artifact{Path: "test.md", ID: "ADR-001", Type: domain.ArtifactTypeADR, Title: "T", Status: domain.StatusProposed, Metadata: map[string]string{"date": "2026-01-01", "decision_makers": "Team"}}},
		{"ADR-0001", &domain.Artifact{Path: "test.md", ID: "ADR-0001", Type: domain.ArtifactTypeADR, Title: "T", Status: domain.StatusProposed, Metadata: map[string]string{"date": "2026-01-01", "decision_makers": "Team"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := artifact.Validate(tt.a)
			if r.Status != "passed" {
				t.Errorf("expected passed for ID %s, got %s: %+v", tt.a.ID, r.Status, r.Errors)
			}
		})
	}
}

func TestValidateInvalidIDFormats(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		artType  domain.ArtifactType
		metadata map[string]string
	}{
		{"INIT no padding", "INIT-1", domain.ArtifactTypeInitiative, map[string]string{"created": "2026-01-01"}},
		{"EPIC wrong prefix", "TASK-001", domain.ArtifactTypeEpic, map[string]string{"initiative": "/i.md"}},
		{"TASK too many digits", "TASK-1234", domain.ArtifactTypeTask, map[string]string{"epic": "/e.md", "initiative": "/i.md"}},
		{"ADR no digits", "ADR-", domain.ArtifactTypeADR, map[string]string{"date": "2026-01-01", "decision_makers": "T"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &domain.Artifact{
				Path:     "test.md",
				ID:       tt.id,
				Type:     tt.artType,
				Title:    "T",
				Status:   domain.StatusDraft,
				Metadata: tt.metadata,
			}
			r := artifact.Validate(a)
			assertHasError(t, r, "id", "invalid ID format")
		})
	}
}

// ── Link validation (§3-4) ──

func TestValidateValidLinks(t *testing.T) {
	a := validGovernance()
	a.Links = []domain.Link{
		{Type: "parent", Target: "/path/to/parent.md"},
		{Type: "related_to", Target: "/path/to/related.md"},
		{Type: "blocked_by", Target: "/path/to/blocker.md"},
		{Type: "supersedes", Target: "/path/to/old.md"},
		{Type: "follow_up_to", Target: "/path/to/original.md"},
	}
	r := artifact.Validate(a)
	if r.Status != "passed" {
		t.Errorf("expected passed, got %s: %+v", r.Status, r.Errors)
	}
}

func TestValidateInvalidLinkType(t *testing.T) {
	a := validGovernance()
	a.Links = []domain.Link{
		{Type: "depends_on", Target: "/path.md"},
	}
	r := artifact.Validate(a)
	assertHasError(t, r, "links[0].type", "unknown link type")
}

func TestValidateEmptyLinkTarget(t *testing.T) {
	a := validGovernance()
	a.Links = []domain.Link{
		{Type: "parent", Target: ""},
	}
	r := artifact.Validate(a)
	assertHasError(t, r, "links[0].target", "link target is required")
}

func TestValidateNonCanonicalLinkTarget(t *testing.T) {
	a := validGovernance()
	a.Links = []domain.Link{
		{Type: "parent", Target: "relative/path.md"},
	}
	r := artifact.Validate(a)
	assertHasError(t, r, "links[0].target", "canonical path starting with /")
}

// ── Composable validation (ValidateField) ──

func TestValidateFieldType(t *testing.T) {
	a := validGovernance()
	if err := artifact.ValidateField(a, "type"); err != nil {
		t.Errorf("expected valid type, got: %v", err)
	}

	a.Type = ""
	if err := artifact.ValidateField(a, "type"); err == nil {
		t.Error("expected error for empty type")
	}
}

func TestValidateFieldStatus(t *testing.T) {
	a := validGovernance()
	if err := artifact.ValidateField(a, "status"); err != nil {
		t.Errorf("expected valid status, got: %v", err)
	}

	a.Status = "InvalidStatus"
	if err := artifact.ValidateField(a, "status"); err == nil {
		t.Error("expected error for invalid status")
	}
}

func TestValidateFieldID(t *testing.T) {
	a := validInitiative()
	if err := artifact.ValidateField(a, "id"); err != nil {
		t.Errorf("expected valid ID, got: %v", err)
	}

	a.ID = "BAD"
	if err := artifact.ValidateField(a, "id"); err == nil {
		t.Error("expected error for bad ID format")
	}
}

func TestValidateFieldTitle(t *testing.T) {
	a := validGovernance()
	if err := artifact.ValidateField(a, "title"); err != nil {
		t.Errorf("expected valid title, got: %v", err)
	}

	a.Title = ""
	if err := artifact.ValidateField(a, "title"); err == nil {
		t.Error("expected error for empty title")
	}
}

// ── Canonical path validation for initiative/epic ──

func TestValidateEpicNonCanonicalInitiative(t *testing.T) {
	a := &domain.Artifact{
		Path:     "epics/EPIC-001/epic.md",
		ID:       "EPIC-001",
		Type:     domain.ArtifactTypeEpic,
		Title:    "Epic",
		Status:   domain.StatusPending,
		Metadata: map[string]string{"initiative": "relative/path.md"},
	}
	r := artifact.Validate(a)
	assertHasError(t, r, "initiative", "canonical path starting with /")
}

func TestValidateTaskNonCanonicalEpic(t *testing.T) {
	a := validTask()
	a.Metadata["epic"] = "relative/epic.md"
	r := artifact.Validate(a)
	assertHasError(t, r, "epic", "canonical path starting with /")
}

func TestValidateTaskNonCanonicalInitiative(t *testing.T) {
	a := validTask()
	a.Metadata["initiative"] = "../init.md"
	r := artifact.Validate(a)
	assertHasError(t, r, "initiative", "canonical path starting with /")
}

// ── Date format validation ──

func TestValidateInitiativeInvalidDateFormat(t *testing.T) {
	a := validInitiative()
	a.Metadata["created"] = "03/04/2026"
	r := artifact.Validate(a)
	assertHasError(t, r, "created", "invalid date format")
}

func TestValidateADRInvalidDateFormat(t *testing.T) {
	a := validADR()
	a.Metadata["date"] = "yesterday"
	r := artifact.Validate(a)
	assertHasError(t, r, "date", "invalid date format")
}

// ── ValidateField ID required ──

func TestValidateFieldIDRequiredForTask(t *testing.T) {
	a := validTask()
	a.ID = ""
	if err := artifact.ValidateField(a, "id"); err == nil {
		t.Error("expected error for missing ID on Task")
	}
}

func TestValidateFieldIDNotRequiredForGovernance(t *testing.T) {
	a := validGovernance()
	if err := artifact.ValidateField(a, "id"); err != nil {
		t.Errorf("governance should not require ID, got: %v", err)
	}
}

// ── Multiple errors ──

func TestValidateMultipleErrors(t *testing.T) {
	a := &domain.Artifact{
		Path: "bad.md",
		Type: domain.ArtifactTypeTask,
		// Missing: title, status, id, epic, initiative
		Metadata: map[string]string{},
	}
	r := artifact.Validate(a)
	if r.Status != "failed" {
		t.Errorf("expected failed, got %s", r.Status)
	}
	if len(r.Errors) < 4 {
		t.Errorf("expected at least 4 errors, got %d: %+v", len(r.Errors), r.Errors)
	}
}

// ── helpers ──

func assertHasError(t *testing.T, r domain.ValidationResult, field, msgSubstring string) {
	t.Helper()
	if r.Status != "failed" {
		t.Errorf("expected failed, got %s", r.Status)
		return
	}
	for _, e := range r.Errors {
		if e.Field == field && contains(e.Message, msgSubstring) {
			return
		}
	}
	t.Errorf("expected error on field %q containing %q, got: %+v", field, msgSubstring, r.Errors)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
