package scheduler_test

// INIT-024 TASK-008 (audit P3-27, open half): the multi-workspace scheduler
// `spine serve` now runs in its shared modes — merge retries under the
// budget across workspaces (AC 3), the concurrency cap and the soft budget
// (AC 5), shutdown that drains every scan without leaking a goroutine, and
// what two replicas over one database do (Decision 3: the documented
// single-leader assumption).

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/core/domain"
)

type wsRef string

func (w wsRef) WorkspaceID() string { return string(w) }

type listResolver []string

func (r listResolver) List(context.Context) ([]scheduler.WorkspaceRef, error) {
	out := make([]scheduler.WorkspaceRef, len(r))
	for i, id := range r {
		out[i] = wsRef(id)
	}
	return out, nil
}

// countingPool hands out fixed ServiceSets and counts the references held.
type countingPool struct {
	mu   sync.Mutex
	sets map[string]scheduler.ServiceSet
	refs map[string]int
	gets atomic.Int32
}

func newCountingPool(sets map[string]scheduler.ServiceSet) *countingPool {
	return &countingPool{sets: sets, refs: map[string]int{}}
}

func (p *countingPool) Get(_ context.Context, id string) (scheduler.ServiceSet, error) {
	p.gets.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	ss, ok := p.sets[id]
	if !ok {
		return scheduler.ServiceSet{}, errors.New("no such workspace")
	}
	p.refs[id]++
	return ss, nil
}

func (p *countingPool) Release(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refs[id]--
}

func (p *countingPool) held() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for id, n := range p.refs {
		if n != 0 {
			out[id] = n
		}
	}
	return out
}

// mergeWorkspace is one workspace with one committing run whose governed
// merge fails transiently on every attempt: its retry records the attempt
// the way the engine does (one failed, transient primary outcome per
// attempt), and its exhaustion handler decides like
// AbandonExhaustedMerge and routes the run away from committing.
type mergeWorkspace struct {
	mu        sync.Mutex
	fs        *fakeStore
	retries   int
	abandoned []int // the maxRetries each abandonment was asked with
}

func newMergeWorkspace(runID string) *mergeWorkspace {
	fs := newFakeStore()
	fs.runs = []domain.Run{{RunID: runID, Status: domain.RunStatusCommitting, TraceID: "trace-" + runID + "-0123456789"}}
	// The inline merge at the end of the run's last step already failed
	// once (attempt 1); every scheduler retry is one more attempt.
	fs.outcomes = map[string][]domain.RepositoryMergeOutcome{runID: {transientOutcome(runID, 1)}}
	return &mergeWorkspace{fs: fs}
}

func (w *mergeWorkspace) set() scheduler.ServiceSet {
	return scheduler.ServiceSet{
		Store: w.fs,
		CommitRetryFn: func(_ context.Context, runID string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.retries++
			o := w.fs.outcomes[runID]
			o[0].Attempts++
			return errors.New("remote unavailable")
		},
		CommitExhaustedFn: func(_ context.Context, runID string, maxRetries int) (bool, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if domain.FirstExhaustedOutcome(w.fs.outcomes[runID], maxRetries) == nil {
				return false, nil
			}
			w.abandoned = append(w.abandoned, maxRetries)
			for i := range w.fs.runs {
				if w.fs.runs[i].RunID == runID {
					w.fs.runs[i].Status = domain.RunStatusFailed
				}
			}
			return true, nil
		},
	}
}

// AC 3: in every workspace, a committing run whose merge keeps failing
// transiently is retried CommitMaxRetries (3) times by the scheduler and
// then handed to CommitExhaustedFn, which takes it off the loop; later
// ticks do not retry it again.
func TestMultiScheduler_RetriesCommittingRun(t *testing.T) {
	a, b := newMergeWorkspace("run-a"), newMergeWorkspace("run-b")
	pool := newCountingPool(map[string]scheduler.ServiceSet{"ws-a": a.set(), "ws-b": b.set()})
	ms := scheduler.NewMultiScheduler(pool, listResolver{"ws-a", "ws-b"}, scheduler.MultiSchedulerConfig{})
	if got := ms.Config().CommitMaxRetries; got != 3 {
		t.Fatalf("CommitMaxRetries = %d, want the standalone default 3", got)
	}
	for tick := 1; tick <= 6; tick++ {
		ms.RunCommitRetry(context.Background())
		if held := pool.held(); len(held) != 0 {
			t.Fatalf("tick %d: pool references held: %v", tick, held)
		}
	}
	for name, w := range map[string]*mergeWorkspace{"ws-a": a, "ws-b": b} {
		w.mu.Lock()
		if w.retries != 3 {
			t.Errorf("%s: %d scheduler retries, want 3 (CommitMaxRetries)", name, w.retries)
		}
		if len(w.abandoned) != 1 || w.abandoned[0] != 3 {
			t.Errorf("%s: abandonments %v, want exactly one, asked with maxRetries 3", name, w.abandoned)
		}
		if st := w.fs.runs[0].Status; st != domain.RunStatusFailed {
			t.Errorf("%s: run status %s, want it routed off committing", name, st)
		}
		w.mu.Unlock()
	}
}

