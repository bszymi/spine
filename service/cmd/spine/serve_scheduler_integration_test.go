//go:build integration

package main

// INIT-024 TASK-008 (audit P3-27, open half), against PostgreSQL: a
// WORKSPACE_RESOLVER=db deployment — the registry, two workspaces on their
// own runtime databases, wired by buildServeWorkspaces and buildServerConfig
// exactly as serveCmd wires them — runs the multi-workspace scheduler:
// one tick of each loop times out, fails orphaned and retries committing
// runs in both workspaces, each on its own database; a workspace added at
// runtime is scanned on the next tick and a deactivated one never again;
// no tick leaves a pool reference behind. At the scaffold base the runtime
// held no scheduler in this mode, so none of the rows below ever moved.
//
// The databases are created next to SPINE_TEST_DATABASE_URL (named
// <that database>_<suffix>) and dropped afterwards; without CREATEDB the
// test skips (the CI step that runs it fails on a skip).

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/bszymi/spine/testutil"
)

// t008Seed writes a workflow projection (step "a", with a one-minute
// timeout) and four runs into one workspace's runtime database: an active
// run past its timeout with a live execution, an active run whose step has
// been in progress for two hours (past the step timeout), an active run
// with no activity for ten hours (an orphan under a one-hour threshold: it
// is failed past three thresholds), and a run in committing.
func t008Seed(t *testing.T, st *store.PostgresStore, ws string) {
	t.Helper()
	ctx := context.Background()
	def, err := json.Marshal(domain.WorkflowDefinition{
		ID: "task", Name: "task", Version: "1", Status: domain.WorkflowStatus("Active"), EntryStep: "a",
		Steps: []domain.StepDefinition{{ID: "a", Name: "a", Type: domain.StepType("manual"), Timeout: "1m"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorkflowProjection(ctx, &store.WorkflowProjection{
		WorkflowPath: "workflows/task.yaml", WorkflowID: "task", Name: "task", Version: "1", Status: "Active",
		AppliesTo: []byte("[]"), Definition: def, SourceCommit: "0123456789abcdef0123456789abcdef01234567",
	}); err != nil {
		t.Fatalf("%s: workflow projection: %v", ws, err)
	}
	now := time.Now()
	past := now.Add(-time.Hour)
	runs := []domain.Run{
		{RunID: ws + "-timeout", Status: domain.RunStatusActive, TimeoutAt: &past, CreatedAt: now.Add(-2 * time.Hour), CurrentStepID: "a"},
		{RunID: ws + "-step", Status: domain.RunStatusActive, CreatedAt: now.Add(-2 * time.Hour), CurrentStepID: "a"},
		{RunID: ws + "-orphan", Status: domain.RunStatusActive, CreatedAt: now.Add(-10 * time.Hour)},
		{RunID: ws + "-commit", Status: domain.RunStatusCommitting, CreatedAt: now.Add(-time.Hour)},
	}
	for i := range runs {
		r := &runs[i]
		r.TaskPath = "tasks/" + r.RunID + ".md"
		r.WorkflowPath = "workflows/task.yaml"
		r.WorkflowID = "task"
		r.WorkflowVersion = "0123456789abcdef0123456789abcdef01234567"
		r.TraceID = "trace-" + r.RunID + "-0123456789abcdef"
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("%s: create run %s: %v", ws, r.RunID, err)
		}
	}
	started := now.Add(-2 * time.Hour)
	execs := []domain.StepExecution{
		{ExecutionID: ws + "-timeout-a-1", RunID: ws + "-timeout", StepID: "a",
			Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: now},
		{ExecutionID: ws + "-step-a-1", RunID: ws + "-step", StepID: "a", ActorID: "worker",
			Status: domain.StepStatusInProgress, Attempt: 1, CreatedAt: started, StartedAt: &started},
	}
	for i := range execs {
		if err := st.CreateStepExecution(ctx, &execs[i]); err != nil {
			t.Fatalf("%s: create execution %s: %v", ws, execs[i].ExecutionID, err)
		}
	}
}

func t008RunStatus(t *testing.T, st *store.PostgresStore, runID string) domain.RunStatus {
	t.Helper()
	r, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	return r.Status
}

func t008TimedOutRun(t *testing.T, st *store.PostgresStore, runID string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	if err := st.CreateRun(context.Background(), &domain.Run{
		RunID: runID, TaskPath: "tasks/" + runID + ".md", WorkflowPath: "workflows/task.yaml", WorkflowID: "task",
		Status: domain.RunStatusActive, TraceID: "trace-" + runID + "-0123456789abcdef", TimeoutAt: &past,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
}

func TestServe_DBModeMultiSchedulerFiresAcrossWorkspaces(t *testing.T) {
	ctx := context.Background()
	regDSN := t009CreateDB(t, "t008_reg")
	t009Migrate(t, regDSN, filepath.Join(store.FindMigrationsDir(), "registry"))
	dsns := map[string]string{
		"ws-a": t009CreateDB(t, "t008_wsa"),
		"ws-b": t009CreateDB(t, "t008_wsb"),
		"ws-c": t009CreateDB(t, "t008_wsc"),
	}
	stores := map[string]*store.PostgresStore{}
	for id, dsn := range dsns {
		stores[id] = t009Migrate(t, dsn, store.FindMigrationsDir())
	}
	t008Seed(t, stores["ws-a"], "ws-a")
	t008Seed(t, stores["ws-b"], "ws-b")

	setup, err := workspace.NewDBProvider(ctx, regDSN, workspace.DBProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ws-a", "ws-b"} {
		if err := setup.CreateWorkspace(ctx, workspace.Config{
			ID: id, DisplayName: id, DatabaseURL: secrets.NewSecretValue([]byte(dsns[id])),
			RepoPath: testutil.NewTempRepo(t), Status: workspace.StatusActive,
		}); err != nil {
			t.Fatal(err)
		}
	}
	setup.Close()

	clearSecretBackendEnv(t)
	t.Setenv("SPINE_SECRET_ENCRYPTION_KEY", "")
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	t.Setenv("WORKSPACE_RESOLVER", "db")
	t.Setenv("SPINE_REGISTRY_DATABASE_URL", regDSN)
	clearSchedulerEnv(t)
	log := slog.New(slog.DiscardHandler)
	ws, err := buildServeWorkspaces(ctx, "development", log, workspaceDeliveryConfig{}, "", engine.StrictStepEntryPolicy())
	if err != nil {
		t.Fatalf("buildServeWorkspaces: %v", err)
	}
	t.Cleanup(ws.Close)
	if ws.Wiring.Pool == nil || ws.Store != nil {
		t.Fatalf("db wiring: pool %v, process store %v", ws.Wiring.Pool != nil, ws.Store != nil)
	}

	repoPath := testutil.NewTempRepo(t)
	q := queue.NewMemoryQueue(16)
	qctx, qcancel := context.WithCancel(ctx)
	t.Cleanup(qcancel)
	go q.Start(qctx)
	rt, err := buildServerConfig(ctx, serveDeps{
		RepoPath:        repoPath,
		SpineCfg:        &config.SpineConfig{ArtifactsDir: "/"},
		GitClient:       git.NewCLIClient(repoPath),
		Queue:           q,
		Events:          event.NewQueueRouter(q),
		WSResolver:      ws.Wiring.Resolver,
		WSDBProvider:    ws.Wiring.DBProvider,
		WSServicePool:   ws.Wiring.Pool,
		DevMode:         true,
		RuntimeEnv:      "development",
		OrphanThreshold: time.Hour,
		StepEntryPolicy: engine.StrictStepEntryPolicy(),
	})
	if err != nil {
		t.Fatalf("buildServerConfig: %v", err)
	}
	ms := rt.MultiScheduler
	if ms == nil || rt.Scheduler != nil {
		t.Fatalf("db mode runtime: MultiScheduler %v, single Scheduler %v; want only the MultiScheduler", ms != nil, rt.Scheduler != nil)
	}
	noRefs := func(when string) {
		t.Helper()
		for _, id := range []string{"ws-a", "ws-b", "ws-c"} {
			if n := ws.Wiring.Pool.RefCount(id); n != 0 {
				t.Fatalf("%s: %s holds %d pool references, want 0", when, id, n)
			}
		}
	}

	// Timeouts: in each workspace, on its own database, the step past its
	// step timeout fails as timed out, and the run past its run timeout is
	// cancelled with its live execution terminated (strict policy).
	ms.ScanTimeouts(ctx)
	noRefs("after the timeout tick")
	for _, id := range []string{"ws-a", "ws-b"} {
		st := stores[id]
		step, err := st.GetStepExecution(ctx, id+"-step-a-1")
		if err != nil {
			t.Fatal(err)
		}
		if step.Status != domain.StepStatusFailed || step.ErrorDetail == nil || step.ErrorDetail.Classification != domain.FailureTimeout {
			t.Fatalf("%s: step past its timeout = %s %+v after one timeout tick, want failed/timeout", id, step.Status, step.ErrorDetail)
		}
		if got := t008RunStatus(t, st, id+"-timeout"); got != domain.RunStatusCancelled {
			t.Fatalf("%s: timed-out run is %s after one timeout tick, want cancelled", id, got)
		}
		execs, err := st.ListStepExecutionsByRun(ctx, id+"-timeout")
		if err != nil || len(execs) != 1 {
			t.Fatalf("%s: executions %v, %v", id, execs, err)
		}
		if execs[0].Status != domain.StepStatusSkipped || execs[0].ErrorDetail == nil ||
			execs[0].ErrorDetail.Classification != domain.FailureRunTerminated {
			t.Fatalf("%s: live execution after the timeout = %s %+v, want skipped/run_terminated", id, execs[0].Status, execs[0].ErrorDetail)
		}
		if got := t008RunStatus(t, st, id+"-orphan"); got != domain.RunStatusActive {
			t.Fatalf("%s: the timeout tick moved the orphan to %s", id, got)
		}
	}

	// Orphans: an active run with no activity for over three thresholds is
	// failed, in each workspace.
	ms.ScanOrphans(ctx)
	noRefs("after the orphan tick")
	for _, id := range []string{"ws-a", "ws-b"} {
		if got := t008RunStatus(t, stores[id], id+"-orphan"); got != domain.RunStatusFailed {
			t.Fatalf("%s: orphaned run is %s after one orphan tick, want failed", id, got)
		}
	}

	// Commit retries: each workspace's committing run gets its governed
	// merge retried by its own orchestrator, on its own repository and
	// database; with no run branch to merge the retry completes it.
	ms.RunCommitRetry(ctx)
	noRefs("after the commit-retry tick")
	for _, id := range []string{"ws-a", "ws-b"} {
		if got := t008RunStatus(t, stores[id], id+"-commit"); got != domain.RunStatusCompleted {
			t.Fatalf("%s: committing run is %s after one commit-retry tick, want completed by the retried merge", id, got)
		}
	}

	// Runtime changes, the way the operator API makes them: ws-c is
	// created (the provider's list cache dropped), ws-b deactivated (the
	// resolver cache dropped and its services evicted).
	if err := ws.Wiring.DBProvider.CreateWorkspace(ctx, workspace.Config{
		ID: "ws-c", DisplayName: "ws-c", DatabaseURL: secrets.NewSecretValue([]byte(dsns["ws-c"])),
		RepoPath: testutil.NewTempRepo(t), Status: workspace.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	ws.Wiring.DBProvider.Invalidate("ws-c")
	if err := ws.Wiring.DBProvider.DeactivateWorkspace(ctx, "ws-b"); err != nil {
		t.Fatal(err)
	}
	ws.Wiring.DBProvider.Invalidate("ws-b")
	ws.Wiring.Pool.Evict("ws-b")
	t008TimedOutRun(t, stores["ws-c"], "ws-c-timeout")
	t008TimedOutRun(t, stores["ws-b"], "ws-b-timeout-after-deactivation")

	ms.ScanTimeouts(ctx)
	noRefs("after the timeout tick following the runtime changes")
	if got := t008RunStatus(t, stores["ws-c"], "ws-c-timeout"); got != domain.RunStatusCancelled {
		t.Fatalf("workspace added at runtime: its timed-out run is %s after the next tick, want cancelled", got)
	}
	if got := t008RunStatus(t, stores["ws-b"], "ws-b-timeout-after-deactivation"); got != domain.RunStatusActive {
		t.Fatalf("deactivated workspace: its run was moved to %s; a deactivated workspace must not be scheduled", got)
	}
}
