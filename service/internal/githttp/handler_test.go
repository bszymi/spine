package githttp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

func TestIsTrustedIP(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		TrustedCIDRs:    []string{"172.16.0.0/12", "10.0.0.0/8"},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"docker internal", "172.18.0.3:12345", true},
		{"private 10.x", "10.0.0.5:80", true},
		{"external", "203.0.113.1:80", false},
		{"localhost", "127.0.0.1:80", false},
		{"no port", "172.18.0.3", true},
		{"invalid", "not-an-ip", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.IsTrustedIP(tt.remoteAddr)
			if got != tt.want {
				t.Errorf("IsTrustedIP(%q) = %v, want %v", tt.remoteAddr, got, tt.want)
			}
		})
	}
}

func TestIsTrustedIP_NoCIDRs(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if h.IsTrustedIP("172.18.0.3:80") {
		t.Error("expected no IPs to be trusted when TrustedCIDRs is empty")
	}
}

func TestNewHandler_InvalidCIDR(t *testing.T) {
	_, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		TrustedCIDRs:    []string{"not-a-cidr"},
	})
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestNewHandler_Defaults(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	if cap(h.sem) != 5 {
		t.Errorf("expected default MaxConcurrent=5, got %d", cap(h.sem))
	}
	if h.opTimeout != 30*time.Second {
		t.Errorf("expected default OpTimeout=30s, got %v", h.opTimeout)
	}
}

func TestNewHandler_CustomConfig(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		MaxConcurrent:   10,
		OpTimeout:       60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if cap(h.sem) != 10 {
		t.Errorf("expected MaxConcurrent=10, got %d", cap(h.sem))
	}
	if h.opTimeout != 60*time.Second {
		t.Errorf("expected OpTimeout=60s, got %v", h.opTimeout)
	}
}

func TestIsReadOnly(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		query  string
		want   bool
	}{
		{"info refs upload-pack", "GET", "/info/refs", "service=git-upload-pack", true},
		{"info refs no service", "GET", "/info/refs", "", true},
		{"info refs receive-pack", "GET", "/info/refs", "service=git-receive-pack", false},
		{"upload-pack POST", "POST", "/git-upload-pack", "", true},
		{"receive-pack POST", "POST", "/git-receive-pack", "", false},
		{"GET objects", "GET", "/objects/pack/pack-abc.pack", "", true},
		{"GET HEAD", "GET", "/HEAD", "", true},
		{"random POST", "POST", "/something", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := tt.path
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest(tt.method, url, nil)
			got := isReadOnly(req)
			if got != tt.want {
				t.Errorf("isReadOnly(%s %s?%s) = %v, want %v", tt.method, tt.path, tt.query, got, tt.want)
			}
		})
	}
}

func TestIsReceivePack(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		query  string
		want   bool
	}{
		{"info refs receive-pack", "GET", "/info/refs", "service=git-receive-pack", true},
		{"info refs upload-pack", "GET", "/info/refs", "service=git-upload-pack", false},
		{"info refs no service", "GET", "/info/refs", "", false},
		{"POST receive-pack", "POST", "/git-receive-pack", "", true},
		{"POST upload-pack", "POST", "/git-upload-pack", "", false},
		{"GET HEAD", "GET", "/HEAD", "", false},
		{"GET objects", "GET", "/objects/pack/pack-abc.pack", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := tt.path
			if tt.query != "" {
				url += "?" + tt.query
			}
			req := httptest.NewRequest(tt.method, url, nil)
			got := IsReceivePack(req)
			if got != tt.want {
				t.Errorf("IsReceivePack(%s %s?%s) = %v, want %v",
					tt.method, tt.path, tt.query, got, tt.want)
			}
		})
	}
}

