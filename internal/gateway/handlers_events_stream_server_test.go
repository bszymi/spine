package gateway_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/store"
)

// setupStreamServer returns a gateway configured with an EventBroadcaster so
// SSE handlers can be driven end-to-end.
func setupStreamServer(t *testing.T) (*httptest.Server, *subFakeStore, *delivery.EventBroadcaster, string) {
	t.Helper()
	sfs := newSubFakeStore()
	sfs.fakeStore.actors["admin-1"] = &domain.Actor{
		ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
		Role: domain.RoleAdmin, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(sfs)
	plaintext, _, err := authSvc.CreateToken(context.Background(), "admin-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	bcast := delivery.NewEventBroadcaster()
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:            sfs,
		Auth:             authSvc,
		EventBroadcaster: bcast,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sfs, bcast, plaintext
}

// TestEventStream_NoBroadcaster verifies the 503 branch when the server has
// no broadcaster wired in (single-mode setup).
func TestEventStream_NoBroadcaster(t *testing.T) {
	ts, _, token := setupSubServer(t) // no broadcaster
	resp := doJSON(t, http.MethodGet, ts.URL+"/api/v1/events/stream", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", resp.StatusCode)
	}
}

// TestEventStream_HappyPath connects, receives a broadcast event, and
// disconnects cleanly.
func TestEventStream_HappyPath(t *testing.T) {
	ts, _, bcast, token := setupStreamServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/api/v1/events/stream?types=run.completed", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}

	// Give the handler a moment to subscribe before broadcasting.
	time.Sleep(50 * time.Millisecond)

	bcast.Broadcast(domain.Event{
		EventID: "evt-stream-1",
		Type:    domain.EventType("run.completed"),
	})
	// Broadcast a filtered-out type that should NOT be delivered.
	bcast.Broadcast(domain.Event{
		EventID: "evt-stream-ignored",
		Type:    domain.EventType("run.started"),
	})

	// Read until we see the event or timeout.
	br := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(1 * time.Second)
	var accumulated strings.Builder
	for time.Now().Before(deadline) {
		_ = setReadDeadline(resp.Body, 200*time.Millisecond)
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			break
		}
		accumulated.WriteString(line)
		if strings.Contains(accumulated.String(), "evt-stream-1") {
			break
		}
	}
	got := accumulated.String()
	if !strings.Contains(got, "evt-stream-1") {
		t.Fatalf("did not receive broadcast event; got: %q", got)
	}
	if strings.Contains(got, "evt-stream-ignored") {
		t.Errorf("filtered-out event leaked through: %q", got)
	}
}

// TestEventStream_Replay verifies Last-Event-ID replay reads from the store.
func TestEventStream_Replay(t *testing.T) {
	ts, fs, _, token := setupStreamServer(t)
	fs.events = []store.EventLogEntry{
		{EventID: "evt-replay-1", EventType: "run.completed", Payload: []byte(`{}`)},
		{EventID: "evt-replay-2", EventType: "run.completed", Payload: []byte(`{}`)},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/api/v1/events/stream?types=run.completed", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Last-Event-ID", "evt-replay-0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// Read a short window then cancel.
	done := make(chan struct{})
	var body strings.Builder
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		_ = setReadDeadline(resp.Body, 500*time.Millisecond)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				body.Write(buf[:n])
			}
			if err != nil {
				return
			}
			if strings.Contains(body.String(), "evt-replay-2") {
				return
			}
		}
	}()
	<-done

	got := body.String()
	if !strings.Contains(got, "evt-replay-1") || !strings.Contains(got, "evt-replay-2") {
		t.Errorf("expected replayed events, got: %q", got)
	}
}

// setReadDeadline is a best-effort helper: the underlying conn may not expose
// a SetReadDeadline method when wrapped. Returns nil on unsupported.
func setReadDeadline(r io.Reader, d time.Duration) error {
	type deadliner interface {
		SetReadDeadline(time.Time) error
	}
	if dr, ok := r.(deadliner); ok {
		return dr.SetReadDeadline(time.Now().Add(d))
	}
	return nil
}
