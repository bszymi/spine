package engine

// Bounded governed-merge retries (INIT-023 TASK-012; audit P3-27).
//
// A transient merge or push failure keeps a run in committing, and the
// scheduler retried committing runs on every tick, forever:
// scheduler.WithCommitRetry stored a retry budget nobody read. A run whose
// push kept failing transiently (network, a stale index.lock reported as
// "repository locked") was retried every minute indefinitely, and
// engine-state-machine.md's "after retries" never happened.
//
// The budget is enforced where the knob lives (the scheduler gates its
// commit-retry pass on scheduler.WithCommitExhausted) and decided here,
// from the existing ledger: repository_merge_outcomes.attempts, no new
// column. AbandonExhaustedMerge ends a committing run whose recorded
// outcome shows the budget spent, routing it exactly like a permanent
// merge failure.
//
// What the bound counts (design-check note): only attempts that reach
// recordPrimaryMergeOutcome, or a code repository's outcome, with a
// transient class. A governance hold returns before the merge and records
// nothing, so it is never counted; a lock wait cut short by the caller's
// context records nothing either (§2.7); outcome-persistence failures are
// a store outage and are not counted.
//
// What it never abandons (review round 1): an attempt whose merge landed
// on the local target and whose push failed (domain.MergedLocally), and a
// later attempt of such a run, or of a run whose merge was recorded
// merged but whose completion did not follow, that failed before its own
// push (domain.KeepPendingPublication; codex rounds 6–7). The merge is
// there — the next push of the branch by any writer publishes it, or it
// is already published — so routing the run as failed would contradict
// the branch, and taking the merge back is unsafe (another writer's push
// may already have published it; runs started since may have been cut
// from it). Such a run stays in committing and is retried every tick
// until it completes, with an Error log once the budget is passed
// (publication pending).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// MergeRetriesExhaustedError is the cause recorded when a committing run's
// automatic merge retries are spent (INIT-023 TASK-012, P3-27).
type MergeRetriesExhaustedError struct {
	RepositoryID string
	Attempts     int
	MaxRetries   int
	LastClass    domain.MergeFailureClass
	LastDetail   string
}

func (e *MergeRetriesExhaustedError) Error() string {
	return fmt.Sprintf("merge retries exhausted for repository %s after %d attempts (max_retries=%d); last failure (%s): %s",
		e.RepositoryID, e.Attempts, e.MaxRetries, e.LastClass, e.LastDetail)
}

// retriesExhaustedMarker starts the failure detail of an outcome whose
// retries were abandoned.
const retriesExhaustedMarker = "retries exhausted ("

