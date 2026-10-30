package gateway_test

// INIT-024 TASK-007, review finding N-14: an open /events/stream kept
// delivering a workspace's events after its token was revoked or its actor
// suspended, because the bearer was checked once, at connect. Each
// heartbeat now re-validates the bearer and closes the stream on failure.
// TestEventStream_ClosesOnRevocation ports the review PoC
// (scratch/standalone/sa3/zz_sa3_sse_revocation_test.go,
// TestZZSA3_SSEOutlivesSuspension).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/workspace"
)

// testSSEHeartbeat is the shortened heartbeat (and so revalidation)
// interval the stream tests run with.
const testSSEHeartbeat = 40 * time.Millisecond

// lockedAuthStore serializes the token and actor reads the stream's
// revalidation makes (on the handler goroutine) with the test's writes
// (revoke, suspend, fail), and hands out copies, so the tests are clean
// under -race.
type lockedAuthStore struct {
	*subFakeStore
	mu          sync.Mutex
	failLookups bool
}

func newLockedAuthStore() *lockedAuthStore {
	l := &lockedAuthStore{subFakeStore: newSubFakeStore()}
	l.actors["admin-1"] = &domain.Actor{
		ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
		Role: domain.RoleAdmin, Status: domain.ActorStatusActive,
	}
	return l
}

func (l *lockedAuthStore) GetActorByTokenHash(ctx context.Context, hash string) (*domain.Actor, *domain.Token, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failLookups {
		return nil, nil, errors.New("connection refused")
	}
	a, tok, err := l.fakeStore.GetActorByTokenHash(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	ac, tc := *a, *tok
	return &ac, &tc, nil
}

func (l *lockedAuthStore) GetActor(ctx context.Context, id string) (*domain.Actor, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.fakeStore.GetActor(ctx, id)
	if err != nil {
		return nil, err
	}
	ac := *a
	return &ac, nil
}

func (l *lockedAuthStore) CreateToken(ctx context.Context, rec *store.TokenRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeStore.CreateToken(ctx, rec)
}

func (l *lockedAuthStore) RevokeToken(ctx context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeStore.RevokeToken(ctx, id)
}

func (l *lockedAuthStore) setStatus(id string, st domain.ActorStatus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fakeStore.actors[id].Status = st
}

func (l *lockedAuthStore) setFailLookups(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failLookups = v
}

// revalStreamServer is setupStreamServer with the shortened heartbeat and
// the race-safe store. expiresAt, when non-nil, is the token's expiry.
func revalStreamServer(t *testing.T, expiresAt *time.Time) (*httptest.Server, *lockedAuthStore, *delivery.EventBroadcaster, string, string) {
	t.Helper()
	ls := newLockedAuthStore()
	authSvc := auth.NewService(ls)
	token, tokenID, err := authSvc.CreateToken(context.Background(), "admin-1", "stream", expiresAt)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	bcast := delivery.NewEventBroadcaster()
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:            ls,
		Auth:             authSvc,
		EventBroadcaster: bcast,
	})
	gateway.SetSSEHeartbeatForTest(srv, testSSEHeartbeat)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, ls, bcast, token, tokenID
}

// waitClosed waits for the stream's response body to end.
func (s *sseStream) waitClosed(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(within):
		t.Fatalf("stream still open %v after the bearer stopped being valid; got %q", within, s.text())
	}
}

// assertClosedForGood checks the closing frame, that an event broadcast
// after the close never reaches the old stream, and that a fresh request
// with the same bearer — including a reconnect carrying Last-Event-ID —
// is refused.
func assertClosedForGood(t *testing.T, ts *httptest.Server, s *sseStream, bcast *delivery.EventBroadcaster, token, marker string) {
	t.Helper()
	if !strings.Contains(s.text(), ": reauthentication required\n\n") {
		t.Errorf("closed stream has no closing comment frame: %q", s.text())
	}
	bcast.Broadcast(domain.Event{EventID: marker, Type: domain.EventType("run_completed"), RunID: "run-secret"})
	time.Sleep(3 * testSSEHeartbeat)
	if strings.Contains(s.text(), marker) {
		t.Errorf("event %q reached a stream whose bearer is no longer valid", marker)
	}
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/events/stream", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("fresh stream request with the same bearer: %d, want 401", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events/stream", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Last-Event-ID", "evt-probe-0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reconnect with Last-Event-ID: %d, want 401", resp.StatusCode)
	}
}

