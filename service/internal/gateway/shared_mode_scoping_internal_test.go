package gateway

// INIT-023 TASK-015 (audit P3-29) regression: process-global gateway
// state must not leak across workspaces in shared multi-workspace mode
// (a workspace.ServicePool is wired). At 61994fe a tenant-A operator's
// /system/metrics named tenant B's workspace and carried the
// all-tenant counters, tenant B read and overwrote A's rebuild status by
// reusing A's X-Trace-Id prefix, and A's streams exhausted the same
// actor ID's SSE slots in B (AC 5, 6, 7).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/service/internal/workspace"
)

// t015SharedServer returns a server in shared mode: a ServicePool is
// wired. The pool is never asked for a service set by these tests.
func t015SharedServer(t *testing.T) *Server {
	t.Helper()
	resolver := &poolStubResolver{cfg: workspace.Config{ID: "t015-ws-a", RepoPath: t.TempDir()}}
	pool := workspace.NewServicePool(context.Background(), resolver, workspace.PoolConfig{})
	t.Cleanup(pool.Close)
	return &Server{servicePool: pool}
}

// t015Ctx is the context workspaceMiddleware and authMiddleware build:
// the resolved workspace config and the authenticated actor.
func t015Ctx(ws string, role domain.ActorRole, actorID string) context.Context {
	ctx := context.Background()
	if ws != "" {
		ctx = context.WithValue(ctx, workspaceKey{}, &workspace.Config{ID: ws})
	}
	a := &domain.Actor{ActorID: actorID, Role: role, Status: domain.ActorStatusActive}
	ctx = context.WithValue(ctx, actorKey{}, a)
	return domain.WithActor(ctx, a)
}

func TestP329_Metrics_SharedModeShowsOnlyCallerWorkspace(t *testing.T) {
	observe.PoolMetrics("t015-ws-a").Open.Inc()
	observe.PoolMetrics("t015-ws-b").Open.Inc()
	observe.GlobalMetrics.RunsStarted.Inc()
	s := t015SharedServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", http.NoBody).
		WithContext(t015Ctx("t015-ws-a", domain.RoleOperator, "op"))
	w := httptest.NewRecorder()
	s.handleMetrics(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, body)
	}
	if !strings.Contains(body, `spine_workspace_pool_open_total{workspace_id="t015-ws-a"} `) {
		t.Errorf("the caller's own series is missing:\n%s", body)
	}
	if strings.Contains(body, "t015-ws-b") {
		t.Errorf("another workspace's ID leaked to a tenant operator:\n%s", body)
	}
	if strings.Contains(body, "spine_runs_started_total") {
		t.Errorf("process-global (all-tenant) counters leaked to a tenant operator:\n%s", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q", ct)
	}

	// No resolved workspace in shared mode fails closed instead of
	// falling back to the full export.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", http.NoBody).
		WithContext(t015Ctx("", domain.RoleOperator, "op"))
	w = httptest.NewRecorder()
	s.handleMetrics(w, req)
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "t015-ws") {
		t.Errorf("shared mode without a workspace: %d %q, want 400 without metrics", w.Code, w.Body.String())
	}
}

// Guard (passes before and after): single-workspace mode, where file mode
// also resolves a workspace config, keeps the full export unchanged.
func TestP329_Metrics_SingleModeUnchanged(t *testing.T) {
	observe.PoolMetrics("t015-single").Open.Inc()
	s := &Server{}
	for _, ws := range []string{"", "default"} {
		// Global metrics are process-wide; retry until no background
		// goroutine changed them while the handler ran.
		matched := false
		for attempt := 0; attempt < 5 && !matched; attempt++ {
			before := observe.ExportPrometheus()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", http.NoBody).
				WithContext(t015Ctx(ws, domain.RoleOperator, "op"))
			w := httptest.NewRecorder()
			s.handleMetrics(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("workspace %q: status %d", ws, w.Code)
			}
			if before != observe.ExportPrometheus() {
				continue
			}
			if w.Body.String() != before {
				t.Fatalf("workspace %q: single-mode export changed", ws)
			}
			matched = true
		}
		if !matched {
			t.Fatalf("workspace %q: global metrics never settled", ws)
		}
	}
}

func TestP329_PlatformMetrics_OperatorTokenOnly(t *testing.T) {
	const tok = "t015-platform-operator-token-0123456789abcdef"
	t.Setenv("SPINE_OPERATOR_TOKEN", tok)
	observe.PoolMetrics("t015-ws-b").Open.Inc()
	srv := NewServer(":0", ServerConfig{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(bearer string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/platform/metrics", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(tok); code != http.StatusOK || !strings.Contains(body, `workspace_id="t015-ws-b"`) || !strings.Contains(body, "spine_runs_started_total") {
		t.Fatalf("platform metrics with the operator token: %d %q", code, body)
	}
	if code, body := get("wrong-token"); code != http.StatusUnauthorized || strings.Contains(body, "t015-ws-b") {
		t.Fatalf("wrong token: %d, want 401 without metrics", code)
	}
	if code, _ := get(""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", code)
	}

	// Unset operator token: the route answers 503, like /workspaces.
	t.Setenv("SPINE_OPERATOR_TOKEN", "")
	srv2 := NewServer(":0", ServerConfig{})
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	resp, err := http.Get(ts2.URL + "/api/v1/platform/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unset operator token: %d, want 503", resp.StatusCode)
	}
}

type t015ProjSync struct {
	mu    sync.Mutex
	calls int
}

func (p *t015ProjSync) FullRebuild(context.Context) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil
}

