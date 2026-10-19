package main

// INIT-023 TASK-017 AC 13: `spine serve` turns the declared status
// lifecycle on everywhere — both orchestrator builders (with merge binding
// and the workspace's artifacts_dir), the workflow service (commit.status
// at create/update/validate) and the validator (blocking SC-002/SC-003).
// Each assertion fails when its wiring line is deleted.

import (
	"context"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/core/validation"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

// foreignStatusWorkflow commits a status no Task can hold.
const foreignStatusWorkflow = `id: foreign-status
name: Foreign
version: "1.0"
status: Active
description: commit.status that is not a Task status
applies_to:
  - Task
entry_step: start
steps:
  - id: start
    name: Start
    type: manual
    outcomes:
      - id: accept
        name: Accept
        next_step: end
        commit:
          status: Accepted
`

func requireLifecycleOrchestrator(t *testing.T, where string, orch *engine.Orchestrator, dir string) {
	t.Helper()
	if orch.StatusLifecycle() == nil {
		t.Fatalf("%s: no status lifecycle", where)
	}
	if !orch.MergeBindingEnabled() {
		t.Fatalf("%s: the lifecycle needs merge binding, which is off", where)
	}
	if got := orch.ArtifactsDir(); got != dir {
		t.Fatalf("%s: artifacts dir = %q, want %q", where, got, dir)
	}
	if dir != "" {
		if got := artifact.NewPathScope(orch.ArtifactsDir()).RepoPath("initiatives/x.md"); got != dir+"/initiatives/x.md" {
			t.Fatalf("%s: initiatives/x.md resolves to %q", where, got)
		}
	}
}

func TestServeWiring_StatusLifecycle_BuildServerConfig(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	repoPath := testutil.NewTempRepo(t)
	q := queue.NewMemoryQueue(16)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go q.Start(ctx)
	rt, err := buildServerConfig(ctx, serveDeps{
		Store:           stubStore{},
		RepoPath:        repoPath,
		SpineCfg:        &config.SpineConfig{ArtifactsDir: "spine"},
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
	orch, ok := rt.Config.StepAssigner.(*engine.Orchestrator)
	if !ok {
		t.Fatalf("StepAssigner = %T", rt.Config.StepAssigner)
	}
	requireLifecycleOrchestrator(t, "process orchestrator", orch, "spine")

	if rt.Config.Validator == nil {
		t.Fatal("no validator")
	}
	if got := rt.Config.Validator.BlockingStatusConflicts(); !reflect.DeepEqual(got, validation.DefaultBlockingStatusConflicts) {
		t.Fatalf("validator blocking set = %v, want %v", got, validation.DefaultBlockingStatusConflicts)
	}
	if res := rt.Config.Workflows.ValidateBody(context.Background(), "foreign-status", foreignStatusWorkflow); res.Status != "failed" {
		t.Fatalf("workflow ValidateBody = %s %v, want failed (commit_status)", res.Status, res.Errors)
	}
}

func TestServeWiring_StatusLifecycle_Builders(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repoPath := testutil.NewTempRepo(t)
	gitClient := git.NewCLIClient(repoPath)
	q := queue.NewMemoryQueue(4)
	events := event.NewQueueRouter(q)

	artifacts := artifact.NewService(gitClient, events, repoPath)
	artifacts.WithArtifactsDir("spine")

	st := newTimeoutStore()
	_, wfProvider := buildWorkflowResolver(st, gitClient)
	orch := buildOrchestrator(st, wfProvider, gitClient, events, q, artifacts, nil, engine.StrictStepEntryPolicy(), nil, observe.Logger(ctx))
	if orch == nil {
		t.Fatal("buildOrchestrator returned nil")
	}
	requireLifecycleOrchestrator(t, "buildOrchestrator", orch, "spine")

	wsStore := newTimeoutStore()
	repoSpec := repository.PrimarySpec{LocalPath: repoPath}
	reg := repository.New("", repoSpec, func(context.Context) (*repository.Catalog, error) {
		return repository.ParseCatalog(nil, repoSpec)
	}, wsStore)
	pool, err := gitpool.New(gitClient, reg, gitpool.NewCLIClientFactory())
	if err != nil {
		t.Fatal(err)
	}
	ss := &workspace.ServiceSet{Store: wsStore, GitClient: gitClient, GitPool: pool, Queue: q, Events: events, Artifacts: artifacts}
	if err := workspaceOrchestratorBuilder(ctx, ss, engine.StepEntryPolicy{}); err != nil {
		t.Fatalf("workspaceOrchestratorBuilder: %v", err)
	}
	wsOrch, ok := ss.StepAssigner.(*engine.Orchestrator)
	if !ok {
		t.Fatalf("ss.StepAssigner = %T", ss.StepAssigner)
	}
	requireLifecycleOrchestrator(t, "workspace orchestrator", wsOrch, "spine")

	svc, err := buildWorkflowService(gitClient, repoPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res := svc.ValidateBody(ctx, "foreign-status", foreignStatusWorkflow); res.Status != "failed" {
		t.Fatalf("buildWorkflowService: ValidateBody = %s, want failed", res.Status)
	}
}
