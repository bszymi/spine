package gateway_test

// INIT-024 TASK-007, review finding N-17: run.status attached evidence read
// from the author-writable run branch with nothing to say so, and a reviewer
// read its "passed" checks as fact. The response now carries, per
// repository, the ref the record was read from and whether it is verified,
// plus the run-level list of unverified repositories.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/evidence"
	"gopkg.in/yaml.v3"
)

// refFileReader is an evidence.GitReader over a (ref, path) table that
// answers a miss the way CLIClient does.
type refFileReader map[string][]byte

func (f refFileReader) ReadFile(_ context.Context, ref, path string) ([]byte, error) {
	if b, ok := f[ref+"\t"+path]; ok {
		return b, nil
	}
	return nil, &git.GitError{Kind: git.ErrKindNotFound, Op: "read_file", Message: "not found"}
}

// authorEvidence is the record a run's author can push to the run branch:
// schema-valid, every required check "passed".
func authorEvidence(t *testing.T, runID, repoID, branch string) []byte {
	t.Helper()
	ev := domain.ExecutionEvidence{
		SchemaVersion:  domain.ExecutionEvidenceSchemaVersion,
		RunID:          runID,
		TaskPath:       "/tasks/x.md",
		RepositoryID:   repoID,
		BranchName:     branch,
		BaseCommit:     "0000000000000000000000000000000000000001",
		HeadCommit:     "0000000000000000000000000000000000000002",
		ChangedPaths:   domain.ChangedPathsSummary{FilesChanged: 1, Paths: []string{"a.go"}},
		RequiredChecks: []string{"unit-tests"},
		CheckResults: []domain.CheckResult{{
			CheckID: "unit-tests", Status: domain.CheckStatusPassed,
			Producer: domain.CheckProducerAutomated, ProducedBy: "ci/github-actions", Summary: "all green",
		}},
		Actor:       "user/author",
		TraceID:     "trace-1",
		Status:      domain.EvidenceStatusPassed,
		GeneratedAt: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC),
	}
	ev.Canonicalize()
	if err := ev.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	b, err := yaml.Marshal(&ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRunStatus_ShowsUnverifiedEvidence is AC 5's gateway half. An active
// run whose "passed" evidence exists only on its run branch is shown with
// verified=false, the run branch as source_ref and the repository in
// unverified_repositories; evidence on the authoritative branch is shown
// verified. At the scaffold base none of the three keys exists.
func TestRunStatus_ShowsUnverifiedEvidence(t *testing.T) {
	const branch = "spine/run/run-n17-abc"
	run := &domain.Run{
		Status:               domain.RunStatusActive,
		BranchName:           branch,
		AffectedRepositories: []string{"billing", "spine"},
	}
	reader := refFileReader{
		branch + "\t" + evidence.EvidencePath("run-n17", "billing"): authorEvidence(t, "run-n17", "billing", branch),
		"main\t" + evidence.EvidencePath("run-n17", "spine"):        authorEvidence(t, "run-n17", "spine", branch),
	}
	ts, token := newRunStatusServerWithEvidence(t, run, evidence.NewQuerier(reader))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/runs/run-n17", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct {
		Evidence map[string]any `json:"evidence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Evidence == nil {
		t.Fatal("no evidence block")
	}
	repos, _ := body.Evidence["repositories"].([]any)
	by := map[string]map[string]any{}
	for _, r := range repos {
		m, _ := r.(map[string]any)
		by[m["repository_id"].(string)] = m
	}
	billing := by["billing"]
	if billing == nil || billing["present"] != true || billing["status"] != "passed" {
		t.Fatalf("billing entry = %v", billing)
	}
	if v, ok := billing["verified"]; !ok || v != false {
		t.Errorf("billing (run branch): verified = %v (present=%v), want an explicit false", v, ok)
	}
	if billing["source_ref"] != branch {
		t.Errorf("billing: source_ref = %v, want %q", billing["source_ref"], branch)
	}
	spine := by["spine"]
	if spine == nil || spine["verified"] != true || spine["source_ref"] != "main" {
		t.Errorf("spine (authoritative branch): %v, want verified=true source_ref=main", spine)
	}
	unverified, _ := body.Evidence["unverified_repositories"].([]any)
	if len(unverified) != 1 || unverified[0] != "billing" {
		t.Errorf("unverified_repositories = %v, want [billing]", body.Evidence["unverified_repositories"])
	}
}
