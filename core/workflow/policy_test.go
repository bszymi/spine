package workflow_test

// INIT-023 TASK-014 (audit F7): workflow.Service consults the ADR-009
// branch-protection policy when one is installed (AC 1), and the nil-policy
// path — the shape SMP embeds — is unchanged (AC 2).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// polSvc returns a service over a temp repo whose main holds pol-flow v1.0,
// with the bootstrap rules installed (StaticRules(nil): main
// no-delete + no-direct-write).
func polSvc(t *testing.T) (*workflow.Service, string) {
	t.Helper()
	svc, repo := newSvc(t)
	// Seed through the nil-policy path (I4), then install the policy.
	if _, err := svc.Create(ctxWithActor(), "pol-flow", fmtYAML("pol-flow", "1.0")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc.WithPolicy(branchprotect.New(branchprotect.StaticRules(nil)))
	return svc, repo
}

func asRole(role domain.ActorRole) context.Context {
	ctx := observe.WithActorID(context.Background(), "a-"+string(role))
	return domain.WithActor(ctx, &domain.Actor{ActorID: "a-" + string(role), Role: role, Status: domain.ActorStatusActive})
}

func revParse(t *testing.T, repo, ref string) string {
	t.Helper()
	return strings.TrimSpace(mustOutput(t, repo, "git", "rev-parse", ref))
}

func worktreeCount(t *testing.T, repo string) int {
	t.Helper()
	return strings.Count(mustOutput(t, repo, "git", "worktree", "list", "--porcelain"), "worktree ")
}

func spineCode(err error) domain.ErrorCode {
	var se *domain.SpineError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// AC 1(a): a reviewer's non-bypass write to main is refused with the
// artifact deny shape; main does not move and no worktree is created.
func TestWorkflowService_Policy_DeniesNonBypassDirectWriteToMain(t *testing.T) {
	for _, op := range []string{"update", "create"} {
		t.Run(op, func(t *testing.T) {
			svc, repo := polSvc(t)
			before := revParse(t, repo, "main")
			var err error
			if op == "update" {
				_, err = svc.Update(asRole(domain.RoleReviewer), "pol-flow", fmtYAML("pol-flow", "1.1"))
			} else {
				_, err = svc.Create(asRole(domain.RoleReviewer), "new-flow", fmtYAML("new-flow", "1.0"))
			}
			var se *domain.SpineError
			if !errors.As(err, &se) || se.Code != domain.ErrForbidden {
				t.Fatalf("reviewer direct %s with policy: err=%v, want forbidden", op, err)
			}
			detail, ok := se.Detail.(map[string]any)
			if !ok || detail["branch"] != "main" || detail["reasons"] == nil {
				t.Fatalf("deny detail = %#v, want {branch: main, reasons: [...]}", se.Detail)
			}
			if after := revParse(t, repo, "main"); after != before {
				t.Fatalf("main moved %s -> %s on a denied write", before, after)
			}
			if n := worktreeCount(t, repo); n != 1 {
				t.Fatalf("a denied write left %d worktrees, want only the main one", n)
			}
		})
	}
}

// AC 1(b): a write scoped to a planning branch matches no rule and lands.
func TestWorkflowService_Policy_RunBranchWriteAllowed(t *testing.T) {
	svc, repo := polSvc(t)
	mustRun(t, repo, "git", "branch", "spine/plan/pol-flow-abc")
	ctx := workflow.WithWriteContext(asRole(domain.RoleReviewer), workflow.WriteContext{Branch: "spine/plan/pol-flow-abc"})
	if _, err := svc.Update(ctx, "pol-flow", fmtYAML("pol-flow", "1.1")); err != nil {
		t.Fatalf("reviewer write on a planning branch: %v", err)
	}
	if body := mustOutput(t, repo, "git", "show", "spine/plan/pol-flow-abc:workflows/pol-flow.yaml"); !strings.Contains(body, `version: "1.1"`) {
		t.Fatalf("branch does not carry the update:\n%s", body)
	}
}

type errPolicy struct{}

func (errPolicy) Evaluate(context.Context, branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	return branchprotect.DecisionDeny, nil, errors.New("rule source down")
}

// AC 1(c): an evaluation error fails closed (500) and nothing is written.
func TestWorkflowService_Policy_EvalErrorFailsClosed(t *testing.T) {
	svc, repo := polSvc(t)
	svc.WithPolicy(errPolicy{})
	before := revParse(t, repo, "main")
	_, err := svc.Update(asRole(domain.RoleReviewer), "pol-flow", fmtYAML("pol-flow", "1.1"))
	if code := spineCode(err); code != domain.ErrInternal {
		t.Fatalf("eval error: err=%v, want internal_error", err)
	}
	if after := revParse(t, repo, "main"); after != before {
		t.Fatal("main moved after a policy evaluation error")
	}
}

// AC 1(d): a bypass write needs an operator-or-above actor; an operator
// bypass lands with the Workflow-Bypass trailer.
func TestWorkflowService_Policy_BypassRequiresOperator(t *testing.T) {
	svc, repo := polSvc(t)
	before := revParse(t, repo, "main")

	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"reviewer", workflow.WithBypass(asRole(domain.RoleReviewer))},
		{"contributor", workflow.WithBypass(asRole(domain.RoleContributor))},
		{"actor-less", workflow.WithBypass(observe.WithActorID(context.Background(), "someone"))},
	}
	for _, c := range cases {
		if _, err := svc.Update(c.ctx, "pol-flow", fmtYAML("pol-flow", "1.1")); spineCode(err) != domain.ErrForbidden {
			t.Fatalf("%s bypass with a policy: err=%v, want forbidden", c.name, err)
		}
	}
	if after := revParse(t, repo, "main"); after != before {
		t.Fatal("main moved on a refused bypass")
	}

	for _, role := range []domain.ActorRole{domain.RoleOperator, domain.RoleAdmin} {
		v := "1.1"
		if role == domain.RoleAdmin {
			v = "1.2"
		}
		if _, err := svc.Update(workflow.WithBypass(asRole(role)), "pol-flow", fmtYAML("pol-flow", v)); err != nil {
			t.Fatalf("%s bypass write: %v", role, err)
		}
		if msg := mustOutput(t, repo, "git", "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "Workflow-Bypass: true") {
			t.Fatalf("%s bypass commit lacks the trailer:\n%s", role, msg)
		}
	}
}

