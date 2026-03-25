package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/scheduler"
	"github.com/bszymi/spine/internal/store"
)

// ── Fake Store ──

type fakeStore struct {
	store.Store // embed to satisfy interface; only override what we need

	runs         []domain.Run
	stepExecs    []domain.StepExecution
	workflows    map[string]*store.WorkflowProjection
	updatedRuns  map[string]domain.RunStatus
	updatedSteps map[string]*domain.StepExecution
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		workflows:    make(map[string]*store.WorkflowProjection),
		updatedRuns:  make(map[string]domain.RunStatus),
		updatedSteps: make(map[string]*domain.StepExecution),
	}
}

func (f *fakeStore) ListRunsByStatus(_ context.Context, status domain.RunStatus) ([]domain.Run, error) {
	var result []domain.Run
	for i := range f.runs {
		if f.runs[i].Status == status {
			result = append(result, f.runs[i])
		}
	}
	return result, nil
}

func (f *fakeStore) ListActiveStepExecutions(_ context.Context) ([]domain.StepExecution, error) {
	var result []domain.StepExecution
	for i := range f.stepExecs {
		if !f.stepExecs[i].Status.IsTerminal() {
			result = append(result, f.stepExecs[i])
		}
	}
	return result, nil
}

func (f *fakeStore) ListTimedOutRuns(_ context.Context, now time.Time) ([]domain.Run, error) {
	var result []domain.Run
	for i := range f.runs {
		r := &f.runs[i]
		if (r.Status == domain.RunStatusActive || r.Status == domain.RunStatusPaused) &&
			r.TimeoutAt != nil && !r.TimeoutAt.After(now) {
			result = append(result, *r)
		}
	}
	return result, nil
}

func (f *fakeStore) ListStaleActiveRuns(_ context.Context, noActivitySince time.Time) ([]domain.Run, error) {
	var result []domain.Run
	for i := range f.runs {
		if f.runs[i].Status == domain.RunStatusActive && f.runs[i].CreatedAt.Before(noActivitySince) {
			result = append(result, f.runs[i])
		}
	}
	return result, nil
}

func (f *fakeStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	for i := range f.runs {
		if f.runs[i].RunID == runID {
			return &f.runs[i], nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "run not found")
}

func (f *fakeStore) ListStepExecutionsByRun(_ context.Context, runID string) ([]domain.StepExecution, error) {
	var result []domain.StepExecution
	for i := range f.stepExecs {
		if f.stepExecs[i].RunID == runID {
			result = append(result, f.stepExecs[i])
		}
	}
	return result, nil
}

func (f *fakeStore) UpdateRunStatus(_ context.Context, runID string, status domain.RunStatus) error {
	f.updatedRuns[runID] = status
	for i := range f.runs {
		if f.runs[i].RunID == runID {
			f.runs[i].Status = status
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "run not found")
}

func (f *fakeStore) UpdateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	f.updatedSteps[exec.ExecutionID] = exec
	for i := range f.stepExecs {
		if f.stepExecs[i].ExecutionID == exec.ExecutionID {
			f.stepExecs[i] = *exec
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "step execution not found")
}

func (f *fakeStore) GetWorkflowProjection(_ context.Context, workflowPath string) (*store.WorkflowProjection, error) {
	proj, ok := f.workflows[workflowPath]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "workflow not found")
	}
	return proj, nil
}

func (f *fakeStore) WithTx(_ context.Context, fn func(tx store.Tx) error) error {
	return fn(&fakeTx{store: f})
}

type fakeTx struct {
	store *fakeStore
}

func (t *fakeTx) CreateRun(_ context.Context, run *domain.Run) error {
	t.store.runs = append(t.store.runs, *run)
	return nil
}

func (t *fakeTx) UpdateRunStatus(_ context.Context, runID string, status domain.RunStatus) error {
	t.store.updatedRuns[runID] = status
	for i := range t.store.runs {
		if t.store.runs[i].RunID == runID {
			t.store.runs[i].Status = status
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "run not found")
}

func (t *fakeTx) CreateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	t.store.stepExecs = append(t.store.stepExecs, *exec)
	return nil
}

func (t *fakeTx) UpdateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	t.store.updatedSteps[exec.ExecutionID] = exec
	for i := range t.store.stepExecs {
		if t.store.stepExecs[i].ExecutionID == exec.ExecutionID {
			t.store.stepExecs[i] = *exec
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "step execution not found")
}

// ── Fake Event Router ──

type fakeEventRouter struct {
	events []domain.Event
}

func (f *fakeEventRouter) Emit(_ context.Context, evt domain.Event) error {
	f.events = append(f.events, evt)
	return nil
}

func (f *fakeEventRouter) Subscribe(_ context.Context, _ domain.EventType, _ event.EventHandler) error {
	return nil
}

