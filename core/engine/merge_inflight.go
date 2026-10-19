package engine

// Merges whose post-merge checks never ran (INIT-023 wave 4 cross-task
// review, finding P2-1).
//
// verifyBoundMerge (TASK-005), the status lifecycle's V1 (TASK-017) and
// the governance guard's post-merge assertion (TASK-006) all run after
// MergeBound has moved the local main, and all accept "already up to date"
// (a replay) as a merge an earlier attempt judged: a refusal is taken back
// before the tree is released. That holds only while an attempt reaches
// its checks. Two ways it did not:
//
//   - MergeBound reported an error after git had moved main: the trailing
//     `rev-parse HEAD` failed, or the git child was killed after its ref
//     update. The block only ran `git merge --abort`, which fails with no
//     MERGE_HEAD, and left the merge on main.
//   - The process died inside the block: on SIGTERM the scheduler and the
//     HTTP server do not wait for a merge block in flight.
//
// The next attempt found main up to date, V1 and the guard returned nil
// for the replay, and the unverified merge was pushed and the run
// completed (a review-less workflow carried into workflows/ by a rename on
// main; a criss-cross resolution that reopens a Completed task).
//
// With V1 or the guard on (verifiesMergeResult), an attempt that can move
// main records CommitMetaMergeUnverified = "<base> <source>" on the run in
// the merge block, before MergeBound — one row write, like the consumption
// marker. The write that accepts the merge (with the consumption marker,
// when there is one) clears it before anything is pushed; a refusal, or a
// failure, whose take-back left main clean clears it after the block. A
// MergeBound error after main moved is taken back in the block
// (takeBackFailedMerge). And every binding-on attempt of a run that still
// carries the record settles it first (settleUnverifiedMerge), before the
// merge source is resolved against main. Both checks off: nothing is
// written and nothing changes (INIT-023 rule 2).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// CommitMetaMergeUnverified is the reserved commit_meta key a run carries
// while a merge attempt of it may have moved the local main without its
// post-merge checks having accepted the result: "<base sha> <source sha>",
// the local main before the merge and the commit merged. Written only with
// the status lifecycle or the governance guard on; "" once settled. It
// survives a restart, so the run's next attempt takes the unverified merge
// back before anything else.
const CommitMetaMergeUnverified = "spine.merge.unverified"

// errUnverifiedMergeStuck is the cause of the refusal of a run whose
// unverified merge is on the local main and cannot be taken back.
var errUnverifiedMergeStuck = errors.New("an unverified merge of the run is on the local main")

// verifiesMergeResult reports whether a merge's result is checked after
// MergeBound by more than TASK-005's structure check: V1 or the guard.
func (o *Orchestrator) verifiesMergeResult() bool {
	return o.bindingOn && (o.lifecycle != nil || o.governance != nil)
}

// parseUnverifiedMerge splits a CommitMetaMergeUnverified value.
func parseUnverifiedMerge(v string) (base, source string, ok bool) {
	base, source, ok = strings.Cut(v, " ")
	return base, source, ok && isBoundObjectID(base) && isBoundObjectID(source)
}

// recordMergeInFlight runs in the primary merge block, before MergeBound.
// It returns the local main the merge is computed on ("" when nothing was
// recorded: the checks are off, or the attempt is a replay, which cannot
// move main). A main that cannot be read, or a record that cannot be
// written, is a transient merge failure: nothing is merged, and the
// attempt counts against the retry budget like any transient failure.
func (o *Orchestrator) recordMergeInFlight(ctx context.Context, run *domain.Run, source string) (string, error) {
	if !o.verifiesMergeResult() {
		return "", nil
	}
	pre, err := o.binder.ResolveBranchHead(ctx, authoritativeBranch)
	if err == nil && !isBoundObjectID(pre) {
		err = fmt.Errorf("operator returned a malformed head %q", truncateForLog(pre))
	}
	if err != nil {
		return "", &git.GitError{Kind: git.ErrKindTransient, Op: "merge_binding",
			Message: "merge binding: the local " + authoritativeBranch + " could not be read before the merge (will retry)", Err: err}
	}
	if in, err := o.binder.IsAncestor(ctx, source, pre); err == nil && in {
		return "", nil
	}
	if err := o.mergeRunCommitMeta(ctx, run.RunID, map[string]string{CommitMetaMergeUnverified: pre + " " + source}); err != nil {
		return "", &git.GitError{Kind: git.ErrKindTransient, Op: "merge_binding",
			Message: "merge binding: the merge in flight could not be recorded (will retry)", Err: err}
	}
	return pre, nil
}

