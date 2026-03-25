package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scheduler"
)

func TestScanRunTimeouts_CancelsTimedOutRun(t *testing.T) {
	st := newFakeStore()
	ev := &fakeEventRouter{}

	past := time.Now().Add(-1 * time.Hour)
	st.runs = []domain.Run{{
		RunID:     "run-1",
		TaskPath:  "tasks/task.md",
		Status:    domain.RunStatusActive,
		TraceID:   "trace-abc123456789",
		TimeoutAt: &past,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}}

	s := scheduler.New(st, ev)
	err := s.ScanRunTimeouts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if st.runs[0].Status != domain.RunStatusCancelled {
		t.Errorf("expected run to be cancelled, got %s", st.runs[0].Status)
	}

	if len(ev.events) == 0 {
		t.Error("expected run_timeout event")
	} else if ev.events[len(ev.events)-1].Type != domain.EventRunTimeout {
		t.Errorf("expected run_timeout event, got %s", ev.events[len(ev.events)-1].Type)
	}
}

func TestScanRunTimeouts_SkipsNotYetExpired(t *testing.T) {
	st := newFakeStore()
	ev := &fakeEventRouter{}

	future := time.Now().Add(1 * time.Hour)
	st.runs = []domain.Run{{
		RunID:     "run-1",
		TaskPath:  "tasks/task.md",
		Status:    domain.RunStatusActive,
		TraceID:   "trace-abc123456789",
		TimeoutAt: &future,
		CreatedAt: time.Now(),
	}}

	s := scheduler.New(st, ev)
	err := s.ScanRunTimeouts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if st.runs[0].Status != domain.RunStatusActive {
		t.Errorf("expected run to remain active, got %s", st.runs[0].Status)
	}
}

func TestScanRunTimeouts_SkipsRunsWithoutTimeout(t *testing.T) {
	st := newFakeStore()
	ev := &fakeEventRouter{}

	st.runs = []domain.Run{{
		RunID:     "run-1",
		TaskPath:  "tasks/task.md",
		Status:    domain.RunStatusActive,
		TraceID:   "trace-abc123456789",
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}}

	s := scheduler.New(st, ev)
	err := s.ScanRunTimeouts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if st.runs[0].Status != domain.RunStatusActive {
		t.Errorf("expected run to remain active, got %s", st.runs[0].Status)
	}
}
