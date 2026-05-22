package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newCORSTestHandler(origins []string) http.Handler {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return corsMiddleware(origins)(next)
}

func TestCORS_NoOriginHeader_PassesThrough(t *testing.T) {
	h := newCORSTestHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("should not set allow-origin without Origin header")
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatalf("expected Vary: Origin, got %q", rec.Header().Get("Vary"))
	}
}

func TestCORS_EmptyAllowlist_RejectsCrossOrigin(t *testing.T) {
	h := newCORSTestHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestCORS_AllowedOrigin_Echoes(t *testing.T) {
	h := newCORSTestHandler([]string{"https://app.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected origin echo, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("expected credentials true, got %q", got)
	}
}

func TestCORS_DisallowedOrigin_Rejected(t *testing.T) {
	h := newCORSTestHandler([]string{"https://app.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestCORS_Preflight_AllowedOrigin_Returns204(t *testing.T) {
	h := newCORSTestHandler([]string{"https://app.example.com"})
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "POST" {
		t.Fatalf("expected allow-methods POST, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("expected allow-headers echo, got %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("preflight should have empty body, got %d bytes", rec.Body.Len())
	}
}

func TestCORS_Wildcard_AllowsAnyWithoutCredentials(t *testing.T) {
	h := newCORSTestHandler([]string{"*"})
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://random.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected wildcard, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("wildcard must not set credentials, got %q", got)
	}
}

func TestCORS_WildcardPlusExplicit_ExplicitPrefersCredentials(t *testing.T) {
	h := newCORSTestHandler([]string{"*", "https://app.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected explicit echo when origin is listed, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("expected credentials true for explicit listing, got %q", got)
	}
}

func TestParseCORSOrigins(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"https://a.example", []string{"https://a.example"}},
		{" https://a.example , https://b.example ", []string{"https://a.example", "https://b.example"}},
		{",,,", nil},
		{"*", []string{"*"}},
	}
	for _, c := range cases {
		got := parseCORSOrigins(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("parseCORSOrigins(%q) length: got %v want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseCORSOrigins(%q): got %v want %v", c.in, got, c.want)
			}
		}
	}
}
