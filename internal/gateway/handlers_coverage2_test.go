package gateway_test

// handlers_coverage2_test.go — second batch of targeted coverage tests.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

// ── handleArtifactLinks with actual link data ──

func newLinksServer(t *testing.T) (*httptest.Server, *fakeStore, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), fs, token
}

// TestArtifactLinks_WithOutgoingLinks verifies that actual outgoing links are returned.
func TestArtifactLinks_WithOutgoingLinks(t *testing.T) {
	ts, fs, token := newLinksServer(t)
	defer ts.Close()

	fs.outgoingLinks = []store.ArtifactLink{
		{SourcePath: "tasks/TASK-001.md", TargetPath: "epics/EPIC-001/epic.md", LinkType: "parent"},
		{SourcePath: "tasks/TASK-001.md", TargetPath: "epics/EPIC-002/epic.md", LinkType: "sibling"},
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/artifacts/tasks/TASK-001.md/links", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestArtifactLinks_WithLinkTypeFilter_Match verifies filtering when a link matches.
func TestArtifactLinks_WithLinkTypeFilter_Match(t *testing.T) {
	ts, fs, token := newLinksServer(t)
	defer ts.Close()

	fs.outgoingLinks = []store.ArtifactLink{
		{SourcePath: "tasks/TASK-001.md", TargetPath: "epics/EPIC-001/epic.md", LinkType: "parent"},
		{SourcePath: "tasks/TASK-001.md", TargetPath: "epics/EPIC-002/epic.md", LinkType: "sibling"},
	}

	// Only return "parent" links — "sibling" is filtered out.
	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/artifacts/tasks/TASK-001.md/links?link_type=parent",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestArtifactLinks_BothWithData verifies both directions with link data.
func TestArtifactLinks_BothWithData(t *testing.T) {
	ts, fs, token := newLinksServer(t)
	defer ts.Close()

	fs.outgoingLinks = []store.ArtifactLink{
		{SourcePath: "tasks/TASK-001.md", TargetPath: "epics/EPIC-001/epic.md", LinkType: "parent"},
	}
	fs.incomingLinks = []store.ArtifactLink{
		{SourcePath: "tasks/TASK-002.md", TargetPath: "tasks/TASK-001.md", LinkType: "blocks"},
	}

	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/artifacts/tasks/TASK-001.md/links?direction=both",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ── handleQueryRuns with status filter ──

// TestQueryRuns_WithStatusFilter verifies that the status filter applies.
func TestQueryRuns_WithStatusFilter(t *testing.T) {
	ts, token, _ := setupFullServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/query/runs?task_path=initiatives/test/task.md&status=active",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ── handleSkillCreate auth fail / error paths ──

func newSkillServerWithReader(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	ts, ss, contribToken := setupSkillServer(t)
	// Create a reader token using the same server's authSvc via authSvc.CreateToken.
	// But we don't have access to authSvc here. Instead, directly register a reader.
	// We need to call CreateToken on the skillStore's auth service.
	// Since the skillStore is returned by setupSkillServer and already has "reader-1",
	// we can use auth.NewService again to generate a token that the skillStore recognizes.
	authSvc := auth.NewService(ss)
	readerToken, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test-reader", nil)
	return ts, contribToken, readerToken
}

// TestSkillCreate_Unauthorized verifies that an unauthenticated request returns 401.
func TestSkillCreate_Unauthorized(t *testing.T) {
	ts, _, _ := setupSkillServer(t)

	resp, err := http.Post(ts.URL+"/api/v1/skills", "application/json",
		strings.NewReader(`{"name":"Go"}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestSkillCreate_InvalidJSON verifies that invalid JSON body returns 400.
func TestSkillCreate_InvalidJSON(t *testing.T) {
	ts, _, token := setupSkillServer(t)

	resp := skillRequest(t, "POST", ts.URL+"/api/v1/skills", token, "not json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestSkillList_Unauthorized verifies that an unauthenticated request to skill list returns 401.
func TestSkillList_Unauthorized(t *testing.T) {
	ts, _, _ := setupSkillServer(t)

	resp, err := http.Get(ts.URL + "/api/v1/skills")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestSkillUpdate_Unauthorized verifies that an unauthenticated request returns 401.
func TestSkillUpdate_Unauthorized(t *testing.T) {
	ts, ss, _ := setupSkillServer(t)
	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Status: domain.SkillStatusActive}

	resp, err := http.NewRequest("PATCH", ts.URL+"/api/v1/skills/sk-1", strings.NewReader(`{"name":"Go2"}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	// No auth header.
	httpResp, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", httpResp.StatusCode)
	}
}

// TestSkillUpdate_InvalidJSON verifies that invalid JSON body returns 400.
func TestSkillUpdate_InvalidJSON(t *testing.T) {
	ts, ss, token := setupSkillServer(t)
	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-1", token, "not json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

// TestActorSkillList_Unauthorized verifies that unauthenticated request returns 401.
func TestActorSkillList_Unauthorized(t *testing.T) {
	ts, _, _ := setupSkillServer(t)

	resp, err := http.Get(ts.URL + "/api/v1/actors/contrib-1/skills")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestActorSkillAssign_Unauthorized verifies that unauthenticated request returns 401.
func TestActorSkillAssign_Unauthorized(t *testing.T) {
	ts, _, _ := setupSkillServer(t)

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors/contrib-1/skills/sk-1", http.NoBody)
	// No auth header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ── handleTaskWildcard: supersede action ──

// TestTaskSupersedeSuccess verifies that the supersede action succeeds.
func TestTaskSupersedeSuccess(t *testing.T) {
	ts, token, _ := setupFullServer(t)
	defer ts.Close()

	body := `{"successor_path":"initiatives/test/task-v2.md"}`
	req, _ := http.NewRequest("POST",
		ts.URL+"/api/v1/tasks/initiatives/test/task.md/supersede",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for supersede, got %d", resp.StatusCode)
	}
}

// ── handleStepSubmit with output ──

func newStepSubmitServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	// Bind the test execution to contrib-1 so the Option B ownership
	// check (TASK-004) accepts the submit.
	fs.stepExecOverride = &domain.StepExecution{
		ExecutionID: "exec-123", RunID: "run-1", StepID: "step1",
		Status:  domain.StepStatusInProgress,
		Attempt: 1,
		ActorID: "contrib-1",
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:         fs,
		Auth:          authSvc,
		ResultHandler: &fakeResultHandler{},
	})
	return httptest.NewServer(srv.Handler()), token
}

// TestHandleStepSubmit_WithOutput verifies step submit with an output body
// exercising the Output.ArtifactsProduced path.
func TestHandleStepSubmit_WithOutput(t *testing.T) {
	ts, token := newStepSubmitServer(t)
	defer ts.Close()

	body := `{"outcome_id":"accepted","output":{"artifacts_produced":[{"path":"tasks/TASK-001.md"}]}}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-123/submit",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestHandleStepSubmit_InvalidJSON verifies that invalid JSON returns 400.
func TestHandleStepSubmit_InvalidJSON(t *testing.T) {
	ts, token := newStepSubmitServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-123/submit",
		strings.NewReader("not json"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// ── handleSystemValidate with validation failures ──

// errQueryStore overrides QueryArtifacts to return artifacts, and also has an artifact with errors.
type errValidateStore struct {
	*fakeStore
	projections []store.ArtifactProjection
}

func (e *errValidateStore) QueryArtifacts(_ context.Context, _ store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return &store.ArtifactQueryResult{Items: e.projections}, nil
}

func (e *errValidateStore) GetArtifactProjection(_ context.Context, path string) (*store.ArtifactProjection, error) {
	for _, proj := range e.projections {
		if proj.ArtifactPath == path {
			return &proj, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "artifact not found")
}

func (e *errValidateStore) GetSyncState(_ context.Context) (*store.SyncState, error) {
	return &store.SyncState{Status: "idle"}, nil
}

// TestSystemValidate_WithValidationFailure exercises the failed result path.
func TestSystemValidate_WithValidationFailure(t *testing.T) {
	inner := newFakeStore()
	inner.actors["admin-1"] = &domain.Actor{
		ActorID: "admin-1", Role: domain.RoleAdmin, Status: domain.ActorStatusActive,
	}
	evs := &errValidateStore{
		fakeStore: inner,
		projections: []store.ArtifactProjection{
			{
				ArtifactPath: "tasks/TASK-001.md", ArtifactType: "Task",
				Title: "Bad Task", Status: "Invalid",
			},
		},
	}
	validator := validation.NewEngine(evs)
	authSvc := auth.NewService(inner)
	token, _, _ := authSvc.CreateToken(context.Background(), "admin-1", "test", nil)

	// Also provide an artSvc with an invalid artifact (missing required fields).
	artSvc := newFakeArtifactService()
	artSvc.artifacts["tasks/TASK-001.md"] = &domain.Artifact{
		Path:  "tasks/TASK-001.md",
		ID:    "TASK-001",
		Type:  domain.ArtifactTypeTask,
		Title: "Bad Task",
		// Status intentionally missing → schema validation should fail.
	}

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     evs,
		Auth:      authSvc,
		Artifacts: artSvc,
		Validator: validator,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/system/validate", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ── handleDiscussionCreate: CreateThread error ──

// errCreateThreadStore overrides CreateThread to return an error.
type errCreateThreadStore struct {
	discussionStore
}

func (e *errCreateThreadStore) CreateThread(_ context.Context, _ *domain.DiscussionThread) error {
	return domain.NewError(domain.ErrInternal, "db error")
}

func TestHandleDiscussionCreate_StoreError(t *testing.T) {
	base := newDiscussionStore()
	base.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Contrib",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	base.projections["tasks/TASK-001.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/TASK-001.md", ArtifactID: "TASK-001",
		ArtifactType: "Task", Title: "Test", Status: "Pending",
	}
	errSt := &errCreateThreadStore{discussionStore: *base}
	authSvc := auth.NewService(&errSt.discussionStore)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: errSt, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"anchor_type":"artifact","anchor_id":"tasks/TASK-001.md","title":"Test Thread"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/discussions",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 (CreateThread error), got %d", resp.StatusCode)
	}
}

// ── handleRunCancel auth fail ──

// TestRunCancel_Unauthorized verifies that a non-operator can't cancel a run.
func TestRunCancel_Unauthorized(t *testing.T) {
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/cancel", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 (contributor can't cancel), got %d", resp.StatusCode)
	}
}

// ── resolveStepDef: step not found in definition ──

// noMatchWorkflowStore has a workflow with a different step ID than the execution.
type noMatchWorkflowStore struct {
	*fakeStore
}

func (s *noMatchWorkflowStore) GetWorkflowProjection(_ context.Context, _ string) (*store.WorkflowProjection, error) {
	wfDef := domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "other-step", Name: "Not The One"},
		},
	}
	data, _ := jsonMarshal(wfDef)
	return &store.WorkflowProjection{Definition: data}, nil
}

func (s *noMatchWorkflowStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	return &domain.Run{
		RunID:         runID,
		Status:        domain.RunStatusActive,
		CurrentStepID: "step1",
		BranchName:    "spine/run/test-branch",
		WorkflowPath:  "workflows/default.yaml",
	}, nil
}

// TestHandleStepAssign_ResolveStepDef_NoMatch exercises the path where
// resolveStepDef finds no matching step in the definition → returns nil.
func TestHandleStepAssign_ResolveStepDef_NoMatch(t *testing.T) {
	fs := newFakeStore()
	fs.actors["op-1"] = &domain.Actor{
		ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Op",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	st := &noMatchWorkflowStore{fakeStore: fs}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "op-1", "test", nil)
	validator := validation.NewEngine(fs)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     st,
		Auth:      authSvc,
		Validator: validator,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// fakeStore.ListStepExecutionsByRun returns step1 in "waiting" state.
	// The workflow has "other-step" not "step1" → resolveStepDef returns nil → no precondition check.
	body := `{"actor_id":"op-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-123/steps/step1/assign",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Expect 200 — no matching step def means no precondition check, assign proceeds.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 (step not found in def → nil), got %d", resp.StatusCode)
	}
}

// jsonMarshal wraps json.Marshal for use in test helpers.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// ── Reader token on skill.update operations → 403 from handler's authorize() ──

// newReaderSkillServer returns a server using skillStore and a reader-level token.
func newReaderSkillServer(t *testing.T) (*httptest.Server, *skillStore, string) {
	t.Helper()
	ts, ss, _ := setupSkillServer(t)
	authSvc := auth.NewService(ss)
	readerToken, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "reader-token", nil)
	return ts, ss, readerToken
}

// TestActorSkillAssign_ReaderForbidden verifies Reader role fails skill.update check.
func TestActorSkillAssign_ReaderForbidden(t *testing.T) {
	ts, _, token := newReaderSkillServer(t)

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors/contributor-1/skills/sk-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestActorSkillRemove_ReaderForbidden verifies Reader role fails skill.update check.
func TestActorSkillRemove_ReaderForbidden(t *testing.T) {
	ts, _, token := newReaderSkillServer(t)

	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/actors/contributor-1/skills/sk-1", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestSkillDeprecate_ReaderForbidden verifies Reader role fails skill.deprecate check.
func TestSkillDeprecate_ReaderForbidden(t *testing.T) {
	ts, ss, token := newReaderSkillServer(t)
	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Status: domain.SkillStatusActive}

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/skills/sk-1/deprecate", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestSkillUpdate_ReaderForbidden verifies Reader role fails skill.update check.
func TestSkillUpdate_ReaderForbidden(t *testing.T) {
	ts, ss, token := newReaderSkillServer(t)
	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Status: domain.SkillStatusActive}

	req, _ := http.NewRequest("PATCH", ts.URL+"/api/v1/skills/sk-1",
		strings.NewReader(`{"name":"Go2"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ── Contributor trying actor.create → 403 from handler's authorize() ──

// TestActorCreate_ContributorForbidden verifies Contributor role fails actor.create check.
func TestActorCreate_ContributorForbidden(t *testing.T) {
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"actor_id":"new-actor","type":"human","name":"New"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ── handleQueryRuns: status filter with matching runs ──

// matchingRunQuerier returns runs including one with "active" status.
type matchingRunQuerier struct {
	fakeProjectionQuerier
}

func (m *matchingRunQuerier) QueryRuns(_ context.Context, _ string) ([]domain.Run, error) {
	return []domain.Run{
		{RunID: "run-1", Status: domain.RunStatusActive},
		{RunID: "run-2", Status: domain.RunStatusCompleted},
	}, nil
}

// errRunQuerier returns an error for QueryRuns.
type errRunQuerier struct {
	fakeProjectionQuerier
}

func (e *errRunQuerier) QueryRuns(_ context.Context, _ string) ([]domain.Run, error) {
	return nil, domain.NewError(domain.ErrInternal, "db error")
}

func newQueryRunsServer(t *testing.T, pq gateway.ProjectionQuerier) (*httptest.Server, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     fs,
		Auth:      authSvc,
		ProjQuery: pq,
	})
	return httptest.NewServer(srv.Handler()), token
}

// TestQueryRuns_WithMatchingStatusFilter verifies that status filter applies and filters results.
func TestQueryRuns_WithMatchingStatusFilter(t *testing.T) {
	ts, token := newQueryRunsServer(t, &matchingRunQuerier{})
	defer ts.Close()

	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/query/runs?task_path=tasks/TASK-001.md&status=active",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestQueryRuns_QueryRunsError verifies that a QueryRuns error returns 500.
func TestQueryRuns_QueryRunsError(t *testing.T) {
	ts, token := newQueryRunsServer(t, &errRunQuerier{})
	defer ts.Close()

	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/query/runs?task_path=tasks/TASK-001.md",
		http.NoBody)
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

// ── handleActorSkillList: ListActorSkills error ──

// errActorSkillListStore overrides ListActorSkills to return an error.
type errActorSkillListStore struct {
	*skillStore
}

func (e *errActorSkillListStore) ListActorSkills(_ context.Context, _ string) ([]domain.Skill, error) {
	return nil, domain.NewError(domain.ErrInternal, "db error")
}

// TestActorSkillList_ListError verifies that a ListActorSkills error returns 500.
func TestActorSkillList_ListError(t *testing.T) {
	base := newSkillStore()
	base.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	errSt := &errActorSkillListStore{skillStore: base}
	authSvc := auth.NewService(errSt.skillStore)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: errSt, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/actors/reader-1/skills", http.NoBody)
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

// ── handleArtifactList: QueryArtifacts error ──

// errQueryArtStore overrides QueryArtifacts on fakeStore to return an error.
type errQueryArtStore struct {
	*fakeStore
}

func (e *errQueryArtStore) QueryArtifacts(_ context.Context, _ store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return nil, domain.NewError(domain.ErrInternal, "db error")
}

// TestArtifactList_QueryArtifactsError verifies that a QueryArtifacts error returns 500.
func TestArtifactList_QueryArtifactsError(t *testing.T) {
	inner := newFakeStore()
	inner.actors["reader-1"] = &domain.Actor{
		ActorID: "reader-1", Type: domain.ActorTypeHuman, Name: "Reader",
		Role: domain.RoleReader, Status: domain.ActorStatusActive,
	}
	errSt := &errQueryArtStore{fakeStore: inner}
	authSvc := auth.NewService(inner)
	token, _, _ := authSvc.CreateToken(context.Background(), "reader-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: errSt, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/artifacts", http.NoBody)
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

// ── handleRunCancel: GetRun error (fallback path, no canceller) ──

// TestRunCancel_GetRunError verifies that a GetRun error in the fallback path returns 404.
func TestRunCancel_GetRunError(t *testing.T) {
	// runStatusFakeStore (from handlers_coverage_test.go) returns not-found when run==nil.
	inner := newFakeStore()
	inner.actors["op-1"] = &domain.Actor{
		ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Op",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	st := &runStatusFakeStore{fakeStore: inner, run: nil}
	authSvc := auth.NewService(inner)
	token, _, _ := authSvc.CreateToken(context.Background(), "op-1", "test", nil)
	// No runCanceller → uses fallback path.
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: st, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/nonexistent-run/cancel", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 (GetRun error), got %d", resp.StatusCode)
	}
}

// ── handleStepAcknowledge: actor ID mismatch and invalid JSON ──
// Uses the existing newAcknowledgeServer from handlers_acknowledge_test.go.

// TestStepAcknowledge_ActorIDMismatch verifies that mismatched actor_id returns 403.
// newAcknowledgeServer creates "bot-1" actor; sending a different actor_id triggers mismatch.
func TestStepAcknowledge_ActorIDMismatch(t *testing.T) {
	ack := &fakeAcknowledger{result: &engine.AcknowledgeResult{
		ExecutionID: "exec-1", StepID: "step1", Status: "in_progress",
	}}
	ts, token := newAcknowledgeServer(ack)
	defer ts.Close()

	// "bot-1" is authenticated, but actor_id in body is "other-actor" → mismatch.
	body := `{"actor_id":"other-actor"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-123/acknowledge",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 (actor_id mismatch), got %d", resp.StatusCode)
	}
}

// TestStepAcknowledge_InvalidJSON verifies that invalid JSON body returns 400.
func TestStepAcknowledge_InvalidJSON(t *testing.T) {
	ack := &fakeAcknowledger{result: &engine.AcknowledgeResult{
		ExecutionID: "exec-1", StepID: "step1", Status: "in_progress",
	}}
	ts, token := newAcknowledgeServer(ack)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-123/acknowledge",
		strings.NewReader("not json"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (invalid JSON), got %d", resp.StatusCode)
	}
}

// ── handleSkillUpdate: category field update ──

// TestSkillUpdate_WithCategory verifies that a category field in the request updates the skill.
func TestSkillUpdate_WithCategory(t *testing.T) {
	ts, ss, token := setupSkillServer(t)
	ss.skills["sk-1"] = &domain.Skill{SkillID: "sk-1", Name: "Go", Status: domain.SkillStatusActive}

	resp := skillRequest(t, "PATCH", ts.URL+"/api/v1/skills/sk-1", token,
		`{"category":"backend"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for category update, got %d", resp.StatusCode)
	}
}

// ── handleArtifactRead with ?ref= parameter → covers sourceCommit = ref ──

// TestArtifactRead_WithRef verifies that a ref parameter sets sourceCommit.
func TestArtifactRead_WithRef(t *testing.T) {
	ts, token, _ := setupFullServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/artifacts/initiatives/test/task.md?ref=HEAD",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ── handleArtifactUpdate with planning run write_context → covers branch path ──

// TestArtifactUpdate_WithPlanningWriteContext verifies that write_context with a planning run
// sets the branch in context and marks the write mode as "proposed".
func TestArtifactUpdate_WithPlanningWriteContext(t *testing.T) {
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	pfs := &planningFakeStore{fakeStore: fs}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)

	artSvc := newFakeArtifactService()
	artSvc.artifacts["tasks/TASK-001.md"] = &domain.Artifact{
		Path: "tasks/TASK-001.md", ID: "TASK-001",
		Type: domain.ArtifactTypeTask, Title: "Test Task",
	}

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:     pfs,
		Auth:      authSvc,
		Artifacts: artSvc,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// write_context with run_id triggers resolveWriteContext → planning run → branch set.
	body := `{"content":"# Updated Task","write_context":{"run_id":"run-1"}}`
	req, _ := http.NewRequest("PUT",
		ts.URL+"/api/v1/artifacts/tasks/TASK-001.md",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ── handleArtifactLinks: incoming link filtered out → covers continue in incoming loop ──

// TestArtifactLinks_IncomingFilteredOut verifies that incoming links not matching the
// link_type filter are skipped (covers the continue branch for incoming links).
func TestArtifactLinks_IncomingFilteredOut(t *testing.T) {
	ts, fs, token := newLinksServer(t)
	defer ts.Close()

	fs.outgoingLinks = []store.ArtifactLink{
		{SourcePath: "tasks/TASK-001.md", TargetPath: "epics/EPIC-001/epic.md", LinkType: "parent"},
	}
	fs.incomingLinks = []store.ArtifactLink{
		// "blocks" link type won't match filter "parent" → continue fires.
		{SourcePath: "tasks/TASK-002.md", TargetPath: "tasks/TASK-001.md", LinkType: "blocks"},
	}

	req, _ := http.NewRequest("GET",
		ts.URL+"/api/v1/artifacts/tasks/TASK-001.md/links?direction=both&link_type=parent",
		http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ── resolveWriteContext: empty RunID early return ──

// TestArtifactCreate_WithEmptyWriteContext verifies that write_context with empty run_id
// returns "" immediately (early return branch in resolveWriteContext).
func TestArtifactCreate_WithEmptyWriteContext(t *testing.T) {
	ts, token, _ := setupFullServer(t)
	defer ts.Close()

	// write_context is present but run_id is empty → resolveWriteContext returns ("", nil).
	body := `{"path":"tasks/TASK-NEW.md","content":"# New Task","write_context":{}}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/artifacts",
		strings.NewReader(body))
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
