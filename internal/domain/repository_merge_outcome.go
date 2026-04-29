package domain

import (
	"fmt"
	"time"
)

// RepositoryMergeStatus is the per-repository merge lifecycle status.
//
// EPIC-005 deliberately keeps merge outcomes per-repo rather than flattening
// a single status onto the run, because cross-repo merges are not atomic and
// an aggregate "run merged" status would hide partial states (one repo
// merged, another failed) that operators must see and act on.
type RepositoryMergeStatus string

const (
	// RepositoryMergeStatusPending — no merge attempted yet, or last attempt
	// is still in flight. Default state when an outcome row is first written
	// alongside the run.
	RepositoryMergeStatusPending RepositoryMergeStatus = "pending"

	// RepositoryMergeStatusMerged — branch was merged into its target. Terminal.
	RepositoryMergeStatusMerged RepositoryMergeStatus = "merged"

	// RepositoryMergeStatusFailed — merge attempt failed. Whether the next
	// attempt has any chance of succeeding is encoded by FailureClass.
	RepositoryMergeStatusFailed RepositoryMergeStatus = "failed"

	// RepositoryMergeStatusSkipped — repo was in AffectedRepositories but the
	// run produced nothing to merge there (e.g. no commits on the run branch
	// for this repo). Terminal.
	RepositoryMergeStatusSkipped RepositoryMergeStatus = "skipped"

	// RepositoryMergeStatusResolvedExternally — operator merged or rolled
	// forward outside Spine (manual git operation, hotfix on top of a failed
	// run). Terminal but distinct from Merged so dashboards can flag the
	// out-of-band path. Requires ResolvedBy + ResolutionReason.
	RepositoryMergeStatusResolvedExternally RepositoryMergeStatus = "resolved-externally"
)

// ValidRepositoryMergeStatuses returns every recognized status.
func ValidRepositoryMergeStatuses() []RepositoryMergeStatus {
	return []RepositoryMergeStatus{
		RepositoryMergeStatusPending,
		RepositoryMergeStatusMerged,
		RepositoryMergeStatusFailed,
		RepositoryMergeStatusSkipped,
		RepositoryMergeStatusResolvedExternally,
	}
}

// IsTerminal reports whether the status forbids further automatic retry.
// Pending and Failed (transient) are non-terminal — the scheduler may
// re-attempt them. Merged, Skipped, and ResolvedExternally are terminal.
// A Failed outcome with a permanent FailureClass is terminal at the
// outcome layer; that is encoded on RepositoryMergeOutcome.IsTerminal.
func (s RepositoryMergeStatus) IsTerminal() bool {
	switch s {
	case RepositoryMergeStatusMerged,
		RepositoryMergeStatusSkipped,
		RepositoryMergeStatusResolvedExternally:
		return true
	}
	return false
}

// MergeFailureClass categorizes why a merge attempt failed. Two rules
// drive the categorization:
//   - "transient" means a retry has a real chance of succeeding without
//     human action (network blip, transient lock).
//   - "permanent" means the next attempt will fail the same way until a
//     human resolves the underlying state (conflict, branch protection
//     denial, missing credentials).
//
// The distinction is required by the EPIC-005 deliverable so the
// scheduler knows whether to retry automatically (see TASK-006:
// manual-resolution-and-retry).
type MergeFailureClass string

const (
	// MergeFailureUnknown — class not yet classified. Treated as
	// permanent by IsTransient so the scheduler does not blindly retry
	// an unclassified failure.
	MergeFailureUnknown MergeFailureClass = "unknown"

	// MergeFailureConflict — git merge conflict requiring human
	// resolution. Permanent until the operator rebases or resolves.
	MergeFailureConflict MergeFailureClass = "merge_conflict"

	// MergeFailureBranchProtection — target branch protection rule
	// rejected the merge (no-direct-write, required reviews missing,
	// etc.). Permanent until governance is satisfied.
	MergeFailureBranchProtection MergeFailureClass = "branch_protection"

	// MergeFailurePrecondition — local precondition unmet (e.g. branch
	// missing, target moved). Permanent unless caller resyncs first.
	MergeFailurePrecondition MergeFailureClass = "precondition"

	// MergeFailureAuth — authentication/authorization to the remote
	// failed (expired token, revoked deploy key). Permanent until
	// credentials are rotated.
	MergeFailureAuth MergeFailureClass = "auth"

	// MergeFailureNetwork — transient network/transport error reaching
	// the remote. Worth retrying.
	MergeFailureNetwork MergeFailureClass = "network"

	// MergeFailureRemoteUnavailable — remote returned 5xx / refused
	// connection / replied "service unavailable". Worth retrying.
	MergeFailureRemoteUnavailable MergeFailureClass = "remote_unavailable"
)

// ValidMergeFailureClasses returns every recognized failure class.
func ValidMergeFailureClasses() []MergeFailureClass {
	return []MergeFailureClass{
		MergeFailureUnknown,
		MergeFailureConflict,
		MergeFailureBranchProtection,
		MergeFailurePrecondition,
		MergeFailureAuth,
		MergeFailureNetwork,
		MergeFailureRemoteUnavailable,
	}
}