func TestServeHTTP_NoRepoPath(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/info/refs?service=git-upload-pack", nil)
	// No repo path in context.
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestServeHTTP_PushRejectedWhenFlagOff(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.ReceivePackEnabled() {
		t.Fatal("expected ReceivePackEnabled default to be false")
	}

	// Both push entry points (info/refs service and POST) must be
	// rejected when the flag is off. If only one is gated, a client
	// can still drive a push by skipping the advertisement.
	cases := []struct {
		name   string
		method string
		url    string
	}{
		{"info-refs", "GET", "/info/refs?service=git-receive-pack"},
		{"post-receive-pack", "POST", "/git-receive-pack"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.url, nil)
			ctx := WithRepoPath(req.Context(), "/tmp/repo")
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403 for push attempt, got %d", w.Code)
			}
			// Error message must name the flag so operators can
			// find it without grepping source.
			if !strings.Contains(w.Body.String(), "SPINE_GIT_RECEIVE_PACK_ENABLED") {
				t.Errorf("rejection message should name the config flag, got: %q",
					w.Body.String())
			}
		})
	}
}

// TestServeHTTP_PushPassesGateWhenFlagOn asserts that the receive-pack
// request gate no longer rejects a push when ReceivePackEnabled is true.
// The test cannot complete a full push because that would require a real
// bare repo and git-http-backend CGI; instead it sets ResolveRepoPath to
// a path that fails the ensureReceivePackConfig step, proving the gate
// itself passed and the request reached the config-align stage. The
// end-to-end push path is covered by the scenario test.
func TestServeHTTP_PushPassesGateWhenFlagOn(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		ReceivePackEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !h.ReceivePackEnabled() {
		t.Fatal("expected ReceivePackEnabled=true")
	}

	req := httptest.NewRequest("GET", "/info/refs?service=git-receive-pack", nil)
	// Deliberately non-existent path so ensureReceivePackConfig fails;
	// that 500 proves we passed the 403 push gate.
	ctx := WithRepoPath(req.Context(), "/nonexistent/definitely/not/a/repo")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatalf("expected push gate to pass when flag is on, got 403 %q", w.Body.String())
	}
}

// buildPushBody renders a receive-pack request body for the given ref
// updates followed by an empty PACK-section sentinel so tests can
// exercise the pre-receive parser end-to-end without constructing a
// real pack.
func buildPushBody(t *testing.T, updates ...RefUpdate) []byte {
	t.Helper()
	return buildPushBodyWithOptions(t, nil, updates...)
}

// buildPushBodyWithOptions is buildPushBody with a push-options
// section appended between the command-flush and the PACK. When
// options is non-nil (even empty), the first ref frame's capability
// string advertises `push-options` so the parser reads the section.
func buildPushBodyWithOptions(t *testing.T, options []string, updates ...RefUpdate) []byte {
	t.Helper()
	var body []byte
	caps := "report-status"
	if options != nil {
		caps += " push-options"
	}
	for i, u := range updates {
		line := u.OldSHA + " " + u.NewSHA + " " + u.Ref
		if i == 0 {
			line += "\x00" + caps
		}
		line += "\n"
		body = append(body, pktLine(line)...)
	}
	body = append(body, []byte(flushPkt)...)
	if options != nil {
		for _, opt := range options {
			body = append(body, pktLine(opt+"\n")...)
		}
		body = append(body, []byte(flushPkt)...)
	}
	body = append(body, []byte("PACK-stub")...)
	return body
}

// recordedEmitter captures emitted events so assertions can inspect
// the ADR-009 §4 payload shape without subscribing through the real
// event router.
type recordedEmitter struct{ events []domain.Event }

func (r *recordedEmitter) Emit(_ context.Context, e domain.Event) error {
	r.events = append(r.events, e)
	return nil
}

// staticRulePolicy returns a branchprotect.Policy that evaluates
// against the given rules. Used by handler tests that exercise the
// pre-receive gate without wiring a Store.
func staticRulePolicy(rules ...config.Rule) branchprotect.Policy {
	return branchprotect.New(branchprotect.StaticRules(rules))
}

