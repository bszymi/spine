// Package githttp provides a Git smart HTTP endpoint that wraps
// git-http-backend CGI. Upload-pack (clone/fetch) is always served;
// receive-pack (push) is gated by a config flag and, when enabled,
// wrapped with an HTTP-layer pre-receive check that consults
// branchprotect.Policy on every ref update (ADR-009 / EPIC-004).
package githttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
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

	// policy evaluates every ref update on a push (ADR-009 / EPIC-004
	// TASK-002). A nil policy is equivalent to a permissive policy —
	// pushes pass through unchecked. Production wires the
	// projection-backed policy; tests install whatever shape they need.
	policy branchprotect.Policy

	// maxPushBody caps the buffered portion of a receive-pack request
	// body at the command-section boundary. The full push body can be
	// very large (pack data), but the ref-update section is tiny — a
	// few hundred bytes per ref. We read pkt-lines until the flush
	// packet; this cap bounds the damage if a malicious client sends
	// an endless command stream with no flush.
	maxPushCommandBytes int

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
	// (git-receive-pack). Default false. Upgrade paths must opt in
	// explicitly — an existing deployment that installs this change
	// without setting the flag keeps the read-only behaviour it had
	// before.
	ReceivePackEnabled bool

	// Policy evaluates every ref update on a push against the
	// branch-protection ruleset (ADR-009 §3). When nil, pushes pass
	// through with no protection — appropriate for local development
	// and early-bootstrap deployments, but production should always
	// wire a real policy or pushes to `main` will succeed unchecked.
	Policy branchprotect.Policy
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
		resolveRepoPath:     cfg.ResolveRepoPath,
		gitBackendPath:      backendPath,
		sem:                 make(chan struct{}, maxConcurrent),
		opTimeout:           opTimeout,
		trustedCIDRs:        trustedCIDRs,
		receivePackEnabled:  cfg.ReceivePackEnabled,
		policy:              cfg.Policy,
		maxPushCommandBytes: defaultMaxPushCommandBytes,
		ensuredRepos:        make(map[string]bool),
	}, nil
}

