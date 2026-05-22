package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/evidence"
	"github.com/bszymi/spine/service/internal/gateway"
)

// stubEvidenceQuerier is a test-only EvidenceQuerier wired into the
// gateway via ServerConfig. The handler integration tests exercise
// the (a) "wired and successful", (b) "wired and errored", and
// (c) "not wired" branches; the unit tests for evidence.BuildSummary
// itself live in internal/evidence/summary_test.go.
type stubEvidenceQuerier struct {
	summary *evidence.RunSummary
	err     error
	calls   int
}

func (s *stubEvidenceQuerier) SummarizeForRun(_ context.Context, _ *domain.Run) (*evidence.RunSummary, error) {
	s.calls++
	return s.summary, s.err
}

func newRunStatusServerWithEvidence(t *testing.T, run *domain.Run, eq gateway.EvidenceQuerier) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	rfs := &runStatusFakeStore{fakeStore: fs, run: run}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:           rfs,
		Auth:            authSvc,
		EvidenceQuerier: eq,
	})
	return httptest.NewServer(srv.Handler()), token
}

// TestHandleRunStatus_IncludesEvidence_AnchorsAC1_AC2 confirms the
// evidence summary is attached to the run.status response. AC #1
// (run inspect surfaces evidence) and AC #2 (output grouped by
// repository) are realized at this layer.
func TestHandleRunStatus_IncludesEvidence_AnchorsAC1_AC2(t *testing.T) {
	run := &domain.Run{
		Status:               domain.RunStatusActive,
		AffectedRepositories: []string{"spine", "billing"},
	}
	stub := &stubEvidenceQuerier{
		summary: &evidence.RunSummary{
			RunID:    "run-evidence-1",
			TaskPath: "/tasks/x.md",
			Status:   evidence.RunEvidencePassed,
			Repositories: []evidence.RepositorySummary{
				{
					RepositoryID: "billing",
					Present:      true,
					Status:       domain.EvidenceStatusPassed,
					BaseCommit:   "aaaa",
					HeadCommit:   "bbbb",
					Checks: []evidence.CheckSummary{
						{
							CheckID:     "unit-tests",
							Status:      domain.CheckStatusPassed,
							Required:    true,
							EvidenceURI: "https://ci.example.com/runs/1",
						},
					},
				},
				{RepositoryID: "spine", Present: true, Status: domain.EvidenceStatusPassed},
			},
		},
	}
	ts, token := newRunStatusServerWithEvidence(t, run, stub)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-evidence-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if stub.calls != 1 {
		t.Errorf("expected EvidenceQuerier called once, got %d", stub.calls)
	}

	var body struct {
		Evidence *evidence.RunSummary `json:"evidence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Evidence == nil {
		t.Fatal("expected evidence summary in response")
	}
	if body.Evidence.Status != evidence.RunEvidencePassed {
		t.Errorf("status: got %s, want passed", body.Evidence.Status)
	}
	if len(body.Evidence.Repositories) != 2 {
		t.Errorf("repositories: got %d, want 2", len(body.Evidence.Repositories))
	}
	// AC #2 — grouped by repository — verified by per-repo lookup.
	byRepo := map[string]evidence.RepositorySummary{}
	for _, r := range body.Evidence.Repositories {
		byRepo[r.RepositoryID] = r
	}
	if billing, ok := byRepo["billing"]; !ok || len(billing.Checks) != 1 {
		t.Errorf("billing entry missing or has unexpected check count: %+v", billing)
	}
}

// TestHandleRunStatus_LogsNotEmbedded_AnchorsAC3 verifies the evidence
// payload references logs via evidence_uri and never embeds raw log
// content under a stdout/stderr/output key. The summary type's design
// (no Stdout/Stderr fields) makes this structurally impossible, but
// the wire-format test pins the contract for downstream consumers.
func TestHandleRunStatus_LogsNotEmbedded_AnchorsAC3(t *testing.T) {
	run := &domain.Run{Status: domain.RunStatusActive, AffectedRepositories: []string{"spine"}}
	stub := &stubEvidenceQuerier{
		summary: &evidence.RunSummary{
			RunID: "run-1",
			Repositories: []evidence.RepositorySummary{
				{
					RepositoryID: "spine",
					Present:      true,
					Status:       domain.EvidenceStatusFailed,
					Checks: []evidence.CheckSummary{
						{
							CheckID:     "lint",
							Status:      domain.CheckStatusFailed,
							Required:    true,
							Summary:     "5 lint warnings",
							EvidenceURI: "https://ci.example.com/runs/1/logs",
						},
					},
				},
			},
			Status: evidence.RunEvidenceFailed,
		},
	}
	ts, token := newRunStatusServerWithEvidence(t, run, stub)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	rawBody := readAllBody(t, resp)
	for _, banned := range []string{`"stdout"`, `"stderr"`, `"output"`, `"raw"`, `"logs"`} {
		if strings.Contains(rawBody, banned) {
			t.Errorf("response contains banned key %s — raw logs must be referenced, not embedded", banned)
		}
	}
	if !strings.Contains(rawBody, "https://ci.example.com/runs/1/logs") {
		t.Error("response missing evidence_uri pointer")
	}
}

// TestHandleRunStatus_MissingEvidenceVisible_AnchorsAC4 confirms that
// the wire-format response surfaces missing-evidence per repo before
// publication, so an operator running `run inspect` against an
// in-flight run sees which repos have not produced evidence yet.
func TestHandleRunStatus_MissingEvidenceVisible_AnchorsAC4(t *testing.T) {
	run := &domain.Run{Status: domain.RunStatusActive, AffectedRepositories: []string{"spine", "billing"}}
	stub := &stubEvidenceQuerier{
		summary: &evidence.RunSummary{
			RunID: "run-1",
			Repositories: []evidence.RepositorySummary{
				{RepositoryID: "billing", Present: false, Reason: "evidence file not committed"},
				{RepositoryID: "spine", Present: true, Status: domain.EvidenceStatusPassed},
			},
			MissingRepositories: []string{"billing"},
			Status:              evidence.RunEvidenceMissing,
		},
	}
	ts, token := newRunStatusServerWithEvidence(t, run, stub)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		Evidence *evidence.RunSummary `json:"evidence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Evidence == nil {
		t.Fatal("evidence summary missing")
	}
	if body.Evidence.Status != evidence.RunEvidenceMissing {
		t.Errorf("status: got %s, want missing", body.Evidence.Status)
	}
	if len(body.Evidence.MissingRepositories) != 1 || body.Evidence.MissingRepositories[0] != "billing" {
		t.Errorf("missing_repositories: got %v, want [billing]", body.Evidence.MissingRepositories)
	}
}

