package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"gopkg.in/yaml.v3"
)

func policyFixedTime() time.Time {
	return time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
}

// validPolicyDocument returns a minimal-but-valid validation policy
// document with one policy carrying one deterministic blocking check.
// Tests mutate copies for negative cases.
func validPolicyDocument() domain.ValidationPolicyDocument {
	return domain.ValidationPolicyDocument{
		SchemaVersion: domain.ValidationPolicySchemaVersion,
		GeneratedAt:   policyFixedTime(),
		Policies: []domain.ValidationPolicy{
			{
				PolicyID:    "code-quality",
				Version:     "1",
				Title:       "Code quality baseline",
				Description: "Run unit tests and linters before publish.",
				Status:      domain.ValidationPolicyStatusActive,
				ADRPaths: []string{
					"/architecture/adr/ADR-014-evidence.md",
				},
				Selector: domain.PolicySelector{
					RepositoryRoles: []string{"code"},
					PathPatterns:    []string{"cmd/*", "internal/*"},
				},
				Checks: []domain.PolicyCheck{
					{
						CheckID:        "unit-tests",
						Name:           "Unit tests",
						Kind:           domain.PolicyCheckKindCommand,
						Command:        "go test ./...",
						Interpretation: domain.PolicyCheckInterpretationDeterministic,
						Severity:       domain.PolicySeverityBlocking,
						TimeoutSeconds: 600,
					},
				},
			},
		},
	}
}

func TestValidationPolicyStatus_Enum(t *testing.T) {
	wantSet := map[domain.ValidationPolicyStatus]struct{}{
		domain.ValidationPolicyStatusDraft:      {},
		domain.ValidationPolicyStatusActive:     {},
		domain.ValidationPolicyStatusDeprecated: {},
		domain.ValidationPolicyStatusSuperseded: {},
	}
	got := domain.ValidValidationPolicyStatuses()
	if len(got) != len(wantSet) {
		t.Fatalf("ValidValidationPolicyStatuses: got %d, want %d", len(got), len(wantSet))
	}
	for _, s := range got {
		if _, ok := wantSet[s]; !ok {
			t.Errorf("unexpected status %q", s)
		}
	}
}

func TestPolicyCheckKind_Enum(t *testing.T) {
	want := []domain.PolicyCheckKind{
		domain.PolicyCheckKindCommand,
		domain.PolicyCheckKindExternal,
	}
	got := domain.ValidPolicyCheckKinds()
	if len(got) != len(want) {
		t.Fatalf("kinds: got %d, want %d", len(got), len(want))
	}
}

func TestPolicyCheckInterpretation_Enum(t *testing.T) {
	want := []domain.PolicyCheckInterpretation{
		domain.PolicyCheckInterpretationDeterministic,
		domain.PolicyCheckInterpretationAdvisory,
	}
	got := domain.ValidPolicyCheckInterpretations()
	if len(got) != len(want) {
		t.Fatalf("interpretations: got %d, want %d", len(got), len(want))
	}
}

func TestPolicySeverity_Enum(t *testing.T) {
	want := []domain.PolicySeverity{
		domain.PolicySeverityBlocking,
		domain.PolicySeverityWarning,
	}
	got := domain.ValidPolicySeverities()
	if len(got) != len(want) {
		t.Fatalf("severities: got %d, want %d", len(got), len(want))
	}
}