// ── Helper ──

func workflowWithStep(stepID, timeout, timeoutOutcome string, retryLimit int) []byte {
	wf := domain.WorkflowDefinition{
		ID:        "wf-1",
		Name:      "test-workflow",
		EntryStep: stepID,
		Steps: []domain.StepDefinition{
			{
				ID:             stepID,
				Name:           stepID,
				Timeout:        timeout,
				TimeoutOutcome: timeoutOutcome,
			},
		},
	}
	if retryLimit > 0 {
		wf.Steps[0].Retry = &domain.RetryConfig{Limit: retryLimit, Backoff: "exponential"}
	}
	b, _ := json.Marshal(wf)
	return b
}

// ── Timeout Scanner Tests ──

func TestScanTimeoutsExpiredStep(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusInProgress, StartedAt: &started},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, ok := fs.updatedSteps["e1"]
	if !ok {
		t.Fatal("expected step e1 to be updated")
	}
	if updated.Status != domain.StepStatusFailed {
		t.Errorf("expected failed, got %s", updated.Status)
	}
	if updated.ErrorDetail == nil || updated.ErrorDetail.Classification != domain.FailureTimeout {
		t.Error("expected FailureTimeout classification")
	}
	if len(events.events) != 1 || events.events[0].Type != domain.EventStepTimeout {
		t.Error("expected step_timeout event")
	}
}

func TestScanTimeoutsNotExpired(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusInProgress, StartedAt: &started},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fs.updatedSteps) != 0 {
		t.Error("expected no step updates for non-expired timeout")
	}
}

func TestScanTimeoutsNoTimeout(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusInProgress, StartedAt: &started},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "", "", 0), // no timeout
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fs.updatedSteps) != 0 {
		t.Error("expected no updates for step without timeout")
	}
}

func TestScanTimeoutsWithTimeoutOutcome(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusInProgress, StartedAt: &started},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "auto_approve", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := fs.updatedSteps["e1"]
	if updated.Status != domain.StepStatusCompleted {
		t.Errorf("expected completed (timeout_outcome), got %s", updated.Status)
	}
	if updated.OutcomeID != "auto_approve" {
		t.Errorf("expected outcome_id=auto_approve, got %s", updated.OutcomeID)
	}
}

func TestScanTimeoutsIdempotent(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusInProgress, StartedAt: &started},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	// First scan marks it as failed
	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	// Second scan should not error — step is now terminal and won't be listed
	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("second scan: %v", err)
	}
}

func TestScanTimeoutsWaitingStepExpired(t *testing.T) {
	// Waiting steps don't have StartedAt but can time out via CreatedAt.
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusWaiting, CreatedAt: time.Now().Add(-2 * time.Hour)},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, ok := fs.updatedSteps["e1"]
	if !ok {
		t.Fatal("expected waiting step to be timed out")
	}
	if updated.Status != domain.StepStatusFailed {
		t.Errorf("expected failed, got %s", updated.Status)
	}
}

func TestScanTimeoutsWaitingStepNotExpired(t *testing.T) {
	// Waiting step created recently — should not time out.
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusWaiting, CreatedAt: time.Now().Add(-10 * time.Minute)},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fs.updatedSteps) != 0 {
		t.Error("expected no updates for non-expired waiting step")
	}
}

// ── Orphan Scanner Tests ──

func TestScanOrphansDetectsStaleRun(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{
			RunID:     "r1",
			Status:    domain.RunStatusActive,
			CreatedAt: time.Now().Add(-10 * time.Minute),
		},
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events, scheduler.WithOrphanThreshold(5*time.Minute))

	if err := sched.ScanOrphans(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Orphan detection only logs — verify no state changes
	if len(fs.updatedRuns) != 0 {
		t.Error("orphan detector should not modify run state")
	}
}

func TestScanOrphansIgnoresRecentRun(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{
			RunID:     "r1",
			Status:    domain.RunStatusActive,
			CreatedAt: time.Now().Add(-1 * time.Minute),
		},
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events, scheduler.WithOrphanThreshold(5*time.Minute))

	if err := sched.ScanOrphans(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── Recovery Tests ──

func TestRecoverPendingRuns(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusPending, WorkflowPath: "wf/test.yaml"},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PendingActivated != 1 {
		t.Errorf("expected 1 pending activated, got %d", result.PendingActivated)
	}
	if fs.updatedRuns["r1"] != domain.RunStatusActive {
		t.Errorf("expected active, got %s", fs.updatedRuns["r1"])
	}
	// Verify entry step execution was created
	if len(fs.stepExecs) != 1 {
		t.Fatalf("expected 1 step execution, got %d", len(fs.stepExecs))
	}
	if fs.stepExecs[0].StepID != "step1" {
		t.Errorf("expected step1, got %s", fs.stepExecs[0].StepID)
	}
	if fs.stepExecs[0].Status != domain.StepStatusWaiting {
		t.Errorf("expected waiting, got %s", fs.stepExecs[0].Status)
	}
}

func TestRecoverActiveRunWithWaitingStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusWaiting, Attempt: 1},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ActiveResumed != 1 {
		t.Errorf("expected 1 active resumed, got %d", result.ActiveResumed)
	}
}

func TestRecoverActiveRunWithAssignedStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusAssigned, Attempt: 1},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ActiveResumed != 1 {
		t.Errorf("expected 1 active resumed, got %d", result.ActiveResumed)
	}
	// Assigned step should be reset to waiting
	updated := fs.updatedSteps["e1"]
	if updated == nil {
		t.Fatal("expected step e1 to be updated")
	}
	if updated.Status != domain.StepStatusWaiting {
		t.Errorf("expected waiting, got %s", updated.Status)
	}
}

func TestRecoverActiveRunWithFailedRetryableStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{
			ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusFailed, Attempt: 1,
			ErrorDetail: &domain.ErrorDetail{Classification: domain.FailureTransient, Message: "timeout"},
		},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 3),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StepsRecovered != 1 {
		t.Errorf("expected 1 step recovered, got %d", result.StepsRecovered)
	}
}

func TestRecoverCommittingRuns(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusCommitting},
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CommittingFound != 1 {
		t.Errorf("expected 1 committing found, got %d", result.CommittingFound)
	}
	// Should NOT modify state — just log
	if len(fs.updatedRuns) != 0 {
		t.Error("committing runs should not be modified")
	}
}

func TestRecoverEmptyDatabase(t *testing.T) {
	fs := newFakeStore()
	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PendingActivated != 0 || result.ActiveResumed != 0 || result.CommittingFound != 0 {
		t.Error("expected all zeros for empty database")
	}
	// Should still emit engine_recovered event
	found := false
	for _, evt := range events.events {
		if evt.Type == domain.EventEngineRecovered {
			found = true
		}
	}
	if !found {
		t.Error("expected engine_recovered event")
	}
}

func TestRecoverTerminalRunsIgnored(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusCompleted},
		{RunID: "r2", Status: domain.RunStatusFailed},
		{RunID: "r3", Status: domain.RunStatusCancelled},
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PendingActivated != 0 || result.ActiveResumed != 0 || result.CommittingFound != 0 {
		t.Error("terminal runs should be ignored")
	}
}

func TestRecoverActiveRunWithInProgressStep(t *testing.T) {
	started := time.Now().Add(-30 * time.Minute)
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusInProgress, StartedAt: &started, Attempt: 1},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ActiveResumed != 1 {
		t.Errorf("expected 1 active resumed, got %d", result.ActiveResumed)
	}
	// In-progress step left as-is for timeout scanner
	if len(fs.updatedSteps) != 0 {
		t.Error("in-progress step should not be modified during recovery")
	}
}

func TestRecoverActiveRunWithBlockedStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusBlocked, Attempt: 1},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ActiveResumed != 1 {
		t.Errorf("expected 1 active resumed, got %d", result.ActiveResumed)
	}
	// Blocked step should remain unchanged
	if len(fs.updatedSteps) != 0 {
		t.Error("blocked step should not be modified during recovery")
	}
}

func TestRecoverActiveRunWithCompletedStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusCompleted, Attempt: 1, OutcomeID: "accepted"},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ActiveResumed != 1 {
		t.Errorf("expected 1 active resumed, got %d", result.ActiveResumed)
	}
}

func TestRecoverActiveRunWithNonRetryableFailedStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{
			ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusFailed, Attempt: 1,
			ErrorDetail: &domain.ErrorDetail{Classification: domain.FailurePermanent, Message: "fatal"},
		},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 3),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StepsRecovered != 1 {
		t.Errorf("expected 1 step recovered, got %d", result.StepsRecovered)
	}
}

func TestRecoverActiveRunWithSkippedStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusSkipped, Attempt: 1},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ActiveResumed != 1 {
		t.Errorf("expected 1 active resumed, got %d", result.ActiveResumed)
	}
}

func TestRecoverActiveRunNoCurrentStep(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: ""},
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Active run with no current step is warned but counted
	if result.ActiveResumed != 0 {
		t.Errorf("expected 0 active resumed for run with no step, got %d", result.ActiveResumed)
	}
}

func TestRecoverMultipleRuns(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusPending, WorkflowPath: "wf/test.yaml"},
		{RunID: "r2", Status: domain.RunStatusPending, WorkflowPath: "wf/test.yaml"},
		{RunID: "r3", Status: domain.RunStatusCommitting},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PendingActivated != 2 {
		t.Errorf("expected 2 pending activated, got %d", result.PendingActivated)
	}
	if result.CommittingFound != 1 {
		t.Errorf("expected 1 committing found, got %d", result.CommittingFound)
	}
}