// recordingPolicy captures every request and answers a fixed decision.
type recordingPolicy struct {
	mu       sync.Mutex
	requests []branchprotect.Request
	inner    branchprotect.Policy
}

func (p *recordingPolicy) Evaluate(ctx context.Context, req branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return p.inner.Evaluate(ctx, req)
}

// An operator bypass on the authoritative branch is not rule-evaluated
// (Decision 4), while a non-bypass write is evaluated as OpDirectWrite on
// its real target with the request actor, run and trace.
func TestWorkflowService_Policy_RequestShape(t *testing.T) {
	svc, repo := polSvc(t)
	rec := &recordingPolicy{inner: branchprotect.New(branchprotect.StaticRules([]config.Rule{}))}
	svc.WithPolicy(rec)

	if _, err := svc.Update(workflow.WithBypass(asRole(domain.RoleOperator)), "pol-flow", fmtYAML("pol-flow", "1.1")); err != nil {
		t.Fatalf("operator bypass: %v", err)
	}
	if len(rec.requests) != 0 {
		t.Fatalf("operator bypass on main was rule-evaluated: %+v", rec.requests)
	}

	mustRun(t, repo, "git", "branch", "spine/plan/pol-flow-def")
	ctx := observe.WithRunID(observe.WithTraceID(asRole(domain.RoleReviewer), "trace-xyz"), "run-def")
	ctx = workflow.WithWriteContext(ctx, workflow.WriteContext{Branch: "spine/plan/pol-flow-def"})
	if _, err := svc.Update(ctx, "pol-flow", fmtYAML("pol-flow", "1.2")); err != nil {
		t.Fatalf("branch write: %v", err)
	}
	if len(rec.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(rec.requests))
	}
	got := rec.requests[0]
	if got.Branch != "spine/plan/pol-flow-def" || got.Kind != branchprotect.OpDirectWrite ||
		got.Actor.ActorID != "a-reviewer" || got.Actor.Role != domain.RoleReviewer ||
		got.RunID != "run-def" || got.TraceID != "trace-xyz" || got.Override {
		t.Fatalf("request = %+v", got)
	}

	// A bypass that also names a non-authoritative branch falls through to
	// evaluation (the gateway never builds one; the service must not treat
	// it as the sanctioned main commit).
	mustRun(t, repo, "git", "branch", "spine/plan/pol-flow-ghi")
	bctx := workflow.WithBypass(workflow.WithWriteContext(asRole(domain.RoleOperator), workflow.WriteContext{Branch: "spine/plan/pol-flow-ghi"}))
	if _, err := svc.Update(bctx, "pol-flow", fmtYAML("pol-flow", "1.3")); err != nil {
		t.Fatalf("operator bypass on a branch: %v", err)
	}
	if len(rec.requests) != 2 || rec.requests[1].Branch != "spine/plan/pol-flow-ghi" {
		t.Fatalf("bypass on a non-authoritative branch was not evaluated: %+v", rec.requests)
	}
}

