package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workspace"
)

// captureStore wraps stubStore and records WriteEventLog and
// EnqueueDelivery calls, so wireWorkspaceDelivery's effect on the
// per-workspace store can be observed end-to-end without a real DB.
type captureStore struct {
	stubStore
	mu       sync.Mutex
	eventLog []store.EventLogEntry
	delivery []store.DeliveryEntry
	subs     []store.EventSubscription
}

func (c *captureStore) WriteEventLog(_ context.Context, entry *store.EventLogEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventLog = append(c.eventLog, *entry)
	return nil
}

func (c *captureStore) EnqueueDelivery(_ context.Context, entry *store.DeliveryEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delivery = append(c.delivery, *entry)
	return nil
}

func (c *captureStore) ListSubscriptions(_ context.Context, _ string) ([]store.EventSubscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]store.EventSubscription, len(c.subs))
	copy(out, c.subs)
	return out, nil
}

func (c *captureStore) ListActiveSubscriptionsByEventType(_ context.Context, eventType string) ([]store.EventSubscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []store.EventSubscription
	for _, sub := range c.subs {
		if sub.Status != "active" {
			continue
		}
		if len(sub.EventTypes) == 0 {
			out = append(out, sub)
			continue
		}
		for _, et := range sub.EventTypes {
			if et == eventType {
				out = append(out, sub)
				break
			}
		}
	}
	return out, nil
}

func (c *captureStore) CreateSubscription(_ context.Context, sub *store.EventSubscription) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs = append(c.subs, *sub)
	return nil
}

func (c *captureStore) eventLogSnapshot() []store.EventLogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]store.EventLogEntry, len(c.eventLog))
	copy(out, c.eventLog)
	return out
}

func (c *captureStore) deliverySnapshot() []store.DeliveryEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]store.DeliveryEntry, len(c.delivery))
	copy(out, c.delivery)
	return out
}

// TestLoadWorkspaceDeliveryConfig anchors the env contract that drives
// per-workspace delivery wiring. SPINE_EVENT_DELIVERY toggles the
// feature; the rest provides target URL, signing token, retention, and
// (implicitly) the SSRF allow-list. SMP_WORKSPACE_ID is intentionally
// not in the struct — pooled modes get it from the per-workspace
// binding.
func TestLoadWorkspaceDeliveryConfig_ReadsEnv(t *testing.T) {
	t.Setenv("SPINE_EVENT_DELIVERY", "true")
	t.Setenv("SMP_EVENT_URL", "http://customer-app:8080/internal/step-events")
	t.Setenv("SMP_INTERNAL_TOKEN", "secret-token")
	t.Setenv("SPINE_EVENT_RETENTION", "168h")
	t.Setenv("SPINE_WEBHOOK_ALLOWED_HOSTS", "")

	cfg := loadWorkspaceDeliveryConfig()

	if !cfg.Enabled {
		t.Error("Enabled should be true when SPINE_EVENT_DELIVERY=true")
	}
	if cfg.SMPEventURL != "http://customer-app:8080/internal/step-events" {
		t.Errorf("SMPEventURL = %q", cfg.SMPEventURL)
	}
	if cfg.SMPInternalToken != "secret-token" {
		t.Errorf("SMPInternalToken = %q", cfg.SMPInternalToken)
	}
	if cfg.EventRetention != 168*time.Hour {
		t.Errorf("EventRetention = %v, want 168h", cfg.EventRetention)
	}
	if cfg.WebhookTargets == nil {
		t.Error("WebhookTargets should always be non-nil so the dispatcher's SSRF gate is wired")
	}
}

func TestLoadWorkspaceDeliveryConfig_DisabledByDefault(t *testing.T) {
	t.Setenv("SPINE_EVENT_DELIVERY", "")
	t.Setenv("SMP_EVENT_URL", "")
	t.Setenv("SMP_INTERNAL_TOKEN", "")
	t.Setenv("SPINE_EVENT_RETENTION", "")
	t.Setenv("SPINE_WEBHOOK_ALLOWED_HOSTS", "")

	cfg := loadWorkspaceDeliveryConfig()

	if cfg.Enabled {
		t.Error("Enabled should be false when SPINE_EVENT_DELIVERY is unset — delivery is opt-in")
	}
}

func TestLoadWorkspaceDeliveryConfig_InvalidRetentionSilentlyZero(t *testing.T) {
	t.Setenv("SPINE_EVENT_DELIVERY", "true")
	t.Setenv("SMP_EVENT_URL", "")
	t.Setenv("SMP_INTERNAL_TOKEN", "")
	t.Setenv("SPINE_EVENT_RETENTION", "not-a-duration")
	t.Setenv("SPINE_WEBHOOK_ALLOWED_HOSTS", "")

	cfg := loadWorkspaceDeliveryConfig()

	if cfg.EventRetention != 0 {
		t.Errorf("invalid retention should fall back to zero (no cleanup); got %v", cfg.EventRetention)
	}
}

