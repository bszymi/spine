package delivery

import (
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

func TestEventBroadcaster_SubscribeDeliversToListener(t *testing.T) {
	b := NewEventBroadcaster()
	ch := make(chan domain.Event, 1)
	id := b.Subscribe(ch)
	if id == 0 {
		t.Fatal("Subscribe returned zero ID")
	}

	evt := domain.Event{EventID: "e-1", Type: "artifact_created"}
	b.Broadcast(evt)

	select {
	case got := <-ch:
		if got.EventID != evt.EventID {
			t.Errorf("listener received %+v, want %+v", got, evt)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("listener did not receive broadcast within deadline")
	}
}

func TestEventBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewEventBroadcaster()
	ch := make(chan domain.Event, 1)
	id := b.Subscribe(ch)
	b.Unsubscribe(id)

	b.Broadcast(domain.Event{EventID: "ignored"})

	select {
	case got := <-ch:
		t.Errorf("unsubscribed listener received event %+v", got)
	case <-time.After(20 * time.Millisecond):
		// Expected — no delivery.
	}
}

// TestEventBroadcaster_SlowListenerDoesNotBlockOthers exercises the
// drop-on-full-channel branch in Broadcast. Without it, a single slow
// listener would stall every other subscriber.
func TestEventBroadcaster_SlowListenerDoesNotBlockOthers(t *testing.T) {
	b := NewEventBroadcaster()

	slow := make(chan domain.Event, 1) // capacity 1 — quickly fills
	fast := make(chan domain.Event, 10)
	b.Subscribe(slow)
	b.Subscribe(fast)

	// Fill the slow listener's buffer.
	slow <- domain.Event{EventID: "prefill"}

	done := make(chan struct{})
	go func() {
		b.Broadcast(domain.Event{EventID: "fan-out"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Broadcast blocked on a full listener channel")
	}

	// Fast listener must have received the event regardless.
	select {
	case got := <-fast:
		if got.EventID != "fan-out" {
			t.Errorf("fast listener got %q, want fan-out", got.EventID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("fast listener did not receive broadcast")
	}
}

func TestEventBroadcaster_ConcurrentSubscribeBroadcast(t *testing.T) {
	b := NewEventBroadcaster()

	const listeners = 20
	chans := make([]chan domain.Event, listeners)
	ids := make([]int64, listeners)
	var wg sync.WaitGroup
	wg.Add(listeners)
	for i := 0; i < listeners; i++ {
		chans[i] = make(chan domain.Event, 8)
		go func(i int) {
			defer wg.Done()
			ids[i] = b.Subscribe(chans[i])
		}(i)
	}
	wg.Wait()

	b.Broadcast(domain.Event{EventID: "broadcast"})

	for i, ch := range chans {
		select {
		case got := <-ch:
			if got.EventID != "broadcast" {
				t.Errorf("listener %d got %q", i, got.EventID)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("listener %d did not receive broadcast", i)
		}
	}

	// Unsubscribing unknown IDs is a safe no-op.
	b.Unsubscribe(int64(9999))
}
