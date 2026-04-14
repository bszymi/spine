package gateway_test

// handlers_coverage_test.go contains targeted tests to increase coverage on
// specific uncovered paths discovered during coverage analysis.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

// ── runStatusFakeStore: GetRun returns a run with timestamps ──

type runStatusFakeStore struct {
	*fakeStore
	run *domain.Run
}

func (r *runStatusFakeStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	if r.run != nil {
		r.run.RunID = runID
		return r.run, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "run not found")
}

func (r *runStatusFakeStore) ListStepExecutionsByRun(_ context.Context, _ string) ([]domain.StepExecution, error) {
	if r.fakeStore.pingErr != nil {
		return nil, r.fakeStore.pingErr
	}
	return nil, nil
}

func newRunStatusServer(t *testing.T, run *domain.Run) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	rfs := &runStatusFakeStore{fakeStore: fs, run: run}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: rfs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), token
}

// TestHandleRunStatus_WithStartedAt verifies that started_at appears in the response.
func TestHandleRunStatus_WithStartedAt(t *testing.T) {
	startedAt := time.Now().Add(-time.Hour)
	run := &domain.Run{
		Status:        domain.RunStatusActive,
		CurrentStepID: "step1",
		BranchName:    "spine/run/test-branch",
		StartedAt:     &startedAt,
	}
	ts, token := newRunStatusServer(t, run)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-123", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["started_at"] == nil {
		t.Error("expected started_at in response")
	}
}

// TestHandleRunStatus_WithCompletedAt verifies that completed_at appears in the response.
func TestHandleRunStatus_WithCompletedAt(t *testing.T) {
	completedAt := time.Now().Add(-10 * time.Minute)
	run := &domain.Run{
		Status:      domain.RunStatusCompleted,
		BranchName:  "spine/run/test-branch",
		CompletedAt: &completedAt,
	}
	ts, token := newRunStatusServer(t, run)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-123", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["completed_at"] == nil {
		t.Error("expected completed_at in response")
	}
}

// TestHandleRunStatus_NotFound verifies GetRun error → 404.
func TestHandleRunStatus_NotFound(t *testing.T) {
	ts, token := newRunStatusServer(t, nil) // nil run → GetRun returns 404
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/nonexistent", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 (run not found), got %d", resp.StatusCode)
	}
}

// TestHandleRunStatus_ListStepsError verifies ListStepExecutionsByRun error → 500.
func TestHandleRunStatus_ListStepsError(t *testing.T) {
	run := &domain.Run{
		Status:        domain.RunStatusActive,
		CurrentStepID: "step1",
		BranchName:    "spine/run/test-branch",
	}
	fs := newFakeStore()
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	// pingErr is repurposed as signal to fail ListStepExecutionsByRun
	fs.pingErr = domain.NewError(domain.ErrInternal, "db error")
	rfs := &runStatusFakeStore{fakeStore: fs, run: run}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: rfs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/runs/run-123", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 (list steps error), got %d", resp.StatusCode)
	}
}

// ── resolveStepDef tests via handleStepAssign ──

type stepAssignWorkflowStore struct {
	*fakeStore
}

func (s *stepAssignWorkflowStore) GetWorkflowProjection(_ context.Context, _ string) (*store.WorkflowProjection, error) {
	wfDef := domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "step1", Name: "Execute Step"},
		},
	}
	data, _ := json.Marshal(wfDef)
	return &store.WorkflowProjection{Definition: data}, nil
}

func (s *stepAssignWorkflowStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	return &domain.Run{
		RunID:         runID,
		Status:        domain.RunStatusActive,
		CurrentStepID: "step1",
		BranchName:    "spine/run/test-branch",
		WorkflowPath:  "workflows/default.yaml",
	}, nil
}

