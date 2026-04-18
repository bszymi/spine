package branchprotect

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
)

// Policy evaluates branch-protection rules against a Request.
//
// Both enforcement points (Git push, Spine write path — ADR-009 §3)
// construct a Request and call Evaluate. A non-nil error signals an
// infrastructure failure (rule source unavailable) and must be treated
// as a Deny by the caller — fail closed, not open.
type Policy interface {
	Evaluate(ctx context.Context, req Request) (Decision, []Reason, error)
}

// New returns a Policy backed by the given RuleSource. If src returns
// (nil, nil) the policy applies BootstrapDefaults(); any non-nil error
// is propagated to the caller.
func New(src RuleSource) Policy {
	if src == nil {
		src = StaticRules(nil)
	}
	return &policy{src: src}
}

type policy struct {
	src RuleSource
}

// Evaluate implements Policy. The decision matrix (ADR-009 §2–§4):
//
//   - OpGovernedMerge → Allow, reason=governed_merge. Governed merges are
//     the authorised write path; rules do not gate them.
//   - OpDelete / OpDirectWrite against a branch with no matching rule →
//     Allow, reason=no_matching_rule.
//   - OpDelete / OpDirectWrite against a matching no-delete /
//     no-direct-write rule, Override=false → Deny, reason=rule_denies.
//   - Same as above, Override=true, Actor.Role < operator → Deny,
//     reason=override_not_authorised.
//   - Same as above, Override=true, Actor.Role ≥ operator → Allow,
//     reason=override_honoured.
//
// Multiple rules can match a single branch (e.g. "main" and "*"); the
// union of their kinds applies.
func (p *policy) Evaluate(ctx context.Context, req Request) (Decision, []Reason, error) {
	// Classify before anything else. An enforcement point that forgets
	// to set Kind — or sets a value we don't recognise — is a caller
	// bug. Fail closed: the policy is the central enforcement boundary
	// and must not default unknown operations to allow. Same shape as
	// the source-error path: Deny + non-nil error.
	switch req.Kind {
	case OpDelete, OpDirectWrite, OpGovernedMerge:
		// ok
	default:
		return DecisionDeny, nil, fmt.Errorf("branchprotect: unknown operation kind %q (caller must classify as %q, %q, or %q)", req.Kind, OpDelete, OpDirectWrite, OpGovernedMerge)
	}

	// OpGovernedMerge is unconditional — even if an operator happens to
	// mark Override on a governed merge, we do not emit an override
	// reason because there was nothing to override.
	if req.Kind == OpGovernedMerge {
		return DecisionAllow, []Reason{{
			Code:    ReasonGovernedMerge,
			Message: "governed merge — branch protection does not gate this operation",
		}}, nil
	}

	// Non-branch refs are out of scope. `refs/tags/*`, `refs/notes/*`,
	// `refs/meta/*`, etc. must never reach the matcher — a broad pattern
	// like `*/*/*` would otherwise drag tag pushes into a branch rule's
	// deny set. Only `refs/heads/...` survives (normalised to short
	// form below); short names without a `refs/` prefix are assumed to
	// be branches.
	if strings.HasPrefix(req.Branch, "refs/") && !strings.HasPrefix(req.Branch, "refs/heads/") {
		return DecisionAllow, []Reason{{
			Code:    ReasonNoMatchingRule,
			Message: fmt.Sprintf("ref %q is not a branch — out of scope for branch protection (ADR-009 §6)", req.Branch),
		}}, nil
	}

	rules, err := p.effectiveRules(ctx)
	if err != nil {
		return DecisionDeny, nil, fmt.Errorf("branchprotect: load rules: %w", err)
	}

	kinds := relevantKinds(rules, req)
	if len(kinds) == 0 {
		return DecisionAllow, []Reason{{
			Code:    ReasonNoMatchingRule,
			Message: fmt.Sprintf("no protection rule matches branch %q", req.Branch),
		}}, nil
	}

	// At least one rule denies the operation kind. Now resolve override.
	if !req.Override {
		return DecisionDeny, denyReasons(kinds, req, ReasonRuleDenies), nil
	}
	if !req.Actor.Role.HasAtLeast(domain.RoleOperator) {
		return DecisionDeny, denyReasons(kinds, req, ReasonOverrideNotAuthorised), nil
	}
	return DecisionAllow, allowOverrideReasons(kinds, req), nil
}

// effectiveRules returns the rules from the source, falling back to
// bootstrap defaults when the source returns an empty set. A source
// that returns (nil, nil) means the workspace has no config file yet;
// an explicit empty config (`rules: []`) returns ([]config.Rule{}, nil)
// and keeps protection off — the projection layer distinguishes the two.
func (p *policy) effectiveRules(ctx context.Context) ([]config.Rule, error) {
	rules, err := p.src.Rules(ctx)
	if err != nil {
		return nil, err
	}
	if rules == nil {
		return BootstrapDefaults(), nil
	}
	return rules, nil
}

// relevantKinds returns the set of protection kinds that apply to the
// request's operation. Ordering is stable: rules are processed in source
// order, kinds within a rule in declaration order, duplicates removed.
func relevantKinds(rules []config.Rule, req Request) []config.RuleKind {
	cfg := &config.Config{Rules: rules}
	matched := cfg.MatchRules(normalizeBranch(req.Branch))

	// req.Kind is already validated to be OpDelete or OpDirectWrite —
	// Evaluate handles OpGovernedMerge and rejects unknown kinds before
	// calling here.
	wanted := config.KindNoDirectWrite
	if req.Kind == OpDelete {
		wanted = config.KindNoDelete
	}

	seen := make(map[config.RuleKind]struct{})
	var out []config.RuleKind
	for _, r := range matched {
		for _, k := range r.Protections {
			if k != wanted {
				continue
			}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

func denyReasons(kinds []config.RuleKind, req Request, code ReasonCode) []Reason {
	reasons := make([]Reason, 0, len(kinds))
	for _, k := range kinds {
		reasons = append(reasons, Reason{
			Code:     code,
			Message:  denyMessage(code, k, req.Branch),
			RuleKind: k,
		})
	}
	return reasons
}

func allowOverrideReasons(kinds []config.RuleKind, req Request) []Reason {
	reasons := make([]Reason, 0, len(kinds))
	for _, k := range kinds {
		reasons = append(reasons, Reason{
			Code:     ReasonOverrideHonoured,
			Message:  fmt.Sprintf("operator override honoured for rule %q on branch %q", k, req.Branch),
			RuleKind: k,
		})
	}
	return reasons
}

// normalizeBranch accepts either a short branch name ("main") or a Git
// full-ref form ("refs/heads/main") and returns the short name that
// config patterns match against. Enforcement points are documented to
// pass the short form (see Request.Branch), but the Git pre-receive
// path receives refs in full form from the wire protocol — stripping
// the prefix here is a safety net so a forgotten strip in the caller
// does not become a silent protection bypass.
//
// Tags (`refs/tags/*`) are explicitly out of scope for v1
// (ADR-009 §6); they are returned unchanged so they can never match a
// branch rule by coincidence.
func normalizeBranch(branch string) string {
	return strings.TrimPrefix(branch, "refs/heads/")
}

func denyMessage(code ReasonCode, k config.RuleKind, branch string) string {
	if code == ReasonOverrideNotAuthorised {
		return fmt.Sprintf("override requested but actor lacks operator role (rule %q still applies on branch %q)", k, branch)
	}
	return fmt.Sprintf("rule %q blocks this operation on branch %q", k, branch)
}
