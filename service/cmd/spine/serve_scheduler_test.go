package main

// INIT-024 TASK-008 (audit P3-27, open half): shared-mode `spine serve`
// builds, starts and drains the multi-workspace scheduler over the
// workspace pool; single mode keeps the single-workspace scheduler.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

// multiResolver resolves a fixed set of workspaces; List returns the
// active ones in id order, as the db provider does.
type multiResolver struct {
	mu   sync.Mutex
	cfgs map[string]workspace.Config
	// canonical maps a requested id to the config Resolve returns
	// (a resolver that canonicalises ids).
	canonical map[string]string
	list      []workspace.Config // when set, List returns exactly this
}

func newMultiResolver(t *testing.T, ids ...string) *multiResolver {
	r := &multiResolver{cfgs: map[string]workspace.Config{}}
	for _, id := range ids {
		r.cfgs[id] = workspace.Config{ID: id, RepoPath: testutil.NewTempRepo(t), Status: workspace.StatusActive}
	}
	return r
}

func (r *multiResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.canonical[id]; ok {
		id = c
	}
	cfg, ok := r.cfgs[id]
	if !ok {
		return nil, workspace.ErrWorkspaceNotFound
	}
	if cfg.Status != workspace.StatusActive {
		return nil, workspace.ErrWorkspaceInactive
	}
	return &cfg, nil
}

