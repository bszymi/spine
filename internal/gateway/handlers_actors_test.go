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
)

func newActorCreateServer() (*httptest.Server, string) {
	fs := newFakeStore()
	fs.actors["admin-1"] = &domain.Actor{
		ActorID: "admin-1", Type: domain.ActorTypeHuman, Name: "Admin",
		Role: domain.RoleAdmin, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "admin-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{Store: fs, Auth: authSvc})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleActorCreate_Success(t *testing.T) {
	ts, token := newActorCreateServer()
	defer ts.Close()

	body := `{"type":"automated_system","name":"Runner Bot","role":"contributor"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors", strings.NewReader(body))
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
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["type"] != "automated_system" {
		t.Errorf("expected type automated_system, got %v", out["type"])
	}
}

func TestHandleActorCreate_WithExplicitID(t *testing.T) {
	ts, token := newActorCreateServer()
	defer ts.Close()

	body := `{"actor_id":"my-runner","type":"ai_agent","name":"AI Runner"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors", strings.NewReader(body))
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
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["actor_id"] != "my-runner" {
		t.Errorf("expected actor_id my-runner, got %v", out["actor_id"])
	}
}

func TestHandleActorCreate_MissingType(t *testing.T) {
	ts, token := newActorCreateServer()
	defer ts.Close()

	body := `{"name":"Runner Bot"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors", strings.NewReader(body))
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

func TestHandleActorCreate_InvalidType(t *testing.T) {
	ts, token := newActorCreateServer()
	defer ts.Close()

	body := `{"type":"robot","name":"Bot"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors", strings.NewReader(body))
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

func TestHandleActorCreate_InvalidRole(t *testing.T) {
	ts, token := newActorCreateServer()
	defer ts.Close()

	body := `{"type":"human","name":"Bot","role":"superuser"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors", strings.NewReader(body))
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

func TestHandleActorCreate_Unauthorized(t *testing.T) {
	ts, _ := newActorCreateServer()
	defer ts.Close()

	body := `{"type":"human","name":"Bot"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/actors", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}
