package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

var someTime = time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

func validOutcome() domain.RepositoryMergeOutcome {
	return domain.RepositoryMergeOutcome{
		RunID:        "run-1",
		RepositoryID: "payments-service",
		Status:       domain.RepositoryMergeStatusPending,
		SourceBranch: "spine/run/run-1",
		TargetBranch: "main",
	}
}

func TestRepositoryMergeStatus_IsTerminal(t *testing.T) {
	cases := map[domain.RepositoryMergeStatus]bool{
		domain.RepositoryMergeStatusPending:             false,
		domain.RepositoryMergeStatusFailed:              false,
		domain.RepositoryMergeStatusMerged:              true,
		domain.RepositoryMergeStatusSkipped:             true,
		domain.RepositoryMergeStatusResolvedExternally:  true,
	}
	for status, want := range cases {
		if got := status.IsTerminal(); got != want {
			t.Errorf("status %q IsTerminal: got %v, want %v", status, got, want)
		}
	}
}

func TestMergeFailureClass_IsTransient(t *testing.T) {
	transient := []domain.MergeFailureClass{
		domain.MergeFailureNetwork,
		domain.MergeFailureRemoteUnavailable,
	}
	permanent := []domain.MergeFailureClass{
		domain.MergeFailureUnknown,
		domain.MergeFailureConflict,
		domain.MergeFailureBranchProtection,
		domain.MergeFailurePrecondition,
		domain.MergeFailureAuth,
	}
	for _, c := range transient {
		if !c.IsTransient() {
			t.Errorf("class %q expected transient", c)
		}
	}
	for _, c := range permanent {
		if c.IsTransient() {
			t.Errorf("class %q expected permanent", c)
		}
	}
}

func TestRepositoryMergeOutcome_IsTerminal_StatusOnly(t *testing.T) {
	o := validOutcome()
	o.Status = domain.RepositoryMergeStatusMerged
	o.MergeCommitSHA = "deadbeef"
	if !o.IsTerminal() {
		t.Errorf("merged outcome must be terminal")
	}
	o = validOutcome()
	o.Status = domain.RepositoryMergeStatusSkipped
	if !o.IsTerminal() {
		t.Errorf("skipped outcome must be terminal")
	}
}

func TestRepositoryMergeOutcome_IsTerminal_FailedSplitsByClass(t *testing.T) {
	o := validOutcome()
	o.Status = domain.RepositoryMergeStatusFailed
	o.FailureClass = domain.MergeFailureNetwork
	if o.IsTerminal() {
		t.Errorf("failed+transient must NOT be terminal (scheduler should retry)")
	}

	o.FailureClass = domain.MergeFailureConflict
	if !o.IsTerminal() {
		t.Errorf("failed+permanent must be terminal (no retry without human action)")
	}
}

func TestRepositoryMergeOutcome_IsPrimaryRepository(t *testing.T) {
	o := validOutcome()
	if o.IsPrimaryRepository() {
		t.Errorf("non-primary repo must report false")
	}
	o.RepositoryID = domain.PrimaryRepositoryID
	if !o.IsPrimaryRepository() {
		t.Errorf("primary repo must report true")
	}
}

func TestRepositoryMergeOutcome_LogFields_BaseAlwaysPresent(t *testing.T) {
	o := validOutcome()
	o.Attempts = 3

	fields := o.LogFields()
	keys := flattenKeys(fields)

	for _, want := range []string{
		"run_id", "repository_id", "merge_status",
		"source_branch", "target_branch", "merge_attempts",
	} {
		if _, ok := keys[want]; !ok {
			t.Errorf("LogFields missing required key %q; got keys %v", want, keys)
		}
	}
}

func TestRepositoryMergeOutcome_LogFields_OptionalGated(t *testing.T) {
	o := validOutcome()
	o.Status = domain.RepositoryMergeStatusFailed
	o.FailureClass = domain.MergeFailureNetwork
	o.FailureDetail = "connection refused"
	o.ResolvedBy = "actor:bszymi"

	keys := flattenKeys(o.LogFields())
	for _, want := range []string{"failure_class", "failure_transient", "resolved_by"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("LogFields missing optional key %q when populated; got keys %v", want, keys)
		}
	}

	bare := validOutcome().LogFields()
	bareKeys := flattenKeys(bare)
	for _, unwanted := range []string{
		"failure_class", "failure_transient",
		"merge_commit_sha", "ledger_commit_sha", "resolved_by",
	} {
		if _, ok := bareKeys[unwanted]; ok {
			t.Errorf("bare outcome LogFields must not include %q (avoid empty fields in dashboards)", unwanted)
		}
	}
}

func TestRepositoryMergeOutcome_Validate_OK(t *testing.T) {
	cases := []struct {
		name string
		mut  func(o *domain.RepositoryMergeOutcome)
	}{
		{
			name: "pending baseline",
			mut:  func(*domain.RepositoryMergeOutcome) {},
		},
		{
			name: "merged with sha and timestamp",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusMerged
				o.MergeCommitSHA = "abc123"
				now := someTime
				o.MergedAt = &now
			},
		},
		{
			name: "primary merged with ledger commit",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.RepositoryID = domain.PrimaryRepositoryID
				o.Status = domain.RepositoryMergeStatusMerged
				o.MergeCommitSHA = "abc123"
				o.LedgerCommitSHA = "ledger456"
				now := someTime
				o.MergedAt = &now
			},
		},
		{
			name: "failed with class",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusFailed
				o.FailureClass = domain.MergeFailureConflict
				o.FailureDetail = "merge conflict at foo.go"
			},
		},
		{
			name: "skipped",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusSkipped
			},
		},
		{
			name: "resolved externally with audit",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusResolvedExternally
				o.ResolvedBy = "actor:bszymi"
				o.ResolutionReason = "merged manually after rebase"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := validOutcome()
			tc.mut(&o)
			if err := o.Validate(); err != nil {
				t.Fatalf("Validate: unexpected error: %v", err)
			}
		})
	}
}

