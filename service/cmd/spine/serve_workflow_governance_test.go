package main

// INIT-023 TASK-014 (audit F7) AC 12: `spine serve` single-workspace wiring.
// buildServerConfig with a store yields a workflow planning starter (before
// this task only pooled workspaces had one, so reviewer workflow updates
// routed through the lifecycle would have answered 503) and a workflow
// service that consults the projection-backed branch-protection policy, so
// a reviewer's non-bypass write to a protected main is refused at the
// service.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/testutil"
)

// bootstrapRuleStore serves the bootstrap rule (main: no-delete,
// no-direct-write) from the branch-protection projection, as a freshly
// migrated workspace does.
type bootstrapRuleStore struct{ stubStore }

func (bootstrapRuleStore) ListBranchProtectionRules(context.Context) ([]store.BranchProtectionRuleProjection, error) {
	p, _ := json.Marshal([]string{"no-delete", "no-direct-write"})
	return []store.BranchProtectionRuleProjection{{BranchPattern: "main", Protections: p}}, nil
}

const governanceProbeWorkflow = `id: probe-flow
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

func asActor(role domain.ActorRole) context.Context {
	ctx := observe.WithActorID(context.Background(), "a-"+string(role))
	return domain.WithActor(ctx, &domain.Actor{ActorID: "a-" + string(role), Role: role, Status: domain.ActorStatusActive})
}

func TestBuildServerConfig_WiresWorkflowGovernance(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repoPath := testutil.NewTempRepo(t)
	q := queue.NewMemoryQueue(16)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go q.Start(ctx)
	rt, err := buildServerConfig(ctx, serveDeps{
		Store:           bootstrapRuleStore{},
		RepoPath:        repoPath,
		SpineCfg:        &config.SpineConfig{ArtifactsDir: "/"},
		GitClient:       git.NewCLIClient(repoPath),
		Queue:           q,
		Events:          event.NewQueueRouter(q),
		DevMode:         true,
		RuntimeEnv:      "development",
		StepEntryPolicy: engine.StrictStepEntryPolicy(),
	})
	if err != nil {
		t.Fatalf("buildServerConfig: %v", err)
	}

	adapter, ok := rt.Config.WFPlanningStarter.(*workflowPlanningRunAdapter)
	if !ok || adapter == nil || adapter.orch == nil {
		t.Fatalf("WFPlanningStarter = %#v, want the orchestrator-backed adapter", rt.Config.WFPlanningStarter)
	}
	if any(adapter.orch) != any(rt.Config.StepAssigner) {
		t.Fatal("the workflow planning starter must use the gateway's orchestrator")
	}

	svc := rt.Config.Workflows
	if svc == nil {
		t.Fatal("no workflow service")
	}
	_, err = svc.Create(asActor(domain.RoleReviewer), "probe-flow", governanceProbeWorkflow)
	var se *domain.SpineError
	if !errors.As(err, &se) || se.Code != domain.ErrForbidden {
		t.Fatalf("reviewer non-bypass write to main: %v, want forbidden under the bootstrap rule", err)
	}
	if _, err := svc.Create(workflow.WithBypass(asActor(domain.RoleReviewer)), "probe-flow", governanceProbeWorkflow); err == nil {
		t.Fatal("a reviewer bypass write must be refused once a policy is installed")
	}
	res, err := svc.Create(workflow.WithBypass(asActor(domain.RoleOperator)), "probe-flow", governanceProbeWorkflow)
	if err != nil || res.CommitSHA == "" {
		t.Fatalf("operator bypass (the ADR-008 recovery path): %v", err)
	}
}

// buildWorkflowService installs the same policy shape as
// buildArtifactService: permissive with no store (early bootstrap), and
// projection-backed with one.
func TestBuildWorkflowService_InstallsPolicy(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	for _, tc := range []struct {
		name          string
		st            store.Store
		wantForbidden bool
	}{
		{"no store: permissive", nil, false},
		{"store: projection-backed", bootstrapRuleStore{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.NewTempRepo(t)
			svc, err := buildWorkflowService(git.NewCLIClient(repo), repo, tc.st)
			if err != nil {
				t.Fatal(err)
			}
			// Any installed policy refuses an actor-less bypass.
			if _, err := svc.Create(workflow.WithBypass(context.Background()), "probe-flow", governanceProbeWorkflow); err == nil {
				t.Fatal("actor-less bypass accepted: no policy installed")
			}
			_, err = svc.Create(asActor(domain.RoleReviewer), "probe-flow", governanceProbeWorkflow)
			var se *domain.SpineError
			gotForbidden := errors.As(err, &se) && se.Code == domain.ErrForbidden
			if gotForbidden != tc.wantForbidden {
				t.Fatalf("reviewer non-bypass write to main: %v, want forbidden=%v", err, tc.wantForbidden)
			}
		})
	}
}
