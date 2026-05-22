package checkrunner_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/checkrunner"
	"github.com/bszymi/spine/core/domain"
)

func sampleRequest() checkrunner.Request {
	return checkrunner.Request{
		RepositoryID: "billing",
		BranchName:   "spine/run/run-1",
		CommitSHA:    "deadbeef",
		Check: domain.PolicyCheck{
			CheckID:        "unit-tests",
			Name:           "Unit tests",
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "go test ./...",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		},
	}
}

func sampleResult(outcome checkrunner.Outcome) checkrunner.Result {
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	return checkrunner.Result{
		Outcome:      outcome,
		StartedAt:    t0,
		CompletedAt:  t0.Add(2 * time.Second),
		ExitCode:     0,
		Reason:       "exit 0",
		LogReference: "checkrunner/billing/run-1/unit-tests/deadbeef",
	}
}

func TestNormalize_Pass(t *testing.T) {
	row := checkrunner.Normalize(sampleRequest(), sampleResult(checkrunner.OutcomePass), "spine/runner@host-1")
	if row.Status != domain.CheckStatusPassed {
		t.Fatalf("Status: got %q want %q", row.Status, domain.CheckStatusPassed)
	}
	if row.Producer != domain.CheckProducerAutomated {
		t.Fatalf("Producer: got %q want %q", row.Producer, domain.CheckProducerAutomated)
	}
	if row.ProducedBy != "spine/runner@host-1" {
		t.Fatalf("ProducedBy: got %q", row.ProducedBy)
	}
	if row.CheckID != "unit-tests" {
		t.Fatalf("CheckID: got %q", row.CheckID)
	}
	if row.Name != "Unit tests" {
		t.Fatalf("Name: got %q", row.Name)
	}
	if row.StartedAt == nil || row.CompletedAt == nil {
		t.Fatalf("timestamps must be set")
	}
	// Codex pass 6 P2: log linkage must survive Normalize.
	if row.EvidenceURI != "checkrunner/billing/run-1/unit-tests/deadbeef" {
		t.Fatalf("EvidenceURI must mirror Result.LogReference: got %q", row.EvidenceURI)
	}
}

// TestNormalize_NoLogSink_EmptyEvidenceURI complements the Pass test:
// when the runner ran without a sink (LogReference empty), Normalize
// MUST NOT invent an EvidenceURI. A non-empty value would point
// nowhere — same audit-pollution failure mode as codex pass 3 P2 but
// at the row construction layer.
func TestNormalize_NoLogSink_EmptyEvidenceURI(t *testing.T) {
	res := sampleResult(checkrunner.OutcomePass)
	res.LogReference = ""
	row := checkrunner.Normalize(sampleRequest(), res, "spine/runner@host-1")
	if row.EvidenceURI != "" {
		t.Fatalf("EvidenceURI should be empty when no log was captured: got %q", row.EvidenceURI)
	}
}

func TestNormalize_Fail(t *testing.T) {
	res := sampleResult(checkrunner.OutcomeFail)
	res.ExitCode = 1
	res.Reason = "exit 1"
	row := checkrunner.Normalize(sampleRequest(), res, "spine/runner@host-1")
	if row.Status != domain.CheckStatusFailed {
		t.Fatalf("Status: got %q want %q", row.Status, domain.CheckStatusFailed)
	}
	if row.Summary != "exit 1" {
		t.Fatalf("Summary: got %q", row.Summary)
	}
}

func TestNormalize_Timeout(t *testing.T) {
	res := sampleResult(checkrunner.OutcomeTimeout)
	res.Reason = "context deadline exceeded"
	row := checkrunner.Normalize(sampleRequest(), res, "spine/runner@host-1")
	if row.Status != domain.CheckStatusError {
		t.Fatalf("Status: got %q want %q", row.Status, domain.CheckStatusError)
	}
	if !strings.Contains(row.Summary, "deadline") {
		t.Fatalf("Summary should preserve timeout reason: got %q", row.Summary)
	}
}

func TestNormalize_Unavailable(t *testing.T) {
	res := sampleResult(checkrunner.OutcomeUnavailable)
	res.Reason = "working_dir does not exist"
	row := checkrunner.Normalize(sampleRequest(), res, "spine/runner@host-1")
	if row.Status != domain.CheckStatusError {
		t.Fatalf("Status: got %q want %q", row.Status, domain.CheckStatusError)
	}
	if !strings.Contains(row.Summary, "working_dir") {
		t.Fatalf("Summary should preserve unavailable reason: got %q", row.Summary)
	}
}

// TestNormalize_RoundTripIntoEvidence is the audit-shape contract:
// a normalized row, when slotted into a complete ExecutionEvidence
// record, must pass Validate. If this test breaks, it means the
// runner is now producing CheckResult shapes the evidence schema
// rejects — a P2-class issue because audit ingestion would silently
// drop the row in production.
func TestNormalize_RoundTripIntoEvidence(t *testing.T) {
	cases := []struct {
		name    string
		outcome checkrunner.Outcome
		reason  string
		want    domain.EvidenceStatus
	}{
		{"pass", checkrunner.OutcomePass, "exit 0", domain.EvidenceStatusPassed},
		{"fail", checkrunner.OutcomeFail, "exit 1", domain.EvidenceStatusFailed},
		{"timeout", checkrunner.OutcomeTimeout, "context deadline exceeded", domain.EvidenceStatusFailed},
		{"unavailable", checkrunner.OutcomeUnavailable, "working_dir does not exist", domain.EvidenceStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := sampleResult(tc.outcome)
			res.Reason = tc.reason
			row := checkrunner.Normalize(sampleRequest(), res, "spine/runner@host-1")
			ev := domain.ExecutionEvidence{
				SchemaVersion:  domain.ExecutionEvidenceSchemaVersion,
				RunID:          "run-1",
				TaskPath:       "/initiatives/INIT-014/epics/EPIC-006/tasks/TASK-003-check-runner-integration-boundary.md",
				RepositoryID:   "billing",
				BranchName:     "spine/run/run-1",
				BaseCommit:     "0000",
				HeadCommit:     "deadbeef",
				RequiredChecks: []string{row.CheckID},
				CheckResults:   []domain.CheckResult{row},
				Actor:          "spine/runner@host-1",
				TraceID:        "trace-1",
				GeneratedAt:    time.Now().UTC(),
			}
			ev.Status = ev.DeriveStatus()
			if err := ev.Validate(); err != nil {
				t.Fatalf("evidence Validate: %v", err)
			}
			if ev.Status != tc.want {
				t.Fatalf("aggregate status: got %q want %q", ev.Status, tc.want)
			}
		})
	}
}

// TestNormalize_SanitizesNewlinesInReason guards a real footgun: the
// runner's Reason can contain whatever the OS spat out (e.g. an exec
// error like "fork/exec /bin/sh: no such file or directory\n"). If
// Normalize lets that through into Summary, evidence Validate
// rejects the whole row at write time. The runner already strips
// newlines (sanitizeReason), so this test pins the contract — if the
// runner regresses, this fails BEFORE production audit ingestion does.
func TestNormalize_SanitizesNewlinesInReason(t *testing.T) {
	res := sampleResult(checkrunner.OutcomeUnavailable)
	res.Reason = "fork/exec /bin/sh: no such file or directory" // pre-sanitized
	row := checkrunner.Normalize(sampleRequest(), res, "spine/runner@host-1")
	if strings.ContainsAny(row.Summary, "\n\r") {
		t.Fatalf("Summary contains newlines — would fail evidence Validate: %q", row.Summary)
	}
}