// WithDefaultRef: the branch reported for a write with no WriteContext.
func TestWorkflowService_Policy_DefaultRef(t *testing.T) {
	svc, _ := polSvc(t)
	rec := &recordingPolicy{inner: branchprotect.New(branchprotect.StaticRules([]config.Rule{}))}
	svc.WithPolicy(rec)
	svc.WithDefaultRef("") // ignored
	svc.WithDefaultRef("trunk")
	if _, err := svc.Update(asRole(domain.RoleReviewer), "pol-flow", fmtYAML("pol-flow", "1.1")); err != nil {
		t.Fatalf("update with an empty rule set: %v", err)
	}
	if len(rec.requests) != 1 || rec.requests[0].Branch != "trunk" {
		t.Fatalf("requests = %+v, want one on trunk", rec.requests)
	}
}

// AC 2 — the SMP compatibility gate. SMP builds the service with no policy
// and writes with WithBypass and no domain actor; a non-bypass actor-less
// write must keep committing too. Both land on main exactly as before
// TASK-014. WithPolicy(nil) restores the same mode.
func TestWorkflowService_NilPolicy_KeepsPreTask014Behaviour(t *testing.T) {
	for _, reset := range []bool{false, true} {
		svc, repo := newSvc(t)
		if reset {
			svc.WithPolicy(branchprotect.New(branchprotect.StaticRules(nil)))
			svc.WithPolicy(nil)
		}
		if _, err := svc.Create(workflow.WithBypass(observe.WithActorID(context.Background(), "smp-admin")), "pol-flow", fmtYAML("pol-flow", "1.0")); err != nil {
			t.Fatalf("nil-policy actor-less bypass create (SMP shape): %v", err)
		}
		if _, err := svc.Update(workflow.WithBypass(context.Background()), "pol-flow", fmtYAML("pol-flow", "1.1")); err != nil {
			t.Fatalf("nil-policy actor-less bypass update (SMP shape): %v", err)
		}
		if msg := mustOutput(t, repo, "git", "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "Workflow-Bypass: true") {
			t.Fatalf("bypass trailer missing:\n%s", msg)
		}
		if _, err := svc.Update(context.Background(), "pol-flow", fmtYAML("pol-flow", "1.2")); err != nil {
			t.Fatalf("nil-policy actor-less non-bypass update: %v", err)
		}
		if body := mustOutput(t, repo, "git", "show", "main:workflows/pol-flow.yaml"); !strings.Contains(body, `version: "1.2"`) {
			t.Fatalf("main does not carry the last write:\n%s", body)
		}
	}
}

// ValidateID / FilePath are the exported id rule and path rule.
func TestValidateIDAndFilePath(t *testing.T) {
	for _, ok := range []string{"task-default", "a", "x.y_z-1"} {
		if err := workflow.ValidateID(ok); err != nil {
			t.Errorf("ValidateID(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "../x", "A", "-x", "x/y", "x y"} {
		if spineCode(workflow.ValidateID(bad)) != domain.ErrInvalidParams {
			t.Errorf("ValidateID(%q) accepted", bad)
		}
	}
	if got := workflow.FilePath("task-default"); got != "workflows/task-default.yaml" {
		t.Errorf("FilePath = %q", got)
	}
}
