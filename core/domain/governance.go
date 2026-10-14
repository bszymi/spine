package domain

// Reserved runs.commit_meta keys of the governance-path guard (ADR-016;
// INIT-023 TASK-006). They sit in the engine's reserved "spine." namespace
// (core/engine commitmeta.go, INIT-023 TASK-005): workflow outcomes can
// never write them (every outcome commit map is stripped of the prefix),
// and only the engine's decision and merge paths do. They are defined
// here so adapters/store, which reads them for the projection's
// provenance query, shares the names with the engine.
const (
	// CommitMetaGovernanceApprovalPrefix is the namespace of the keys below.
	CommitMetaGovernanceApprovalPrefix = "spine.governance_approval."
	// CommitMetaGovernanceApprovalSHA is the bound commit an operator
	// approved. "" is a revoked approval (a reject tombstone: a JSONB merge
	// cannot delete a key).
	CommitMetaGovernanceApprovalSHA = CommitMetaGovernanceApprovalPrefix + "sha"
	// CommitMetaGovernanceApprovalActorID and ...ActorRole name the
	// approver at approval time.
	CommitMetaGovernanceApprovalActorID   = CommitMetaGovernanceApprovalPrefix + "actor_id"
	CommitMetaGovernanceApprovalActorRole = CommitMetaGovernanceApprovalPrefix + "actor_role"
	// CommitMetaGovernanceApprovalApprovedAt is the approval time, RFC 3339
	// in UTC (second precision, so the text sorts by time).
	CommitMetaGovernanceApprovalApprovedAt = CommitMetaGovernanceApprovalPrefix + "approved_at"
	// CommitMetaGovernanceApprovalMergeSHA and ...BaseSHA are the
	// consumption marker: the merge commit an approved merge produced and
	// the target tip it was computed on. They are written before the push,
	// only by a merge that consumed an approval, and are the provenance the
	// projection's protection-removal guard accepts.
	CommitMetaGovernanceApprovalMergeSHA = CommitMetaGovernanceApprovalPrefix + "merge_sha"
	CommitMetaGovernanceApprovalBaseSHA  = CommitMetaGovernanceApprovalPrefix + "base_sha"
)
