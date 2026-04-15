package delivery

import (
	"net/http"
	"testing"
	"time"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		isNetworkError bool
		want           bool
	}{
		{"network error", 0, true, true},
		{"500", 500, false, true},
		{"502", 502, false, true},
		{"503", 503, false, true},
		{"429", 429, false, true},
		{"400", 400, false, false},
		{"401", 401, false, false},
		{"403", 403, false, false},
		{"404", 404, false, false},
		{"200", 200, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryable(tt.statusCode, tt.isNetworkError)
			if got != tt.want {
				t.Errorf("isRetryable(%d, %v) = %v, want %v", tt.statusCode, tt.isNetworkError, got, tt.want)
			}
		})
	}
}

func TestNextRetryDelay(t *testing.T) {
	expected := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}

	for i, want := range expected {
		got := nextRetryDelay(i)
		if got != want {
			t.Errorf("nextRetryDelay(%d) = %v, want %v", i, got, want)
		}
	}

	// Beyond max should return last delay
	got := nextRetryDelay(10)
	if got != 16*time.Second {
		t.Errorf("nextRetryDelay(10) = %v, want 16s", got)
	}
}

func TestMaxRetriesFor(t *testing.T) {
	if got := maxRetriesFor("run_started"); got != MaxRetriesDomain {
		t.Errorf("domain event: got %d, want %d", got, MaxRetriesDomain)
	}
	if got := maxRetriesFor("step_assigned"); got != MaxRetriesDomain {
		t.Errorf("domain event: got %d, want %d", got, MaxRetriesDomain)
	}
	if got := maxRetriesFor("engine_recovered"); got != MaxRetriesOperational {
		t.Errorf("operational event: got %d, want %d", got, MaxRetriesOperational)
	}
	if got := maxRetriesFor("thread_created"); got != MaxRetriesOperational {
		t.Errorf("operational event: got %d, want %d", got, MaxRetriesOperational)
	}
}

func TestRetryAfterFromHeader(t *testing.T) {
	// Seconds value
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"30"}}}
	if got := retryAfterFromHeader(resp); got != 30*time.Second {
		t.Errorf("seconds: got %v, want 30s", got)
	}

	// No header
	resp = &http.Response{Header: http.Header{}}
	if got := retryAfterFromHeader(resp); got != 0 {
		t.Errorf("empty: got %v, want 0", got)
	}

	// Nil response
	if got := retryAfterFromHeader(nil); got != 0 {
		t.Errorf("nil: got %v, want 0", got)
	}
}

func TestIsDomainEvent(t *testing.T) {
	domain := []string{"artifact_created", "run_started", "step_assigned", "run_timeout"}
	for _, e := range domain {
		if !isDomainEvent(e) {
			t.Errorf("%s should be domain event", e)
		}
	}

	operational := []string{"engine_recovered", "thread_created", "projection_synced"}
	for _, e := range operational {
		if isDomainEvent(e) {
			t.Errorf("%s should not be domain event", e)
		}
	}
}
