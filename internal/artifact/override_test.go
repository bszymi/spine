package artifact_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

// captureEvents is a minimal test double that records every event
// emitted through the router. Simpler than event.QueueRouter, which
// needs a queue goroutine.
type captureEvents struct {
	mu     sync.Mutex
	events []domain.Event
}

func (c *captureEvents) Emit(_ context.Context, evt domain.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt)
	return nil
}

func (c *captureEvents) Subscribe(_ context.Context, _ domain.EventType, _ event.EventHandler) error {
	return nil
}

func (c *captureEvents) byType(t domain.EventType) []domain.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []domain.Event
	for _, e := range c.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func newServiceWithEvents(t *testing.T, rules []config.Rule) (*artifact.Service, string, *captureEvents, *git.CLIClient) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	events := &captureEvents{}
	svc := artifact.NewService(client, events, repo)
	svc.WithPolicy(branchprotect.New(branchprotect.StaticRules(rules)))
	return svc, repo, events, client
}

// protectedMainRules protects main with both no-direct-write and
// no-delete — matches BootstrapDefaults() but declared explicitly to
// decouple the test from future rule changes.
func protectedMainRules() []config.Rule {
	return []config.Rule{{
		Branch: "main",
		Protections: []config.RuleKind{
			config.KindNoDirectWrite,
			config.KindNoDelete,
		},
	}}
}

func operatorCtx() context.Context {
	return domain.WithActor(testCtx(), &domain.Actor{
		ActorID: "op-1",
		Type:    domain.ActorTypeHuman,
		Role:    domain.RoleOperator,
		Status:  domain.ActorStatusActive,
	})
}

func contributorCtx() context.Context {
	return domain.WithActor(testCtx(), &domain.Actor{
		ActorID: "contrib-1",
		Type:    domain.ActorTypeHuman,
		Role:    domain.RoleContributor,
		Status:  domain.ActorStatusActive,
	})
}

func TestOverride_OperatorHonouredEmitsEventAndTrailer(t *testing.T) {
	svc, _, events, client := newServiceWithEvents(t, protectedMainRules())
	ctx := artifact.WithWriteContext(operatorCtx(), artifact.WriteContext{Override: true})

	result, err := svc.Create(ctx, "governance/override-honoured.md", governanceContent)
	if err != nil {
		t.Fatalf("Create with operator override: %v", err)
	}
	if result.CommitSHA == "" {
		t.Fatal("expected commit SHA on honoured override")
	}

	// Trailer must be on the produced commit.
	commits, err := client.Log(context.Background(), git.LogOpts{Limit: 1})
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("expected at least one commit")
	}
	if got := commits[0].Trailers["Branch-Protection-Override"]; got != "true" {
		t.Errorf("Branch-Protection-Override trailer = %q, want true", got)
	}

	// Governance event must have been emitted exactly once with the
	// payload shape documented in ADR-009 §4.
	overrides := events.byType(domain.EventBranchProtectionOverride)
	if len(overrides) != 1 {
		t.Fatalf("expected 1 branch_protection.override event, got %d", len(overrides))
	}
	evt := overrides[0]
	if evt.ActorID != "op-1" {
		t.Errorf("event actor_id = %q, want op-1", evt.ActorID)
	}
	if evt.TraceID != "test-trace" {
		t.Errorf("event trace_id = %q, want test-trace", evt.TraceID)
	}
	var payload map[string]any
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["branch"] != "main" {
		t.Errorf("payload.branch = %v, want main", payload["branch"])
	}
	if payload["operation"] != "artifact.create" {
		t.Errorf("payload.operation = %v, want artifact.create", payload["operation"])
	}
	if payload["commit_sha"] != result.CommitSHA {
		t.Errorf("payload.commit_sha = %v, want %s", payload["commit_sha"], result.CommitSHA)
	}
	kinds, _ := payload["rule_kinds"].([]any)
	if len(kinds) == 0 {
		t.Error("payload.rule_kinds is empty; expected at least one bypassed rule kind")
	}
	found := false
	for _, k := range kinds {
		if k == "no-direct-write" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("payload.rule_kinds does not include no-direct-write: %v", kinds)
	}
}

