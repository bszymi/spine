// Package branchprotect evaluates whether a branch mutation is allowed
// against the ruleset in /.spine/branch-protection.yaml.
//
// The package is consumed at two enforcement points (ADR-009 §3): the
// Git push path (`internal/githttp`) and the Spine write path (artifact
// service + orchestrator). Both call Policy.Evaluate with a Request
// describing the operation, and honour the returned Decision.
//
// Evaluation logic is self-contained. Rules are supplied by a RuleSource
// (see source.go) so this package has no runtime-DB dependency; the
// projection layer (EPIC-002 TASK-003) provides a RuleSource that reads
// the projected ruleset table, and unit tests can supply any []config.Rule
// directly.
package branchprotect

import (
	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
)

// OperationKind identifies the shape of the operation under evaluation.
// Enforcement points classify every ref-touching operation into one of
// these before calling Policy.Evaluate.
type OperationKind string

const (
	// OpDelete is a ref deletion. The policy consults no-delete rules.
	OpDelete OperationKind = "delete"
	// OpDirectWrite is any advance of a ref that is not a Spine-governed
	// merge — a Git push, a direct artifact.create, a workflow.create
	// without write_context.run_id. The policy consults no-direct-write
	// rules.
	OpDirectWrite OperationKind = "direct_write"
	// OpGovernedMerge is a merge advancing a ref, produced by the
	// Artifact Service's merge path inside a Run whose workflow reached
	// an authorising outcome. Governed merges are always allowed —
	// they are the intended write path (ADR-009 §2).
	OpGovernedMerge OperationKind = "governed_merge"
)

// Decision is the terminal outcome of Policy.Evaluate.
type Decision string

const (
	// DecisionAllow permits the operation.
	DecisionAllow Decision = "allow"
	// DecisionDeny blocks it. The accompanying []Reason explains why.
	DecisionDeny Decision = "deny"
)

// ReasonCode is a machine-readable classifier for a Reason. Call sites
// pattern-match on this to render distinct error messages (for example,
// "the rule denies" vs "you do not have override authority" should look
// different to a user).
type ReasonCode string

const (
	// ReasonNoMatchingRule is emitted with DecisionAllow when no rule in
	// the source targets the branch under evaluation. Surfaced so
	// auditors can see that a write was allowed because the branch was
	// unprotected, not because of an override.
	ReasonNoMatchingRule ReasonCode = "no_matching_rule"
	// ReasonGovernedMerge is emitted with DecisionAllow when the
	// operation kind is OpGovernedMerge — governed merges always pass.
	ReasonGovernedMerge ReasonCode = "governed_merge"
	// ReasonOverrideHonoured is emitted with DecisionAllow when the
	// operation would have been denied but an operator-authorized
	// override let it through. Drives the mandatory audit event on the
	// override path (ADR-009 §4).
	ReasonOverrideHonoured ReasonCode = "override_honoured"

	// ReasonRuleDenies is emitted with DecisionDeny when a matching rule
	// blocks the operation and no override was requested.
	ReasonRuleDenies ReasonCode = "rule_denies"
	// ReasonOverrideNotAuthorised is emitted with DecisionDeny when the
	// caller set Override=true but their role is below operator. The
	// distinct code lets a UI say "you cannot override" separately from
	// "this cannot be overridden."
	ReasonOverrideNotAuthorised ReasonCode = "override_not_authorised"
)

// Reason accompanies a Decision. Code is stable and machine-readable;
// Message is the human-facing string. RuleKind is populated on denial
// reasons so the UI can render "no-delete" separately from
// "no-direct-write"; it is empty on allow reasons.
type Reason struct {
	Code     ReasonCode      `json:"code"`
	Message  string          `json:"message"`
	RuleKind config.RuleKind `json:"rule_kind,omitempty"`
}

// Request describes the operation the caller wants to perform. The
// enforcement point populates every field it can; missing TraceID/RunID
// is allowed but reduces downstream audit usefulness.
type Request struct {
	// Branch is the destination ref. Short form ("main") is preferred;
	// the Git full-ref form ("refs/heads/main") is also accepted and
	// normalised before matching. Tag refs ("refs/tags/...") are not
	// normalised (they are out of scope for v1 — ADR-009 §6) and will
	// therefore not match any branch rule. Match is performed by
	// config.Config.MatchRules so both literal names and globs in the
	// source resolve correctly.
	Branch string
	// Kind classifies the operation. See OperationKind.
	Kind OperationKind
	// Actor identifies the caller. Only Role is consulted for override
	// authority, but ActorID flows through into audit events.
	Actor domain.Actor
	// Override signals the caller opts into a protection bypass. The
	// policy honours it only when Actor.Role is operator+.
	Override bool
	// RunID is the run that authorises OpGovernedMerge. Empty for
	// OpDelete / OpDirectWrite.
	RunID string
	// TraceID threads through to the audit event emitted on override so
	// reviewers can correlate a push to a specific request trace.
	TraceID string
}
