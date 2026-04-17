package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/workflow"
)

// ── Fake Workflow Service ──

type fakeWorkflowSvc struct {
	createErr        error
	updateErr        error
	readErr          error
	listErr          error
	lastCreateID     string
	lastCreateBody   string
	lastCreateBranch string
	lastUpdateID     string
	lastUpdateBody   string
	lastUpdateBranch string
	lastReadID       string
	lastValidateID   string
	validateStatus   string
	validateErrors   []domain.ValidationError
	stored           map[string]*domain.WorkflowDefinition
}

func newFakeWorkflowSvc() *fakeWorkflowSvc {
	return &fakeWorkflowSvc{
		stored: map[string]*domain.WorkflowDefinition{},
	}
}

func (f *fakeWorkflowSvc) Create(ctx context.Context, id, body string) (*workflow.WriteResult, error) {
	f.lastCreateID = id
	f.lastCreateBody = body
	if wc := workflow.GetWriteContext(ctx); wc != nil {
		f.lastCreateBranch = wc.Branch
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	wf := &domain.WorkflowDefinition{ID: id, Name: id, Version: "1.0.0", Status: domain.WorkflowStatusActive, Path: "workflows/" + id + ".yaml"}
	f.stored[id] = wf
	return &workflow.WriteResult{Workflow: wf, Path: wf.Path, CommitSHA: "cafef00d"}, nil
}

func (f *fakeWorkflowSvc) Update(ctx context.Context, id, body string) (*workflow.WriteResult, error) {
	f.lastUpdateID = id
	f.lastUpdateBody = body
	if wc := workflow.GetWriteContext(ctx); wc != nil {
		f.lastUpdateBranch = wc.Branch
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	wf, ok := f.stored[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "not found")
	}
	wf.Version = "1.0.1"
	return &workflow.WriteResult{Workflow: wf, Path: wf.Path, CommitSHA: "deadbeef"}, nil
}

func (f *fakeWorkflowSvc) Read(_ context.Context, id, _ string) (*workflow.ReadResult, error) {
	f.lastReadID = id
	if f.readErr != nil {
		return nil, f.readErr
	}
	wf, ok := f.stored[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "not found")
	}
	return &workflow.ReadResult{Workflow: wf, Path: wf.Path, Body: "id: " + id + "\n", SourceCommit: "abc1234"}, nil
}

func (f *fakeWorkflowSvc) List(_ context.Context, _ workflow.ListOptions) ([]*domain.WorkflowDefinition, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*domain.WorkflowDefinition, 0, len(f.stored))
	for _, wf := range f.stored {
		out = append(out, wf)
	}
	return out, nil
}

func (f *fakeWorkflowSvc) ValidateBody(_ context.Context, id, _ string) domain.ValidationResult {
	f.lastValidateID = id
	status := f.validateStatus
	if status == "" {
		status = "passed"
	}
	return domain.ValidationResult{Status: status, Errors: f.validateErrors}
}

// ── Test harness ──

func newWorkflowServer(t *testing.T, svc gateway.WorkflowService, role domain.ActorRole) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: role, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, err := authSvc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc, Workflows: svc})
	return httptest.NewServer(srv.Handler()), token
}

func doWorkflowJSON(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

// ── Tests ──

func TestWorkflowCreate_Success(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows", tok, `{"id":"task-default","body":"id: task-default\n"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["id"] != "task-default" {
		t.Errorf("id: %v", body["id"])
	}
	if body["commit_sha"] != "cafef00d" {
		t.Errorf("commit_sha: %v", body["commit_sha"])
	}
	if svc.lastCreateBody == "" {
		t.Error("body not forwarded to service")
	}
}