func TestStartStop(t *testing.T) {
	fs := newFakeStore()
	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events,
		scheduler.WithTimeoutScanInterval(10*time.Millisecond),
		scheduler.WithOrphanScanInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.Start(ctx)
		close(done)
	}()

	// Let it run a few scan cycles
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good — scheduler stopped
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after context cancellation")
	}
}

func TestStopViaMethod(t *testing.T) {
	fs := newFakeStore()
	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events,
		scheduler.WithTimeoutScanInterval(10*time.Millisecond),
		scheduler.WithOrphanScanInterval(10*time.Millisecond),
	)

	done := make(chan struct{})
	go func() {
		sched.Start(context.Background())
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	sched.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after Stop()")
	}
}

func TestRecoverFailedStepNoErrorDetail(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusFailed, Attempt: 1},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 3),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No error detail defaults to transient, which is retryable
	if result.StepsRecovered != 1 {
		t.Errorf("expected 1 step recovered, got %d", result.StepsRecovered)
	}
}

func TestScanTimeoutsAssignedStepTimedOut(t *testing.T) {
	// Assigned steps use CreatedAt for timeout since StartedAt is nil
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "e1", RunID: "r1", StepID: "step1", Status: domain.StepStatusAssigned, CreatedAt: time.Now().Add(-2 * time.Hour)},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("step1", "1h", "", 0),
	}

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	if err := sched.ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := fs.updatedSteps["e1"]
	if updated == nil {
		t.Fatal("expected assigned step to be timed out")
	}
	if updated.Status != domain.StepStatusFailed {
		t.Errorf("expected failed, got %s", updated.Status)
	}
}

func TestRecoverPendingRunMissingWorkflow(t *testing.T) {
	// Test recovery when workflow lookup fails (no projection)
	fs := newFakeStore()
	fs.runs = []domain.Run{
		{RunID: "r1", Status: domain.RunStatusPending, WorkflowPath: "wf/missing.yaml"},
	}
	// No workflow in store → lookupEntryStep should handle gracefully

	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events)

	result, err := sched.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should skip (can't resolve entry step) but not crash
	_ = result
}

// ── Backoff Tests ──

func TestCalculateBackoffExponential(t *testing.T) {
	d0 := scheduler.CalculateBackoff(0, "exponential")
	d1 := scheduler.CalculateBackoff(1, "exponential")
	d2 := scheduler.CalculateBackoff(2, "exponential")

	// Base is 1s, so attempt 0 = 1s + jitter, attempt 1 = 2s + jitter, attempt 2 = 4s + jitter
	if d0 < 1*time.Second || d0 >= 2*time.Second {
		t.Errorf("attempt 0: expected [1s, 2s), got %v", d0)
	}
	if d1 < 2*time.Second || d1 >= 3*time.Second {
		t.Errorf("attempt 1: expected [2s, 3s), got %v", d1)
	}
	if d2 < 4*time.Second || d2 >= 5*time.Second {
		t.Errorf("attempt 2: expected [4s, 5s), got %v", d2)
	}
}

func TestCalculateBackoffLinear(t *testing.T) {
	d0 := scheduler.CalculateBackoff(0, "linear")
	d1 := scheduler.CalculateBackoff(1, "linear")

	if d0 < 1*time.Second || d0 >= 2*time.Second {
		t.Errorf("attempt 0: expected [1s, 2s), got %v", d0)
	}
	if d1 < 2*time.Second || d1 >= 3*time.Second {
		t.Errorf("attempt 1: expected [2s, 3s), got %v", d1)
	}
}

func TestCalculateBackoffMaxCap(t *testing.T) {
	d := scheduler.CalculateBackoff(100, "exponential")
	if d > 5*time.Minute {
		t.Errorf("expected max 5m, got %v", d)
	}
}

func TestCalculateBackoffDefaultIsExponential(t *testing.T) {
	d := scheduler.CalculateBackoff(0, "")
	if d < 1*time.Second || d >= 2*time.Second {
		t.Errorf("default should be exponential: expected [1s, 2s), got %v", d)
	}
}

// ── Options Tests ──

func TestWithOptions(t *testing.T) {
	fs := newFakeStore()
	events := &fakeEventRouter{}
	sched := scheduler.New(fs, events,
		scheduler.WithTimeoutScanInterval(10*time.Second),
		scheduler.WithOrphanScanInterval(20*time.Second),
		scheduler.WithOrphanThreshold(3*time.Minute),
	)
	// Options are applied correctly if the scheduler doesn't panic
	if sched == nil {
		t.Fatal("expected non-nil scheduler")
	}
}
