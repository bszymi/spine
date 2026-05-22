package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ExecutionEvidenceSchemaVersion identifies the on-disk evidence schema.
// Stored in every record so consumers (validation rules, dashboards,
// audit tooling) can reject or migrate older shapes when the schema
// evolves. The version is bumped only when a non-additive change is
// made — adding an optional field is not a version bump.
const ExecutionEvidenceSchemaVersion = "1"

// CheckStatus is the lifecycle status of a single required check
// invocation. EPIC-006 records check execution as a per-check row so
// dashboards can show which check is in flight, which passed, and which
// failed without scraping logs.
type CheckStatus string

const (
	// CheckStatusPending — check has been declared (in RequiredChecks)
	// but no producer has reported a result yet. Default state.
	CheckStatusPending CheckStatus = "pending"

	// CheckStatusRunning — a producer has claimed the check and is
	// actively running it. Useful so the operator UI can distinguish
	// "queued" from "in flight".
	CheckStatusRunning CheckStatus = "running"

	// CheckStatusPassed — terminal success. The check produced a
	// deterministic pass result.
	CheckStatusPassed CheckStatus = "passed"

	// CheckStatusFailed — terminal authoritative failure. The artifact
	// under check did not satisfy the policy.
	CheckStatusFailed CheckStatus = "failed"

	// CheckStatusSkipped — terminal non-failure. The check was declared
	// but did not apply (e.g. no relevant paths changed). Distinct from
	// passed so dashboards can surface declared-but-not-run checks.
	CheckStatusSkipped CheckStatus = "skipped"

	// CheckStatusError — terminal infrastructure failure. The runner
	// could not produce a verdict (network error, missing tool). Distinct
	// from failed so retry logic and operator dashboards can react
	// differently — a real policy violation versus a flaky pipeline.
	CheckStatusError CheckStatus = "error"
)

// ValidCheckStatuses returns every recognized check status.
func ValidCheckStatuses() []CheckStatus {
	return []CheckStatus{
		CheckStatusPending,
		CheckStatusRunning,
		CheckStatusPassed,
		CheckStatusFailed,
		CheckStatusSkipped,
		CheckStatusError,
	}
}

// IsTerminal reports whether the status forbids further automatic
// retry. Pending and Running are non-terminal. Passed/Failed/Skipped/
// Error are terminal — Error is terminal at the check layer because
// only the producer (or an operator) can decide whether to retry.
func (s CheckStatus) IsTerminal() bool {
	switch s {
	case CheckStatusPassed,
		CheckStatusFailed,
		CheckStatusSkipped,
		CheckStatusError:
		return true
	}
	return false
}

// IsSuccess reports whether the status counts as a passed required
// check for the purposes of EvidenceStatus aggregation. Skipped is
// included because a declared-and-not-applicable check is a satisfied
// requirement, not a missing one.
func (s CheckStatus) IsSuccess() bool {
	return s == CheckStatusPassed || s == CheckStatusSkipped
}

// CheckProducerKind identifies whether a check result was produced by
// a human reviewer or by automation. Both shapes are first-class — the
// schema deliberately does not privilege automation, because a human
// signoff (e.g. "security review approved") is a legitimate piece of
// execution evidence (EPIC-006 AC requires both producer kinds).
type CheckProducerKind string

const (
	CheckProducerHuman     CheckProducerKind = "human"
	CheckProducerAutomated CheckProducerKind = "automated"
)

// ValidCheckProducerKinds returns every recognized producer kind.
func ValidCheckProducerKinds() []CheckProducerKind {
	return []CheckProducerKind{CheckProducerHuman, CheckProducerAutomated}
}

// EvidenceStatus is the aggregate status of an ExecutionEvidence
// record. Computed by DeriveStatus from the per-check rows so it can
// never disagree with the rows it summarizes.
type EvidenceStatus string