func TestValidationPolicy_Validate_Happy(t *testing.T) {
	doc := validPolicyDocument()
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidationPolicy_Validate_RequiredFields(t *testing.T) {
	cases := map[string]func(d *domain.ValidationPolicyDocument){
		"schema_version empty":        func(d *domain.ValidationPolicyDocument) { d.SchemaVersion = "" },
		"generated_at zero":           func(d *domain.ValidationPolicyDocument) { d.GeneratedAt = time.Time{} },
		"no policies":                 func(d *domain.ValidationPolicyDocument) { d.Policies = nil },
		"policy_id empty":             func(d *domain.ValidationPolicyDocument) { d.Policies[0].PolicyID = "" },
		"version empty":               func(d *domain.ValidationPolicyDocument) { d.Policies[0].Version = "" },
		"title empty":                 func(d *domain.ValidationPolicyDocument) { d.Policies[0].Title = "" },
		"adr_paths empty":             func(d *domain.ValidationPolicyDocument) { d.Policies[0].ADRPaths = nil },
		"checks empty":                func(d *domain.ValidationPolicyDocument) { d.Policies[0].Checks = nil },
		"check_id empty":              func(d *domain.ValidationPolicyDocument) { d.Policies[0].Checks[0].CheckID = "" },
		"command empty for command":   func(d *domain.ValidationPolicyDocument) { d.Policies[0].Checks[0].Command = "" },
		"selector neither id nor role": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Selector.RepositoryIDs = nil
			d.Policies[0].Selector.RepositoryRoles = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := validPolicyDocument()
			mutate(&doc)
			if err := doc.Validate(); err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
}

func TestValidationPolicy_Validate_RejectsUnknownSchemaVersion(t *testing.T) {
	doc := validPolicyDocument()
	doc.SchemaVersion = "999"
	err := doc.Validate()
	if err == nil {
		t.Fatal("expected error for unknown schema_version")
	}
	if !strings.Contains(err.Error(), "unsupported schema_version") {
		t.Fatalf("expected unsupported schema_version error, got: %v", err)
	}
}

func TestValidationPolicy_Validate_RejectsDuplicatePolicyID(t *testing.T) {
	doc := validPolicyDocument()
	dup := doc.Policies[0]
	doc.Policies = append(doc.Policies, dup)
	if err := doc.Validate(); err == nil {
		t.Fatal("expected duplicate policy_id error")
	}
}

func TestValidationPolicy_Validate_RejectsDuplicateCheckID(t *testing.T) {
	doc := validPolicyDocument()
	dup := doc.Policies[0].Checks[0]
	doc.Policies[0].Checks = append(doc.Policies[0].Checks, dup)
	if err := doc.Validate(); err == nil {
		t.Fatal("expected duplicate check_id error")
	}
}

func TestValidationPolicy_Validate_RejectsDuplicateADRPath(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].ADRPaths = append(doc.Policies[0].ADRPaths, doc.Policies[0].ADRPaths[0])
	if err := doc.Validate(); err == nil {
		t.Fatal("expected duplicate adr_path error")
	}
}

func TestValidationPolicy_Validate_RejectsDuplicateRepositoryID(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].Selector.RepositoryIDs = []string{"payments", "payments"}
	doc.Policies[0].Selector.RepositoryRoles = nil
	if err := doc.Validate(); err == nil {
		t.Fatal("expected duplicate repository_id error")
	}
}

func TestValidationPolicy_Validate_RejectsNewlinesInSingleLineFields(t *testing.T) {
	cases := map[string]func(d *domain.ValidationPolicyDocument){
		"policy_id newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].PolicyID = "code\nquality"
		},
		"version newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Version = "1\n"
		},
		"title newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Title = "Title\rwith CR"
		},
		"adr_path newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].ADRPaths = []string{"/architecture/adr/ADR-014.md\n"}
		},
		"repository_id newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Selector.RepositoryIDs = []string{"payments\n"}
			d.Policies[0].Selector.RepositoryRoles = nil
		},
		"repository_role newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Selector.RepositoryRoles = []string{"code\n"}
		},
		"check_id newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Checks[0].CheckID = "unit\ntests"
		},
		"check name newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Checks[0].Name = "Unit\ntests"
		},
		"command newline": func(d *domain.ValidationPolicyDocument) {
			d.Policies[0].Checks[0].Command = "go test\n./..."
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := validPolicyDocument()
			mutate(&doc)
			err := doc.Validate()
			if err == nil {
				t.Fatalf("expected newline rejection for %q", name)
			}
			if !strings.Contains(err.Error(), "newline") {
				t.Fatalf("expected newline-related error for %q, got: %v", name, err)
			}
		})
	}
}

func TestValidationPolicy_Validate_RejectsWhitespaceInPathPattern(t *testing.T) {
	cases := []string{
		"cmd/ main.go",
		"cmd/\tmain.go",
		"cmd/\nmain.go",
		"cmd/main.go ", // NBSP
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			doc := validPolicyDocument()
			doc.Policies[0].Selector.PathPatterns = []string{p}
			if err := doc.Validate(); err == nil {
				t.Fatalf("expected whitespace rejection for %q", p)
			}
		})
	}
}