// TestWireWorkspaceDelivery_PersistsEventToWorkspaceStore is the
// regression test for the bug TASK-003 fixes. Before the fix, an event
// emitted on a per-workspace event router never reached any subscriber
// in platform-binding mode, so runtime.event_log stayed empty and no
// webhook fired. With the fix, the per-workspace subscriber writes the
// event into the per-workspace store's event log within a deterministic
// window.
func TestWireWorkspaceDelivery_PersistsEventToWorkspaceStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &captureStore{}
	q := queue.NewMemoryQueue(16)
	go q.Start(ctx)
	t.Cleanup(q.Stop)
	router := event.NewQueueRouter(q)

	ss := &workspace.ServiceSet{
		Config: workspace.Config{ID: "test-ws"},
		Store:  cs,
		Events: router,
	}
	cfg := workspaceDeliveryConfig{
		Enabled:        true,
		WebhookTargets: delivery.NewTargetValidator(nil),
		EventRetention: time.Hour,
	}

	wireWorkspaceDelivery(ctx, ss, cfg, observe.Logger(ctx))

	if err := router.Emit(ctx, domain.Event{
		EventID:   "evt-1",
		Type:      domain.EventStepCompleted,
		Timestamp: time.Now().UTC(),
		RunID:     "run-1",
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entries := cs.eventLogSnapshot(); len(entries) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	entries := cs.eventLogSnapshot()
	if len(entries) != 1 {
		t.Fatalf("expected 1 event log entry after emit, got %d (delivery subscriber not wired to per-workspace router)", len(entries))
	}
	if entries[0].EventID != "evt-1" {
		t.Errorf("event log entry id = %q, want evt-1", entries[0].EventID)
	}
	if entries[0].EventType != string(domain.EventStepCompleted) {
		t.Errorf("event log entry type = %q, want step.completed", entries[0].EventType)
	}
}

// When an active subscription matches the emitted event type, the
// subscriber must enqueue a delivery row to the per-workspace store.
// This is the second half of the AC: subscriptions create deliveries.
func TestWireWorkspaceDelivery_EnqueuesDeliveryForActiveSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &captureStore{
		subs: []store.EventSubscription{
			{
				SubscriptionID: "sub-1",
				WorkspaceID:    "smp-test",
				Status:         "active",
				EventTypes:     []string{string(domain.EventStepCompleted)},
				TargetURL:      "http://customer-app:8080/internal/step-events",
			},
		},
	}
	q := queue.NewMemoryQueue(16)
	go q.Start(ctx)
	t.Cleanup(q.Stop)
	router := event.NewQueueRouter(q)

	ss := &workspace.ServiceSet{
		Config: workspace.Config{ID: "test-ws"},
		Store:  cs,
		Events: router,
	}
	cfg := workspaceDeliveryConfig{
		Enabled:        true,
		WebhookTargets: delivery.NewTargetValidator(nil),
	}

	wireWorkspaceDelivery(ctx, ss, cfg, observe.Logger(ctx))

	if err := router.Emit(ctx, domain.Event{
		EventID:   "evt-step",
		Type:      domain.EventStepCompleted,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entries := cs.deliverySnapshot(); len(entries) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	deliveries := cs.deliverySnapshot()
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery row, got %d", len(deliveries))
	}
	if deliveries[0].SubscriptionID != "sub-1" {
		t.Errorf("delivery subscription_id = %q, want sub-1", deliveries[0].SubscriptionID)
	}
	if deliveries[0].EventType != string(domain.EventStepCompleted) {
		t.Errorf("delivery event_type = %q, want step.completed", deliveries[0].EventType)
	}
}

// TestNewPooledWorkspaceBuilder_DeliveryDisabled_NoWiring proves the
// composition is conditional on the env flag — disabling
// SPINE_EVENT_DELIVERY must keep a workspace's lifecycle fully
// orchestrator-only. With Store==nil, workspaceOrchestratorBuilder
// short-circuits, so this test exercises the gate alone.
func TestNewPooledWorkspaceBuilder_DeliveryDisabled_NoWiring(t *testing.T) {
	cfg := workspaceDeliveryConfig{Enabled: false}
	builder := newPooledWorkspaceBuilder(cfg, observe.Logger(context.Background()))

	ss := &workspace.ServiceSet{}
	if err := builder(context.Background(), ss); err != nil {
		t.Fatalf("builder returned err: %v", err)
	}
	// No closer should be installed; ss.close stays as zero value, which
	// the pool will treat as a no-op when called.
}

// TestNewPooledWorkspaceBuilder_NilStore_SkipsDelivery: even with
// delivery enabled, a workspace that has no store yet (early bootstrap)
// must not wire delivery — the subscriber and dispatcher both require
// a store to read/write subscriptions and event logs. Skipping cleanly
// preserves the pre-fix behavior for that early window.
func TestNewPooledWorkspaceBuilder_NilStore_SkipsDelivery(t *testing.T) {
	cfg := workspaceDeliveryConfig{
		Enabled:        true,
		WebhookTargets: delivery.NewTargetValidator(nil),
	}
	builder := newPooledWorkspaceBuilder(cfg, observe.Logger(context.Background()))

	ss := &workspace.ServiceSet{} // no Store, no Events
	if err := builder(context.Background(), ss); err != nil {
		t.Fatalf("builder returned err: %v", err)
	}
}