func (r *multiResolver) List(context.Context) ([]workspace.Config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.list != nil {
		return append([]workspace.Config(nil), r.list...), nil
	}
	var out []workspace.Config
	for _, c := range r.cfgs {
		if c.Status == workspace.StatusActive {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// timeoutPool is a ServicePool over multiResolver whose workspaces each
// get their own timeoutStore (one active run past its timeout, with two
// live executions) and the real pooled builder — the per-workspace
// orchestrator and the scheduler callbacks spine serve installs — under
// the strict step-entry policy.
func timeoutPool(t *testing.T, res *multiResolver) (*workspace.ServicePool, map[string]*timeoutStore) {
	t.Helper()
	stores := map[string]*timeoutStore{}
	byID := map[string]store.Store{}
	for id := range res.cfgs {
		stores[id] = newTimeoutStore()
		byID[id] = stores[id]
	}
	return pooledStores(t, res, byID), stores
}

// pooledStores is a ServicePool over res that gives each workspace the
// store in stores and then runs the real pooled builder.
func pooledStores(t *testing.T, res workspace.Resolver, stores map[string]store.Store) *workspace.ServicePool {
	t.Helper()
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	pooled := newPooledWorkspaceBuilder(workspaceDeliveryConfig{}, engine.StrictStepEntryPolicy(), slog.New(slog.DiscardHandler))
	pool := workspace.NewServicePool(context.Background(), res, workspace.PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(ctx context.Context, ss *workspace.ServiceSet) error {
			st, ok := stores[ss.Config.ID]
			if !ok {
				return errors.New("no store for " + ss.Config.ID)
			}
			ss.Store = st
			return pooled(ctx, ss)
		},
	})
	t.Cleanup(pool.Close)
	return pool
}

func sharedDeps(t *testing.T, res workspace.Resolver, pool *workspace.ServicePool) serveDeps {
	t.Helper()
	repoPath := testutil.NewTempRepo(t)
	q := queue.NewMemoryQueue(16)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go q.Start(ctx)
	return serveDeps{
		RepoPath:        repoPath,
		SpineCfg:        &config.SpineConfig{ArtifactsDir: "/"},
		GitClient:       git.NewCLIClient(repoPath),
		Queue:           q,
		Events:          event.NewQueueRouter(q),
		WSResolver:      res,
		WSServicePool:   pool,
		DevMode:         true,
		RuntimeEnv:      "development",
		StepEntryPolicy: engine.StrictStepEntryPolicy(),
	}
}

// clearSchedulerEnv unsets both overrides for the test (t.Setenv first, so
// the previous values are restored afterwards).
func clearSchedulerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{schedulerMaxConcurrentEnv, schedulerWorkspaceBudgetEnv} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// AC 1: in shared mode (a ServicePool, no process-level store) the
// runtime holds a MultiScheduler over the pool — and no single-workspace
// scheduler — with the standalone merge retry budget, the orphan threshold
// from SPINE_ORPHAN_THRESHOLD and the bounds' defaults or overrides; the
// lifecycle starts it (Wait returns only once Start has run and returned).
// At the scaffold base the shared-mode runtime held no scheduler at all.
func TestServe_SharedModeBuildsMultiScheduler(t *testing.T) {
	for _, tc := range []struct {
		name        string
		env         map[string]string
		wantMax     int
		wantBudget  time.Duration
		wantTickMax time.Duration
	}{
		{name: "defaults", wantMax: scheduler.DefaultMaxConcurrentWorkspaces, wantBudget: 30 * time.Second, wantTickMax: 60 * time.Second},
		{name: "overrides", env: map[string]string{schedulerMaxConcurrentEnv: "9", schedulerWorkspaceBudgetEnv: "45s"},
			wantMax: 9, wantBudget: 45 * time.Second, wantTickMax: 75 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSchedulerEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			res := newMultiResolver(t, "ws-a", "ws-b")
			pool, _ := timeoutPool(t, res)
			deps := sharedDeps(t, res, pool)
			deps.OrphanThreshold = 7 * time.Hour
			rt, err := buildServerConfig(context.Background(), deps)
			if err != nil {
				t.Fatalf("buildServerConfig: %v", err)
			}
			if rt.MultiScheduler == nil {
				t.Fatal("shared-mode runtime has no MultiScheduler: no timeouts, orphan scans or merge retries would run")
			}
			if rt.Scheduler != nil {
				t.Fatal("shared-mode runtime also holds a single-workspace scheduler")
			}
			cfg := rt.MultiScheduler.Config()
			if cfg.CommitMaxRetries != defaultCommitMaxRetries || cfg.OrphanThreshold != 7*time.Hour ||
				cfg.MaxConcurrentWorkspaces != tc.wantMax || cfg.WorkspaceBudget != tc.wantBudget || cfg.TickWait != tc.wantTickMax {
				t.Fatalf("MultiScheduler config = %+v", cfg)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startBackgroundServices(ctx, rt, observe.Logger(ctx))
			stopBackgroundServices(rt)
			waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer waitCancel()
			if err := rt.MultiScheduler.Wait(waitCtx); err != nil {
				t.Fatalf("the lifecycle did not start (or stop) the MultiScheduler: %v", err)
			}
		})
	}
}

// AC 6: single (file) mode is the previous path: a process-level store
// gets buildScheduler's scheduler (the retry budget with its exhaustion
// handler) and no MultiScheduler; the new environment variables do not
// change it.
func TestServe_SingleModeSchedulerUnchanged(t *testing.T) {
	for _, env := range []map[string]string{nil, {schedulerMaxConcurrentEnv: "2", schedulerWorkspaceBudgetEnv: "5s"}} {
		clearSchedulerEnv(t)
		for k, v := range env {
			t.Setenv(k, v)
		}
		repoPath := testutil.NewTempRepo(t)
		q := queue.NewMemoryQueue(16)
		ctx, cancel := context.WithCancel(context.Background())
		go q.Start(ctx)
		st := newTimeoutStore()
		rt, err := buildServerConfig(ctx, serveDeps{
			Store:           st,
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
			cancel()
			t.Fatalf("buildServerConfig: %v", err)
		}
		if rt.MultiScheduler != nil {
			t.Fatal("single-mode runtime holds a MultiScheduler")
		}
		if rt.Scheduler == nil {
			t.Fatal("single-mode runtime lost its scheduler")
		}
		if maxRetries, handler := rt.Scheduler.CommitRetryBudget(); maxRetries != defaultCommitMaxRetries || !handler {
			t.Fatalf("single-mode commit retry budget = %d (handler %v), want buildScheduler's %d with the handler",
				maxRetries, handler, defaultCommitMaxRetries)
		}
		// The same callbacks as before: a run timeout cancels the run and
		// the strict policy's termination hook closes its executions.
		if err := rt.Scheduler.ScanRunTimeouts(ctx); err != nil {
			t.Fatal(err)
		}
		if st.run.Status != domain.RunStatusCancelled {
			t.Fatalf("run status = %s, want cancelled", st.run.Status)
		}
		requireTermination(t, st, true)
		cancel()
	}
}

func TestMultiSchedulerConfigFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name       string
		max        *string
		budget     *string
		wantMax    int
		wantBudget time.Duration
		wantErr    string
	}{
		{name: "unset"},
		{name: "valid", max: ptr("8"), budget: ptr("90s"), wantMax: 8, wantBudget: 90 * time.Second},
		{name: "spaces", max: ptr(" 2 "), budget: ptr(" 1m "), wantMax: 2, wantBudget: time.Minute},
		{name: "ceiling", max: ptr("256"), wantMax: 256},
		{name: "empty max", max: ptr(""), wantErr: schedulerMaxConcurrentEnv},
		{name: "zero max", max: ptr("0"), wantErr: schedulerMaxConcurrentEnv},
		{name: "negative max", max: ptr("-1"), wantErr: schedulerMaxConcurrentEnv},
		{name: "over ceiling", max: ptr("257"), wantErr: schedulerMaxConcurrentEnv},
		{name: "not a number", max: ptr("four"), wantErr: schedulerMaxConcurrentEnv},
		{name: "empty budget", budget: ptr(""), wantErr: schedulerWorkspaceBudgetEnv},
		{name: "zero budget", budget: ptr("0s"), wantErr: schedulerWorkspaceBudgetEnv},
		{name: "negative budget", budget: ptr("-5s"), wantErr: schedulerWorkspaceBudgetEnv},
		{name: "unitless budget", budget: ptr("30"), wantErr: schedulerWorkspaceBudgetEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSchedulerEnv(t)
			if tc.max != nil {
				t.Setenv(schedulerMaxConcurrentEnv, *tc.max)
			}
			if tc.budget != nil {
				t.Setenv(schedulerWorkspaceBudgetEnv, *tc.budget)
			}
			cfg, err := multiSchedulerConfigFromEnv(3 * time.Hour)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one naming %s (serve must refuse to start)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := scheduler.MultiSchedulerConfig{OrphanThreshold: 3 * time.Hour, CommitMaxRetries: defaultCommitMaxRetries,
				MaxConcurrentWorkspaces: tc.wantMax, WorkspaceBudget: tc.wantBudget}
			if cfg != want {
				t.Fatalf("config = %+v, want %+v", cfg, want)
			}
		})
	}
	// An invalid value refuses to start in single mode too.
	clearSchedulerEnv(t)
	t.Setenv(schedulerMaxConcurrentEnv, "lots")
	if _, err := buildMultiScheduler(serveDeps{}, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("buildMultiScheduler accepted an invalid override in single mode")
	}
}

