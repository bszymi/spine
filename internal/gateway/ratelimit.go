package gateway

import (
	"net"
	"net/http"
	"strings"
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
// trustedProxies are CIDR-parsed networks whose X-Forwarded-For header
// will be honored. Pass nil to disable (the default) — the limiter
// then keys on r.RemoteAddr exactly as before.
func rateLimitMiddleware(rps float64, burst int, trustedProxies []*net.IPNet) func(http.Handler) http.Handler {
	limiter := newIPRateLimiter(rate.Limit(rps), burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIPForRateLimit(r, trustedProxies)
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

// clientIPForRateLimit returns the IP used to bucket a request. When
// RemoteAddr is inside a trusted proxy CIDR, the right-most
// X-Forwarded-For entry that is NOT itself trusted is returned; this
// makes each real client its own bucket when the deployment sits
// behind a reverse proxy. When no trusted proxies are configured, or
// when RemoteAddr is not among them, the function returns
// RemoteAddr — naive X-Forwarded-For trust would let any client forge
// a source IP and evade the limiter.
func clientIPForRateLimit(r *http.Request, trustedProxies []*net.IPNet) string {
	remote := extractIP(r)
	if len(trustedProxies) == 0 {
		return remote
	}
	remoteIP := net.ParseIP(remote)
	if remoteIP == nil || !ipInNets(remoteIP, trustedProxies) {
		return remote
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remote
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			continue
		}
		if ipInNets(ip, trustedProxies) {
			continue
		}
		return candidate
	}
	return remote
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseTrustedProxyCIDRs parses a comma-separated CIDR list. Blank
// entries are skipped. An unparseable entry is returned as an error
// so misconfiguration is surfaced at server startup rather than
// silently degrading to "no proxies trusted".
func ParseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, c := range strings.Split(raw, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			return nil, err
		}
		out = append(out, ipnet)
	}
	return out, nil
}
