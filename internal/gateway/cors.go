package gateway

import (
	"net/http"
	"strings"

	"github.com/bszymi/spine/internal/domain"
)

// corsMiddleware enforces a deny-by-default CORS policy.
//
// allowedOrigins is a list of fully-qualified origins (scheme+host[+port]).
// The literal value "*" enables wildcard mode, which allows any origin but
// suppresses Access-Control-Allow-Credentials to comply with browser rules.
//
// Requests without an Origin header (CLI clients, server-to-server) are
// unaffected. Requests with an Origin header outside the allowlist receive
// a 403, including preflight OPTIONS.
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := false
	set := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o == "*" {
			allowAll = true
			continue
		}
		set[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			_, listed := set[origin]
			if !allowAll && !listed {
				WriteError(w, domain.NewError(domain.ErrForbidden, "cross-origin request not allowed"))
				return
			}

			if allowAll && !listed {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				reqMethod := r.Header.Get("Access-Control-Request-Method")
				w.Header().Set("Access-Control-Allow-Methods", reqMethod)
				if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
				}
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// parseCORSOrigins parses the SPINE_CORS_ALLOWED_ORIGINS env var value
// into a list of trimmed entries. Empty string yields an empty list
// (deny-all).
func parseCORSOrigins(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
