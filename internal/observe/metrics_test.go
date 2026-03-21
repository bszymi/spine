package observe_test

import (
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/observe"
)

func TestCounterIncAndValue(t *testing.T) {
	var c observe.Counter

	if c.Value() != 0 {
		t.Errorf("expected 0, got %d", c.Value())
	}

	c.Inc()
	c.Inc()
	c.Inc()

	if c.Value() != 3 {
		t.Errorf("expected 3, got %d", c.Value())
	}
}

func TestCounterAdd(t *testing.T) {
	var c observe.Counter

	c.Add(10)
	c.Add(5)

	if c.Value() != 15 {
		t.Errorf("expected 15, got %d", c.Value())
	}
}

func TestCounterConcurrent(t *testing.T) {
	var c observe.Counter
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()

	if c.Value() != 100 {
		t.Errorf("expected 100, got %d", c.Value())
	}
}

func TestGlobalMetrics(t *testing.T) {
	m := observe.GlobalMetrics

	m.RunsStarted.Inc()
	m.RunsCompleted.Inc()
	m.StepsCompleted.Add(5)
	m.GitCommits.Inc()
	m.EventsEmitted.Add(10)

	if m.RunsStarted.Value() < 1 {
		t.Error("RunsStarted should be at least 1")
	}
	if m.StepsCompleted.Value() < 5 {
		t.Error("StepsCompleted should be at least 5")
	}
}
