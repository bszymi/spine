package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/store"
)

// dispatcherStore implements the store methods used by WebhookDispatcher.
type dispatcherStore struct {
	mu           sync.Mutex
	queue        []store.DeliveryEntry
	claimed      []store.DeliveryEntry
	delivered    map[string]bool
	statuses     map[string]string
	logs         []store.DeliveryLogEntry
}

func newDispatcherStore() *dispatcherStore {
	return &dispatcherStore{
		delivered: make(map[string]bool),
		statuses:  make(map[string]string),
	}
}

func (s *dispatcherStore) addToQueue(entries ...store.DeliveryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, entries...)
}

func (s *dispatcherStore) ClaimDeliveries(_ context.Context, limit int) ([]store.DeliveryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := limit
	if n > len(s.queue) {
		n = len(s.queue)
	}
	claimed := make([]store.DeliveryEntry, n)
	copy(claimed, s.queue[:n])
	s.queue = s.queue[n:]
	s.claimed = append(s.claimed, claimed...)
	return claimed, nil
}

func (s *dispatcherStore) UpdateDeliveryStatus(_ context.Context, deliveryID, status string, lastError string, nextRetryAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[deliveryID] = status
	return nil
}

func (s *dispatcherStore) MarkDelivered(_ context.Context, deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered[deliveryID] = true
	s.statuses[deliveryID] = "delivered"
	return nil
}

func (s *dispatcherStore) LogDeliveryAttempt(_ context.Context, entry *store.DeliveryLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, *entry)
	return nil
}

func (s *dispatcherStore) getStatus(deliveryID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statuses[deliveryID]
}

func (s *dispatcherStore) getLogs() []store.DeliveryLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]store.DeliveryLogEntry, len(s.logs))
	copy(result, s.logs)
	return result
}

// staticResolver returns a fixed subscription for any ID.
type staticResolver struct {
	sub *SubscriptionDetail
}

func (r *staticResolver) GetSubscription(_ context.Context, _ string) (*SubscriptionDetail, error) {
	return r.sub, nil
}

func TestWebhookDispatcher_SuccessfulDelivery(t *testing.T) {
	var received []byte
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		headers = r.Header
		w.WriteHeader(200)
	}))
	defer server.Close()

	ds := newDispatcherStore()
	resolver := &staticResolver{sub: &SubscriptionDetail{
		SubscriptionID: "sub-1",
		TargetURL:      server.URL,
		SigningSecret:  "test-secret",
	}}

	payload, _ := json.Marshal(map[string]string{"event_id": "evt-1", "type": "run_started"})
	ds.addToQueue(store.DeliveryEntry{
		DeliveryID:     "dlv-001",
		SubscriptionID: "sub-1",
		EventID:        "evt-1",
		EventType:      "run_started",
		Payload:        payload,
		Status:         "delivering",
		CreatedAt:      time.Now().UTC(),
	})

	dispatcher := NewWebhookDispatcher(
		newDispatcherMinimalStore(ds),
		resolver,
		DispatcherConfig{Concurrency: 1, HTTPTimeout: 5 * time.Second, PollInterval: 50 * time.Millisecond},
	)

	ctx, cancel := context.WithCancel(context.Background())
	go dispatcher.Run(ctx)

	// Wait for delivery
	deadline := time.After(2 * time.Second)
	for {
		if ds.getStatus("dlv-001") == "delivered" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for delivery, status=%s", ds.getStatus("dlv-001"))
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	// Verify payload was received
	if string(received) != string(payload) {
		t.Errorf("payload mismatch: got %s", string(received))
	}

	// Verify headers
	if headers.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type: got %s", headers.Get("Content-Type"))
	}
	if headers.Get("X-Spine-Event") != "run_started" {
		t.Errorf("X-Spine-Event: got %s", headers.Get("X-Spine-Event"))
	}
	if headers.Get("X-Spine-Delivery") != "dlv-001" {
		t.Errorf("X-Spine-Delivery: got %s", headers.Get("X-Spine-Delivery"))
	}

	// Verify HMAC signature
	sig := headers.Get("X-Spine-Signature")
	if sig == "" {
		t.Fatal("missing X-Spine-Signature")
	}
	expectedMAC := computeHMAC(payload, "test-secret")
	if sig != "sha256="+expectedMAC {
		t.Errorf("signature mismatch: got %s, want sha256=%s", sig, expectedMAC)
	}

	// Verify delivery log was recorded
	logs := ds.getLogs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	if logs[0].DeliveryID != "dlv-001" {
		t.Errorf("log delivery_id: got %s", logs[0].DeliveryID)
	}
	if logs[0].StatusCode == nil || *logs[0].StatusCode != 200 {
		t.Errorf("log status_code: got %v", logs[0].StatusCode)
	}
}

