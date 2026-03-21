package event_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/queue"
)

func TestEmitAndSubscribe(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received := make(chan domain.Event, 1)

	if err := router.Subscribe(ctx, domain.EventRunStarted, func(ctx context.Context, evt domain.Event) error {
		received <- evt
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	go q.Start(ctx)
	defer q.Stop()

	err := router.Emit(ctx, domain.Event{
		EventID:   "evt-001",
		Type:      domain.EventRunStarted,
		Timestamp: time.Now(),
		RunID:     "run-123",
		TraceID:   "trace-456",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	select {
	case evt := <-received:
		if evt.EventID != "evt-001" {
			t.Errorf("expected evt-001, got %s", evt.EventID)
		}
		if evt.Type != domain.EventRunStarted {
			t.Errorf("expected run_started, got %s", evt.Type)
		}
		if evt.RunID != "run-123" {
			t.Errorf("expected run-123, got %s", evt.RunID)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for event")
	}
}

func TestEventTypeFiltering(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var runCount, stepCount atomic.Int32

	router.Subscribe(ctx, domain.EventRunStarted, func(ctx context.Context, evt domain.Event) error {
		runCount.Add(1)
		return nil
	})
	router.Subscribe(ctx, domain.EventStepCompleted, func(ctx context.Context, evt domain.Event) error {
		stepCount.Add(1)
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	router.Emit(ctx, domain.Event{EventID: "e-1", Type: domain.EventRunStarted, Timestamp: time.Now()})
	router.Emit(ctx, domain.Event{EventID: "e-2", Type: domain.EventStepCompleted, Timestamp: time.Now()})
	router.Emit(ctx, domain.Event{EventID: "e-3", Type: domain.EventRunStarted, Timestamp: time.Now()})

	time.Sleep(200 * time.Millisecond)

	if got := runCount.Load(); got != 2 {
		t.Errorf("expected 2 run events, got %d", got)
	}
	if got := stepCount.Load(); got != 1 {
		t.Errorf("expected 1 step event, got %d", got)
	}
}

func TestEventIdempotency(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var count atomic.Int32

	router.Subscribe(ctx, domain.EventArtifactCreated, func(ctx context.Context, evt domain.Event) error {
		count.Add(1)
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	// Emit same event ID twice — should only be delivered once
	evt := domain.Event{EventID: "dup-001", Type: domain.EventArtifactCreated, Timestamp: time.Now()}
	router.Emit(ctx, evt)
	router.Emit(ctx, evt)

	time.Sleep(200 * time.Millisecond)

	if got := count.Load(); got != 1 {
		t.Errorf("expected 1 delivery (idempotent), got %d", got)
	}
}

func TestEmitRequiresEventID(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx := context.Background()

	err := router.Emit(ctx, domain.Event{Type: domain.EventRunStarted})
	if err == nil {
		t.Fatal("expected error for missing event_id")
	}
}

func TestEmitRequiresEventType(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx := context.Background()

	err := router.Emit(ctx, domain.Event{EventID: "e-1"})
	if err == nil {
		t.Fatal("expected error for missing event type")
	}
}