// IsTransient reports whether a retry without human intervention has a
// real chance of succeeding. Unknown is treated as permanent so an
// unclassified failure does not silently spin up retry storms.
func (c MergeFailureClass) IsTransient() bool {
	switch c {
	case MergeFailureNetwork, MergeFailureRemoteUnavailable:
		return true
	}
	return false
}

// RepositoryMergeOutcome records the merge progress of one repository
// participating in a Run. EPIC-005 stores one row per (run_id,
// repository_id) so partial cross-repo merge states are explicit and
// queryable rather than inferred from prose.
type RepositoryMergeOutcome struct {
	// RunID + RepositoryID together form the identity. RepositoryID is
	// the catalog ID — PrimaryRepositoryID for the workspace primary,
	// or a code repo's id from /.spine/repositories.yaml.
	RunID        string `json:"run_id" yaml:"run_id"`
	RepositoryID string `json:"repository_id" yaml:"repository_id"`

	Status RepositoryMergeStatus `json:"status" yaml:"status"`

	// SourceBranch is the branch we attempted to merge (typically the
	// run branch in this repo). TargetBranch is what we merged into
	// (typically the repo's default branch or the configured
	// authoritative branch).
	SourceBranch string `json:"source_branch" yaml:"source_branch"`
	TargetBranch string `json:"target_branch" yaml:"target_branch"`

	// MergeCommitSHA is the resulting merge commit on TargetBranch.
	// Set only when Status == RepositoryMergeStatusMerged. The primary
	// repo also uses LedgerCommitSHA for the governance ledger commit
	// produced by the run; for code repos LedgerCommitSHA stays empty.
	MergeCommitSHA  string `json:"merge_commit_sha,omitempty" yaml:"merge_commit_sha,omitempty"`
	LedgerCommitSHA string `json:"ledger_commit_sha,omitempty" yaml:"ledger_commit_sha,omitempty"`

	// FailureClass + FailureDetail are set when Status ==
	// RepositoryMergeStatusFailed. FailureDetail is human-readable;
	// FailureClass is the machine-readable category that drives retry
	// decisions and dashboards.
	FailureClass  MergeFailureClass `json:"failure_class,omitempty" yaml:"failure_class,omitempty"`
	FailureDetail string            `json:"failure_detail,omitempty" yaml:"failure_detail,omitempty"`

	// ResolvedBy + ResolutionReason are the audit pair required for
	// RepositoryMergeStatusResolvedExternally and any retry that an
	// operator triggers manually (EPIC-005 TASK-006). ResolvedBy is
	// the actor ID; ResolutionReason is free-form.
	ResolvedBy       string `json:"resolved_by,omitempty" yaml:"resolved_by,omitempty"`
	ResolutionReason string `json:"resolution_reason,omitempty" yaml:"resolution_reason,omitempty"`

	// Attempts increments every time a merge is attempted on this
	// (run, repo) — successful or not. Used for retry budgets and
	// observability.
	Attempts int `json:"attempts" yaml:"attempts"`

	// Timestamps. CreatedAt is set on first write; UpdatedAt is bumped
	// on every store-side change. MergedAt is set when Status flips to
	// merged. LastAttemptedAt records the most recent attempt
	// regardless of outcome.
	CreatedAt       time.Time  `json:"created_at" yaml:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at" yaml:"updated_at"`
	MergedAt        *time.Time `json:"merged_at,omitempty" yaml:"merged_at,omitempty"`
	LastAttemptedAt *time.Time `json:"last_attempted_at,omitempty" yaml:"last_attempted_at,omitempty"`
}

// IsTerminal reports whether this outcome is in a state the scheduler
// must not auto-retry. A failed outcome with a permanent FailureClass
// is terminal here even though RepositoryMergeStatusFailed.IsTerminal
// returns false — the status alone cannot answer the question.
func (o RepositoryMergeOutcome) IsTerminal() bool {
	if o.Status.IsTerminal() {
		return true
	}
	if o.Status == RepositoryMergeStatusFailed && !o.FailureClass.IsTransient() {
		return true
	}
	return false
}

// IsPrimaryRepository reports whether this outcome is for the workspace
// primary repository — the only repository allowed to record a
// LedgerCommitSHA.
func (o RepositoryMergeOutcome) IsPrimaryRepository() bool {
	return o.RepositoryID == PrimaryRepositoryID
}

