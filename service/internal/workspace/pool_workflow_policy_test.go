package workspace

// INIT-023 TASK-014 (audit F7) AC 12: every per-workspace workflow.Service
// carries a branch-protection policy — projection-backed with a store
// (pool_workflow_policy_integration_test.go), permissive without one. The
// probe is behavioural: with any policy installed an actor-less ADR-008
// bypass write is refused (it needs an operator actor), while a
// policy-less service — the pre-TASK-014 shape, and SMP's — accepts it.

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/testutil"
)

const policyProbeWorkflow = `id: probe-flow
name: Probe
version: "1.0"
status: Active
description: probe
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: done
        name: Done
        next_step: end
`

func TestBuildServiceSet_WorkflowServiceHasPolicy_NoStore(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	cfg := Config{ID: "ws-wfpolicy", RepoPath: testutil.NewTempRepo(t)}
	ss, err := buildServiceSet(context.Background(), cfg, nil, nil, "", nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	defer ss.close("shutdown")
	if ss.Workflows == nil {
		t.Fatal("no workflow service")
	}

	ctx := workflow.WithBypass(observe.WithActorID(context.Background(), "nobody"))
	_, err = ss.Workflows.Create(ctx, "probe-flow", policyProbeWorkflow)
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrForbidden {
		t.Fatalf("actor-less bypass on the pooled workflow service: %v, want forbidden (a policy must be installed)", err)
	}
	// The permissive policy denies nothing else: an operator bypass lands.
	op := domain.WithActor(context.Background(), &domain.Actor{ActorID: "op", Role: domain.RoleOperator, Status: domain.ActorStatusActive})
	if _, err := ss.Workflows.Create(workflow.WithBypass(op), "probe-flow", policyProbeWorkflow); err != nil {
		t.Fatalf("operator bypass with the permissive policy: %v", err)
	}
}
