package gateway_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/gateway"
)

const testOperatorToken = "test-operator-token-32-chars-long-xx"

// newOperatorServer creates a server with the operator token set.
// Must call t.Setenv before creating the server so the middleware captures it.
func newOperatorServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("SPINE_OPERATOR_TOKEN", testOperatorToken)
	srv := gateway.NewServer(":0", gateway.ServerConfig{})
	return httptest.NewServer(srv.Handler())
}

func operatorReq(method, url, token string, body string) (*http.Request, error) {
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if bodyReader != nil {
		req, err = http.NewRequest(method, url, bodyReader)
	} else {
		req, err = http.NewRequest(method, url, http.NoBody)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func TestHandleWorkspaceCreate_NoProvider(t *testing.T) {
	ts := newOperatorServer(t)
	defer ts.Close()

	req, _ := operatorReq("POST", ts.URL+"/api/v1/workspaces", testOperatorToken, `{"workspace_id":"ws-1"}`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no provider), got %d", resp.StatusCode)
	}
}

func TestHandleWorkspaceList_NoProvider(t *testing.T) {
	ts := newOperatorServer(t)
	defer ts.Close()

	req, _ := operatorReq("GET", ts.URL+"/api/v1/workspaces", testOperatorToken, "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no provider), got %d", resp.StatusCode)
	}
}

func TestHandleWorkspaceGet_NoProvider(t *testing.T) {
	ts := newOperatorServer(t)
	defer ts.Close()

	req, _ := operatorReq("GET", ts.URL+"/api/v1/workspaces/ws-1", testOperatorToken, "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no provider), got %d", resp.StatusCode)
	}
}

func TestHandleWorkspaceDeactivate_NoProvider(t *testing.T) {
	ts := newOperatorServer(t)
	defer ts.Close()

	req, _ := operatorReq("POST", ts.URL+"/api/v1/workspaces/ws-1/deactivate", testOperatorToken, "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no provider), got %d", resp.StatusCode)
	}
}

func TestHandleWorkspace_BadToken(t *testing.T) {
	ts := newOperatorServer(t)
	defer ts.Close()

	req, _ := operatorReq("GET", ts.URL+"/api/v1/workspaces", "wrong-token", "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 (bad token), got %d", resp.StatusCode)
	}
}

func TestHandleWorkspaceCreate_MissingWorkspaceID(t *testing.T) {
	ts := newOperatorServer(t)
	defer ts.Close()

	// No wsDBProvider, so this still returns 503 from the nil check.
	req, _ := operatorReq("POST", ts.URL+"/api/v1/workspaces", testOperatorToken, `{}`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Provider is nil → 503 (provider check happens before workspace_id check).
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}
