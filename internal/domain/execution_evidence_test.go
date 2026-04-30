package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"gopkg.in/yaml.v3"
)

func evidenceFixedTime() time.Time {
	return time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
}

// validEvidence returns a minimal-but-valid evidence record with one
// required check that has passed. Tests mutate copies for negative
// cases.
func validEvidence() domain.ExecutionEvidence {
	checkAt := evidenceFixedTime()
	return domain.ExecutionEvidence{
		SchemaVersion: domain.ExecutionEvidenceSchemaVersion,
		RunID:         "run-evidence-1",
		TaskPath:      "/initiatives/INIT-014/epics/EPIC-006/tasks/TASK-001-x.md",
		RepositoryID:  "payments-service",
		BranchName:    "spine/run/run-evidence-1",
		BaseCommit:    "base000000000000000000000000000000000000",
		HeadCommit:    "head000000000000000000000000000000000000",
		ChangedPaths: domain.ChangedPathsSummary{
			FilesChanged: 2,
			Insertions:   10,
			Deletions:    3,
			Paths:        []string{"cmd/main.go", "internal/api/handler.go"},
		},
		RequiredChecks: []string{"unit-tests"},
		CheckResults: []domain.CheckResult{
			{
				CheckID:     "unit-tests",
				Name:        "unit tests",
				Status:      domain.CheckStatusPassed,
				Producer:    domain.CheckProducerAutomated,
				ProducedBy:  "ci/github-actions",
				Summary:     "go test ./... passed (231 cases)",
				EvidenceURI: "https://ci.example.com/runs/123",
				StartedAt:   ptrTime(checkAt.Add(-2 * time.Minute)),
				CompletedAt: ptrTime(checkAt.Add(-30 * time.Second)),
			},
		},
		ValidationPolicies: []domain.ValidationPolicyRef{
			{ADRPath: "/architecture/adr/ADR-014-evidence.md", PolicyID: "code-quality-v1"},
		},
		Actor:       "user/alice",
		TraceID:     "trace-evidence-1",
		Status:      domain.EvidenceStatusPassed,
		GeneratedAt: checkAt,
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestCheckStatus_IsTerminal(t *testing.T) {
	cases := map[domain.CheckStatus]bool{
		domain.CheckStatusPending: false,
		domain.CheckStatusRunning: false,
		domain.CheckStatusPassed:  true,
		domain.CheckStatusFailed:  true,
		domain.CheckStatusSkipped: true,
		domain.CheckStatusError:   true,
	}
	for status, want := range cases {
		if got := status.IsTerminal(); got != want {
			t.Errorf("status %q IsTerminal: got %v, want %v", status, got, want)
		}
	}
}

func TestCheckStatus_IsSuccess(t *testing.T) {
	cases := map[domain.CheckStatus]bool{
		domain.CheckStatusPending: false,
		domain.CheckStatusRunning: false,
		domain.CheckStatusPassed:  true,
		domain.CheckStatusFailed:  false,
		domain.CheckStatusSkipped: true, // skipped = declared-and-not-applicable, counts as satisfied
		domain.CheckStatusError:   false,
	}
	for status, want := range cases {
		if got := status.IsSuccess(); got != want {
			t.Errorf("status %q IsSuccess: got %v, want %v", status, got, want)
		}
	}
}

func TestEvidenceStatus_IsTerminal(t *testing.T) {
	cases := map[domain.EvidenceStatus]bool{
		domain.EvidenceStatusPending: false,
		domain.EvidenceStatusPassed:  true,
		domain.EvidenceStatusFailed:  true,
	}
	for status, want := range cases {
		if got := status.IsTerminal(); got != want {
			t.Errorf("status %q IsTerminal: got %v, want %v", status, got, want)
		}
	}
}

func TestExecutionEvidence_DeriveStatus(t *testing.T) {
	cases := []struct {
		name           string
		required       []string
		results        []domain.CheckResult
		wantStatus     domain.EvidenceStatus
	}{
		{
			name:     "no required checks → passed (degenerate)",
			required: nil,
			results:  nil,
			wantStatus: domain.EvidenceStatusPassed,
		},
		{
			name:       "required without result → pending",
			required:   []string{"a", "b"},
			results:    []domain.CheckResult{checkPassed("a")},
			wantStatus: domain.EvidenceStatusPending,
		},
		{
			name:     "required with running result → pending",
			required: []string{"a"},
			results: []domain.CheckResult{
				{CheckID: "a", Status: domain.CheckStatusRunning, Producer: domain.CheckProducerAutomated, ProducedBy: "ci/x"},
			},
			wantStatus: domain.EvidenceStatusPending,
		},
		{
			name:       "all passed → passed",
			required:   []string{"a", "b"},
			results:    []domain.CheckResult{checkPassed("a"), checkPassed("b")},
			wantStatus: domain.EvidenceStatusPassed,
		},
		{
			name:     "skipped counts as success",
			required: []string{"a", "b"},
			results: []domain.CheckResult{
				checkPassed("a"),
				{CheckID: "b", Status: domain.CheckStatusSkipped, Producer: domain.CheckProducerAutomated, ProducedBy: "ci/x"},
			},
			wantStatus: domain.EvidenceStatusPassed,
		},
		{
			name:     "any failed → failed",
			required: []string{"a", "b"},
			results: []domain.CheckResult{
				checkPassed("a"),
				{CheckID: "b", Status: domain.CheckStatusFailed, Producer: domain.CheckProducerAutomated, ProducedBy: "ci/x"},
			},
			wantStatus: domain.EvidenceStatusFailed,
		},
		{
			name:     "any error → failed",
			required: []string{"a"},
			results: []domain.CheckResult{
				{CheckID: "a", Status: domain.CheckStatusError, Producer: domain.CheckProducerAutomated, ProducedBy: "ci/x"},
			},
			wantStatus: domain.EvidenceStatusFailed,
		},
		{
			name:     "missing one + failure on another → pending (missing wins)",
			required: []string{"a", "b"},
			results: []domain.CheckResult{
				{CheckID: "a", Status: domain.CheckStatusFailed, Producer: domain.CheckProducerAutomated, ProducedBy: "ci/x"},
			},
			wantStatus: domain.EvidenceStatusPending,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := domain.ExecutionEvidence{
				RequiredChecks: tc.required,
				CheckResults:   tc.results,
			}
			if got := e.DeriveStatus(); got != tc.wantStatus {
				t.Errorf("DeriveStatus: got %q, want %q", got, tc.wantStatus)
			}
		})
	}
}

func checkPassed(id string) domain.CheckResult {
	return domain.CheckResult{
		CheckID:    id,
		Status:     domain.CheckStatusPassed,
		Producer:   domain.CheckProducerAutomated,
		ProducedBy: "ci/test",
	}
}

func TestExecutionEvidence_Validate_Happy(t *testing.T) {
	e := validEvidence()
	if err := e.Validate(); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RequiredFields(t *testing.T) {
	cases := map[string]func(*domain.ExecutionEvidence){
		"missing schema_version": func(e *domain.ExecutionEvidence) { e.SchemaVersion = "" },
		"missing run_id":         func(e *domain.ExecutionEvidence) { e.RunID = "" },
		"missing task_path":      func(e *domain.ExecutionEvidence) { e.TaskPath = "" },
		"missing repository_id":  func(e *domain.ExecutionEvidence) { e.RepositoryID = "" },
		"missing branch_name":    func(e *domain.ExecutionEvidence) { e.BranchName = "" },
		"missing base_commit":    func(e *domain.ExecutionEvidence) { e.BaseCommit = "" },
		"missing head_commit":    func(e *domain.ExecutionEvidence) { e.HeadCommit = "" },
		"missing actor":          func(e *domain.ExecutionEvidence) { e.Actor = "" },
		"missing trace_id":       func(e *domain.ExecutionEvidence) { e.TraceID = "" },
		"zero generated_at":      func(e *domain.ExecutionEvidence) { e.GeneratedAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvidence()
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Errorf("expected validation error, got nil")
			}
		})
	}
}

