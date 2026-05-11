package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/auth"
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

// authCaptureStore wraps stubStore with in-memory actor/token tracking
// so the pooled-builder's BootstrapInternalAdmin call is observable
// end-to-end at the cmd_serve gateway boundary.
type authCaptureStore struct {
	stubStore
	mu     sync.Mutex
	actors map[string]*domain.Actor
	tokens map[string]*store.TokenRecord
	subs   []store.EventSubscription
}

func newAuthCaptureStore() *authCaptureStore {
	return &authCaptureStore{
		actors: make(map[string]*domain.Actor),
		tokens: make(map[string]*store.TokenRecord),
	}
}

func (a *authCaptureStore) GetActor(_ context.Context, actorID string) (*domain.Actor, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	actor, ok := a.actors[actorID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	clone := *actor
	return &clone, nil
}

func (a *authCaptureStore) CreateActor(_ context.Context, actor *domain.Actor) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.actors[actor.ActorID]; ok {
		return domain.NewError(domain.ErrConflict, "actor_id already exists")
	}
	clone := *actor
	a.actors[actor.ActorID] = &clone
	return nil
}

func (a *authCaptureStore) UpdateActor(_ context.Context, actor *domain.Actor) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	clone := *actor
	a.actors[actor.ActorID] = &clone
	return nil
}

func (a *authCaptureStore) GetActorByTokenHash(_ context.Context, hash string) (*domain.Actor, *domain.Token, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.tokens[hash]
	if !ok {
		return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
	}
	actor, ok := a.actors[rec.ActorID]
	if !ok {
		return nil, nil, domain.NewError(domain.ErrUnauthorized, "actor not found")
	}
	actorClone := *actor
	tok := &domain.Token{TokenID: rec.TokenID, ActorID: rec.ActorID, Name: rec.Name, CreatedAt: rec.CreatedAt}
	return &actorClone, tok, nil
}

func (a *authCaptureStore) CreateToken(_ context.Context, rec *store.TokenRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	clone := *rec
	a.tokens[rec.TokenHash] = &clone
	return nil
}

func (a *authCaptureStore) ListSubscriptions(_ context.Context, _ string) ([]store.EventSubscription, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]store.EventSubscription, len(a.subs))
	copy(out, a.subs)
	return out, nil
}

func (a *authCaptureStore) ListActiveSubscriptionsByEventType(_ context.Context, _ string) ([]store.EventSubscription, error) {
	return nil, nil
}

func (a *authCaptureStore) tokenCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.tokens)
}

func (a *authCaptureStore) actorByID(id string) *domain.Actor {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.actors[id] == nil {
		return nil
	}
	clone := *a.actors[id]
	return &clone
}

// TestBootstrapInternalAdmin_RunsWhenTokenSet proves the admin
// bootstrap seeds smp-admin rows regardless of the event-delivery gate:
// a platform-binding deployment uses bearer auth on every workspace
// request whether or not it subscribes to events, so gating the
// bootstrap on SPINE_EVENT_DELIVERY would defeat the very 401 it
// exists to prevent.
func TestBootstrapInternalAdmin_RunsWhenTokenSet(t *testing.T) {
	ctx := context.Background()
	cs := newAuthCaptureStore()

	cfg := workspaceDeliveryConfig{
		Enabled:       false, // delivery off — admin bootstrap must still run
		SMPAdminToken: "smp-bearer",
	}
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-admin"}, Store: cs}

	if err := bootstrapInternalAdmin(ctx, ss, cfg, observe.Logger(ctx)); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if cs.actorByID(auth.InternalAdminActorID) == nil {
		t.Fatalf("expected smp-admin actor seeded by bootstrap")
	}
	if got := cs.tokenCount(); got != 1 {
		t.Fatalf("expected 1 token row, got %d", got)
	}

	// Re-running (idle eviction → re-load) must be idempotent.
	if err := bootstrapInternalAdmin(ctx, ss, cfg, observe.Logger(ctx)); err != nil {
		t.Fatalf("re-bootstrap: %v", err)
	}
	if got := cs.tokenCount(); got != 1 {
		t.Errorf("expected token bootstrap to be idempotent, got %d rows", got)
	}
}

// TestBootstrapInternalAdmin_NoOpWhenTokenUnset preserves the
// single-workspace / pre-platform-binding default.
func TestBootstrapInternalAdmin_NoOpWhenTokenUnset(t *testing.T) {
	ctx := context.Background()
	cs := newAuthCaptureStore()

	cfg := workspaceDeliveryConfig{Enabled: true}
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-no-admin"}, Store: cs}

	if err := bootstrapInternalAdmin(ctx, ss, cfg, observe.Logger(ctx)); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if cs.actorByID(auth.InternalAdminActorID) != nil {
		t.Errorf("expected no smp-admin actor when SMP_ADMIN_TOKEN unset")
	}
	if got := cs.tokenCount(); got != 0 {
		t.Errorf("expected no token rows, got %d", got)
	}
}

// TestBootstrapInternalAdmin_NoOpWhenStoreNil guards against the early-
// bootstrap window where the workspace ServiceSet has no store yet.
func TestBootstrapInternalAdmin_NoOpWhenStoreNil(t *testing.T) {
	ctx := context.Background()
	cfg := workspaceDeliveryConfig{SMPAdminToken: "bearer"}
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-nostore"}}

	// Must not panic on a nil store.
	if err := bootstrapInternalAdmin(ctx, ss, cfg, observe.Logger(ctx)); err != nil {
		t.Fatalf("bootstrap on nil store should be a no-op, got %v", err)
	}
}

