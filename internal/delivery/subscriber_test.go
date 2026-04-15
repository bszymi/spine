package delivery

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
)

// fakeStore captures enqueued deliveries.
type fakeStore struct {
	mu        sync.Mutex
	deliveries []store.DeliveryEntry
}

func (f *fakeStore) EnqueueDelivery(_ context.Context, entry *store.DeliveryEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = append(f.deliveries, *entry)
	return nil
}

func (f *fakeStore) getDeliveries() []store.DeliveryEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]store.DeliveryEntry, len(f.deliveries))
	copy(result, f.deliveries)
	return result
}

// staticSubscriptions returns the same subscriptions for all event types.
type staticSubscriptions struct {
	subs []Subscription
}

func (s *staticSubscriptions) ListActiveSubscriptions(_ context.Context, _ domain.EventType) ([]Subscription, error) {
	return s.subs, nil
}

// filteredSubscriptions returns subscriptions only for matching event types.
type filteredSubscriptions struct {
	subs []Subscription
}

func (f *filteredSubscriptions) ListActiveSubscriptions(_ context.Context, eventType domain.EventType) ([]Subscription, error) {
	var result []Subscription
	for _, sub := range f.subs {
		if len(sub.EventTypes) == 0 {
			result = append(result, sub)
			continue
		}
		for _, et := range sub.EventTypes {
			if et == string(eventType) {
				result = append(result, sub)
				break
			}
		}
	}
	return result, nil
}

func TestDeliverySubscriber_EnqueuesForMatchingSubscriptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &fakeStore{}
	subs := &staticSubscriptions{
		subs: []Subscription{
			{SubscriptionID: "sub-1"},
			{SubscriptionID: "sub-2"},
		},
	}

	subscriber := NewDeliverySubscriber(newMinimalStore(fs), subs)

	q := queue.NewMemoryQueue(100)
	go q.Start(ctx)

	router := newTestRouter(q)
	if err := subscriber.Subscribe(ctx, router); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	evt := domain.Event{
		EventID:   "evt-001",
		Type:      domain.EventRunStarted,
		Timestamp: time.Now().UTC(),
	}
	if err := router.Emit(ctx, evt); err != nil {
		t.Fatalf("emit: %v", err)
	}

	// Wait for async delivery
	deadline := time.After(2 * time.Second)
	for {
		deliveries := fs.getDeliveries()
		if len(deliveries) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for deliveries, got %d", len(fs.getDeliveries()))
		case <-time.After(10 * time.Millisecond):
		}
	}

	deliveries := fs.getDeliveries()
	if len(deliveries) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(deliveries))
	}

	// Verify both subscriptions received the event
	subIDs := map[string]bool{}
	for _, d := range deliveries {
		subIDs[d.SubscriptionID] = true
		if d.EventID != "evt-001" {
			t.Errorf("expected event_id evt-001, got %s", d.EventID)
		}
		if d.EventType != "run_started" {
			t.Errorf("expected event_type run_started, got %s", d.EventType)
		}
		if d.Status != "pending" {
			t.Errorf("expected status pending, got %s", d.Status)
		}

		// Verify payload is the marshaled event
		var decoded domain.Event
		if err := json.Unmarshal(d.Payload, &decoded); err != nil {
			t.Errorf("unmarshal payload: %v", err)
		}
		if decoded.EventID != "evt-001" {
			t.Errorf("payload event_id: got %s, want evt-001", decoded.EventID)
		}
	}
	if !subIDs["sub-1"] || !subIDs["sub-2"] {
		t.Errorf("expected both sub-1 and sub-2, got %v", subIDs)
	}
}

func TestDeliverySubscriber_NoSubscriptionsNoDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &fakeStore{}
	subs := &staticSubscriptions{subs: nil}

	subscriber := NewDeliverySubscriber(newMinimalStore(fs), subs)

	q := queue.NewMemoryQueue(100)
	go q.Start(ctx)

	router := newTestRouter(q)
	if err := subscriber.Subscribe(ctx, router); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	evt := domain.Event{
		EventID:   "evt-002",
		Type:      domain.EventStepAssigned,
		Timestamp: time.Now().UTC(),
	}
	if err := router.Emit(ctx, evt); err != nil {
		t.Fatalf("emit: %v", err)
	}

	// Give time for potential delivery
	time.Sleep(200 * time.Millisecond)

	deliveries := fs.getDeliveries()
	if len(deliveries) != 0 {
		t.Errorf("expected 0 deliveries, got %d", len(deliveries))
	}
}