const (
	// EvidenceStatusPending — at least one required check is still
	// pending or running. Non-terminal.
	EvidenceStatusPending EvidenceStatus = "pending"

	// EvidenceStatusPassed — every required check has a successful
	// terminal result (passed or skipped). Terminal.
	EvidenceStatusPassed EvidenceStatus = "passed"

	// EvidenceStatusFailed — at least one required check terminated in
	// failed or error. Terminal until an operator acts (e.g. by
	// re-running the check or amending the policy).
	EvidenceStatusFailed EvidenceStatus = "failed"
)

// ValidEvidenceStatuses returns every recognized evidence status.
func ValidEvidenceStatuses() []EvidenceStatus {
	return []EvidenceStatus{
		EvidenceStatusPending,
		EvidenceStatusPassed,
		EvidenceStatusFailed,
	}
}

// IsTerminal reports whether the aggregate status is settled. Pending
// is the only non-terminal state.
func (s EvidenceStatus) IsTerminal() bool {
	return s == EvidenceStatusPassed || s == EvidenceStatusFailed
}

// ChangedPathsSummary is a deterministic, secret-free description of
// the file diff between BaseCommit and HeadCommit. The schema
// deliberately captures counts plus a capped path list — never the
// raw diff content — so evidence files stay small and never carry
// secrets that may have been added or removed inside changed files.
type ChangedPathsSummary struct {
	FilesChanged int      `json:"files_changed" yaml:"files_changed"`
	Insertions   int      `json:"insertions"    yaml:"insertions"`
	Deletions    int      `json:"deletions"     yaml:"deletions"`
	Paths        []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	// Truncated is true when the producer dropped paths to keep the
	// list under a size budget. Consumers must treat the file as having
	// MORE changes than Paths suggests.
	Truncated bool `json:"truncated,omitempty" yaml:"truncated,omitempty"`
}

