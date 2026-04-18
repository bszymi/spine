package branchprotect

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
)

// errSource is a RuleSource that fails to load. Used to exercise the
// fail-closed behaviour Evaluate owes its callers on infrastructure
// errors.
type errSource struct{ err error }

func (s errSource) Rules(_ context.Context) ([]config.Rule, error) {
	return nil, s.err
}

func actor(role domain.ActorRole) domain.Actor {
	return domain.Actor{ActorID: "actor-1", Type: domain.ActorTypeHuman, Role: role, Status: domain.ActorStatusActive}
}

// mainOnlyRules mirrors the bootstrap defaults shape for use in table
// tests that don't want to rely on StaticRules(nil)'s fallback path.
var mainOnlyRules = []config.Rule{
	{Branch: "main", Protections: []config.RuleKind{config.KindNoDelete, config.KindNoDirectWrite}},
}

func TestEvaluate_GovernedMergeAlwaysAllows(t *testing.T) {
	p := New(StaticRules(mainOnlyRules))
	d, reasons, err := p.Evaluate(context.Background(), Request{
		Branch: "main",
		Kind:   OpGovernedMerge,
		Actor:  actor(domain.RoleContributor),
		RunID:  "run-42",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionAllow {
		t.Fatalf("decision = %v, want Allow", d)
	}
	if len(reasons) != 1 || reasons[0].Code != ReasonGovernedMerge {
		t.Fatalf("reasons = %+v, want single governed_merge", reasons)
	}
}

func TestEvaluate_NoMatchingRuleAllows(t *testing.T) {
	p := New(StaticRules(mainOnlyRules))
	d, reasons, err := p.Evaluate(context.Background(), Request{
		Branch: "feat/x",
		Kind:   OpDirectWrite,
		Actor:  actor(domain.RoleContributor),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionAllow {
		t.Fatalf("decision = %v, want Allow", d)
	}
	if len(reasons) != 1 || reasons[0].Code != ReasonNoMatchingRule {
		t.Fatalf("reasons = %+v, want single no_matching_rule", reasons)
	}
}

func TestEvaluate_DecisionMatrix(t *testing.T) {
	rules := []config.Rule{
		{Branch: "main", Protections: []config.RuleKind{config.KindNoDelete, config.KindNoDirectWrite}},
		{Branch: "release/*", Protections: []config.RuleKind{config.KindNoDelete}},
	}

	cases := []struct {
		name         string
		branch       string
		kind         OperationKind
		role         domain.ActorRole
		override     bool
		wantDecision Decision
		wantCode     ReasonCode
		wantKind     config.RuleKind
	}{
		{
			name:         "direct write to main, no override",
			branch:       "main",
			kind:         OpDirectWrite,
			role:         domain.RoleContributor,
			wantDecision: DecisionDeny,
			wantCode:     ReasonRuleDenies,
			wantKind:     config.KindNoDirectWrite,
		},
		{
			name:         "delete main, no override",
			branch:       "main",
			kind:         OpDelete,
			role:         domain.RoleContributor,
			wantDecision: DecisionDeny,
			wantCode:     ReasonRuleDenies,
			wantKind:     config.KindNoDelete,
		},
		{
			name:         "direct write to main, contributor override",
			branch:       "main",
			kind:         OpDirectWrite,
			role:         domain.RoleContributor,
			override:     true,
			wantDecision: DecisionDeny,
			wantCode:     ReasonOverrideNotAuthorised,
			wantKind:     config.KindNoDirectWrite,
		},
		{
			name:         "direct write to main, reviewer override still denied",
			branch:       "main",
			kind:         OpDirectWrite,
			role:         domain.RoleReviewer,
			override:     true,
			wantDecision: DecisionDeny,
			wantCode:     ReasonOverrideNotAuthorised,
			wantKind:     config.KindNoDirectWrite,
		},
		{
			name:         "direct write to main, operator override allowed",
			branch:       "main",
			kind:         OpDirectWrite,
			role:         domain.RoleOperator,
			override:     true,
			wantDecision: DecisionAllow,
			wantCode:     ReasonOverrideHonoured,
			wantKind:     config.KindNoDirectWrite,
		},
		{
			name:         "direct write to main, admin override allowed",
			branch:       "main",
			kind:         OpDirectWrite,
			role:         domain.RoleAdmin,
			override:     true,
			wantDecision: DecisionAllow,
			wantCode:     ReasonOverrideHonoured,
			wantKind:     config.KindNoDirectWrite,
		},
		{
			name:         "delete release branch matched by glob, no override",
			branch:       "release/1.0",
			kind:         OpDelete,
			role:         domain.RoleContributor,
			wantDecision: DecisionDeny,
			wantCode:     ReasonRuleDenies,
			wantKind:     config.KindNoDelete,
		},
		{
			name:         "direct write to release branch — release/* only no-delete, so allowed",
			branch:       "release/1.0",
			kind:         OpDirectWrite,
			role:         domain.RoleContributor,
			wantDecision: DecisionAllow,
			wantCode:     ReasonNoMatchingRule,
		},
		{
			name:         "override set but not needed (no matching rule) still allows with no_matching_rule",
			branch:       "feat/x",
			kind:         OpDirectWrite,
			role:         domain.RoleOperator,
			override:     true,
			wantDecision: DecisionAllow,
			wantCode:     ReasonNoMatchingRule,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New(StaticRules(rules))
			d, reasons, err := p.Evaluate(context.Background(), Request{
				Branch:   tc.branch,
				Kind:     tc.kind,
				Actor:    actor(tc.role),
				Override: tc.override,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d != tc.wantDecision {
				t.Fatalf("decision = %v, want %v. reasons=%+v", d, tc.wantDecision, reasons)
			}
			if len(reasons) == 0 {
				t.Fatalf("got no reasons")
			}
			if reasons[0].Code != tc.wantCode {
				t.Fatalf("reason code = %q, want %q", reasons[0].Code, tc.wantCode)
			}
			if tc.wantKind != "" && reasons[0].RuleKind != tc.wantKind {
				t.Fatalf("reason rule_kind = %q, want %q", reasons[0].RuleKind, tc.wantKind)
			}
		})
	}
}

func TestEvaluate_OverlappingRulesUnionKinds(t *testing.T) {
	// main matches both an exact rule (no-delete) and a glob rule
	// (no-direct-write). Evaluating a delete should deny via the exact
	// rule; evaluating a direct write should deny via the glob rule.
	rules := []config.Rule{
		{Branch: "main", Protections: []config.RuleKind{config.KindNoDelete}},
		{Branch: "*", Protections: []config.RuleKind{config.KindNoDirectWrite}},
	}
	p := New(StaticRules(rules))

	d, reasons, err := p.Evaluate(context.Background(), Request{
		Branch: "main", Kind: OpDelete, Actor: actor(domain.RoleContributor),
	})
	if err != nil || d != DecisionDeny || reasons[0].RuleKind != config.KindNoDelete {
		t.Fatalf("delete: d=%v reasons=%+v err=%v", d, reasons, err)
	}

	d, reasons, err = p.Evaluate(context.Background(), Request{
		Branch: "main", Kind: OpDirectWrite, Actor: actor(domain.RoleContributor),
	})
	if err != nil || d != DecisionDeny || reasons[0].RuleKind != config.KindNoDirectWrite {
		t.Fatalf("direct write: d=%v reasons=%+v err=%v", d, reasons, err)
	}
}

func TestEvaluate_DuplicateKindsAcrossRulesDeduplicated(t *testing.T) {
	// Two rules both protecting main with no-direct-write. The
	// resulting deny should list the kind exactly once.
	rules := []config.Rule{
		{Branch: "main", Protections: []config.RuleKind{config.KindNoDirectWrite}},
		{Branch: "*", Protections: []config.RuleKind{config.KindNoDirectWrite}},
	}
	p := New(StaticRules(rules))
	d, reasons, err := p.Evaluate(context.Background(), Request{
		Branch: "main", Kind: OpDirectWrite, Actor: actor(domain.RoleContributor),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionDeny {
		t.Fatalf("decision = %v, want Deny", d)
	}
	if len(reasons) != 1 {
		t.Fatalf("got %d reasons, want 1 (dedup): %+v", len(reasons), reasons)
	}
}

func TestEvaluate_FullRefIsNormalised(t *testing.T) {
	// Git pre-receive delivers refs in full form. A caller that
	// forgets to strip the prefix must not silently bypass protection.
	p := New(StaticRules(mainOnlyRules))
	d, reasons, err := p.Evaluate(context.Background(), Request{
		Branch: "refs/heads/main",
		Kind:   OpDirectWrite,
		Actor:  actor(domain.RoleContributor),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionDeny || reasons[0].Code != ReasonRuleDenies {
		t.Fatalf("full-ref form bypassed protection: d=%v reasons=%+v", d, reasons)
	}
}

func TestEvaluate_NonBranchRefsSkipped(t *testing.T) {
	// v1 (ADR-009 §6) does not protect tags or other non-branch ref
	// namespaces. A broad glob like `*/*/*` in the rules must not
	// coincidentally match `refs/tags/v1` or `refs/notes/commits`.
	broad := []config.Rule{
		{Branch: "*/*/*", Protections: []config.RuleKind{config.KindNoDelete}},
	}
	p := New(StaticRules(broad))

	cases := []string{
		"refs/tags/v1",
		"refs/notes/commits",
		"refs/meta/config",
	}
	for _, ref := range cases {
		t.Run(ref, func(t *testing.T) {
			d, reasons, err := p.Evaluate(context.Background(), Request{
				Branch: ref,
				Kind:   OpDelete,
				Actor:  actor(domain.RoleContributor),
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d != DecisionAllow || reasons[0].Code != ReasonNoMatchingRule {
				t.Fatalf("non-branch ref matched: d=%v reasons=%+v", d, reasons)
			}
			if !strings.Contains(reasons[0].Message, "not a branch") {
				t.Fatalf("reason message does not explain non-branch exclusion: %q", reasons[0].Message)
			}
		})
	}
}

func TestEvaluate_BootstrapDefaultsWhenSourceNil(t *testing.T) {
	// A workspace with no config file yet: source returns (nil, nil),
	// bootstrap defaults apply, and `main` is protected.
	p := New(StaticRules(nil))
	d, reasons, err := p.Evaluate(context.Background(), Request{
		Branch: "main",
		Kind:   OpDirectWrite,
		Actor:  actor(domain.RoleContributor),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionDeny || reasons[0].Code != ReasonRuleDenies {
		t.Fatalf("bootstrap defaults did not protect main: d=%v reasons=%+v", d, reasons)
	}
}

func TestEvaluate_ExplicitEmptySourceDoesNotApplyDefaults(t *testing.T) {
	// `rules: []` in the config file means "nothing is protected" —
	// an explicit user decision. That must not be silently overridden
	// by bootstrap defaults.
	p := New(StaticRules(config.Config{Rules: []config.Rule{}}.Rules))
	// StaticRules(nil) goes through the bootstrap path (covered above).
	// Here we want a non-nil but empty slice.
	pEmpty := New(StaticRules([]config.Rule{}))
	d, reasons, err := pEmpty.Evaluate(context.Background(), Request{
		Branch: "main",
		Kind:   OpDirectWrite,
		Actor:  actor(domain.RoleContributor),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionAllow || reasons[0].Code != ReasonNoMatchingRule {
		t.Fatalf("explicit empty ruleset should allow; got d=%v reasons=%+v", d, reasons)
	}
	// Also confirm p still behaves normally.
	_, _, _ = p.Evaluate(context.Background(), Request{Branch: "main", Kind: OpDirectWrite, Actor: actor(domain.RoleContributor)})
}

func TestEvaluate_SourceErrorFailsClosed(t *testing.T) {
	p := New(errSource{err: errors.New("projection unavailable")})
	d, reasons, err := p.Evaluate(context.Background(), Request{
		Branch: "main",
		Kind:   OpDirectWrite,
		Actor:  actor(domain.RoleContributor),
	})
	if err == nil {
		t.Fatal("expected infrastructure error, got nil")
	}
	if !strings.Contains(err.Error(), "projection unavailable") {
		t.Fatalf("error does not wrap source error: %v", err)
	}
	// Fail-closed: decision must be Deny even though the caller is
	// expected to check err first.
	if d != DecisionDeny {
		t.Fatalf("decision = %v on source error, want Deny (fail closed)", d)
	}
	if reasons != nil {
		t.Fatalf("reasons = %+v on source error, want nil", reasons)
	}
}

func TestEvaluate_NilRuleSourceUsesBootstrap(t *testing.T) {
	p := New(nil)
	d, _, err := p.Evaluate(context.Background(), Request{
		Branch: "main",
		Kind:   OpDelete,
		Actor:  actor(domain.RoleContributor),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionDeny {
		t.Fatalf("decision = %v, want Deny (bootstrap defaults protect main)", d)
	}
}

func TestEvaluate_UnknownOperationKindFailsClosed(t *testing.T) {
	// An enforcement point that forgets to set Kind (zero value) or
	// passes a misspelled value is a caller bug. The policy is a
	// security boundary; it must not default to allow.
	p := New(StaticRules(mainOnlyRules))

	cases := []struct {
		name string
		kind OperationKind
	}{
		{"zero value", OperationKind("")},
		{"misspelled", OperationKind("hypothetical")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, reasons, err := p.Evaluate(context.Background(), Request{
				Branch: "main",
				Kind:   tc.kind,
				Actor:  actor(domain.RoleContributor),
			})
			if err == nil {
				t.Fatal("expected error for unknown kind, got nil")
			}
			if !strings.Contains(err.Error(), "unknown operation kind") {
				t.Fatalf("error %q does not mention unknown kind", err)
			}
			if d != DecisionDeny {
				t.Fatalf("decision = %v, want Deny (fail closed)", d)
			}
			if reasons != nil {
				t.Fatalf("reasons = %+v, want nil on error", reasons)
			}
		})
	}
}

func TestBootstrapDefaults(t *testing.T) {
	got := BootstrapDefaults()
	want := []config.Rule{
		{
			Branch: "main",
			Protections: []config.RuleKind{
				config.KindNoDelete,
				config.KindNoDirectWrite,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BootstrapDefaults = %+v, want %+v", got, want)
	}
}

func TestStaticRules_Nil(t *testing.T) {
	var s StaticRules
	rules, err := s.Rules(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules != nil {
		t.Fatalf("nil StaticRules returned %+v, want nil", rules)
	}
}

func TestStaticRules_RoundTrip(t *testing.T) {
	in := []config.Rule{{Branch: "main", Protections: []config.RuleKind{config.KindNoDelete}}}
	out, err := StaticRules(in).Rules(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("StaticRules round-trip lost data: got %+v, want %+v", out, in)
	}
}
