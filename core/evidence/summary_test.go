package evidence_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/evidence"
	"gopkg.in/yaml.v3"
)

// fixtureEvidence builds a CANONICAL evidence record for a given
// (runID, repoID) plus per-test overrides. Tests pass an `apply`
// callback to mutate the record before it is written to the fake
// GitReader; canonicalize+validate are run on the way out so the
// fixture is always loadable. Newlines are intentionally absent —
// every single-line field rejects them.
func fixtureEvidence(t *testing.T, runID, repoID string, apply func(*domain.ExecutionEvidence)) []byte {
	t.Helper()
	ev := domain.ExecutionEvidence{
		SchemaVersion:  domain.ExecutionEvidenceSchemaVersion,
		RunID:          runID,
		TaskPath:       "/initiatives/INIT-014/epics/EPIC-006/tasks/TASK-005.md",
		RepositoryID:   repoID,
		BranchName:     "spine/run/abc",
		BaseCommit:     "0000000000000000000000000000000000000001",
		HeadCommit:     "0000000000000000000000000000000000000002",
		ChangedPaths:   domain.ChangedPathsSummary{FilesChanged: 1, Insertions: 2, Deletions: 0, Paths: []string{"a.go"}},
		RequiredChecks: []string{"unit-tests"},
		CheckResults: []domain.CheckResult{
			{
				CheckID:    "unit-tests",
				Status:     domain.CheckStatusPassed,
				Producer:   domain.CheckProducerAutomated,
				ProducedBy: "ci/github-actions",
				Summary:    "all green",
			},
		},
		Actor:       "user/alice",
		TraceID:     "trace-1",
		Status:      domain.EvidenceStatusPassed,
		GeneratedAt: time.Date(2026, 5, 6, 9, 0, 0, 0, time.UTC),
	}
	if apply != nil {
		apply(&ev)
	}
	ev.Canonicalize()
	if err := ev.Validate(); err != nil {
		t.Fatalf("fixture validate: %v", err)
	}
	out, err := yaml.Marshal(&ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func newFakeReaderWithFiles(files map[string][]byte) *fakeGitReader {
	return &fakeGitReader{files: files}
}

func keyFor(ref, runID, repoID string) string {
	return ref + "\t" + evidence.EvidencePath(runID, repoID)
}

func TestBuildSummary_AllPresentPassed_AnchorsAC1AC2(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "billing"): fixtureEvidence(t, "run-1", "billing", nil),
		keyFor("HEAD", "run-1", "spine"):   fixtureEvidence(t, "run-1", "spine", nil),
	}
	run := &domain.Run{
		RunID:                "run-1",
		TaskPath:             "/tasks/x.md",
		Status:               domain.RunStatusCompleted,
		AffectedRepositories: []string{"spine", "billing"},
	}
	got, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if got.Status != evidence.RunEvidencePassed {
		t.Errorf("status = %q, want passed", got.Status)
	}
	if len(got.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2", len(got.Repositories))
	}
	// AC #2: lexically sorted output.
	if got.Repositories[0].RepositoryID != "billing" || got.Repositories[1].RepositoryID != "spine" {
		t.Errorf("expected billing,spine order; got %s,%s", got.Repositories[0].RepositoryID, got.Repositories[1].RepositoryID)
	}
	for _, r := range got.Repositories {
		if !r.Present {
			t.Errorf("repo %s: expected Present=true", r.RepositoryID)
		}
		if r.Status != domain.EvidenceStatusPassed {
			t.Errorf("repo %s: status = %q, want passed", r.RepositoryID, r.Status)
		}
	}
}

