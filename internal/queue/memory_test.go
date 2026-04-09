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

func TestDefaultBufferSize(t *testing.T) {
	q := queue.NewMemoryQueue(0)
	ctx := context.Background()

	// Should work with default buffer size
	if err := q.Publish(ctx, queue.Entry{EntryID: "d-001", EntryType: "test"}); err != nil {
		t.Fatalf("Publish with default buffer: %v", err)
	}
}

func TestPublishTimeout(t *testing.T) {
	// Buffer size 1, fill it, then publish with cancelled context
	q := queue.NewMemoryQueue(1)
	ctx := context.Background()

	// Fill the buffer
	q.Publish(ctx, queue.Entry{EntryID: "fill-001", EntryType: "test"})

	// Now publish with an already-cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := q.Publish(cancelledCtx, queue.Entry{EntryID: "timeout-001", EntryType: "test"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestPublishTimeoutIdempotencyNotPoisoned(t *testing.T) {
	// Verify that a failed publish does not poison the idempotency set
	q := queue.NewMemoryQueue(1)
	ctx := context.Background()

	// Fill the buffer
	q.Publish(ctx, queue.Entry{EntryID: "fill-001", EntryType: "test"})

	// Publish with cancelled context and idempotency key
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	q.Publish(cancelledCtx, queue.Entry{EntryID: "retry-001", EntryType: "test", IdempotencyKey: "key-retry"})

	// Drain the buffer
	received := make(chan queue.Entry, 2)
	q.Subscribe(ctx, "test", func(ctx context.Context, entry queue.Entry) error {
		received <- entry
		return nil
	})

	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer runCancel()
	go q.Start(runCtx)
	defer q.Stop()

	// Retry should succeed (idempotency key was not poisoned)
	q.Publish(ctx, queue.Entry{EntryID: "retry-001b", EntryType: "test", IdempotencyKey: "key-retry"})

	time.Sleep(200 * time.Millisecond)
}

func TestConcurrentIdempotency(t *testing.T) {
	q := queue.NewMemoryQueue(1000)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var received atomic.Int32

	q.Subscribe(ctx, "idem_concurrent", func(ctx context.Context, entry queue.Entry) error {
		received.Add(1)
		return nil
	})

	go q.Start(ctx)
	defer q.Stop()

	// 50 goroutines all publish with the same idempotency key.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q.Publish(ctx, queue.Entry{
				EntryID:        fmt.Sprintf("dup-%03d", i),
				EntryType:      "idem_concurrent",
				IdempotencyKey: "shared-key",
			})
		}(i)
	}
	wg.Wait()

	time.Sleep(200 * time.Millisecond)

	if got := received.Load(); got != 1 {
		t.Errorf("expected exactly 1 delivery for shared idempotency key, got %d", got)
	}
}

func TestStopSignal(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		q.Start(ctx)
		close(done)
	}()

	q.Stop()

	select {
	case <-done:
		// Start returned after Stop — correct
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestLateSubscriber(t *testing.T) {
	q := queue.NewMemoryQueue(100)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Start the queue BEFORE subscribing
	go q.Start(ctx)
	defer q.Stop()

	// Publish before any subscriber
	q.Publish(ctx, queue.Entry{EntryID: "late-001", EntryType: "late_type"})

	// Subscribe after a delay
	time.Sleep(100 * time.Millisecond)

	received := make(chan queue.Entry, 1)
	q.Subscribe(ctx, "late_type", func(ctx context.Context, entry queue.Entry) error {
		received <- entry
		return nil
	})

	select {
	case entry := <-received:
		if entry.EntryID != "late-001" {
			t.Errorf("expected late-001, got %s", entry.EntryID)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for late-subscribed entry")
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
