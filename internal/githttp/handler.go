// Package githttp provides a read-only Git smart HTTP endpoint that wraps
// git-http-backend CGI. It allows runner containers to clone workspace
// repositories via HTTP without needing SSH keys or external git hosting.
package githttp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: CVE-2016-5386 is a pre-1.6.3 Go issue; this codebase runs 1.26+
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/observe"
)

// Handler serves Git repositories over HTTP using git-http-backend CGI.
// Upload-pack (clone/fetch) is always served; receive-pack (push) is
// gated by ReceivePackEnabled — see ADR-009 / EPIC-004 TASK-001 for the
// config surface. When enabled, this handler is a plain passthrough with
// no branch-protection; TASK-002 wraps it with the pre-receive policy.
type Handler struct {
	// resolveRepoPath returns the absolute filesystem path for a given workspace ID.
	// Returns an error if the workspace is unknown or inactive.
	resolveRepoPath func(ctx context.Context, workspaceID string) (string, error)

	// gitBackendPath is the absolute path to the git-http-backend binary.
	gitBackendPath string

	// sem limits concurrent git pack operations.
	sem chan struct{}

	// opTimeout is the per-request timeout for git operations.
	opTimeout time.Duration

	// trustedCIDRs are IP ranges that bypass authentication.
	trustedCIDRs []*net.IPNet

	// receivePackEnabled controls whether git-receive-pack (push) is
	// reachable. Default false — an existing deployment upgrading past
	// this change does not silently start accepting pushes.
	receivePackEnabled bool

	// mu protects ensuredRepos which tracks repos whose local
	// http.receivepack config has been aligned with receivePackEnabled.
	mu           sync.Mutex
	ensuredRepos map[string]bool
}

// Config configures the git HTTP handler.
type Config struct {
	// ResolveRepoPath returns the filesystem path for a workspace ID.
	ResolveRepoPath func(ctx context.Context, workspaceID string) (string, error)

	// MaxConcurrent is the maximum number of concurrent git pack operations.
	// Defaults to 5.
	MaxConcurrent int

	// OpTimeout is the per-request timeout for git operations.
	// Defaults to 30s.
	OpTimeout time.Duration

	// TrustedCIDRs are IP ranges (CIDR notation) that bypass authentication.
	// Requests from these ranges can access the git endpoint without a bearer token.
	// Example: ["172.16.0.0/12", "10.0.0.0/8"]
	TrustedCIDRs []string

	// ReceivePackEnabled turns on the git push endpoint
	// (git-receive-pack). Default false. This is a bare on-off switch
	// per EPIC-004 TASK-001 — no branch-protection logic runs here yet;
	// TASK-002 wraps receive-pack with pre-receive enforcement. Upgrade
	// paths must opt in explicitly.
	ReceivePackEnabled bool
}

// NewHandler creates a new git HTTP handler.
func NewHandler(cfg Config) (*Handler, error) {
	backendPath, err := findGitHTTPBackend()
	if err != nil {
		return nil, fmt.Errorf("git-http-backend not found: %w", err)
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}

	opTimeout := cfg.OpTimeout
	if opTimeout <= 0 {
		opTimeout = 30 * time.Second
	}

	var trustedCIDRs []*net.IPNet
	for _, cidr := range cfg.TrustedCIDRs {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted CIDR %q: %w", cidr, err)
		}
		trustedCIDRs = append(trustedCIDRs, ipnet)
	}

	return &Handler{
		resolveRepoPath:    cfg.ResolveRepoPath,
		gitBackendPath:     backendPath,
		sem:                make(chan struct{}, maxConcurrent),
		opTimeout:          opTimeout,
		trustedCIDRs:       trustedCIDRs,
		receivePackEnabled: cfg.ReceivePackEnabled,
		ensuredRepos:       make(map[string]bool),
	}, nil
}

// ReceivePackEnabled reports whether git push is configured to reach
// the backend. Exposed for assembly-site logging and tests; callers
// should not rely on this to gate requests — ServeHTTP is authoritative.
func (h *Handler) ReceivePackEnabled() bool {
	return h.receivePackEnabled
}