func TestServeHTTP_PreReceiveRejectsDirectWriteOnProtectedBranch(t *testing.T) {
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := buildPushBody(t,
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/main"},
	)

	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	// Set up a valid repo so ensureReceivePackConfig does not 500
	// before the pre-receive check runs.
	repo := initBareIshRepo(t)
	ctx := WithRepoPath(req.Context(), repo)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with receive-pack-result denial body, got %d (body: %s)",
			w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-git-receive-pack-result" {
		t.Errorf("expected receive-pack-result content-type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "no-direct-write") {
		t.Errorf("rejection should name the rule kind, got: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "main") {
		t.Errorf("rejection should name the branch, got: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ng refs/heads/main pre-receive hook declined") {
		t.Errorf("rejection should include per-ref ng line, got: %s", w.Body.String())
	}
}

func TestServeHTTP_PreReceiveRejectsDeleteOnProtectedBranch(t *testing.T) {
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDelete},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("a", 40)
	body := buildPushBody(t,
		RefUpdate{OldSHA: old, NewSHA: zeroSHA, Ref: "refs/heads/main"},
	)

	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	req = req.WithContext(WithRepoPath(req.Context(), initBareIshRepo(t)))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with denial body, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no-delete") {
		t.Errorf("rejection should name no-delete kind, got: %s", w.Body.String())
	}
}

func TestServeHTTP_PreReceiveMixedRefsRejectsEntirePush(t *testing.T) {
	// Pre-receive semantics: any ref denied → whole push rejected.
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := buildPushBody(t,
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/feature"},
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/main"},
	)

	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	req = req.WithContext(WithRepoPath(req.Context(), initBareIshRepo(t)))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 denial, got %d", w.Code)
	}
	s := w.Body.String()
	// Every ref must be marked ng — pre-receive is all-or-nothing.
	if !strings.Contains(s, "ng refs/heads/feature") {
		t.Errorf("expected feature to be ng'd on mixed-push rejection, got: %s", s)
	}
	if !strings.Contains(s, "ng refs/heads/main") {
		t.Errorf("expected main to be ng'd, got: %s", s)
	}
}

// admPush posts a receive-pack body with the given ref updates through
// h, after letting prep decorate the request context (actor, run
// reader), and returns the response body. The gate's verdict is all a
// caller asserts: an admitted push then reaches CGI with fake object
// ids, which may fail there, but never with the pre-receive denial body.
func admPush(t *testing.T, h *Handler, prep func(context.Context) context.Context, updates ...RefUpdate) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(buildPushBody(t, updates...)))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	ctx := WithRepoPath(req.Context(), initBareIshRepo(t))
	if prep != nil {
		ctx = prep(ctx)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	return w.Body.String()
}

func withActor(a *domain.Actor) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context { return domain.WithActor(ctx, a) }
}

// countingPolicy wraps a policy and records every ref it evaluates, so
// a test can assert that a ref never reached Policy.Evaluate rather
// than infer it from the absence of a denial.
type countingPolicy struct {
	inner     branchprotect.Policy
	evaluated []string
}

func (c *countingPolicy) Evaluate(ctx context.Context, req branchprotect.Request) (branchprotect.Decision, []branchprotect.Reason, error) {
	c.evaluated = append(c.evaluated, req.Branch)
	return c.inner.Evaluate(ctx, req)
}