func ptr(s string) *string { return &s }

// AC 2: through the real wiring (pool adapter, pooled builder, real
// per-workspace orchestrators), one timeout tick fails over the run past
// its timeout in each of two workspaces — each on its own store — and
// calls TerminateLiveSteps (INIT-023 TASK-007), which under the strict
// policy closes the run's live executions.
func TestMultiScheduler_TimesOutRunsAcrossWorkspaces(t *testing.T) {
	clearSchedulerEnv(t)
	res := newMultiResolver(t, "ws-a", "ws-b")
	pool, stores := timeoutPool(t, res)
	ms, err := buildMultiScheduler(sharedDeps(t, res, pool), slog.New(slog.DiscardHandler))
	if err != nil || ms == nil {
		t.Fatalf("buildMultiScheduler = %v, %v", ms, err)
	}
	ms.ScanTimeouts(context.Background())
	for id, st := range stores {
		if st.run.Status != domain.RunStatusCancelled {
			t.Fatalf("%s: run status %s after one timeout tick, want cancelled", id, st.run.Status)
		}
		requireTermination(t, st, true)
		if n := pool.RefCount(id); n != 0 {
			t.Fatalf("%s: pool refcount %d after the tick, want 0", id, n)
		}
	}
}

// AC 4: after every kind of tick each scanned workspace's pool refcount
// is back to zero — also for a workspace whose scan panicked, and none is
// taken for one whose services fail to build.
func TestMultiScheduler_ReleasesPoolRefEachTick(t *testing.T) {
	clearSchedulerEnv(t)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler)) // the recovered panics log their stacks
	t.Cleanup(func() { slog.SetDefault(prev) })
	res := newMultiResolver(t, "ws-a", "ws-b", "ws-panic", "ws-broken")
	stores := map[string]*timeoutStore{"ws-a": newTimeoutStore(), "ws-b": newTimeoutStore()}
	pooled := newPooledWorkspaceBuilder(workspaceDeliveryConfig{}, engine.StrictStepEntryPolicy(), slog.New(slog.DiscardHandler))
	pool := workspace.NewServicePool(context.Background(), res, workspace.PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(ctx context.Context, ss *workspace.ServiceSet) error {
			switch ss.Config.ID {
			case "ws-broken":
				return errors.New("workspace services failed to build")
			case "ws-panic":
				ss.Store = panicStore{}
				return nil
			}
			ss.Store = stores[ss.Config.ID]
			return pooled(ctx, ss)
		},
	})
	t.Cleanup(pool.Close)
	ms, err := buildMultiScheduler(sharedDeps(t, res, pool), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, tick := range map[string]func(context.Context){
		"timeouts": ms.ScanTimeouts, "orphans": ms.ScanOrphans, "commit-retry": ms.RunCommitRetry,
	} {
		tick(ctx)
		for _, id := range []string{"ws-a", "ws-b", "ws-panic", "ws-broken"} {
			if n := pool.RefCount(id); n != 0 {
				t.Fatalf("after the %s tick %s holds %d pool references, want 0", name, id, n)
			}
		}
	}
	if stores["ws-a"].run.Status != domain.RunStatusCancelled || stores["ws-b"].run.Status != domain.RunStatusCancelled {
		t.Fatal("a workspace beside the panicking and the broken one was not scanned")
	}
}

