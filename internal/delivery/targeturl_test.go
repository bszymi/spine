package delivery

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTargetValidator_ValidateURL(t *testing.T) {
	v := NewTargetValidator(nil)
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		// Positive cases.
		{name: "https basic", url: "https://example.com/hook"},
		{name: "https with query", url: "https://example.com/hook?x=1"},
		{name: "https non-default port", url: "https://example.com:8443/h"},

		// Syntactic rejections.
		{name: "empty", url: "", wantErr: "empty"},
		{name: "relative-only", url: "/path/only", wantErr: "missing scheme"},
		{name: "unsupported scheme — file", url: "file:///etc/passwd", wantErr: "unsupported scheme"},
		{name: "unsupported scheme — gopher", url: "gopher://example.com/", wantErr: "unsupported scheme"},
		{name: "userinfo rejected", url: "https://user:pass@example.com/", wantErr: "userinfo"},
		{name: "empty host", url: "https:///path", wantErr: "missing host"},
		{name: "http not allowlisted", url: "http://example.com/", wantErr: "http scheme only permitted"},
		{name: "parse error", url: "https://[invalid", wantErr: "parse error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateURL(tt.url)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestTargetValidator_AllowlistPermitsHTTPAndPrivate(t *testing.T) {
	v := NewTargetValidator([]string{"Localhost", "internal.example.com"})

	// http permitted because localhost is allowlisted (case-insensitive)
	if err := v.ValidateURL("http://localhost:8080/hook"); err != nil {
		t.Errorf("allowlisted http should pass: %v", err)
	}

	// Private IP allowed for allowlisted host
	if err := v.CheckAddr("internal.example.com", net.ParseIP("10.0.1.2")); err != nil {
		t.Errorf("allowlisted private IP should pass: %v", err)
	}

	// Non-allowlisted host with same IP rejected
	if err := v.CheckAddr("random.example.com", net.ParseIP("10.0.1.2")); err == nil {
		t.Error("non-allowlisted private IP must be rejected")
	}

	// Whitespace-only/empty entries are ignored at construction.
	v2 := NewTargetValidator([]string{"", "   ", "ok.example.com"})
	if err := v2.CheckAddr("", net.ParseIP("10.0.0.1")); err == nil {
		t.Error("empty allowlist entries must not promote empty-host matches")
	}
}

func TestTargetValidator_CheckAddr_RejectsDangerousRanges(t *testing.T) {
	v := NewTargetValidator(nil)
	tests := []struct {
		name    string
		ip      string
		wantErr string
	}{
		{name: "AWS IMDS", ip: "169.254.169.254", wantErr: "link-local"},
		{name: "IPv4 loopback", ip: "127.0.0.1", wantErr: "loopback"},
		{name: "IPv6 loopback", ip: "::1", wantErr: "loopback"},
		{name: "unspecified v4", ip: "0.0.0.0", wantErr: "unspecified"},
		{name: "unspecified v6", ip: "::", wantErr: "unspecified"},
		{name: "multicast v4", ip: "224.0.0.1", wantErr: "multicast"},
		{name: "IPv6 link-local", ip: "fe80::1", wantErr: "link-local"},
		{name: "RFC1918 10/8", ip: "10.1.2.3", wantErr: "private"},
		{name: "RFC1918 172.16/12", ip: "172.16.5.5", wantErr: "private"},
		{name: "RFC1918 192.168/16", ip: "192.168.1.1", wantErr: "private"},
		{name: "IPv6 ULA", ip: "fc00::1", wantErr: "private"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.CheckAddr("example.net", net.ParseIP(tt.ip))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CheckAddr(%s) = %v, want error containing %q", tt.ip, err, tt.wantErr)
			}
		})
	}

	// A public address passes.
	if err := v.CheckAddr("example.net", net.ParseIP("8.8.8.8")); err != nil {
		t.Errorf("public address rejected: %v", err)
	}
	// nil IP trips a guard.
	if err := v.CheckAddr("example.net", nil); err == nil {
		t.Error("nil IP must be rejected")
	}
}

func TestTargetValidator_Nil_IsPermissive(t *testing.T) {
	// Nil validator short-circuits so callers that forgot to wire one
	// don't silently reject every URL. Having all call sites route
	// through a non-nil validator is the enforcement strategy; this
	// test just guards the nil-safety contract.
	var v *TargetValidator
	if err := v.CheckAddr("anything", net.ParseIP("127.0.0.1")); err != nil {
		t.Errorf("nil validator CheckAddr must not reject: %v", err)
	}
	if !v.isAllowedHost("whatever") {
		t.Error("nil validator isAllowedHost must return true")
	}
}

// TestTargetValidator_HTTPClient_RejectsLoopback drives the validator
// end-to-end: a real httptest server on loopback, plus the dialer that
// rejects loopback at connect time. Proves that even a persisted
// unsafe URL cannot reach its destination.
func TestTargetValidator_HTTPClient_RejectsLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	v := NewTargetValidator(nil)
	client := v.HTTPClient(2 * time.Second)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected connection rejection for loopback")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback rejection, got %v", err)
	}
}

func TestTargetValidator_HTTPClient_AllowlistReachesLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()

	// httptest.Server binds to 127.0.0.1; allow the hostname used in
	// its URL (which is "127.0.0.1" — we allowlist it directly).
	host, _, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	v := NewTargetValidator([]string{host})
	client := v.HTTPClient(2 * time.Second)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("allowlisted loopback must reach server: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("expected 418, got %d", resp.StatusCode)
	}
}