// AC 5: no more than MaxConcurrentWorkspaces workspace scans run at once
// (and that many do), and a workspace whose scan spends its soft budget
// leaves the rest for the next tick, which resumes where it stopped.
func TestMultiScheduler_RespectsConcurrencyAndBudget(t *testing.T) {
	t.Run("concurrency cap", func(t *testing.T) {
		var running, peak atomic.Int32
		sets := map[string]scheduler.ServiceSet{}
		var ids []string
		for i := 0; i < 9; i++ {
			id := fmt.Sprintf("ws-%d", i)
			ids = append(ids, id)
			fs := newFakeStore()
			fs.runs = []domain.Run{{RunID: "r-" + id, Status: domain.RunStatusCommitting}}
			sets[id] = scheduler.ServiceSet{Store: fs, CommitRetryFn: func(context.Context, string) error {
				n := running.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
				running.Add(-1)
				return nil
			}}
		}
		pool := newCountingPool(sets)
		ms := scheduler.NewMultiScheduler(pool, listResolver(ids), scheduler.MultiSchedulerConfig{
			MaxConcurrentWorkspaces: 3, CommitMaxRetries: -1,
		})
		ms.RunCommitRetry(context.Background())
		if p := peak.Load(); p != 3 {
			t.Fatalf("peak concurrent workspace scans = %d, want exactly MaxConcurrentWorkspaces (3)", p)
		}
		if held := pool.held(); len(held) != 0 {
			t.Fatalf("pool references held: %v", held)
		}
	})

	t.Run("soft budget leaves items for the next tick", func(t *testing.T) {
		fs := newFakeStore()
		const n = 40
		for i := 0; i < n; i++ {
			fs.runs = append(fs.runs, domain.Run{RunID: fmt.Sprintf("r-%03d", i), Status: domain.RunStatusCommitting})
		}
		var mu sync.Mutex
		var order []string
		pool := newCountingPool(map[string]scheduler.ServiceSet{"ws": {Store: fs, CommitRetryFn: func(_ context.Context, runID string) error {
			mu.Lock()
			order = append(order, runID)
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			return nil
		}}})
		ms := scheduler.NewMultiScheduler(pool, listResolver{"ws"}, scheduler.MultiSchedulerConfig{
			WorkspaceBudget: 35 * time.Millisecond, CommitMaxRetries: -1,
		})
		ms.RunCommitRetry(context.Background())
		mu.Lock()
		first := len(order)
		mu.Unlock()
		if first == 0 || first >= n {
			t.Fatalf("first tick retried %d of %d runs, want a budgeted part", first, n)
		}
		ms.RunCommitRetry(context.Background())
		mu.Lock()
		defer mu.Unlock()
		if len(order) == first {
			t.Fatal("second tick retried nothing")
		}
		if want := fmt.Sprintf("r-%03d", first); order[first] != want {
			t.Fatalf("second tick started at %s, want %s (where the first stopped)", order[first], want)
		}
	})
}