// isMergeOf reports whether tip is a merge MergeBound made of source onto
// base: a merge commit [base, source], or a fast-forward to source (an
// embedder's binder; git.CLIClient always makes a merge commit). A read
// error is "no".
func (o *Orchestrator) isMergeOf(ctx context.Context, tip, base, source string) bool {
	if tip == source {
		in, err := o.binder.IsAncestor(ctx, base, source)
		return err == nil && in
	}
	parents, err := o.binder.CommitParents(ctx, tip)
	return err == nil && len(parents) == 2 && parents[0] == base && parents[1] == source
}

// failedMerge is what takeBackFailedMerge did after a MergeBound error.
type failedMerge struct {
	// clean: the local main is known not to hold a merge of this attempt
	// (it did not move, or the merge was taken back).
	clean bool
	// stuck is the merge left on the local main when its take-back failed.
	stuck string
}

// takeBackFailedMerge runs in the merge block after MergeBound returned an
// error, when the attempt recorded pre (recordMergeInFlight). An error does
// not prove main is unchanged: git can move the ref and then fail, or be
// killed after the update. A main that moved to this attempt's merge is
// taken back to pre (takeBackToBase: a compare-and-swap; on the shared
// tree a failure arms git.CLIClient's pending take-back). A main that moved
// to anything else (another process wrote) or that cannot be read is left
// alone and the record kept: the next attempt settles it.
func (o *Orchestrator) takeBackFailedMerge(ctx context.Context, run *domain.Run, pre, source string, mergeErr error) failedMerge {
	log := observe.Logger(ctx)
	tip, err := o.binder.ResolveBranchHead(ctx, authoritativeBranch)
	if err != nil || !isBoundObjectID(tip) {
		log.Warn("merge binding: the local main could not be read after a failed merge; the next attempt checks it",
			"run_id", run.RunID, "error", err)
		return failedMerge{}
	}
	if tip == pre {
		return failedMerge{clean: true}
	}
	if !o.isMergeOf(ctx, tip, pre, source) {
		log.Warn("merge binding: the local main moved during a failed merge, not to this attempt's merge; the next attempt checks it",
			"run_id", run.RunID, "main_sha", tip, "base_sha", pre)
		return failedMerge{}
	}
	tb := o.takeBackToBase(ctx, git.MergeResult{SHA: tip, BaseSHA: pre}, "merge binding")
	if !tb.clean {
		log.Error("merge binding: the merge failed after git moved the local main, and it could not be taken back; the run stays committing",
			"run_id", run.RunID, "merge_sha", tip, "base_sha", pre, "merge_error", mergeErr)
		return failedMerge{stuck: tip}
	}
	log.Warn("merge binding: the merge failed after git moved the local main; the merge was taken back unverified",
		"run_id", run.RunID, "merge_sha", tip, "restored_to", pre, "merge_error", mergeErr)
	return failedMerge{clean: true}
}

// clearMergeInFlight settles the run's record after an attempt left main
// clean. A failure is a Warn: the next attempt finds main at the recorded
// base (or past it without this merge) and clears it then.
func (o *Orchestrator) clearMergeInFlight(ctx context.Context, run *domain.Run) {
	if err := o.mergeRunCommitMeta(ctx, run.RunID, map[string]string{CommitMetaMergeUnverified: ""}); err != nil {
		observe.Logger(ctx).Warn("merge binding: failed to clear the merge-in-flight record; the next attempt settles it",
			"run_id", run.RunID, "error", err)
		return
	}
	if run.CommitMeta != nil {
		run.CommitMeta[CommitMetaMergeUnverified] = ""
	}
}