func TestBuildSummary_OneRepoMissing_AnchorsAC4(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "spine"): fixtureEvidence(t, "run-1", "spine", nil),
	}
	run := &domain.Run{
		RunID:                "run-1",
		Status:               domain.RunStatusActive,
		AffectedRepositories: []string{"spine", "billing"},
	}
	got, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if got.Status != evidence.RunEvidenceMissing {
		t.Errorf("status = %q, want missing", got.Status)
	}
	if len(got.MissingRepositories) != 1 || got.MissingRepositories[0] != "billing" {
		t.Errorf("MissingRepositories = %v, want [billing]", got.MissingRepositories)
	}
	// The present repo's full data must still surface — partial state
	// is the AC #4 mandate.
	for _, r := range got.Repositories {
		if r.RepositoryID == "spine" && !r.Present {
			t.Error("spine: expected Present=true even with sibling missing")
		}
		if r.RepositoryID == "billing" && r.Reason == "" {
			t.Error("billing: expected Reason explaining the gap")
		}
	}
}

func TestBuildSummary_FailingChecks_AnchorAC2_RepoStatusFailed(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "spine"): fixtureEvidence(t, "run-1", "spine", func(ev *domain.ExecutionEvidence) {
			ev.RequiredChecks = []string{"unit-tests", "lint"}
			ev.CheckResults = []domain.CheckResult{
				{CheckID: "unit-tests", Status: domain.CheckStatusPassed, Producer: domain.CheckProducerAutomated, ProducedBy: "ci"},
				{CheckID: "lint", Status: domain.CheckStatusFailed, Producer: domain.CheckProducerAutomated, ProducedBy: "ci", Summary: "5 lint warnings", EvidenceURI: "https://ci/lint/123"},
			}
			ev.Status = domain.EvidenceStatusFailed
		}),
	}
	run := &domain.Run{
		RunID:                "run-1",
		Status:               domain.RunStatusActive,
		AffectedRepositories: []string{"spine"},
	}
	got, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if got.Status != evidence.RunEvidenceFailed {
		t.Errorf("run status = %q, want failed", got.Status)
	}
	if len(got.Repositories) != 1 {
		t.Fatalf("repositories = %d, want 1", len(got.Repositories))
	}
	repo := got.Repositories[0]
	if len(repo.FailingChecks) != 1 || repo.FailingChecks[0] != "lint" {
		t.Errorf("FailingChecks = %v, want [lint]", repo.FailingChecks)
	}
}

func TestBuildSummary_PendingChecks_AnchorsAC4(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "spine"): fixtureEvidence(t, "run-1", "spine", func(ev *domain.ExecutionEvidence) {
			ev.RequiredChecks = []string{"unit-tests", "integration"}
			ev.CheckResults = []domain.CheckResult{
				{CheckID: "unit-tests", Status: domain.CheckStatusPassed, Producer: domain.CheckProducerAutomated, ProducedBy: "ci"},
				{CheckID: "integration", Status: domain.CheckStatusRunning, Producer: domain.CheckProducerAutomated, ProducedBy: "ci"},
			}
			ev.Status = domain.EvidenceStatusPending
		}),
	}
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusActive, AffectedRepositories: []string{"spine"}}
	got, _ := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)
	if got.Status != evidence.RunEvidencePending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.Repositories[0].PendingChecks[0] != "integration" {
		t.Errorf("PendingChecks = %v", got.Repositories[0].PendingChecks)
	}
}

func TestBuildSummary_MissingRequiredCheck_RecordedAsMissing(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "spine"): fixtureEvidence(t, "run-1", "spine", func(ev *domain.ExecutionEvidence) {
			ev.RequiredChecks = []string{"unit-tests", "security-scan"}
			// security-scan declared but no result row: the check is
			// missing, and DeriveStatus -> pending.
			ev.CheckResults = []domain.CheckResult{
				{CheckID: "unit-tests", Status: domain.CheckStatusPassed, Producer: domain.CheckProducerAutomated, ProducedBy: "ci"},
			}
			ev.Status = domain.EvidenceStatusPending
		}),
	}
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusActive, AffectedRepositories: []string{"spine"}}
	got, _ := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)
	repo := got.Repositories[0]
	if len(repo.MissingChecks) != 1 || repo.MissingChecks[0] != "security-scan" {
		t.Errorf("MissingChecks = %v, want [security-scan]", repo.MissingChecks)
	}
}

