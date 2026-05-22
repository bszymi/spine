package evidence

import (
	"context"

	"github.com/bszymi/spine/core/domain"
)

// PrimaryAuthoritativeBranchDefault is the spine-convention default
// authoritative branch name. It MUST stay in lockstep with
// `internal/engine/merge.go::authoritativeBranch`, the constant the
// engine merge step writes evidence to. Reader and writer cannot
// diverge: a misaligned reader would surface false missing-evidence
// for runs whose merge wrote to the actual ledger branch but whose
// query layer read a different one (codex-pass-11 finding, iter 22).
//
// When the engine path becomes per-workspace configurable (a future
// epic), the reader's branch resolution can grow alongside it via
// [Querier.WithPrimaryBranch]; until then, production wiring uses
// the default and only test callers vary it.
const PrimaryAuthoritativeBranchDefault = "main"

// Querier is the production evidence query implementation wired into
// the gateway via [github.com/bszymi/spine/service/internal/gateway.EvidenceQuerier].
// It reads from the workspace's primary-repo Git tree using the
// supplied [GitReader].
type Querier struct {
	reader        GitReader
	primaryBranch string
}

// NewQuerier constructs a [Querier] that reads via the supplied
// GitReader and treats `PrimaryAuthoritativeBranchDefault` ("main")
// as the workspace's authoritative ledger branch. Override with
// [Querier.WithPrimaryBranch] for workspaces whose primary repo's
// authoritative branch is something else (e.g., "trunk").
//
// IMPORTANT: do NOT pass "HEAD" as the authoritative branch (codex
// pass-6 finding, iter 22). HEAD is the working tree's CURRENT
// branch and any concurrent run or operator-side `git checkout` will
// flip it mid-query, surfacing stale or wrong-branch evidence. The
// engine merge step writes durable evidence to the named primary
// branch; the querier MUST read from that same named branch.
func NewQuerier(reader GitReader) *Querier {
	return &Querier{reader: reader, primaryBranch: PrimaryAuthoritativeBranchDefault}
}

// WithPrimaryBranch overrides the authoritative branch the querier
// treats as the durable ledger ref. Empty branch is ignored — the
// default ([PrimaryAuthoritativeBranchDefault]) remains in force.
// Returns the receiver so wiring sites can chain.
//
// PRODUCTION wiring MUST NOT call this today: the engine merge target
// is hardcoded to "main", and a reader override would silently break
// run.status for any operator whose configuration drifts from the
// merge target. This method exists for tests (to vary the branch
// fixture key) and for the future epic that makes per-workspace
// authoritative branches a first-class concept across both reader
// and writer paths.
func (q *Querier) WithPrimaryBranch(branch string) *Querier {
	if q == nil || branch == "" {
		return q
	}
	q.primaryBranch = branch
	return q
}

// SummarizeForRun returns the deterministic evidence summary for the
// given Run. It picks a priority-ordered set of refs via [RefsForRun]
// and the loader probes each per repository: the first ref that has
// evidence wins. This handles all four run-state shapes — active runs
// staging on the run branch, completed runs whose branch was cleaned
// up post-merge (HEAD is authoritative), failed runs whose branch is
// PRESERVED for debugging (the run branch may carry evidence that
// never made it to HEAD), and cancelled runs in either configuration.
//
// Planning runs are silently skipped: they exist to govern artifact
// CREATION on the primary repo (architecture/execution-evidence.md
// §2.2) and do not invoke the check runner, so no execution evidence
// is ever produced. The TASK-004 EV-* rules already skip them at the
// validation layer; the query layer mirrors that policy by returning
// (nil, nil), which makes the run.status handler omit the `evidence`
// field rather than reporting `missing` for a run state that has no
// evidence concept.
func (q *Querier) SummarizeForRun(ctx context.Context, run *domain.Run) (*RunSummary, error) {
	if q == nil || q.reader == nil {
		return nil, nil
	}
	if run != nil && run.Mode == domain.RunModePlanning {
		return nil, nil
	}
	return BuildSummary(ctx, q.reader, RefsForRun(run, q.primaryBranch), run)
}

// RefsForRun returns the priority-ordered list of Git refs the loader
// should try when reading evidence for a run. Refs are deduplicated
// in input order; the first ref that yields a found+valid evidence
// file wins per repository. `primaryBranch` is the workspace's
// authoritative ledger branch (typically "main"); empty defaults to
// [PrimaryAuthoritativeBranchDefault].
//
// Ordering rationale, by run state:
//
//   - Terminal (completed / failed / cancelled): primary branch first.
//     For completed runs, the merge step makes evidence durable on
//     the primary repo's authoritative branch and the run branch is
//     deleted as part of the same step. For failed/cancelled runs,
//     the engine's CleanupRunBranch path typically deletes the run
//     branch when no failed merge outcome is preserved (i.e., the
//     common step-failure / pre-merge-cancel paths), so the primary
//     branch is the only place evidence might land. The branch
//     fallback is a recovery for the rarer paths where someone
//     preserved the branch (e.g., a partially-merged-then-failed
//     run promoted to terminal with branch debug-preserved). When
//     evidence was only ever staged on a branch that has since been
//     deleted, the report correctly surfaces "missing" — durability
//     of evidence across cleanup is owned by the engine merge-step
//     write to the primary branch, not the query layer.
//
//   - Non-terminal (active / paused / committing / partially-merged):
//     run branch first. In-flight evidence stages on the run branch
//     (architecture/execution-evidence.md §6.2); the partially-merged
//     state in particular preserves the branch so the operator can
//     see what was attempted across repos. Primary-branch fallback
//     covers the edge case where evidence is already partially on
//     the primary branch (a resume from partial merge).
//
// An empty BranchName collapses to [primaryBranch] — pre-branch-creation
// lookups don't error. A nil run also collapses to [primaryBranch].
//
// Codex iter 22 trail: pass 1 surfaced the missing branch fallback
// for failed runs; pass 3 corrected the over-correction (the typical
// failed/cancelled path DELETES the branch, so branch-first wastes a
// Git read and over-promises the recovery); pass 6 corrected the
// "HEAD" use (mutable working-tree state) to a stable named branch.
// The current shape is the resolution: every state probes both refs,
// but the priority order matches the *typical* location of durable
// evidence per state, and the "ledger" ref is named, not symbolic.
func RefsForRun(run *domain.Run, primaryBranch string) []string {
	if primaryBranch == "" {
		primaryBranch = PrimaryAuthoritativeBranchDefault
	}
	if run == nil || run.BranchName == "" {
		return []string{primaryBranch}
	}
	if run.Status.IsTerminal() {
		return uniqueRefs(primaryBranch, run.BranchName)
	}
	return uniqueRefs(run.BranchName, primaryBranch)
}

func uniqueRefs(refs ...string) []string {
	out := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
