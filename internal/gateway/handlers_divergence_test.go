package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
)

type fakeBranchCreator struct {
	branch *domain.Branch
	err    error
}

func (f *fakeBranchCreator) CreateExploratoryBranch(_ context.Context, _ *domain.DivergenceContext, branchID, _ string) (*domain.Branch, error) {
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

func TestHandleCreateBranch_Success(t *testing.T) {
	bc := &fakeBranchCreator{branch: &domain.Branch{BranchID: "b-1", Status: "active", CurrentStepID: "step-1"}}
	ts, contribToken, _ := newDivergenceFullServer(t, bc)
	defer ts.Close()

	body := `{"branch_id":"b-1","start_step":"step-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs/run-1/divergences/div-1/branches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+contribToken)
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