func TestExecutionEvidence_Validate_RejectsNewlinesInSingleLineFields(t *testing.T) {
	// Trailer-injection / log-bleed defense: every single-line field
	// must reject embedded newlines. Lesson from EPIC-005 TASK-006.
	cases := map[string]func(*domain.ExecutionEvidence){
		"actor newline":        func(e *domain.ExecutionEvidence) { e.Actor = "user/alice\nFAKE-TRAILER: x" },
		"trace_id newline":     func(e *domain.ExecutionEvidence) { e.TraceID = "trace\rinjection" },
		"branch_name newline":  func(e *domain.ExecutionEvidence) { e.BranchName = "spine/run/x\nfoo" },
		"name newline":         func(e *domain.ExecutionEvidence) { e.CheckResults[0].Name = "Unit\nTests" },
		"summary newline":      func(e *domain.ExecutionEvidence) { e.CheckResults[0].Summary = "ok\nbad" },
		"evidence_uri newline": func(e *domain.ExecutionEvidence) { e.CheckResults[0].EvidenceURI = "https://x\ny" },
		"evidence_uri space": func(e *domain.ExecutionEvidence) {
			e.CheckResults[0].EvidenceURI = "https://x with space"
		},
		"evidence_uri vertical tab": func(e *domain.ExecutionEvidence) {
			e.CheckResults[0].EvidenceURI = "https://x\vy"
		},
		"evidence_uri form feed": func(e *domain.ExecutionEvidence) {
			e.CheckResults[0].EvidenceURI = "https://x\fy"
		},
		"evidence_uri unicode line separator": func(e *domain.ExecutionEvidence) {
			e.CheckResults[0].EvidenceURI = "https://x y"
		},
		"changed_paths newline": func(e *domain.ExecutionEvidence) {
			e.ChangedPaths.Paths = []string{"cmd/main.go\nattack"}
		},
		"adr_path newline": func(e *domain.ExecutionEvidence) {
			e.ValidationPolicies[0].ADRPath = "/architecture/adr/ADR-014.md\nbad"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvidence()
			mutate(&e)
			err := e.Validate()
			if err == nil {
				t.Fatalf("expected validation error for embedded newline / whitespace, got nil")
			}
			if !strings.Contains(err.Error(), "newline") && !strings.Contains(err.Error(), "whitespace") {
				t.Errorf("expected newline/whitespace error, got: %v", err)
			}
		})
	}
}

