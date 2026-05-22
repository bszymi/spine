package scheduler

import (
	"math"
	"math/rand/v2"
	"time"
)

const (
	baseDelay = 1 * time.Second
	maxDelay  = 5 * time.Minute
)

// CalculateBackoff returns the delay before the next retry attempt.
// Supports "exponential" (default) and "linear" backoff strategies.
func CalculateBackoff(attempt int, backoffType string) time.Duration {
	var delay time.Duration

	switch backoffType {
	case "linear":
		delay = baseDelay * time.Duration(attempt+1)
	default: // exponential
		delay = baseDelay * time.Duration(math.Pow(2, float64(attempt)))
	}

	// Add jitter: random value in [0, baseDelay)
	jitter := time.Duration(rand.Int64N(int64(baseDelay)))
	delay += jitter

	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}
