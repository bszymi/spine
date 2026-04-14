package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/gateway"
)

type fakeStepReleaser struct {
	err error
}

func (f *fakeStepReleaser) ReleaseStep(_ context.Context, _ engine.ReleaseRequest) error {
	return f.err
}

func newReleaseServer(releaser gateway.StepReleaser) (*httptest.Server, string) {
	fs := newFakeStore()
	fs.actors["bot-1"] = &domain.Actor{
		ActorID: "bot-1", Type: domain.ActorTypeAutomated, Name: "Bot",
		Role: domain.RoleContributor, Status: domain.ActorStatusActive,
	}
	authSvc := auth.NewService(fs)
	token, _, _ := authSvc.CreateToken(context.Background(), "bot-1", "test", nil)
	srv := gateway.NewServer(":0", gateway.ServerConfig{
		Store:        fs,
		Auth:         authSvc,
		StepReleaser: releaser,
	})
	return httptest.NewServer(srv.Handler()), token
}

func TestHandleExecutionRelease_Success(t *testing.T) {
	ts, token := newReleaseServer(&fakeStepReleaser{})
	defer ts.Close()

	body := `{"actor_id":"bot-1","assignment_id":"assign-1","reason":"retry"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/release", strings.NewReader(body))
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

func TestHandleExecutionRelease_Unavailable(t *testing.T) {
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

	body := `{"actor_id":"bot-1","assignment_id":"assign-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/release", strings.NewReader(body))
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

func TestHandleExecutionRelease_Forbidden_Impersonation(t *testing.T) {
	ts, token := newReleaseServer(&fakeStepReleaser{})
	defer ts.Close()

	// actor_id doesn't match authenticated caller (bot-1)
	body := `{"actor_id":"other-bot","assignment_id":"assign-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/release", strings.NewReader(body))
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

func TestHandleExecutionRelease_ServiceError(t *testing.T) {
	releaser := &fakeStepReleaser{err: domain.NewError(domain.ErrNotFound, "assignment not found")}
	ts, token := newReleaseServer(releaser)
	defer ts.Close()

	body := `{"actor_id":"bot-1","assignment_id":"assign-1"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/execution/release", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
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
