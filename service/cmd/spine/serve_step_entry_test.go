package main

// INIT-023 TASK-007 AC 18: `spine serve` selects the engine step-entry
// policy from SPINE_STEP_ENTRY_POLICY (strict by default) and installs the
// same policy on the process-level and per-workspace orchestrators, plus
// the run-timeout termination hook on both schedulers.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

func TestStepEntryPolicyFromEnv(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    engine.StepEntryPolicy
		wantErr bool
	}{
		{value: "", want: engine.StrictStepEntryPolicy()},
		{value: "strict", want: engine.StrictStepEntryPolicy()},
		{value: " STRICT ", want: engine.StrictStepEntryPolicy()},
		{value: "legacy", want: engine.StepEntryPolicy{}},
		{value: "foo", wantErr: true},
		{value: "off", wantErr: true},
	} {
		tc := tc
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv(stepEntryPolicyEnv, tc.value)
			got, err := stepEntryPolicyFromEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%s=%q: expected an error, serve must refuse to start", stepEntryPolicyEnv, tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s=%q: %v", stepEntryPolicyEnv, tc.value, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s=%q: policy = %+v, want %+v", stepEntryPolicyEnv, tc.value, got, tc.want)
			}
		})
	}
}

// timeoutStore is a stub store holding one active run past its timeout
// with two live executions, for driving the scheduler's run-timeout hook.
type timeoutStore struct {
	stubStore
	run   domain.Run
	execs []domain.StepExecution
}

func newTimeoutStore() *timeoutStore {
	past := time.Now().Add(-time.Hour)
	return &timeoutStore{
		run: domain.Run{RunID: "run-1", TaskPath: "tasks/t.md", Status: domain.RunStatusActive,
			TraceID: "trace-0123456789ab", TimeoutAt: &past},
		execs: []domain.StepExecution{
			{ExecutionID: "run-1-a-1", RunID: "run-1", StepID: "a", Status: domain.StepStatusWaiting, Attempt: 1},
			{ExecutionID: "run-1-b-1", RunID: "run-1", StepID: "b", Status: domain.StepStatusAssigned, ActorID: "worker", Attempt: 1},
		},
	}
}

func (s *timeoutStore) ListTimedOutRuns(context.Context, time.Time) ([]domain.Run, error) {
	if s.run.Status != domain.RunStatusActive {
		return nil, nil
	}
	return []domain.Run{s.run}, nil
}

func (s *timeoutStore) GetRun(context.Context, string) (*domain.Run, error) {
	r := s.run
	return &r, nil
}

func (s *timeoutStore) UpdateRunStatusAt(_ context.Context, _ string, status domain.RunStatus, _ time.Time) error {
	s.run.Status = status
	return nil
}

func (s *timeoutStore) ListStepExecutionsByRun(context.Context, string) ([]domain.StepExecution, error) {
	return append([]domain.StepExecution(nil), s.execs...), nil
}

func (s *timeoutStore) UpdateStepExecution(_ context.Context, e *domain.StepExecution) error {
	for i := range s.execs {
		if s.execs[i].ExecutionID == e.ExecutionID {
			s.execs[i] = *e
		}
	}
	return nil
}

// requireTermination asserts the timeout store's executions are terminated
// (strict) or untouched (legacy).
func requireTermination(t *testing.T, st *timeoutStore, terminated bool) {
	t.Helper()
	for _, e := range st.execs {
		isTerminated := e.Status == domain.StepStatusSkipped && e.ErrorDetail != nil &&
			e.ErrorDetail.Classification == domain.FailureRunTerminated
		if isTerminated != terminated {
			t.Fatalf("%s: status %s detail %+v; terminated=%v, want %v", e.ExecutionID, e.Status, e.ErrorDetail, isTerminated, terminated)
		}
	}
	if !terminated && (st.execs[0].Status != domain.StepStatusWaiting || st.execs[1].Status != domain.StepStatusAssigned) {
		t.Fatalf("legacy: executions changed: %+v", st.execs)
	}
}

func TestServeWiring_InstallsEnvSelectedStepEntryPolicy(t *testing.T) {
	for _, value := range []string{"", "strict", "legacy"} {
		value := value
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv(stepEntryPolicyEnv, value)
			t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
			policy, err := stepEntryPolicyFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			strict := !policy.IsZero()
			ctx := context.Background()
			log := observe.Logger(ctx)
			repoPath := testutil.NewTempRepo(t)
			gitClient := git.NewCLIClient(repoPath)
			q := queue.NewMemoryQueue(4)
			events := event.NewQueueRouter(q)

			// Process-level orchestrator and scheduler (buildServerConfig path).
			st := newTimeoutStore()
			_, wfProvider := buildWorkflowResolver(st, gitClient)
			orch := buildOrchestrator(st, wfProvider, gitClient, events, q, nil, nil, policy, log)
			if orch == nil {
				t.Fatal("buildOrchestrator returned nil")
			}
			if got := orch.StepEntryPolicy(); !reflect.DeepEqual(got, policy) {
				t.Fatalf("buildOrchestrator policy = %+v, want %+v", got, policy)
			}
			sched := buildScheduler(st, events, orch, 0)
			if err := sched.ScanRunTimeouts(ctx); err != nil {
				t.Fatal(err)
			}
			if st.run.Status != domain.RunStatusCancelled {
				t.Fatalf("run status = %s, want cancelled", st.run.Status)
			}
			// buildScheduler installs WithRunTerminated(orch.TerminateLiveSteps).
			requireTermination(t, st, strict)

			// Per-workspace orchestrator (workspaceOrchestratorBuilder path).
			wsStore := newTimeoutStore()
			repoSpec := repository.PrimarySpec{LocalPath: repoPath}
			reg := repository.New("", repoSpec, func(context.Context) (*repository.Catalog, error) {
				return repository.ParseCatalog(nil, repoSpec)
			}, wsStore)
			pool, err := gitpool.New(gitClient, reg, gitpool.NewCLIClientFactory())
			if err != nil {
				t.Fatal(err)
			}
			ss := &workspace.ServiceSet{Store: wsStore, GitClient: gitClient, GitPool: pool, Queue: q, Events: events}
			if err := workspaceOrchestratorBuilder(ctx, ss, policy); err != nil {
				t.Fatalf("workspaceOrchestratorBuilder: %v", err)
			}
			wsOrch, ok := ss.StepAssigner.(*engine.Orchestrator)
			if !ok {
				t.Fatalf("ss.StepAssigner = %T, want *engine.Orchestrator", ss.StepAssigner)
			}
			if got := wsOrch.StepEntryPolicy(); !reflect.DeepEqual(got, policy) {
				t.Fatalf("workspace orchestrator policy = %+v, want %+v", got, policy)
			}
			if ss.RunTerminatedFn == nil {
				t.Fatal("workspaceOrchestratorBuilder must set ss.RunTerminatedFn")
			}
			ss.RunTerminatedFn(ctx, "run-1", "run cancelled: run timeout")
			requireTermination(t, wsStore, strict)
		})
	}
}