func TestWorkflowCreate_Forbidden(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleContributor)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows", tok, `{"id":"x","body":"y"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestWorkflowCreate_MissingFields(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows", tok, `{"id":"only-id"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestWorkflowCreate_ValidationFailed(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.createErr = domain.NewErrorWithDetail(domain.ErrValidationFailed, "bad", []domain.ValidationError{{RuleID: "SCHEMA", Message: "x"}})
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows", tok, `{"id":"bad","body":"xxx"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestWorkflowUpdate_Success(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.stored["task-default"] = &domain.WorkflowDefinition{ID: "task-default", Version: "1.0.0", Path: "workflows/task-default.yaml"}
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "PUT", ts.URL+"/api/v1/workflows/task-default", tok, `{"body":"new"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if svc.lastUpdateID != "task-default" {
		t.Errorf("id forwarded: %q", svc.lastUpdateID)
	}
}

func TestWorkflowUpdate_NotFound(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.updateErr = domain.NewError(domain.ErrNotFound, "missing")
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "PUT", ts.URL+"/api/v1/workflows/missing", tok, `{"body":"x"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestWorkflowRead_Success(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.stored["task-default"] = &domain.WorkflowDefinition{ID: "task-default", Version: "1.0.0", Path: "workflows/task-default.yaml", Name: "Task Default"}
	ts, tok := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "GET", ts.URL+"/api/v1/workflows/task-default", tok, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["id"] != "task-default" {
		t.Errorf("id: %v", body["id"])
	}
	if body["body"] == "" {
		t.Errorf("body missing")
	}
	if body["source_commit"] != "abc1234" {
		t.Errorf("source_commit: %v", body["source_commit"])
	}
}

func TestWorkflowRead_NotFound(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "GET", ts.URL+"/api/v1/workflows/missing", tok, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestWorkflowList_Success(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.stored["a"] = &domain.WorkflowDefinition{ID: "a", Version: "1", Path: "workflows/a.yaml"}
	svc.stored["b"] = &domain.WorkflowDefinition{ID: "b", Version: "2", Path: "workflows/b.yaml"}
	ts, tok := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "GET", ts.URL+"/api/v1/workflows", tok, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Items) != 2 {
		t.Errorf("items: %d", len(body.Items))
	}
}

func TestWorkflowValidate_Success(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows/task-default/validate", tok, `{"body":"yaml"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if svc.lastValidateID != "task-default" {
		t.Errorf("id: %q", svc.lastValidateID)
	}
}

func TestWorkflowValidate_Failed(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.validateStatus = "failed"
	svc.validateErrors = []domain.ValidationError{{RuleID: "SCHEMA", Message: "bad"}}
	ts, tok := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows/bad/validate", tok, `{"body":"yaml"}`)
	defer resp.Body.Close()

	// validate returns 200 with a failed status in the body per spec.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "failed" {
		t.Errorf("status: %v", body["status"])
	}
}

func TestWorkflow_Unauthenticated(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, _ := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/workflows", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_ServiceNotConfigured(t *testing.T) {
	ts, tok := newWorkflowServer(t, nil, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows", tok, `{"id":"x","body":"y"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_UpdateMissingBody(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "PUT", ts.URL+"/api/v1/workflows/x", tok, `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_ValidateMissingBody(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows/x/validate", tok, `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_WildcardUnknownMethod(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "DELETE", ts.URL+"/api/v1/workflows/x", tok, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_ListServiceError(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.listErr = domain.NewError(domain.ErrInternal, "boom")
	ts, tok := newWorkflowServer(t, svc, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "GET", ts.URL+"/api/v1/workflows", tok, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

// workflowPlanningStore returns a planning-mode Active run from GetRun so that
// resolveWriteContext routes writes to the run's branch without requiring a
// task_path (the workflow lifecycle is a planning flow per ADR-006/008).
type workflowPlanningStore struct {
	fakeStore
	branch string
}

func (s *workflowPlanningStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	return &domain.Run{
		RunID:      runID,
		Status:     domain.RunStatusActive,
		Mode:       domain.RunModePlanning,
		BranchName: s.branch,
	}, nil
}

func newWorkflowServerWithStore(t *testing.T, svc gateway.WorkflowService, store *workflowPlanningStore, role domain.ActorRole) (*httptest.Server, string) {
	t.Helper()
	store.fakeStore = *newFakeStore()
	store.actors["actor-1"] = &domain.Actor{
		ActorID: "actor-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: role, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(store)
	token, _, err := authSvc.CreateToken(context.Background(), "actor-1", "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: store, Auth: authSvc, Workflows: svc})
	return httptest.NewServer(srv.Handler()), token
}

func TestWorkflowCreate_WithWriteContext_RoutesToBranch(t *testing.T) {
	svc := newFakeWorkflowSvc()
	store := &workflowPlanningStore{branch: "spine/run/wf-42"}
	ts, tok := newWorkflowServerWithStore(t, svc, store, domain.RoleReviewer)
	defer ts.Close()

	body := `{"id":"task-default","body":"id: task-default\n","write_context":{"run_id":"run-abc"}}`
	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows", tok, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["write_mode"] != "proposed" {
		t.Errorf("write_mode: %v", out["write_mode"])
	}
	if svc.lastCreateBranch != "spine/run/wf-42" {
		t.Errorf("branch not forwarded: %q", svc.lastCreateBranch)
	}
}

func TestWorkflowCreate_WithoutWriteContext_AuthoritativeMode(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()

	body := `{"id":"task-default","body":"id: task-default\n"}`
	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows", tok, body)
	defer resp.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["write_mode"] != "authoritative" {
		t.Errorf("write_mode: %v", out["write_mode"])
	}
	if svc.lastCreateBranch != "" {
		t.Errorf("expected no branch, got %q", svc.lastCreateBranch)
	}
}

func TestWorkflowUpdate_WithWriteContext_RoutesToBranch(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.stored["task-default"] = &domain.WorkflowDefinition{ID: "task-default", Version: "1.0.0", Path: "workflows/task-default.yaml"}
	store := &workflowPlanningStore{branch: "spine/run/wf-99"}
	ts, tok := newWorkflowServerWithStore(t, svc, store, domain.RoleReviewer)
	defer ts.Close()

	body := `{"body":"new","write_context":{"run_id":"run-99"}}`
	resp := doWorkflowJSON(t, "PUT", ts.URL+"/api/v1/workflows/task-default", tok, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["write_mode"] != "proposed" {
		t.Errorf("write_mode: %v", out["write_mode"])
	}
	if svc.lastUpdateBranch != "spine/run/wf-99" {
		t.Errorf("branch not forwarded: %q", svc.lastUpdateBranch)
	}
}

func TestWorkflow_UpdateNotConfigured(t *testing.T) {
	ts, tok := newWorkflowServer(t, nil, domain.RoleReviewer)
	defer ts.Close()

	resp := doWorkflowJSON(t, "PUT", ts.URL+"/api/v1/workflows/x", tok, `{"body":"b"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_ReadNotConfigured(t *testing.T) {
	ts, tok := newWorkflowServer(t, nil, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "GET", ts.URL+"/api/v1/workflows/x", tok, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_ListNotConfigured(t *testing.T) {
	ts, tok := newWorkflowServer(t, nil, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "GET", ts.URL+"/api/v1/workflows", tok, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestWorkflow_ValidateNotConfigured(t *testing.T) {
	ts, tok := newWorkflowServer(t, nil, domain.RoleReader)
	defer ts.Close()

	resp := doWorkflowJSON(t, "POST", ts.URL+"/api/v1/workflows/x/validate", tok, `{"body":"b"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d", resp.StatusCode)
	}
}
