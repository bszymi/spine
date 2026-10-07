package main

// INIT-023 wave 2 integration: TASK-009 made sharedModePoolConfig the one
// ServicePool constructor of both shared resolvers (db and
// platform-binding), and TASK-007 threads the step-entry policy into the
// per-workspace builder. The two meet in that constructor, so this pins
// that the pool's Builder installs the configured policy on every pooled
// workspace's orchestrator: a builder built with a zero policy would
// compile and leave shared-mode workspaces on the legacy (unchecked)
// assign and claim paths while the process-level orchestrator is strict.

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func TestSharedModePoolConfig_BuilderInstallsStepEntryPolicy(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	for _, tc := range []struct {
		name   string
		policy engine.StepEntryPolicy
	}{
		{"strict (serve default)", engine.StrictStepEntryPolicy()},
		{"single switch", engine.StepEntryPolicy{EnforceRunStatus: true}},
		{"legacy (zero value)", engine.StepEntryPolicy{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := sharedModePoolConfig(workspaceDeliveryConfig{}, slog.New(slog.DiscardHandler), nil, "", nil, "", tc.policy)
			if cfg.Builder == nil {
				t.Fatal("shared-mode pool config has no builder")
			}

			repoPath := testutil.NewTempRepo(t)
			gitClient := git.NewCLIClient(repoPath)
			q := queue.NewMemoryQueue(4)
			wsStore := newTimeoutStore()
			repoSpec := repository.PrimarySpec{LocalPath: repoPath}
			reg := repository.New("", repoSpec, func(context.Context) (*repository.Catalog, error) {
				return repository.ParseCatalog(nil, repoSpec)
			}, wsStore)
			pool, err := gitpool.New(gitClient, reg, gitpool.NewCLIClientFactory())
			if err != nil {
				t.Fatal(err)
			}
			ss := &workspace.ServiceSet{
				Config:    workspace.Config{ID: "ws-wave2"},
				Store:     wsStore,
				GitClient: gitClient,
				GitPool:   pool,
				Queue:     q,
				Events:    event.NewQueueRouter(q),
			}
			if err := cfg.Builder(ctx, ss); err != nil {
				t.Fatalf("pool builder: %v", err)
			}
			orch, ok := ss.StepAssigner.(*engine.Orchestrator)
			if !ok {
				t.Fatalf("ss.StepAssigner = %T, want *engine.Orchestrator", ss.StepAssigner)
			}
			if got := orch.StepEntryPolicy(); !reflect.DeepEqual(got, tc.policy) {
				t.Fatalf("pooled workspace orchestrator policy = %+v, want %+v", got, tc.policy)
			}
			if ss.RunTerminatedFn == nil {
				t.Fatal("pooled builder must set ss.RunTerminatedFn")
			}
		})
	}
}