// CheckResult is one row of structured evidence produced by a single
// required check. Both human and automated producers fill the same
// shape so downstream rule evaluation does not have to discriminate
// (EPIC-006 AC: schema supports both).
type CheckResult struct {
	// CheckID is the policy-defined identifier of the required check.
	// It MUST match an entry in the parent ExecutionEvidence's
	// RequiredChecks list — orphan results are a schema violation
	// because they cannot be tied back to a governed expectation.
	CheckID string `json:"check_id" yaml:"check_id"`

	// Name is a human-readable label. Optional; when empty the
	// CheckID is used in UI.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	Status CheckStatus `json:"status" yaml:"status"`

	// Producer carries an empty value while Status is pending and no
	// producer has claimed the check yet. omitempty on the tag keeps
	// the empty enum out of the marshaled output — strict consumers
	// that treat "" as an invalid CheckProducerKind would otherwise
	// reject pending evidence.
	Producer CheckProducerKind `json:"producer,omitempty" yaml:"producer,omitempty"`

	// ProducedBy is the actor identifier that produced this row. For a
	// human producer this is the actor ID; for an automated producer
	// this is the runner identity (e.g. "ci/github-actions"). Required
	// once the row leaves Pending — anonymous evidence is not
	// auditable.
	ProducedBy string `json:"produced_by,omitempty" yaml:"produced_by,omitempty"`

	// Summary is a single-line, human-readable description of the
	// result. The schema forbids embedded newlines (Validate enforces)
	// — raw logs and multi-line dumps do NOT belong in evidence; they
	// belong behind EvidenceURI.
	Summary string `json:"summary,omitempty" yaml:"summary,omitempty"`

	// EvidenceURI optionally references where the producer stored
	// detailed logs / artifacts (object storage, CI run URL). The URI
	// is a pointer, not the content — keeping logs out of the
	// committed evidence is required by AC "Evidence excludes secrets
	// and raw logs by default."
	EvidenceURI string `json:"evidence_uri,omitempty" yaml:"evidence_uri,omitempty"`

	StartedAt   *time.Time `json:"started_at,omitempty"   yaml:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
}

// ValidationPolicyRef ties a check (or the whole evidence record) to a
// governed validation policy. The ADR is the durable governance
// authority; the optional PolicyPath/PolicyID is a pointer to the
// concrete deterministic policy artifact defined by EPIC-006 TASK-002.
//
// This iteration intentionally keeps the ref thin — the policy
// artifact's own schema is owned by TASK-002 and TASK-007. What this
// schema commits to is the relationship: every required check can be
// traced back to an ADR, satisfying EPIC-006 AC #2.
type ValidationPolicyRef struct {
	ADRPath    string `json:"adr_path"            yaml:"adr_path"`
	PolicyPath string `json:"policy_path,omitempty" yaml:"policy_path,omitempty"`
	PolicyID   string `json:"policy_id,omitempty"   yaml:"policy_id,omitempty"`
}

// ExecutionEvidence is the structured record proving what happened in
// one (RunID, RepositoryID) tuple. EPIC-006 stores one evidence file
// per affected repository so missing/failed evidence on any one repo
// can block publication of the whole run (AC #4) without flattening
// the per-repo detail away.
type ExecutionEvidence struct {
	// SchemaVersion is the on-disk schema version. Always set; readers
	// should reject unknown versions rather than guess.
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`

	RunID        string `json:"run_id"        yaml:"run_id"`
	TaskPath     string `json:"task_path"     yaml:"task_path"`
	RepositoryID string `json:"repository_id" yaml:"repository_id"`
	BranchName   string `json:"branch_name"   yaml:"branch_name"`

	// BaseCommit and HeadCommit anchor the evidence to a specific
	// commit range in the repo. AC #1 (evidence is tied to repository,
	// branch, and commit) is satisfied by these three required fields
	// taken together.
	BaseCommit string `json:"base_commit" yaml:"base_commit"`
	HeadCommit string `json:"head_commit" yaml:"head_commit"`

	ChangedPaths ChangedPathsSummary `json:"changed_paths" yaml:"changed_paths"`

	// RequiredChecks is the list of check IDs the governing policy /
	// task declared as **blocking**. A failed or missing required check
	// flips DeriveStatus to failed/pending, gating publication per
	// EPIC-006 AC #4. EPIC-006 AC #1 ("a task can require evidence for
	// each affected repository") is realized by populating this list
	// from the task's repo-scoped requirements; CheckResults fills in
	// as producers report.
	RequiredChecks []string `json:"required_checks,omitempty" yaml:"required_checks,omitempty"`

	// AdvisoryChecks is the list of check IDs the governing policy
	// declared as **non-blocking** (warning severity / advisory
	// interpretation, see architecture/validation-policy.md). Advisory
	// checks still produce CheckResult rows so dashboards and audit
	// surfaces can show their outcomes, but DeriveStatus does NOT
	// aggregate their failures into the evidence status — a failed
	// advisory check leaves the aggregate at passed. RequiredChecks and
	// AdvisoryChecks MUST NOT overlap; a check is either blocking or
	// advisory, not both.
	AdvisoryChecks []string `json:"advisory_checks,omitempty" yaml:"advisory_checks,omitempty"`

	CheckResults []CheckResult `json:"check_results,omitempty" yaml:"check_results,omitempty"`

	// ValidationPolicies records every ADR-linked policy that informed
	// the required-check set. Often one entry per ADR; nothing forbids
	// more than one policy contributing to a single evidence record.
	ValidationPolicies []ValidationPolicyRef `json:"validation_policies,omitempty" yaml:"validation_policies,omitempty"`

	// Actor is the principal that owns this evidence record (often the
	// run's primary actor). TraceID is the observability correlation
	// that joins this record to engine/event logs.
	Actor   string `json:"actor"    yaml:"actor"`
	TraceID string `json:"trace_id" yaml:"trace_id"`

	// Status is the aggregate computed by DeriveStatus. Stored
	// explicitly so consumers do not have to recompute on every read,
	// but Validate cross-checks it against the rows so the file cannot
	// silently disagree with itself.
	Status EvidenceStatus `json:"status" yaml:"status"`

	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
}