// TestServeHTTP_SpineRefsSkipPolicyButNotAdmission replaces
// TestServeHTTP_PreReceiveAllowsSpineRefsWithoutPolicy (INIT-023
// TASK-016 AC 7a). spine/** refs are still out of scope for
// user-authored branch-protection rules and never reach
// Policy.Evaluate (I3, ADR-009 §3), so an operator's push passes rules
// that really match every spine/** ref. They no longer skip the gate
// itself: the same push with no actor is refused by the ref-class
// admission.
//
// Review round 1: the AC's deny-all `*` rule matched nothing here
// (path.Match's `*` stops at '/'), so deleting the `continue` that
// keeps admitted spine/** refs away from the policy went unnoticed.
// The rules below match each ref (asserted directly against the
// policy), and a counting wrapper asserts the policy is never called.
// Mutation check: deleting that `continue` fails this test.
func TestServeHTTP_SpineRefsSkipPolicyButNotAdmission(t *testing.T) {
	deny := []config.RuleKind{config.KindNoDirectWrite, config.KindNoDelete}
	rules := staticRulePolicy(
		config.Rule{Branch: "main", Protections: deny},
		config.Rule{Branch: "spine", Protections: deny},
		config.Rule{Branch: "spine/*", Protections: deny},
		config.Rule{Branch: "spine/*/*", Protections: deny},
		config.Rule{Branch: "spine/*/*/*", Protections: deny},
		config.Rule{Branch: "spine/*/*/*/*", Protections: deny},
	)
	policy := &countingPolicy{inner: rules}
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	operator := withActor(&domain.Actor{ActorID: "op-1", Role: domain.RoleOperator})
	z, n := strings.Repeat("0", 40), strings.Repeat("a", 40)

	for _, ref := range []string{
		"refs/heads/spine/run/abc",                            // reserved (no run suffix)
		"refs/heads/spine/run/task-001-x-0a1b2c3d",            // run_branch
		"refs/heads/spine/plan/task-001-x-0a1b2c3d",           // run_branch (plan)
		"refs/heads/spine/run-0a1b2c3d/run-0a1b2c3d-div-d/b1", // divergence_branch
		"refs/heads/spine/init",                               // init_branch
		"refs/heads/spine",                                    // reserved
	} {
		// The rules really match the ref: evaluated directly, the
		// policy denies both a write and a delete.
		for _, kind := range []branchprotect.OperationKind{branchprotect.OpDirectWrite, branchprotect.OpDelete} {
			if dec, _, err := rules.Evaluate(context.Background(), branchprotect.Request{Branch: ref, Kind: kind}); err != nil || dec != branchprotect.DecisionDeny {
				t.Fatalf("fixture: rules on %s (%s) = %v %v, want deny", ref, kind, dec, err)
			}
		}
		for _, u := range []RefUpdate{
			{OldSHA: z, NewSHA: n, Ref: ref},
			{OldSHA: n, NewSHA: z, Ref: ref},
		} {
			policy.evaluated = nil
			body := admPush(t, h, operator, u)
			if strings.Contains(body, "pre-receive hook declined") || strings.Contains(body, "branch-protection:") {
				t.Errorf("operator push %s (delete=%v) must skip the policy, got denial: %s", ref, u.IsDelete(), body)
			}
			if len(policy.evaluated) != 0 {
				t.Errorf("operator push %s (delete=%v): Policy.Evaluate called for %v, want never", ref, u.IsDelete(), policy.evaluated)
			}
		}
	}

	// Control: an ordinary branch does reach the policy, so the counter
	// and the rules are live.
	policy.evaluated = nil
	body := admPush(t, h, operator, RefUpdate{OldSHA: z, NewSHA: n, Ref: "refs/heads/main"})
	if !strings.Contains(body, `branch-protection: rule "no-direct-write"`) || len(policy.evaluated) != 1 {
		t.Errorf("control: operator push to main: evaluated %v, body %s; want one evaluation and the no-direct-write denial", policy.evaluated, body)
	}

	u := RefUpdate{OldSHA: z, NewSHA: n, Ref: "refs/heads/spine/run/abc"}
	policy.evaluated = nil
	body = admPush(t, h, nil, u)
	if !strings.Contains(body, "ng refs/heads/spine/run/abc pre-receive hook declined") {
		t.Errorf("anonymous push of a spine/* ref must be ng'd, got: %s", body)
	}
	if !strings.Contains(body, "ref-admission: spine/run/abc is in the reserved spine namespace; operator role required") {
		t.Errorf("anonymous push should carry the ref-admission line, got: %s", body)
	}
	if strings.Contains(body, "branch-protection:") || len(policy.evaluated) != 0 {
		t.Errorf("a spine/* ref must never reach the policy, got evaluated %v, body: %s", policy.evaluated, body)
	}
}

