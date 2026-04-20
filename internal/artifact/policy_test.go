package artifact_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/testutil"
)

func runGit(repo string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

// fakePolicy records the Request it was called with and returns a
// canned Decision/Reasons/error. Enough to verify the wiring without
// exercising the real evaluator (which has its own extensive tests in
// internal/branchprotect).
type fakePolicy struct {
	mu       sync.Mutex
	called   bool
	req      branchprotect.Request
	decision branchprotect.Decision
	reasons  []branchprotect.Reason
	err      error
}

func (f *fakePolicy) Evaluate(_ context.Context, req branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.req = req
	return f.decision, f.reasons, f.err
}

func (f *fakePolicy) lastRequest() branchprotect.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req
}

func (f *fakePolicy) wasCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

func newServiceWith(t *testing.T, p branchprotect.Policy) (*artifact.Service, string) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	svc := artifact.NewService(client, router, repo)
	svc.WithPolicy(p)
	return svc, repo
}

func TestCreate_PolicyCalledWithDirectWriteOnMain(t *testing.T) {
	p := &fakePolicy{decision: branchprotect.DecisionAllow}
	svc, _ := newServiceWith(t, p)

	if _, err := svc.Create(testCtx(), "governance/policy.md", governanceContent); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !p.wasCalled() {
		t.Fatal("expected policy.Evaluate to be called")
	}
	req := p.lastRequest()
	if req.Branch != "main" {
		t.Errorf("branch = %q, want main", req.Branch)
	}
	if req.Kind != branchprotect.OpDirectWrite {
		t.Errorf("kind = %q, want direct_write", req.Kind)
	}
	if req.TraceID != "test-trace" {
		t.Errorf("trace_id = %q, want test-trace", req.TraceID)
	}
	if req.Override {
		t.Error("Override must be false until TASK-003 wires write_context.override")
	}
}

func TestCreate_PolicyDeniesBlocksWrite(t *testing.T) {
	p := &fakePolicy{
		decision: branchprotect.DecisionDeny,
		reasons: []branchprotect.Reason{{
			Code:     branchprotect.ReasonRuleDenies,
			Message:  `rule "no-direct-write" blocks this operation on branch "main"`,
			RuleKind: config.KindNoDirectWrite,
		}},
	}
	svc, repo := newServiceWith(t, p)

	_, err := svc.Create(testCtx(), "governance/denied.md", governanceContent)
	if err == nil {
		t.Fatal("expected Create to fail on Deny")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if spineErr.Code != domain.ErrForbidden {
		t.Errorf("error code = %q, want forbidden", spineErr.Code)
	}
	if !strings.Contains(spineErr.Message, "no-direct-write") {
		t.Errorf("error message does not mention the rule: %q", spineErr.Message)
	}

	// File must NOT be on disk. The guard runs before enterBranch.
	if _, err := os.Stat(filepath.Join(repo, "governance/denied.md")); !os.IsNotExist(err) {
		t.Errorf("denied write left file on disk: err=%v", err)
	}
}

func TestCreate_PolicyErrorFailsClosed(t *testing.T) {
	p := &fakePolicy{
		decision: branchprotect.DecisionDeny,
		err:      context.DeadlineExceeded, // any non-nil error — the policy fails closed
	}
	svc, _ := newServiceWith(t, p)

	_, err := svc.Create(testCtx(), "governance/evalerr.md", governanceContent)
	if err == nil {
		t.Fatal("expected Create to fail when policy.Evaluate errors")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrInternal {
		t.Errorf("error code = %q, want internal (policy-eval failure is infra, not rule-based denial)", spineErr.Code)
	}
}

func TestCreate_NoPolicyFailsClosed(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	svc := artifact.NewService(client, router, repo)
	// Intentionally do NOT call WithPolicy.

	_, err := svc.Create(testCtx(), "governance/nopolicy.md", governanceContent)
	if err == nil {
		t.Fatal("expected Create to fail when no policy is wired")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrUnavailable {
		t.Errorf("error code = %q, want service_unavailable", spineErr.Code)
	}
	if !strings.Contains(spineErr.Message, "branch-protection policy") {
		t.Errorf("error message should name the missing dependency: %q", spineErr.Message)
	}
}

func TestCreate_PolicySeesActorRoleFromContext(t *testing.T) {
	p := &fakePolicy{decision: branchprotect.DecisionAllow}
	svc, _ := newServiceWith(t, p)

	operator := &domain.Actor{
		ActorID: "op-1",
		Type:    domain.ActorTypeHuman,
		Role:    domain.RoleOperator,
		Status:  domain.ActorStatusActive,
	}
	ctx := domain.WithActor(testCtx(), operator)

	if _, err := svc.Create(ctx, "governance/witactor.md", governanceContent); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := p.lastRequest().Actor
	if got.ActorID != "op-1" || got.Role != domain.RoleOperator {
		t.Errorf("policy saw actor %+v, want operator op-1", got)
	}
}

func TestCreate_WriteContextBranchIsEvaluated(t *testing.T) {
	// With main protected by no-direct-write, a write that specifies a
	// non-main branch via WriteContext must still succeed — the policy
	// sees the run branch ("spine/run/…") which no rule targets, and
	// returns Allow/no_matching_rule. This pins the behaviour EPIC-002
	// TASK-002 (orchestrator merges) relies on: run-branch writes are not
	// collateral damage of main-protection.
	p := &fakePolicy{decision: branchprotect.DecisionAllow}
	svc, repo := newServiceWith(t, p)

	// Pre-create a branch so EnterBranch can create a worktree pointing at it.
	if err := runGit(repo, "branch", "spine/run/example"); err != nil {
		t.Fatalf("git branch: %v", err)
	}

	ctx := artifact.WithWriteContext(testCtx(), artifact.WriteContext{Branch: "spine/run/example"})
	if _, err := svc.Create(ctx, "governance/rb.md", governanceContent); err != nil {
		t.Fatalf("Create on run branch: %v", err)
	}

	if got := p.lastRequest().Branch; got != "spine/run/example" {
		t.Errorf("policy.Branch = %q, want spine/run/example", got)
	}
}

// End-to-end check against the real policy evaluator. Shows that the
// Artifact Service + projection-style rule source + policy all compose —
// the targeted unit tests above use a fake, this one exercises the real
// decision path.
func TestCreate_RealPolicyWithProtectedMainBlocks(t *testing.T) {
	rules := []config.Rule{{
		Branch: "main",
		Protections: []config.RuleKind{
			config.KindNoDirectWrite,
			config.KindNoDelete,
		},
	}}
	svc, _ := newServiceWith(t, branchprotect.New(branchprotect.StaticRules(rules)))

	_, err := svc.Create(testCtx(), "governance/real.md", governanceContent)
	if err == nil {
		t.Fatal("expected Create on protected main to be denied")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok || spineErr.Code != domain.ErrForbidden {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}
