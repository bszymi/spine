package gateway

// INIT-023 TASK-015 review round 1 (P3): the Warn the SSE writer logs
// when it omits an unsafe id:/event: line is rate-limited like the
// proxied-git-peer Warn. Before, it logged once per field per event: an
// unsafe-ID event logged once per live subscriber, and once a workflow
// had produced more than sseReplayCap such events the client cursor no
// longer advanced, so every reconnect replayed the same window and wrote
// up to 2*sseReplayCap Warn lines — a reader-role client reconnecting in
// a loop drove log volume without bound.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// t015ReplayStore serves a fixed runtime.event_log window to
// Last-Event-ID replay, whatever the cursor.
type t015ReplayStore struct {
	store.Store
	events []store.EventLogEntry
}

func (f t015ReplayStore) ListEventsAfter(_ context.Context, _ string, _ []string, limit int) ([]store.EventLogEntry, error) {
	if len(f.events) > limit {
		return f.events[:limit], nil
	}
	return f.events, nil
}

// t015Reconnect runs one SSE request that replays from a cursor. The
// request context is already cancelled, so the handler replays the
// window, enters the live loop and returns at once.
func t015Reconnect(t *testing.T, s *Server, sa *streamAuthStore) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t015Ctx("", domain.RoleReader, "reader-1"))
	cancel()
	req := sa.withStreamBearer(httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", http.NoBody).WithContext(ctx), "reader-1")
	req.Header.Set("Last-Event-ID", "evt-0")
	w := httptest.NewRecorder()
	s.handleEventStream(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stream status %d, want 200", w.Code)
	}
	return w.Body.String()
}

func TestP328_UnsafeSSEFieldWarnIsRateLimited(t *testing.T) {
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A full replay window of events whose ID and type both break the
	// field rule; the ID is attacker-sized as well.
	long := strings.Repeat("x", 4096)
	events := make([]store.EventLogEntry, sseReplayCap)
	for i := range events {
		events[i] = store.EventLogEntry{
			EventID:   fmt.Sprintf("evt-0123456789ab-a\ndata: {\"type\":\"run_completed\"}\n\nid: %s-%d-completed", long, i),
			EventType: "step.completed\nevent: forged",
			Payload:   []byte(`{}`),
		}
	}
	sa, authSvc := newStreamAuth()
	s := &Server{store: t015ReplayStore{events: events}, auth: authSvc, eventBroadcaster: delivery.NewEventBroadcaster(), sseLimiter: newSSELimiter(10)}

	const reconnects = 3
	for i := 0; i < reconnects; i++ {
		body := t015Reconnect(t, s, sa)
		// The frames themselves are unchanged by the rate limit: every
		// unsafe id:/event: line is still omitted.
		if strings.Contains(body, "id: ") || strings.Contains(body, "event: ") || strings.Contains(body, "\ndata: {\"type\"") {
			t.Fatalf("an unsafe field reached the stream:\n%.400s", body)
		}
		if n := strings.Count(body, "data: {}\n\n"); n != sseReplayCap {
			t.Fatalf("replayed %d frames, want %d", n, sseReplayCap)
		}
	}

	out := buf.String()
	const msg = "omitting unsafe event fields"
	if n := strings.Count(out, msg); n != 1 {
		t.Fatalf("%d replays of %d unsafe events logged the Warn %d times, want 1", reconnects, sseReplayCap, n)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "event:") {
			t.Errorf("an unsafe value split the log line: %q", line)
		}
		if strings.Contains(line, msg) && len(line) > 1024 {
			t.Errorf("the Warn repeats the whole identifier (%d bytes)", len(line))
		}
	}
	if strings.Contains(out, strings.Repeat("x", maxIdentifierEcho+1)) {
		t.Error("the Warn is not bounded to maxIdentifierEcho bytes of the identifier")
	}

	// Once the interval has passed, the next omission logs again and
	// reports how many were not logged meanwhile: the first Warn covered
	// one event of reconnects*sseReplayCap.
	s.sseUnsafeWarnAt.Store(time.Now().Add(-2 * sseUnsafeWarnInterval).UnixNano())
	_ = t015Reconnect(t, s, sa)
	out = buf.String()
	if n := strings.Count(out, msg); n != 2 {
		t.Fatalf("after the interval the Warn was logged %d times in all, want 2", n)
	}
	if want := fmt.Sprintf("suppressed_since_last_warn=%d", reconnects*sseReplayCap-1); !strings.Contains(out, want) {
		t.Errorf("the second Warn must carry %s:\n%s", want, lastLineWith(out, msg))
	}

	// A safe event never logs and never counts.
	before := s.sseUnsafeSuppressed.Load()
	s.warnUnsafeSSEFields(slog.Default(), "evt-0123456789ab-build-completed", string(domain.EventStepCompleted))
	if got := s.sseUnsafeSuppressed.Load(); got != before {
		t.Errorf("a safe event changed the suppressed count: %d -> %d", before, got)
	}
}

func lastLineWith(out, s string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], s) {
			return lines[i]
		}
	}
	return ""
}