// TestServeHTTP_RefAdmissionRunsWithoutPolicy pins TASK-016 I1: the
// admission runs with no branch-protection policy at all (early
// bootstrap, no store), where the old gate let every ref land.
// Mutation check: re-wrapping the admission loop in `if policy != nil`
// fails it.
func TestServeHTTP_RefAdmissionRunsWithoutPolicy(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	contributor := withActor(&domain.Actor{ActorID: "c-1", Role: domain.RoleContributor})
	z, n := strings.Repeat("0", 40), strings.Repeat("a", 40)

	body := admPush(t, h, contributor, RefUpdate{OldSHA: z, NewSHA: n, Ref: "refs/tags/x"})
	if !strings.Contains(body, "ng refs/tags/x pre-receive hook declined") ||
		!strings.Contains(body, "ref-admission: refs/tags/x is not a branch; only operators may push tags or other non-branch refs") {
		t.Errorf("contributor tag push with no policy must be refused by admission, got: %s", body)
	}

	// An ordinary branch still passes with no policy, as before.
	body = admPush(t, h, contributor, RefUpdate{OldSHA: z, NewSHA: n, Ref: "refs/heads/feature/x"})
	if strings.Contains(body, "pre-receive hook declined") {
		t.Errorf("ordinary branch with no policy must pass the gate, got: %s", body)
	}
}

// TestServeHTTP_RefAdmissionMixedPushAllOrNothing: one admission
// refusal refuses the whole push, so the ordinary branch riding with a
// contributor's tag is ng'd too (TASK-016 AC 7c).
func TestServeHTTP_RefAdmissionMixedPushAllOrNothing(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             branchprotect.NewPermissive(),
	})
	if err != nil {
		t.Fatal(err)
	}
	z, n := strings.Repeat("0", 40), strings.Repeat("a", 40)
	body := admPush(t, h, withActor(&domain.Actor{ActorID: "c-1", Role: domain.RoleContributor}),
		RefUpdate{OldSHA: z, NewSHA: n, Ref: "refs/heads/feature/x"},
		RefUpdate{OldSHA: z, NewSHA: n, Ref: "refs/tags/x"},
	)
	for _, ref := range []string{"refs/heads/feature/x", "refs/tags/x"} {
		if !strings.Contains(body, "ng "+ref+" pre-receive hook declined") {
			t.Errorf("%s must be ng'd on a mixed push, got: %s", ref, body)
		}
	}
}

