package gateway_test

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
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/gateway"
)

// ── Fake StepAcknowledger ──

type fakeAcknowledger struct {
	result *engine.AcknowledgeResult
	err    error
}

func (f *fakeAcknowledger) AcknowledgeStep(_ context.Context, _ engine.AcknowledgeRequest) (*engine.AcknowledgeResult, error) {
	return f.result, f.err
}

func newAcknowledgeServer(ack gateway.StepAcknowledger) (*httptest.Server, string) {
	fs := newFakeStore()
	fs.actors["bot-1"] = &domain.Actor{
		ActorID: "bot-1", Type: domain.ActorTypeAutomated, Name: "Bot",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "bot-1", "test", nil)

	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:            fs,
		Auth:             authSvc,
		StepAcknowledger: ack,
	})
	ts := httptest.NewServer(srv.Handler())
	return ts, token
}

func TestHandleStepAcknowledge_Success(t *testing.T) {
	now := time.Now()
	ack := &fakeAcknowledger{
		result: &engine.AcknowledgeResult{
			ExecutionID: "exec-1",
			StepID:      "execute",
			Status:      "in_progress",
			StartedAt:   &now,
		},
	}
	ts, token := newAcknowledgeServer(ack)
	defer ts.Close()

	body := `{"actor_id":"bot-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-1/acknowledge", strings.NewReader(body))
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
	var body2 map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body2); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body2["status"] != "in_progress" {
		t.Errorf("expected status in_progress, got %v", body2["status"])
	}
}

func TestHandleStepAcknowledge_Conflict(t *testing.T) {
	ack := &fakeAcknowledger{
		err: domain.NewError(domain.ErrConflict, "step is not in assigned state"),
	}
	ts, token := newAcknowledgeServer(ack)
	defer ts.Close()

	body := `{"actor_id":"bot-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-1/acknowledge", strings.NewReader(body))
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

func TestHandleStepAcknowledge_Forbidden(t *testing.T) {
	ack := &fakeAcknowledger{
		err: domain.NewError(domain.ErrForbidden, "actor is not assigned to this step"),
	}
	ts, token := newAcknowledgeServer(ack)
	defer ts.Close()

	body := `{"actor_id":"bot-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-1/acknowledge", strings.NewReader(body))
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

func TestHandleStepAcknowledge_Unavailable(t *testing.T) {
	// No StepAcknowledger configured.
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

	body := `{"actor_id":"bot-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/steps/exec-1/acknowledge", strings.NewReader(body))
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