// defaultMaxPushCommandBytes caps the pre-PACK portion of a push body
// we buffer for ref-update parsing. 1 MiB is generous — a push with
// 1000 refs is ~200 KiB of command text.
const defaultMaxPushCommandBytes = 1 << 20

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

	// Pre-receive branch-protection check. Runs only on the POST
	// /git-receive-pack request (the one that carries ref updates);
	// GET info/refs?service=git-receive-pack is just the capability
	// advertisement and carries no ref updates to evaluate.
	if push && r.Method == http.MethodPost {
		newBody, ok := h.prereceive(w, r, log)
		if !ok {
			return
		}
		r.Body = newBody
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

// prereceive intercepts the receive-pack request body, peels the
// ref-update command section off the front, evaluates each update
// against the handler's policy, and — if anything denies — writes a
// git-shaped error response and returns ok=false. On allow it returns
// a new Body that stitches the buffered command bytes back in front of
// the still-pending PACK stream so CGI sees the full push unchanged.
//
// Pre-receive semantics are all-or-nothing: any Deny rejects the
// whole push (no partial application). The response mirrors
// git-http-backend's shape when a real pre-receive hook exits
// non-zero so Git clients render the rejection as "remote: ..." lines
// plus per-ref "ng" results.
//
// A nil policy is treated as permissive — we still parse the command
// section (so a malformed push is rejected), but we do not gate any
// update. This keeps early-bootstrap deployments functional and
// matches how the API-path policy guards behave.
func (h *Handler) prereceive(w http.ResponseWriter, r *http.Request, log *slog.Logger) (io.ReadCloser, bool) {
	// Buffer up to maxPushCommandBytes looking for the flush that
	// terminates the command section. Anything after the flush is
	// PACK data and must reach CGI unchanged — so we do not fully
	// drain the body.
	cmdBuf, pendingBody, err := readPushCommands(r.Body, h.maxPushCommandBytes)
	if err != nil {
		log.Warn("pre-receive parse failed",
			"remote_addr", r.RemoteAddr, "error", err)
		h.writeReceivePackDenial(w,
			[]string{fmt.Sprintf("branch-protection: malformed push: %s", err)},
			nil)
		return nil, false
	}

	updates, err := parseRefUpdates(bytes.NewReader(cmdBuf))
	if err != nil {
		log.Warn("pre-receive parse failed",
			"remote_addr", r.RemoteAddr, "error", err)
		h.writeReceivePackDenial(w,
			[]string{fmt.Sprintf("branch-protection: malformed push: %s", err)},
			nil)
		return nil, false
	}

	// Resolve the policy for this push. The context value wins — the
	// gateway sets it per-request so a shared-mode deployment
	// evaluates against the target workspace's rules, not a
	// process-wide store captured at startup. The handler's Config
	// policy is the fallback for single-mode and tests.
	policy := policyFromContext(r.Context())
	if policy == nil {
		policy = h.policy
	}

	// Evaluate every non-spine/* ref. spine/* refs are out of scope
	// for user-authored rules (ADR-009 §3) but still flow through CGI
	// for audit/logging on the read side; we pass them through here
	// without calling Policy.Evaluate so the API-path and Git-path
	// audit surfaces remain consistent.
	if policy != nil {
		actor := actorForPush(r.Context())
		traceID := traceIDForPush(r.Context())
		var messages []string
		for _, u := range updates {
			if strings.HasPrefix(u.Ref, "refs/heads/spine/") || strings.HasPrefix(u.Ref, "spine/") {
				continue
			}
			kind := branchprotect.OpDirectWrite
			if u.IsDelete() {
				kind = branchprotect.OpDelete
			}
			dec, reasons, err := policy.Evaluate(r.Context(), branchprotect.Request{
				Branch:  u.Ref,
				Kind:    kind,
				Actor:   actor,
				TraceID: traceID,
			})
			if err != nil {
				log.Error("branch-protection evaluation failed, rejecting push",
					"ref", u.Ref, "error", err)
				messages = append(messages,
					fmt.Sprintf("branch-protection: evaluation error on %s", trimRefsHeads(u.Ref)))
				continue
			}
			if dec == branchprotect.DecisionDeny {
				for _, rc := range reasons {
					messages = append(messages,
						fmt.Sprintf("branch-protection: %s", rc.Message))
				}
			}
		}
		if len(messages) > 0 {
			log.Warn("pre-receive rejected push",
				"remote_addr", r.RemoteAddr,
				"refs", refPaths(updates),
				"reasons", messages,
			)
			h.writeReceivePackDenial(w, messages, updates)
			return nil, false
		}
	}

	// All updates allowed. Stitch the buffered command bytes back in
	// front of the remaining body so CGI sees the original stream.
	combined := io.MultiReader(bytes.NewReader(cmdBuf), pendingBody)
	return &combinedBody{r: combined, closer: pendingBody}, true
}

// combinedBody adapts io.Reader + io.Closer into an io.ReadCloser the
// http.Request body field expects.
type combinedBody struct {
	r      io.Reader
	closer io.Closer
}

func (c *combinedBody) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *combinedBody) Close() error {
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

// readPushCommands reads pkt-lines from body, appending them to a
// buffer, until the first flush-pkt (0000) is consumed. The buffer
// includes the flush so the caller can replay it verbatim. The
// returned body is the reader positioned just past the flush — that
// is the PACK stream (plus the trailing response close on the client
// side).
func readPushCommands(body io.ReadCloser, maxBytes int) ([]byte, io.ReadCloser, error) {
	var buf []byte
	for {
		if len(buf) >= maxBytes {
			return nil, body, errors.New("command section exceeds size cap without a flush packet")
		}
		lenBytes := make([]byte, 4)
		if _, err := io.ReadFull(body, lenBytes); err != nil {
			return nil, body, fmt.Errorf("read pkt-line length: %w", err)
		}
		buf = append(buf, lenBytes...)
		if string(lenBytes) == flushPkt {
			return buf, body, nil
		}
		length, err := parseHex16(lenBytes)
		if err != nil {
			return nil, body, fmt.Errorf("parse pkt-line length %q: %w", lenBytes, err)
		}
		if length < 4 {
			return nil, body, fmt.Errorf("pkt-line length %d below minimum (4)", length)
		}
		payload := make([]byte, length-4)
		if _, err := io.ReadFull(body, payload); err != nil {
			return nil, body, fmt.Errorf("read pkt-line payload: %w", err)
		}
		buf = append(buf, payload...)
	}
}

// writeReceivePackDenial renders a receive-pack-result body that
// rejects every update and surfaces the given messages as
// "remote: ..." lines on the client. Uses HTTP 200 so Git's smart
// HTTP client parses the sideband response rather than treating the
// push as a connection error.
func (h *Handler) writeReceivePackDenial(w http.ResponseWriter, messages []string, updates []RefUpdate) {
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buildReceivePackDenial(messages, updates))
}

// actorForPush returns the authenticated actor on the request, or an
// empty Actor if none is attached. Branch-protection evaluation
// tolerates an empty Actor (override defaults to false, role is the
// zero-value role which is below operator) — this is the right
// fail-closed behaviour when an unauthenticated push somehow reaches
// the gate.
func actorForPush(ctx context.Context) domain.Actor {
	if a := domain.ActorFromContext(ctx); a != nil {
		return *a
	}
	return domain.Actor{}
}

// traceIDForPush returns a trace id for the push. Prefers the incoming
// request trace id; falls back to a fresh random id so override-audit
// events always have something to correlate on.
func traceIDForPush(ctx context.Context) string {
	if id := observe.TraceID(ctx); id != "" {
		return id
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return ""
}

// refPaths extracts the Ref field of each update for logging.
func refPaths(updates []RefUpdate) []string {
	out := make([]string, len(updates))
	for i, u := range updates {
		out[i] = u.Ref
	}
	return out
}

// trimRefsHeads strips the "refs/heads/" prefix for readability in
// log/error messages; leaves non-branch refs alone.
func trimRefsHeads(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
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

// policyKey is the context key for a per-request branch-protection
// policy. Gateways set this before delegating to the handler so each
// push can be evaluated against the workspace's own rules (shared
// mode has per-workspace stores, so a single fixed policy captured at
// startup would mix or miss rules).
type policyKey struct{}

// WithPolicy returns a copy of ctx carrying the given policy. Reads
// in prereceive prefer this value over the handler's default Policy,
// letting the gateway scope each push to the target workspace.
func WithPolicy(ctx context.Context, p branchprotect.Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}

// policyFromContext returns the per-request policy or nil if none is
// attached.
func policyFromContext(ctx context.Context) branchprotect.Policy {
	p, _ := ctx.Value(policyKey{}).(branchprotect.Policy)
	return p
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