// TestEventStream_ClosesOnRevocation is AC 1 (regression). At the scaffold
// base every variant's stream stays open and keeps delivering.
func TestEventStream_ClosesOnRevocation(t *testing.T) {
	t.Run("actor suspended (review PoC)", func(t *testing.T) {
		ts, ls, bcast, token, _ := revalStreamServer(t, nil)
		s := openSSE(t, ts.URL, token, "")
		waitSubscribed(t, s, bcast)

		// Operator suspends the actor.
		ls.setStatus("admin-1", domain.ActorStatusSuspended)

		// A fresh request with the same bearer is refused...
		resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/events/stream", token, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for a suspended actor, got %d", resp.StatusCode)
		}
		// ...and the open stream closes at the next heartbeat, so the
		// event the PoC saw delivered never arrives.
		s.waitClosed(t, 2*time.Second)
		assertClosedForGood(t, ts, s, bcast, token, "evt-after-suspension")
	})

	t.Run("token revoked", func(t *testing.T) {
		ts, ls, bcast, token, tokenID := revalStreamServer(t, nil)
		s := openSSE(t, ts.URL, token, "")
		waitSubscribed(t, s, bcast)
		if err := ls.RevokeToken(context.Background(), tokenID); err != nil {
			t.Fatal(err)
		}
		s.waitClosed(t, 2*time.Second)
		assertClosedForGood(t, ts, s, bcast, token, "evt-after-revocation")
	})

	t.Run("token expired", func(t *testing.T) {
		exp := time.Now().Add(time.Second)
		ts, _, bcast, token, _ := revalStreamServer(t, &exp)
		s := openSSE(t, ts.URL, token, "")
		waitSubscribed(t, s, bcast)
		s.waitClosed(t, 3*time.Second)
		if time.Now().Before(exp) {
			t.Fatal("stream closed before its token expired")
		}
		assertClosedForGood(t, ts, s, bcast, token, "evt-after-expiry")
	})

	t.Run("token store fails (fail closed)", func(t *testing.T) {
		ts, ls, bcast, token, _ := revalStreamServer(t, nil)
		s := openSSE(t, ts.URL, token, "")
		waitSubscribed(t, s, bcast)
		ls.setFailLookups(true)
		s.waitClosed(t, 2*time.Second)
		if !strings.Contains(s.text(), ": reauthentication required\n\n") {
			t.Errorf("no closing frame: %q", s.text())
		}
	})
}

// A stream whose bearer stays valid is not closed by revalidation: it
// keeps its heartbeats (a heartbeat-only stream) and its events.
func TestEventStream_ValidBearerStaysOpen(t *testing.T) {
	ts, _, bcast, token, _ := revalStreamServer(t, nil)
	s := openSSE(t, ts.URL, token, "")
	waitSubscribed(t, s, bcast)
	time.Sleep(10 * testSSEHeartbeat)
	select {
	case <-s.done:
		t.Fatalf("stream with a valid bearer closed: %q", s.text())
	default:
	}
	if n := strings.Count(s.text(), ": keepalive\n\n"); n < 3 {
		t.Errorf("want at least 3 heartbeats in %v, got %d", 10*testSSEHeartbeat, n)
	}
	bcast.Broadcast(domain.Event{EventID: "evt-still-valid", Type: domain.EventType("run_completed")})
	s.waitFrame(t, "evt-still-valid", 2*time.Second)
	if strings.Contains(s.text(), "reauthentication required") {
		t.Errorf("valid stream got a closing frame: %q", s.text())
	}
}

// Shared mode: the stream revalidates against the workspace's own auth
// service, captured from its ServiceSet at connect, after the handler has
// released its pool reference.
func TestEventStream_SharedModeClosesOnRevocation(t *testing.T) {
	ls := newLockedAuthStore()
	authSvc := auth.NewService(ls)
	token, tokenID, err := authSvc.CreateToken(context.Background(), "admin-1", "stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	bcast := delivery.NewEventBroadcaster()
	table := func() *countingResolver {
		return &countingResolver{calls: map[string]int{}, known: map[string]workspace.Config{
			"ws-1": {ID: "ws-1", RepoPath: t.TempDir(), Status: workspace.StatusActive},
		}}
	}
	pool := workspace.NewServicePool(context.Background(), table(), workspace.PoolConfig{
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			ss.Store = ls
			ss.Auth = authSvc
			ss.EventBroadcaster = bcast
			return nil
		},
	})
	t.Cleanup(pool.Close)
	srv := gateway.NewServer(":0", gateway.ServerConfig{WorkspaceResolver: table(), ServicePool: pool})
	gateway.SetSSEHeartbeatForTest(srv, testSSEHeartbeat)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events/stream", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(gateway.WorkspaceHeader, "ws-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	s := &sseStream{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(s.done)
		defer resp.Body.Close()
		b := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(b)
			s.mu.Lock()
			s.buf.Write(b[:n])
			s.mu.Unlock()
			if rerr != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-s.done })
	waitSubscribed(t, s, bcast)

	if err := ls.RevokeToken(context.Background(), tokenID); err != nil {
		t.Fatal(err)
	}
	s.waitClosed(t, 2*time.Second)
	if !strings.Contains(s.text(), ": reauthentication required\n\n") {
		t.Errorf("no closing frame: %q", s.text())
	}
}
