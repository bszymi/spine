package scheduler

// INIT-023 TASK-012, audit P3-27 (AC 17): the multi-workspace scheduler
// scans workspaces concurrently but bounded, under a soft per-workspace
// budget that never cancels the context, and hands every per-workspace
// Scheduler the commit retry budget.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

type msWorkspace string

func (w msWorkspace) WorkspaceID() string { return string(w) }

type msResolver struct{ ids []string }

func (r msResolver) List(context.Context) ([]WorkspaceRef, error) {
	out := make([]WorkspaceRef, len(r.ids))
	for i, id := range r.ids {
		out[i] = msWorkspace(id)
	}
	return out, nil
}

// msStore lists committing runs; every other method is unused here.
type msStore struct {
	store.Store
	runs []domain.Run
}

func (s *msStore) ListRunsByStatus(_ context.Context, status domain.RunStatus) ([]domain.Run, error) {
	var out []domain.Run
	for i := range s.runs {
		if s.runs[i].Status == status {
			out = append(out, s.runs[i])
		}
	}
	return out, nil
}

func (s *msStore) ListRepositoryMergeOutcomes(context.Context, string) ([]domain.RepositoryMergeOutcome, error) {
	return nil, nil
}

type msPool struct {
	mu       sync.Mutex
	sets     map[string]ServiceSet
	released map[string]int
}

func (p *msPool) Get(_ context.Context, id string) (ServiceSet, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sets[id], nil
}

func (p *msPool) Release(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.released == nil {
		p.released = map[string]int{}
	}
	p.released[id]++
}

