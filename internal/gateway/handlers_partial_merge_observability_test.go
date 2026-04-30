package gateway_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
)

// TestRunStatus_PartiallyMergedExposesMergeOutcomes anchors EPIC-005
// TASK-005 AC #2 — "partial merge state is observable through API."
//
// A run sitting in partially-merged must surface (a) the run status,
// (b) the per-repo merge outcomes mid-recovery: the primary plus the
// already-merged code repo carry their merge_commit_sha, while the
// failed code repo carries its failure class and detail. Without this
// the operator dashboard cannot distinguish "blocked on a real
// conflict" from "blocked on auth" and recovery decisions go blind.
//
// AC #4 covers the symmetric completed-state expectation in the engine
// suite. The wire-format guarantee here matters because audit /
// observability tooling consumes this JSON shape directly.
func TestRunStatus_PartiallyMergedExposesMergeOutcomes(t *testing.T) {
	mergedAt := time.Now().Add(-5 * time.Minute)
	branch := "spine/run/run-partial-1"
	run := &domain.Run{
		Status:               domain.RunStatusPartiallyMerged,
		BranchName:           branch,
		AffectedRepositories: []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"},
	}
	outcomes := []domain.RepositoryMergeOutcome{
		{
			RunID:          "run-partial-1",
			RepositoryID:   repository.PrimaryRepositoryID,
			Status:         domain.RepositoryMergeStatusMerged,
			SourceBranch:   branch,
			TargetBranch:   "main",
			MergeCommitSHA: "primary-sha-aaa",
			Attempts:       1,
			MergedAt:       &mergedAt,
		},
		{
			RunID:          "run-partial-1",
			RepositoryID:   "payments-service",
			Status:         domain.RepositoryMergeStatusMerged,
			SourceBranch:   branch,
			TargetBranch:   "main",
			MergeCommitSHA: "code-sha-bbb",
			Attempts:       1,
			MergedAt:       &mergedAt,
		},
		{
			RunID:         "run-partial-1",
			RepositoryID:  "api-gateway",
			Status:        domain.RepositoryMergeStatusFailed,
			SourceBranch:  branch,
			TargetBranch:  "main",
			FailureClass:  domain.MergeFailureConflict,
			FailureDetail: "git merge: merge conflict in handler.go",
			Attempts:      1,
		},
	}
	ts, token := newRunStatusServerWithOutcomes(t, run, outcomes)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-partial-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Status               string                           `json:"status"`
		BranchName           string                           `json:"branch_name"`
		AffectedRepositories []string                         `json:"affected_repositories"`
		MergeOutcomes        []domain.RepositoryMergeOutcome  `json:"merge_outcomes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Status != string(domain.RunStatusPartiallyMerged) {
		t.Errorf("status: got %q, want %q", body.Status, domain.RunStatusPartiallyMerged)
	}
	if body.BranchName != branch {
		t.Errorf("branch_name: got %q, want %q", body.BranchName, branch)
	}
	if len(body.AffectedRepositories) != 3 {
		t.Errorf("affected_repositories: got %d, want 3", len(body.AffectedRepositories))
	}

	// merge_outcomes carries one row per repo in the canonical shape the
	// operator dashboards consume. Index by repo so the assertions are
	// resilient to row order — the engine writes in declared order today
	// but a future change could legitimately reshuffle.
	byRepo := map[string]domain.RepositoryMergeOutcome{}
	for _, o := range body.MergeOutcomes {
		byRepo[o.RepositoryID] = o
	}
	if len(byRepo) != 3 {
		t.Fatalf("merge_outcomes: got %d unique repos, want 3", len(byRepo))
	}

	primary, ok := byRepo[repository.PrimaryRepositoryID]
	if !ok {
		t.Fatal("merge_outcomes missing primary entry")
	}
	if primary.Status != domain.RepositoryMergeStatusMerged || primary.MergeCommitSHA != "primary-sha-aaa" {
		t.Errorf("primary: got %+v, want merged with sha primary-sha-aaa", primary)
	}

	pmt, ok := byRepo["payments-service"]
	if !ok {
		t.Fatal("merge_outcomes missing payments-service entry")
	}
	if pmt.Status != domain.RepositoryMergeStatusMerged || pmt.MergeCommitSHA != "code-sha-bbb" {
		t.Errorf("payments-service: got %+v, want merged with sha code-sha-bbb", pmt)
	}

	api, ok := byRepo["api-gateway"]
	if !ok {
		t.Fatal("merge_outcomes missing api-gateway entry")
	}
	if api.Status != domain.RepositoryMergeStatusFailed {
		t.Errorf("api-gateway: got status %s, want failed", api.Status)
	}
	if api.FailureClass != domain.MergeFailureConflict {
		t.Errorf("api-gateway: got failure_class %s, want conflict", api.FailureClass)
	}
	if api.FailureDetail == "" {
		t.Errorf("api-gateway: failure_detail must be non-empty so operators can act")
	}
	if api.MergeCommitSHA != "" {
		t.Errorf("api-gateway: merge_commit_sha must be empty on failed status, got %q",
			api.MergeCommitSHA)
	}
}