func TestValidationPolicy_Validate_RejectsInvalidPathPattern(t *testing.T) {
	cases := []string{
		"cmd/[invalid",  // unclosed bracket past literal prefix
		"[unclosed",     // unclosed bracket
		"cmd/*[invalid", // unclosed bracket past wildcard
	}
	for _, pat := range cases {
		t.Run(pat, func(t *testing.T) {
			doc := validPolicyDocument()
			doc.Policies[0].Selector.PathPatterns = []string{pat}
			err := doc.Validate()
			if err == nil {
				t.Fatalf("expected invalid glob error for %q", pat)
			}
			if !strings.Contains(err.Error(), "is not a valid glob") {
				t.Fatalf("expected invalid-glob message, got: %v", err)
			}
		})
	}
}

func TestValidateAcrossDocuments_RejectsCheckIDCollision(t *testing.T) {
	doc1 := validPolicyDocument()
	doc2 := validPolicyDocument()
	doc2.Policies[0].PolicyID = "other"
	// Same check_id "unit-tests" lives in both documents; cross-document
	// validation must catch it.
	err := domain.ValidateAcrossDocuments([]domain.ValidationPolicyDocument{doc1, doc2})
	if err == nil {
		t.Fatal("expected cross-document collision error")
	}
	if !strings.Contains(err.Error(), "unique across the entire policy set") {
		t.Fatalf("expected cross-document-collision message, got: %v", err)
	}
}

func TestValidateAcrossDocuments_AcceptsDistinctSets(t *testing.T) {
	doc1 := validPolicyDocument()
	doc2 := validPolicyDocument()
	doc2.Policies[0].PolicyID = "other"
	doc2.Policies[0].Checks[0].CheckID = "vet"
	if err := domain.ValidateAcrossDocuments([]domain.ValidationPolicyDocument{doc1, doc2}); err != nil {
		t.Fatalf("expected pass: %v", err)
	}
}

func TestValidateAcrossDocuments_PropagatesDocumentValidationError(t *testing.T) {
	doc1 := validPolicyDocument()
	doc2 := validPolicyDocument()
	doc2.SchemaVersion = "999"
	if err := domain.ValidateAcrossDocuments([]domain.ValidationPolicyDocument{doc1, doc2}); err == nil {
		t.Fatal("expected schema_version error to propagate")
	}
}

func TestValidationPolicy_Validate_RejectsAdvisoryBlockingCombo(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].Checks[0].Interpretation = domain.PolicyCheckInterpretationAdvisory
	doc.Policies[0].Checks[0].Severity = domain.PolicySeverityBlocking
	err := doc.Validate()
	if err == nil {
		t.Fatal("expected advisory+blocking rejection")
	}
	if !strings.Contains(err.Error(), "advisory") {
		t.Fatalf("expected advisory-related error, got: %v", err)
	}
}

func TestValidationPolicy_Validate_AllowsAdvisoryWarning(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].Checks[0].Interpretation = domain.PolicyCheckInterpretationAdvisory
	doc.Policies[0].Checks[0].Severity = domain.PolicySeverityWarning
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidationPolicy_Validate_RejectsExternalWithCommand(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].Checks[0].Kind = domain.PolicyCheckKindExternal
	// command field still set — should fail
	if err := doc.Validate(); err == nil {
		t.Fatal("expected external+command rejection")
	}
}

func TestValidationPolicy_Validate_AllowsExternalWithoutCommand(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].Checks[0].Kind = domain.PolicyCheckKindExternal
	doc.Policies[0].Checks[0].Command = ""
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidationPolicy_Validate_RejectsNegativeTimeout(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].Checks[0].TimeoutSeconds = -1
	if err := doc.Validate(); err == nil {
		t.Fatal("expected negative timeout rejection")
	}
}

func TestValidationPolicy_Validate_RejectsInvalidEnum(t *testing.T) {
	cases := map[string]func(d *domain.ValidationPolicyDocument){
		"status":         func(d *domain.ValidationPolicyDocument) { d.Policies[0].Status = "active!!" },
		"kind":           func(d *domain.ValidationPolicyDocument) { d.Policies[0].Checks[0].Kind = "shell" },
		"interpretation": func(d *domain.ValidationPolicyDocument) { d.Policies[0].Checks[0].Interpretation = "ai" },
		"severity":       func(d *domain.ValidationPolicyDocument) { d.Policies[0].Checks[0].Severity = "block" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := validPolicyDocument()
			mutate(&doc)
			if err := doc.Validate(); err == nil {
				t.Fatalf("expected invalid enum error for %q", name)
			}
		})
	}
}

