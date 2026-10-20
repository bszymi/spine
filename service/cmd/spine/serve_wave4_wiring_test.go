package main

// INIT-023 wave 4 integration: on both orchestrator builders of `spine
// serve` the wave's options compose — the primary client is the shared
// home-branch tree (TASK-012) and is what the governance guard's merge-base
// listing needs (TASK-006); merge binding (TASK-005) is on, so the status
// lifecycle (TASK-017) and the governance guard, which both require it,
// are on too, under the artifact service's artifacts_dir; the scheduler
// enforces the retry budget with the exhaustion handler (TASK-012) and
// retries through the governance-aware wrapper (TASK-006).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func requireWave4Orchestrator(t *testing.T, where string, orch *engine.Orchestrator) {
	t.Helper()
	if orch == nil {
		t.Fatalf("%s: no orchestrator", where)
	}
	if !orch.MergeBindingEnabled() {
		t.Fatalf("%s: merge binding is off; the lifecycle and the governance guard require it", where)
	}
	requireLifecycleOrchestrator(t, where, orch, "spine")
	checkGuard(t, where, orch, true, "workflows/,spine/workflows/")
}

func TestServeWiring_Wave4ComposesOnBothBuilders(t *testing.T) {
	t.Setenv(governanceGuardEnv, "")
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repoPath := testutil.NewTempRepo(t)
	gitClient := buildPrimaryGitClient(repoPath, git.PushAuthOpts())
	if gitClient.HomeBranch() != engine.AuthoritativeBranch {
		t.Fatalf("primary client home branch = %q", gitClient.HomeBranch())
	}
	if _, ok := any(gitClient).(engine.MergeBaseLister); !ok {
		t.Fatal("the primary client is not an engine.MergeBaseLister; the governance guard could not be enabled")
	}
	q := queue.NewMemoryQueue(4)
	events := event.NewQueueRouter(q)
	artifacts := artifact.NewService(gitClient, events, repoPath)
	artifacts.WithArtifactsDir("spine")

	st := newTimeoutStore()
	_, wfProvider := buildWorkflowResolver(st, gitClient)
	orch := buildOrchestrator(st, wfProvider, gitClient, events, q, artifacts, nil, engine.StrictStepEntryPolicy(),
		workflowDirsFor(&config.SpineConfig{ArtifactsDir: "spine"}), observe.Logger(ctx))
	requireWave4Orchestrator(t, "buildOrchestrator", orch)
	sched := buildScheduler(st, events, orch, 0)
	if maxRetries, handler := sched.CommitRetryBudget(); maxRetries != defaultCommitMaxRetries || !handler {
		t.Fatalf("buildScheduler commit retry budget = %d (handler %v), want %d with the exhaustion handler",
			maxRetries, handler, defaultCommitMaxRetries)
	}

	wsStore := newTimeoutStore()
	repoSpec := repository.PrimarySpec{LocalPath: repoPath}
	reg := repository.New("", repoSpec, func(context.Context) (*repository.Catalog, error) {
		return repository.ParseCatalog(nil, repoSpec)
	}, wsStore)
	pool, err := gitpool.New(gitClient, reg, gitpool.NewCLIClientFactory())
	if err != nil {
		t.Fatal(err)
	}
	projSync := projection.NewService(gitClient, wsStore, events, time.Minute)
	projSync.WithArtifactsDir("spine")
	ss := &workspace.ServiceSet{Store: wsStore, GitClient: gitClient, GitPool: pool, Queue: q, Events: events,
		ProjSync: projSync, Artifacts: artifacts}
	if err := workspaceOrchestratorBuilder(ctx, ss, engine.StrictStepEntryPolicy()); err != nil {
		t.Fatalf("workspaceOrchestratorBuilder: %v", err)
	}
	wsOrch, ok := ss.StepAssigner.(*engine.Orchestrator)
	if !ok {
		t.Fatalf("ss.StepAssigner = %T", ss.StepAssigner)
	}
	requireWave4Orchestrator(t, "workspaceOrchestratorBuilder", wsOrch)
	if ss.CommitExhaustedFn == nil || ss.CommitRetryFn == nil {
		t.Fatal("the workspace ServiceSet lacks the commit retry or the exhaustion handler")
	}
	if !projSync.ProtectionRemovalGuardEnabled() {
		t.Fatal("the workspace projection's protection-removal guard is off")
	}
	// The wrapped retry: a hold is not a failed retry, anything else is.
	held := governanceAwareCommitRetry(func(context.Context, string) error {
		return domain.NewErrorWithCause(domain.ErrPrecondition, "held", engine.ErrGovernanceApprovalRequired)
	})
	if err := held(ctx, "run-1"); err != nil {
		t.Fatalf("governance-aware retry of a held merge = %v, want nil", err)
	}
	boom := errors.New("boom")
	if err := governanceAwareCommitRetry(func(context.Context, string) error { return boom })(ctx, "run-1"); !errors.Is(err, boom) {
		t.Fatalf("governance-aware retry of a failure = %v, want it returned", err)
	}
}
