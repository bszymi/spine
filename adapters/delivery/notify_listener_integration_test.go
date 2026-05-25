//go:build integration

package delivery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// TestIntegration_NotifyListener_RoundTrip is sub-PR 10a's canonical
// regression: a NOTIFY on the listener's channel triggers a fetch
// against runtime.event_log and a broadcast to a subscribed channel.
// Drives the full path end-to-end against a real Postgres so the
// pgx LISTEN handshake, the WaitForNotification loop, the GetEventByID
// query, and the broadcaster send are all exercised together. The
// unit tests cover the predicate / payload-parse / handle-notification
// branches; this is the "wiring really works" smoke.
//
// Gated by SPINE_TEST_DATABASE_URL (default
// postgres://spine_test:spine_test@localhost:5433/spine_test). Uses
// the in-package NewTestStore which migrates the runtime schema, so
// the assertions below only depend on event_id round-tripping through
// runtime.event_log.
func TestIntegration_NotifyListener_RoundTrip(t *testing.T) {
	st := store.NewTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Hermetic channel name so two parallel test runs on the same DB
	// can't cross-talk. The listener accepts the override via
	// WithChannel; SMP's writer in production uses the canonical
	// constant.
	channel := fmt.Sprintf("test_event_log_%d", time.Now().UnixNano())

	broadcaster := delivery.NewEventBroadcaster()
	subscriber := make(chan domain.Event, 16)
	subID := broadcaster.Subscribe(subscriber)
	defer broadcaster.Unsubscribe(subID)

	listener := delivery.NewNotifyListener(st.RawPool(), broadcaster).WithChannel(channel)
	attached := listener.WithAttachedSignal()

	runCh := make(chan error, 1)
	go func() { runCh <- listener.Run(ctx) }()

	// Wait for the listener's LISTEN handshake to complete before
	// issuing the real NOTIFY. PostgreSQL drops notifications sent to
	// a channel with no current listeners, so sending the test event
	// before LISTEN has executed would silently lose the event and
	// the test would flake to the 5s timeout below. The WithAttachedSignal
	// channel closes immediately after `LISTEN <channel>` returns,
	// which is the precise moment the backend has registered our
	// subscription — strictly deterministic where the earlier probe
	// loop was racy.
	select {
	case <-attached:
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not attach LISTEN within 2s")
	}

	// Insert a real event_log row and emit the matching NOTIFY. The
	// listener should fetch the row and broadcast the reconstructed
	// event within ~tens of ms.
	now := time.Now().UTC().Truncate(time.Millisecond)
	evt := &domain.Event{
		EventID:   fmt.Sprintf("evt-int-%d", now.UnixNano()),
		Type:      domain.EventRunCancelled,
		Timestamp: now,
		RunID:     "run-int-1",
		TraceID:   "trc-int-1",
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := st.WriteEventLog(ctx, &store.EventLogEntry{
		EventID:   evt.EventID,
		EventType: string(evt.Type),
		Payload:   payload,
		CreatedAt: evt.Timestamp,
	}); err != nil {
		t.Fatalf("WriteEventLog: %v", err)
	}
	if err := st.ExecRaw(ctx, fmt.Sprintf("NOTIFY %s, %s", channel, sqlString(`{"event_id":"`+evt.EventID+`"}`))); err != nil {
		t.Fatalf("NOTIFY: %v", err)
	}

	select {
	case got := <-subscriber:
		if got.EventID != evt.EventID {
			t.Errorf("event_id: got %q, want %q", got.EventID, evt.EventID)
		}
		if got.Type != evt.Type {
			t.Errorf("type: got %q, want %q", got.Type, evt.Type)
		}
		if got.RunID != evt.RunID || got.TraceID != evt.TraceID {
			t.Errorf("run/trace: got %+v, want %+v", got, evt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not broadcast within 5s")
	}

	cancel()
	select {
	case err := <-runCh:
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("listener did not stop after context cancel")
	}
}

// sqlString single-quotes a Postgres string literal for the NOTIFY
// payload, escaping embedded single quotes. The test issues NOTIFY
// via ExecRaw rather than $1 binding because NOTIFY's payload arg
// must be a literal — pgx silently rejects parameterised NOTIFY.
func sqlString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, c)
	}
	out = append(out, '\'')
	return string(out)
}
