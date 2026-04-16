package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failingWriter returns an error on the Nth Write call.
type failingWriter struct {
	http.ResponseWriter
	writes     int
	failAfter  int
	flushCalls int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > f.failAfter {
		return 0, errors.New("simulated write error")
	}
	return f.ResponseWriter.Write(p)
}

func (f *failingWriter) Flush() {
	f.flushCalls++
	if flusher, ok := f.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestWriteSSEEvent_ReturnsErrorOnWriteFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := &failingWriter{ResponseWriter: rec, failAfter: 0}
	rc := http.NewResponseController(fw)

	err := writeSSEEvent(rc, fw, fw, "id-1", "test.event", []byte(`{"a":1}`))
	if err == nil {
		t.Fatal("expected write error, got nil")
	}
	if !strings.Contains(err.Error(), "simulated write error") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteSSEEvent_Success(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := &failingWriter{ResponseWriter: rec, failAfter: 1000}
	rc := http.NewResponseController(fw)

	err := writeSSEEvent(rc, fw, fw, "id-1", "test.event", []byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fw.flushCalls != 1 {
		t.Fatalf("expected 1 flush, got %d", fw.flushCalls)
	}
	body := rec.Body.String()
	for _, want := range []string{"id: id-1", "event: test.event", `data: {"a":1}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %q", want, body)
		}
	}
}

func TestWriteSSEHeartbeat_ReturnsErrorOnWriteFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := &failingWriter{ResponseWriter: rec, failAfter: 0}
	rc := http.NewResponseController(fw)

	err := writeSSEHeartbeat(rc, fw, fw)
	if err == nil {
		t.Fatal("expected heartbeat error, got nil")
	}
}

func TestSSEReplayCapConstant(t *testing.T) {
	if sseReplayCap > 500 {
		t.Fatalf("sseReplayCap=%d is larger than intended; hardening regresses", sseReplayCap)
	}
	if sseReplayCap < 10 {
		t.Fatalf("sseReplayCap=%d is unusably small", sseReplayCap)
	}
}
