package scheduler_test

// INIT-023 TASK-012, audit P3-27 (AC 16): the scheduler's commit retry is
// gated by the retry budget WithCommitRetry declares.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/core/domain"
)

// budgetCalls counts what the gate let through.
type budgetCalls struct {
	mu      sync.Mutex
	retries []string
	asked   []string
	maxSeen []int
}

func (b *budgetCalls) retry(_ context.Context, runID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retries = append(b.retries, runID)
	return nil
}

// exhausted is an exhaustion handler that decides like the engine's
// AbandonExhaustedMerge (domain.FirstExhaustedOutcome on the store's
// outcomes) and reports an abandon.
func (b *budgetCalls) exhausted(fs *fakeStore, err error) scheduler.CommitExhaustedFunc {
	return func(_ context.Context, runID string, maxRetries int) (bool, error) {
		b.mu.Lock()
		b.asked = append(b.asked, runID)
		b.maxSeen = append(b.maxSeen, maxRetries)
		b.mu.Unlock()
		if err != nil {
			return false, err
		}
		return domain.FirstExhaustedOutcome(fs.outcomes[runID], maxRetries) != nil, nil
	}
}

func transientOutcome(runID string, attempts int) domain.RepositoryMergeOutcome {
	return domain.RepositoryMergeOutcome{RunID: runID, RepositoryID: domain.PrimaryRepositoryID,
		Status: domain.RepositoryMergeStatusFailed, FailureClass: domain.MergeFailureNetwork,
		SourceBranch: "spine/run/" + runID, TargetBranch: "main", Attempts: attempts}
}

func budgetStore() *fakeStore {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r-spent", Status: domain.RunStatusCommitting, TraceID: "trace-spent"},
		{RunID: "r-left", Status: domain.RunStatusCommitting, TraceID: "trace-left"},
	}
	fs.outcomes = map[string][]domain.RepositoryMergeOutcome{
		"r-spent": {transientOutcome("r-spent", 4)},
		"r-left":  {transientOutcome("r-left", 3)},
	}
	return fs
}

func captureSchedLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestCommitBudget_ExhaustedRunIsHandedOverNotRetried(t *testing.T) {
	ctx := context.Background()
	fs := budgetStore()
	var b budgetCalls
	s := scheduler.New(fs, &fakeEventRouter{},
		scheduler.WithCommitRetry(b.retry, 3, 0),
		scheduler.WithCommitExhausted(b.exhausted(fs, nil)))
	s.RunRetryCycle(ctx)
	if strings.Join(b.retries, ",") != "r-left" {
		t.Fatalf("retried %v, want only r-left", b.retries)
	}
	if strings.Join(b.asked, ",") != "r-spent,r-left" {
		t.Fatalf("exhaustion handler asked for %v, want both runs", b.asked)
	}
	for _, m := range b.maxSeen {
		if m != 3 {
			t.Fatalf("exhaustion handler got maxRetries %d, want 3", m)
		}
	}
}

func TestCommitBudget_HandlerErrorSkipsTheRun(t *testing.T) {
	logs := captureSchedLogs(t)
	fs := budgetStore()
	var b budgetCalls
	s := scheduler.New(fs, &fakeEventRouter{},
		scheduler.WithCommitRetry(b.retry, 3, 0),
		scheduler.WithCommitExhausted(b.exhausted(fs, errors.New("store down"))))
	s.RunRetryCycle(context.Background())
	if len(b.retries) != 0 {
		t.Fatalf("retried %v after a handler error, want none", b.retries)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "store down") {
		t.Fatalf("no Error log for the handler failure:\n%s", logs.String())
	}
}

func TestCommitBudget_ZeroMaxRetriesNeverAsks(t *testing.T) {
	fs := budgetStore()
	var b budgetCalls
	s := scheduler.New(fs, &fakeEventRouter{},
		scheduler.WithCommitRetry(b.retry, 0, 0),
		scheduler.WithCommitExhausted(b.exhausted(fs, nil)))
	s.RunRetryCycle(context.Background())
	if len(b.asked) != 0 {
		t.Fatalf("maxRetries 0 asked the exhaustion handler for %v", b.asked)
	}
	if strings.Join(b.retries, ",") != "r-spent,r-left" {
		t.Fatalf("maxRetries 0 retried %v, want both (unbounded)", b.retries)
	}
}

func TestCommitBudget_NoHandlerLeavesExhaustedRunCommitting(t *testing.T) {
	logs := captureSchedLogs(t)
	fs := budgetStore()
	var b budgetCalls
	s := scheduler.New(fs, &fakeEventRouter{}, scheduler.WithCommitRetry(b.retry, 3, 0))
	s.RunRetryCycle(context.Background())
	if strings.Join(b.retries, ",") != "r-left" {
		t.Fatalf("retried %v without a handler, want only r-left", b.retries)
	}
	if !strings.Contains(logs.String(), "level=ERROR") ||
		!strings.Contains(logs.String(), "no exhaustion handler wired") || !strings.Contains(logs.String(), "r-spent") {
		t.Fatalf("no Error log for the unhandled exhausted run:\n%s", logs.String())
	}
	if _, moved := fs.updatedRuns["r-spent"]; moved {
		t.Fatal("the scheduler changed the exhausted run's status itself")
	}
}

func TestCommitBudget_RecoverOnStartupAppliesTheGate(t *testing.T) {
	fs := budgetStore()
	var b budgetCalls
	s := scheduler.New(fs, &fakeEventRouter{},
		scheduler.WithCommitRetry(b.retry, 3, 0),
		scheduler.WithCommitExhausted(b.exhausted(fs, nil)))
	if _, err := s.RecoverOnStartup(context.Background()); err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	if strings.Join(b.retries, ",") != "r-left" {
		t.Fatalf("startup recovery retried %v, want only r-left", b.retries)
	}
	if strings.Join(b.asked, ",") != "r-spent,r-left" {
		t.Fatalf("startup recovery asked for %v", b.asked)
	}
}

// TASK-006: a governance hold returns before the merge and records no
// outcome, so a held run is never counted down, however long it waits.
func TestCommitBudget_HeldRunWithoutOutcomeIsNeverAbandoned(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{{RunID: "r-held", Status: domain.RunStatusCommitting, TraceID: "trace-held"}}
	var b budgetCalls
	hold := func(ctx context.Context, runID string) error {
		_ = b.retry(ctx, runID)
		return errors.New("governance hold: awaiting operator approval")
	}
	s := scheduler.New(fs, &fakeEventRouter{},
		scheduler.WithCommitRetry(hold, 3, 0),
		scheduler.WithCommitExhausted(b.exhausted(fs, nil)))
	for tick := 0; tick < 10; tick++ {
		s.RunRetryCycle(context.Background())
	}
	if len(b.retries) != 10 || len(b.asked) != 10 {
		t.Fatalf("over 10 ticks: %d retries, %d budget checks; want 10 each", len(b.retries), len(b.asked))
	}
}
