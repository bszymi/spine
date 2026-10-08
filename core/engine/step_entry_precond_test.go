package engine

// INIT-023 TASK-007 — strict precondition evaluation (AC 9, 10).

import (
	"context"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

func preconditionOrch(md []byte, p StepEntryPolicy) (*Orchestrator, *domain.Run) {
	run := runFixture("run-1", domain.RunStatusActive)
	run.TaskPath = "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001.md"
	o := &Orchestrator{artifacts: &legacyRefReader{byRef: map[string][]byte{"HEAD": md}}}
	o.WithStepEntryPolicy(p)
	return o, run
}

func typedFieldsTask(status string) []byte {
	return []byte("---\nid: TASK-001\ntype: Task\ntitle: Do The Thing\nstatus: " + status +
		"\nowner: bszymi\nepic: /initiatives/INIT-001/epics/EPIC-001/epic.md\n" +
		"initiative: /initiatives/INIT-001/initiative.md\n---\n# T\n")
}

// From poc/skeptic-precond: the parser keeps id/type/title/status out of
// Metadata, so `field_value status` could never pass. Under
// EnforcePreconditions those four keys read the typed fields; every other
// key, and the zero value, keep reading Metadata.
// Mutation: removing the `status` case from preconditionField fails the
// field_value-status and field_present-status rows.
func TestStepEntry_FieldValueReadsTypedFields(t *testing.T) {
	fv := func(field, value string) domain.Precondition {
		return domain.Precondition{Type: "field_value", Config: map[string]string{"field": field, "value": value}}
	}
	fp := func(field string) domain.Precondition {
		return domain.Precondition{Type: "field_present", Config: map[string]string{"field": field}}
	}
	cases := []struct {
		name       string
		status     string
		pre        domain.Precondition
		wantZero   bool
		wantStrict bool
	}{
		{"field_value status matches", "In Progress", fv("status", "In Progress"), false, true},
		{"field_value status differs", "Pending", fv("status", "In Progress"), false, false},
		{"field_value id matches", "Pending", fv("id", "TASK-001"), false, true},
		{"field_value id differs", "Pending", fv("id", "TASK-999"), false, false},
		{"field_value type matches", "Pending", fv("type", "Task"), false, true},
		{"field_value type differs", "Pending", fv("type", "Epic"), false, false},
		{"field_value title matches", "Pending", fv("title", "Do The Thing"), false, true},
		{"field_value title differs", "Pending", fv("title", "Other"), false, false},
		{"field_present status", "Pending", fp("status"), false, true},
		{"field_present id", "Pending", fp("id"), false, true},
		{"field_present type", "Pending", fp("type"), false, true},
		{"field_present title", "Pending", fp("title"), false, true},
		{"metadata key unchanged (value)", "Pending", fv("owner", "bszymi"), true, true},
		{"metadata key unchanged (present)", "Pending", fp("owner"), true, true},
		{"absent metadata key", "Pending", fp("reviewer"), false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			step := &domain.StepDefinition{ID: "s", Preconditions: []domain.Precondition{tc.pre}}
			o, run := preconditionOrch(typedFieldsTask(tc.status), StepEntryPolicy{})
			if got, _ := o.evaluatePreconditions(context.Background(), step, run); got != tc.wantZero {
				t.Fatalf("zero value: evaluatePreconditions = %v, want %v", got, tc.wantZero)
			}
			o, run = preconditionOrch(typedFieldsTask(tc.status), StepEntryPolicy{EnforcePreconditions: true})
			if got, _ := o.evaluatePreconditions(context.Background(), step, run); got != tc.wantStrict {
				t.Fatalf("EnforcePreconditions: evaluatePreconditions = %v, want %v", got, tc.wantStrict)
			}
		})
	}
}

// TestStepEntry_StrictPreconditionsFailClosed: the evaluator's three
// forward-compatible skips refuse under EnforcePreconditions (Owner
// question 4) and stay skips at the zero value.
func TestStepEntry_StrictPreconditionsFailClosed(t *testing.T) {
	cases := []struct {
		name string
		pre  domain.Precondition
	}{
		{"documented but unimplemented custom type", domain.Precondition{Type: "custom", Config: map[string]string{"check": "anything"}}},
		{"unknown type", domain.Precondition{Type: "bogus"}},
		{"cross_artifact_valid without a validator", domain.Precondition{Type: "cross_artifact_valid"}},
		{"discussions_resolved without a checker", domain.Precondition{Type: "discussions_resolved"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			step := &domain.StepDefinition{ID: "s", Preconditions: []domain.Precondition{tc.pre}}
			o, run := preconditionOrch(typedFieldsTask("Pending"), StepEntryPolicy{})
			if got, _ := o.evaluatePreconditions(context.Background(), step, run); !got {
				t.Fatal("zero value must keep the forward-compatible skip (pass)")
			}
			o, run = preconditionOrch(typedFieldsTask("Pending"), StepEntryPolicy{EnforcePreconditions: true})
			if got, _ := o.evaluatePreconditions(context.Background(), step, run); got {
				t.Fatal("EnforcePreconditions must fail closed")
			}
		})
	}
}
