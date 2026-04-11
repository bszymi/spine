package gateway

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/bszymi/spine/internal/domain"
)

// rateLimiterEntry tracks the limiter and last-seen time for an IP.
type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ipRateLimiter manages per-IP rate limiters.
type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rateLimiterEntry
	rate     rate.Limit
	burst    int
}

func newIPRateLimiter(r rate.Limit, burst int) *ipRateLimiter {
	rl := &ipRateLimiter{
		limiters: make(map[string]*rateLimiterEntry),
		rate:     r,
		burst:    burst,
	}
	go rl.evictLoop()
	return rl
}

func (rl *ipRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	entry, ok := rl.limiters[ip]
	if !ok {
		entry = &rateLimiterEntry{
			limiter:  rate.NewLimiter(rl.rate, rl.burst),
			lastSeen: time.Now(),
		}
		rl.limiters[ip] = entry
		return entry.limiter
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// evictLoop removes entries not seen for 10 minutes.
func (rl *ipRateLimiter) evictLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		rl.mu.Lock()
		for ip, entry := range rl.limiters {
			if time.Since(entry.lastSeen) > 10*time.Minute {
				delete(rl.limiters, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// rateLimitMiddleware creates a per-IP rate limiting middleware.
// rps is the allowed requests per second; burst is the max burst size.
func rateLimitMiddleware(rps float64, burst int) func(http.Handler) http.Handler {
	limiter := newIPRateLimiter(rate.Limit(rps), burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r)
			if !limiter.getLimiter(ip).Allow() {
				w.Header().Set("Retry-After", "1")
				WriteError(w, domain.NewError(domain.ErrRateLimited, "rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// extractIP returns the client IP from RemoteAddr, stripping the port.
func extractIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
