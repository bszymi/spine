package delivery

import (
	"net/http"
	"strconv"
	"time"
)

// MaxRetriesDomain is the max retry count for domain events.
const MaxRetriesDomain = 5

// MaxRetriesOperational is the max retry count for operational events.
const MaxRetriesOperational = 2

// backoffDelays defines exponential backoff intervals.
var backoffDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
}

// isDomainEvent returns true for events reconstructible from Git.
func isDomainEvent(eventType string) bool {
	switch eventType {
	case "artifact_created", "artifact_updated",
		"run_started", "run_completed", "run_failed", "run_cancelled",
		"run_paused", "run_resumed",
		"step_assigned", "step_started", "step_completed", "step_failed",
		"step_timeout", "retry_attempted", "run_timeout":
		return true
	}
	return false
}

// maxRetriesFor returns the max retry count based on event type.
func maxRetriesFor(eventType string) int {
	if isDomainEvent(eventType) {
		return MaxRetriesDomain
	}
	return MaxRetriesOperational
}

// isRetryable determines if a delivery failure should be retried.
// 5xx, timeouts, and network errors are retryable.
// 4xx are permanent failures except 429 (Too Many Requests).
func isRetryable(statusCode int, isNetworkError bool) bool {
	if isNetworkError {
		return true
	}
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	if statusCode >= 500 {
		return true
	}
	return false
}

// nextRetryDelay returns the backoff delay for the given attempt count.
func nextRetryDelay(attemptCount int) time.Duration {
	if attemptCount >= len(backoffDelays) {
		return backoffDelays[len(backoffDelays)-1]
	}
	return backoffDelays[attemptCount]
}

// retryAfterFromHeader parses the Retry-After header value.
// Returns 0 if not present or unparseable.
func retryAfterFromHeader(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	val := resp.Header.Get("Retry-After")
	if val == "" {
		return 0
	}
	// Try as seconds first
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	// Try as HTTP date
	if t, err := http.ParseTime(val); err == nil {
		delay := time.Until(t)
		if delay > 0 {
			return delay
		}
	}
	return 0
}