// AbandonExhaustedMerge ends a committing run whose recorded merge
// outcomes show the retry budget spent. It routes exactly like a permanent
// merge failure: a workflow with a publish step takes merge_failed (back
// to execute, per task-default), a legacy terminal-commit workflow fails
// the run (branch kept). It returns false, and changes nothing, when the
// run is not committing, maxRetries is not positive, or no outcome is
// exhausted — an outcome whose merge landed locally and only its push
// failed never is (domain.RepositoryMergeOutcome.RetriesExhausted).
//
// A code repository's exhausted outcome takes a code repository's
// permanent-failure route instead (codex review round 8): it is marked
// (below), which makes it terminal for the per-repository loop
// (retriesAbandoned), and AbandonExhaustedMerge returns false, so the
// scheduler's retry that follows merges the primary and parks the run in
// partially-merged — the lifecycle in which an operator retries or
// resolves the repository (merge_resolution.go), with any other code
// repository's landed merge kept. Routing it like a primary failure used
// to fail the run (or send it back to execute) while another code
// repository's merge stayed landed, and partial-merge recovery then
// refused the run because it was not partially merged.
//
// The exhausted outcome is re-recorded with its attempts, timestamps and
// transient class unchanged (the outcomes CHECK admits no new class, and
// the last failure was transient) and its failure detail prefixed once
// with "retries exhausted (attempts=N, max_retries=M): ". The step the run
// routes from carries domain.FailureMergeRetriesExhausted.
//
// No tree lock: this does no git work, and it routes steps (worktree.go).
// The scheduler calls it (scheduler.CommitExhaustedFunc) before each
// commit retry.
//
// Coordination (codex review round 4). Within the process it never
// overlaps a merge attempt of the same run: while one is in flight it
// returns false and the next tick decides on the attempt's own outcome,
// and a MergeRunBranch that starts while an abandonment runs stands down
// (mergeAttemptGate). So the abandonment can neither route a run whose
// attempt is publishing, nor re-record a stale outcome over a merged one.
// And an outcome counts only when it was attempted in the run's current
// committing episode — at or after the latest step activity of the run:
// after a merge_failed loop-back and rework, the previous episode's
// exhausted outcome no longer abandons the new episode before its first
// attempt; its first transient failure does (owner question 4). Across
// processes sharing one database the residual race remains until
// TASK-008's compare-and-swap writes (KNOWN-LIMITATIONS).
func (o *Orchestrator) AbandonExhaustedMerge(ctx context.Context, runID string, maxRetries int) (bool, error) {
	if maxRetries <= 0 {
		return false, nil
	}
	end, ok := mergeAttemptGate.beginAbandon(runID)
	if !ok {
		observe.Logger(ctx).Info("merge attempt in flight; the retry budget is checked on the next tick", "run_id", runID)
		return false, nil
	}
	defer end()
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("get run: %w", err)
	}
	if run.Status != domain.RunStatusCommitting {
		return false, nil
	}
	outcomes, err := o.store.ListRepositoryMergeOutcomes(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("list merge outcomes: %w", err)
	}
	// A code repository already handed to the partial-merge lifecycle
	// (marked by an earlier call) is not exhausted again; a marked primary
	// outcome is, so a routing that failed after the mark is retried.
	pending := make([]domain.RepositoryMergeOutcome, 0, len(outcomes))
	for i := range outcomes {
		if !outcomes[i].IsPrimaryRepository() && retriesAbandoned(outcomes[i]) {
			continue
		}
		pending = append(pending, outcomes[i])
	}
	ex := domain.FirstExhaustedOutcome(pending, maxRetries)
	if ex == nil {
		for i := range outcomes {
			if out := outcomes[i]; out.MergedLocally() && out.Attempts > maxRetries {
				observe.Logger(ctx).Error("governed merge landed on the local target but the run's later steps keep failing; "+
					"the run stays in committing and is retried until it completes (publication pending; never abandoned)",
					"run_id", runID, "repository_id", out.RepositoryID, "attempts", out.Attempts,
					"max_retries", maxRetries, "failure_class", string(out.FailureClass))
			}
		}
		return false, nil
	}
	current, err := o.attemptedThisEpisode(ctx, runID, ex)
	if err != nil {
		return false, err
	}
	if !current {
		observe.Logger(ctx).Info("exhausted merge outcome predates the run's current committing episode; waiting for this episode's attempt",
			"run_id", runID, "repository_id", ex.RepositoryID, "attempts", ex.Attempts)
		return false, nil
	}
	cause := &MergeRetriesExhaustedError{
		RepositoryID: ex.RepositoryID,
		Attempts:     ex.Attempts,
		MaxRetries:   maxRetries,
		LastClass:    ex.FailureClass,
		LastDetail:   strings.TrimPrefix(ex.FailureDetail, exhaustedPrefix(ex.Attempts, maxRetries)),
	}
	if !strings.HasPrefix(ex.FailureDetail, retriesExhaustedMarker) {
		marked := *ex
		marked.FailureDetail = exhaustedPrefix(ex.Attempts, maxRetries) + ex.FailureDetail
		if err := o.store.UpsertRepositoryMergeOutcome(ctx, &marked); err != nil {
			return false, fmt.Errorf("record exhausted merge outcome: %w", err)
		}
	}
	observe.AuditLog(ctx, "merge_retries_exhausted",
		"run_id", runID,
		"repository_id", ex.RepositoryID,
		"attempts", ex.Attempts,
		"max_retries", maxRetries,
		"failure_class", string(ex.FailureClass),
	)
	if !ex.IsPrimaryRepository() {
		observe.Logger(ctx).Error("code repository merge retries exhausted; it is now a terminal failure — the primary merges "+
			"and the run waits in partially-merged for an operator to retry or resolve the repository",
			"run_id", runID, "repository_id", ex.RepositoryID, "attempts", ex.Attempts,
			"max_retries", maxRetries, "failure_class", string(ex.FailureClass))
		return false, nil
	}
	observe.Logger(ctx).Error("governed merge retries exhausted; taking the permanent-failure route",
		"run_id", runID, "repository_id", ex.RepositoryID, "attempts", ex.Attempts,
		"max_retries", maxRetries, "failure_class", string(ex.FailureClass))
	return true, o.handlePermanentMergeFailure(ctx, run, cause)
}