func TestExecutionEvidence_Validate_RejectsOrphanCheckResult(t *testing.T) {
	e := validEvidence()
	e.CheckResults = append(e.CheckResults, domain.CheckResult{
		CheckID:    "rogue",
		Status:     domain.CheckStatusPassed,
		Producer:   domain.CheckProducerAutomated,
		ProducedBy: "ci/test",
	})
	// Status no longer matches because we added an extra row, but the
	// orphan guard fires first.
	if err := e.Validate(); err == nil ||
		!strings.Contains(err.Error(), "not in required_checks") {
		t.Fatalf("expected orphan check_result error, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RejectsDuplicateRequired(t *testing.T) {
	e := validEvidence()
	e.RequiredChecks = []string{"unit-tests", "unit-tests"}
	if err := e.Validate(); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate required_checks error, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RejectsDuplicateResultID(t *testing.T) {
	e := validEvidence()
	e.RequiredChecks = []string{"unit-tests"}
	e.CheckResults = append(e.CheckResults, e.CheckResults[0])
	if err := e.Validate(); err == nil ||
		!strings.Contains(err.Error(), "duplicate check_id") {
		t.Fatalf("expected duplicate check_id error, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RequiresProducerOnceCheckLeavesPending(t *testing.T) {
	e := validEvidence()
	e.CheckResults[0].Producer = ""
	e.CheckResults[0].ProducedBy = ""
	if err := e.Validate(); err == nil ||
		!strings.Contains(err.Error(), "produc") {
		t.Fatalf("expected producer/produced_by error, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RejectsInvalidProducerEvenOnPending(t *testing.T) {
	// Codex pass 3: a pending row with a typo'd producer (e.g.
	// "robot") would previously slip past Validate because the enum
	// check only fired once status left pending. Now we validate the
	// enum whenever Producer carries a value, regardless of status.
	e := validEvidence()
	e.RequiredChecks = []string{"unit-tests"}
	e.CheckResults = []domain.CheckResult{
		{
			CheckID:  "unit-tests",
			Status:   domain.CheckStatusPending,
			Producer: domain.CheckProducerKind("robot"),
		},
	}
	e.Status = e.DeriveStatus()
	err := e.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid producer") {
		t.Fatalf("expected invalid producer error, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_AllowsPendingResultWithoutProducer(t *testing.T) {
	// A pending row may be a placeholder a producer has not yet picked
	// up — Producer/ProducedBy can stay empty until the row leaves
	// pending. Anything else would force tooling to invent a producer
	// before any work has happened.
	e := validEvidence()
	e.CheckResults = []domain.CheckResult{
		{CheckID: "unit-tests", Status: domain.CheckStatusPending},
	}
	e.Status = domain.EvidenceStatusPending
	if err := e.Validate(); err != nil {
		t.Fatalf("expected pending-result with empty producer to be valid, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RejectsUnknownSchemaVersion(t *testing.T) {
	// Per the schema doc, readers MUST reject unknown versions rather
	// than guess. A non-empty but unsupported version is a malformed
	// record from this build's perspective, not a forward-compatible
	// upgrade.
	e := validEvidence()
	e.SchemaVersion = "2"
	err := e.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported schema_version") {
		t.Fatalf("expected unsupported schema_version error, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RejectsNewlinesInCheckIDs(t *testing.T) {
	// CheckIDs are committed into the evidence file as governed
	// identifiers; they must satisfy the same trailer-injection
	// defense as the other single-line fields.
	t.Run("required_checks newline", func(t *testing.T) {
		e := validEvidence()
		e.RequiredChecks = []string{"unit-tests\nFAKE-TRAILER: x"}
		// Strip results so we don't fail the orphan check first.
		e.CheckResults = nil
		e.Status = e.DeriveStatus()
		err := e.Validate()
		if err == nil || !strings.Contains(err.Error(), "newline") {
			t.Fatalf("expected newline error on required_checks entry, got: %v", err)
		}
	})
	t.Run("check_results check_id newline", func(t *testing.T) {
		e := validEvidence()
		bad := "unit-tests\rFAKE"
		e.RequiredChecks = []string{bad}
		// The required_checks newline guard fires before the
		// check_results guard, so to test the latter we need a
		// required_checks entry that is itself clean. Use a different
		// shape: clean required + bad result id.
		e.RequiredChecks = []string{"unit-tests"}
		e.CheckResults[0].CheckID = bad
		err := e.Validate()
		if err == nil || !strings.Contains(err.Error(), "newline") {
			t.Fatalf("expected newline error on check_results check_id, got: %v", err)
		}
	})
}

func TestExecutionEvidence_Validate_StatusMustMatchDerived(t *testing.T) {
	e := validEvidence()
	e.Status = domain.EvidenceStatusPending // disagrees with passed-derivation
	if err := e.Validate(); err == nil ||
		!strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("expected stored-vs-derived disagreement error, got: %v", err)
	}
}

func TestExecutionEvidence_Validate_RejectsNegativeChangedCounts(t *testing.T) {
	cases := map[string]func(*domain.ExecutionEvidence){
		"files_changed": func(e *domain.ExecutionEvidence) { e.ChangedPaths.FilesChanged = -1 },
		"insertions":    func(e *domain.ExecutionEvidence) { e.ChangedPaths.Insertions = -1 },
		"deletions":     func(e *domain.ExecutionEvidence) { e.ChangedPaths.Deletions = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvidence()
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatalf("expected error for negative %s", name)
			}
		})
	}
}

func TestExecutionEvidence_Canonicalize_SortsSlices(t *testing.T) {
	e := validEvidence()
	// Add an extra required check + result so we have something to sort.
	e.RequiredChecks = []string{"unit-tests", "lint", "vet"}
	e.CheckResults = []domain.CheckResult{
		checkPassed("vet"),
		checkPassed("unit-tests"),
		checkPassed("lint"),
	}
	e.ValidationPolicies = []domain.ValidationPolicyRef{
		{ADRPath: "/architecture/adr/ADR-014-evidence.md", PolicyID: "z"},
		{ADRPath: "/architecture/adr/ADR-014-evidence.md", PolicyID: "a"},
		{ADRPath: "/architecture/adr/ADR-002-events.md", PolicyID: "m"},
	}
	e.ChangedPaths.Paths = []string{"cmd/main.go", "README.md", "internal/api/handler.go"}
	e.Status = e.DeriveStatus()

	e.Canonicalize()

	wantRequired := []string{"lint", "unit-tests", "vet"}
	if !equalStrings(e.RequiredChecks, wantRequired) {
		t.Errorf("RequiredChecks not sorted: got %v, want %v", e.RequiredChecks, wantRequired)
	}
	wantResultIDs := []string{"lint", "unit-tests", "vet"}
	for i, want := range wantResultIDs {
		if e.CheckResults[i].CheckID != want {
			t.Errorf("CheckResults[%d]: got %q, want %q", i, e.CheckResults[i].CheckID, want)
		}
	}
	wantPaths := []string{"README.md", "cmd/main.go", "internal/api/handler.go"}
	if !equalStrings(e.ChangedPaths.Paths, wantPaths) {
		t.Errorf("ChangedPaths.Paths not sorted: got %v, want %v", e.ChangedPaths.Paths, wantPaths)
	}
	// Policies sort by (ADRPath, PolicyPath, PolicyID).
	if e.ValidationPolicies[0].ADRPath != "/architecture/adr/ADR-002-events.md" ||
		e.ValidationPolicies[1].PolicyID != "a" ||
		e.ValidationPolicies[2].PolicyID != "z" {
		t.Errorf("ValidationPolicies not sorted as expected: %#v", e.ValidationPolicies)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestExecutionEvidence_DeterministicJSON anchors the AC "Evidence is
// serializable as deterministic YAML or JSON." Two semantically-
// identical evidence values must produce byte-identical JSON after
// Canonicalize, regardless of the order their slices were built in.
func TestExecutionEvidence_DeterministicJSON(t *testing.T) {
	a := validEvidence()
	a.RequiredChecks = []string{"vet", "unit-tests", "lint"}
	a.CheckResults = []domain.CheckResult{
		checkPassed("vet"), checkPassed("unit-tests"), checkPassed("lint"),
	}
	a.ChangedPaths.Paths = []string{"z.go", "a.go", "m.go"}
	a.Status = a.DeriveStatus()

	b := validEvidence()
	b.RequiredChecks = []string{"lint", "unit-tests", "vet"}
	b.CheckResults = []domain.CheckResult{
		checkPassed("lint"), checkPassed("unit-tests"), checkPassed("vet"),
	}
	b.ChangedPaths.Paths = []string{"a.go", "m.go", "z.go"}
	b.Status = b.DeriveStatus()

	a.Canonicalize()
	b.Canonicalize()

	aBytes, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	bBytes, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if string(aBytes) != string(bBytes) {
		t.Errorf("canonicalized evidence not byte-identical:\n a: %s\n b: %s", aBytes, bBytes)
	}
}

func TestExecutionEvidence_DeterministicYAML(t *testing.T) {
	a := validEvidence()
	a.RequiredChecks = []string{"vet", "unit-tests", "lint"}
	a.CheckResults = []domain.CheckResult{
		checkPassed("vet"), checkPassed("unit-tests"), checkPassed("lint"),
	}
	a.ChangedPaths.Paths = []string{"z.go", "a.go", "m.go"}
	a.Status = a.DeriveStatus()

	b := validEvidence()
	b.RequiredChecks = []string{"lint", "unit-tests", "vet"}
	b.CheckResults = []domain.CheckResult{
		checkPassed("lint"), checkPassed("unit-tests"), checkPassed("vet"),
	}
	b.ChangedPaths.Paths = []string{"a.go", "m.go", "z.go"}
	b.Status = b.DeriveStatus()

	a.Canonicalize()
	b.Canonicalize()

	aBytes, err := yaml.Marshal(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	bBytes, err := yaml.Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if string(aBytes) != string(bBytes) {
		t.Errorf("canonicalized YAML not byte-identical:\n a: %s\n b: %s", aBytes, bBytes)
	}
}

// TestExecutionEvidence_DeterministicAcrossTimezones pins the cross-
// timezone case: two producers on runners in different zones must
// produce byte-identical evidence after Canonicalize, because the
// timestamps point to the same instant. Codex pass 5 finding.
func TestExecutionEvidence_DeterministicAcrossTimezones(t *testing.T) {
	plus1, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("Europe/Berlin tz unavailable")
	}
	utcInstant := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	zonedInstant := utcInstant.In(plus1) // same instant, different zone

	a := validEvidence()
	a.GeneratedAt = utcInstant
	a.CheckResults[0].StartedAt = ptrTime(utcInstant.Add(-2 * time.Minute))
	a.CheckResults[0].CompletedAt = ptrTime(utcInstant.Add(-30 * time.Second))

	b := validEvidence()
	b.GeneratedAt = zonedInstant
	b.CheckResults[0].StartedAt = ptrTime(zonedInstant.Add(-2 * time.Minute))
	b.CheckResults[0].CompletedAt = ptrTime(zonedInstant.Add(-30 * time.Second))

	a.Canonicalize()
	b.Canonicalize()

	aBytes, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	bBytes, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if string(aBytes) != string(bBytes) {
		t.Errorf("cross-tz canonicalized JSON differs:\n a: %s\n b: %s", aBytes, bBytes)
	}
}

// TestExecutionEvidence_RoundTripJSON pins the wire shape: marshal +
// unmarshal must preserve every field. Catches accidental tag drops.
func TestExecutionEvidence_RoundTripJSON(t *testing.T) {
	original := validEvidence()
	original.Canonicalize()
	bytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got domain.ExecutionEvidence
	if err := json.Unmarshal(bytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("round-tripped evidence failed validation: %v", err)
	}
	// Spot-check fields the schema cares most about.
	if got.RunID != original.RunID || got.RepositoryID != original.RepositoryID ||
		got.HeadCommit != original.HeadCommit || got.Status != original.Status {
		t.Errorf("round-trip lost fields: got=%+v want=%+v", got, original)
	}
}

// TestExecutionEvidence_PendingProducerOmittedFromMarshal pins the
// invariant that a pending check result does not emit `producer: ""`
// (codex pass 2 finding). The schema defines `producer` as conditional
// — it should be absent on pending rows, not present-as-empty-enum.
func TestExecutionEvidence_PendingProducerOmittedFromMarshal(t *testing.T) {
	e := validEvidence()
	e.RequiredChecks = []string{"unit-tests"}
	e.CheckResults = []domain.CheckResult{
		{CheckID: "unit-tests", Status: domain.CheckStatusPending},
	}
	e.Status = e.DeriveStatus()
	if err := e.Validate(); err != nil {
		t.Fatalf("validate pending evidence: %v", err)
	}
	jsonBytes, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	if strings.Contains(string(jsonBytes), `"producer":""`) {
		t.Errorf("JSON contains empty producer enum: %s", jsonBytes)
	}
	yamlBytes, err := yaml.Marshal(e)
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}
	if strings.Contains(string(yamlBytes), "producer: \"\"") ||
		strings.Contains(string(yamlBytes), "producer: ''") {
		t.Errorf("YAML contains empty producer enum: %s", yamlBytes)
	}
}

func TestExecutionEvidence_HumanProducerShape(t *testing.T) {
	// Both producer kinds must be first-class. A human-signoff check
	// must validate without contortions.
	e := validEvidence()
	e.RequiredChecks = []string{"security-review"}
	e.CheckResults = []domain.CheckResult{
		{
			CheckID:    "security-review",
			Name:       "Security signoff",
			Status:     domain.CheckStatusPassed,
			Producer:   domain.CheckProducerHuman,
			ProducedBy: "user/security-lead",
			Summary:    "Reviewed payment flow; no PII regressions.",
		},
	}
	e.Status = e.DeriveStatus()
	if err := e.Validate(); err != nil {
		t.Fatalf("human-producer evidence rejected: %v", err)
	}
}

func TestExecutionEvidence_IsPrimaryRepository(t *testing.T) {
	e := validEvidence()
	e.RepositoryID = domain.PrimaryRepositoryID
	if !e.IsPrimaryRepository() {
		t.Errorf("primary repo evidence not flagged as primary")
	}
	e.RepositoryID = "payments-service"
	if e.IsPrimaryRepository() {
		t.Errorf("non-primary repo evidence wrongly flagged as primary")
	}
}
