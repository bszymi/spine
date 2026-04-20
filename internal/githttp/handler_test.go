package githttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsTrustedIP(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		TrustedCIDRs:    []string{"172.16.0.0/12", "10.0.0.0/8"},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"docker internal", "172.18.0.3:12345", true},
		{"private 10.x", "10.0.0.5:80", true},
		{"external", "203.0.113.1:80", false},
		{"localhost", "127.0.0.1:80", false},
		{"no port", "172.18.0.3", true},
		{"invalid", "not-an-ip", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.IsTrustedIP(tt.remoteAddr)
			if got != tt.want {
				t.Errorf("IsTrustedIP(%q) = %v, want %v", tt.remoteAddr, got, tt.want)
			}
		})
	}
}

func TestIsTrustedIP_NoCIDRs(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if h.IsTrustedIP("172.18.0.3:80") {
		t.Error("expected no IPs to be trusted when TrustedCIDRs is empty")
	}
}

func TestNewHandler_InvalidCIDR(t *testing.T) {
	_, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		TrustedCIDRs:    []string{"not-a-cidr"},
	})
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestNewHandler_Defaults(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if cap(h.sem) != 5 {
		t.Errorf("expected default MaxConcurrent=5, got %d", cap(h.sem))
	}
	if h.opTimeout != 30*time.Second {
		t.Errorf("expected default OpTimeout=30s, got %v", h.opTimeout)
	}
}

func TestNewHandler_CustomConfig(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		MaxConcurrent:   10,
		OpTimeout:       60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if cap(h.sem) != 10 {
		t.Errorf("expected MaxConcurrent=10, got %d", cap(h.sem))
	}
	if h.opTimeout != 60*time.Second {
		t.Errorf("expected OpTimeout=60s, got %v", h.opTimeout)
	}
}

func TestIsReadOnly(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		query  string
		want   bool
	}{
		{"info refs upload-pack", "GET", "/info/refs", "service=git-upload-pack", true},
		{"info refs no service", "GET", "/info/refs", "", true},
		{"info refs receive-pack", "GET", "/info/refs", "service=git-receive-pack", false},
		{"upload-pack POST", "POST", "/git-upload-pack", "", true},
		{"receive-pack POST", "POST", "/git-receive-pack", "", false},
		{"GET objects", "GET", "/objects/pack/pack-abc.pack", "", true},
		{"GET HEAD", "GET", "/HEAD", "", true},
		{"random POST", "POST", "/something", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := tt.path
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest(tt.method, url, nil)
			got := isReadOnly(req)
			if got != tt.want {
				t.Errorf("isReadOnly(%s %s?%s) = %v, want %v", tt.method, tt.path, tt.query, got, tt.want)
			}
		})
	}
}

func TestIsReceivePack(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		query  string
		want   bool
	}{
		{"info refs receive-pack", "GET", "/info/refs", "service=git-receive-pack", true},
		{"info refs upload-pack", "GET", "/info/refs", "service=git-upload-pack", false},
		{"info refs no service", "GET", "/info/refs", "", false},
		{"POST receive-pack", "POST", "/git-receive-pack", "", true},
		{"POST upload-pack", "POST", "/git-upload-pack", "", false},
		{"GET HEAD", "GET", "/HEAD", "", false},
		{"GET objects", "GET", "/objects/pack/pack-abc.pack", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := tt.path
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest(tt.method, url, nil)
			got := IsReceivePack(req)
			if got != tt.want {
				t.Errorf("IsReceivePack(%s %s?%s) = %v, want %v",
					tt.method, tt.path, tt.query, got, tt.want)
			}
		})
	}
}

func TestServeHTTP_NoRepoPath(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/info/refs?service=git-upload-pack", nil)
	// No repo path in context.
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestServeHTTP_PushRejectedWhenFlagOff(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.ReceivePackEnabled() {
		t.Fatal("expected ReceivePackEnabled default to be false")
	}

	// Both push entry points (info/refs service and POST) must be
	// rejected when the flag is off. If only one is gated, a client
	// can still drive a push by skipping the advertisement.
	cases := []struct {
		name   string
		method string
		url    string
	}{
		{"info-refs", "GET", "/info/refs?service=git-receive-pack"},
		{"post-receive-pack", "POST", "/git-receive-pack"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.url, nil)
			ctx := WithRepoPath(req.Context(), "/tmp/repo")
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403 for push attempt, got %d", w.Code)
			}
			// Error message must name the flag so operators can
			// find it without grepping source.
			if !strings.Contains(w.Body.String(), "SPINE_GIT_RECEIVE_PACK_ENABLED") {
				t.Errorf("rejection message should name the config flag, got: %q",
					w.Body.String())
			}
		})
	}
}

// TestServeHTTP_PushPassesGateWhenFlagOn asserts that the receive-pack
// request gate no longer rejects a push when ReceivePackEnabled is true.
// The test cannot complete a full push because that would require a real
// bare repo and git-http-backend CGI; instead it sets ResolveRepoPath to
// a path that fails the ensureReceivePackConfig step, proving the gate
// itself passed and the request reached the config-align stage. The
// end-to-end push path is covered by the scenario test.
func TestServeHTTP_PushPassesGateWhenFlagOn(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		ReceivePackEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !h.ReceivePackEnabled() {
		t.Fatal("expected ReceivePackEnabled=true")
	}

	req := httptest.NewRequest("GET", "/info/refs?service=git-receive-pack", nil)
	// Deliberately non-existent path so ensureReceivePackConfig fails;
	// that 500 proves we passed the 403 push gate.
	ctx := WithRepoPath(req.Context(), "/nonexistent/definitely/not/a/repo")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatalf("expected push gate to pass when flag is on, got 403 %q", w.Body.String())
	}
}

func TestServeHTTP_ConcurrencyLimit(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		MaxConcurrent:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fill the semaphore.
	h.sem <- struct{}{}

	req := httptest.NewRequest("GET", "/info/refs?service=git-upload-pack", nil)
	ctx := WithRepoPath(req.Context(), "/tmp/repo")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when concurrency limit reached, got %d", w.Code)
	}

	// Drain the semaphore.
	<-h.sem
}

func TestWithRepoPath_RoundTrip(t *testing.T) {
	ctx := WithRepoPath(context.Background(), "/var/spine/repos/ws-1")
	got := repoPathFromContext(ctx)
	if got != "/var/spine/repos/ws-1" {
		t.Errorf("expected /var/spine/repos/ws-1, got %q", got)
	}
}

func TestRepoPathFromContext_Empty(t *testing.T) {
	got := repoPathFromContext(context.Background())
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}