// Shutdown: Stop ends Start, Wait waits for a scan still running (and
// reports ctx's error while it does), and once every scan has ended no
// goroutine of the scheduler is left and no pool reference is held.
func TestMultiScheduler_StopDrainsWithoutGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	release := make(chan struct{})
	var blockOnce sync.Once
	slowStarted := make(chan struct{})
	sets := map[string]scheduler.ServiceSet{}
	for _, id := range []string{"ws-a", "ws-b", "ws-slow"} {
		fs := newFakeStore()
		fs.runs = []domain.Run{{RunID: "r-" + id, Status: domain.RunStatusCommitting}}
		retry := func(context.Context, string) error { return nil }
		if id == "ws-slow" {
			retry = func(context.Context, string) error {
				blockOnce.Do(func() { close(slowStarted) })
				<-release
				return nil
			}
		}
		sets[id] = scheduler.ServiceSet{Store: fs, CommitRetryFn: retry}
	}
	pool := newCountingPool(sets)
	ms := scheduler.NewMultiScheduler(pool, listResolver{"ws-a", "ws-b", "ws-slow"}, scheduler.MultiSchedulerConfig{
		TimeoutInterval: 10 * time.Millisecond, OrphanInterval: 15 * time.Millisecond,
		WorkspaceBudget: 5 * time.Millisecond, CommitMaxRetries: -1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startReturned := make(chan struct{})
	go func() {
		ms.Start(ctx)
		close(startReturned)
	}()
	select {
	case <-slowStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the slow workspace's retry never started")
	}
	ms.Stop()
	ms.Stop() // idempotent
	select {
	case <-startReturned:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
	short, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := ms.Wait(short)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait with a scan still running = %v, want the context's deadline error", err)
	}
	gets := pool.gets.Load()
	close(release)
	long, cancelLong := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelLong()
	if err := ms.Wait(long); err != nil {
		t.Fatalf("Wait after the last scan ended = %v", err)
	}
	if held := pool.held(); len(held) != 0 {
		t.Fatalf("pool references held after the drain: %v", held)
	}
	time.Sleep(50 * time.Millisecond)
	if pool.gets.Load() != gets {
		t.Fatal("a workspace was scanned after Stop")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines after the drain = %d, before the scheduler = %d:\n%s",
				runtime.NumGoroutine(), baseline, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// casTimeoutStore is a fakeStore whose run-timeout cancel is the
// compare-and-swap PostgresStore has, safe for two schedulers at once.
type casTimeoutStore struct {
	*fakeStore
	mu sync.Mutex
}

func (c *casTimeoutStore) ListActiveStepExecutions(context.Context) ([]domain.StepExecution, error) {
	return nil, nil
}

func (c *casTimeoutStore) ListTimedOutRuns(ctx context.Context, now time.Time) ([]domain.Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeStore.ListTimedOutRuns(ctx, now)
}

func (c *casTimeoutStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := c.fakeStore.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	cp := *r
	return &cp, nil
}

func (c *casTimeoutStore) ListRunsByStatus(ctx context.Context, st domain.RunStatus) ([]domain.Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeStore.ListRunsByStatus(ctx, st)
}

func (c *casTimeoutStore) TransitionRunStatusAt(_ context.Context, runID string, from, to domain.RunStatus, at time.Time) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.runs {
		if c.runs[i].RunID == runID {
			if c.runs[i].Status != from {
				return false, nil
			}
			c.runs[i].Status = to
			c.runs[i].CompletedAt = &at
			return true, nil
		}
	}
	return false, nil
}

// Decision 3 (no multi-replica leadership; KNOWN-LIMITATIONS): two
// replicas over one workspace database both scan it. The run-timeout
// cancel is a compare-and-swap, so a run is cancelled, and its live steps
// terminated, once; a committing run's merge retry is attempted by each
// replica (the engine's own guards decide the outcome) — the documented
// single-leader assumption, pinned here.
func TestMultiScheduler_TwoReplicasOverOneDatabase(t *testing.T) {
	fs := newFakeStore()
	past := time.Now().Add(-time.Hour)
	fs.runs = []domain.Run{
		{RunID: "run-t", Status: domain.RunStatusActive, TraceID: "trace-run-t-0123456789", TimeoutAt: &past},
		{RunID: "run-c", Status: domain.RunStatusCommitting, TraceID: "trace-run-c-0123456789"},
	}
	st := &casTimeoutStore{fakeStore: fs}
	var terminated, retried atomic.Int32
	set := scheduler.ServiceSet{
		Store:           st,
		RunTerminatedFn: func(context.Context, string, string) { terminated.Add(1) },
		CommitRetryFn:   func(context.Context, string) error { retried.Add(1); return nil },
	}
	replicas := []*scheduler.MultiScheduler{
		scheduler.NewMultiScheduler(newCountingPool(map[string]scheduler.ServiceSet{"ws": set}), listResolver{"ws"}, scheduler.MultiSchedulerConfig{CommitMaxRetries: -1}),
		scheduler.NewMultiScheduler(newCountingPool(map[string]scheduler.ServiceSet{"ws": set}), listResolver{"ws"}, scheduler.MultiSchedulerConfig{CommitMaxRetries: -1}),
	}
	var wg sync.WaitGroup
	for _, ms := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ms.ScanTimeouts(context.Background())
			ms.RunCommitRetry(context.Background())
		}()
	}
	wg.Wait()
	if got, _ := st.GetRun(context.Background(), "run-t"); got.Status != domain.RunStatusCancelled {
		t.Fatalf("timed-out run is %s, want cancelled", got.Status)
	}
	if n := terminated.Load(); n != 1 {
		t.Fatalf("live steps terminated %d times across two replicas, want 1 (the cancel is a compare-and-swap)", n)
	}
	if n := retried.Load(); n != 2 {
		t.Fatalf("merge retries across two replicas = %d; the documented single-leader limitation is that each replica retries (2)", n)
	}
}

// The effective configuration reports the defaults NewMultiScheduler
// applies, including the new TickWait (WorkspaceBudget + TimeoutInterval).
func TestMultiScheduler_ConfigDefaults(t *testing.T) {
	got := scheduler.NewMultiScheduler(newCountingPool(nil), listResolver{}, scheduler.MultiSchedulerConfig{}).Config()
	want := scheduler.MultiSchedulerConfig{
		TimeoutInterval: 30 * time.Second, OrphanInterval: 60 * time.Second, OrphanThreshold: 30 * 24 * time.Hour,
		CommitMaxRetries: 3, WorkspaceBudget: 30 * time.Second, MaxConcurrentWorkspaces: 4, TickWait: 60 * time.Second,
	}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	got = scheduler.NewMultiScheduler(newCountingPool(nil), listResolver{}, scheduler.MultiSchedulerConfig{
		WorkspaceBudget: 5 * time.Second, TickWait: 7 * time.Second, MaxConcurrentWorkspaces: 2,
	}).Config()
	if got.TickWait != 7*time.Second || got.WorkspaceBudget != 5*time.Second || got.MaxConcurrentWorkspaces != 2 {
		t.Fatalf("explicit values not kept: %+v", got)
	}
}
