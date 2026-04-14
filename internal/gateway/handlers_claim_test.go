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
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/gateway"
)

type fakeStepClaimer struct {
	result *engine.ClaimResult
	err    error
}

func (f *fakeStepClaimer) ClaimStep(_ context.Context, _ engine.ClaimRequest) (*engine.ClaimResult, error) {
	return f.result, f.err
}

func newClaimServer(claimer gateway.StepClaimer) (*httptest.Server, string) {
	fs := newFakeStore()
	fs.actors["bot-1"] = &domain.Actor{
		ActorID: "bot-1", Type: domain.ActorTypeAutomated, Name: "Bot",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "bot-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:       fs,
		Auth:        authSvc,
		StepClaimer: claimer,
	})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleExecutionClaim_Success(t *testing.T) {
	active := domain.AssignmentStatusActive
	claimer := &fakeStepClaimer{
		result: &engine.ClaimResult{
			Assignment: &domain.Assignment{AssignmentID: "assign-1", ActorID: "bot-1", Status: active},
			RunID:      "run-1",
			StepID:     "step-1",
		},
	}
	ts, token := newClaimServer(claimer)
	defer ts.Close()

	body := `{"actor_id":"bot-1","execution_id":"exec-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/claim", strings.NewReader(body))
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
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["assignment_id"] != "assign-1" {
		t.Errorf("expected assignment_id assign-1, got %v", out["assignment_id"])
	}
	if out["status"] != "claimed" {
		t.Errorf("expected status claimed, got %v", out["status"])
	}
}

func TestHandleExecutionClaim_Unavailable(t *testing.T) {
	fs := newFakeStore()
	fs.actors["bot-1"] = &domain.Actor{
		ActorID: "bot-1", Type: domain.ActorTypeAutomated, Name: "Bot",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "bot-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"actor_id":"bot-1","execution_id":"exec-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/claim", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
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

func TestHandleExecutionClaim_Forbidden_Impersonation(t *testing.T) {
	claimer := &fakeStepClaimer{}
	ts, token := newClaimServer(claimer)
	defer ts.Close()

	body := `{"actor_id":"other-bot","execution_id":"exec-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/claim", strings.NewReader(body))
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

func TestHandleExecutionClaim_ServiceError(t *testing.T) {
	claimer := &fakeStepClaimer{err: domain.NewError(domain.ErrConflict, "already claimed")}
	ts, token := newClaimServer(claimer)
	defer ts.Close()

	body := `{"actor_id":"bot-1","execution_id":"exec-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/claim", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}