func t015RebuildStatus(s *Server, ctx context.Context, id string) (int, map[string]any) {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("rebuild_id", id)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/rebuild/"+id, http.NoBody).
		WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleSystemRebuildStatus(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func t015StartRebuild(t *testing.T, s *Server, ctx context.Context) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/rebuild", http.NoBody).WithContext(ctx)
	w := httptest.NewRecorder()
	s.handleSystemRebuild(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("start rebuild: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	id, _ := out["rebuild_id"].(string)
	return id
}

func TestP329_Rebuild_RegistryScopedToWorkspace(t *testing.T) {
	s := &Server{projSync: &t015ProjSync{}}
	// Both tenants send the same X-Trace-Id, so both rebuilds get the
	// same rebuild_id ("rb-" + trace[:8]).
	const trace = "deadbeef-0000-4000-8000-000000000000"
	ctxA := observe.WithTraceID(t015Ctx("t015-ws-a", domain.RoleOperator, "op-a"), trace)
	ctxB := observe.WithTraceID(t015Ctx("t015-ws-b", domain.RoleOperator, "op-b"), trace)

	idA := t015StartRebuild(t, s, ctxA)
	if idA != "rb-deadbeef" {
		t.Fatalf("rebuild_id wire format changed: %q", idA)
	}
	if code, _ := t015RebuildStatus(s, ctxA, idA); code != http.StatusOK {
		t.Fatalf("workspace A cannot read its own rebuild: %d", code)
	}
	if code, _ := t015RebuildStatus(s, ctxB, idA); code != http.StatusNotFound {
		t.Fatalf("workspace B read workspace A's rebuild status: %d, want 404", code)
	}
	// B starting a rebuild with the same ID must not replace A's entry.
	_, before := t015RebuildStatus(s, ctxA, idA)
	time.Sleep(2 * time.Millisecond) // a replacement would carry a later started_at
	if idB := t015StartRebuild(t, s, ctxB); idB != idA {
		t.Fatalf("expected the same rebuild_id for the same trace prefix, got %q and %q", idA, idB)
	}
	_, after := t015RebuildStatus(s, ctxA, idA)
	if before["started_at"] != after["started_at"] {
		t.Fatalf("workspace B overwrote workspace A's rebuild entry: %v -> %v", before["started_at"], after["started_at"])
	}
	if code, _ := t015RebuildStatus(s, ctxB, idA); code != http.StatusOK {
		t.Fatalf("workspace B cannot read its own rebuild: %d", code)
	}
}

type t015NilStore struct{ store.Store }

// waitHeld waits until the limiter holds n slots for key.
func waitHeld(t *testing.T, l *sseLimiter, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		got := l.counts[key]
		l.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("limiter never held %d slot(s) for %q", n, key)
}

// Through the real handler: the cap is 1, smp-admin holds one stream in
// workspace A, and the same actor ID in workspace B must still get a
// stream, while a second stream in A is refused.
func TestP329_SSELimiter_KeyedPerWorkspace(t *testing.T) {
	sa, authSvc := newStreamAuth()
	s := &Server{store: t015NilStore{}, auth: authSvc, eventBroadcaster: delivery.NewEventBroadcaster(), sseLimiter: newSSELimiter(1)}
	type stream struct {
		w      *httptest.ResponseRecorder
		cancel context.CancelFunc
		done   chan struct{}
	}
	open := func(ws string) stream {
		ctx, cancel := context.WithCancel(t015Ctx(ws, domain.RoleReader, "smp-admin"))
		req := sa.withStreamBearer(httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", http.NoBody).WithContext(ctx), "smp-admin")
		st := stream{w: httptest.NewRecorder(), cancel: cancel, done: make(chan struct{})}
		go func() { s.handleEventStream(st.w, req); close(st.done) }()
		return st
	}
	keyA := sseLimiterKey(t015Ctx("t015-ws-a", domain.RoleReader, "smp-admin"), "smp-admin")
	keyB := sseLimiterKey(t015Ctx("t015-ws-b", domain.RoleReader, "smp-admin"), "smp-admin")

	a := open("t015-ws-a")
	waitHeld(t, s.sseLimiter, keyA, 1)
	b := open("t015-ws-b")
	waitHeld(t, s.sseLimiter, keyB, 1)
	a2 := open("t015-ws-a")
	<-a2.done // refused at once, or (wrongly) never: then the test times out
	a2.cancel()

	b.cancel()
	<-b.done
	a.cancel()
	<-a.done
	if b.w.Code == http.StatusTooManyRequests {
		t.Fatalf("workspace A's stream exhausted the same actor ID's SSE slot in workspace B")
	}
	if a2.w.Code != http.StatusTooManyRequests {
		t.Fatalf("the per-(workspace, actor) cap is no longer enforced: a second stream in A got %d", a2.w.Code)
	}
	waitHeld(t, s.sseLimiter, keyA, 0)
	waitHeld(t, s.sseLimiter, keyB, 0)
}

func TestP329_SSELimiterKey(t *testing.T) {
	if got := sseLimiterKey(context.Background(), "alice"); got != "alice" {
		t.Errorf("no workspace: %q, want the bare actor ID", got)
	}
	if got := sseLimiterKey(t015Ctx("ws-1", domain.RoleReader, "alice"), ""); got != "" {
		t.Errorf("empty actor ID must stay empty (no limit), got %q", got)
	}
	a := sseLimiterKey(t015Ctx("ws-1", domain.RoleReader, "alice"), "alice")
	b := sseLimiterKey(t015Ctx("ws-2", domain.RoleReader, "alice"), "alice")
	if a == b {
		t.Errorf("the same actor in two workspaces shares a key: %q", a)
	}
}
