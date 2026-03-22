package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/store"
)

// ── Fake Store ──

type fakeStore struct {
	store.Store
	pingErr error
}

func (f *fakeStore) Ping(_ context.Context) error { return f.pingErr }

// ── Response Tests ──

func TestHTTPStatusForErrorCodes(t *testing.T) {
	tests := []struct {
		code   domain.ErrorCode
		expect int
	}{
		{domain.ErrNotFound, 404},
		{domain.ErrAlreadyExists, 409},
		{domain.ErrValidationFailed, 422},
		{domain.ErrUnauthorized, 401},
		{domain.ErrForbidden, 403},
		{domain.ErrConflict, 409},
		{domain.ErrPrecondition, 412},
		{domain.ErrInvalidParams, 400},
		{domain.ErrInternal, 500},
		{domain.ErrUnavailable, 503},
		{domain.ErrGit, 500},
		{domain.ErrWorkflowNotFound, 404},
	}

	// Use a test server to exercise WriteError
	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			w := httptest.NewRecorder()
			gateway.WriteError(w, domain.NewError(tt.code, "test"))
			if w.Code != tt.expect {
				t.Errorf("code %s: expected %d, got %d", tt.code, tt.expect, w.Code)
			}
			// Verify error envelope
			var resp gateway.ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if resp.Status != "error" {
				t.Errorf("expected status=error, got %s", resp.Status)
			}
			if len(resp.Errors) != 1 || resp.Errors[0].Code != string(tt.code) {
				t.Errorf("expected error code %s in envelope", tt.code)
			}
		})
	}
}

func TestWriteErrorPlainError(t *testing.T) {
	w := httptest.NewRecorder()
	gateway.WriteError(w, fmt.Errorf("something broke"))
	if w.Code != 500 {
		t.Errorf("expected 500, got %d", w.Code)
	}
	var resp gateway.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Errors[0].Code != "internal_error" {
		t.Errorf("expected internal_error, got %s", resp.Errors[0].Code)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	gateway.WriteJSON(w, 200, map[string]string{"hello": "world"})
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}

func TestWriteNotImplemented(t *testing.T) {
	w := httptest.NewRecorder()
	gateway.WriteNotImplemented(w)
	if w.Code != 501 {
		t.Errorf("expected 501, got %d", w.Code)
	}
	var resp gateway.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Errors[0].Code != "not_implemented" {
		t.Errorf("expected not_implemented, got %s", resp.Errors[0].Code)
	}
}

// ── Middleware Tests ──

func TestTraceIDGenerated(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/system/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	traceID := resp.Header.Get("X-Trace-Id")
	if traceID == "" {
		t.Error("expected X-Trace-Id header to be set")
	}
}

func TestTraceIDPassthrough(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/system/health", http.NoBody)
	req.Header.Set("X-Trace-Id", "my-trace-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("X-Trace-Id") != "my-trace-123" {
		t.Errorf("expected my-trace-123, got %s", resp.Header.Get("X-Trace-Id"))
	}
}

// ── Health Endpoint Tests ──

func TestHealthWithStore(t *testing.T) {
	srv := gateway.NewServer(":0", &fakeStore{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/system/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "healthy" {
		t.Errorf("expected healthy, got %v", body["status"])
	}
}

func TestHealthWithoutStore(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/system/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("expected degraded, got %v", body["status"])
	}
}

func TestHealthWithUnhealthyStore(t *testing.T) {
	srv := gateway.NewServer(":0", &fakeStore{pingErr: fmt.Errorf("db down")})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/system/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("expected degraded, got %v", body["status"])
	}
}

// ── Stub Endpoint Tests ──

func TestStubEndpointsReturn501(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	stubs := []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/system/rebuild"},
		{"GET", "/api/v1/system/rebuild/rb-123"},
		{"POST", "/api/v1/system/validate"},
		{"POST", "/api/v1/artifacts"},
		{"GET", "/api/v1/artifacts"},
		{"GET", "/api/v1/artifacts/initiatives/INIT-001/task.md"},
		{"PUT", "/api/v1/artifacts/initiatives/INIT-001/task.md"},
		{"POST", "/api/v1/artifacts/initiatives/INIT-001/task.md/validate"},
		{"GET", "/api/v1/artifacts/initiatives/INIT-001/task.md/links"},
		{"POST", "/api/v1/runs"},
		{"GET", "/api/v1/runs/run-123"},
		{"POST", "/api/v1/runs/run-123/cancel"},
		{"POST", "/api/v1/runs/run-123/steps/step-1/assign"},
		{"POST", "/api/v1/steps/assign-123/submit"},
		{"POST", "/api/v1/tasks/initiatives/INIT-001/task.md/accept"},
		{"POST", "/api/v1/tasks/initiatives/INIT-001/task.md/reject"},
		{"POST", "/api/v1/tasks/initiatives/INIT-001/task.md/cancel"},
		{"POST", "/api/v1/tasks/initiatives/INIT-001/task.md/abandon"},
		{"POST", "/api/v1/tasks/initiatives/INIT-001/task.md/supersede"},
		{"GET", "/api/v1/query/artifacts"},
		{"GET", "/api/v1/query/graph"},
		{"GET", "/api/v1/query/history"},
		{"GET", "/api/v1/query/runs"},
	}

	for _, tt := range stubs {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, _ := http.NewRequest(tt.method, ts.URL+tt.path, http.NoBody)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 501 {
				t.Errorf("expected 501, got %d", resp.StatusCode)
			}
			var errResp gateway.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if errResp.Status != "error" {
				t.Errorf("expected status=error, got %s", errResp.Status)
			}
		})
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/nonexistent")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 && resp.StatusCode != 405 {
		t.Errorf("expected 404 or 405, got %d", resp.StatusCode)
	}
}

func TestArtifactWildcardInvalidMethod(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/artifacts/some/path.md", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestTaskWildcardInvalidAction(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/tasks/some/path.md/invalid", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestTaskWildcardInvalidMethod(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/tasks/some/path.md/accept", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ── Response Content-Type ──

func TestResponseContentType(t *testing.T) {
	srv := gateway.NewServer(":0", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/system/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}
