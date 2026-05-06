package checkrunner

import (
	"github.com/bszymi/spine/internal/domain"
)

// Normalize translates a Runner Result into a domain.CheckResult row
// that slots directly into ExecutionEvidence.CheckResults. This is the
// boundary between the runner's classification (4 outcomes) and the
// evidence schema's status enum (6 statuses) — keeping the translation
// in one place is what guarantees TASK-003 AC #2: "Check results are
// normalized into the evidence schema."
//
// Mapping:
//
//	OutcomePass        → CheckStatusPassed
//	OutcomeFail        → CheckStatusFailed
//	OutcomeTimeout     → CheckStatusError (a real verdict was not produced)
//	OutcomeUnavailable → CheckStatusError (runner could not produce a verdict)
//
// Note that OutcomeTimeout and OutcomeUnavailable both collapse to
// CheckStatusError. The evidence schema deliberately conflates them at
// the audit layer — operators care about "the verdict is unknown",
// not whether the unknown was a deadline or a missing tool. Result.Reason
// preserves the runner-internal distinction in CheckResult.Summary so
// that surface is not lost.
//
// Skipped status is NOT produced by Normalize. A "skipped" verdict
// means "the policy declared this check but it didn't apply" — the
// caller (validation service / runner orchestrator) is the layer that
// decides applicability via the policy selector and constructs a
// CheckResult{Status: CheckStatusSkipped} directly. The runner only
// reports on checks it was actually invoked to run.
//
// producer is the actor identifier that owns the resulting row. For an
// automated runner that's typically a runner identity ("ci/spine/host").
// The function does NOT default it because every audit consumer of
// EPIC-006 wants a non-empty produced_by — silent fallbacks defeat
// the audit guarantee.
//
// res.LogReference is copied into CheckResult.EvidenceURI verbatim so
// audit readers can find the captured stdout/stderr from the evidence
// row alone — without this, callers using Normalize as the documented
// path into ExecutionEvidence would silently lose the log linkage
// (codex pass 6 P2). When the runner ran with no sink configured,
// LogReference is empty and EvidenceURI stays empty.  Callers that
// need to transform the reference (wrap a filesystem path in a
// `file://` URL, prefix with an object-store host) can overwrite
// row.EvidenceURI after the call returns.
func Normalize(req Request, res Result, producer string) domain.CheckResult {
	row := domain.CheckResult{
		CheckID:     req.Check.CheckID,
		Name:        req.Check.Name,
		Producer:    domain.CheckProducerAutomated,
		ProducedBy:  producer,
		Summary:     res.Reason,
		EvidenceURI: res.LogReference,
	}
	if !res.StartedAt.IsZero() {
		t := res.StartedAt.UTC()
		row.StartedAt = &t
	}
	if !res.CompletedAt.IsZero() {
		t := res.CompletedAt.UTC()
		row.CompletedAt = &t
	}
	switch res.Outcome {
	case OutcomePass:
		row.Status = domain.CheckStatusPassed
	case OutcomeFail:
		row.Status = domain.CheckStatusFailed
	case OutcomeTimeout, OutcomeUnavailable:
		row.Status = domain.CheckStatusError
	default:
		// An unknown outcome reaching this layer indicates a runner
		// implementation bug. Default to Error so the row still
		// passes evidence Validate (which would otherwise reject an
		// empty status), and the Summary preserves the original
		// reason for forensic analysis.
		row.Status = domain.CheckStatusError
	}
	return row
}