// panicStore panics on every scan read.
type panicStore struct{ stubStore }

func (panicStore) ListActiveStepExecutions(context.Context) ([]domain.StepExecution, error) {
	panic("panicStore: scan")
}

func (panicStore) ListStaleActiveRuns(context.Context, time.Time) ([]domain.Run, error) {
	panic("panicStore: scan")
}

func (panicStore) ListRunsByStatus(context.Context, domain.RunStatus) ([]domain.Run, error) {
	panic("panicStore: scan")
}

// The scheduler never acts on one workspace's runs with another's
// services: a pool entry whose id is not the one asked for (a resolver
// that canonicalises ids) is refused, and its reference is released under
// the id the pool took it under.
func TestSchedulerWorkspacePool_RefusesAnotherWorkspacesServices(t *testing.T) {
	res := newMultiResolver(t, "ws-y")
	res.canonical = map[string]string{"ws-x": "ws-y"}
	pool := workspace.NewServicePool(context.Background(), res, workspace.PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			ss.Store = newTimeoutStore()
			return nil
		},
	})
	t.Cleanup(pool.Close)
	adapter := &schedulerWorkspacePool{pool: pool}
	if _, err := adapter.Get(context.Background(), "ws-x"); err == nil || !strings.Contains(err.Error(), `"ws-y" for "ws-x"`) {
		t.Fatalf("Get(ws-x) resolved to ws-y = %v, want a refusal naming both", err)
	}
	if n := pool.RefCount("ws-y"); n != 0 {
		t.Fatalf("ws-y holds %d references after the refusal, want 0", n)
	}
	ss, err := adapter.Get(context.Background(), "ws-y")
	if err != nil {
		t.Fatal(err)
	}
	if ss.Store == nil || ss.Events == nil {
		t.Fatalf("mapped ServiceSet = %+v, want the workspace's store and events", ss)
	}
	adapter.Release("ws-y")
	if n := pool.RefCount("ws-y"); n != 0 {
		t.Fatalf("ws-y refcount %d after Release, want 0", n)
	}
}