// TestServeHTTP_RefAdmissionPerRequestReaderWins (TASK-016 AC 7d): the
// run reader the gateway attaches per request (WithRunRefs) wins over
// Config.RunRefs, and the step-definition lookup travels with it. With
// both readers nil a bound push is refused (fail closed).
func TestServeHTTP_RefAdmissionPerRequestReaderWins(t *testing.T) {
	const branch = "spine/run/task-001-x-0a1b2c3d"
	alice := &domain.Actor{ActorID: "alice", Role: domain.RoleContributor}
	u := RefUpdate{OldSHA: strings.Repeat("0", 40), NewSHA: strings.Repeat("a", 40), Ref: "refs/heads/" + branch}

	// Config reader: the step is held by bob, so alice is unbound there.
	configReader := boundRun("bob", "task-001-x", "0a1b2c3d")
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             branchprotect.NewPermissive(),
		RunRefs:            configReader,
		StepDefs:           admStepDefs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctxReader := boundRun("alice", "task-001-x", "0a1b2c3d")
	body := admPush(t, h, func(ctx context.Context) context.Context {
		ctx = domain.WithActor(ctx, alice)
		ctx = WithRunRefs(ctx, ctxReader)
		return WithStepDefs(ctx, admStepDefs{})
	}, u)
	if strings.Contains(body, "pre-receive hook declined") {
		t.Errorf("alice is bound in the per-request reader; push must pass the gate, got: %s", body)
	}
	if ctxReader.calls == 0 || configReader.calls != 0 {
		t.Errorf("reader calls: per-request=%d config=%d, want the per-request reader only", ctxReader.calls, configReader.calls)
	}

	// The halves never mix: a per-request reader with no per-request
	// lookup does not borrow Config.StepDefs, so the push is refused.
	body = admPush(t, h, func(ctx context.Context) context.Context {
		return WithRunRefs(domain.WithActor(ctx, alice), boundRun("alice", "task-001-x", "0a1b2c3d"))
	}, u)
	if !strings.Contains(body, "ref-admission: run-branch admission is not configured on this server") {
		t.Errorf("per-request reader without a per-request lookup must fail closed, got: %s", body)
	}

	// Config reader only: alice is unbound there.
	body = admPush(t, h, withActor(alice), u)
	if !strings.Contains(body, "ref-admission: "+branch+" is a run branch") {
		t.Errorf("config reader binds bob, so alice must be refused, got: %s", body)
	}

	// A per-request nil reader is a decision, not an absence: the
	// gateway attaches it for a workspace with no store, and the
	// handler must not fall back to its Config reader (in shared mode,
	// another workspace's store) even when that reader would bind.
	aliceConfig, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             branchprotect.NewPermissive(),
		RunRefs:            boundRun("alice", "task-001-x", "0a1b2c3d"),
		StepDefs:           admStepDefs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	body = admPush(t, aliceConfig, func(ctx context.Context) context.Context {
		ctx = WithRunRefs(domain.WithActor(ctx, alice), nil)
		return WithStepDefs(ctx, nil)
	}, u)
	if !strings.Contains(body, "ref-admission: run-branch admission is not configured on this server") {
		t.Errorf("per-request nil reader must not fall back to the config reader, got: %s", body)
	}

	// Both readers nil: refused.
	bare, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             branchprotect.NewPermissive(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body = admPush(t, bare, withActor(alice), u)
	if !strings.Contains(body, "ng refs/heads/"+branch+" pre-receive hook declined") ||
		!strings.Contains(body, "ref-admission: run-branch admission is not configured on this server; operator role required for "+branch) {
		t.Errorf("with no reader the bound push must be refused, got: %s", body)
	}
}

func TestServeHTTP_PreReceiveOperatorOverrideHonoured(t *testing.T) {
	// Operator role with Override=true allows an otherwise-denied
	// write. For the push path we propagate this through the
	// handler from the actor context. This asserts that the actor
	// threaded from gateway auth reaches the policy evaluator.
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := buildPushBody(t,
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/main"},
	)

	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	ctx := WithRepoPath(req.Context(), initBareIshRepo(t))
	// Attach an operator actor. Override is carried on Request in
	// TASK-003 — for this task we only verify that the actor
	// reaches the evaluator (a contributor would be denied, an
	// operator without Override would still be denied — this test
	// asserts the plumbing, not the override itself).
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "op-1", Role: domain.RoleOperator})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	// Without a Request.Override flag (TASK-003) an operator direct
	// write is still denied by the rule. The check here is that
	// the body mentions the actor-agnostic rule denial rather than
	// erroring out — i.e. policy was actually called.
	if !strings.Contains(w.Body.String(), "no-direct-write") {
		t.Errorf("expected no-direct-write denial when operator pushes without override, got: %s",
			w.Body.String())
	}
}

