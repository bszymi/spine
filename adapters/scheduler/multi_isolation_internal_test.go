package scheduler

// INIT-024 TASK-008 (audit P3-27, adversarial review): in `spine serve`'s
// shared modes one MultiScheduler scans every tenant, so one tenant must not
// be able to stop the others' timeouts. These tests use only API that
// predates the task (NewMultiScheduler, Start, Stop, forEachWorkspace), so
// the regression ones run unchanged against the scaffold base, where they
// fail: a hung scan blocked the shared loop for good, and a panicking scan
// ended the process.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// isoStore is a concurrency-safe stub of the store reads and writes the
// four scans make, with a compare-and-swap run-timeout cancel like
// PostgresStore's.
type isoStore struct {
	store.Store

	mu   sync.Mutex
	runs []domain.Run
	// gate, when set, hides every timed-out run until it is closed.
	gate <-chan struct{}
	// panicOnScan makes the step-timeout listing panic.
	panicOnScan bool
	// blockScan, when set, holds every step-timeout listing until closed.
	blockScan <-chan struct{}
	// blockUntilDone holds every step-timeout listing until its context
	// ends, like a git command waiting on a remote that never answers.
	blockUntilDone bool
	scans          atomic.Int32
	active         atomic.Int32 // step-timeout listings in progress
	peak           atomic.Int32
}

func (s *isoStore) ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error) {
	s.scans.Add(1)
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if s.panicOnScan {
		panic("isoStore: a scan of this workspace's data panicked")
	}
	if s.blockScan != nil {
		<-s.blockScan
	}
	if s.blockUntilDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, nil
}