// The mapped set carries the workspace's own callbacks, and a set without
// an event router maps to a nil interface (never a typed nil).
func TestSchedulerServiceSet_MapsCallbacksAndNilEvents(t *testing.T) {
	var calls []string
	ss := &workspace.ServiceSet{
		Store:         newTimeoutStore(),
		CommitRetryFn: func(context.Context, string) error { calls = append(calls, "retry"); return nil },
		CommitExhaustedFn: func(context.Context, string, int) (bool, error) {
			calls = append(calls, "exhausted")
			return false, nil
		},
		StepRecoveryFn:  func(context.Context, string) error { calls = append(calls, "recover"); return nil },
		RunFailFn:       func(context.Context, string, string) error { calls = append(calls, "fail"); return nil },
		RunTerminatedFn: func(context.Context, string, string) { calls = append(calls, "terminated") },
	}
	got := schedulerServiceSet(ss)
	if got.Events != nil {
		t.Fatalf("Events = %#v, want a nil interface for a set without a router", got.Events)
	}
	ctx := context.Background()
	_ = got.CommitRetryFn(ctx, "r")
	_, _ = got.CommitExhaustedFn(ctx, "r", 3)
	_ = got.StepRecoveryFn(ctx, "e")
	_ = got.RunFailFn(ctx, "r", "x")
	got.RunTerminatedFn(ctx, "r", "x")
	if strings.Join(calls, ",") != "retry,exhausted,recover,fail,terminated" {
		t.Fatalf("callbacks reached %v", calls)
	}
}

// Only active workspaces with a valid id are handed to the pool.
func TestSchedulerWorkspaceResolver_ListsOnlyActiveValidIDs(t *testing.T) {
	res := &multiResolver{list: []workspace.Config{
		{ID: "ws-ok", Status: workspace.StatusActive},
		{ID: "ws-off", Status: workspace.StatusInactive},
		{ID: "ws-unset"},
		{ID: "../ws-escape", Status: workspace.StatusActive},
		{ID: "", Status: workspace.StatusActive},
		{ID: "ws-ok-2", Status: workspace.StatusActive},
	}}
	refs, err := schedulerWorkspaceResolver{resolver: res}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range refs {
		ids = append(ids, r.WorkspaceID())
	}
	if strings.Join(ids, ",") != "ws-ok,ws-ok-2" {
		t.Fatalf("listed %v, want only the active workspaces with valid ids", ids)
	}
}

// signalStore is a timeoutStore that reports when its run's timeout
// cancel lands, so a test can wait for a background tick without reading
// the store while the scheduler writes it.
type signalStore struct {
	*timeoutStore
	once      sync.Once
	cancelled chan struct{}
}

func (s *signalStore) UpdateRunStatusAt(ctx context.Context, runID string, status domain.RunStatus, at time.Time) error {
	err := s.timeoutStore.UpdateRunStatusAt(ctx, runID, status, at)
	if status == domain.RunStatusCancelled {
		s.once.Do(func() { close(s.cancelled) })
	}
	return err
}

