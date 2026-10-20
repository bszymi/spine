package engine

// Shared working-tree sequences (INIT-023 TASK-012; audit F9).
//
// Standalone `spine serve` hands the engine the workspace's one
// *git.CLIClient, whose stateful operations share one on-disk working
// tree. The engine's multi-step git sequences therefore run as git-only
// blocks under that tree's lock (git.WorkingTreeGuard), each taken fresh,
// so its entry restore heals a tree a crash or an operator left dirty or
// off main before the sequence touches it:
//
//	Sequence            Block (git only)                            After the block
//	status cascade      re-check; applyCommitStatus                 log-and-proceed, as before
//	primary merge       re-check; Merge|MergeBound → abort on       record the outcome, classify, retry /
//	                    error → verify (TASK-005), V1 (TASK-017),    route (handlePermanentMergeFailure,
//	                    governance assertion + marker (TASK-006)     refuseStatusLifecycle,
//	                    → take back a refused bound merge →          settleGovernedRefusal)
//	                    Push|PushCommit
//	run start           Head + CreateBranch(run, "HEAD")            PushBranch, code repositories
//	recovery ledger     checkout run → commit → push → checkout     the callers' outcome writes and events
//	code repository     merge → abort / verify + take back → push   the caller records the outcome
//
// No block emits an event or routes a step: MemoryQueue.Publish blocks
// while the queue is full, and a consumer that needs the tree would
// deadlock with a holder that emits. The store is not written inside a
// block either, with one exception: the governance guard's consumption
// marker of an approved merge (INIT-023 TASK-006, checkGovernedMerge),
// which names the merge commit and must land before the push the block
// holds the tree across — a single-row write that neither emits nor
// routes. The other store access inside a block is the run-status
// re-check below, a read.
//
// An operator whose git is not a WorkingTreeGuard (SMP's RealGitOperator,
// every test fake) runs each block's fn directly, so its sequence is
// exactly today's — except the re-check, which runs either way.

import (
	"context"
	"errors"
	"fmt"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// errRunSuperseded is returned from a block whose run is no longer in
// committing when the block starts (design-check DC-2): a second entry for
// the same run — the HTTP submit's merge and the scheduler's commit retry
// both reach MergeRunBranch — stands down instead of merging a branch the
// first entry already deleted, overwriting the merged outcome with a
// failure and flipping the completed run to failed. MergeRunBranch then
// returns nil with no outcome recorded and nothing routed. It relies on
// the ordering completeAfterMerge already has: the run status moves before
// CleanupRunBranch deletes the branch (a locked call), so a second entry
// either sees the new status or merges a still-existing branch as "already
// up to date". TASK-008's compare-and-swap writes remain the store-level
// backstop.
var errRunSuperseded = errors.New("engine: the run is no longer committing")

// runRecheckError is a failed re-read of the run at the start of a block:
// a store error, returned as is (the run stays in committing).
type runRecheckError struct{ err error }

func (e *runRecheckError) Error() string { return "re-check run status: " + e.err.Error() }
func (e *runRecheckError) Unwrap() error { return e.err }

// withTree runs fn as a git-only sequence on the primary repository's
// working tree: under its lock when the operator is a git.WorkingTreeGuard,
// directly otherwise. Never emit events, route steps or write the store
// inside fn.
func (o *Orchestrator) withTree(ctx context.Context, fn func(ctx context.Context) error) error {
	if g, ok := o.git.(git.WorkingTreeGuard); ok {
		return g.WithWorkingTree(ctx, fn)
	}
	return fn(ctx)
}

// withClientTree is withTree for a code repository's client. Code-repo
// clients register no home branch, so this takes the lock only.
func withClientTree(ctx context.Context, client git.GitClient, fn func(ctx context.Context) error) error {
	if g, ok := client.(git.WorkingTreeGuard); ok {
		return g.WithWorkingTree(ctx, fn)
	}
	return fn(ctx)
}

// requireStillCommitting is the re-check each merge block starts with.
func (o *Orchestrator) requireStillCommitting(ctx context.Context, runID string) error {
	cur, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return &runRecheckError{err: err}
	}
	if cur.Status != domain.RunStatusCommitting {
		observe.Logger(ctx).Info("run is no longer committing; this merge attempt stands down",
			"run_id", runID, "status", string(cur.Status))
		return errRunSuperseded
	}
	return nil
}

// isTreeWait reports a *git.TreeWaitError: the caller's context ended
// while another sequence held the tree. Nothing ran, so the attempt is not
// a merge failure (§2.7): MergeRunBranch returns it without recording an
// outcome, and the run stays in committing for the scheduler. Classified
// as a merge failure it would be `unknown` (permanent) and loop the run
// back over a client disconnect.
func isTreeWait(err error) bool {
	var we *git.TreeWaitError
	return errors.As(err, &we)
}

// blockExit maps a merge block's own error (not the merge's) to what
// MergeRunBranch does with it:
//
//   - errRunSuperseded: stand down — done, nil.
//   - a *git.TreeWaitError or a failed re-check: done, the error, nothing
//     recorded.
//   - anything else, in practice a *git.TreeRestoreError (the entry
//     restore failed: an untracked file in the way, a stale index.lock):
//     not done; it is this attempt's merge error, recorded as a failed
//     primary outcome and classified like any other (a transient class
//     stays in committing, bounded by the retry budget).
func blockExit(err error) (done bool, ret error) {
	var rre *runRecheckError
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, errRunSuperseded):
		return true, nil
	case isTreeWait(err), errors.As(err, &rre):
		return true, err
	}
	return false, nil
}

// blockMergeError is the merge error a block's own error stands for when
// blockExit did not end the attempt.
func blockMergeError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("prepare the shared working tree: %w", err)
}