func (s *isoStore) ListTimedOutRuns(_ context.Context, now time.Time) ([]domain.Run, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		default:
			return nil, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Run
	for i := range s.runs {
		r := s.runs[i]
		if r.Status == domain.RunStatusActive && r.TimeoutAt != nil && !r.TimeoutAt.After(now) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *isoStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runs {
		if s.runs[i].RunID == runID {
			r := s.runs[i]
			return &r, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "run not found")
}

func (s *isoStore) TransitionRunStatusAt(_ context.Context, runID string, from, to domain.RunStatus, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runs {
		if s.runs[i].RunID != runID {
			continue
		}
		if s.runs[i].Status != from {
			return false, nil
		}
		s.runs[i].Status = to
		s.runs[i].CompletedAt = &at
		return true, nil
	}
	return false, domain.NewError(domain.ErrNotFound, "run not found")
}

func (s *isoStore) ListStaleActiveRuns(context.Context, time.Time) ([]domain.Run, error) {
	return nil, nil
}

func (s *isoStore) ListRunsByStatus(_ context.Context, status domain.RunStatus) ([]domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Run
	for i := range s.runs {
		if s.runs[i].Status == status {
			out = append(out, s.runs[i])
		}
	}
	return out, nil
}

func (s *isoStore) ListRepositoryMergeOutcomes(context.Context, string) ([]domain.RepositoryMergeOutcome, error) {
	return nil, nil
}

func (s *isoStore) status(runID string) domain.RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.runs {
		if s.runs[i].RunID == runID {
			return s.runs[i].Status
		}
	}
	return ""
}

func timedOutRun(id string) domain.Run {
	past := time.Now().Add(-time.Hour)
	return domain.Run{RunID: id, TaskPath: "tasks/" + id + ".md", Status: domain.RunStatusActive,
		TraceID: "trace-" + id + "-0123456789", TimeoutAt: &past}
}

// timeoutTick is the closure Start runs on every timeout tick.
func timeoutTick(ctx context.Context, sched *Scheduler) {
	_ = sched.ScanTimeouts(ctx)
	_ = sched.ScanRunTimeouts(ctx)
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Regression (red at the scaffold base): workspace a-hung's merge retry
// never returns (a code repository's push has no deadline; a remote that
// accepts the connection and never answers holds it). At the base the
// commit tick waited for it, and with it the one loop every tenant's scans
// run on: b-victim's run, which times out after the hang began, was never
// cancelled. Now the tick stops waiting after TickWait, later ticks skip
// a-hung while its retry runs, and b-victim's timeout fires. a-hung's retry
// is never entered a second time while the first is running.
func TestMultiScheduler_HungWorkspaceDoesNotStarveOthers(t *testing.T) {
	hungStarted := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	var entered, running, maxRunning atomic.Int32
	hungRetry := func(context.Context, string) error {
		entered.Add(1)
		if n := running.Add(1); n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		defer running.Add(-1)
		startOnce.Do(func() { close(hungStarted) })
		<-release
		return nil
	}
	hung := &isoStore{runs: []domain.Run{{RunID: "stuck", Status: domain.RunStatusCommitting}}}
	victim := &isoStore{gate: hungStarted, runs: []domain.Run{timedOutRun("victim-run")}}
	pool := &msPool{sets: map[string]ServiceSet{
		"a-hung":   {Store: hung, CommitRetryFn: hungRetry},
		"b-victim": {Store: victim},
	}}
	ms := NewMultiScheduler(pool, msResolver{ids: []string{"a-hung", "b-victim"}}, MultiSchedulerConfig{
		TimeoutInterval: 20 * time.Millisecond,
		OrphanInterval:  30 * time.Millisecond,
		WorkspaceBudget: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ms.Start(ctx)
	t.Cleanup(ms.Stop)

	select {
	case <-hungStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("a-hung's merge retry never started")
	}
	deadline := time.Now().Add(10 * time.Second)
	for victim.status("victim-run") != domain.RunStatusCancelled {
		if time.Now().After(deadline) {
			t.Fatalf("b-victim's timed-out run is still %q 10s after a-hung's merge retry hung: one tenant starved the others' timeouts",
				victim.status("victim-run"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Several commit ticks have passed; a-hung's retry was never re-entered.
	time.Sleep(100 * time.Millisecond)
	if n := entered.Load(); n != 1 {
		t.Fatalf("a-hung's merge retry entered %d times while the first was still running, want 1", n)
	}
	if n := maxRunning.Load(); n != 1 {
		t.Fatalf("a-hung ran %d retries at once, want 1", n)
	}
}

// Regression (red at the scaffold base, where the panic ended the test
// binary): a panic in one workspace's scan is recovered and logged with
// the workspace and a stack; the other workspace's timeout still fires,
// the panicking workspace's pool reference is released, and the next tick
// scans it again.
func TestMultiScheduler_PanickingWorkspaceIsIsolated(t *testing.T) {
	logs := captureLogs(t)
	boom := &isoStore{panicOnScan: true}
	victim := &isoStore{runs: []domain.Run{timedOutRun("victim-run")}}
	pool := &msPool{sets: map[string]ServiceSet{
		"a-boom":   {Store: boom},
		"b-victim": {Store: victim},
	}}
	ms := NewMultiScheduler(pool, msResolver{ids: []string{"a-boom", "b-victim"}}, MultiSchedulerConfig{})
	ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)

	if got := victim.status("victim-run"); got != domain.RunStatusCancelled {
		t.Fatalf("b-victim's run is %q after the tick, want cancelled", got)
	}
	pool.mu.Lock()
	released := pool.released["a-boom"]
	pool.mu.Unlock()
	if released != 1 {
		t.Fatalf("a-boom's pool reference released %d times, want 1 (also on a panic)", released)
	}
	out := logs.String()
	for _, want := range []string{"workspace scan panicked", "workspace_id=a-boom", "scan=timeout-scan", "isoStore: a scan of this workspace", "goroutine "} {
		if !strings.Contains(out, want) {
			t.Fatalf("the recovered panic's Error lacks %q:\n%s", want, out)
		}
	}
	ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
	if n := boom.scans.Load(); n != 2 {
		t.Fatalf("a-boom scanned %d times over two ticks, want 2 (a panic does not take it out of the rotation)", n)
	}
}

// Regression (red at the scaffold base, which scanned a workspace listed
// twice twice at once): one scan per workspace at a time, whatever the
// resolver returns; the duplicate is skipped with a Warn.
func TestMultiScheduler_OneScanPerWorkspace(t *testing.T) {
	logs := captureLogs(t)
	release := make(chan struct{})
	st := &isoStore{blockScan: release}
	pool := &msPool{sets: map[string]ServiceSet{"ws": {Store: st}}}
	ms := NewMultiScheduler(pool, msResolver{ids: []string{"ws", "ws"}}, MultiSchedulerConfig{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for st.scans.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // give a second scan the chance to start
	close(release)
	<-done
	if n := st.peak.Load(); n != 1 {
		t.Fatalf("%d scans of one workspace ran at once, want 1", n)
	}
	if n := st.scans.Load(); n != 1 {
		t.Fatalf("a workspace listed twice was scanned %d times in one tick, want 1", n)
	}
	if !strings.Contains(logs.String(), "previous scan of this workspace is still running; skipped this tick") {
		t.Fatalf("no Warn for the skipped duplicate:\n%s", logs.String())
	}
}

// dynResolver lists a set of workspaces that changes at runtime.
type dynResolver struct {
	mu  sync.Mutex
	ids []string
}

func (r *dynResolver) set(ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = ids
}

func (r *dynResolver) List(ctx context.Context) ([]WorkspaceRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return msResolver{ids: append([]string(nil), r.ids...)}.List(ctx)
}

// refPool counts the references each workspace holds and refuses the
// inactive ones the way the ServicePool does (its resolver refuses them
// before any reference is taken).
type refPool struct {
	mu       sync.Mutex
	sets     map[string]ServiceSet
	inactive map[string]bool
	refs     map[string]int
	gets     map[string]int
}

func (p *refPool) Get(_ context.Context, id string) (ServiceSet, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gets == nil {
		p.gets, p.refs = map[string]int{}, map[string]int{}
	}
	p.gets[id]++
	if p.inactive[id] {
		return ServiceSet{}, domain.NewError(domain.ErrNotFound, "workspace inactive")
	}
	ss, ok := p.sets[id]
	if !ok {
		return ServiceSet{}, domain.NewError(domain.ErrNotFound, "workspace not found")
	}
	p.refs[id]++
	return ss, nil
}

func (p *refPool) Release(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refs[id]--
}

func (p *refPool) setInactive(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inactive == nil {
		p.inactive = map[string]bool{}
	}
	p.inactive[id] = true
}

func (p *refPool) held() map[string]int {
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

// Workspaces added, removed and deactivated at runtime: the next tick scans
// exactly the active listed ones; a deactivated one still listed (a list
// cache that has not expired yet) is refused by the pool and never
// scanned; no tick leaves a pool reference behind.
func TestMultiScheduler_WorkspacesChangeAtRuntime(t *testing.T) {
	stores := map[string]*isoStore{}
	sets := map[string]ServiceSet{}
	for _, id := range []string{"ws-a", "ws-b", "ws-c"} {
		stores[id] = &isoStore{}
		sets[id] = ServiceSet{Store: stores[id]}
	}
	pool := &refPool{sets: sets}
	res := &dynResolver{}
	res.set("ws-a", "ws-b")
	ms := NewMultiScheduler(pool, res, MultiSchedulerConfig{})
	tick := func() map[string]int32 {
		before := map[string]int32{}
		for id, st := range stores {
			before[id] = st.scans.Load()
		}
		ms.forEachWorkspace(context.Background(), "timeout-scan", timeoutTick)
		got := map[string]int32{}
		for id, st := range stores {
			if d := st.scans.Load() - before[id]; d != 0 {
				got[id] = d
			}
		}
		if held := pool.held(); len(held) != 0 {
			t.Fatalf("pool references held after the tick: %v", held)
		}
		return got
	}
	if got := tick(); len(got) != 2 || got["ws-a"] != 1 || got["ws-b"] != 1 {
		t.Fatalf("tick 1 scanned %v, want ws-a and ws-b once", got)
	}
	// ws-c is added, ws-a removed, ws-b deactivated but still listed.
	res.set("ws-b", "ws-c")
	pool.setInactive("ws-b")
	if got := tick(); len(got) != 1 || got["ws-c"] != 1 {
		t.Fatalf("tick 2 scanned %v, want only ws-c", got)
	}
	// Once the list drops ws-b it is not even asked for.
	res.set("ws-c")
	pool.mu.Lock()
	getsB := pool.gets["ws-b"]
	pool.mu.Unlock()
	if got := tick(); len(got) != 1 || got["ws-c"] != 1 {
		t.Fatalf("tick 3 scanned %v, want only ws-c", got)
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.gets["ws-b"] != getsB {
		t.Fatal("a workspace no longer listed was still asked for")
	}
	if pool.gets["ws-a"] != 1 {
		t.Fatalf("removed ws-a was asked for %d times, want 1 (tick 1 only)", pool.gets["ws-a"])
	}
}
