package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
)

type fakeBranchCreator struct {
	branch *domain.Branch
	err    error
	calls  int
}

func (f *fakeBranchCreator) CreateExploratoryBranch(_ context.Context, _ *domain.DivergenceContext, branchID, _ string) (*domain.Branch, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.branch != nil {
		return f.branch, nil
	}
	return &domain.Branch{BranchID: branchID, Status: "active", CurrentStepID: "step-1"}, nil
}

func (f *fakeBranchCreator) CloseWindow(_ context.Context, _ *domain.DivergenceContext) error {
	return f.err
}

// newDivergenceFullServer creates a server with both a fakeStore and fakeBranchCreator.
func newDivergenceFullServer(t *testing.T, bc gateway.BranchCreator) (*httptest.Server, string, string) {
	t.Helper()
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	fs.actors["op-1"] = &domain.Actor{
		ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Operator",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	// Populate a divergence context for use in tests.
	fs.divContexts = map[string]*domain.DivergenceContext{
		"div-1": {DivergenceID: "div-1", RunID: "run-1"},
	}
	authSvc := auth.NewService(fs)
	contribToken, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	opToken, _, _ := authSvc.CreateToken(context.Background(), "op-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:         fs,
		Auth:          authSvc,
		BranchCreator: bc,
	})
	return httptest.NewServer(srv.Handler()), contribToken, opToken
}