// IsPrimaryRepository reports whether this evidence record is for the
// workspace primary repository.
func (e ExecutionEvidence) IsPrimaryRepository() bool {
	return e.RepositoryID == PrimaryRepositoryID
}

// DeriveStatus computes the aggregate EvidenceStatus from the
// CheckResults plus RequiredChecks. The rule:
//   - If any required check has no terminal result yet (no row, or row
//     in pending/running) → pending.
//   - Else if any required check terminated in failed/error → failed.
//   - Else → passed (all required checks are passed or skipped).
//
// This is deliberately stricter than "any failed → failed" because a
// missing required check is also a blocker: AC #4 says missing OR
// failed required evidence blocks publication.
func (e ExecutionEvidence) DeriveStatus() EvidenceStatus {
	resultByID := make(map[string]CheckResult, len(e.CheckResults))
	for _, r := range e.CheckResults {
		resultByID[r.CheckID] = r
	}
	anyFailure := false
	for _, id := range e.RequiredChecks {
		r, ok := resultByID[id]
		if !ok || !r.Status.IsTerminal() {
			return EvidenceStatusPending
		}
		if !r.Status.IsSuccess() {
			anyFailure = true
		}
	}
	if anyFailure {
		return EvidenceStatusFailed
	}
	return EvidenceStatusPassed
}

// Canonicalize sorts every slice that has a natural deterministic key
// AND normalizes timestamps to UTC so two semantically-identical
// evidence records produce byte-identical JSON / YAML output. AC:
// "Evidence is serializable as deterministic YAML or JSON."
//
// Ordering rules:
//   - RequiredChecks: lexicographic
//   - AdvisoryChecks: lexicographic
//   - CheckResults: by CheckID
//   - ValidationPolicies: by (ADRPath, PolicyPath, PolicyID)
//   - ChangedPaths.Paths: lexicographic
//
// Timestamp rules:
//   - GeneratedAt → UTC
//   - Each CheckResult.StartedAt and CompletedAt (when non-nil) → UTC
//
// Timezone normalization is required because two producers on runners
// in different zones can emit the same instant rendered as different
// strings (`2026-04-30T10:00:00Z` vs `2026-04-30T11:00:00+01:00`),
// which would otherwise break deterministic byte equality.
func (e *ExecutionEvidence) Canonicalize() {
	sort.Strings(e.RequiredChecks)
	sort.Strings(e.AdvisoryChecks)
	sort.Strings(e.ChangedPaths.Paths)
	sort.Slice(e.CheckResults, func(i, j int) bool {
		return e.CheckResults[i].CheckID < e.CheckResults[j].CheckID
	})
	sort.Slice(e.ValidationPolicies, func(i, j int) bool {
		a, b := e.ValidationPolicies[i], e.ValidationPolicies[j]
		if a.ADRPath != b.ADRPath {
			return a.ADRPath < b.ADRPath
		}
		if a.PolicyPath != b.PolicyPath {
			return a.PolicyPath < b.PolicyPath
		}
		return a.PolicyID < b.PolicyID
	})
	e.GeneratedAt = e.GeneratedAt.UTC()
	for i := range e.CheckResults {
		if e.CheckResults[i].StartedAt != nil {
			t := e.CheckResults[i].StartedAt.UTC()
			e.CheckResults[i].StartedAt = &t
		}
		if e.CheckResults[i].CompletedAt != nil {
			t := e.CheckResults[i].CompletedAt.UTC()
			e.CheckResults[i].CompletedAt = &t
		}
	}
}