func TestValidationPolicy_Canonicalize_SortsSlices(t *testing.T) {
	doc := validPolicyDocument()
	// Add a second policy out of order.
	doc.Policies = append(doc.Policies, domain.ValidationPolicy{
		PolicyID: "audit-log",
		Version:  "1",
		Title:    "Audit log",
		Status:   domain.ValidationPolicyStatusActive,
		ADRPaths: []string{
			"/architecture/adr/ADR-099.md",
			"/architecture/adr/ADR-014.md",
		},
		Selector: domain.PolicySelector{
			RepositoryIDs: []string{"zeta-svc", "alpha-svc"},
			PathPatterns:  []string{"z/*", "a/*"},
		},
		Checks: []domain.PolicyCheck{
			{
				CheckID:        "z-check",
				Kind:           domain.PolicyCheckKindCommand,
				Command:        "z",
				Interpretation: domain.PolicyCheckInterpretationDeterministic,
				Severity:       domain.PolicySeverityBlocking,
			},
			{
				CheckID:        "a-check",
				Kind:           domain.PolicyCheckKindCommand,
				Command:        "a",
				Interpretation: domain.PolicyCheckInterpretationDeterministic,
				Severity:       domain.PolicySeverityBlocking,
			},
		},
	})

	doc.Canonicalize()

	if doc.Policies[0].PolicyID != "audit-log" || doc.Policies[1].PolicyID != "code-quality" {
		t.Errorf("policies not sorted by policy_id: %v", []string{doc.Policies[0].PolicyID, doc.Policies[1].PolicyID})
	}
	audit := doc.Policies[0]
	if audit.ADRPaths[0] >= audit.ADRPaths[1] {
		t.Errorf("ADRPaths not sorted: %v", audit.ADRPaths)
	}
	if audit.Selector.RepositoryIDs[0] != "alpha-svc" {
		t.Errorf("RepositoryIDs not sorted: %v", audit.Selector.RepositoryIDs)
	}
	if audit.Selector.PathPatterns[0] != "a/*" {
		t.Errorf("PathPatterns not sorted: %v", audit.Selector.PathPatterns)
	}
	if audit.Checks[0].CheckID != "a-check" {
		t.Errorf("Checks not sorted: %v", audit.Checks)
	}
}