// retriesAbandoned reports an outcome AbandonExhaustedMerge marked: a
// failure whose failure detail opens with the exhausted marker. For a
// code repository it is terminal (mergeAffectedCodeRepositories skips it
// like a permanent failure) until an operator's retry resets the outcome
// to pending or a resolution replaces it. Only AbandonExhaustedMerge
// writes the marker, so an embedder that never calls it (SMP) has none.
func retriesAbandoned(o domain.RepositoryMergeOutcome) bool {
	return o.Status == domain.RepositoryMergeStatusFailed && strings.HasPrefix(o.FailureDetail, retriesExhaustedMarker)
}

func exhaustedPrefix(attempts, maxRetries int) string {
	return fmt.Sprintf("%sattempts=%d, max_retries=%d): ", retriesExhaustedMarker, attempts, maxRetries)
}

// isRetriesExhausted reports a *MergeRetriesExhaustedError.
func isRetriesExhausted(err error) bool {
	var e *MergeRetriesExhaustedError
	return errors.As(err, &e)
}

// attemptedThisEpisode reports whether outcome was attempted in the run's
// current committing episode: its last attempt is at or after the latest
// step activity (creation, start or completion of any of the run's step
// executions). An episode begins with step activity — the commit outcome's
// step completing, a publish step starting — and merge attempts move no
// step timestamp, so an outcome attempted before the latest activity
// belongs to an earlier episode. No timestamp, or an unreadable step list,
// is "not this episode" with the error returned: the abandonment waits.
func (o *Orchestrator) attemptedThisEpisode(ctx context.Context, runID string, outcome *domain.RepositoryMergeOutcome) (bool, error) {
	if outcome.LastAttemptedAt == nil {
		return false, nil
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("list step executions: %w", err)
	}
	var latest time.Time
	for i := range execs {
		for _, ts := range []*time.Time{&execs[i].CreatedAt, execs[i].StartedAt, execs[i].CompletedAt} {
			if ts != nil && ts.After(latest) {
				latest = *ts
			}
		}
	}
	return !outcome.LastAttemptedAt.Before(latest), nil
}

// mergeAttemptGate coordinates, within one process, AbandonExhaustedMerge
// with MergeRunBranch for the same run (codex review round 4). Neither
// side ever waits: an abandonment while an attempt is in flight is
// skipped, and an attempt that starts during an abandonment stands down.
// Package level, like recoveryBindingStripes, because `spine serve` builds
// an orchestrator per workspace and the pool rebuilds them. An embedder
// that never calls AbandonExhaustedMerge (SMP) only ever increments and
// decrements the in-flight count: its merges behave exactly as before.
var mergeAttemptGate = &attemptGate{inFlight: map[string]int{}, abandoning: map[string]bool{}}

type attemptGate struct {
	mu         sync.Mutex
	inFlight   map[string]int
	abandoning map[string]bool
}

// beginAttempt registers a merge attempt of runID; ok is false (and
// nothing is registered) while an abandonment of the run is in progress.
// Nested attempts in one goroutine count up.
func (g *attemptGate) beginAttempt(runID string) (end func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.abandoning[runID] {
		return func() {}, false
	}
	g.inFlight[runID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.inFlight[runID]--
			if g.inFlight[runID] <= 0 {
				delete(g.inFlight, runID)
			}
		})
	}, true
}

// beginAbandon marks an abandonment of runID; ok is false (and nothing is
// marked) while a merge attempt of the run is in flight or another
// abandonment runs.
func (g *attemptGate) beginAbandon(runID string) (end func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight[runID] > 0 || g.abandoning[runID] {
		return func() {}, false
	}
	g.abandoning[runID] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			delete(g.abandoning, runID)
		})
	}, true
}