// TestServeHTTP_OperatorOverrideAllowsAndEmitsEvent covers TASK-003's
// golden path: operator pushes with `-o spine.override=true` to a
// no-direct-write branch, the gate allows the push, and exactly one
// branch_protection.override event is emitted with the ADR-009 §4
// payload shape (including pre_receive_ref).
func TestServeHTTP_OperatorOverrideAllowsAndEmitsEvent(t *testing.T) {
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := buildPushBodyWithOptions(t,
		[]string{"spine.override=true"},
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/main"},
	)

	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	ctx := WithRepoPath(req.Context(), initBareIshRepo(t))
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "op-1", Role: domain.RoleOperator})
	emitter := &recordedEmitter{}
	ctx = WithEvents(ctx, emitter)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	// The push must have passed the gate — the denial body marker
	// must be absent. (CGI runs after; we do not assert on its
	// output because it needs a real bare repo.)
	if strings.Contains(w.Body.String(), "pre-receive hook declined") {
		t.Fatalf("expected operator override to allow push, got denial body: %s", w.Body.String())
	}

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 branch_protection.override event, got %d", len(emitter.events))
	}
	ev := emitter.events[0]
	if ev.Type != domain.EventBranchProtectionOverride {
		t.Errorf("expected type=%s, got %s", domain.EventBranchProtectionOverride, ev.Type)
	}
	if ev.ActorID != "op-1" {
		t.Errorf("expected actor_id=op-1, got %q", ev.ActorID)
	}
	// Payload must carry the ADR-009 §4 fields, including the
	// Git-path-specific pre_receive_ref block.
	for _, substr := range []string{
		`"branch":"refs/heads/main"`,
		`"rule_kinds":["no-direct-write"]`,
		`"operation":"direct_write"`,
		`"commit_sha":"` + new + `"`,
		`"pre_receive_ref"`,
		`"old_sha":"` + old + `"`,
		`"new_sha":"` + new + `"`,
		`"ref":"refs/heads/main"`,
	} {
		if !strings.Contains(string(ev.Payload), substr) {
			t.Errorf("payload missing %q; got:\n%s", substr, ev.Payload)
		}
	}
}

// TestServeHTTP_OverrideEventCarriesTraceID asserts that the emitted
// branch_protection.override event always has a non-empty trace_id,
// even when the request arrived without the gateway's trace
// middleware (direct-embed tests, future non-gateway deployments).
// Without the fallback the audit record loses the correlation key
// ADR-009 §4 requires.
func TestServeHTTP_OverrideEventCarriesTraceID(t *testing.T) {
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := buildPushBodyWithOptions(t,
		[]string{"spine.override=true"},
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/main"},
	)

	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	// No trace middleware — context has no trace-id.
	ctx := WithRepoPath(req.Context(), initBareIshRepo(t))
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "op-trace", Role: domain.RoleOperator})
	emitter := &recordedEmitter{}
	ctx = WithEvents(ctx, emitter)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 override event, got %d", len(emitter.events))
	}
	if emitter.events[0].TraceID == "" {
		t.Errorf("override event must have a fallback trace_id, got empty")
	}
}

// TestServeHTTP_ContributorOverrideRejected covers TASK-003's
// role-gate: a contributor pushes with `-o spine.override=true` and
// the policy denies with ReasonOverrideNotAuthorised, which the gate
// surfaces on the wire and emits NO event (no override was honored).
func TestServeHTTP_ContributorOverrideRejected(t *testing.T) {
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := buildPushBodyWithOptions(t,
		[]string{"spine.override=true"},
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/main"},
	)
	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	ctx := WithRepoPath(req.Context(), initBareIshRepo(t))
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "c-1", Role: domain.RoleContributor})
	emitter := &recordedEmitter{}
	ctx = WithEvents(ctx, emitter)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	// The gate must have denied with the override-not-authorised
	// reason on the wire.
	if !strings.Contains(w.Body.String(), "override") {
		t.Errorf("expected denial mentioning override, got: %s", w.Body.String())
	}
	if len(emitter.events) != 0 {
		t.Errorf("expected no override event for contributor, got %d", len(emitter.events))
	}
}

