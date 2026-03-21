package event_test

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/queue"
)

func TestEmitSetsTimestamp(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received := make(chan domain.Event, 1)
	router.Subscribe(ctx, domain.EventArtifactUpdated, func(ctx context.Context, evt domain.Event) error {
		received <- evt
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	// Emit with zero timestamp — should be auto-set
	err := router.Emit(ctx, domain.Event{
		EventID: "ts-001",
		Type:    domain.EventArtifactUpdated,
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	select {
	case evt := <-received:
		if evt.Timestamp.IsZero() {
			t.Error("expected timestamp to be auto-set")
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}

func TestSubscribeUnmarshalError(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var callCount int
	router.Subscribe(ctx, domain.EventRunStarted, func(ctx context.Context, evt domain.Event) error {
		callCount++
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	// Publish invalid JSON directly to queue as event_delivery
	q.Publish(ctx, queue.Entry{
		EntryID:   "bad-json",
		EntryType: "event_delivery",
		Payload:   []byte(`{invalid json`),
	})

	time.Sleep(200 * time.Millisecond)

	// Handler should not have been called (unmarshal error)
	if callCount > 0 {
		t.Errorf("handler should not be called for invalid JSON, got %d calls", callCount)
	}
}

func TestSubscribeNonMatchingType(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	router := event.NewQueueRouter(q)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var runCount int
	router.Subscribe(ctx, domain.EventRunStarted, func(ctx context.Context, evt domain.Event) error {
		runCount++
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	// Emit a different event type
	router.Emit(ctx, domain.Event{
		EventID: "other-001",
		Type:    domain.EventStepCompleted,
	})

	time.Sleep(200 * time.Millisecond)

	if runCount > 0 {
		t.Errorf("handler should not fire for non-matching type, got %d", runCount)
	}
}
