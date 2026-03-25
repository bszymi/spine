package domain_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestValidStatusesForType(t *testing.T) {
	tests := []struct {
		artifactType   domain.ArtifactType
		expectCount    int
		mustContain    domain.ArtifactStatus
		mustNotContain domain.ArtifactStatus
	}{
		{domain.ArtifactTypeInitiative, 5, domain.StatusInProgress, domain.StatusCancelled},
		{domain.ArtifactTypeEpic, 5, domain.StatusInProgress, domain.StatusAbandoned},
		{domain.ArtifactTypeTask, 7, domain.StatusCompleted, domain.StatusInProgress},
		{domain.ArtifactTypeADR, 4, domain.StatusProposed, domain.StatusPending},
		{domain.ArtifactTypeGovernance, 3, domain.StatusLivingDocument, domain.StatusStable},
		{domain.ArtifactTypeArchitecture, 3, domain.StatusStable, domain.StatusFoundational},
		{domain.ArtifactTypeProduct, 3, domain.StatusStable, domain.StatusFoundational},
	}

	for _, tt := range tests {
		t.Run(string(tt.artifactType), func(t *testing.T) {
			statuses := domain.ValidStatusesForType(tt.artifactType)
			if len(statuses) != tt.expectCount {
				t.Errorf("expected %d statuses, got %d: %v", tt.expectCount, len(statuses), statuses)
			}

			found := false
			for _, s := range statuses {
				if s == tt.mustContain {
					found = true
				}
				if s == tt.mustNotContain {
					t.Errorf("status %q should not be valid for %s", tt.mustNotContain, tt.artifactType)
				}
			}
			if !found {
				t.Errorf("status %q should be valid for %s", tt.mustContain, tt.artifactType)
			}
		})
	}
}

func TestValidStatusesForUnknownType(t *testing.T) {
	statuses := domain.ValidStatusesForType("Unknown")
	if statuses != nil {
		t.Errorf("expected nil for unknown type, got %v", statuses)
	}
}

func TestRunStatusIsTerminal(t *testing.T) {
	terminal := []domain.RunStatus{domain.RunStatusCompleted, domain.RunStatusFailed, domain.RunStatusCancelled}
	nonTerminal := []domain.RunStatus{domain.RunStatusPending, domain.RunStatusActive, domain.RunStatusPaused, domain.RunStatusCommitting}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("expected %q to be terminal", s)
		}
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("expected %q to be non-terminal", s)
		}
	}
}

func TestStepExecutionStatusIsTerminal(t *testing.T) {
	terminal := []domain.StepExecutionStatus{domain.StepStatusCompleted, domain.StepStatusFailed, domain.StepStatusSkipped}
	nonTerminal := []domain.StepExecutionStatus{domain.StepStatusWaiting, domain.StepStatusAssigned, domain.StepStatusInProgress, domain.StepStatusBlocked}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("expected %q to be terminal", s)
		}
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("expected %q to be non-terminal", s)
		}
	}
}

func TestFailureClassificationIsRetryable(t *testing.T) {
	retryable := []domain.FailureClassification{domain.FailureTransient, domain.FailureActorUnavailable, domain.FailureInvalidResult}
	notRetryable := []domain.FailureClassification{domain.FailurePermanent, domain.FailureGitConflict, domain.FailureTimeout, domain.FailureValidation}

	for _, f := range retryable {
		if !f.IsRetryable() {
			t.Errorf("expected %q to be retryable", f)
		}
	}
	for _, f := range notRetryable {
		if f.IsRetryable() {
			t.Errorf("expected %q to not be retryable", f)
		}
	}
}

func TestActorRoleHasAtLeast(t *testing.T) {
	tests := []struct {
		role     domain.ActorRole
		required domain.ActorRole
		expect   bool
	}{
		{domain.RoleAdmin, domain.RoleReader, true},
		{domain.RoleAdmin, domain.RoleAdmin, true},
		{domain.RoleReader, domain.RoleContributor, false},
		{domain.RoleContributor, domain.RoleContributor, true},
		{domain.RoleReviewer, domain.RoleContributor, true},
		{domain.RoleOperator, domain.RoleReviewer, true},
		{domain.RoleContributor, domain.RoleReviewer, false},
	}

	for _, tt := range tests {
		name := string(tt.role) + "_has_at_least_" + string(tt.required)
		t.Run(name, func(t *testing.T) {
			got := tt.role.HasAtLeast(tt.required)
			if got != tt.expect {
				t.Errorf("%s.HasAtLeast(%s) = %v, want %v", tt.role, tt.required, got, tt.expect)
			}
		})
	}
}

func TestSpineError(t *testing.T) {
	err := domain.NewError(domain.ErrNotFound, "artifact not found")
	if err.Error() != "not_found: artifact not found" {
		t.Errorf("unexpected error string: %s", err.Error())
	}

	errWithDetail := domain.NewErrorWithDetail(domain.ErrValidationFailed, "invalid", domain.ValidationError{
		Field:   "status",
		Message: "invalid status",
	})
	if errWithDetail.Detail == nil {
		t.Error("expected detail to be set")
	}
}

func TestValidArtifactTypes(t *testing.T) {
	types := domain.ValidArtifactTypes()
	if len(types) != 7 {
		t.Errorf("expected 7 artifact types, got %d", len(types))
	}
}

func TestErrorDetailScan(t *testing.T) {
	ed := &domain.ErrorDetail{}

	// Scan nil
	if err := ed.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}

	// Scan valid JSON
	data := []byte(`{"classification":"transient_error","message":"timeout"}`)
	if err := ed.Scan(data); err != nil {
		t.Fatalf("Scan(json): %v", err)
	}
	if ed.Classification != domain.FailureTransient {
		t.Errorf("expected transient_error, got %s", ed.Classification)
	}
	if ed.Message != "timeout" {
		t.Errorf("expected 'timeout', got %s", ed.Message)
	}

	// Scan unexpected type
	if err := ed.Scan("not bytes"); err == nil {
		t.Fatal("expected error for non-[]byte type")
	}
}

func TestRoleLevelUnknown(t *testing.T) {
	var unknown domain.ActorRole = "unknown_role"
	if unknown.RoleLevel() != 0 {
		t.Errorf("expected 0 for unknown role, got %d", unknown.RoleLevel())
	}
}

func TestValidRunStatuses(t *testing.T) {
	statuses := domain.ValidRunStatuses()
	if len(statuses) != 7 {
		t.Errorf("expected 7 run statuses, got %d", len(statuses))
	}
}

func TestValidStepStatuses(t *testing.T) {
	statuses := domain.ValidStepStatuses()
	if len(statuses) != 7 {
		t.Errorf("expected 7 step statuses, got %d", len(statuses))
	}
}
