package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-012, codex review round 8: a code repository whose
// transient merge failures spend the retry budget takes a code
// repository's permanent-failure route — the primary merges and the run
// parks in partially-merged, where an operator retries or resolves the
// repository, with the other code repository's landed merge kept —
// instead of the primary's route, which failed a legacy run while that
// merge stayed landed and partial-merge recovery then refused the run.
func TestAbandonExhaustedMerge_CodeRepositoryParksPartiallyMerged(t *testing.T) {
	const runID = "run-exhaust-code-repo"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}
	setup := setupMergeOrderTest(t, runID, "spine/run/"+runID, affected)
	setup.repos["api-gateway"].mergeErr = &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "network error"}
	ctx := context.Background()

	for attempt := 1; attempt <= 4; attempt++ {
		if err := setup.orch.MergeRunBranch(ctx, runID); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if ok, err := setup.orch.AbandonExhaustedMerge(ctx, runID, 3); ok || err != nil {
			t.Fatalf("attempt %d: AbandonExhaustedMerge = %v, %v; want false (a code repository is never routed like the primary)", attempt, ok, err)
		}
		if got := setup.store.runs[runID].Status; got != domain.RunStatusCommitting {
			t.Fatalf("attempt %d: run %s, want committing", attempt, got)
		}
	}
	if n := len(setup.primary.mergeCalls); n != 0 {
		t.Fatalf("primary merged %d times while a code repository was pending", n)
	}
	api := setup.outcomeForRepo(t, runID, "api-gateway")
	if api.Status != domain.RepositoryMergeStatusFailed || api.Attempts != 4 || api.FailureClass != domain.MergeFailureNetwork ||
		!strings.HasPrefix(api.FailureDetail, "retries exhausted (attempts=4, max_retries=3): ") {
		t.Fatalf("exhausted code repository outcome = %s %d %s %q; want it marked, class and attempts kept",
			api.Status, api.Attempts, api.FailureClass, api.FailureDetail)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCommitting {
		t.Fatalf("run %s after the hand-over, want committing (the next retry parks it)", got)
	}

	// The scheduler's retry that follows: the marked repository is not
	// merged again, the primary merges, the run parks partially merged.
	if err := setup.orch.MergeRunBranch(ctx, runID); err != nil {
		t.Fatalf("the retry after the hand-over: %v", err)
	}
	merges := 0
	for _, m := range setup.repos["api-gateway"].mergeCalls {
		if m.Strategy != "abort" && m.Source != "--abort" {
			merges++
		}
	}
	if merges != 4 {
		t.Fatalf("api-gateway merged %d times; want 4 (the marked outcome is terminal)", merges)
	}
	if n := len(setup.primary.mergeCalls); n != 1 {
		t.Fatalf("primary merged %d times; want 1", n)
	}
	run := setup.store.runs[runID]
	if run.Status != domain.RunStatusPartiallyMerged {
		t.Fatalf("run %s, want partially-merged", run.Status)
	}
	if pay := setup.outcomeForRepo(t, runID, "payments-service"); pay.Status != domain.RepositoryMergeStatusMerged {
		t.Fatalf("payments-service %s, want its landed merge kept", pay.Status)
	}
	if err := guardPartialMergeRecovery(run, "api-gateway"); err != nil {
		t.Fatalf("operator recovery of the exhausted repository refused: %v", err)
	}
	if ok, err := setup.orch.AbandonExhaustedMerge(ctx, runID, 3); ok || err != nil {
		t.Fatalf("AbandonExhaustedMerge on the parked run = %v, %v; want false", ok, err)
	}
}

// The primary keeps its route, and a marked primary outcome is exhausted
// again, so a routing that failed after the mark is retried.
func TestAbandonExhaustedMerge_MarkedOutcomes(t *testing.T) {
	marked := func(repoID string) domain.RepositoryMergeOutcome {
		return domain.RepositoryMergeOutcome{RunID: "r", RepositoryID: repoID, Status: domain.RepositoryMergeStatusFailed,
			FailureClass: domain.MergeFailureNetwork, Attempts: 4, FailureDetail: exhaustedPrefix(4, 3) + "network error"}
	}
	if !retriesAbandoned(marked("api-gateway")) || !retriesAbandoned(marked(repository.PrimaryRepositoryID)) {
		t.Fatal("a marked outcome is not recognised")
	}
	plain := marked("api-gateway")
	plain.FailureDetail = "network error"
	merged := domain.RepositoryMergeOutcome{Status: domain.RepositoryMergeStatusMerged, FailureDetail: exhaustedPrefix(4, 3)}
	if retriesAbandoned(plain) || retriesAbandoned(merged) {
		t.Fatal("an unmarked or merged outcome counts as abandoned")
	}
}
