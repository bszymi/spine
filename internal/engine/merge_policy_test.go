package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
)

// capturingPolicy records the Request it was called with and returns
// a canned Decision / Reasons / error. Mirrors the fake used in
// internal/artifact/policy_test.go — a fake is fine here because the
// real evaluator has its own coverage in internal/branchprotect.
type capturingPolicy struct {
	mu       sync.Mutex
	called   bool
	req      branchprotect.Request
	decision branchprotect.Decision
	reasons  []branchprotect.Reason
	err      error
}

func (c *capturingPolicy) Evaluate(_ context.Context, req branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.called = true
	c.req = req
	return c.decision, c.reasons, c.err
}

func (c *capturingPolicy) request() branchprotect.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.req
}

func (c *capturingPolicy) wasCalled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.called
}

// committingRun returns a run that is in the state MergeRunBranch
// expects and therefore will reach the policy check.
func committingRun(runID, branch string) *domain.Run {
	return &domain.Run{
		RunID:      runID,
		TaskPath:   "tasks/t.md",
		Status:     domain.RunStatusCommitting,
		BranchName: branch,
		TraceID:    "trace-" + runID,
	}
}

func TestMergeRunBranch_PolicyCalledWithGovernedMerge(t *testing.T) {
	run := committingRun("run-policy-1", "spine/run/policy-1")
	store := &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}
	gitOp := &mockGitOperator{mergeResult: git.MergeResult{SHA: "merge-sha"}}
	policy := &capturingPolicy{decision: branchprotect.DecisionAllow}
	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   policy,
	}

	if err := orch.MergeRunBranch(context.Background(), run.RunID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	if !policy.wasCalled() {
		t.Fatal("expected policy.Evaluate to be called on merge")
	}
	req := policy.request()
	if req.Kind != branchprotect.OpGovernedMerge {
		t.Errorf("kind = %q, want governed_merge", req.Kind)
	}
	if req.Branch != "main" {
		t.Errorf("branch = %q, want main (authoritative)", req.Branch)
	}
	if req.RunID != run.RunID {
		t.Errorf("run_id = %q, want %q", req.RunID, run.RunID)
	}
	if req.TraceID != run.TraceID {
		t.Errorf("trace_id = %q, want %q", req.TraceID, run.TraceID)
	}
}

func TestMergeRunBranch_NilPolicyFailsClosed(t *testing.T) {
	run := committingRun("run-policy-nil", "spine/run/policy-nil")
	store := &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}
	gitOp := &mockGitOperator{mergeResult: git.MergeResult{SHA: "merge-sha"}}
	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		// Intentionally no policy.
	}

	err := orch.MergeRunBranch(context.Background(), run.RunID)
	if err == nil {
		t.Fatal("expected MergeRunBranch to fail when no policy is configured")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T: %v", err, err)
	}
	if spineErr.Code != domain.ErrUnavailable {
		t.Errorf("error code = %q, want service_unavailable", spineErr.Code)
	}
}

func TestMergeRunBranch_PolicyEvalErrorFailsClosed(t *testing.T) {
	run := committingRun("run-policy-err", "spine/run/policy-err")
	store := &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}
	gitOp := &mockGitOperator{mergeResult: git.MergeResult{SHA: "merge-sha"}}
	policy := &capturingPolicy{
		decision: branchprotect.DecisionDeny,
		err:      errors.New("rule source unreachable"),
	}
	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   policy,
	}

	err := orch.MergeRunBranch(context.Background(), run.RunID)
	if err == nil {
		t.Fatal("expected MergeRunBranch to fail when policy evaluator errors")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrInternal {
		t.Errorf("error code = %q, want internal (evaluator failure is infra)", spineErr.Code)
	}
}

func TestMergeRunBranch_PolicyDenyBlocksMerge(t *testing.T) {
	// OpGovernedMerge is allowed unconditionally by the real policy, but
	// a fake can still return Deny. The Orchestrator must honour that —
	// the code path is what matters, not the evaluator's current rules.
	run := committingRun("run-policy-deny", "spine/run/policy-deny")
	store := &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}
	gitOp := &mockGitOperator{mergeResult: git.MergeResult{SHA: "merge-sha"}}
	policy := &capturingPolicy{
		decision: branchprotect.DecisionDeny,
		reasons: []branchprotect.Reason{{
			Code:     branchprotect.ReasonRuleDenies,
			Message:  `rule "no-direct-write" blocks this operation on branch "main"`,
			RuleKind: config.KindNoDirectWrite,
		}},
	}
	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   policy,
	}

	err := orch.MergeRunBranch(context.Background(), run.RunID)
	if err == nil {
		t.Fatal("expected MergeRunBranch to fail on policy Deny")
	}
	spineErr, ok := err.(*domain.SpineError)
	if !ok {
		t.Fatalf("expected *domain.SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrForbidden {
		t.Errorf("error code = %q, want forbidden", spineErr.Code)
	}
	if !strings.Contains(spineErr.Message, "no-direct-write") {
		t.Errorf("error message does not name the rule: %q", spineErr.Message)
	}
}

// TestMergeRunBranch_RealPolicyAllowsGovernedMerge pins the end-to-end
// semantic: the real branchprotect evaluator (not a fake) allows
// OpGovernedMerge onto main even when main has no-direct-write. This is
// the central ADR-009 §2 guarantee — governed merges are the intended
// write path, rules do not gate them.
func TestMergeRunBranch_RealPolicyAllowsGovernedMerge(t *testing.T) {
	run := committingRun("run-real-policy", "spine/run/real-policy")
	store := &mockRunStore{runs: map[string]*domain.Run{run.RunID: run}}
	gitOp := &mockGitOperator{mergeResult: git.MergeResult{SHA: "merge-sha"}}
	rules := []config.Rule{{
		Branch: "main",
		Protections: []config.RuleKind{
			config.KindNoDirectWrite,
			config.KindNoDelete,
		},
	}}
	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   branchprotect.New(branchprotect.StaticRules(rules)),
	}

	if err := orch.MergeRunBranch(context.Background(), run.RunID); err != nil {
		t.Fatalf("MergeRunBranch on main-with-no-direct-write should be allowed for governed merges: %v", err)
	}
	_ = gitOp // merge succeeded per the non-error return; fine.
}
