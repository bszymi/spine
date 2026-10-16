package domain_test

// INIT-023 TASK-012, audit P3-27 (AC 16, domain table): the retry budget
// predicate.

import (
	"testing"

	"github.com/bszymi/spine/core/domain"
)

func TestRetriesExhausted(t *testing.T) {
	type row struct {
		name     string
		status   domain.RepositoryMergeStatus
		class    domain.MergeFailureClass
		attempts int
		max      int
		want     bool
	}
	var rows []row
	for _, class := range []domain.MergeFailureClass{domain.MergeFailureNetwork, domain.MergeFailureRemoteUnavailable} {
		rows = append(rows,
			row{"transient below the bound", domain.RepositoryMergeStatusFailed, class, 2, 3, false},
			row{"transient at the bound", domain.RepositoryMergeStatusFailed, class, 3, 3, false},
			row{"transient past the bound", domain.RepositoryMergeStatusFailed, class, 4, 3, true},
			row{"transient far past the bound", domain.RepositoryMergeStatusFailed, class, 9, 3, true},
			row{"unbounded (max 0)", domain.RepositoryMergeStatusFailed, class, 9, 0, false},
			row{"unbounded (max < 0)", domain.RepositoryMergeStatusFailed, class, 9, -1, false},
			row{"merged", domain.RepositoryMergeStatusMerged, class, 9, 3, false},
			row{"pending", domain.RepositoryMergeStatusPending, class, 9, 3, false},
		)
	}
	for _, class := range []domain.MergeFailureClass{domain.MergeFailureConflict, domain.MergeFailureUnknown,
		domain.MergeFailurePrecondition, domain.MergeFailureAuth, domain.MergeFailureBranchProtection} {
		rows = append(rows, row{"permanent " + string(class), domain.RepositoryMergeStatusFailed, class, 9, 3, false})
	}
	for _, r := range rows {
		o := domain.RepositoryMergeOutcome{Status: r.status, FailureClass: r.class, Attempts: r.attempts}
		if got := o.RetriesExhausted(r.max); got != r.want {
			t.Errorf("%s (%s, %s, attempts=%d, max=%d) = %v, want %v", r.name, r.status, r.class, r.attempts, r.max, got, r.want)
		}
	}
}

func TestFirstExhaustedOutcome(t *testing.T) {
	outcomes := []domain.RepositoryMergeOutcome{
		{RepositoryID: "a", Status: domain.RepositoryMergeStatusMerged, Attempts: 9},
		{RepositoryID: "b", Status: domain.RepositoryMergeStatusFailed, FailureClass: domain.MergeFailureNetwork, Attempts: 4},
		{RepositoryID: "c", Status: domain.RepositoryMergeStatusFailed, FailureClass: domain.MergeFailureNetwork, Attempts: 7},
	}
	if got := domain.FirstExhaustedOutcome(outcomes, 3); got == nil || got.RepositoryID != "b" {
		t.Fatalf("FirstExhaustedOutcome = %+v, want b (first in slice order)", got)
	}
	got := domain.FirstExhaustedOutcome(outcomes, 3)
	got.Attempts = 0
	if outcomes[1].Attempts != 4 {
		t.Fatal("FirstExhaustedOutcome returned the slice element, not a copy")
	}
	if got := domain.FirstExhaustedOutcome(outcomes, 0); got != nil {
		t.Fatalf("max 0 = %+v, want nil", got)
	}
	if got := domain.FirstExhaustedOutcome(nil, 3); got != nil {
		t.Fatalf("no outcomes = %+v, want nil", got)
	}
}

// Review round 1: an attempt whose merge landed locally and whose push
// failed never exhausts the budget — the run's merge is on the local
// target, so the run is not routed as failed; its push is retried.
func TestRetriesExhausted_MergedLocallyNeverExhausts(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	pushed := domain.RepositoryMergeOutcome{Status: domain.RepositoryMergeStatusFailed, FailureClass: domain.MergeFailureNetwork,
		Attempts: 9, FailureDetail: domain.PushFailureDetail(sha, "git push: network error")}
	if !pushed.MergedLocally() || pushed.RetriesExhausted(3) {
		t.Fatalf("push-phase outcome: MergedLocally=%v RetriesExhausted=%v, want true/false", pushed.MergedLocally(), pushed.RetriesExhausted(3))
	}
	if want := "local merge succeeded (sha=" + sha + ") but push failed: git push: network error"; pushed.FailureDetail != want {
		t.Fatalf("PushFailureDetail = %q, want the recorded format %q", pushed.FailureDetail, want)
	}
	merging := pushed
	merging.FailureDetail = "git merge: repository locked"
	if merging.MergedLocally() || !merging.RetriesExhausted(3) {
		t.Fatal("a merge-phase transient failure must stay bounded")
	}
	merged := pushed
	merged.Status = domain.RepositoryMergeStatusMerged
	if merged.MergedLocally() {
		t.Fatal("a merged outcome is not a failed push")
	}
	if got := domain.FirstExhaustedOutcome([]domain.RepositoryMergeOutcome{pushed}, 3); got != nil {
		t.Fatalf("FirstExhaustedOutcome over a push-phase outcome = %+v, want nil", got)
	}
}
