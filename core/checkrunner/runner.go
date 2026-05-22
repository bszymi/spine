// Package checkrunner is the narrow boundary EPIC-006 TASK-003 commits
// to: a contract that takes a (repository, branch, commit, policy
// check, working tree) tuple and returns a structured Result classified
// as pass / fail / timeout / unavailable.
//
// The boundary deliberately does NOT
//   - clone repositories (TASK-005 / runner clone context owns that),
//   - persist evidence files (TASK-001 / TASK-005 owns that),
//   - aggregate per-run status (TASK-004 owns that).
//
// It DOES classify a single check execution into a shape that maps
// cleanly into the execution evidence schema via Normalize. Splitting
// the boundary this narrowly is what lets the same package serve both
// today's local-command runner and tomorrow's external-CI integrations
// without changing the contract — TASK-003 AC: "the interface does not
// assume a specific CI provider".
package checkrunner

import (
	"context"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// Outcome classifies what happened when a single check ran. The
// boundary distinguishes Timeout and Unavailable from Fail because the
// downstream evidence schema collapses Timeout/Unavailable into
// CheckStatusError but a real verdict (deterministic non-zero exit) is
// CheckStatusFailed — operators react differently to a flaky pipeline
// versus a real policy violation.
//
// This enum lives at the runner boundary, not in the evidence schema,
// because evidence is the durable audit shape: it cares only about
// "passed / failed / error", not about the runner-internal reason. The
// translation happens in Normalize.
type Outcome string

const (
	// OutcomePass — the check ran to completion with a success verdict
	// (exit 0 for command checks).
	OutcomePass Outcome = "pass"

	// OutcomeFail — the check ran to completion with a deterministic
	// failure verdict (non-zero exit for command checks).
	OutcomeFail Outcome = "fail"

	// OutcomeTimeout — the check did not complete within the policy's
	// declared timeout. Distinct from Fail because the producer cannot
	// claim the artifact is non-conformant; it can only claim the
	// verdict is unknown.
	OutcomeTimeout Outcome = "timeout"

	// OutcomeUnavailable — the runner could not produce a verdict for a
	// reason that is not the artifact's fault: missing tool / missing
	// working tree / unsupported check kind / configuration error.
	// Treated as terminal at the runner layer — only the operator can
	// decide whether to retry.
	OutcomeUnavailable Outcome = "unavailable"
)

// IsTerminal reports whether the outcome is settled. All four outcomes
// are terminal at the runner boundary; the helper exists for symmetry
// with domain.CheckStatus.IsTerminal so callers can write status-style
// guards without special-casing the runner enum.
func (o Outcome) IsTerminal() bool {
	switch o {
	case OutcomePass, OutcomeFail, OutcomeTimeout, OutcomeUnavailable:
		return true
	}
	return false
}

// Request is the input boundary. It carries everything a Runner needs
// to execute or dispatch a single check.
//
// The fields mirror what the engine resolves at run start (per
// ADR-015): one (repository, branch, commit) tuple, exactly one check
// declaration, one resolved working tree path. WorkingDir is the path
// to the *already cloned* repository branch — the runner does not
// clone, that boundary is owned by TASK-005's clone context.
type Request struct {
	// RepositoryID is the workspace-scoped repository ID per ADR-013
	// (e.g. "spine" for the primary repo, or a code repo's catalog ID).
	RepositoryID string

	// BranchName is the run branch the check is being evaluated against.
	BranchName string

	// CommitSHA is the head commit of BranchName at the time of
	// invocation. Recorded in the resulting evidence row so a verdict
	// is always tied to a specific commit (EPIC-006 AC #1).
	CommitSHA string

	// WorkingDir is the path on the local filesystem to the cloned
	// repository working tree, already on BranchName. The runner
	// executes commands here. Required for kind=command checks; ignored
	// for kind=external (those are produced by the external system).
	WorkingDir string

	// Check is the policy check declaration to execute. Carries the
	// kind/command/timeout/severity that the runner enforces. The check
	// is the contract; the runner is its evaluator.
	Check domain.PolicyCheck
}

// Result is the structured outcome of one Runner invocation. Carries
// timing, the runner's classification, and an opaque LogReference that
// points to wherever the runner's log sink stored stdout+stderr.
//
// Logs are not embedded inline. The TASK-003 deliverable says
// explicitly: "Preserve raw logs as references, not inline evidence."
// Inlining stdout in evidence would re-introduce the secret-leak
// surface the changed_paths_summary design was specifically built to
// avoid (see execution-evidence.md §4.2).
type Result struct {
	Outcome Outcome

	// StartedAt and CompletedAt bracket the actual command execution
	// (or the dispatch-and-wait window for external runners). Both UTC.
	StartedAt   time.Time
	CompletedAt time.Time

	// ExitCode is the process exit status for command checks. Only
	// meaningful when Outcome == OutcomePass or OutcomeFail. Zero is
	// stored as zero (not omitted) so dashboards can distinguish "ran
	// and returned 0" from "did not run".
	ExitCode int

	// Reason is a single-line, human-readable classification tag (e.g.
	// "exit 1", "context deadline exceeded", "binary not found",
	// "kind=external not supported by this runner"). MUST NOT contain
	// newlines — it lands in CheckResult.Summary, which Validate forbids
	// from carrying \n.
	Reason string

	// LogReference is an opaque pointer to where the runner's log sink
	// stored captured stdout+stderr. Format is sink-defined; common
	// shapes are file paths, content-addressed digests, or
	// object-storage URLs. Empty when no logs were captured (e.g.
	// OutcomeUnavailable due to missing working tree before any process
	// started). The caller decides whether to surface this as
	// CheckResult.EvidenceURI or keep it private.
	LogReference string
}

// Runner is the integration boundary type. Implementations route a
// Request to whatever execution mechanism their kind supports. The
// interface returns (Result, error): the error channel is reserved for
// runner-internal failures the operator should debug (e.g. log sink I/O
// failure); a check that ran and produced a verdict — including a
// deterministic failure or a timeout — is a successful Run that
// returns a non-Pass Outcome with a nil error. This split keeps caller
// code simple: errors are exceptional, outcomes are routine.
type Runner interface {
	Run(ctx context.Context, req Request) (Result, error)
}