// TestHandleStepAssign_WithResolveStepDef exercises the resolveStepDef path
// by providing a workflowProjection with step definitions and a Validator so
// the precondition path inside handleStepAssign is reached.
func TestHandleStepAssign_WithResolveStepDef(t *testing.T) {
	fs := newFakeStore()
	fs.actors["op-1"] = &domain.Actor{
		ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Op",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	st := &stepAssignWorkflowStore{fakeStore: fs}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "op-1", "test", nil)
	// Provide a Validator so validatorFrom returns non-nil → resolveStepDef is called.
	validator := validation.NewEngine(fs)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     st,
		Auth:      authSvc,
		Validator: validator,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"actor_id":"op-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-123/steps/step1/assign", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Expect 200: step has no preconditions → precondition check passes → assign succeeds.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ── ExecutionQuery error paths ──

func newExecQueryStoreError(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["op-1"] = &domain.Actor{
		ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Operator",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	fs.execProjErr = domain.NewError(domain.ErrInternal, "store error")
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "op-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleExecutionTasksBlocked_StoreError(t *testing.T) {
	ts, token := newExecQueryStoreError(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks/blocked", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandleExecutionTasksAll_StoreError(t *testing.T) {
	ts, token := newExecQueryStoreError(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

func TestHandleExecutionTasksAssigned_StoreError(t *testing.T) {
	ts, token := newExecQueryStoreError(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/execution/tasks/assigned?actor_id=op-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// TestHandleExecutionTasksReady_StoreError2 alias to catch the auth-fail path in blocked handler.
func TestHandleExecutionTasksBlocked_Unauthorized(t *testing.T) {
	fs := newFakeStore()
	authSvc := auth.NewService(fs)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/execution/tasks/blocked")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ── OperatorTokenMiddleware: no token configured ──

func TestOperatorTokenMiddleware_NoTokenConfigured(t *testing.T) {
	// Do NOT set SPINE_OPERATOR_TOKEN — middleware returns 503.
	t.Setenv("SPINE_OPERATOR_TOKEN", "")
	srv := gateway.NewServer(":0", gateway.ServerConfig{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/workspaces", http.NoBody)
	req.Header.Set("Authorization", "Bearer some-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no operator token configured), got %d", resp.StatusCode)
	}
}

// TestOperatorTokenMiddleware_MissingAuthHeader verifies 401 when no Authorization header.
func TestOperatorTokenMiddleware_MissingAuthHeader(t *testing.T) {
	t.Setenv("SPINE_OPERATOR_TOKEN", "test-operator-token-32-chars-long-xx")
	srv := gateway.NewServer(":0", gateway.ServerConfig{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/workspaces", http.NoBody)
	// No Authorization header.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 (missing auth header), got %d", resp.StatusCode)
	}
}

// ── Token list error path ──

// errTokenListStore overrides ListTokensByActor to return an error.
type errTokenListStore struct {
	*fakeStore
}

func (e *errTokenListStore) ListTokensByActor(_ context.Context, _ string) ([]domain.Token, error) {
	return nil, domain.NewError(domain.ErrInternal, "db error")
}

func TestHandleTokenList_StoreError(t *testing.T) {
	fs := newFakeStore()
	fs.actors["admin-1"] = &domain.Actor{
		ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
		Role: domain.RoleAdmin, Status: domain.ActorStatusActive,
	}
	errSt := &errTokenListStore{fakeStore: fs}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "admin-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: errSt, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/tokens?actor_id=admin-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// ── resolveParentFromList: artSvc.List error ──

// errArtifactService always returns an error from List.
type errArtifactService struct {
	*fakeArtifactService
	listErr error
}

func (e *errArtifactService) List(_ context.Context, _ string) ([]*domain.Artifact, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.fakeArtifactService.List(context.Background(), "")
}

func TestHandleArtifactEntryCreate_ListError(t *testing.T) {
	errSvc := &errArtifactService{
		fakeArtifactService: newFakeArtifactService(),
		listErr:             domain.NewError(domain.ErrInternal, "git error"),
	}

	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:              fs,
		Auth:               authSvc,
		Artifacts:          errSvc,
		PlanningRunStarter: &fakePlanningRunStarterStub{},
		Git:                &fakeGitReader{files: map[string][]byte{}},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Task with parent → triggers resolveParentFromList → artSvc.List fails → 500
	body := `{"artifact_type":"Task","title":"My Task","parent":"EPIC-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 (list error), got %d", resp.StatusCode)
	}
}

// TestHandleArtifactEntryCreate_WrongParentForEpic verifies that providing a Task as parent
// for an Epic returns 400 (Epic requires an Initiative parent).
func TestHandleArtifactEntryCreate_WrongParentForEpic(t *testing.T) {
	artSvc := newFakeArtifactService()
	// Seed a Task as a fake parent (not a valid parent for Epic).
	artSvc.artifacts["initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001/task.md"] = &domain.Artifact{
		Path:  "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001/task.md",
		ID:    "TASK-001",
		Type:  domain.ArtifactTypeTask,
		Title: "A Task",
	}
	ts, token := newArtifactEntryFullServer(t, artSvc)
	defer ts.Close()

	// Epic with TASK-001 as parent → should fail because Epic requires Initiative parent.
	body := `{"artifact_type":"Epic","title":"My Epic","parent":"TASK-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/entry", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (wrong parent type for Epic), got %d", resp.StatusCode)
	}
}

// ── resolveParentFromRef: wrong parent type for Epic ──

// TestHandleArtifactAdd_PlanningMode_WrongParentForEpic verifies that resolveParentFromRef
// returns an error when the parent's type doesn't match the child type.
func TestHandleArtifactAdd_PlanningMode_WrongParentForEpic(t *testing.T) {
	artSvc := newFakeArtifactService()
	// Seed a Task (wrong type for Epic child).
	artSvc.artifacts["initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001/task.md"] = &domain.Artifact{
		Path:  "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001/task.md",
		ID:    "TASK-001",
		Type:  domain.ArtifactTypeTask,
		Title: "A Task",
	}
	ts, token := newArtifactAddFullServer(t, artSvc)
	defer ts.Close()

	// Epic with TASK-001 as parent → should fail because Epic requires Initiative parent.
	body := `{"run_id":"run-1","artifact_type":"Epic","title":"My Epic","parent":"TASK-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (wrong parent type), got %d", resp.StatusCode)
	}
}

// TestHandleArtifactAdd_PlanningMode_EpicWithInitiativeParent tests the Epic→Initiative
// success path in resolveParentFromRef.
func TestHandleArtifactAdd_PlanningMode_EpicWithInitiativeParent(t *testing.T) {
	artSvc := newFakeArtifactService()
	artSvc.artifacts["initiatives/INIT-001/initiative.md"] = &domain.Artifact{
		Path:  "initiatives/INIT-001/initiative.md",
		ID:    "INIT-001",
		Type:  domain.ArtifactTypeInitiative,
		Title: "My Initiative",
	}
	ts, token := newArtifactAddFullServer(t, artSvc)
	defer ts.Close()

	body := `{"run_id":"run-1","artifact_type":"Epic","title":"My Epic","parent":"INIT-001"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts/add", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

// ── Discussion error paths ──

type discussionErrStore struct {
	discussionStore
	listThreadsErr  error
	listCommentsErr error
	updateThreadErr error
}

func newErrDiscussionStore() *discussionErrStore {
	base := newDiscussionStore()
	return &discussionErrStore{discussionStore: *base}
}

func (d *discussionErrStore) ListThreads(_ context.Context, _ domain.AnchorType, _ string) ([]domain.DiscussionThread, error) {
	if d.listThreadsErr != nil {
		return nil, d.listThreadsErr
	}
	return d.discussionStore.ListThreads(context.Background(), "", "")
}

func (d *discussionErrStore) ListComments(_ context.Context, _ string) ([]domain.Comment, error) {
	if d.listCommentsErr != nil {
		return nil, d.listCommentsErr
	}
	return nil, nil
}

func (d *discussionErrStore) UpdateThread(_ context.Context, thread *domain.DiscussionThread) error {
	if d.updateThreadErr != nil {
		return d.updateThreadErr
	}
	return d.discussionStore.UpdateThread(context.Background(), thread)
}

func setupDiscussionErrServer(t *testing.T) (*httptest.Server, *discussionErrStore, string) {
	t.Helper()
	ds := newErrDiscussionStore()
	ds.discussionStore.actors["reviewer-1"] = &domain.Actor{
		ActorID: "reviewer-1", Type: domain.ActorTypeHuman, Name: "Reviewer",
		Role: domain.RoleReviewer, Status: domain.ActorStatusActive,
	}
	ds.discussionStore.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Contrib",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	ds.discussionStore.projections["tasks/TASK-001.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/TASK-001.md", ArtifactID: "TASK-001",
		ArtifactType: "Task", Title: "Test Task", Status: "Pending",
	}
	// Pre-seed a thread for tests that need one.
	ds.discussionStore.threads["thread-1"] = &domain.DiscussionThread{
		ThreadID:   "thread-1",
		AnchorType: domain.AnchorTypeArtifact,
		AnchorID:   "tasks/TASK-001.md",
		Status:     domain.ThreadStatusOpen,
	}

	authSvc := auth.NewService(&ds.discussionStore)
	reviewerToken, _, _ := authSvc.CreateToken(context.Background(), "reviewer-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: ds, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, ds, reviewerToken
}

func TestHandleDiscussionList_StoreError(t *testing.T) {
	ts, ds, token := setupDiscussionErrServer(t)
	ds.listThreadsErr = domain.NewError(domain.ErrInternal, "db error")

	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/discussions?anchor_type=artifact&anchor_id=tasks/TASK-001.md",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 (list threads error), got %d", resp.StatusCode)
	}
}

func TestHandleDiscussionGet_ListCommentsError(t *testing.T) {
	ts, ds, token := setupDiscussionErrServer(t)
	ds.listCommentsErr = domain.NewError(domain.ErrInternal, "db error")

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/discussions/thread-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 (list comments error), got %d", resp.StatusCode)
	}
}

func TestHandleDiscussionResolve_UpdateError(t *testing.T) {
	ts, ds, token := setupDiscussionErrServer(t)
	ds.updateThreadErr = domain.NewError(domain.ErrInternal, "db error")

	body := `{"resolution_type":"fixed"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/discussions/thread-1/resolve",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 (update thread error), got %d", resp.StatusCode)
	}
}