func TestBuildSummary_LogsReferencedNotEmbedded_AnchorsAC3(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "spine"): fixtureEvidence(t, "run-1", "spine", func(ev *domain.ExecutionEvidence) {
			ev.RequiredChecks = []string{"unit-tests"}
			ev.CheckResults = []domain.CheckResult{
				{
					CheckID:     "unit-tests",
					Status:      domain.CheckStatusPassed,
					Producer:    domain.CheckProducerAutomated,
					ProducedBy:  "ci/github-actions",
					Summary:     "go test ./... passed",
					EvidenceURI: "https://ci.example.com/runs/123/logs",
				},
			}
		}),
	}
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusCompleted, AffectedRepositories: []string{"spine"}}
	got, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	// AC #3: every check exposes EvidenceURI; raw logs are NOT embedded.
	repo := got.Repositories[0]
	if len(repo.Checks) != 1 {
		t.Fatalf("checks = %d", len(repo.Checks))
	}
	chk := repo.Checks[0]
	if chk.EvidenceURI != "https://ci.example.com/runs/123/logs" {
		t.Errorf("expected EvidenceURI carried through, got %q", chk.EvidenceURI)
	}

	// Marshal to JSON; assert no "stdout"/"stderr"/"output"/"raw"/"logs" key
	// that would imply inline log content. Summary single-liner is fine
	// (the schema explicitly preserves that field).
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	for _, banned := range []string{`"stdout"`, `"stderr"`, `"output"`, `"raw"`, `"logs"`} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("response contains banned key %s — raw logs must not be embedded", banned)
		}
	}
}

func TestBuildSummary_DeterministicYAMLAndJSON_AnchorsAC5(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "billing"): fixtureEvidence(t, "run-1", "billing", nil),
		keyFor("HEAD", "run-1", "spine"):   fixtureEvidence(t, "run-1", "spine", nil),
		keyFor("HEAD", "run-1", "api"):     fixtureEvidence(t, "run-1", "api", nil),
	}
	run := &domain.Run{
		RunID: "run-1", Status: domain.RunStatusCompleted,
		AffectedRepositories: []string{"spine", "billing", "api"},
	}
	got1, _ := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)

	// Re-run with a permuted AffectedRepositories order to confirm the
	// summary's repository list is deterministic regardless of input
	// ordering.
	run.AffectedRepositories = []string{"billing", "api", "spine"}
	got2, _ := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)

	j1, _ := json.Marshal(got1)
	j2, _ := json.Marshal(got2)
	if string(j1) != string(j2) {
		t.Errorf("JSON output not deterministic across input orderings")
	}
	y1, _ := yaml.Marshal(got1)
	y2, _ := yaml.Marshal(got2)
	if string(y1) != string(y2) {
		t.Errorf("YAML output not deterministic across input orderings")
	}
}

func TestBuildSummary_BadEvidenceFile_RecordedAsLoadFailed(t *testing.T) {
	files := map[string][]byte{
		keyFor("HEAD", "run-1", "spine"): []byte("not: [valid: yaml"),
	}
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusCompleted, AffectedRepositories: []string{"spine"}}
	got, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(files), []string{"HEAD"}, run)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if got.Repositories[0].Present {
		t.Error("expected Present=false for malformed evidence")
	}
	if got.Repositories[0].Reason == "" {
		t.Error("expected Reason explaining the load failure")
	}
	if got.Status != evidence.RunEvidenceMissing {
		t.Errorf("run status = %q, want missing (load failure dominates)", got.Status)
	}
}

func TestBuildSummary_NilRun_ReturnsError(t *testing.T) {
	if _, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(nil), []string{"HEAD"}, nil); err == nil {
		t.Fatal("expected error on nil run")
	}
}

func TestBuildSummary_NoAffectedRepos_StatusUnknown(t *testing.T) {
	run := &domain.Run{RunID: "run-1", Status: domain.RunStatusCompleted}
	got, err := evidence.BuildSummary(context.Background(), newFakeReaderWithFiles(nil), []string{"HEAD"}, run)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if got.Status != evidence.RunEvidenceUnknown {
		t.Errorf("status = %q, want unknown", got.Status)
	}
}
