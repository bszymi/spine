package harness

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestClock_AdvanceMovesNow(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := NewClock(t0)

	if got := c.Now(); !got.Equal(t0) {
		t.Fatalf("Now before advance: got %v, want %v", got, t0)
	}

	if err := c.Advance(2 * time.Hour); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	want := t0.Add(2 * time.Hour)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("Now after advance: got %v, want %v", got, want)
	}
}

func TestClock_OnAdvance_FiresInRegistrationOrder(t *testing.T) {
	c := NewClock(time.Now())

	var order []string
	c.OnAdvance("first", func() error {
		order = append(order, "first")
		return nil
	})
	c.OnAdvance("second", func() error {
		order = append(order, "second")
		return nil
	})

	if err := c.Advance(time.Minute); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("handlers fired out of order: %v", order)
	}
}

func TestClock_AdvanceReturnsFirstHandlerError_ButRunsAll(t *testing.T) {
	// The contract: Advance reports the first error but lets every
	// handler run so the failing scenario can still observe all
	// downstream side effects (e.g., a second tick that does writes
	// the assertion checks). Drift on this would let a P3 lint-style
	// failure on handler #1 silently mask a P1 bug in handler #2.
	c := NewClock(time.Now())

	var ran []string
	boom := errors.New("boom")
	c.OnAdvance("first", func() error {
		ran = append(ran, "first")
		return boom
	})
	c.OnAdvance("second", func() error {
		ran = append(ran, "second")
		return errors.New("ignored")
	})

	err := c.Advance(time.Minute)
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped boom, got %v", err)
	}
	if len(ran) != 2 || ran[0] != "first" || ran[1] != "second" {
		t.Errorf("expected both handlers to run, got %v", ran)
	}
}

func TestClock_NowIsRaceSafe(t *testing.T) {
	c := NewClock(time.Now())

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Now()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.Advance(time.Second)
	}()
	wg.Wait()
}