func TestMultiScheduler_BoundedConcurrency(t *testing.T) {
	var ids []string
	pool := &msPool{sets: map[string]ServiceSet{}}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("ws-%d", i)
		ids = append(ids, id)
		pool.sets[id] = ServiceSet{Store: &msStore{}}
	}
	ms := NewMultiScheduler(pool, msResolver{ids: ids}, MultiSchedulerConfig{MaxConcurrentWorkspaces: 4})
	var running, peak, finished atomic.Int32
	barrier := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ms.forEachWorkspace(context.Background(), "test", func(context.Context, *Scheduler) {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-barrier
			running.Add(-1)
			finished.Add(1)
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // give a fifth one the chance to start
	if got := running.Load(); got != 4 {
		t.Fatalf("running workspaces = %d, want exactly MaxConcurrentWorkspaces (4)", got)
	}
	select {
	case <-done:
		t.Fatal("forEachWorkspace returned while workspaces were still running")
	default:
	}
	close(barrier)
	<-done
	if f := finished.Load(); f != 10 {
		t.Fatalf("forEachWorkspace returned after %d of 10 workspaces", f)
	}
	if p := peak.Load(); p > 4 {
		t.Fatalf("observed %d workspaces at once, want at most 4", p)
	}
	for _, id := range ids {
		if pool.released[id] != 1 {
			t.Fatalf("workspace %s released %d times, want 1", id, pool.released[id])
		}
	}
}

func TestMultiScheduler_SoftBudgetStopsBetweenItemsWithoutCancelling(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st := &msStore{}
	for i := 0; i < 100; i++ {
		st.runs = append(st.runs, domain.Run{RunID: fmt.Sprintf("r-%03d", i), Status: domain.RunStatusCommitting})
	}
	var calls atomic.Int32
	var cancelled atomic.Bool
	retry := func(ctx context.Context, _ string) error {
		if ctx.Err() != nil {
			cancelled.Store(true)
		}
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		if ctx.Err() != nil {
			cancelled.Store(true)
		}
		return nil
	}
	pool := &msPool{sets: map[string]ServiceSet{"ws": {Store: st, CommitRetryFn: retry}}}
	ms := NewMultiScheduler(pool, msResolver{ids: []string{"ws"}}, MultiSchedulerConfig{WorkspaceBudget: 50 * time.Millisecond})
	ms.forEachWorkspace(context.Background(), "commit-retry", func(ctx context.Context, sched *Scheduler) {
		sched.RunCommitRetry(ctx)
	})
	if n := calls.Load(); n >= 100 || n == 0 {
		t.Fatalf("commit retries in one budgeted tick = %d, want some but fewer than 100", n)
	}
	if cancelled.Load() {
		t.Fatal("the soft budget cancelled the context the retry ran with")
	}
	logs := buf.String()
	if !strings.Contains(logs, "workspace scan budget spent") || !strings.Contains(logs, "remaining=") ||
		strings.Contains(logs, "remaining=0 ") || !strings.Contains(logs, "workspace_id=ws") {
		t.Fatalf("no budget Warn naming the workspace and a positive remainder:\n%s", logs)
	}
}

func TestMultiScheduler_PerWorkspaceSchedulerGetsTheRetryBudget(t *testing.T) {
	var asked atomic.Int32
	exhausted := func(context.Context, string, int) (bool, error) { asked.Add(1); return false, nil }
	pool := &msPool{sets: map[string]ServiceSet{"ws": {
		Store:             &msStore{},
		CommitRetryFn:     func(context.Context, string) error { return nil },
		CommitExhaustedFn: exhausted,
	}}}
	for _, tc := range []struct {
		cfg  MultiSchedulerConfig
		want int
	}{
		{MultiSchedulerConfig{}, DefaultCommitMaxRetries},
		{MultiSchedulerConfig{CommitMaxRetries: 5}, 5},
		{MultiSchedulerConfig{CommitMaxRetries: -1}, 0},
	} {
		ms := NewMultiScheduler(pool, msResolver{ids: []string{"ws"}}, tc.cfg)
		var got int
		var fn CommitExhaustedFunc
		ms.forEachWorkspace(context.Background(), "test", func(_ context.Context, sched *Scheduler) {
			got = sched.commitMaxRetries
			fn = sched.commitExhaustedFn
		})
		if got != tc.want {
			t.Fatalf("cfg %+v: per-workspace commitMaxRetries = %d, want %d", tc.cfg, got, tc.want)
		}
		if fn == nil {
			t.Fatalf("cfg %+v: the per-workspace scheduler has no exhaustion handler", tc.cfg)
		}
		before := asked.Load()
		_, _ = fn(context.Background(), "r", 3)
		if asked.Load() != before+1 {
			t.Fatal("the per-workspace exhaustion handler is not the ServiceSet's")
		}
	}
	if DefaultCommitMaxRetries != 3 {
		t.Fatalf("DefaultCommitMaxRetries = %d, want 3 (the standalone value)", DefaultCommitMaxRetries)
	}
}

// Codex review round 1: a scan cut short by the budget resumes where it
// stopped on the next tick, so the tail of a long list is not starved by
// a prefix that keeps using the whole budget.
func TestMultiScheduler_BudgetedScanResumesWhereItStopped(t *testing.T) {
	st := &msStore{}
	const n = 30
	for i := 0; i < n; i++ {
		st.runs = append(st.runs, domain.Run{RunID: fmt.Sprintf("r-%03d", i), Status: domain.RunStatusCommitting})
	}
	var mu sync.Mutex
	var order []string
	retry := func(_ context.Context, runID string) error {
		mu.Lock()
		order = append(order, runID)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return nil
	}
	pool := &msPool{sets: map[string]ServiceSet{"ws": {Store: st, CommitRetryFn: retry}}}
	ms := NewMultiScheduler(pool, msResolver{ids: []string{"ws"}}, MultiSchedulerConfig{WorkspaceBudget: 35 * time.Millisecond})
	tick := func() []string {
		mu.Lock()
		before := len(order)
		mu.Unlock()
		ms.forEachWorkspace(context.Background(), "commit-retry", func(ctx context.Context, sched *Scheduler) {
			sched.RunCommitRetry(ctx)
		})
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), order[before:]...)
	}
	first := tick()
	if len(first) == 0 || len(first) >= n {
		t.Fatalf("first tick processed %d of %d, want a budgeted part", len(first), n)
	}
	second := tick()
	if len(second) == 0 {
		t.Fatal("second tick processed nothing")
	}
	wantStart := fmt.Sprintf("r-%03d", len(first))
	if second[0] != wantStart {
		t.Fatalf("second tick started at %s, want %s (where the first stopped)", second[0], wantStart)
	}
	seen := map[string]bool{}
	for _, id := range append(first, second...) {
		seen[id] = true
	}
	for k := 0; k < 20 && len(seen) < n; k++ {
		for _, id := range tick() {
			seen[id] = true
		}
	}
	if len(seen) != n {
		t.Fatalf("after repeated budgeted ticks %d of %d runs were ever retried", len(seen), n)
	}
}

// Without a budget (single-workspace mode) the scan visits every item in
// order, as before.
func TestBudgetIter_NoBudgetIsThePlainLoop(t *testing.T) {
	var got []int
	for it := newBudgetIter(context.Background(), "x", 5); it.next(); {
		got = append(got, it.i)
	}
	if fmt.Sprint(got) != "[0 1 2 3 4]" {
		t.Fatalf("visited %v, want [0 1 2 3 4]", got)
	}
	for it := newBudgetIter(context.Background(), "x", 0); it.next(); {
		t.Fatal("visited an item of an empty list")
	}
}