// TestServeHTTP_UnusedOverrideEmitsNoEvent asserts that setting
// `-o spine.override=true` on a push to an unprotected branch does
// not emit an override event. ADR-009 §4: unused overrides are
// silent to avoid log pollution.
func TestServeHTTP_UnusedOverrideEmitsNoEvent(t *testing.T) {
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	// Push to "feature" — no rule matches. Override is set anyway.
	body := buildPushBodyWithOptions(t,
		[]string{"spine.override=true"},
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/feature"},
	)
	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	ctx := WithRepoPath(req.Context(), initBareIshRepo(t))
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "op-2", Role: domain.RoleOperator})
	emitter := &recordedEmitter{}
	ctx = WithEvents(ctx, emitter)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "pre-receive hook declined") {
		t.Fatalf("expected push to be allowed on unprotected branch, got denial: %s", w.Body.String())
	}
	if len(emitter.events) != 0 {
		t.Errorf("unused override must not emit events, got %d", len(emitter.events))
	}
}

// TestServeHTTP_MixedOverridePushEmitsEventOnlyForProtectedRef covers
// the ADR-009 §4 requirement that the event is per-ref-update, not
// per-push: a single push updating an unprotected ref + a protected
// ref (with override) emits exactly one event — for the protected
// ref only.
func TestServeHTTP_MixedOverridePushEmitsEventOnlyForProtectedRef(t *testing.T) {
	policy := staticRulePolicy(config.Rule{
		Branch:      "main",
		Protections: []config.RuleKind{config.KindNoDirectWrite},
	})
	h, err := NewHandler(Config{
		ResolveRepoPath:    func(_ context.Context, _ string) (string, error) { return t.TempDir(), nil },
		ReceivePackEnabled: true,
		Policy:             policy,
	})
	if err != nil {
		t.Fatal(err)
	}

	old := strings.Repeat("0", 40)
	new := strings.Repeat("a", 40)
	body := buildPushBodyWithOptions(t,
		[]string{"spine.override=true"},
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/feature"},
		RefUpdate{OldSHA: old, NewSHA: new, Ref: "refs/heads/main"},
	)
	req := httptest.NewRequest("POST", "/git-receive-pack", bytes.NewReader(body))
	ctx := WithRepoPath(req.Context(), initBareIshRepo(t))
	ctx = domain.WithActor(ctx, &domain.Actor{ActorID: "op-3", Role: domain.RoleOperator})
	emitter := &recordedEmitter{}
	ctx = WithEvents(ctx, emitter)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "pre-receive hook declined") {
		t.Fatalf("expected mixed push to be allowed, got denial: %s", w.Body.String())
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected exactly 1 event for the protected ref only, got %d", len(emitter.events))
	}
	if !strings.Contains(string(emitter.events[0].Payload), `"branch":"refs/heads/main"`) {
		t.Errorf("expected event for refs/heads/main, got payload: %s", emitter.events[0].Payload)
	}
}

// initBareIshRepo creates a tempdir that looks enough like a Git repo
// for `git config --local` to succeed. Tests that exercise the
// pre-receive gate but short-circuit before CGI do not need a full
// bare repo — they just need ensureReceivePackConfig not to 500.
func initBareIshRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return dir
}

func TestServeHTTP_ConcurrencyLimit(t *testing.T) {
	h, err := NewHandler(Config{
		ResolveRepoPath: func(_ context.Context, _ string) (string, error) { return "/tmp", nil },
		MaxConcurrent:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fill the semaphore.
	h.sem <- struct{}{}

	req := httptest.NewRequest("GET", "/info/refs?service=git-upload-pack", nil)
	ctx := WithRepoPath(req.Context(), "/tmp/repo")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when concurrency limit reached, got %d", w.Code)
	}

	// Drain the semaphore.
	<-h.sem
}

func TestWithRepoPath_RoundTrip(t *testing.T) {
	ctx := WithRepoPath(context.Background(), "/var/spine/repos/ws-1")
	got := repoPathFromContext(ctx)
	if got != "/var/spine/repos/ws-1" {
		t.Errorf("expected /var/spine/repos/ws-1, got %q", got)
	}
}

func TestRepoPathFromContext_Empty(t *testing.T) {
	got := repoPathFromContext(context.Background())
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}
