// Package githttp provides a Git smart HTTP endpoint that wraps
// git-http-backend CGI. Upload-pack (clone/fetch) is always served;
// receive-pack (push) is gated by a config flag and, when enabled,
// wrapped with an HTTP-layer pre-receive check that admits every ref
// update by ref class (core/refadmit, INIT-023 TASK-016) and then
// consults branchprotect.Policy on ordinary branches and admitted
// non-branch refs (ADR-009 / EPIC-004).
package githttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/refadmit"
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

	// trustedByWorkspace maps a workspace ID to the IP ranges whose
	// direct peers may read that workspace's repositories without a
	// bearer (INIT-024 TASK-006, review finding N-2). There is no
	// process-wide list: a trusted address opens only the workspaces it
	// is bound to (IsTrustedIPForWorkspace).
	trustedByWorkspace map[string][]*net.IPNet

	// receivePackEnabled controls whether git-receive-pack (push) is
	// reachable. Default false — an existing deployment upgrading past
	// this change does not silently start accepting pushes.
	receivePackEnabled bool

	// policy evaluates every ref update on a push (ADR-009 / EPIC-004
	// TASK-002). A nil policy is equivalent to a permissive policy —
	// pushes pass through unchecked. Production wires the
	// projection-backed policy; tests install whatever shape they need.
	policy branchprotect.Policy

	// runRefs and stepDefs back the ref-class admission of run, plan
	// and divergence branches (INIT-023 TASK-016). They are the
	// fallback pair: a per-request pair attached with WithRunRefs (even
	// a nil one) wins. Either nil makes those refs operator-only (fail
	// closed).
	runRefs  refadmit.RunReader
	stepDefs refadmit.StepDefLookup

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

	// budgetHook is the checkout-budget pre-receive hook every
	// receive-pack POST installs and pins (INIT-024 TASK-003,
	// budget_hook.go). Set only when receive-pack is enabled. hooksWarned
	// (under mu) records the repositories whose overridden core.hooksPath
	// was already logged.
	budgetHook  budgetHook
	hooksWarned map[string]bool
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

	// TrustedCIDRsByWorkspace binds IP ranges (CIDR notation) to the
	// workspace whose repositories their peers may clone and fetch
	// without a bearer token, for example
	// {"ws-a": {"172.28.1.10/32"}, "ws-b": {"172.28.1.11/32"}}. A range
	// trusts its peers for the workspaces it is listed under and no
	// other (INIT-024 TASK-006, review finding N-2: a process-wide list
	// let a runner host trusted for one tenant clone every tenant's
	// repository). It never applies to a push. Single-workspace
	// `spine serve` binds the bare SPINE_GIT_HTTP_TRUSTED_CIDRS list to
	// its one workspace.
	TrustedCIDRsByWorkspace map[string][]string

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
	//
	// The policy is not the only gate: every ref update is first
	// admitted by ref class (core/refadmit, INIT-023 TASK-016), with or
	// without a policy. Tags and other non-branch refs and the reserved
	// spine/** namespace are operator-only, and run, plan and divergence
	// branches admit only the actor bound to their current step.
	Policy branchprotect.Policy

	// RunRefs reads runs, step executions and divergence branches for
	// the run-branch admission (INIT-023 TASK-016). A per-request reader
	// attached with WithRunRefs wins, even a nil one. With no reader,
	// run, plan and divergence refs are operator-only.
	RunRefs refadmit.RunReader

	// StepDefs resolves a run's step definitions for the review freeze
	// (TASK-016 owner question 4). It is paired with RunRefs: when a
	// per-request reader is attached, the per-request lookup (or none)
	// is used, never this one. With no lookup, run, plan and divergence
	// refs are operator-only.
	StepDefs refadmit.StepDefLookup

	// TreeBudget is the checkout budget the pre-receive hook holds every
	// pushed tip to whenever ReceivePackEnabled is on (INIT-024 TASK-003;
	// owner question 3: on with receive-pack, no off switch). Unset
	// fields mean the defaults (git.DefaultTreeBudget). The hook's
	// deadline is OpTimeout.
	TreeBudget git.TreeBudget
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

	trustedByWorkspace := make(map[string][]*net.IPNet, len(cfg.TrustedCIDRsByWorkspace))
	for workspaceID, cidrs := range cfg.TrustedCIDRsByWorkspace {
		if workspaceID == "" {
			return nil, errors.New("trusted CIDRs bound to an empty workspace ID")
		}
		for _, cidr := range cidrs {
			_, ipnet, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted CIDR %q for workspace %q: %w", cidr, workspaceID, err)
			}
			trustedByWorkspace[workspaceID] = append(trustedByWorkspace[workspaceID], ipnet)
		}
	}

	// The checkout-budget hook is built up front: a receive-pack
	// endpoint that cannot install it must not start (fail closed).
	var hook budgetHook
	if cfg.ReceivePackEnabled {
		if hook, err = newBudgetHook(cfg.TreeBudget, opTimeout); err != nil {
			return nil, err
		}
	}

	return &Handler{
		budgetHook:          hook,
		resolveRepoPath:     cfg.ResolveRepoPath,
		gitBackendPath:      backendPath,
		sem:                 make(chan struct{}, maxConcurrent),
		opTimeout:           opTimeout,
		trustedByWorkspace:  trustedByWorkspace,
		receivePackEnabled:  cfg.ReceivePackEnabled,
		policy:              cfg.Policy,
		runRefs:             cfg.RunRefs,
		stepDefs:            cfg.StepDefs,
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

// IsTrustedIPForWorkspace reports whether remoteAddr ("host:port" or a
// bare IP) is inside a trusted CIDR bound to workspaceID (INIT-024
// TASK-006, review finding N-2). The workspace ID is matched exactly,
// and an address bound to another workspace — or to none — is not
// trusted, so a runner host trusted for one tenant gets no token-less
// read of another. The caller passes the ID of the workspace the
// request resolved to (whose repository it will serve), never a raw
// URL segment, and decides which peers qualify (direct peers only:
// the gateway's gitPeerIP).
func (h *Handler) IsTrustedIPForWorkspace(workspaceID, remoteAddr string) bool {
	nets := h.trustedByWorkspace[workspaceID]
	if workspaceID == "" || len(nets) == 0 {
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
	for _, cidr := range nets {
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

	// An info/refs query other than the three a git client sends is
	// ambiguous: git-http-backend parses it with its own parser and
	// may read a service this handler did not (infoRefsServiceOf).
	// Refuse it before the push gate, the read gate and CGI. The raw
	// query is not logged: it is client-chosen and unbounded.
	if isInfoRefs(r) && infoRefsServiceOf(r.URL.RawQuery) == infoRefsNonCanonical {
		log.Warn("git request rejected — non-canonical info/refs query",
			"remote_addr", r.RemoteAddr,
			"path", gitPath,
			"query_bytes", len(r.URL.RawQuery),
		)
		http.Error(w, "unsupported info/refs query", http.StatusBadRequest)
		return
	}

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

	// The checkout-budget pre-receive hook (INIT-024 TASK-003) is put
	// in place before the push reaches git, and git is pointed at it
	// below. A push that cannot have it is refused (fail closed).
	var hooksDir string
	if push && r.Method == http.MethodPost {
		dir, err := h.installBudgetHook(r.Context(), repoPath)
		if err != nil {
			log.Error("checkout-budget pre-receive hook not installed; push refused", "error", err, "repo_path", repoPath)
			http.Error(w, "push gate unavailable", http.StatusInternalServerError)
			return
		}
		hooksDir = dir
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
	if hooksDir != "" {
		cgiHandler.Env = append(cgiHandler.Env, hooksPathEnv(hooksDir))
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
// Every update is first admitted by ref class (core/refadmit, INIT-023
// TASK-016), always, whatever the policy. A nil policy is then treated
// as permissive for the updates admission lets through: ordinary
// branches, and the non-branch refs an operator pushes. This keeps
// early-bootstrap deployments functional for branch pushes and
// matches how the API-path policy guards behave, while tags, notes,
// the reserved spine/** namespace and run branches stay gated with no
// policy at all.
func (h *Handler) prereceive(w http.ResponseWriter, r *http.Request, log *slog.Logger) (io.ReadCloser, bool) {
	// Buffer up to maxPushCommandBytes looking for the flush that
	// terminates the command section. Anything after the flush is
	// PACK data (or a push-options section, see below) and must
	// reach CGI unchanged on allow.
	cmdBuf, pendingBody, err := readPushCommands(r.Body, h.maxPushCommandBytes)
	if err != nil {
		log.Warn("pre-receive parse failed",
			"remote_addr", r.RemoteAddr, "error", err)
		h.writeReceivePackDenial(w,
			[]string{fmt.Sprintf("branch-protection: malformed push: %s", err)},
			nil)
		return nil, false
	}

	updates, caps, err := parseRefUpdates(bytes.NewReader(cmdBuf))
	if err != nil {
		log.Warn("pre-receive parse failed",
			"remote_addr", r.RemoteAddr, "error", err)
		h.writeReceivePackDenial(w,
			[]string{fmt.Sprintf("branch-protection: malformed push: %s", err)},
			nil)
		return nil, false
	}

	// Push-options section — only present when the client advertised
	// the `push-options` capability on the first ref-update frame
	// AND the server repo has `receive.advertisePushOptions=true`
	// (ensureReceivePackConfig sets that when receive-pack is on).
	// Options sit between the command flush and the PACK data; we
	// append the options block to cmdBuf so CGI still sees the
	// original stream on allow.
	pushOptions := map[string]string{}
	if _, ok := caps["push-options"]; ok {
		optsBuf, afterOpts, err := readPushCommands(pendingBody, h.maxPushCommandBytes)
		if err != nil {
			log.Warn("pre-receive push-options parse failed",
				"remote_addr", r.RemoteAddr, "error", err)
			h.writeReceivePackDenial(w,
				[]string{fmt.Sprintf("branch-protection: malformed push options: %s", err)},
				nil)
			return nil, false
		}
		opts, err := parsePushOptions(bytes.NewReader(optsBuf))
		if err != nil {
			log.Warn("pre-receive push-options parse failed",
				"remote_addr", r.RemoteAddr, "error", err)
			h.writeReceivePackDenial(w,
				[]string{fmt.Sprintf("branch-protection: malformed push options: %s", err)},
				nil)
			return nil, false
		}
		pushOptions = opts
		cmdBuf = append(cmdBuf, optsBuf...)
		pendingBody = afterOpts
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

	override := pushOptions["spine.override"] == "true"

	// Override-honored refs collected for governance-event emission
	// after the loop: ADR-009 §4 requires exactly one event per
	// overridden ref update (not per push), with rule_kinds carrying
	// the kinds that actually would have denied.
	type honored struct {
		update    RefUpdate
		ruleKinds []string
	}
	var honoredOverrides []honored
	// Resolve trace-id once at the top so the post-loop event
	// emitter can re-attach it when the request context came in
	// without gateway trace middleware (e.g. direct-embed tests).
	traceID := traceIDForPush(r.Context())

	// Ref-class admission (INIT-023 TASK-016) runs on EVERY ref update,
	// before and independently of the branch-protection policy (I1):
	// with a nil policy (early bootstrap, no store) the old loop let
	// every ref land, and even with a policy it skipped spine/** refs
	// and allowed every non-branch ref (ADR-009 §3/§6 scope branch
	// protection to user-authored branch rules, so nothing else gated
	// them). The run reader and the step-definition lookup travel as a
	// pair: the per-request pair the gateway attaches for the target
	// workspace wins, and the handler's Config pair is the fallback for
	// single mode and tests. Never mix the two halves, or a shared-mode
	// push could resolve its run in one workspace and its step
	// definition in another.
	admActor := domain.ActorFromContext(r.Context())
	runs, defs := h.runRefs, h.stepDefs
	if rr, ok := runRefsFromContext(r.Context()); ok {
		runs, defs = rr, stepDefsFromContext(r.Context())
	}
	adm := refadmit.NewEvaluator(runs).WithStepDefs(defs)
	repositoryID := repositoryIDFromContext(r.Context())
	admActorID := ""
	if admActor != nil {
		admActorID = admActor.ActorID
	}

	policyActor := actorForPush(r.Context())
	var messages []string
	for _, u := range updates {
		dec, err := adm.Decide(r.Context(), refadmit.Request{
			Ref:          u.Ref,
			Delete:       u.IsDelete(),
			Actor:        admActor,
			RepositoryID: repositoryID,
		})
		if err != nil {
			log.Error("ref admission evaluation failed, rejecting push",
				"ref", u.Ref, "actor_id", admActorID, "error", err)
			messages = append(messages, refadmit.EvaluationErrorMessage(u.Ref))
			continue
		}
		if !dec.Allow {
			// The precise reason goes to the log only; the wire text
			// (dec.Message) never names who holds the step. slog quotes
			// a value carrying a control byte (an invalid-class ref),
			// so none reaches the log raw.
			log.Warn("pre-receive ref admission denied",
				"ref", u.Ref, "class", dec.Class, "reason", dec.Reason,
				"actor_id", admActorID, "run_id", dec.RunID)
			messages = append(messages, dec.Message())
			continue
		}
		switch dec.Class {
		case refadmit.ClassRunBranch, refadmit.ClassDivergenceBranch,
			refadmit.ClassInitBranch, refadmit.ClassReserved:
			// Admitted spine/** ref (bound actor or operator). These
			// still never reach Policy.Evaluate, which keeps ADR-009
			// §3's "rules never name spine/*". Operator admissions are
			// audited by this log line only (owner question 3).
			log.Info("pre-receive admitted reserved ref",
				"ref", u.Ref, "class", dec.Class, "reason", dec.Reason,
				"actor_id", admActorID, "run_id", dec.RunID, "execution_id", dec.ExecutionID)
			continue
		case refadmit.ClassNonBranch:
			// Operator-only; logged for the same audit reason, then the
			// policy below sees it as before (it allows non-branch refs).
			log.Info("pre-receive admitted non-branch ref",
				"ref", u.Ref, "reason", dec.Reason, "actor_id", admActorID)
		}
		if policy == nil {
			continue
		}
		kind := branchprotect.OpDirectWrite
		if u.IsDelete() {
			kind = branchprotect.OpDelete
		}
		bpDec, reasons, err := policy.Evaluate(r.Context(), branchprotect.Request{
			Branch:   u.Ref,
			Kind:     kind,
			Actor:    policyActor,
			Override: override,
			TraceID:  traceID,
		})
		if err != nil {
			log.Error("branch-protection evaluation failed, rejecting push",
				"ref", u.Ref, "error", err)
			messages = append(messages,
				fmt.Sprintf("branch-protection: evaluation error on %s", trimRefsHeads(u.Ref)))
			continue
		}
		if bpDec == branchprotect.DecisionDeny {
			for _, rc := range reasons {
				messages = append(messages,
					fmt.Sprintf("branch-protection: %s", rc.Message))
			}
			continue
		}
		// Allowed. If the allow was because of an honored
		// override, record it for the audit event; a plain
		// "no matching rule" allow is not overridable and
		// emits nothing (per ADR-009 §4: avoid log pollution
		// from unused overrides).
		if kinds := overrideHonouredKinds(reasons); len(kinds) > 0 {
			honoredOverrides = append(honoredOverrides, honored{update: u, ruleKinds: kinds})
		}
	}
	if len(messages) > 0 {
		// Pre-receive stays all-or-nothing: one refused ref refuses the
		// whole push.
		log.Warn("pre-receive rejected push",
			"remote_addr", r.RemoteAddr,
			"refs", refPaths(updates),
			"reasons", messages,
		)
		h.writeReceivePackDenial(w, messages, updates)
		return nil, false
	}

	// Allow path: emit one governance event per overridden ref
	// update BEFORE handing off to CGI. This mirrors the API-path
	// wiring — the event is the authoritative audit record and must
	// be written whether or not CGI ultimately persists the ref
	// (CGI failure after this point still represents an override
	// attempt an operator consented to).
	//
	// Re-attach the push trace ID to the context the emitter sees:
	// traceIDForPush falls back to a freshly generated ID when the
	// request arrived without gateway trace middleware (unit tests,
	// future direct-embed deployments). Without this re-attach
	// the audit event emits with an empty trace_id, breaking the
	// correlation guarantee ADR-009 §4 expects.
	if emitter := eventsFromContext(r.Context()); emitter != nil && len(honoredOverrides) > 0 {
		emitCtx := r.Context()
		if observe.TraceID(emitCtx) == "" && traceID != "" {
			emitCtx = observe.WithTraceID(emitCtx, traceID)
		}
		for _, h := range honoredOverrides {
			emitOverrideEvent(emitCtx, emitter, h.update, h.ruleKinds)
		}
	}

	// All updates allowed. Stitch the buffered command bytes back in
	// front of the remaining body so CGI sees the original stream.
	combined := io.MultiReader(bytes.NewReader(cmdBuf), pendingBody)
	return &combinedBody{r: combined, closer: pendingBody}, true
}

// overrideHonouredKinds extracts the rule kinds from reasons marked
// ReasonOverrideHonoured. Empty result means the allow was not an
// override (e.g. no matching rule or governed merge), so no event
// should be emitted.
func overrideHonouredKinds(reasons []branchprotect.Reason) []string {
	var kinds []string
	for _, r := range reasons {
		if r.Code == branchprotect.ReasonOverrideHonoured {
			kinds = append(kinds, string(r.RuleKind))
		}
	}
	return kinds
}

// emitOverrideEvent emits a branch_protection.override governance
// event for one overridden ref update. The payload shape matches
// ADR-009 §4 and is a superset of the API-path event: pre_receive_ref
// carries the (old, new, ref) triple the policy evaluated, which is
// the Git-path's only record of what the client intended to push
// (pushes are not rewritten to add a commit trailer — ADR-009 §4).
func emitOverrideEvent(ctx context.Context, emitter event.Emitter, u RefUpdate, ruleKinds []string) {
	actorID := observe.ActorID(ctx)
	if a := domain.ActorFromContext(ctx); a != nil && a.ActorID != "" {
		actorID = a.ActorID
	}
	operation := string(branchprotect.OpDirectWrite)
	commitSHA := u.NewSHA
	if u.IsDelete() {
		operation = string(branchprotect.OpDelete)
		commitSHA = ""
	}
	eventID, _ := observe.GenerateTraceID()
	evt := domain.Event{
		EventID:   eventID,
		Type:      domain.EventBranchProtectionOverride,
		Timestamp: time.Now(),
		ActorID:   actorID,
		TraceID:   observe.TraceID(ctx),
		Payload: mustJSONPayload(map[string]any{
			"branch":     u.Ref,
			"rule_kinds": ruleKinds,
			"operation":  operation,
			"commit_sha": nullableSHA(commitSHA),
			"pre_receive_ref": map[string]any{
				"old_sha": u.OldSHA,
				"new_sha": u.NewSHA,
				"ref":     u.Ref,
			},
		}),
	}
	event.EmitLogged(ctx, emitter, evt)
}

// nullableSHA returns nil for an empty SHA so JSON marshals to null,
// matching the API-path event shape for override-deletes (ADR-009 §4).
func nullableSHA(sha string) any {
	if sha == "" {
		return nil
	}
	return sha
}

// mustJSONPayload marshals v to JSON. An encoding error here means a
// developer put a non-marshalable value in the payload map (maps are
// marshalable by definition, so the only realistic failure is a
// chan/func/unsupported-type bug); we log it but still emit an event
// with an empty payload rather than dropping the audit record.
func mustJSONPayload(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
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

// ensureReceivePackConfig aligns the repo's local git config with the
// handler's ReceivePackEnabled setting. Idempotent per repo path per
// handler lifetime — the values never flip at runtime, so caching by
// path is safe.
//
// Writes two keys:
//   - http.receivepack: exposes (or hides) git-http-backend's
//     receive-pack entry for this repo.
//   - receive.advertisePushOptions: makes the ref-advertisement include
//     the `push-options` capability. Without this, `git push -o ...`
//     on the client silently drops options and our pre-receive gate
//     never sees them — EPIC-004 TASK-003 depends on this being set.
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

	for _, kv := range [][2]string{
		{"http.receivepack", value},
		{"receive.advertisePushOptions", value},
	} {
		// G702 flags cmd.Dir as taint-tracked. repoPath is resolved
		// by the caller from the workspace registry (validated
		// workspace IDs), never from untrusted client input.
		cmd := exec.CommandContext(ctx, "git", "config", "--local", kv[0], kv[1]) //nolint:gosec // G702: repoPath is server-resolved from workspace registry, not request input
		cmd.Dir = repoPath
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git config %s=%s: %w", kv[0], kv[1], err)
		}
	}

	h.mu.Lock()
	h.ensuredRepos[repoPath] = true
	h.mu.Unlock()
	return nil
}

// isReadOnly returns true if the request is a read-only git operation.
func isReadOnly(r *http.Request) bool {
	path := r.URL.Path

	// GET /info/refs?service=git-upload-pack — ref advertisement for
	// clone/fetch — and the bare GET /info/refs of the dumb protocol.
	// Any other query is never a read (infoRefsServiceOf), including
	// `service=` with an empty value, which git-http-backend refuses.
	if isInfoRefs(r) {
		switch infoRefsServiceOf(r.URL.RawQuery) {
		case infoRefsDumb, infoRefsUploadPack:
			return true
		default:
			return false
		}
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

	if isInfoRefs(r) {
		// A non-canonical query fails closed to "push": git-http-backend
		// may read it as receive-pack (infoRefsServiceOf), so it needs a
		// bearer at the gateway, and ServeHTTP then refuses it.
		switch infoRefsServiceOf(r.URL.RawQuery) {
		case infoRefsReceivePack, infoRefsNonCanonical:
			return true
		default:
			return false
		}
	}
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/git-receive-pack") {
		return true
	}
	return false
}

// IsNonCanonicalInfoRefs reports whether r is a GET .../info/refs whose
// query is none of the three a git client sends (infoRefsNonCanonical):
// IsReceivePack counts it as a push, and ServeHTTP refuses it with 400.
// Exported so the gateway keeps such a request out of its
// push-disabled auth bypass (wave 3 review): it must meet the auth gate
// like a read, or an unauthenticated caller could tell an existing
// workspace (the handler's 400) from a missing one (the gate's uniform
// 401).
func IsNonCanonicalInfoRefs(r *http.Request) bool {
	return isInfoRefs(r) && infoRefsServiceOf(r.URL.RawQuery) == infoRefsNonCanonical
}

// isInfoRefs reports whether r is the ref advertisement request, the
// only git-http-backend route that reads the query string (its
// get_info_refs reads `service`; every other route ignores the query).
// HEAD, which git-http-backend serves as GET, is neither a read nor a
// push here, so ServeHTTP refuses it before CGI.
func isInfoRefs(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs")
}

// infoRefsService is the advertisement a GET .../info/refs query asks
// git-http-backend for (infoRefsServiceOf).
type infoRefsService int

const (
	// infoRefsDumb: the empty query — the dumb-protocol ref list, a read.
	infoRefsDumb infoRefsService = iota
	// infoRefsUploadPack: exactly `service=git-upload-pack` (clone, fetch).
	infoRefsUploadPack
	// infoRefsReceivePack: exactly `service=git-receive-pack` (push).
	infoRefsReceivePack
	// infoRefsNonCanonical: any other query. Never a read, always a
	// possible push, and refused by ServeHTTP.
	infoRefsNonCanonical
)

// infoRefsServiceOf classifies the raw query of a GET .../info/refs
// request by its exact bytes. Only the three queries a git client sends
// are recognised: the empty query (the dumb protocol), and
// `service=git-upload-pack` / `service=git-receive-pack` (remote-curl
// appends exactly `service=<name>` to the info/refs URL; protocol v2
// travels in the Git-Protocol header, not the query). Everything else is
// infoRefsNonCanonical.
//
// The classification has to be the one git-http-backend acts on, and
// git-http-backend parses QUERY_STRING — r.URL.RawQuery verbatim
// (net/http/cgi) — with its own parser (http-backend.c get_parameters,
// url.c url_decode_parameter_name / _value), not url.ParseQuery. The two
// disagree on inputs no client sends, in both directions:
//
//   - git keeps the LAST value of a repeated parameter, url.Values.Get
//     the FIRST: `service=git-upload-pack&service=git-receive-pack`;
//   - git ends a name at '&' or '=', consumes that byte, and then reads
//     the value up to the next '&', so a name without '=' takes the NEXT
//     segment as its value: `service&git-receive-pack` is
//     service=git-receive-pack to git, but service="" plus an unrelated
//     key to url.ParseQuery (and `x&service=git-receive-pack` is x=… to
//     git, with no service at all);
//   - git keeps ';', "%00" and an invalid escape literally where
//     url.ParseQuery drops the pair or decodes it.
//
// Each of the first two made a request that this package classified as
// a read reach git-http-backend as the receive-pack advertisement, on the
// gateway's token-less read paths (the trusted-CIDR runner bypass and
// SPINE_GIT_HTTP_DEV_NO_AUTH): INIT-023 TASK-015 review rounds 1 and 2.
// Comparing the exact bytes needs no model of either parser: each
// recognised query is one name, one '=' and one value, with no byte
// either parser treats specially, so both read it the same way, and
// anything else never reaches git-http-backend.
func infoRefsServiceOf(rawQuery string) infoRefsService {
	switch rawQuery {
	case "":
		return infoRefsDumb
	case "service=git-upload-pack":
		return infoRefsUploadPack
	case "service=git-receive-pack":
		return infoRefsReceivePack
	default:
		return infoRefsNonCanonical
	}
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

// eventsKey is the context key for a per-request event emitter. Used
// by the pre-receive gate to emit branch_protection.override
// governance events (ADR-009 §4). Like policyKey, this is per-request
// so shared-mode deployments audit to the right workspace stream.
type eventsKey struct{}

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

// WithEvents returns a copy of ctx carrying the given event emitter.
// The pre-receive gate reads this when it needs to emit
// branch_protection.override events on an operator-honored override.
func WithEvents(ctx context.Context, e event.Emitter) context.Context {
	return context.WithValue(ctx, eventsKey{}, e)
}

// eventsFromContext returns the per-request event emitter or nil if
// none is attached.
func eventsFromContext(ctx context.Context) event.Emitter {
	e, _ := ctx.Value(eventsKey{}).(event.Emitter)
	return e
}

// runRefsKey, stepDefsKey and repositoryIDKey are the context keys for
// the per-request ref-admission inputs (INIT-023 TASK-016). Like
// policyKey they are per-request, so a shared-mode deployment admits
// each push against the target workspace's own runs.
type (
	runRefsKey      struct{}
	stepDefsKey     struct{}
	repositoryIDKey struct{}
)

// runRefsValue wraps the per-request reader so that an attached nil
// reader is distinguishable from no attachment at all.
type runRefsValue struct{ rr refadmit.RunReader }

// WithRunRefs returns a copy of ctx carrying the run reader the
// pre-receive admission uses for run, plan and divergence refs. Once
// it is attached — even as nil — it wins over the handler's
// Config.RunRefs, and the step-definition lookup is taken from the
// context too (WithStepDefs), never from Config.StepDefs.
//
// Attaching nil is meaningful: it pins "no run store for this push"
// (a shared-mode workspace with no database yet), so run, plan and
// divergence refs are operator-only for it instead of being resolved
// against the handler's process-level fallback store, which would be
// another workspace's.
func WithRunRefs(ctx context.Context, rr refadmit.RunReader) context.Context {
	return context.WithValue(ctx, runRefsKey{}, runRefsValue{rr: rr})
}

// runRefsFromContext returns the per-request reader and whether one was
// attached (possibly nil).
func runRefsFromContext(ctx context.Context) (refadmit.RunReader, bool) {
	v, ok := ctx.Value(runRefsKey{}).(runRefsValue)
	return v.rr, ok
}

// WithStepDefs returns a copy of ctx carrying the step-definition
// lookup for the review freeze (TASK-016 owner question 4). It is read
// only together with a reader attached by WithRunRefs. A nil lookup is
// not attached.
func WithStepDefs(ctx context.Context, l refadmit.StepDefLookup) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, stepDefsKey{}, l)
}

func stepDefsFromContext(ctx context.Context) refadmit.StepDefLookup {
	l, _ := ctx.Value(stepDefsKey{}).(refadmit.StepDefLookup)
	return l
}

// WithRepositoryID returns a copy of ctx carrying the repository the
// git request targets, as the gateway parsed it from the URL ("" and
// "spine" both mean the workspace primary). The run-branch admission
// requires the bound execution to target the same repository.
func WithRepositoryID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, repositoryIDKey{}, id)
}

func repositoryIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(repositoryIDKey{}).(string)
	return id
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
