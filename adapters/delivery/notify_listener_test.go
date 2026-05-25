package delivery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// TestParseNotifyPayload covers the two accepted payload shapes plus
// the degenerate cases the listener must fail closed on (logged + drop).
func TestParseNotifyPayload(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"json envelope", `{"event_id":"evt-abc"}`, "evt-abc"},
		{"json envelope with extra fields", `{"event_id":"evt-x","source":"smp"}`, "evt-x"},
		{"json missing field", `{"foo":"bar"}`, ""},
		{"malformed json (starts with brace)", `{not json`, ""},
		{"bare event id", "evt-bare-123", "evt-bare-123"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseNotifyPayload(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestQuoteIdent ensures we double-quote a SQL identifier the same
// way pgx.Identifier.Sanitize does. The listener interpolates the
// channel name into LISTEN; even though the production channel is a
// fixed literal, a test-overridden channel could in theory carry an
// embedded quote and the escape must be honest.
func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "spine_event_log", `"spine_event_log"`},
		{"with quote", `weird"name`, `"weird""name"`},
		{"empty", "", `""`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := quoteIdent(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// fakeFetcher is a deterministic EventFetcher for the handleNotification
// unit test. Records every event_id it's asked for, returns a canned
// event for one specific id, nil for another, and an error for a third.
type fakeFetcher struct {
	mu     sync.Mutex
	asked  []string
	canned map[string]*domain.Event
	errs   map[string]error
}

func (f *fakeFetcher) GetEventByID(ctx context.Context, eventID string) (*domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, eventID)
	if err, ok := f.errs[eventID]; ok {
		return nil, err
	}
	return f.canned[eventID], nil
}

// TestHandleNotification_BroadcastsCannedEvent proves the happy path:
// payload arrives, fetcher returns a real event, broadcaster receives
// the SAME shape (event_id, type, timestamp preserved).
func TestHandleNotification_BroadcastsCannedEvent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	canned := &domain.Event{
		EventID:   "evt-happy",
		Type:      domain.EventRunCancelled,
		Timestamp: now,
		RunID:     "run-1",
		TraceID:   "trc-1",
	}
	f := &fakeFetcher{canned: map[string]*domain.Event{"evt-happy": canned}}
	b := NewEventBroadcaster()

	ch := make(chan domain.Event, 1)
	id := b.Subscribe(ch)
	defer b.Unsubscribe(id)

	l := NewNotifyListener(nil, b)
	l.handleNotification(context.Background(), f, &pgconnNotification{Channel: l.channel, Payload: `{"event_id":"evt-happy"}`})

	select {
	case got := <-ch:
		if got.EventID != canned.EventID {
			t.Errorf("event_id: got %q, want %q", got.EventID, canned.EventID)
		}
		if got.Type != canned.Type {
			t.Errorf("type: got %q, want %q", got.Type, canned.Type)
		}
		if !got.Timestamp.Equal(canned.Timestamp) {
			t.Errorf("timestamp: got %v, want %v", got.Timestamp, canned.Timestamp)
		}
		if got.RunID != canned.RunID || got.TraceID != canned.TraceID {
			t.Errorf("run/trace: got %+v, want %+v", got, canned)
		}
	case <-time.After(time.Second):
		t.Fatal("broadcast did not arrive within 1s")
	}
}

// TestHandleNotification_DropsOnEmptyPayload proves a notification with
// an empty payload never reaches the fetcher (no row lookup) and never
// broadcasts (no event with EventID == "" gets delivered). The fetcher
// would silently miss every real event if we let "" through and got
// back a not-found.
func TestHandleNotification_DropsOnEmptyPayload(t *testing.T) {
	f := &fakeFetcher{}
	b := NewEventBroadcaster()
	ch := make(chan domain.Event, 1)
	b.Subscribe(ch)

	l := NewNotifyListener(nil, b)
	l.handleNotification(context.Background(), f, &pgconnNotification{Channel: l.channel, Payload: ""})

	if len(f.asked) != 0 {
		t.Errorf("fetcher was asked %v on empty payload; expected no lookups", f.asked)
	}
	select {
	case got := <-ch:
		t.Errorf("unexpected broadcast on empty payload: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestHandleNotification_SkipsOnFetchError covers the transient-error
// path: a failed fetch is logged but does NOT broadcast a partial
// event. The reconnect-replay path remains the safety net.
func TestHandleNotification_SkipsOnFetchError(t *testing.T) {
	f := &fakeFetcher{errs: map[string]error{"evt-err": errors.New("boom")}}
	b := NewEventBroadcaster()
	ch := make(chan domain.Event, 1)
	b.Subscribe(ch)

	l := NewNotifyListener(nil, b)
	l.handleNotification(context.Background(), f, &pgconnNotification{Channel: l.channel, Payload: `{"event_id":"evt-err"}`})

	if len(f.asked) != 1 || f.asked[0] != "evt-err" {
		t.Errorf("expected one fetch for evt-err, got %v", f.asked)
	}
	select {
	case got := <-ch:
		t.Errorf("unexpected broadcast on fetch error: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestHandleNotification_SkipsOnNotFound covers the race the seam doc
// flags: a NOTIFY can arrive before the corresponding event_log row is
// visible to a fresh transaction (e.g. tx isolation, replication lag).
// The listener must silently drop on (nil, nil) — the reconnect-replay
// path picks the event up when the row commits.
func TestHandleNotification_SkipsOnNotFound(t *testing.T) {
	f := &fakeFetcher{} // canned is nil → returns (nil, nil)
	b := NewEventBroadcaster()
	ch := make(chan domain.Event, 1)
	b.Subscribe(ch)

	l := NewNotifyListener(nil, b)
	l.handleNotification(context.Background(), f, &pgconnNotification{Channel: l.channel, Payload: `{"event_id":"evt-missing"}`})

	if len(f.asked) != 1 || f.asked[0] != "evt-missing" {
		t.Errorf("expected one fetch for evt-missing, got %v", f.asked)
	}
	select {
	case got := <-ch:
		t.Errorf("unexpected broadcast on row-not-found: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestNewNotifyListener_DefaultsToCanonicalChannel covers the most
// important wiring invariant: the constructor MUST default the channel
// to EventLogNotifyChannel so SMP's pg_notify reaches every Spine
// instance without per-deployment configuration. A regression here
// would silently break the bridge.
func TestNewNotifyListener_DefaultsToCanonicalChannel(t *testing.T) {
	l := NewNotifyListener(nil, NewEventBroadcaster())
	if l.channel != EventLogNotifyChannel {
		t.Errorf("channel: got %q, want %q", l.channel, EventLogNotifyChannel)
	}
}

// TestNotifyListener_WithChannelOverrides keeps the test-only escape
// hatch honest. Production wiring relies on the default; tests that
// want hermetic isolation use WithChannel.
func TestNotifyListener_WithChannelOverrides(t *testing.T) {
	l := NewNotifyListener(nil, NewEventBroadcaster()).WithChannel("custom_ch")
	if l.channel != "custom_ch" {
		t.Errorf("channel: got %q, want custom_ch", l.channel)
	}
}

// TestNotifyListener_DefaultsToConnFetcher pins the production
// wiring: NewNotifyListener MUST default to a non-nil fetcher factory
// so the listener fetches event_log rows on its own LISTEN connection
// rather than re-acquiring from the pool (which deadlocks at
// max_conns=1). A regression that swapped DefaultConnFetcher back to
// a pool-acquiring fetcher would still pass this check; the integration
// test covers the deadlock-free path end-to-end at max_conns=1.
func TestNotifyListener_DefaultsToConnFetcher(t *testing.T) {
	l := NewNotifyListener(nil, NewEventBroadcaster())
	if l.fetchFactory == nil {
		t.Fatal("fetchFactory must be non-nil after NewNotifyListener")
	}
}