func newDivergenceServer() (*httptest.Server, string, string) {
	fs := newFakeStore()
	fs.actors["contrib-1"] = &domain.Actor{
		ActorID: "contrib-1", Type: domain.ActorTypeHuman, Name: "Dev",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	fs.actors["op-1"] = &domain.Actor{
		ActorID: "op-1", Type: domain.ActorTypeHuman, Name: "Operator",
		Role: domain.RoleOperator, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	contribToken, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	opToken, _, _ := authSvc.CreateToken(context.Background(), "op-1", "test", nil)
	// No BranchCreator configured → 503 for divergence routes.
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), contribToken, opToken
}

func TestHandleCreateBranch_Unavailable(t *testing.T) {
	ts, contribToken, _ := newDivergenceServer()
	defer ts.Close()

	body := `{"branch_id":"branch-1","start_step":"execute"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/div-1/branches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+contribToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestHandleCloseWindow_Unavailable(t *testing.T) {
	ts, _, opToken := newDivergenceServer()
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/div-1/close-window", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+opToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestHandleCreateBranch_MissingFields(t *testing.T) {
	bc := &fakeBranchCreator{}
	ts, contribToken, _ := newDivergenceFullServer(t, bc)
	defer ts.Close()

	// Missing start_step → 400
	body := `{"branch_id":"branch-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/div-1/branches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+contribToken)
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

// TestHandleCreateBranch_Success creates a branch as an operator, who
// needs no binding to the run (INIT-023 TASK-016 owner question 6).
// Before TASK-016 this test used an unbound contributor; that caller is
// now refused (TestHandleCreateBranch_UnboundContributorForbidden).
func TestHandleCreateBranch_Success(t *testing.T) {
	bc := &fakeBranchCreator{branch: &domain.Branch{BranchID: "b-1", Status: "active", CurrentStepID: "step-1"}}
	ts, _, opToken := newDivergenceFullServer(t, bc)
	defer ts.Close()

	body := `{"branch_id":"b-1","start_step":"step-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/div-1/branches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opToken)
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

func TestHandleCreateBranch_DivergenceNotFound(t *testing.T) {
	bc := &fakeBranchCreator{}
	ts, contribToken, _ := newDivergenceFullServer(t, bc)
	defer ts.Close()

	body := `{"branch_id":"b-1","start_step":"step-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/nonexistent/branches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+contribToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleCloseWindow_Success(t *testing.T) {
	bc := &fakeBranchCreator{}
	ts, _, opToken := newDivergenceFullServer(t, bc)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/div-1/close-window", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+opToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleCloseWindow_ServiceError(t *testing.T) {
	bc := &fakeBranchCreator{err: domain.NewError(domain.ErrInternal, "branch system failure")}
	ts, _, opToken := newDivergenceFullServer(t, bc)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/div-1/close-window", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+opToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

// explorationStore is the gateway fakeStore in the state the engine
// really produces while an exploratory divergence's window is open
// (core/engine startDivergence): the run cursor is "divergence:div-1",
// and the latest run-level execution is the completed step1 whose
// outcome triggered the divergence, submitted by trigger. contrib-2
// completed the earlier step0. No execution exists for any branch: the
// engine creates none for an exploratory branch, so nobody holds a
// live cursor of the run (TASK-016 review round 1).
type explorationStore struct {
	*fakeStore
	trigger string
}

func (e *explorationStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	return &domain.Run{RunID: runID, Status: domain.RunStatusActive, CurrentStepID: "divergence:div-1", BranchName: "spine/run/test-branch"}, nil
}

func (e *explorationStore) ListStepExecutionsByRun(_ context.Context, runID string) ([]domain.StepExecution, error) {
	return []domain.StepExecution{
		{ExecutionID: "exec-0", RunID: runID, StepID: "step0", ActorID: "contrib-2", Status: domain.StepStatusCompleted, Attempt: 1},
		{ExecutionID: "exec-1", RunID: runID, StepID: "step1", ActorID: e.trigger, Status: domain.StepStatusCompleted, Attempt: 1},
	}, nil
}

// createBranchAs posts a create-branch request with the given body and
// bearer and returns the status code.
func createBranchAs(t *testing.T, url, token, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/api/v1/runs/run-1/divergences/div-1/branches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// newDivergenceBindingServer is newDivergenceFullServer over an
// explorationStore whose divergence trigger is trigger.
func newDivergenceBindingServer(t *testing.T, bc gateway.BranchCreator, trigger string) (*httptest.Server, string, string) {
	t.Helper()
	fs := newFakeStore()
	for id, role := range map[string]domain.ActorRole{"contrib-1": domain.RoleContributor, "contrib-2": domain.RoleContributor, "op-1": domain.RoleOperator} {
		fs.actors[id] = &domain.Actor{ActorID: id, Type: domain.ActorTypeHuman, Name: id, Role: role, Status: domain.ActorStatusActive}
	}
	fs.divContexts = map[string]*domain.DivergenceContext{"div-1": {DivergenceID: "div-1", RunID: "run-1"}}
	st := &explorationStore{fakeStore: fs, trigger: trigger}
	authSvc := auth.NewService(st)
	c1, _, _ := authSvc.CreateToken(context.Background(), "contrib-1", "test", nil)
	c2, _, _ := authSvc.CreateToken(context.Background(), "contrib-2", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: st, Auth: authSvc, BranchCreator: bc})
	return httptest.NewServer(srv.Handler()), c1, c2
}

// TestHandleCreateBranch_UnboundContributorForbidden: holding the
// divergence.create_branch permission is no longer enough to attach a
// branch to someone else's run (INIT-023 TASK-016 owner question 6).
func TestHandleCreateBranch_UnboundContributorForbidden(t *testing.T) {
	bc := &fakeBranchCreator{}
	ts, contribToken, _ := newDivergenceFullServer(t, bc)
	defer ts.Close()
	if code := createBranchAs(t, ts.URL, contribToken, `{"branch_id":"b-1","start_step":"step-1"}`); code != http.StatusForbidden {
		t.Errorf("unbound contributor: status %d, want 403", code)
	}
	if bc.calls != 0 {
		t.Errorf("branch creator called %d times, want 0", bc.calls)
	}
}

// TestHandleCreateBranch_DivergenceTriggerHolderAllowed: in the state
// the engine produces for an open exploratory divergence, the actor who
// submitted the triggering step may add a branch; a contributor who
// completed an earlier step of the same run may not. (Before review
// round 1 this test modelled a live run-level execution of the current
// step beside the divergence, which the engine never produces, so no
// contributor could in fact pass the binding.)
func TestHandleCreateBranch_DivergenceTriggerHolderAllowed(t *testing.T) {
	bc := &fakeBranchCreator{}
	ts, triggerToken, otherToken := newDivergenceBindingServer(t, bc, "contrib-1")
	defer ts.Close()
	if code := createBranchAs(t, ts.URL, triggerToken, `{"branch_id":"b-1","start_step":"step-1"}`); code != http.StatusCreated {
		t.Errorf("divergence trigger holder: status %d, want 201", code)
	}
	if code := createBranchAs(t, ts.URL, otherToken, `{"branch_id":"b-2","start_step":"step-1"}`); code != http.StatusForbidden {
		t.Errorf("contributor who completed an earlier step: status %d, want 403", code)
	}
	if bc.calls != 1 {
		t.Errorf("branch creator called %d times, want 1", bc.calls)
	}
}

// TestHandleCreateBranch_InvalidBranchIDRejected: branch_id becomes a
// git ref component, so it must match the workflow ID grammar. A bad
// one is a 400 for every caller (the operator here), before any lookup
// and before the branch-protection guard could turn a control byte
// into a 500 (INIT-023 TASK-001 cross-task review).
func TestHandleCreateBranch_InvalidBranchIDRejected(t *testing.T) {
	bc := &fakeBranchCreator{}
	ts, _, opToken := newDivergenceFullServer(t, bc)
	defer ts.Close()
	for _, id := range []string{
		`a\u0000`, `a\u0001`, `a\nb`, `a\u007f`, "A", "a/b", "a b", "-a", ".a", "a..b", "a.", "a.lock",
		"a~1", "a:b", "a^", "é", strings.Repeat("a", 129),
	} {
		body := `{"branch_id":"` + id + `","start_step":"step-1"}`
		if code := createBranchAs(t, ts.URL, opToken, body); code != http.StatusBadRequest {
			t.Errorf("branch_id %q: status %d, want 400", id, code)
		}
	}
	if bc.calls != 0 {
		t.Errorf("branch creator called %d times, want 0", bc.calls)
	}
	if code := createBranchAs(t, ts.URL, opToken, `{"branch_id":"`+strings.Repeat("a", 128)+`","start_step":"step-1"}`); code != http.StatusCreated {
		t.Errorf("128-byte branch_id: status %d, want 201", code)
	}
}