func TestRepositoryMergeOutcome_Validate_Errors(t *testing.T) {
	cases := []struct {
		name      string
		mut       func(o *domain.RepositoryMergeOutcome)
		wantSubst string
	}{
		{
			name:      "empty run_id",
			mut:       func(o *domain.RepositoryMergeOutcome) { o.RunID = "" },
			wantSubst: "run_id",
		},
		{
			name:      "empty repository_id",
			mut:       func(o *domain.RepositoryMergeOutcome) { o.RepositoryID = "" },
			wantSubst: "repository_id",
		},
		{
			name:      "invalid status",
			mut:       func(o *domain.RepositoryMergeOutcome) { o.Status = "bogus" },
			wantSubst: "invalid status",
		},
		{
			name:      "empty source branch",
			mut:       func(o *domain.RepositoryMergeOutcome) { o.SourceBranch = "" },
			wantSubst: "source_branch",
		},
		{
			name:      "empty target branch",
			mut:       func(o *domain.RepositoryMergeOutcome) { o.TargetBranch = "" },
			wantSubst: "target_branch",
		},
		{
			name:      "negative attempts",
			mut:       func(o *domain.RepositoryMergeOutcome) { o.Attempts = -1 },
			wantSubst: "attempts",
		},
		{
			name: "invalid failure class",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusFailed
				o.FailureClass = "bogus-class"
			},
			wantSubst: "invalid failure_class",
		},
		{
			name: "merged without sha",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusMerged
				now := someTime
				o.MergedAt = &now
			},
			wantSubst: "merge_commit_sha required",
		},
		{
			name: "merged without timestamp",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusMerged
				o.MergeCommitSHA = "abc"
			},
			wantSubst: "merged_at required when status=merged",
		},
		{
			name: "merged with failure leftovers",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusMerged
				o.MergeCommitSHA = "abc"
				now := someTime
				o.MergedAt = &now
				o.FailureClass = domain.MergeFailureNetwork
			},
			wantSubst: "failure fields must be empty",
		},
		{
			name: "failed without class",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusFailed
			},
			wantSubst: "failure_class required",
		},
		{
			name: "failed with merge sha",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusFailed
				o.FailureClass = domain.MergeFailureConflict
				o.MergeCommitSHA = "abc"
			},
			wantSubst: "merge_commit_sha forbidden",
		},
		{
			name: "resolved-externally without audit",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusResolvedExternally
			},
			wantSubst: "resolved_by and resolution_reason required",
		},
		{
			name: "code repo with ledger sha",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.RepositoryID = "payments-service"
				o.Status = domain.RepositoryMergeStatusMerged
				o.MergeCommitSHA = "abc"
				o.LedgerCommitSHA = "ledger"
				now := someTime
				o.MergedAt = &now
			},
			wantSubst: "ledger_commit_sha is only valid on the primary repository",
		},
		{
			name: "resolved-externally with stale failure class",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusResolvedExternally
				o.ResolvedBy = "actor:bszymi"
				o.ResolutionReason = "manual"
				o.FailureClass = domain.MergeFailureNetwork
			},
			wantSubst: "failure fields must be empty when status=resolved-externally",
		},
		{
			name: "pending with stale failure detail",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusPending
				o.FailureDetail = "stale message from prior attempt"
			},
			wantSubst: "failure fields must be empty when status=pending",
		},
		{
			name: "skipped with merged_at",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusSkipped
				now := someTime
				o.MergedAt = &now
			},
			wantSubst: "merged_at forbidden when status=skipped",
		},
		{
			name: "failed with merged_at",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusFailed
				o.FailureClass = domain.MergeFailureConflict
				now := someTime
				o.MergedAt = &now
			},
			wantSubst: "merged_at forbidden when status=failed",
		},
		{
			name: "resolved-externally with merge_commit_sha",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusResolvedExternally
				o.ResolvedBy = "actor:bszymi"
				o.ResolutionReason = "manual"
				o.MergeCommitSHA = "abc"
			},
			wantSubst: "merge_commit_sha forbidden when status=resolved-externally",
		},
		{
			name: "resolved-externally with merged_at",
			mut: func(o *domain.RepositoryMergeOutcome) {
				o.Status = domain.RepositoryMergeStatusResolvedExternally
				o.ResolvedBy = "actor:bszymi"
				o.ResolutionReason = "manual"
				now := someTime
				o.MergedAt = &now
			},
			wantSubst: "merged_at forbidden when status=resolved-externally",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := validOutcome()
			tc.mut(&o)
			err := o.Validate()
			if err == nil {
				t.Fatalf("Validate: expected error containing %q, got nil", tc.wantSubst)
			}
			if !strings.Contains(err.Error(), tc.wantSubst) {
				t.Fatalf("Validate: error %q does not contain %q", err.Error(), tc.wantSubst)
			}
		})
	}
}

func flattenKeys(fields []any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(fields); i += 2 {
		k, ok := fields[i].(string)
		if !ok {
			continue
		}
		out[k] = fields[i+1]
	}
	return out
}