// LogFields returns a flat list of key/value pairs suitable for slog
// or any other structured logger. Defined here so the engine, scheduler,
// and HTTP handlers all emit the same field names — EPIC-005 AC
// "per-outcome metrics/log fields are defined so observability
// dashboards can break down success/failure by repo without inferring
// it from prose."
func (o RepositoryMergeOutcome) LogFields() []any {
	fields := []any{
		"run_id", o.RunID,
		"repository_id", o.RepositoryID,
		"merge_status", string(o.Status),
		"source_branch", o.SourceBranch,
		"target_branch", o.TargetBranch,
		"merge_attempts", o.Attempts,
	}
	if o.MergeCommitSHA != "" {
		fields = append(fields, "merge_commit_sha", o.MergeCommitSHA)
	}
	if o.LedgerCommitSHA != "" {
		fields = append(fields, "ledger_commit_sha", o.LedgerCommitSHA)
	}
	if o.FailureClass != "" {
		fields = append(fields,
			"failure_class", string(o.FailureClass),
			"failure_transient", o.FailureClass.IsTransient(),
		)
	}
	if o.ResolvedBy != "" {
		fields = append(fields, "resolved_by", o.ResolvedBy)
	}
	return fields
}

// Validate enforces the invariants the storage layer relies on so a
// malformed outcome is rejected before it hits Postgres. Validation is
// intentionally strict: the alternative is a Postgres CHECK violation
// surfacing as a generic error, which is harder to act on in practice.
func (o RepositoryMergeOutcome) Validate() error {
	if o.RunID == "" {
		return NewError(ErrInvalidParams, "merge outcome: run_id is required")
	}
	if o.RepositoryID == "" {
		return NewError(ErrInvalidParams, "merge outcome: repository_id is required")
	}
	if !isValidMergeStatus(o.Status) {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("merge outcome: invalid status %q", o.Status))
	}
	if o.SourceBranch == "" {
		return NewError(ErrInvalidParams, "merge outcome: source_branch is required")
	}
	if o.TargetBranch == "" {
		return NewError(ErrInvalidParams, "merge outcome: target_branch is required")
	}
	if o.Attempts < 0 {
		return NewError(ErrInvalidParams, "merge outcome: attempts must be non-negative")
	}
	if o.FailureClass != "" && !isValidFailureClass(o.FailureClass) {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("merge outcome: invalid failure_class %q", o.FailureClass))
	}

	// Status-conditional invariants. Failure fields belong only on
	// failed rows so dashboards and retry logic cannot mistake a
	// resolved/pending/skipped outcome for a fresh failure (codex
	// review caught this on the first pass).
	switch o.Status {
	case RepositoryMergeStatusMerged:
		if o.MergeCommitSHA == "" {
			return NewError(ErrInvalidParams,
				"merge outcome: merge_commit_sha required when status=merged")
		}
		if o.MergedAt == nil {
			return NewError(ErrInvalidParams,
				"merge outcome: merged_at required when status=merged")
		}
		if o.FailureClass != "" || o.FailureDetail != "" {
			return NewError(ErrInvalidParams,
				"merge outcome: failure fields must be empty when status=merged")
		}
	case RepositoryMergeStatusFailed:
		if o.FailureClass == "" {
			return NewError(ErrInvalidParams,
				"merge outcome: failure_class required when status=failed")
		}
		if o.MergeCommitSHA != "" {
			return NewError(ErrInvalidParams,
				"merge outcome: merge_commit_sha forbidden when status=failed")
		}
		if o.MergedAt != nil {
			return NewError(ErrInvalidParams,
				"merge outcome: merged_at forbidden when status=failed")
		}
	case RepositoryMergeStatusResolvedExternally:
		if o.ResolvedBy == "" || o.ResolutionReason == "" {
			return NewError(ErrInvalidParams,
				"merge outcome: resolved_by and resolution_reason required when status=resolved-externally")
		}
		if o.FailureClass != "" || o.FailureDetail != "" {
			return NewError(ErrInvalidParams,
				"merge outcome: failure fields must be empty when status=resolved-externally")
		}
		// resolved-externally explicitly means "not merged via Spine" —
		// the audit pair is what records the resolution. A
		// Spine-tracked merge commit / merged_at on this status would
		// make dashboards classify the row ambiguously.
		if o.MergeCommitSHA != "" {
			return NewError(ErrInvalidParams,
				"merge outcome: merge_commit_sha forbidden when status=resolved-externally")
		}
		if o.MergedAt != nil {
			return NewError(ErrInvalidParams,
				"merge outcome: merged_at forbidden when status=resolved-externally")
		}
	case RepositoryMergeStatusPending, RepositoryMergeStatusSkipped:
		if o.MergeCommitSHA != "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("merge outcome: merge_commit_sha forbidden when status=%s", o.Status))
		}
		if o.FailureClass != "" || o.FailureDetail != "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("merge outcome: failure fields must be empty when status=%s", o.Status))
		}
		if o.MergedAt != nil {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("merge outcome: merged_at forbidden when status=%s", o.Status))
		}
	}

	if o.LedgerCommitSHA != "" && !o.IsPrimaryRepository() {
		return NewError(ErrInvalidParams,
			"merge outcome: ledger_commit_sha is only valid on the primary repository")
	}

	return nil
}

func isValidMergeStatus(s RepositoryMergeStatus) bool {
	for _, v := range ValidRepositoryMergeStatuses() {
		if s == v {
			return true
		}
	}
	return false
}

func isValidFailureClass(c MergeFailureClass) bool {
	for _, v := range ValidMergeFailureClasses() {
		if c == v {
			return true
		}
	}
	return false
}