func TestValidationPolicy_DeterministicJSON(t *testing.T) {
	d1 := validPolicyDocument()
	d2 := validPolicyDocument()
	// Permute slices in d2 — Canonicalize should make them byte-identical.
	d2.Policies[0].ADRPaths = []string{
		"/architecture/adr/ADR-099.md",
		"/architecture/adr/ADR-014-evidence.md",
	}
	d2.Policies[0].Selector.PathPatterns = []string{"internal/*", "cmd/*"}
	d1.Policies[0].ADRPaths = []string{
		"/architecture/adr/ADR-014-evidence.md",
		"/architecture/adr/ADR-099.md",
	}

	d1.Canonicalize()
	d2.Canonicalize()

	b1, err := json.Marshal(d1)
	if err != nil {
		t.Fatalf("marshal d1: %v", err)
	}
	b2, err := json.Marshal(d2)
	if err != nil {
		t.Fatalf("marshal d2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("JSON not deterministic:\nd1=%s\nd2=%s", b1, b2)
	}
}

func TestValidationPolicy_DeterministicYAML(t *testing.T) {
	d1 := validPolicyDocument()
	d2 := validPolicyDocument()
	d2.Policies[0].Selector.PathPatterns = []string{"internal/*", "cmd/*"}
	d1.Canonicalize()
	d2.Canonicalize()
	b1, err := yaml.Marshal(d1)
	if err != nil {
		t.Fatalf("marshal d1: %v", err)
	}
	b2, err := yaml.Marshal(d2)
	if err != nil {
		t.Fatalf("marshal d2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("YAML not deterministic:\nd1=%s\nd2=%s", b1, b2)
	}
}

func TestValidationPolicy_DeterministicAcrossTimezones(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("Europe/Berlin tz unavailable: %v", err)
	}
	d1 := validPolicyDocument()
	d2 := validPolicyDocument()
	d2.GeneratedAt = d2.GeneratedAt.In(berlin)
	d1.Canonicalize()
	d2.Canonicalize()
	b1, _ := json.Marshal(d1)
	b2, _ := json.Marshal(d2)
	if string(b1) != string(b2) {
		t.Errorf("cross-tz drift:\nd1=%s\nd2=%s", b1, b2)
	}
}

func TestValidationPolicy_RoundTripJSON(t *testing.T) {
	src := validPolicyDocument()
	src.Canonicalize()
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst domain.ValidationPolicyDocument
	if err := json.Unmarshal(b, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dst.Canonicalize()
	b2, _ := json.Marshal(dst)
	if string(b) != string(b2) {
		t.Errorf("round-trip mismatch:\norig=%s\nback=%s", b, b2)
	}
}

func TestValidationPolicy_RoundTripYAML(t *testing.T) {
	src := validPolicyDocument()
	src.Canonicalize()
	b, err := yaml.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst domain.ValidationPolicyDocument
	if err := yaml.Unmarshal(b, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dst.Canonicalize()
	b2, _ := yaml.Marshal(dst)
	if string(b) != string(b2) {
		t.Errorf("round-trip mismatch:\norig=%s\nback=%s", b, b2)
	}
}

func TestPolicyDocument_PolicyByID(t *testing.T) {
	doc := validPolicyDocument()
	if _, ok := doc.PolicyByID("code-quality"); !ok {
		t.Fatal("expected hit")
	}
	if _, ok := doc.PolicyByID("missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestValidationPolicy_CheckByID(t *testing.T) {
	doc := validPolicyDocument()
	if _, ok := doc.Policies[0].CheckByID("unit-tests"); !ok {
		t.Fatal("expected hit")
	}
	if _, ok := doc.Policies[0].CheckByID("missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestPolicyCheck_IsBlocking(t *testing.T) {
	cases := map[domain.PolicySeverity]bool{
		domain.PolicySeverityBlocking: true,
		domain.PolicySeverityWarning:  false,
	}
	for sev, want := range cases {
		c := domain.PolicyCheck{Severity: sev}
		if got := c.IsBlocking(); got != want {
			t.Errorf("severity %q IsBlocking: got %v, want %v", sev, got, want)
		}
	}
}

func TestPolicySelector_MatchesRepository(t *testing.T) {
	cases := []struct {
		name       string
		sel        domain.PolicySelector
		repoID     string
		repoRole   string
		wantMatch  bool
	}{
		{
			name:      "id match",
			sel:       domain.PolicySelector{RepositoryIDs: []string{"payments"}},
			repoID:    "payments",
			repoRole:  "code",
			wantMatch: true,
		},
		{
			name:      "role match",
			sel:       domain.PolicySelector{RepositoryRoles: []string{"code"}},
			repoID:    "payments",
			repoRole:  "code",
			wantMatch: true,
		},
		{
			name:      "id miss",
			sel:       domain.PolicySelector{RepositoryIDs: []string{"orders"}},
			repoID:    "payments",
			repoRole:  "code",
			wantMatch: false,
		},
		{
			name:      "role miss",
			sel:       domain.PolicySelector{RepositoryRoles: []string{"spine"}},
			repoID:    "payments",
			repoRole:  "code",
			wantMatch: false,
		},
		{
			name: "id wins over missing role",
			sel: domain.PolicySelector{
				RepositoryIDs:   []string{"payments"},
				RepositoryRoles: []string{"spine"},
			},
			repoID:    "payments",
			repoRole:  "code",
			wantMatch: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sel.MatchesRepository(tc.repoID, tc.repoRole); got != tc.wantMatch {
				t.Errorf("got %v, want %v", got, tc.wantMatch)
			}
		})
	}
}

func TestPolicySelector_MatchesAnyPath(t *testing.T) {
	cases := []struct {
		name      string
		sel       domain.PolicySelector
		summary   domain.ChangedPathsSummary
		wantMatch bool
	}{
		{
			name:      "empty patterns always match",
			sel:       domain.PolicySelector{},
			summary:   domain.ChangedPathsSummary{Paths: []string{"any/path"}},
			wantMatch: true,
		},
		{
			name:      "empty patterns even with no paths",
			sel:       domain.PolicySelector{},
			summary:   domain.ChangedPathsSummary{},
			wantMatch: true,
		},
		{
			name:      "single pattern hits",
			sel:       domain.PolicySelector{PathPatterns: []string{"cmd/*"}},
			summary:   domain.ChangedPathsSummary{Paths: []string{"cmd/main.go"}},
			wantMatch: true,
		},
		{
			name:      "single pattern misses",
			sel:       domain.PolicySelector{PathPatterns: []string{"cmd/*"}},
			summary:   domain.ChangedPathsSummary{Paths: []string{"internal/api/handler.go"}},
			wantMatch: false,
		},
		{
			// path.Match's `*` does not cross `/`, so `internal/*` matches
			// flat children only. Documenting the boundary so users do not
			// expect recursive globs without saying so.
			name:      "single star does not cross slashes",
			sel:       domain.PolicySelector{PathPatterns: []string{"cmd/*", "internal/*"}},
			summary:   domain.ChangedPathsSummary{Paths: []string{"internal/api/handler.go"}},
			wantMatch: false,
		},
		{
			name:      "wildcard hits flat path",
			sel:       domain.PolicySelector{PathPatterns: []string{"internal/*"}},
			summary:   domain.ChangedPathsSummary{Paths: []string{"internal/handler.go"}},
			wantMatch: true,
		},
		{
			// Codex pass-1 P2: truncated path summaries are non-exhaustive;
			// matching against the visible slice alone could silently skip
			// a path-gated blocking policy on a large diff. MatchesAnyPath
			// returns true conservatively when the summary is truncated.
			name: "truncated summary forces conservative match",
			sel:  domain.PolicySelector{PathPatterns: []string{"cmd/*"}},
			summary: domain.ChangedPathsSummary{
				Paths:     []string{"internal/api/handler.go"},
				Truncated: true,
			},
			wantMatch: true,
		},
		{
			// Truncated still wins even when the visible slice is empty.
			name: "truncated summary with no visible paths",
			sel:  domain.PolicySelector{PathPatterns: []string{"cmd/*"}},
			summary: domain.ChangedPathsSummary{
				Truncated: true,
			},
			wantMatch: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sel.MatchesAnyPath(tc.summary); got != tc.wantMatch {
				t.Errorf("got %v, want %v", got, tc.wantMatch)
			}
		})
	}
}

func TestValidationPolicy_Validate_RejectsRelativeADRPath(t *testing.T) {
	doc := validPolicyDocument()
	doc.Policies[0].ADRPaths = []string{"architecture/adr/ADR-014.md"}
	err := doc.Validate()
	if err == nil {
		t.Fatal("expected canonical-path rejection")
	}
	if !strings.Contains(err.Error(), "canonical path") {
		t.Fatalf("expected canonical-path message, got: %v", err)
	}
}

func TestValidationPolicy_Validate_RejectsWhitespaceOnlyCommand(t *testing.T) {
	cases := []string{
		"   ",
		"\t",
		" \t ",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			doc := validPolicyDocument()
			doc.Policies[0].Checks[0].Command = cmd
			err := doc.Validate()
			if err == nil {
				t.Fatalf("expected whitespace-only command rejection for %q", cmd)
			}
			if !strings.Contains(err.Error(), "whitespace-only") {
				t.Fatalf("expected whitespace-only message, got: %v", err)
			}
		})
	}
}

func TestValidationPolicy_Validate_RejectsCheckIDCollisionAcrossPolicies(t *testing.T) {
	doc := validPolicyDocument()
	other := domain.ValidationPolicy{
		PolicyID: "other",
		Version:  "1",
		Title:    "Other",
		Status:   domain.ValidationPolicyStatusActive,
		ADRPaths: []string{"/architecture/adr/ADR-099.md"},
		Selector: domain.PolicySelector{RepositoryRoles: []string{"code"}},
		Checks: []domain.PolicyCheck{{
			CheckID:        "unit-tests", // collides with code-quality.unit-tests
			Kind:           domain.PolicyCheckKindCommand,
			Command:        "go vet ./...",
			Interpretation: domain.PolicyCheckInterpretationDeterministic,
			Severity:       domain.PolicySeverityBlocking,
		}},
	}
	doc.Policies = append(doc.Policies, other)
	err := doc.Validate()
	if err == nil {
		t.Fatal("expected document-wide check_id collision rejection")
	}
	if !strings.Contains(err.Error(), "unique document-wide") {
		t.Fatalf("expected document-wide collision message, got: %v", err)
	}
}

// TestValidationPolicy_ExampleAPIContract exercises an API-contract
// shape per AC #5: documentation includes examples for API contract,
// migration, and lint checks.
func TestValidationPolicy_ExampleAPIContract(t *testing.T) {
	doc := domain.ValidationPolicyDocument{
		SchemaVersion: domain.ValidationPolicySchemaVersion,
		GeneratedAt:   policyFixedTime(),
		Policies: []domain.ValidationPolicy{{
			PolicyID:    "api-contract",
			Version:     "1",
			Title:       "API contract compatibility",
			Description: "OpenAPI diff must show no breaking changes for /v1/* routes.",
			Status:      domain.ValidationPolicyStatusActive,
			ADRPaths:    []string{"/architecture/adr/ADR-021-api-versioning.md"},
			Selector: domain.PolicySelector{
				RepositoryRoles: []string{"code"},
				PathPatterns:    []string{"openapi/*"},
			},
			Checks: []domain.PolicyCheck{{
				CheckID:        "openapi-diff",
				Name:           "OpenAPI diff",
				Kind:           domain.PolicyCheckKindCommand,
				Command:        "scripts/openapi-diff.sh",
				Interpretation: domain.PolicyCheckInterpretationDeterministic,
				Severity:       domain.PolicySeverityBlocking,
				TimeoutSeconds: 120,
			}},
		}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidationPolicy_ExampleMigration covers a migration check.
func TestValidationPolicy_ExampleMigration(t *testing.T) {
	doc := domain.ValidationPolicyDocument{
		SchemaVersion: domain.ValidationPolicySchemaVersion,
		GeneratedAt:   policyFixedTime(),
		Policies: []domain.ValidationPolicy{{
			PolicyID: "migration-safety",
			Version:  "1",
			Title:    "Database migration safety",
			Status:   domain.ValidationPolicyStatusActive,
			ADRPaths: []string{"/architecture/adr/ADR-008-migrations.md"},
			Selector: domain.PolicySelector{
				RepositoryRoles: []string{"code"},
				PathPatterns:    []string{"db/migrations/*"},
			},
			Checks: []domain.PolicyCheck{{
				CheckID:        "migration-review",
				Name:           "Manual migration review",
				Kind:           domain.PolicyCheckKindExternal,
				Interpretation: domain.PolicyCheckInterpretationDeterministic,
				Severity:       domain.PolicySeverityBlocking,
			}},
		}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidationPolicy_ExampleLint covers a lint check.
func TestValidationPolicy_ExampleLint(t *testing.T) {
	doc := domain.ValidationPolicyDocument{
		SchemaVersion: domain.ValidationPolicySchemaVersion,
		GeneratedAt:   policyFixedTime(),
		Policies: []domain.ValidationPolicy{{
			PolicyID: "lint",
			Version:  "1",
			Title:    "Lint",
			Status:   domain.ValidationPolicyStatusActive,
			ADRPaths: []string{"/architecture/adr/ADR-006-code-style.md"},
			Selector: domain.PolicySelector{
				RepositoryRoles: []string{"code"},
			},
			Checks: []domain.PolicyCheck{{
				CheckID:        "golangci-lint",
				Name:           "golangci-lint",
				Kind:           domain.PolicyCheckKindCommand,
				Command:        "golangci-lint run ./...",
				Interpretation: domain.PolicyCheckInterpretationDeterministic,
				Severity:       domain.PolicySeverityBlocking,
				TimeoutSeconds: 300,
			}},
		}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidationPolicy_AdvisoryAIExample documents the advisory shape:
// AI-assisted interpretation MUST be warning, never blocking. AC #4.
func TestValidationPolicy_AdvisoryAIExample(t *testing.T) {
	doc := domain.ValidationPolicyDocument{
		SchemaVersion: domain.ValidationPolicySchemaVersion,
		GeneratedAt:   policyFixedTime(),
		Policies: []domain.ValidationPolicy{{
			PolicyID: "ai-readability",
			Version:  "1",
			Title:    "AI readability review",
			Status:   domain.ValidationPolicyStatusActive,
			ADRPaths: []string{"/architecture/adr/ADR-014-evidence.md"},
			Selector: domain.PolicySelector{
				RepositoryRoles: []string{"code"},
			},
			Checks: []domain.PolicyCheck{{
				CheckID:        "llm-review",
				Name:           "LLM readability review",
				Kind:           domain.PolicyCheckKindExternal,
				Interpretation: domain.PolicyCheckInterpretationAdvisory,
				Severity:       domain.PolicySeverityWarning, // MUST be warning.
			}},
		}},
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