func TestWebhookDispatcher_FailedDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	ds := newDispatcherStore()
	resolver := &staticResolver{sub: &SubscriptionDetail{
		SubscriptionID: "sub-1",
		TargetURL:      server.URL,
	}}

	ds.addToQueue(store.DeliveryEntry{
		DeliveryID:     "dlv-fail",
		SubscriptionID: "sub-1",
		EventID:        "evt-2",
		EventType:      "step_assigned",
		Payload:        []byte(`{"event_id":"evt-2"}`),
		Status:         "delivering",
		CreatedAt:      time.Now().UTC(),
	})

	dispatcher := NewWebhookDispatcher(
		newDispatcherMinimalStore(ds),
		resolver,
		DispatcherConfig{Concurrency: 1, PollInterval: 50 * time.Millisecond},
	)

	ctx, cancel := context.WithCancel(context.Background())
	go dispatcher.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		if ds.getStatus("dlv-fail") != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for status update")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	if ds.getStatus("dlv-fail") != "failed" {
		t.Errorf("expected status failed, got %s", ds.getStatus("dlv-fail"))
	}

	logs := ds.getLogs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	if logs[0].StatusCode == nil || *logs[0].StatusCode != 500 {
		t.Errorf("log status_code: got %v", logs[0].StatusCode)
	}
}

func TestWebhookDispatcher_NoSignatureWithoutSecret(t *testing.T) {
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header
		w.WriteHeader(200)
	}))
	defer server.Close()

	ds := newDispatcherStore()
	resolver := &staticResolver{sub: &SubscriptionDetail{
		SubscriptionID: "sub-1",
		TargetURL:      server.URL,
		SigningSecret:  "", // no secret
	}}

	ds.addToQueue(store.DeliveryEntry{
		DeliveryID:     "dlv-nosig",
		SubscriptionID: "sub-1",
		EventID:        "evt-3",
		EventType:      "run_completed",
		Payload:        []byte(`{"event_id":"evt-3"}`),
		Status:         "delivering",
		CreatedAt:      time.Now().UTC(),
	})

	dispatcher := NewWebhookDispatcher(
		newDispatcherMinimalStore(ds),
		resolver,
		DispatcherConfig{Concurrency: 1, PollInterval: 50 * time.Millisecond},
	)

	ctx, cancel := context.WithCancel(context.Background())
	go dispatcher.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		if ds.getStatus("dlv-nosig") == "delivered" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()

	if headers.Get("X-Spine-Signature") != "" {
		t.Errorf("expected no signature header, got %s", headers.Get("X-Spine-Signature"))
	}
}

// TestWebhookDispatcher_RejectsPersistedUnsafeURL proves the dispatcher
// refuses to deliver to a persisted SSRF-flavoured target_url even
// when the resolver returns it — the validator is consulted on every
// dispatch, not just at subscription create time.
func TestWebhookDispatcher_RejectsPersistedUnsafeURL(t *testing.T) {
	ds := newDispatcherStore()
	resolver := &staticResolver{sub: &SubscriptionDetail{
		SubscriptionID: "sub-bad",
		TargetURL:      "http://169.254.169.254/latest/meta-data/",
	}}
	ds.addToQueue(store.DeliveryEntry{
		DeliveryID:     "dlv-bad",
		SubscriptionID: "sub-bad",
		EventID:        "evt-1",
		EventType:      "run_started",
		Payload:        []byte(`{}`),
		Status:         "delivering",
		CreatedAt:      time.Now().UTC(),
	})

	dispatcher := NewWebhookDispatcher(
		newDispatcherMinimalStore(ds),
		resolver,
		DispatcherConfig{
			Concurrency:  1,
			PollInterval: 50 * time.Millisecond,
			Targets:      NewTargetValidator(nil),
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatcher.Run(ctx)

	deadline := time.After(2 * time.Second)
	for {
		if ds.getStatus("dlv-bad") == "failed" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected delivery to be marked failed, got status=%q", ds.getStatus("dlv-bad"))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestComputeHMAC(t *testing.T) {
	payload := []byte(`{"test":"data"}`)
	secret := "my-secret"

	result := computeHMAC(payload, secret)

	// Verify independently
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	if result != expected {
		t.Errorf("HMAC mismatch: got %s, want %s", result, expected)
	}
}

// dispatcherMinimalStore wraps dispatcherStore and satisfies store.Store.
type dispatcherMinimalStore struct {
	ds *dispatcherStore
	minimalStore
}

func newDispatcherMinimalStore(ds *dispatcherStore) *dispatcherMinimalStore {
	return &dispatcherMinimalStore{ds: ds, minimalStore: minimalStore{fake: &fakeStore{}}}
}

func (s *dispatcherMinimalStore) ClaimDeliveries(ctx context.Context, limit int) ([]store.DeliveryEntry, error) {
	return s.ds.ClaimDeliveries(ctx, limit)
}

func (s *dispatcherMinimalStore) UpdateDeliveryStatus(ctx context.Context, deliveryID, status string, lastError string, nextRetryAt *time.Time) error {
	return s.ds.UpdateDeliveryStatus(ctx, deliveryID, status, lastError, nextRetryAt)
}

func (s *dispatcherMinimalStore) MarkDelivered(ctx context.Context, deliveryID string) error {
	return s.ds.MarkDelivered(ctx, deliveryID)
}

func (s *dispatcherMinimalStore) LogDeliveryAttempt(ctx context.Context, entry *store.DeliveryLogEntry) error {
	return s.ds.LogDeliveryAttempt(ctx, entry)
}
