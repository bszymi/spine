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
