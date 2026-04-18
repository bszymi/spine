package branchprotect

import (
	"context"

	"github.com/bszymi/spine/internal/branchprotect/config"
)

// RuleSource supplies the rules the policy evaluates against. It is a
// narrow interface on purpose: anything that can return an ordered list
// of rules for a workspace satisfies it. The projection layer
// (EPIC-002 TASK-003) will wrap the `branch_protection_rules` runtime
// table; tests use StaticRules.
//
// A RuleSource that returns (nil, nil) means "this workspace has no
// explicit ruleset" — the bootstrap defaults apply (ADR-009 §1). A
// non-nil error aborts evaluation and surfaces as Policy.Evaluate's
// error return; enforcement points must fail closed in that case
// rather than allowing the operation.
type RuleSource interface {
	Rules(ctx context.Context) ([]config.Rule, error)
}

// StaticRules is a RuleSource backed by a fixed []config.Rule. Useful
// for tests and for the in-process fallback when the projection is
// warming up.
type StaticRules []config.Rule

// Rules implements RuleSource.
func (s StaticRules) Rules(_ context.Context) ([]config.Rule, error) {
	if s == nil {
		return nil, nil
	}
	return []config.Rule(s), nil
}

// BootstrapDefaults returns the ruleset applied when a workspace has no
// /.spine/branch-protection.yaml yet (ADR-009 §1). `main` is protected
// with both no-delete and no-direct-write so a freshly-imported or
// partially-rolled-out repository still has authoritative-branch
// invariants.
//
// The seed file written by spine init-repo (EPIC-002 TASK-004) must match
// this return value exactly; the round-trip is pinned by a test in that
// task's package.
func BootstrapDefaults() []config.Rule {
	return []config.Rule{
		{
			Branch: "main",
			Protections: []config.RuleKind{
				config.KindNoDelete,
				config.KindNoDirectWrite,
			},
		},
	}
}
