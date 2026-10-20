package main

// INIT-023 TASK-012 AC 19: `spine serve` builds the workspace's primary
// client with the home branch, enforces the commit retry budget, and wires
// the exhaustion handler on both scheduler paths.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func TestServeWiring_PrimaryClientHomeBranchAndRetryBudget(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	ctx := context.Background()
	repoPath := testutil.NewTempRepo(t)
	base := git.PushAuthOpts()
	baseLen := len(base)
	// serveCmd's own construction (review round 1: the test used to
	// rebuild the expression instead of exercising it).
	gitClient := buildPrimaryGitClient(repoPath, base)
	if got := gitClient.HomeBranch(); got != engine.AuthoritativeBranch {
		t.Fatalf("primary client home branch = %q, want %q", got, engine.AuthoritativeBranch)
	}
	if len(base) != baseLen {
		t.Fatal("PrimaryClientOptions modified the base options")
	}
	// ... and serveCmd builds its primary client only through it.
	src, err := os.ReadFile("serve_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "buildPrimaryGitClient(repoPath, gitOpts)") || strings.Contains(string(src), "git.NewCLIClient(") {
		t.Fatal("serve_cmd.go must build the workspace's primary client with buildPrimaryGitClient (home branch), not git.NewCLIClient")
	}
	codeRepo := gitpool.NewCLIClientFactory(base...)(testutil.NewTempRepo(t), gitpool.Credential{})
	if cc, ok := codeRepo.(*git.CLIClient); !ok || cc.HomeBranch() != "" {
		t.Fatalf("a code-repository client got a home branch (%T)", codeRepo)
	}

	q := queue.NewMemoryQueue(4)
	events := event.NewQueueRouter(q)
	st := newTimeoutStore()
	_, wfProvider := buildWorkflowResolver(st, gitClient)
	orch := buildOrchestrator(st, wfProvider, gitClient, events, q, nil, nil, engine.StrictStepEntryPolicy(), nil, observe.Logger(ctx))
	if orch == nil {
		t.Fatal("buildOrchestrator returned nil")
	}
	sched := buildScheduler(st, events, orch, 0)
	if maxRetries, handler := sched.CommitRetryBudget(); maxRetries != defaultCommitMaxRetries || !handler {
		t.Fatalf("buildScheduler commit retry budget = %d (handler %v), want %d with the exhaustion handler",
			maxRetries, handler, defaultCommitMaxRetries)
	}
	if defaultCommitMaxRetries != 3 {
		t.Fatalf("defaultCommitMaxRetries = %d, want 3 (owner question 1)", defaultCommitMaxRetries)
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
	ss := &workspace.ServiceSet{Store: wsStore, GitClient: gitClient, GitPool: pool, Queue: q, Events: events}
	if err := workspaceOrchestratorBuilder(ctx, ss, engine.StrictStepEntryPolicy()); err != nil {
		t.Fatalf("workspaceOrchestratorBuilder: %v", err)
	}
	if ss.CommitExhaustedFn == nil {
		t.Fatal("workspaceOrchestratorBuilder must set ss.CommitExhaustedFn")
	}
	// The run is active, not committing: the handler changes nothing.
	if abandoned, err := ss.CommitExhaustedFn(ctx, "run-1", 3); abandoned || err != nil {
		t.Fatalf("CommitExhaustedFn on an active run = %v, %v", abandoned, err)
	}
}