// settleUnverifiedMerge runs at the start of every binding-on merge
// attempt of a run that still carries CommitMetaMergeUnverified — an
// earlier attempt ended without settling it (the process died in the
// block, or a take-back or the clear failed) — before the merge source is
// resolved against main. With base and source from the record:
//
//   - main is at base: nothing landed; the record is cleared.
//   - main is this run's merge of source onto base: it is taken back to
//     base (compare-and-swap) and the record cleared, and the attempt
//     merges again and checks the new merge. A take-back that fails keeps
//     the record (on the shared tree git.CLIClient also keeps the
//     take-back pending), blocks governed merges on it with the guard on,
//     and refuses this attempt as unavailable (the run stays committing).
//   - main moved on: if neither source nor base is in its history, or
//     main's first-parent chain reaches base without passing a merge of
//     source onto base, this run's merge is not there (source reached main
//     another way); the record is cleared. A merge of it buried under later
//     commits cannot be taken back, so the attempt is refused as
//     unavailable, the record kept and, with the guard on, governed merges
//     blocked on it, until an operator resets the local main so it no
//     longer contains that merge (cancel is not allowed from committing).
//     A walk that cannot decide (a read error, the walk limit) refuses the
//     same way.
//
// It returns nil when the attempt may proceed.
func (o *Orchestrator) settleUnverifiedMerge(ctx context.Context, run *domain.Run) error {
	v := run.CommitMeta[CommitMetaMergeUnverified]
	if v == "" || !o.verifiesMergeResult() {
		return nil
	}
	log := observe.Logger(ctx)
	refuse := func(msg string, cause error) error {
		log.Error("merge binding: merge refused; "+msg, "run_id", run.RunID, "error", cause)
		return domain.NewErrorWithCause(domain.ErrUnavailable, "merge binding: "+msg, errors.Join(errUnverifiedMergeStuck, cause))
	}
	base, source, ok := parseUnverifiedMerge(v)
	if !ok {
		return refuse(fmt.Sprintf("the run's merge-in-flight record is malformed (%q)", truncateForLog(v)), nil)
	}
	var (
		clean   bool
		stuck   string // the unverified merge left on main
		readErr error
		reason  string
	)
	blockErr := o.withTree(ctx, func(ctx context.Context) error {
		tip, err := o.binder.ResolveBranchHead(ctx, authoritativeBranch)
		if err == nil && !isBoundObjectID(tip) {
			err = fmt.Errorf("operator returned a malformed head %q", truncateForLog(tip))
		}
		if err != nil {
			readErr, reason = err, "the local "+authoritativeBranch+" could not be read to settle an unverified merge of the run"
			return nil
		}
		if tip == base {
			clean = true
			return nil
		}
		if o.isMergeOf(ctx, tip, base, source) {
			tb := o.takeBackToBase(ctx, git.MergeResult{SHA: tip, BaseSHA: base}, "merge binding")
			if !tb.clean {
				stuck, reason = tip, "an unverified merge of the run ("+tip+") is on the local "+authoritativeBranch+
					" and could not be taken back to "+base
				return nil
			}
			log.Warn("merge binding: a merge of the run that reached the local main without its checks was taken back; merging again",
				"run_id", run.RunID, "merge_sha", tip, "restored_to", base)
			clean = true
			return nil
		}
		m, found, err := o.findUnverifiedMerge(ctx, tip, base, source)
		switch {
		case err != nil:
			readErr, reason = err, "could not tell whether an unverified merge of the run is in the history of the local "+authoritativeBranch
		case found:
			stuck, reason = m, "an unverified merge of the run ("+m+") is in the history of the local "+authoritativeBranch+
				" (something was committed on top of it); reset the local "+authoritativeBranch+
				" so it no longer contains it"
		default:
			clean = true
		}
		return nil
	})
	if isTreeWait(blockErr) {
		return blockErr // nothing ran (§2.7); the run stays committing
	}
	if blockErr != nil {
		return refuse("the shared working tree could not be taken to settle an unverified merge of the run", blockErr)
	}
	switch {
	case stuck != "":
		o.blockGovernedMergesOn(ctx, run, stuck, base)
		return refuse(reason, nil)
	case readErr != nil:
		return refuse(reason, readErr)
	case clean:
		o.clearMergeInFlight(ctx, run)
	}
	return nil
}

// findUnverifiedMerge decides whether main's history (tip) contains a merge
// of source onto base, when tip itself is not one. found=false with no
// error means it does not: source is not in main's history, base is not
// (a merge onto base would bring it), or main's first-parent chain reaches
// base without passing one (each later write builds on main, so the merge
// would be on that chain). The walk is bounded by priorMergeWalkLimit;
// past it, at a root commit before base, or on a read error, it returns an
// error (undecided).
func (o *Orchestrator) findUnverifiedMerge(ctx context.Context, tip, base, source string) (string, bool, error) {
	for _, anc := range []string{source, base} {
		in, err := o.binder.IsAncestor(ctx, anc, tip)
		if err != nil {
			return "", false, err
		}
		if !in {
			return "", false, nil
		}
	}
	if in, err := o.binder.IsAncestor(ctx, source, base); err != nil {
		return "", false, err
	} else if in {
		return "", false, nil // source was already on main: no merge of it could move main
	}
	x := tip
	for i := 0; i < priorMergeWalkLimit; i++ {
		if x == base {
			return "", false, nil
		}
		if x == source {
			return x, true, nil // a fast-forward to source, built on since
		}
		parents, err := o.binder.CommitParents(ctx, x)
		if err != nil {
			return "", false, err
		}
		if len(parents) == 0 {
			break
		}
		if len(parents) == 2 && parents[0] == base && parents[1] == source {
			return x, true, nil
		}
		x = parents[0]
	}
	return "", false, fmt.Errorf("main's first-parent chain does not reach %s within %d commits", base, priorMergeWalkLimit)
}
