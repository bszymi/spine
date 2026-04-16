package gateway

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/observe"
)

// Escape-byte hygiene: even if a client somehow smuggles ANSI or
// control bytes into a trace-id value, the traceIDMiddleware validator
// must reject it BEFORE any logger sees the string. Structured logging
// would escape these on output anyway; this regression test asserts
// the server-level invariant (never log raw escapes), locking down the
// behavior described in TASK-023.
func TestTraceIDMiddleware_RejectsEscapeBytes(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	mw := traceIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observe.Logger(r.Context()).Info("handler reached")
		w.WriteHeader(http.StatusOK)
	}))

	// Malicious header — the validator must discard this and generate
	// a fresh ID. Without the validator, the raw bytes would land in
	// every structured-log attribute downstream.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Trace-Id", "\x1b[31mpwn\x1b[0m\n\t")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	logged := buf.String()
	if strings.ContainsRune(logged, '\x1b') {
		t.Fatalf("log output contains raw escape byte: %q", logged)
	}
	// The outbound trace-id header must also be sanitized — never the
	// raw value the client sent.
	got := rec.Header().Get("X-Trace-Id")
	if strings.ContainsAny(got, "\x1b\n\t") {
		t.Fatalf("X-Trace-Id response header contains unsanitized bytes: %q", got)
	}
}

func TestTraceIDMiddleware_AcceptsValidID(t *testing.T) {
	mw := traceIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Trace-Id", "trace-abcd1234")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Trace-Id"); got != "trace-abcd1234" {
		t.Fatalf("expected trace-id to echo back, got %q", got)
	}
}
