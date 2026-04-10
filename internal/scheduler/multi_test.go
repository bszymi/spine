package scheduler_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/scheduler"
	"github.com/bszymi/spine/internal/workspace"
)

// multiTestStore extends fakeStore with committing run support.
type multiTestStore struct {
	fakeStore
}

// multiTestResolver returns a fixed set of workspace configs.
type multiTestResolver struct {
	workspaces []workspace.Config
}

func (r *multiTestResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	for i := range r.workspaces {
		if r.workspaces[i].ID == id {
			return &r.workspaces[i], nil
		}
	}
	return nil, workspace.ErrWorkspaceNotFound
}

func (r *multiTestResolver) List(_ context.Context) ([]workspace.Config, error) {
	return r.workspaces, nil
}

func TestMultiScheduler_CommitRetryTicker(t *testing.T) {
	// Set up a workspace with a committing run and a commit retry callback.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &multiTestStore{}
	fs.runs = []domain.Run{
		{
			RunID:  "run-1",
			Status: domain.RunStatusCommitting,
		},
	}

	var retryCount atomic.Int32

	q := queue.NewMemoryQueue(10)
	go q.Start(ctx)
	defer q.Stop()

	ev := event.NewQueueRouter(q)

	resolver := &multiTestResolver{
		workspaces: []workspace.Config{
			{ID: "ws-1", RepoPath: "."},
		},
	}

	pool := workspace.NewServicePool(ctx, resolver, workspace.PoolConfig{
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			ss.Store = fs
			ss.Events = ev
			ss.CommitRetryFn = func(_ context.Context, runID string) error {
				retryCount.Add(1)
				return nil
			}
			return nil
		},
	})
	defer pool.Close()

	ms := scheduler.NewMultiScheduler(pool, resolver, scheduler.MultiSchedulerConfig{
		TimeoutInterval: 1 * time.Hour, // won't fire during test
		OrphanInterval:  50 * time.Millisecond,
		OrphanThreshold: 30 * 24 * time.Hour,
	})

	go ms.Start(ctx)
	defer ms.Stop()

	// Wait for commit retry to fire (orphan interval = 50ms, so commit ticker also 50ms).
	time.Sleep(200 * time.Millisecond)

	if retryCount.Load() == 0 {
		t.Error("expected commit retry callback to be called at least once")
	}
}

func TestMultiScheduler_RunFailCallback_Wired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &multiTestStore{}
	now := time.Now()
	staleTime := now.Add(-365 * 24 * time.Hour) // very stale
	fs.runs = []domain.Run{
		{
			RunID:     "orphan-1",
			Status:    domain.RunStatusActive,
			CreatedAt: staleTime,
		},
	}
	fs.updatedRuns = make(map[string]domain.RunStatus)

	var failCalled atomic.Int32

	q := queue.NewMemoryQueue(10)
	go q.Start(ctx)
	defer q.Stop()

	ev := event.NewQueueRouter(q)

	resolver := &multiTestResolver{
		workspaces: []workspace.Config{
			{ID: "ws-1", RepoPath: "."},
		},
	}

	pool := workspace.NewServicePool(ctx, resolver, workspace.PoolConfig{
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			ss.Store = fs
			ss.Events = ev
			ss.RunFailFn = func(_ context.Context, runID, reason string) error {
				failCalled.Add(1)
				return nil
			}
			return nil
		},
	})
	defer pool.Close()

	ms := scheduler.NewMultiScheduler(pool, resolver, scheduler.MultiSchedulerConfig{
		TimeoutInterval: 1 * time.Hour,
		OrphanInterval:  50 * time.Millisecond,
		OrphanThreshold: 1 * time.Millisecond, // everything is orphaned
	})

	go ms.Start(ctx)
	defer ms.Stop()

	time.Sleep(200 * time.Millisecond)

	if failCalled.Load() == 0 {
		t.Error("expected runFail callback to be called for orphaned run")
	}
}

func TestMultiScheduler_NoCallbacks_NoError(t *testing.T) {
	// Verify the scheduler doesn't panic when callbacks are nil.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &multiTestStore{}
	fs.runs = []domain.Run{
		{
			RunID:     "run-no-cb",
			Status:    domain.RunStatusCommitting,
			CreatedAt: time.Now(),
		},
	}

	q := queue.NewMemoryQueue(10)
	go q.Start(ctx)
	defer q.Stop()

	ev := event.NewQueueRouter(q)

	resolver := &multiTestResolver{
		workspaces: []workspace.Config{
			{ID: "ws-1", RepoPath: "."},
		},
	}

	pool := workspace.NewServicePool(ctx, resolver, workspace.PoolConfig{
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			ss.Store = fs
			ss.Events = ev
			// No callbacks set — should not panic.
			return nil
		},
	})
	defer pool.Close()

	ms := scheduler.NewMultiScheduler(pool, resolver, scheduler.MultiSchedulerConfig{
		TimeoutInterval: 1 * time.Hour,
		OrphanInterval:  50 * time.Millisecond,
		OrphanThreshold: 30 * 24 * time.Hour,
	})

	go ms.Start(ctx)
	defer ms.Stop()

	// Just verify no panic occurs.
	time.Sleep(150 * time.Millisecond)
}