// The serve lifecycle starts the MultiScheduler, whose real tickers fire
// timeouts in every workspace, and on shutdown stops it and waits for its
// scans before returning (so serveCmd closes the pool only afterwards).
func TestServeLifecycle_StartsAndDrainsMultiScheduler(t *testing.T) {
	clearSchedulerEnv(t)
	res := newMultiResolver(t, "ws-a", "ws-b")
	stores := map[string]*signalStore{}
	byID := map[string]store.Store{}
	for id := range res.cfgs {
		stores[id] = &signalStore{timeoutStore: newTimeoutStore(), cancelled: make(chan struct{})}
		byID[id] = stores[id]
	}
	pool := pooledStores(t, res, byID)
	ms := scheduler.NewMultiScheduler(&schedulerWorkspacePool{pool: pool}, schedulerWorkspaceResolver{resolver: res},
		scheduler.MultiSchedulerConfig{TimeoutInterval: 20 * time.Millisecond, OrphanInterval: 40 * time.Millisecond,
			CommitMaxRetries: defaultCommitMaxRetries})
	rt := &serveRuntime{MultiScheduler: ms}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := observe.Logger(ctx)
	startBackgroundServices(ctx, rt, log)
	for id, st := range stores {
		select {
		case <-st.cancelled:
		case <-time.After(10 * time.Second):
			stopBackgroundServices(rt)
			t.Fatalf("%s: the started MultiScheduler's ticks never timed its run out", id)
		}
	}
	stopBackgroundServices(rt)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	drainBackgroundServices(shutdownCtx, rt, log)
	if shutdownCtx.Err() != nil {
		t.Fatal("the drain used the whole shutdown deadline with nothing left running")
	}
	immediate, c := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer c()
	if err := ms.Wait(immediate); err != nil {
		t.Fatalf("after the drain the scheduler still runs: %v", err)
	}
	for id, st := range stores {
		requireTermination(t, st.timeoutStore, true)
		if n := pool.RefCount(id); n != 0 {
			t.Fatalf("%s refcount %d after shutdown, want 0", id, n)
		}
	}
}

// AC 1, platform-binding mode, from the environment: the start-up path
// serveCmd takes (buildServeWorkspaces, then buildServerConfig) gives the
// runtime a MultiScheduler over the binding-backed pool. Constructible
// without a platform or a database (the provider dials only when a
// workspace is first requested); the db branch is driven end to end by
// TestServe_DBModeMultiSchedulerFiresAcrossWorkspaces (integration).
func TestServe_PlatformBindingModeBuildsMultiScheduler(t *testing.T) {
	clearSchedulerEnv(t)
	t.Setenv("SPINE_SECRET_PREVIOUS_KEYS", "")
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", "")
	t.Setenv("SPINE_SECRET_ENVELOPE_MODE", "")
	t.Setenv("SPINE_ENV", "")
	t.Setenv("WORKSPACE_RESOLVER", "platform-binding")
	t.Setenv("SPINE_PLATFORM_URL", "http://127.0.0.1:1")
	t.Setenv("SPINE_PLATFORM_SERVICE_TOKEN", "svc-token")
	t.Setenv("SPINE_SECRET_PROVIDER", "file")
	t.Setenv("SPINE_SECRET_FILE_ROOT", t.TempDir())
	ctx := context.Background()
	ws, err := buildServeWorkspaces(ctx, "development", slog.New(slog.DiscardHandler), workspaceDeliveryConfig{}, "", engine.StrictStepEntryPolicy())
	if err != nil {
		t.Fatalf("buildServeWorkspaces: %v", err)
	}
	t.Cleanup(ws.Close)
	rt, err := buildServerConfig(ctx, sharedDeps(t, ws.Wiring.Resolver, ws.Wiring.Pool))
	if err != nil {
		t.Fatalf("buildServerConfig: %v", err)
	}
	if rt.MultiScheduler == nil || rt.Scheduler != nil {
		t.Fatalf("platform-binding runtime: MultiScheduler %v, single Scheduler %v; want only the MultiScheduler",
			rt.MultiScheduler != nil, rt.Scheduler != nil)
	}
	// No binding resolved yet: a tick lists nothing and touches nothing.
	rt.MultiScheduler.ScanTimeouts(ctx)
	if n := ws.Wiring.Pool.ActiveCount(); n != 0 {
		t.Fatalf("a tick with no resolved binding built %d workspace service sets", n)
	}
}
