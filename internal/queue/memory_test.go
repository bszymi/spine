package queue_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/queue"
)

func TestPublishAndSubscribe(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	received := make(chan queue.Entry, 1)

	if err := q.Subscribe(ctx, "test_type", func(ctx context.Context, entry queue.Entry) error {
		received <- entry
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	go q.Start(ctx)
	defer q.Stop()

	if err := q.Publish(ctx, queue.Entry{
		EntryID:   "entry-001",
		EntryType: "test_type",
		Payload:   []byte(`{"key":"value"}`),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case entry := <-received:
		if entry.EntryID != "entry-001" {
			t.Errorf("expected entry-001, got %s", entry.EntryID)
		}
		if entry.EntryType != "test_type" {
			t.Errorf("expected test_type, got %s", entry.EntryType)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for entry")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var count atomic.Int32

	for i := 0; i < 3; i++ {
		q.Subscribe(ctx, "multi", func(ctx context.Context, entry queue.Entry) error {
			count.Add(1)
			return nil
		})
	}

	go q.Start(ctx)
	defer q.Stop()

	q.Publish(ctx, queue.Entry{EntryID: "m-001", EntryType: "multi"})

	time.Sleep(100 * time.Millisecond)

	if got := count.Load(); got != 3 {
		t.Errorf("expected 3 handler calls, got %d", got)
	}
}

func TestIdempotencyKey(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var count atomic.Int32

	q.Subscribe(ctx, "idem", func(ctx context.Context, entry queue.Entry) error {
		count.Add(1)
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	// Publish same idempotency key twice
	q.Publish(ctx, queue.Entry{EntryID: "i-001", EntryType: "idem", IdempotencyKey: "key-1"})
	q.Publish(ctx, queue.Entry{EntryID: "i-002", EntryType: "idem", IdempotencyKey: "key-1"})

	time.Sleep(100 * time.Millisecond)

	if got := count.Load(); got != 1 {
		t.Errorf("expected 1 delivery (idempotent), got %d", got)
	}
}

func TestAcknowledge(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx := context.Background()

	if q.IsAcknowledged("ack-001") {
		t.Error("should not be acknowledged yet")
	}

	if err := q.Acknowledge(ctx, "ack-001"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	if !q.IsAcknowledged("ack-001") {
		t.Error("should be acknowledged")
	}
}

func TestPublishRequiresEntryID(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx := context.Background()

	err := q.Publish(ctx, queue.Entry{EntryType: "test"})
	if err == nil {
		t.Fatal("expected error for missing entry_id")
	}
}

func TestConcurrentPublish(t *testing.T) {
	q := queue.NewMemoryQueue(1000)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var received atomic.Int32

	q.Subscribe(ctx, "concurrent", func(ctx context.Context, entry queue.Entry) error {
		received.Add(1)
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q.Publish(ctx, queue.Entry{
				EntryID:   fmt.Sprintf("c-%03d", i),
				EntryType: "concurrent",
			})
		}(i)
	}
	wg.Wait()

	time.Sleep(200 * time.Millisecond)

	if got := received.Load(); got != 100 {
		t.Errorf("expected 100 deliveries, got %d", got)
	}
}

func TestQueueNotDurable(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx := context.Background()

	q.Publish(ctx, queue.Entry{EntryID: "d-001", EntryType: "durable_test"})

	if q.Len() != 1 {
		t.Errorf("expected 1 pending entry, got %d", q.Len())
	}

	// Simulate restart by creating a new queue
	q2 := queue.NewMemoryQueue(100)
	if q2.Len() != 0 {
		t.Errorf("new queue should be empty, got %d entries", q2.Len())
	}
}

func TestMultipleEntryTypes(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var assignmentCount, eventCount atomic.Int32

	q.Subscribe(ctx, "step_assignment", func(ctx context.Context, entry queue.Entry) error {
		assignmentCount.Add(1)
		return nil
	})
	q.Subscribe(ctx, "event_delivery", func(ctx context.Context, entry queue.Entry) error {
		eventCount.Add(1)
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	q.Publish(ctx, queue.Entry{EntryID: "a-001", EntryType: "step_assignment"})
	q.Publish(ctx, queue.Entry{EntryID: "e-001", EntryType: "event_delivery"})
	q.Publish(ctx, queue.Entry{EntryID: "a-002", EntryType: "step_assignment"})

	time.Sleep(100 * time.Millisecond)

	if got := assignmentCount.Load(); got != 2 {
		t.Errorf("expected 2 assignment deliveries, got %d", got)
	}
	if got := eventCount.Load(); got != 1 {
		t.Errorf("expected 1 event delivery, got %d", got)
	}
}
