package store

// Governance-path guard storage (ADR-016; INIT-023 TASK-006). No schema
// change: the approval and its consumption marker live in reserved
// runtime.runs.commit_meta keys (JSONB), and override provenance is the
// existing branch_protection.override event in runtime.event_log. Neither
// method below joins the Store interface — the engine and the projection
// find them through narrow optional interfaces, so no embedder's Store
// implementation (fakes included) has to change.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/jackc/pgx/v5"
)

// MergeCommitMeta writes patch over a run's commit_meta, keeping every
// other key (`commit_meta || patch`; a NULL column starts empty). It is
// the engine's optional atomic partial write (core/engine
// commitMetaMerger): the governance decision and the merge's consumption
// marker change a few reserved keys and must not drop keys another writer
// added in between, which SetCommitMeta's whole-map replace would.
func (s *PostgresStore) MergeCommitMeta(ctx context.Context, runID string, patch map[string]string) error {
	if patch == nil {
		patch = map[string]string{}
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE runtime.runs SET commit_meta = COALESCE(commit_meta, '{}'::jsonb) || $1::jsonb WHERE run_id = $2`,
		patch, runID)
	if err != nil {
		return err
	}
	return mustAffect(tag, "run not found")
}

// MergeCommitMetaIfStatus is MergeCommitMeta applied only while the run
// is in status, in one statement; applied reports whether it was (false
// also for an unknown run). It is the engine's optional conditional write
// (core/engine commitMetaStatusMerger) for the governance approval
// (INIT-023 TASK-006 review round 1): an approval written after a
// concurrent reject had already routed the run out of committing would
// otherwise survive the loop-back and merge the same commit on resubmit.
// The row lock the UPDATE takes orders it against the reject's status
// transition (READ COMMITTED re-checks the WHERE on the newest row).
func (s *PostgresStore) MergeCommitMetaIfStatus(ctx context.Context, runID string, status domain.RunStatus, patch map[string]string) (bool, error) {
	if patch == nil {
		patch = map[string]string{}
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE runtime.runs SET commit_meta = COALESCE(commit_meta, '{}'::jsonb) || $1::jsonb WHERE run_id = $2 AND status = $3`,
		patch, runID, string(status))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Sources of a GovernanceAuthorization.
const (
	GovernanceSourceOverrideEvent    = "override_event"
	GovernanceSourceApprovedMerge    = "governance_approval"
	governanceOverrideEventType      = "branch_protection.override"
	governanceOverrideOperationWrite = "direct_write"
)

// GovernanceAuthorization is one database record of an operator act that
// may vouch for a branch-protection change the projection is about to
// install (the protection-removal guard, adapters/projection).
type GovernanceAuthorization struct {
	// Source is GovernanceSourceOverrideEvent or GovernanceSourceApprovedMerge.
	Source string
	// CommitSHA is, for an override, the pushed commit the operator's
	// override was honoured for; for an approval, the merge commit the
	// approved governed merge produced.
	CommitSHA string
	// BaseSHA is the commit the act started from: the push's old value,
	// or the target tip the approved merge was computed on. "" or an
	// all-zero id means the ref (and so the config) did not exist before.
	// The projection counts a record only if the config differs between
	// BaseSHA and CommitSHA — the act changed the config (owner question 8).
	BaseSHA    string
	ActorID    string
	RunID      string // approvals
	EventID    string // overrides
	RecordedAt time.Time
}

// ListGovernanceAuthorizations returns at most limit records per source,
// newest first, for pushes to branch (short name or refs/heads/<name>)
// and for approved governed merges:
//
//   - honoured branch_protection.override events of a direct write whose
//     payload names the branch, the pushed commit and the push's old
//     value (the git push path records pre_receive_ref; API-path override
//     events have no old value and cannot produce a parseable
//     .spine/branch-protection.yaml anyway, so they are not counted);
//   - runs whose reserved commit_meta carries an approval and its
//     consumption marker (merge_sha and base_sha, written by the merge
//     that consumed the approval before it pushed). A revoked approval
//     (the reject tombstone: an empty sha) is excluded. Whether the merge
//     commit reached the branch is the caller's reachability check, not
//     this query's.
//
// The event_log rows are the whole domain.Event JSON, as both Spine's
// delivery subscriber and SMP's emitter write them. event_log is pruned
// by retention; an override is only needed between the push and the
// first projection that installs it.
func (s *PostgresStore) ListGovernanceAuthorizations(ctx context.Context, branch string, limit int) ([]GovernanceAuthorization, error) {
	if limit <= 0 {
		limit = 200
	}
	var out []GovernanceAuthorization

	overrides, err := queryAll(ctx, s.pool, `
		SELECT event_id,
		       COALESCE(payload->>'actor_id', ''),
		       payload->'payload'->>'commit_sha',
		       payload->'payload'->'pre_receive_ref'->>'old_sha',
		       created_at
		FROM runtime.event_log
		WHERE event_type = $1
		  AND payload->'payload'->>'operation' = $2
		  AND payload->'payload'->>'branch' IN ($3, 'refs/heads/' || $3)
		  AND payload->'payload'->>'commit_sha' IS NOT NULL
		  AND payload->'payload'->'pre_receive_ref'->>'old_sha' IS NOT NULL
		ORDER BY created_at DESC
		LIMIT $4`,
		[]any{governanceOverrideEventType, governanceOverrideOperationWrite, branch, limit},
		func(row pgx.Rows, a *GovernanceAuthorization) error {
			return row.Scan(&a.EventID, &a.ActorID, &a.CommitSHA, &a.BaseSHA, &a.RecordedAt)
		})
	if err != nil {
		return nil, fmt.Errorf("list override authorizations: %w", err)
	}
	for i := range overrides {
		overrides[i].Source = GovernanceSourceOverrideEvent
	}
	out = append(out, overrides...)

	type approvalRow struct {
		GovernanceAuthorization
		approvedAt string
	}
	approvals, err := queryAll(ctx, s.pool, `
		SELECT run_id,
		       COALESCE(commit_meta->>$1, ''),
		       commit_meta->>$2,
		       commit_meta->>$3,
		       COALESCE(commit_meta->>$4, '')
		FROM runtime.runs
		WHERE COALESCE(commit_meta->>$2, '') <> ''
		  AND COALESCE(commit_meta->>$3, '') <> ''
		  AND COALESCE(commit_meta->>$5, '') <> ''
		ORDER BY COALESCE(commit_meta->>$4, '') DESC, run_id
		LIMIT $6`,
		[]any{
			domain.CommitMetaGovernanceApprovalActorID,
			domain.CommitMetaGovernanceApprovalMergeSHA,
			domain.CommitMetaGovernanceApprovalBaseSHA,
			domain.CommitMetaGovernanceApprovalApprovedAt,
			domain.CommitMetaGovernanceApprovalSHA,
			limit,
		},
		func(row pgx.Rows, a *approvalRow) error {
			return row.Scan(&a.RunID, &a.ActorID, &a.CommitSHA, &a.BaseSHA, &a.approvedAt)
		})
	if err != nil {
		return nil, fmt.Errorf("list approval authorizations: %w", err)
	}
	for i := range approvals {
		rec := approvals[i].GovernanceAuthorization
		rec.Source = GovernanceSourceApprovedMerge
		if t, err := time.Parse(time.RFC3339, approvals[i].approvedAt); err == nil {
			rec.RecordedAt = t
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out, nil
}