// IsTrustedIP returns true if the IP is within a trusted CIDR range.
func (h *Handler) IsTrustedIP(remoteAddr string) bool {
	if len(h.trustedCIDRs) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range h.trustedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ServeHTTP handles git smart HTTP requests.
// The request path must already have the workspace prefix stripped,
// leaving only the git-specific path (e.g., "/info/refs", "/git-upload-pack").
// The workspace's repo path must be set in context via WithRepoPath.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repoPath := repoPathFromContext(r.Context())
	if repoPath == "" {
		http.Error(w, "repository path not resolved", http.StatusInternalServerError)
		return
	}

	gitPath := r.URL.Path
	log := observe.Logger(r.Context())

	// Push path: reachable only when ReceivePackEnabled is on. The
	// flag default is false so existing deployments see no behaviour
	// change on upgrade. When off, deny with a message that names the
	// flag so operators can find it without grepping source.
	push := IsReceivePack(r)
	if push && !h.receivePackEnabled {
		log.Warn("git push attempt rejected — receive-pack disabled",
			"remote_addr", r.RemoteAddr,
			"path", gitPath,
		)
		http.Error(w,
			"git push is disabled — enable via git.receive_pack_enabled "+
				"(SPINE_GIT_RECEIVE_PACK_ENABLED=true); see ADR-009",
			http.StatusForbidden)
		return
	}

	// Non-read, non-push requests are unsupported (e.g. arbitrary POSTs
	// to paths outside the git protocol).
	if !push && !isReadOnly(r) {
		log.Warn("git request rejected — not a supported operation",
			"remote_addr", r.RemoteAddr,
			"path", gitPath,
			"method", r.Method,
		)
		http.Error(w, "unsupported git operation", http.StatusForbidden)
		return
	}

	// Acquire concurrency semaphore.
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		log.Warn("git request rejected — concurrency limit reached",
			"remote_addr", r.RemoteAddr,
		)
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many concurrent git operations", http.StatusServiceUnavailable)
		return
	}

	// Align the repo's local http.receivepack with the flag. Without
	// this, git-http-backend refuses push even when our gate above
	// allows it, because the repo config still carries an explicit
	// `http.receivepack=false` from a previous handler lifetime.
	if err := h.ensureReceivePackConfig(r.Context(), repoPath); err != nil {
		log.Error("failed to align receivepack config", "error", err, "repo_path", repoPath)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	start := time.Now()

	// Apply per-operation timeout.
	ctx, cancel := context.WithTimeout(r.Context(), h.opTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	// Build CGI handler for git-http-backend.
	cgiHandler := &cgi.Handler{
		Path: h.gitBackendPath,
		Dir:  repoPath,
		Env: []string{
			"GIT_PROJECT_ROOT=" + repoPath,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	cgiHandler.ServeHTTP(w, r)

	duration := time.Since(start)
	log.Info("git http request",
		"remote_addr", r.RemoteAddr,
		"git_path", gitPath,
		"repo_path", repoPath,
		"duration_ms", duration.Milliseconds(),
	)
}

// ensureReceivePackConfig aligns the repo's local http.receivepack with
// the handler's ReceivePackEnabled setting. Idempotent per repo path per
// handler lifetime — the value never flips at runtime, so caching by
// path is safe.
func (h *Handler) ensureReceivePackConfig(ctx context.Context, repoPath string) error {
	h.mu.Lock()
	if h.ensuredRepos[repoPath] {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	value := "false"
	if h.receivePackEnabled {
		value = "true"
	}

	// G702 flags cmd.Dir as taint-tracked. repoPath is resolved by
	// the caller from the workspace registry (validated workspace
	// IDs), never from untrusted client input.
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "http.receivepack", value) //nolint:gosec // G702: repoPath is server-resolved from workspace registry, not request input
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git config http.receivepack=%s: %w", value, err)
	}

	h.mu.Lock()
	h.ensuredRepos[repoPath] = true
	h.mu.Unlock()
	return nil
}

// isReadOnly returns true if the request is a read-only git operation.
func isReadOnly(r *http.Request) bool {
	path := r.URL.Path

	// GET /info/refs?service=git-upload-pack — ref advertisement for clone/fetch
	if r.Method == http.MethodGet && strings.HasSuffix(path, "/info/refs") {
		service := r.URL.Query().Get("service")
		return service == "" || service == "git-upload-pack"
	}

	// POST /git-upload-pack — pack data for clone/fetch
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/git-upload-pack") {
		return true
	}

	// GET for dumb HTTP protocol files (HEAD, objects/*, etc.)
	// Excludes info/refs?service=git-receive-pack which falls through the
	// query-string check above (returns false there).
	if r.Method == http.MethodGet {
		return true
	}

	return false
}

// IsReceivePack returns true if the request is a git push (receive-pack)
// operation — either the ref advertisement for push or the pack data.
// The receive-pack gate in ServeHTTP drives off this and must stay in
// sync with isReadOnly's rejection of the same paths.
//
// Exported so the gateway's auth middleware can distinguish push from
// read: trusted-CIDR bypass applies to clone/fetch but not to push, so
// every push has an actor identity attached for branch-protection and
// audit.
func IsReceivePack(r *http.Request) bool {
	path := r.URL.Path

	if r.Method == http.MethodGet && strings.HasSuffix(path, "/info/refs") {
		return r.URL.Query().Get("service") == "git-receive-pack"
	}
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/git-receive-pack") {
		return true
	}
	return false
}

// findGitHTTPBackend locates the git-http-backend binary.
func findGitHTTPBackend() (string, error) {
	// Check common locations.
	candidates := []string{
		"/usr/lib/git-core/git-http-backend",
		"/usr/libexec/git-core/git-http-backend",
	}

	// Also check git --exec-path.
	if out, err := exec.Command("git", "--exec-path").Output(); err == nil {
		execPath := strings.TrimSpace(string(out))
		candidates = append([]string{filepath.Join(execPath, "git-http-backend")}, candidates...)
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("git-http-backend not found in any known location")
}

// repoPathKey is the context key for the resolved repo filesystem path.
type repoPathKey struct{}

// WithRepoPath returns a context with the resolved repository path.
func WithRepoPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, repoPathKey{}, path)
}

// repoPathFromContext extracts the repo path from context.
func repoPathFromContext(ctx context.Context) string {
	path, _ := ctx.Value(repoPathKey{}).(string)
	return path
}

// LogCloneOperation logs a clone operation with structured fields for observability.
func LogCloneOperation(ctx context.Context, workspaceID, remoteAddr, requestedRef string, duration time.Duration) {
	log := observe.Logger(ctx)
	log.Info("git clone operation",
		slog.String("workspace_id", workspaceID),
		slog.String("remote_addr", remoteAddr),
		slog.String("requested_ref", requestedRef),
		slog.Int64("duration_ms", duration.Milliseconds()),
	)
}