// TestBootstrapInternalAdmin_StrictProductionFailsOnCollision verifies
// that under SPINE_ENV=production (cfg.ProductionStrict=true), an
// auth.ErrAdminTokenHashCollision is surfaced so the pool builder
// fails workspace load loudly — matching the strict-startup philosophy
// that gates SPINE_DEV_MODE / SPINE_SECRET_ENCRYPTION_KEY at boot.
//
// The historical warn-and-continue behaviour hid this under sampled
// logging while leaving every workspace request 401-ing; the strict
// failure points operators at the colliding auth.tokens row directly.
func TestBootstrapInternalAdmin_StrictProductionFailsOnCollision(t *testing.T) {
	ctx := context.Background()
	cs := newAuthCaptureStore()

	const bearer = "platform-bearer"
	hash := auth.HashToken(bearer)
	// Pre-seed a non-bootstrap actor + token row whose hash collides
	// with SMP_ADMIN_TOKEN. This is the deliberate-reuse case (an
	// operator pasted the platform bearer onto a human/service actor);
	// with a 256-bit hash, random collision is unreachable.
	collidingActor := "human-operator-42"
	cs.actors[collidingActor] = &domain.Actor{
		ActorID: collidingActor,
		Type:    domain.ActorTypeHuman,
		Name:    "Operator",
		Role:    domain.RoleReader,
		Status:  domain.ActorStatusActive,
	}
	cs.tokens[hash] = &store.TokenRecord{
		TokenID:   "operator-token",
		ActorID:   collidingActor,
		TokenHash: hash,
		Name:      "operator personal bearer",
	}

	cfg := workspaceDeliveryConfig{
		SMPAdminToken:    bearer,
		ProductionStrict: true,
	}
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-prod"}, Store: cs}

	err := bootstrapInternalAdmin(ctx, ss, cfg, observe.Logger(ctx))
	if err == nil {
		t.Fatalf("expected collision to surface as error in production, got nil")
	}
	if !errors.Is(err, auth.ErrAdminTokenHashCollision) {
		t.Fatalf("expected ErrAdminTokenHashCollision, got %v", err)
	}
	// Workspace ID must appear in the error so the workspace.ServicePool
	// log line ("workspace X failed to load: ...") names the offender.
	if msg := err.Error(); !strings.Contains(msg, "ws-prod") {
		t.Errorf("error message %q should name the workspace ID", msg)
	}
	// Strict failure must not have rebound the colliding row — that's
	// the whole point of returning instead of overwriting.
	if cs.tokens[hash].ActorID != collidingActor {
		t.Errorf("colliding row was rebound: %q -> %q", collidingActor, cs.tokens[hash].ActorID)
	}
}

// TestBootstrapInternalAdmin_NonProductionLogsCollision: outside
// production, the same collision must be logged but NOT bubble up,
// preserving the dev-convenience swallow behaviour BootstrapInternal-
// Subscription also uses (see wireWorkspaceDelivery). This keeps dev
// stacks runnable when an operator re-uses a token for testing.
func TestBootstrapInternalAdmin_NonProductionLogsCollision(t *testing.T) {
	ctx := context.Background()
	cs := newAuthCaptureStore()

	const bearer = "platform-bearer"
	hash := auth.HashToken(bearer)
	collidingActor := "service-x"
	cs.actors[collidingActor] = &domain.Actor{
		ActorID: collidingActor,
		Type:    domain.ActorTypeAutomated,
		Name:    "Service X",
		Role:    domain.RoleReader,
		Status:  domain.ActorStatusActive,
	}
	cs.tokens[hash] = &store.TokenRecord{
		TokenID:   "service-x-token",
		ActorID:   collidingActor,
		TokenHash: hash,
	}

	cfg := workspaceDeliveryConfig{
		SMPAdminToken:    bearer,
		ProductionStrict: false,
	}
	ss := &workspace.ServiceSet{Config: workspace.Config{ID: "ws-dev"}, Store: cs}

	if err := bootstrapInternalAdmin(ctx, ss, cfg, observe.Logger(ctx)); err != nil {
		t.Fatalf("non-production collision must not bubble up: %v", err)
	}
	if cs.tokens[hash].ActorID != collidingActor {
		t.Errorf("colliding row was rebound: %q -> %q", collidingActor, cs.tokens[hash].ActorID)
	}
}

// TestLoadWorkspaceDeliveryConfig_ProductionStrictFromEnv anchors that
// the env-derived workspaceDeliveryConfig sets ProductionStrict from
// SPINE_ENV. Without this, the bootstrapInternalAdmin strict path would
// only be exercised through a struct-literal — operators changing
// SPINE_ENV at runtime would silently lose the strict-fail behaviour.
func TestLoadWorkspaceDeliveryConfig_ProductionStrictFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"production", true},
		{"PRODUCTION", true},
		{"staging", false},
		{"development", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SPINE_ENV", tc.env)
			cfg := loadWorkspaceDeliveryConfig()
			if cfg.ProductionStrict != tc.want {
				t.Errorf("SPINE_ENV=%q: ProductionStrict = %v, want %v", tc.env, cfg.ProductionStrict, tc.want)
			}
		})
	}
}

// TestLoadWorkspaceDeliveryConfig_ReadsAdminToken anchors the env
// contract for the bootstrap admin path.
func TestLoadWorkspaceDeliveryConfig_ReadsAdminToken(t *testing.T) {
	t.Setenv("SPINE_EVENT_DELIVERY", "true")
	t.Setenv("SMP_ADMIN_TOKEN", "platform-bearer")

	cfg := loadWorkspaceDeliveryConfig()
	if cfg.SMPAdminToken != "platform-bearer" {
		t.Errorf("SMPAdminToken = %q, want platform-bearer", cfg.SMPAdminToken)
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
