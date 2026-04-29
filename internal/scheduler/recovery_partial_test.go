package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scheduler"
)

// TestRunRetryCycle_ResumesPartiallyMerged pins the EPIC-005 TASK-003
// AC: "Scheduler can resume partial merge runs." A run sitting in
// partially-merged is resumed once the previously-failed code repo's
// outcome has been cleared (the operator-resolution signal). Without
// that signal the run stays parked — see
// TestRunRetryCycle_PartiallyMergedResumeIsGatedOnResolution.
func TestRunRetryCycle_ResumesPartiallyMerged(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r-partial", Status: domain.RunStatusPartiallyMerged, TraceID: "trace-1"},
	}
	// Operator has resolved the conflict and re-marked the previously
	// failed outcome as resolved-externally. The scheduler treats this
	// non-failed state as the "ready to retry" signal.
	mergedAt := time.Now()
	fs.outcomes = map[string][]domain.RepositoryMergeOutcome{
		"r-partial": {
			{RunID: "r-partial", RepositoryID: "spine", Status: domain.RepositoryMergeStatusMerged,
				SourceBranch: "spine/run/r-partial", TargetBranch: "main",
				MergeCommitSHA: "sha-spine", Attempts: 1, MergedAt: &mergedAt},
			{RunID: "r-partial", RepositoryID: "payments-service",
				Status: domain.RepositoryMergeStatusResolvedExternally,
				SourceBranch: "spine/run/r-partial", TargetBranch: "main",
				ResolvedBy: "actor-op-1", ResolutionReason: "manual conflict resolution", Attempts: 1},
		},
	}

	var retried []string
	retryFn := func(_ context.Context, runID string) error {
		retried = append(retried, runID)
		return nil
	}

	sched := scheduler.New(fs, &fakeEventRouter{}, scheduler.WithCommitRetry(retryFn, 0, 0))
	sched.RunRetryCycle(context.Background())

	if len(retried) != 1 || retried[0] != "r-partial" {
		t.Errorf("retry callback: got %v, want [r-partial]", retried)
	}
	if got := fs.runs[0].Status; got != domain.RunStatusCommitting {
		t.Errorf("run status after retry sweep: got %s, want committing", got)
	}
	if fs.updatedRuns["r-partial"] != domain.RunStatusCommitting {
		t.Errorf("expected updatedRuns[r-partial]=committing, got %v", fs.updatedRuns)
	}
}

// TestRunRetryCycle_PartiallyMergedResumeIsGatedOnResolution pins
// the codex pass-1 finding: the resume MUST NOT flip the run back to
// committing when a previously-failed code repo's outcome is still
// recorded as `failed`. Otherwise every tick would loop the run
// committing → partially-merged, emit duplicate events, and inflate
// the primary outcome's attempts counter without making progress.
func TestRunRetryCycle_PartiallyMergedResumeIsGatedOnResolution(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r-stuck", Status: domain.RunStatusPartiallyMerged, TraceID: "trace-1"},
	}
	mergedAt := time.Now()
	fs.outcomes = map[string][]domain.RepositoryMergeOutcome{
		"r-stuck": {
			{RunID: "r-stuck", RepositoryID: "spine", Status: domain.RepositoryMergeStatusMerged,
				SourceBranch: "spine/run/r-stuck", TargetBranch: "main",
				MergeCommitSHA: "sha-spine", Attempts: 1, MergedAt: &mergedAt},
			// Failed code-repo outcome is still on file (operator has
			// not acted yet). This is the steady-state of an
			// unresolved partial merge.
			{RunID: "r-stuck", RepositoryID: "payments-service",
				Status: domain.RepositoryMergeStatusFailed,
				SourceBranch: "spine/run/r-stuck", TargetBranch: "main",
				FailureClass: domain.MergeFailureConflict, FailureDetail: "git merge: merge conflict",
				Attempts: 1},
		},
	}

	var retried []string
	retryFn := func(_ context.Context, runID string) error {
		retried = append(retried, runID)
		return nil
	}

	sched := scheduler.New(fs, &fakeEventRouter{}, scheduler.WithCommitRetry(retryFn, 0, 0))
	sched.RunRetryCycle(context.Background())

	// Run stays parked — no transition, no callback.
	if len(retried) != 0 {
		t.Errorf("retry callback: got %v, want none", retried)
	}
	if got := fs.runs[0].Status; got != domain.RunStatusPartiallyMerged {
		t.Errorf("run status: got %s, want unchanged partially-merged", got)
	}
	if _, ok := fs.updatedRuns["r-stuck"]; ok {
		t.Errorf("unresolved partial-merge should not be transitioned, got updatedRuns=%v",
			fs.updatedRuns)
	}
}

// TestRunRetryCycle_CommittingRunsStayCommitting confirms the existing
// committing-retry path is unaffected by the partially-merged
// extension: the run state is not transitioned (the transient retry
// trigger is the run's own concern via retryMerge), only the callback
// fires.
func TestRunRetryCycle_CommittingRunsStayCommitting(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r-committing", Status: domain.RunStatusCommitting, TraceID: "trace-1"},
	}

	var retried []string
	retryFn := func(_ context.Context, runID string) error {
		retried = append(retried, runID)
		return nil
	}

	sched := scheduler.New(fs, &fakeEventRouter{}, scheduler.WithCommitRetry(retryFn, 0, 0))
	sched.RunRetryCycle(context.Background())

	if len(retried) != 1 || retried[0] != "r-committing" {
		t.Errorf("retry callback: got %v, want [r-committing]", retried)
	}
	if got := fs.runs[0].Status; got != domain.RunStatusCommitting {
		t.Errorf("run status: got %s, want unchanged committing", got)
	}
	// fakeStore.TransitionRunStatus updates updatedRuns even for no-op
	// transitions; for committing runs we never call it, so the map
	// must NOT contain the run.
	if _, ok := fs.updatedRuns["r-committing"]; ok {
		t.Errorf("committing run should not be transitioned, got updatedRuns=%v", fs.updatedRuns)
	}
}

// TestRunRetryCycle_NoOpWithoutCommitRetryFn confirms the cycle is a
// no-op when the retry function is not configured. Without this guard
// retryCommittingRuns would panic dereferencing a nil function for
// every committing run on every tick.
func TestRunRetryCycle_NoOpWithoutCommitRetryFn(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r-partial", Status: domain.RunStatusPartiallyMerged, TraceID: "trace-1"},
	}

	sched := scheduler.New(fs, &fakeEventRouter{})
	sched.RunRetryCycle(context.Background()) // must not panic

	// Run status untouched.
	if got := fs.runs[0].Status; got != domain.RunStatusPartiallyMerged {
		t.Errorf("run status: got %s, want unchanged partially-merged", got)
	}
}
