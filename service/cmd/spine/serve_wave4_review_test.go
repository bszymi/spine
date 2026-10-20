package main

// INIT-023 wave 4 cross-task review: `spine serve` wiring that no test
// pinned — the scheduler's step-recovery callback (TASK-008: a completed
// execution re-routes through RecoverCompletedStep), the scheduler's
// governance-aware commit retry (TASK-006 AC 35) and the shared-mode push
// resolver's provenance-recording emitter (TASK-006, codex review round
// 1). Each test drives the production callback, not a copy of it.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/workspace"
)

// fakeSchedulerEngine records the scheduler callbacks' engine calls.
type fakeSchedulerEngine struct {
	recovered []string
	retried   []string
	merges    []string
	mergeErr  error
}

func (f *fakeSchedulerEngine) RecoverCompletedStep(_ context.Context, execID string) error {
	f.recovered = append(f.recovered, execID)
	return nil
}

func (f *fakeSchedulerEngine) RetryStep(_ context.Context, exec *domain.StepExecution) error {
	f.retried = append(f.retried, exec.ExecutionID)
	return nil
}

func (f *fakeSchedulerEngine) FailRun(context.Context, string, string) error { return nil }

func (f *fakeSchedulerEngine) TerminateLiveSteps(context.Context, string, string) {}

func (f *fakeSchedulerEngine) MergeRunBranch(_ context.Context, runID string) error {
	f.merges = append(f.merges, runID)
	return f.mergeErr
}

func (f *fakeSchedulerEngine) AbandonExhaustedMerge(context.Context, string, int) (bool, error) {
	return false, nil
}

var _ schedulerEngine = (*engine.Orchestrator)(nil)

// recoveryStore holds one active run whose current step's execution is
// completed and has no successor: the crash between the completion write
// and the routing.
type recoveryStore struct {
	stubStore
	exec domain.StepExecution
}

func (s *recoveryStore) ListRunsByStatus(_ context.Context, status domain.RunStatus) ([]domain.Run, error) {
	if status == domain.RunStatusActive {
		return []domain.Run{{RunID: s.exec.RunID, Status: domain.RunStatusActive, CurrentStepID: s.exec.StepID}}, nil
	}
	return nil, nil
}

func (s *recoveryStore) ListStepExecutionsByRun(context.Context, string) ([]domain.StepExecution, error) {
	return []domain.StepExecution{s.exec}, nil
}

func (s *recoveryStore) GetStepExecution(context.Context, string) (*domain.StepExecution, error) {
	e := s.exec
	return &e, nil
}

// TestServeScheduler_StepRecoveryUsesRecoverCompletedStep: startup
// recovery through the scheduler `spine serve` builds hands a completed
// execution to RecoverCompletedStep (never retried, never resubmitted),
// and an execution in any other state to RetryStep.
func TestServeScheduler_StepRecoveryUsesRecoverCompletedStep(t *testing.T) {
	ctx := context.Background()
	events := event.NewQueueRouter(queue.NewMemoryQueue(4))
	st := &recoveryStore{exec: domain.StepExecution{ExecutionID: "run-1-review-1", RunID: "run-1", StepID: "review",
		Status: domain.StepStatusCompleted, OutcomeID: "accepted"}}
	eng := &fakeSchedulerEngine{}
	if _, err := scheduler.New(st, events, engineSchedulerOptions(st, eng)...).RecoverOnStartup(ctx); err != nil {
		t.Fatal(err)
	}
	if len(eng.recovered) != 1 || eng.recovered[0] != "run-1-review-1" || len(eng.retried) != 0 {
		t.Fatalf("recovered %v retried %v; want the completed execution re-routed through RecoverCompletedStep", eng.recovered, eng.retried)
	}
	// The shared-mode ServiceSet's callback is the same function.
	eng = &fakeSchedulerEngine{}
	st.exec.Status = domain.StepStatusFailed
	if err := stepRecoveryFor(st, eng)(ctx, "run-1-review-1"); err != nil {
		t.Fatal(err)
	}
	if len(eng.retried) != 1 || len(eng.recovered) != 0 {
		t.Fatalf("recovered %v retried %v; want a non-completed execution retried", eng.recovered, eng.retried)
	}
}

// TestServeScheduler_HeldMergeLogsNoError is TASK-006 AC 35 through the
// scheduler `spine serve` builds: its commit-retry sweep over a held merge
// logs no Error line.
func TestServeScheduler_HeldMergeLogsNoError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	eng := &fakeSchedulerEngine{mergeErr: domain.NewErrorWithCause(domain.ErrPrecondition,
		"governed merge changes 1 governance path(s)", engine.ErrGovernanceApprovalRequired)}
	st := committingStore{}
	scheduler.New(st, nil, engineSchedulerOptions(st, eng)...).RunRetryCycle(context.Background())
	if len(eng.merges) != 1 {
		t.Fatalf("merges %v, want the committing run retried once", eng.merges)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("a held merge logged an Error line through the built scheduler:\n%s", buf.String())
	}
}

// TestGitPushResolver_RecordsOverrideProvenanceInSharedMode: the
// shared-mode push resolver hands githttp the provenance-recording
// emitter over the workspace's own store, so an honoured override reaches
// that workspace's event_log synchronously.
func TestGitPushResolver_RecordsOverrideProvenanceInSharedMode(t *testing.T) {
	t.Setenv(governanceGuardEnv, "")
	ctx := context.Background()
	wsStore := &eventLogStore{}
	pool := workspace.NewServicePool(ctx, builderResolver{cfg: workspace.Config{
		ID: "ws-3", RepoPath: t.TempDir(), Status: workspace.StatusActive,
	}}, workspace.PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(_ context.Context, ss *workspace.ServiceSet) error {
			ss.Store = wsStore
			return nil
		},
	})
	defer pool.Close()
	res, release, err := buildGitPushResolver(pool, &eventLogStore{}, nil, nil)(ctx, "ws-3")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	pe, ok := res.Events.(*overrideProvenanceEmitter)
	if !ok {
		t.Fatalf("shared-mode push resolver events = %T, want the provenance-recording emitter", res.Events)
	}
	if pe.log != eventLogWriter(wsStore) {
		t.Fatalf("provenance written to %T, want the workspace's own store", pe.log)
	}
	ov := domain.Event{EventID: "evt-ov-ws", Type: domain.EventBranchProtectionOverride, Timestamp: time.Now(), ActorID: "op-1",
		Payload: []byte(`{"branch":"refs/heads/main","operation":"direct_write"}`)}
	if err := res.Events.Emit(ctx, ov); err != nil {
		t.Fatal(err)
	}
	if len(wsStore.writes) != 1 || wsStore.writes[0].EventID != "evt-ov-ws" {
		t.Fatalf("workspace event-log writes = %+v, want the override", wsStore.writes)
	}
}