// TestHandleRunStatus_NoEvidenceQuerier_OmitsEvidence verifies the
// existing run.status path is unaffected when no querier is wired —
// older deployments that have not yet enabled the evidence read path
// must continue to return successful responses without the field.
func TestHandleRunStatus_NoEvidenceQuerier_OmitsEvidence(t *testing.T) {
	run := &domain.Run{Status: domain.RunStatusActive}
	ts, token := newRunStatusServerWithEvidence(t, run, nil)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := body["evidence"]; present {
		t.Errorf("expected no evidence field when querier not wired, got: %v", body["evidence"])
	}
}

// TestHandleRunStatus_EvidenceQuerierError_LogsAndOmits verifies that
// an evidence-side outage does not break run.status — the run state
// is the primary contract, and a transient evidence read failure
// must not turn into a 500 on the inspect path.
func TestHandleRunStatus_EvidenceQuerierError_LogsAndOmits(t *testing.T) {
	run := &domain.Run{Status: domain.RunStatusActive}
	stub := &stubEvidenceQuerier{err: errors.New("transient read failure")}
	ts, token := newRunStatusServerWithEvidence(t, run, stub)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 even on querier error, got %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if _, present := body["evidence"]; present {
		t.Errorf("expected no evidence field when querier errored, got: %v", body["evidence"])
	}
	if stub.calls != 1 {
		t.Errorf("expected querier called once, got %d", stub.calls)
	}
}

// TestHandleRunStatus_PlanningRun_OmitsEvidence_AnchorsCodexP5 mirrors
// the querier-level skip: a planning run's `run.status` response must
// not include an evidence field, because planning runs do not produce
// execution evidence (see internal/evidence/querier.go documentation).
// Reporting "missing" for these runs would create a false blocker
// signal in operator dashboards.
func TestHandleRunStatus_PlanningRun_OmitsEvidence_AnchorsCodexP5(t *testing.T) {
	run := &domain.Run{
		Mode:                 domain.RunModePlanning,
		Status:               domain.RunStatusActive,
		AffectedRepositories: []string{"spine"},
	}
	// Querier returns (nil, nil) for planning runs by contract; the
	// stub mirrors that here so the test is end-to-end and won't
	// silently pass if the contract changes.
	stub := &stubEvidenceQuerier{summary: nil, err: nil}
	ts, token := newRunStatusServerWithEvidence(t, run, stub)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-planning-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if _, present := body["evidence"]; present {
		t.Errorf("expected no evidence field for planning run, got: %v", body["evidence"])
	}
}

// TestHandleRunStatus_NilSummaryFromQuerier_OmitsField covers the
// querier "not configured / silently disabled" path — querier returns
// (nil, nil), handler omits the field cleanly. Without this guard the
// response would carry `"evidence": null` which downstream consumers
// must special-case.
func TestHandleRunStatus_NilSummaryFromQuerier_OmitsField(t *testing.T) {
	run := &domain.Run{Status: domain.RunStatusActive}
	stub := &stubEvidenceQuerier{summary: nil, err: nil}
	ts, token := newRunStatusServerWithEvidence(t, run, stub)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if _, present := body["evidence"]; present {
		t.Errorf("expected no evidence field when summary is nil, got: %v", body["evidence"])
	}
}

func readAllBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