// Validate enforces the schema invariants that storage and downstream
// rule evaluation rely on. Validation is intentionally strict: a
// malformed evidence file should be rejected at write time so audit
// readers can trust every committed record.
func (e ExecutionEvidence) Validate() error {
	if e.SchemaVersion == "" {
		return NewError(ErrInvalidParams, "evidence: schema_version is required")
	}
	if e.SchemaVersion != ExecutionEvidenceSchemaVersion {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("evidence: unsupported schema_version %q (this build supports %q)",
				e.SchemaVersion, ExecutionEvidenceSchemaVersion))
	}
	if e.RunID == "" {
		return NewError(ErrInvalidParams, "evidence: run_id is required")
	}
	if e.TaskPath == "" {
		return NewError(ErrInvalidParams, "evidence: task_path is required")
	}
	if e.RepositoryID == "" {
		return NewError(ErrInvalidParams, "evidence: repository_id is required")
	}
	if e.BranchName == "" {
		return NewError(ErrInvalidParams, "evidence: branch_name is required")
	}
	if e.BaseCommit == "" {
		return NewError(ErrInvalidParams, "evidence: base_commit is required")
	}
	if e.HeadCommit == "" {
		return NewError(ErrInvalidParams, "evidence: head_commit is required")
	}
	if e.Actor == "" {
		return NewError(ErrInvalidParams, "evidence: actor is required")
	}
	if e.TraceID == "" {
		return NewError(ErrInvalidParams, "evidence: trace_id is required")
	}
	if !isValidEvidenceStatus(e.Status) {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("evidence: invalid status %q", e.Status))
	}
	if e.GeneratedAt.IsZero() {
		return NewError(ErrInvalidParams, "evidence: generated_at is required")
	}

	// Single-line fields must not contain newlines. Lesson from
	// EPIC-005 TASK-006: free-form operator input committed without
	// newline checks creates trailer-injection / log-bleed surfaces.
	for field, value := range map[string]string{
		"run_id":        e.RunID,
		"task_path":     e.TaskPath,
		"repository_id": e.RepositoryID,
		"branch_name":   e.BranchName,
		"base_commit":   e.BaseCommit,
		"head_commit":   e.HeadCommit,
		"actor":         e.Actor,
		"trace_id":      e.TraceID,
	} {
		if strings.ContainsAny(value, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: %s must not contain newlines", field))
		}
	}

	if e.ChangedPaths.FilesChanged < 0 {
		return NewError(ErrInvalidParams, "evidence: changed_paths.files_changed must be non-negative")
	}
	if e.ChangedPaths.Insertions < 0 {
		return NewError(ErrInvalidParams, "evidence: changed_paths.insertions must be non-negative")
	}
	if e.ChangedPaths.Deletions < 0 {
		return NewError(ErrInvalidParams, "evidence: changed_paths.deletions must be non-negative")
	}
	for _, p := range e.ChangedPaths.Paths {
		if strings.ContainsAny(p, "\n\r") {
			return NewError(ErrInvalidParams, "evidence: changed_paths.paths entries must not contain newlines")
		}
	}

	requiredSet := make(map[string]struct{}, len(e.RequiredChecks))
	for _, id := range e.RequiredChecks {
		if id == "" {
			return NewError(ErrInvalidParams, "evidence: required_checks entries must not be empty")
		}
		if strings.ContainsAny(id, "\n\r") {
			return NewError(ErrInvalidParams,
				"evidence: required_checks entries must not contain newlines")
		}
		if _, dup := requiredSet[id]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: required_checks contains duplicate entry %q", id))
		}
		requiredSet[id] = struct{}{}
	}
	advisorySet := make(map[string]struct{}, len(e.AdvisoryChecks))
	for _, id := range e.AdvisoryChecks {
		if id == "" {
			return NewError(ErrInvalidParams, "evidence: advisory_checks entries must not be empty")
		}
		if strings.ContainsAny(id, "\n\r") {
			return NewError(ErrInvalidParams,
				"evidence: advisory_checks entries must not contain newlines")
		}
		if _, dup := advisorySet[id]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: advisory_checks contains duplicate entry %q", id))
		}
		// A check is either blocking (RequiredChecks) or advisory
		// (AdvisoryChecks); overlap is a schema violation because the
		// CheckResult row would have ambiguous gate semantics.
		if _, both := requiredSet[id]; both {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_id %q appears in both required_checks and advisory_checks", id))
		}
		advisorySet[id] = struct{}{}
	}

	seenResultIDs := make(map[string]struct{}, len(e.CheckResults))
	for i, r := range e.CheckResults {
		if r.CheckID == "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%d].check_id is required", i))
		}
		if strings.ContainsAny(r.CheckID, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%d].check_id must not contain newlines", i))
		}
		if _, dup := seenResultIDs[r.CheckID]; dup {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results contains duplicate check_id %q", r.CheckID))
		}
		seenResultIDs[r.CheckID] = struct{}{}
		// Orphan results — a result whose check_id is not in either
		// required_checks or advisory_checks — are rejected so
		// dashboards cannot show rows for checks the policy never asked
		// for. Either declaration list satisfies the contract.
		_, declaredRequired := requiredSet[r.CheckID]
		_, declaredAdvisory := advisorySet[r.CheckID]
		if !declaredRequired && !declaredAdvisory {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%q] is not in required_checks or advisory_checks", r.CheckID))
		}
		if !isValidCheckStatus(r.Status) {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%q] has invalid status %q", r.CheckID, r.Status))
		}
		// Producer enum is checked whenever it carries a value, even on
		// pending rows: a typo like "robot" or a stale runner field
		// would otherwise serialize through and be rejected by strict
		// downstream readers.
		if r.Producer != "" && !isValidCheckProducerKind(r.Producer) {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%q] has invalid producer %q", r.CheckID, r.Producer))
		}
		// A check that has left pending must declare its producer and
		// who produced it — otherwise the row is unauditable.
		if r.Status != CheckStatusPending {
			if r.Producer == "" {
				return NewError(ErrInvalidParams,
					fmt.Sprintf("evidence: check_results[%q] requires producer once status leaves pending", r.CheckID))
			}
			if r.ProducedBy == "" {
				return NewError(ErrInvalidParams,
					fmt.Sprintf("evidence: check_results[%q] requires produced_by once status leaves pending", r.CheckID))
			}
		}
		if strings.ContainsAny(r.Name, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%q].name must not contain newlines", r.CheckID))
		}
		if strings.ContainsAny(r.Summary, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%q].summary must not contain newlines", r.CheckID))
		}
		if strings.IndexFunc(r.EvidenceURI, unicode.IsSpace) >= 0 {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%q].evidence_uri must not contain whitespace", r.CheckID))
		}
		if strings.ContainsAny(r.ProducedBy, "\n\r") {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: check_results[%q].produced_by must not contain newlines", r.CheckID))
		}
	}

	for i, p := range e.ValidationPolicies {
		if p.ADRPath == "" {
			return NewError(ErrInvalidParams,
				fmt.Sprintf("evidence: validation_policies[%d].adr_path is required", i))
		}
		for field, value := range map[string]string{
			"adr_path":    p.ADRPath,
			"policy_path": p.PolicyPath,
			"policy_id":   p.PolicyID,
		} {
			if strings.ContainsAny(value, "\n\r") {
				return NewError(ErrInvalidParams,
					fmt.Sprintf("evidence: validation_policies[%d].%s must not contain newlines", i, field))
			}
		}
	}

	if got, want := e.Status, e.DeriveStatus(); got != want {
		return NewError(ErrInvalidParams,
			fmt.Sprintf("evidence: stored status %q disagrees with derived status %q", got, want))
	}

	return nil
}

func isValidEvidenceStatus(s EvidenceStatus) bool {
	for _, v := range ValidEvidenceStatuses() {
		if s == v {
			return true
		}
	}
	return false
}

func isValidCheckStatus(s CheckStatus) bool {
	for _, v := range ValidCheckStatuses() {
		if s == v {
			return true
		}
	}
	return false
}

func isValidCheckProducerKind(k CheckProducerKind) bool {
	for _, v := range ValidCheckProducerKinds() {
		if k == v {
			return true
		}
	}
	return false
}