func TestDeliverySubscriber_FiltersByEventType(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &fakeStore{}
	subs := &filteredSubscriptions{
		subs: []Subscription{
			{SubscriptionID: "sub-all", EventTypes: nil},                               // matches all
			{SubscriptionID: "sub-runs", EventTypes: []string{"run_started", "run_completed"}}, // matches run events only
		},
	}

	subscriber := NewDeliverySubscriber(newMinimalStore(fs), subs)

	q := queue.NewMemoryQueue(100)
	go q.Start(ctx)

	router := newTestRouter(q)
	if err := subscriber.Subscribe(ctx, router); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Emit a step event — only sub-all should match
	if err := router.Emit(ctx, domain.Event{
		EventID:   "evt-step",
		Type:      domain.EventStepCompleted,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		deliveries := fs.getDeliveries()
		if len(deliveries) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for deliveries")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Small delay to ensure no more come in
	time.Sleep(100 * time.Millisecond)

	deliveries := fs.getDeliveries()
	if len(deliveries) != 1 {
		t.Fatalf("expected 1 delivery (sub-all only), got %d", len(deliveries))
	}
	if deliveries[0].SubscriptionID != "sub-all" {
		t.Errorf("expected sub-all, got %s", deliveries[0].SubscriptionID)
	}
}

// newTestRouter creates a QueueRouter for testing. This duplicates the
// event.NewQueueRouter constructor to avoid a circular dependency.
func newTestRouter(q queue.Queue) *testRouter {
	return &testRouter{q: q, handlers: make(map[domain.EventType][]event.EventHandler)}
}

type testRouter struct {
	q        queue.Queue
	handlers map[domain.EventType][]event.EventHandler
}

func (r *testRouter) Emit(ctx context.Context, evt domain.Event) error {
	payload, _ := json.Marshal(evt)
	return r.q.Publish(ctx, queue.Entry{
		EntryID:        evt.EventID,
		EntryType:      "event_delivery",
		Payload:        payload,
		IdempotencyKey: evt.EventID,
		CreatedAt:      evt.Timestamp,
	})
}

func (r *testRouter) Subscribe(ctx context.Context, eventType domain.EventType, handler event.EventHandler) error {
	r.handlers[eventType] = append(r.handlers[eventType], handler)
	return r.q.Subscribe(ctx, "event_delivery", func(ctx context.Context, entry queue.Entry) error {
		var e domain.Event
		if err := json.Unmarshal(entry.Payload, &e); err != nil {
			return err
		}
		if e.Type != eventType {
			return nil
		}
		return handler(ctx, e)
	})
}

// minimalStore wraps fakeStore and satisfies store.Store for the methods
// the subscriber uses. Other methods panic (not called in these tests).
type minimalStore struct {
	fake *fakeStore
}

func newMinimalStore(fs *fakeStore) *minimalStore {
	return &minimalStore{fake: fs}
}

func (m *minimalStore) EnqueueDelivery(ctx context.Context, entry *store.DeliveryEntry) error {
	return m.fake.EnqueueDelivery(ctx, entry)
}

// Unused store.Store methods — the subscriber only calls EnqueueDelivery.
func (m *minimalStore) WithTx(context.Context, func(store.Tx) error) error { panic("not used") }
func (m *minimalStore) Ping(context.Context) error                         { panic("not used") }
func (m *minimalStore) CreateRun(context.Context, *domain.Run) error       { panic("not used") }
func (m *minimalStore) GetRun(context.Context, string) (*domain.Run, error) {
	panic("not used")
}
func (m *minimalStore) UpdateRunStatus(context.Context, string, domain.RunStatus) error {
	panic("not used")
}
func (m *minimalStore) TransitionRunStatus(context.Context, string, domain.RunStatus, domain.RunStatus) (bool, error) {
	panic("not used")
}
func (m *minimalStore) UpdateCurrentStep(context.Context, string, string) error { panic("not used") }
func (m *minimalStore) SetCommitMeta(context.Context, string, map[string]string) error {
	panic("not used")
}
func (m *minimalStore) ListRunsByTask(context.Context, string) ([]domain.Run, error) {
	panic("not used")
}
func (m *minimalStore) CreateStepExecution(context.Context, *domain.StepExecution) error {
	panic("not used")
}
func (m *minimalStore) GetStepExecution(context.Context, string) (*domain.StepExecution, error) {
	panic("not used")
}
func (m *minimalStore) UpdateStepExecution(context.Context, *domain.StepExecution) error {
	panic("not used")
}
func (m *minimalStore) ListStepExecutionsByRun(context.Context, string) ([]domain.StepExecution, error) {
	panic("not used")
}
func (m *minimalStore) UpsertArtifactProjection(context.Context, *store.ArtifactProjection) error {
	panic("not used")
}
func (m *minimalStore) DeleteArtifactProjection(context.Context, string) error { panic("not used") }
func (m *minimalStore) GetArtifactProjection(context.Context, string) (*store.ArtifactProjection, error) {
	panic("not used")
}
func (m *minimalStore) QueryArtifacts(context.Context, store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	panic("not used")
}
func (m *minimalStore) DeleteAllProjections(context.Context) error { panic("not used") }
func (m *minimalStore) UpsertArtifactLinks(context.Context, string, []store.ArtifactLink, string) error {
	panic("not used")
}
func (m *minimalStore) DeleteArtifactLinks(context.Context, string) error { panic("not used") }
func (m *minimalStore) QueryArtifactLinks(context.Context, string) ([]store.ArtifactLink, error) {
	panic("not used")
}
func (m *minimalStore) QueryArtifactLinksByTarget(context.Context, string) ([]store.ArtifactLink, error) {
	panic("not used")
}
func (m *minimalStore) GetActor(context.Context, string) (*domain.Actor, error) { panic("not used") }
func (m *minimalStore) CreateActor(context.Context, *domain.Actor) error        { panic("not used") }
func (m *minimalStore) UpdateActor(context.Context, *domain.Actor) error        { panic("not used") }
func (m *minimalStore) ListActors(context.Context) ([]domain.Actor, error)      { panic("not used") }
func (m *minimalStore) ListActorsByStatus(context.Context, domain.ActorStatus) ([]domain.Actor, error) {
	panic("not used")
}
func (m *minimalStore) GetActorByTokenHash(context.Context, string) (*domain.Actor, *domain.Token, error) {
	panic("not used")
}
func (m *minimalStore) CreateToken(context.Context, *store.TokenRecord) error  { panic("not used") }
func (m *minimalStore) RevokeToken(context.Context, string) error              { panic("not used") }
func (m *minimalStore) ListTokensByActor(context.Context, string) ([]domain.Token, error) {
	panic("not used")
}
func (m *minimalStore) CreateDivergenceContext(context.Context, *domain.DivergenceContext) error {
	panic("not used")
}
func (m *minimalStore) UpdateDivergenceContext(context.Context, *domain.DivergenceContext) error {
	panic("not used")
}
func (m *minimalStore) GetDivergenceContext(context.Context, string) (*domain.DivergenceContext, error) {
	panic("not used")
}
func (m *minimalStore) CreateBranch(context.Context, *domain.Branch) error  { panic("not used") }
func (m *minimalStore) UpdateBranch(context.Context, *domain.Branch) error  { panic("not used") }
func (m *minimalStore) GetBranch(context.Context, string) (*domain.Branch, error) {
	panic("not used")
}
func (m *minimalStore) ListBranchesByDivergence(context.Context, string) ([]domain.Branch, error) {
	panic("not used")
}
func (m *minimalStore) CreateAssignment(context.Context, *domain.Assignment) error {
	panic("not used")
}
func (m *minimalStore) UpdateAssignmentStatus(context.Context, string, domain.AssignmentStatus, *time.Time) error {
	panic("not used")
}
func (m *minimalStore) GetAssignment(context.Context, string) (*domain.Assignment, error) {
	panic("not used")
}
func (m *minimalStore) ListAssignmentsByActor(context.Context, string, *domain.AssignmentStatus) ([]domain.Assignment, error) {
	panic("not used")
}
func (m *minimalStore) ListExpiredAssignments(context.Context, time.Time) ([]domain.Assignment, error) {
	panic("not used")
}
func (m *minimalStore) ListRunsByStatus(context.Context, domain.RunStatus) ([]domain.Run, error) {
	panic("not used")
}
func (m *minimalStore) ListActiveStepExecutions(context.Context) ([]domain.StepExecution, error) {
	panic("not used")
}
func (m *minimalStore) ListStaleActiveRuns(context.Context, time.Time) ([]domain.Run, error) {
	panic("not used")
}
func (m *minimalStore) ListTimedOutRuns(context.Context, time.Time) ([]domain.Run, error) {
	panic("not used")
}
func (m *minimalStore) UpsertWorkflowProjection(context.Context, *store.WorkflowProjection) error {
	panic("not used")
}
func (m *minimalStore) DeleteWorkflowProjection(context.Context, string) error { panic("not used") }
func (m *minimalStore) GetWorkflowProjection(context.Context, string) (*store.WorkflowProjection, error) {
	panic("not used")
}
func (m *minimalStore) ListActiveWorkflowProjections(context.Context) ([]store.WorkflowProjection, error) {
	panic("not used")
}
func (m *minimalStore) GetSyncState(context.Context) (*store.SyncState, error) { panic("not used") }
func (m *minimalStore) UpdateSyncState(context.Context, *store.SyncState) error {
	panic("not used")
}
func (m *minimalStore) CreateThread(context.Context, *domain.DiscussionThread) error {
	panic("not used")
}
func (m *minimalStore) GetThread(context.Context, string) (*domain.DiscussionThread, error) {
	panic("not used")
}
func (m *minimalStore) ListThreads(context.Context, domain.AnchorType, string) ([]domain.DiscussionThread, error) {
	panic("not used")
}
func (m *minimalStore) UpdateThread(context.Context, *domain.DiscussionThread) error {
	panic("not used")
}
func (m *minimalStore) CreateComment(context.Context, *domain.Comment) error { panic("not used") }
func (m *minimalStore) ListComments(context.Context, string) ([]domain.Comment, error) {
	panic("not used")
}
func (m *minimalStore) HasOpenThreads(context.Context, domain.AnchorType, string) (bool, error) {
	panic("not used")
}
func (m *minimalStore) CreateSkill(context.Context, *domain.Skill) error  { panic("not used") }
func (m *minimalStore) GetSkill(context.Context, string) (*domain.Skill, error) {
	panic("not used")
}
func (m *minimalStore) UpdateSkill(context.Context, *domain.Skill) error    { panic("not used") }
func (m *minimalStore) ListSkills(context.Context) ([]domain.Skill, error)  { panic("not used") }
func (m *minimalStore) ListSkillsByCategory(context.Context, string) ([]domain.Skill, error) {
	panic("not used")
}
func (m *minimalStore) AddSkillToActor(context.Context, string, string) error    { panic("not used") }
func (m *minimalStore) RemoveSkillFromActor(context.Context, string, string) error {
	panic("not used")
}
func (m *minimalStore) ListActorSkills(context.Context, string) ([]domain.Skill, error) {
	panic("not used")
}
func (m *minimalStore) ListActorsBySkills(context.Context, []string) ([]domain.Actor, error) {
	panic("not used")
}
func (m *minimalStore) UpsertExecutionProjection(context.Context, *store.ExecutionProjection) error {
	panic("not used")
}
func (m *minimalStore) GetExecutionProjection(context.Context, string) (*store.ExecutionProjection, error) {
	panic("not used")
}
func (m *minimalStore) QueryExecutionProjections(context.Context, store.ExecutionProjectionQuery) ([]store.ExecutionProjection, error) {
	panic("not used")
}
func (m *minimalStore) DeleteExecutionProjection(context.Context, string) error { panic("not used") }
func (m *minimalStore) ClaimDeliveries(context.Context, int) ([]store.DeliveryEntry, error) {
	panic("not used")
}
func (m *minimalStore) UpdateDeliveryStatus(context.Context, string, string, string, *time.Time) error {
	panic("not used")
}
func (m *minimalStore) MarkDelivered(context.Context, string) error { panic("not used") }
func (m *minimalStore) LogDeliveryAttempt(context.Context, *store.DeliveryLogEntry) error {
	panic("not used")
}
func (m *minimalStore) ListDeliveryHistory(context.Context, store.DeliveryHistoryQuery) ([]store.DeliveryLogEntry, error) {
	panic("not used")
}
func (m *minimalStore) ListEventsAfter(context.Context, string, []string, int) ([]store.DeliveryEntry, error) {
	panic("not used")
}
func (m *minimalStore) GetDelivery(context.Context, string) (*store.DeliveryEntry, error) {
	panic("not used")
}
func (m *minimalStore) ListDeliveries(context.Context, string, string, int) ([]store.DeliveryEntry, error) {
	panic("not used")
}
func (m *minimalStore) GetDeliveryStats(context.Context, string) (*store.DeliveryStats, error) {
	panic("not used")
}
func (m *minimalStore) CreateSubscription(context.Context, *store.EventSubscription) error {
	panic("not used")
}
func (m *minimalStore) GetSubscription(context.Context, string) (*store.EventSubscription, error) {
	panic("not used")
}
func (m *minimalStore) UpdateSubscription(context.Context, *store.EventSubscription) error {
	panic("not used")
}
func (m *minimalStore) DeleteSubscription(context.Context, string) error { panic("not used") }
func (m *minimalStore) ListSubscriptions(context.Context, string) ([]store.EventSubscription, error) {
	panic("not used")
}
func (m *minimalStore) ListActiveSubscriptionsByEventType(context.Context, string) ([]store.EventSubscription, error) {
	panic("not used")
}
func (m *minimalStore) ApplyMigrations(context.Context, string) error      { panic("not used") }
func (m *minimalStore) IsMigrationApplied(context.Context, string) (bool, error) {
	panic("not used")
}
func (m *minimalStore) Close() { panic("not used") }