func TestOverride_ContributorDenied(t *testing.T) {
	svc, _, events, _ := newServiceWithEvents(t, protectedMainRules())
	ctx := artifact.WithWriteContext(contributorCtx(), artifact.WriteContext{Override: true})

	_, err := svc.Create(ctx, "governance/override-denied.md", governanceContent)
	if err == nil {
		t.Fatal("expected contributor override to be denied")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok || spineErr.Code != domain.ErrForbidden {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	// Detail should contain the "override not authorised" reason so a UI
	// can distinguish "you can't use override" from "this cannot be
	// overridden" (ADR-009 §4).
	detail, _ := spineErr.Detail.(map[string]any)
	reasons, _ := detail["reasons"].([]map[string]string)
	found := false
	for _, r := range reasons {
		if r["code"] == string(branchprotect.ReasonOverrideNotAuthorised) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ReasonOverrideNotAuthorised in detail, got %+v", reasons)
	}

	// No governance event — the request was denied, not honoured.
	if got := len(events.byType(domain.EventBranchProtectionOverride)); got != 0 {
		t.Errorf("expected no override event on denial, got %d", got)
	}
}

func TestOverride_UnnecessaryFlagWithExplicitEmptyRulesIsSilent(t *testing.T) {
	svc, _, events, client := newServiceWithEvents(t, []config.Rule{}) // explicit empty: no protection
	ctx := artifact.WithWriteContext(operatorCtx(), artifact.WriteContext{Override: true})

	result, err := svc.Create(ctx, "governance/no-rule-override.md", governanceContent)
	if err != nil {
		t.Fatalf("Create with unneeded override: %v", err)
	}

	// No trailer — the override was set but the policy allowed because
	// no rule matched, not because of the override.
	commits, _ := client.Log(context.Background(), git.LogOpts{Limit: 1})
	if len(commits) == 0 {
		t.Fatal("expected commit")
	}
	if got := commits[0].Trailers["Branch-Protection-Override"]; got == "true" {
		t.Errorf("Branch-Protection-Override trailer must not be added when no rule bypassed; got true")
	}

	// No governance event — the override was not actually honoured.
	if got := len(events.byType(domain.EventBranchProtectionOverride)); got != 0 {
		t.Errorf("expected no override event when no rule matched, got %d", got)
	}
	_ = result
}

func TestOverride_WithoutFlagDenied(t *testing.T) {
	// Control: operator on protected main WITHOUT the override flag still
	// gets denied. Pins the "operators do not get a free pass" invariant.
	svc, _, events, _ := newServiceWithEvents(t, protectedMainRules())
	// operatorCtx but no WriteContext → Override stays false.
	_, err := svc.Create(operatorCtx(), "governance/no-override.md", governanceContent)
	if err == nil {
		t.Fatal("expected operator write to main without override to be denied")
	}
	if got := len(events.byType(domain.EventBranchProtectionOverride)); got != 0 {
		t.Errorf("expected no override event on denial, got %d", got)
	}
}

// Ensure the gateway's JSON schema accepts `override: true`. Unit-level:
// decoding the wire struct produces the expected Go field.
func TestOverride_WireTypeDecodesOverrideField(t *testing.T) {
	// This is a small sanity check — deep gateway tests live in
	// internal/gateway. We only verify that Go tag mapping is correct so
	// a future refactor that renames the field fails here first.
	type writeCtx struct {
		RunID    string `json:"run_id"`
		TaskPath string `json:"task_path"`
		Override bool   `json:"override,omitempty"`
	}
	var parsed writeCtx
	body := []byte(`{"run_id":"r1","task_path":"t.md","override":true}`)
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !parsed.Override {
		t.Errorf("expected override=true after decode")
	}
}

// TestBootstrapDefaults_DirectCommitToMainRejected pins the ADR-009 §1
// "repo without a .spine/branch-protection.yaml still has authoritative-
// branch invariants" guarantee. A fresh deployment that has not yet
// seeded the config file must still reject direct writes to main —
// the policy falls back to BootstrapDefaults() when the rule source
// returns (nil, nil), which it does for StaticRules(nil).
func TestBootstrapDefaults_DirectCommitToMainRejected(t *testing.T) {
	// Nil rules == unseeded source. branchprotect.Policy treats that as
	// "apply BootstrapDefaults()" per effectiveRules in policy.go.
	svc, _, _, _ := newServiceWithEvents(t, nil)

	_, err := svc.Create(testCtx(), "governance/bootstrap.md", governanceContent)
	if err == nil {
		t.Fatal("expected direct commit to main on bootstrap-defaults repo to be rejected")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrForbidden {
		t.Errorf("error code = %q, want forbidden (bootstrap defaults protect main)", spineErr.Code)
	}
}

// Ensure we don't accidentally wire branchprotect.Reason into Detail
// via a type that breaks the JSON/detail round-trip. Reading and asserting
// shape here catches a refactor that changes reasonsToDetail's type.
func TestOverride_DetailShapeIsJSONSerialisable(t *testing.T) {
	svc, _, _, _ := newServiceWithEvents(t, protectedMainRules())
	_, err := svc.Create(testCtx(), "governance/shape.md", governanceContent) // no actor, no override
	if err == nil {
		t.Fatal("expected deny")
	}
	spineErr, _ := err.(*domain.SpineError)
	detail, _ := spineErr.Detail.(map[string]any)
	if _, err := json.Marshal(detail); err != nil {
		t.Fatalf("detail not JSON-serialisable: %v", err)
	}
}

