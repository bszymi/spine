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
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/observe"
)

// Handler serves Git repositories over HTTP using git-http-backend CGI.
// It is read-only (upload-pack only) and supports workspace-scoped repo resolution.
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

	// mu protects ensuredRepos which tracks repos with receivepack disabled.
	mu          sync.Mutex
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
		resolveRepoPath: cfg.ResolveRepoPath,
		gitBackendPath:  backendPath,
		sem:             make(chan struct{}, maxConcurrent),
		opTimeout:       opTimeout,
		trustedCIDRs:    trustedCIDRs,
		ensuredRepos:    make(map[string]bool),
	}, nil
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

	// Only allow read operations (upload-pack).
	if !isReadOnly(r) {
		log.Warn("git push attempt rejected",
			"remote_addr", r.RemoteAddr,
			"path", gitPath,
		)
		http.Error(w, "push is not allowed — this endpoint is read-only", http.StatusForbidden)
		return
	}

	// Acquire concurrency semaphore.
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		log.Warn("git clone rejected — concurrency limit reached",
			"remote_addr", r.RemoteAddr,
		)
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many concurrent git operations", http.StatusServiceUnavailable)
		return
	}

	// Ensure receivepack is disabled for this repo.
	if err := h.ensureReceivePackDisabled(r.Context(), repoPath); err != nil {
		log.Error("failed to disable receivepack", "error", err, "repo_path", repoPath)
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

// ensureReceivePackDisabled sets http.receivepack=false on the repo's git config.
// This is idempotent and only runs once per repo path per handler lifetime.
func (h *Handler) ensureReceivePackDisabled(ctx context.Context, repoPath string) error {
	h.mu.Lock()
	if h.ensuredRepos[repoPath] {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	cmd := exec.CommandContext(ctx, "git", "config", "--local", "http.receivepack", "false")
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git config http.receivepack=false: %w", err)
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
	if r.Method == http.MethodGet {
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
