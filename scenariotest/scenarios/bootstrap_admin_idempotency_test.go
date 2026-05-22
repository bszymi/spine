//go:build scenario

package scenarios_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/service/internal/gateway"
	scenarioEngine "github.com/bszymi/spine/scenariotest/engine"
	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/service/internal/workspace"
)

// State keys for the bootstrap-admin scenario. The bundle in
// stateBootstrapEnv carries the gateway server, log buffer, and bearer
// strings; subsequent steps thread it through scenario state instead of
// closing over a per-test struct so each step's helper stays
// self-contained and re-usable.
const (
	stateBootstrapEnv      = "bootstrap_admin_env"
	bootstrapBearerInitial = "smp-bootstrap-bearer-foo"
	bootstrapBearerRotated = "smp-bootstrap-bearer-bar"
)

// bootstrapAdminEnv bundles the wiring the bootstrap-admin scenario
// drives. The gateway server is anchored to ParentT so it survives the
// per-step subtest scope; logBuf is a synchronized buffer behind the
// global slog handler so cross-step log inspection is race-free.
type bootstrapAdminEnv struct {
	Server  *httptest.Server
	BaseURL string
	LogBuf  *syncBuffer
}

// syncBuffer is a goroutine-safe wrapper around bytes.Buffer. The
// global slog handler we install during the test is shared across the
// scenario steps and the gateway server's own log emissions, so writes
// can race with the test goroutine reading the buffer for assertions.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
}

// TestBootstrapInternalAdmin_IdempotencyAndRotation locks the v0.x
// platform-binding bootstrap contract from internal/auth/bootstrap.go.
// The bootstrap is invoked from cmd/spine/cmd_serve.go's
// newPooledWorkspaceBuilder on every workspace activation (re-resolve
// after idle eviction included), so its three-state behavior is the
// regression bait this scenario locks in:
//
//  1. First call with token=foo creates one auth.actors row + one
//     auth.tokens row; the bearer authenticates over HTTP.
//  2. Re-call with token=foo is a no-op — row counts unchanged, the
//     "internal admin token already configured" DEBUG line is emitted,
//     same bearer still authenticates.
//  3. Re-call with rotated token=bar inserts a second auth.tokens row
//     bound to the same actor; new bearer authenticates immediately
//     and (per the v0.x deferral on rotation cleanup documented in
//     bootstrap.go:46-47 + INIT-020/EPIC-002/TASK-001) the OLD bearer
//     also continues to authenticate. If a future task wires rotation
//     cleanup the old-bearer expectation flips and this scenario must
//     update in lockstep.
//
// AC mapping notes: the task body's "ON CONFLICT (token_hash) DO
// UPDATE" wording is reconciled here against what actually ships —
// internal/store/postgres_tokens.go::CreateToken is a plain INSERT and
// the idempotency lives at the application layer, in
// upsertInternalAdminToken's GetActorByTokenHash early-return. The
// equivalent mutation target is "remove the early-return for the
// matching actor in upsertInternalAdminToken" — the second bootstrap
// then re-attempts CreateToken with the same deterministic token_id
// (and same UNIQUE token_hash), failing the third step's bootstrap
// call. Verified manually before checking in: dropping the
// `actor != nil && actor.ActorID == InternalAdminActorID` early-return
// fails the second-bootstrap step here.
func TestBootstrapInternalAdmin_IdempotencyAndRotation(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "bootstrap-admin-idempotency-and-rotation",
		Description: "Locks BootstrapInternalAdmin's three-state contract: first-create, no-op re-resolve, dual-bearer rotation.",
		Steps: []scenarioEngine.Step{
			setupBootstrapAdminEnv(),
			firstBootstrap(),
			assertSingleActorAndTokenAfterFirstBootstrap(),
			driveAuthenticatedRequestExpectingOK(bootstrapBearerInitial, "phase=first"),
			secondBootstrapNoOp(),
			assertNoRowDuplicationAfterSecondBootstrap(),
			assertDebugIdempotentLogPresent(),
			driveAuthenticatedRequestExpectingOK(bootstrapBearerInitial, "phase=second"),
			rotationBootstrap(),
			assertRotationRowCounts(),
			driveAuthenticatedRequestExpectingOK(bootstrapBearerRotated, "phase=after-rotation-new-bearer"),
			driveAuthenticatedRequestExpectingOK(bootstrapBearerInitial, "phase=after-rotation-old-bearer"),
		},
	})
}

// setupBootstrapAdminEnv wires the minimal HTTP gateway needed to drive
// bearer-authenticated requests against the runtime store, plus a
// global slog handler bound to a shared buffer so subsequent steps can
// inspect log lines emitted by auth.BootstrapInternalAdmin.
//
// Anchoring the slog override and the httptest.Server cleanup to
// sc.ParentT (rather than sc.T, which is the per-step subtest) keeps
// the gateway and the log buffer alive across the rest of the steps.
// The buffer is reset at the start of each phase that asserts on its
// contents so a previous phase's emissions cannot satisfy the
// assertion accidentally.
func setupBootstrapAdminEnv() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-bootstrap-admin-env",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			authSvc := auth.NewService(sc.Runtime.Store)

			srv := gateway.NewServer(":0", gateway.ServerConfig{
				Store: sc.Runtime.Store,
				Auth:  authSvc,
			})
			ts := httptest.NewServer(srv.Handler())
			sc.ParentT.Cleanup(ts.Close)

			logBuf := &syncBuffer{}
			prevSlog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			})))
			sc.ParentT.Cleanup(func() { slog.SetDefault(prevSlog) })

			sc.Set(stateBootstrapEnv, &bootstrapAdminEnv{
				Server:  ts,
				BaseURL: ts.URL,
				LogBuf:  logBuf,
			})
			return nil
		},
	}
}

// firstBootstrap drives auth.BootstrapInternalAdmin with the initial
// bearer foo. The log buffer is reset right before the call so the
// second-bootstrap step's debug-line assertion is not satisfied by
// info-level emissions from this first call.
func firstBootstrap() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "first-bootstrap-with-foo",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateBootstrapEnv).(*bootstrapAdminEnv)
			env.LogBuf.Reset()
			err := auth.BootstrapInternalAdmin(sc.Ctx, sc.Runtime.Store, auth.BootstrapAdminConfig{
				Token: bootstrapBearerInitial,
			})
			if err != nil {
				return fmt.Errorf("first bootstrap: %w", err)
			}
			return nil
		},
	}
}

// assertSingleActorAndTokenAfterFirstBootstrap pins the post-first-
// bootstrap state: one smp-admin actor row exists with the canonical
// shape, exactly one token row exists for that actor, and its hash
// matches HashToken(initial bearer). The hash check is what proves the
// bootstrap actually persisted the right secret rather than a
// placeholder — without it a regression that swapped the hash for an
// empty string would still satisfy the row-count assertions.
func assertSingleActorAndTokenAfterFirstBootstrap() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-actor-and-token-after-first-bootstrap",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			actor, err := sc.Runtime.Store.GetActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("GetActor(%s): %w", auth.InternalAdminActorID, err)
			}
			if actor.Role != domain.RoleAdmin || actor.Status != domain.ActorStatusActive {
				return fmt.Errorf("smp-admin actor shape: role=%q status=%q, want %q/%q",
					actor.Role, actor.Status, domain.RoleAdmin, domain.ActorStatusActive)
			}

			tokens, err := sc.Runtime.Store.ListTokensByActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("ListTokensByActor: %w", err)
			}
			if len(tokens) != 1 {
				return fmt.Errorf("token count after first bootstrap: got %d, want 1", len(tokens))
			}

			// The hash itself is not surfaced through ListTokensByActor;
			// instead, look up by hash and confirm the bearer maps to the
			// expected actor. A regression that persisted a different
			// hash would surface as an unauthorized lookup here.
			expectHash := auth.HashToken(bootstrapBearerInitial)
			gotActor, gotToken, err := sc.Runtime.Store.GetActorByTokenHash(sc.Ctx, expectHash)
			if err != nil {
				return fmt.Errorf("GetActorByTokenHash(initial): %w", err)
			}
			if gotActor.ActorID != auth.InternalAdminActorID {
				return fmt.Errorf("token hash bound to %q, want %q",
					gotActor.ActorID, auth.InternalAdminActorID)
			}
			// The deterministic token_id is bootstrap-<first 12 hex of hash>;
			// pin it so a regression that randomized the ID (and thereby
			// broke the second-bootstrap idempotency check) would surface.
			wantTokenID := "bootstrap-" + expectHash[:12]
			if gotToken.TokenID != wantTokenID {
				return fmt.Errorf("token_id: got %q, want %q", gotToken.TokenID, wantTokenID)
			}
			return nil
		},
	}
}

// secondBootstrapNoOp re-invokes BootstrapInternalAdmin with the same
// initial bearer. With the application-layer idempotency in
// upsertInternalAdminToken intact, this is a pure no-op: no new rows,
// no error, and a DEBUG-level "internal admin token already
// configured" line. The log buffer reset here scopes the next step's
// log assertion to this call only.
func secondBootstrapNoOp() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "second-bootstrap-noop",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateBootstrapEnv).(*bootstrapAdminEnv)
			env.LogBuf.Reset()
			err := auth.BootstrapInternalAdmin(sc.Ctx, sc.Runtime.Store, auth.BootstrapAdminConfig{
				Token: bootstrapBearerInitial,
			})
			if err != nil {
				return fmt.Errorf("second bootstrap: %w", err)
			}
			return nil
		},
	}
}

// assertNoRowDuplicationAfterSecondBootstrap is the AC mutation target.
// If upsertInternalAdminToken's GetActorByTokenHash early-return is
// removed, the second bootstrap re-attempts CreateToken with the same
// deterministic token_id and same UNIQUE token_hash, failing on the
// PK/unique violation — secondBootstrapNoOp would surface the error.
// Even if a regression silently swallowed the error, this step would
// still fire because the row count would change (or the actor row
// would be re-stamped). Together they cover both the early-return-
// removed and the early-return-converted-to-INSERT-OR-UPDATE flavors.
func assertNoRowDuplicationAfterSecondBootstrap() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-no-row-duplication-after-second-bootstrap",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			tokens, err := sc.Runtime.Store.ListTokensByActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("ListTokensByActor: %w", err)
			}
			if len(tokens) != 1 {
				return fmt.Errorf("token count after second bootstrap: got %d, want 1 (idempotency broken)",
					len(tokens))
			}
			return nil
		},
	}
}

// assertDebugIdempotentLogPresent inspects the log buffer for the
// DEBUG line bootstrap.go emits when the token row already matches the
// desired shape. The substring is the operator-visible signal that
// re-resolve was a clean no-op rather than a silent rewrite.
//
// The level check is anchored to the same log record as the token
// message: a global "any line at level=DEBUG" check would be satisfied
// by the sibling actor-already-configured DEBUG line emitted earlier in
// the same bootstrap call, so a regression that demoted only the token
// line to INFO would still pass. Splitting the buffer per-record and
// requiring level=DEBUG on the matching record closes that gap.
func assertDebugIdempotentLogPresent() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-debug-idempotent-log-present",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateBootstrapEnv).(*bootstrapAdminEnv)
			logged := env.LogBuf.String()
			const wantMsg = "internal admin token already configured"
			var matched string
			for _, line := range strings.Split(strings.TrimRight(logged, "\n"), "\n") {
				if strings.Contains(line, wantMsg) {
					matched = line
					break
				}
			}
			if matched == "" {
				return fmt.Errorf("expected log line containing %q in second-bootstrap output; full log:\n%s",
					wantMsg, logged)
			}
			if !strings.Contains(matched, "level=DEBUG") {
				return fmt.Errorf("expected level=DEBUG on token-already-configured record; got line:\n%s\nfull log:\n%s",
					matched, logged)
			}
			return nil
		},
	}
}

// rotationBootstrap drives the third bootstrap with the rotated bearer.
// Per the v0.x contract the old auth.tokens row is left in place; the
// new bearer's hash is inserted as a second row bound to the same
// actor. The log buffer is reset here so a future rotation-cleanup
// task that adds an info-level "old token revoked" line can be pinned
// in a follow-up scenario without colliding with this step's noise.
func rotationBootstrap() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "rotation-bootstrap-with-bar",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateBootstrapEnv).(*bootstrapAdminEnv)
			env.LogBuf.Reset()
			err := auth.BootstrapInternalAdmin(sc.Ctx, sc.Runtime.Store, auth.BootstrapAdminConfig{
				Token: bootstrapBearerRotated,
			})
			if err != nil {
				return fmt.Errorf("rotation bootstrap: %w", err)
			}
			return nil
		},
	}
}

// assertRotationRowCounts pins the dual-bearer state: still one
// smp-admin actor, but now two auth.tokens rows — one bound to the
// initial-bearer hash, one bound to the rotated-bearer hash. Both
// rows reference smp-admin. A regression that quietly dropped or
// rebound the old row would surface as a failed lookup against the
// initial-bearer hash here, before the dual-bearer auth check below
// even runs.
func assertRotationRowCounts() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-rotation-row-counts",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			tokens, err := sc.Runtime.Store.ListTokensByActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("ListTokensByActor: %w", err)
			}
			if len(tokens) != 2 {
				return fmt.Errorf("token count after rotation: got %d, want 2 (old + new)", len(tokens))
			}

			oldHash := auth.HashToken(bootstrapBearerInitial)
			newHash := auth.HashToken(bootstrapBearerRotated)
			oldActor, _, err := sc.Runtime.Store.GetActorByTokenHash(sc.Ctx, oldHash)
			if err != nil {
				return fmt.Errorf("GetActorByTokenHash(old): %w", err)
			}
			if oldActor.ActorID != auth.InternalAdminActorID {
				return fmt.Errorf("old hash bound to %q, want %q",
					oldActor.ActorID, auth.InternalAdminActorID)
			}
			newActor, _, err := sc.Runtime.Store.GetActorByTokenHash(sc.Ctx, newHash)
			if err != nil {
				return fmt.Errorf("GetActorByTokenHash(new): %w", err)
			}
			if newActor.ActorID != auth.InternalAdminActorID {
				return fmt.Errorf("new hash bound to %q, want %q",
					newActor.ActorID, auth.InternalAdminActorID)
			}
			return nil
		},
	}
}

// driveAuthenticatedRequestExpectingOK issues a GET against an
// auth-protected gateway endpoint with the given bearer and asserts a
// 200. Used three times across the scenario:
//
//   - Phase 1: foo authenticates after first bootstrap.
//   - Phase 2: foo still authenticates after the no-op re-bootstrap.
//   - Phase 3: bar authenticates after rotation, AND foo continues to
//     authenticate (the v0.x dual-bearer contract).
//
// /api/v1/system/metrics is the smallest auth-protected endpoint that
// only requires Operator (smp-admin is RoleAdmin, which satisfies it)
// and returns 200 OK without needing a workspace resolver, orchestrator,
// or any other dependency the scenario does not configure. The
// response body itself is not asserted — only the HTTP status — so the
// test does not depend on the prometheus exposition format staying
// stable.
func driveAuthenticatedRequestExpectingOK(bearer, phase string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "drive-authenticated-request-" + phase,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(stateBootstrapEnv).(*bootstrapAdminEnv)
			req, err := http.NewRequestWithContext(sc.Ctx, http.MethodGet,
				env.BaseURL+"/api/v1/system/metrics", http.NoBody)
			if err != nil {
				return fmt.Errorf("build request (%s): %w", phase, err)
			}
			req.Header.Set("Authorization", "Bearer "+bearer)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("issue request (%s): %w", phase, err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("phase %s: bearer=%s got status %d, want 200; body=%s",
					phase, redactBearer(bearer), resp.StatusCode, string(body))
			}
			return nil
		},
	}
}

// redactBearer trims the bearer for error messages so the failure
// path of a real-deployment-shaped scenario does not echo a full
// secret to the test log. The local test bearer is synthetic but the
// helper stays disciplined.
func redactBearer(bearer string) string {
	if len(bearer) <= 4 {
		return "[redacted]"
	}
	return bearer[:4] + "..."
}

// ─── Pooled-builder bootstrap scenario (INIT-022 EPIC-001 TASK-035) ───
//
// The test above drives BootstrapInternalAdmin via direct calls and
// asserts the contract through sc.Runtime.Store + an unpooled gateway.
// That keeps coverage of the single-workspace fallback (non-platform
// mode) but does NOT exercise the platform-binding wiring this
// bootstrap exists for: in production, cmd/spine's
// newPooledWorkspaceBuilder invokes BootstrapInternalAdmin from inside
// the workspace.ServicePool's ServiceSetBuilder on every workspace
// activation, and the gateway resolves auth through the resulting
// ServiceSet rather than through process-level Store/Auth handles.
//
// TestBootstrapInternalAdmin_PooledBuilderIdempotencyAndRotation
// covers the same three-phase contract (first-create, idempotent
// re-resolve, rotation insertion) but through that production-shaped
// chain end-to-end:
//
//   1. workspace.ServicePool with a builder that calls
//      auth.BootstrapInternalAdmin against the workspace's own
//      ServiceSet.Store (a per-workspace pgx pool over the same test
//      Postgres instance the harness uses).
//   2. Gateway wired with WorkspaceResolver + ServicePool and NO
//      direct Store/Auth handles, so the auth middleware can only
//      succeed through the pooled ServiceSet's auth service.
//   3. Idle "reload" simulated by pool.Evict between requests, so the
//      next request fans through workspace.Resolve + ServiceSet
//      construction + builder invocation again — the same chain a
//      real workspace's idle eviction would re-run.
//
// AC bait (TASK-035 AC #1): reverting any link in that chain breaks
// the HTTP authentication assertion. Verified manually for two
// links before checking in:
//
//   - Replacing the builder's BootstrapInternalAdmin call with a
//     no-op fails phase=first with a 401 (no smp-admin actor in
//     ss.Store → bearer rejected).
//   - Pointing the builder at a token of "" (env-derived plumbing
//     reverted) also fails phase=first with a 401 (BootstrapInternalAdmin
//     no-ops on empty token).
func TestBootstrapInternalAdmin_PooledBuilderIdempotencyAndRotation(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "bootstrap-admin-pooled-idempotency-and-rotation",
		Description: "Locks BootstrapInternalAdmin's three-state contract end-to-end through the production-shaped WorkspaceResolver + ServicePool + builder chain.",
		Steps: []scenarioEngine.Step{
			setupPooledBootstrapAdminEnv(bootstrapBearerInitial),
			drivePooledAuthenticatedRequestExpectingOK(bootstrapBearerInitial, "phase=first"),
			assertSingleActorAndTokenAfterPooledFirstBootstrap(),
			resetPooledLogBufAndEvictWorkspace(),
			drivePooledAuthenticatedRequestExpectingOK(bootstrapBearerInitial, "phase=after-idle-reload"),
			assertNoRowDuplicationAfterPooledSecondBootstrap(),
			assertPooledDebugIdempotentLogPresent(),
			rotatePooledAdminTokenTo(bootstrapBearerRotated),
			resetPooledLogBufAndEvictWorkspace(),
			drivePooledAuthenticatedRequestExpectingOK(bootstrapBearerRotated, "phase=after-rotation-new-bearer"),
			drivePooledAuthenticatedRequestExpectingOK(bootstrapBearerInitial, "phase=after-rotation-old-bearer"),
			assertRotationRowCountsAfterPooledBootstrap(),
		},
	})
}

// statePooledBootstrapEnv is the scenario-state key for the pooled
// scenario's environment bundle. Separate from stateBootstrapEnv so the
// direct-bootstrap helpers and the pool-backed helpers can coexist in
// one file without colliding on state.
const (
	statePooledBootstrapEnv = "pooled_bootstrap_admin_env"
	pooledWorkspaceID       = "ws-bootstrap-pool"
)

// pooledBootstrapAdminEnv bundles the platform-binding-shaped wiring
// the pool-backed scenario drives. AdminTokenRef is a pointer so the
// rotation step can flip the value the builder closure reads on each
// invocation — mirroring the env-derived SMP_ADMIN_TOKEN plumbing that
// cmd/spine's newPooledWorkspaceBuilder closes over.
type pooledBootstrapAdminEnv struct {
	Server        *httptest.Server
	BaseURL       string
	LogBuf        *syncBuffer
	Pool          *workspace.ServicePool
	Resolver      *fixedWorkspaceResolver
	AdminTokenRef *string
}

// fixedWorkspaceResolver is the minimal workspace.Resolver
// implementation the scenario needs: it returns one configured
// workspace.Config for the well-known scenario workspace ID and
// ErrWorkspaceNotFound for everything else. Production uses
// workspace.PlatformBindingProvider / workspace.DBProvider; for the
// scope of this scenario the interface contract is what matters, not
// the lookup substrate.
type fixedWorkspaceResolver struct {
	workspaceID string
	cfg         *workspace.Config
}

func (r *fixedWorkspaceResolver) Resolve(_ context.Context, id string) (*workspace.Config, error) {
	if id != r.workspaceID {
		return nil, workspace.ErrWorkspaceNotFound
	}
	return r.cfg, nil
}

func (r *fixedWorkspaceResolver) List(_ context.Context) ([]workspace.Config, error) {
	return []workspace.Config{*r.cfg}, nil
}

// setupPooledBootstrapAdminEnv wires the platform-binding-shaped stack:
// fixedWorkspaceResolver → workspace.ServicePool → ServiceSetBuilder
// that calls auth.BootstrapInternalAdmin against each per-workspace
// ServiceSet's own Store. The gateway is constructed with
// WorkspaceResolver+ServicePool and NO direct Store/Auth — the only
// path to successful authentication is through a fully-built ServiceSet.
//
// initialToken is the value the builder reads on its first invocation.
// Subsequent rotations are realised by overwriting *AdminTokenRef in
// rotatePooledAdminTokenTo; the builder re-reads the pointee on every
// invocation, so the next pool.Get-after-Evict produces a builder call
// with the new value (matching what a redeployed cmd/spine process
// with a rotated SMP_ADMIN_TOKEN would observe).
//
// IdleCheckInterval is set to -1 (disabled) so the background eviction
// loop cannot evict between scenario steps; the scenario drives
// pool.Evict explicitly to simulate idle reload.
func setupPooledBootstrapAdminEnv(initialToken string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "setup-pooled-bootstrap-admin-env",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			cfg := &workspace.Config{
				ID:          pooledWorkspaceID,
				DisplayName: "Bootstrap Pool",
				DatabaseURL: secrets.NewSecretValue([]byte(store.TestDSN())),
				RepoPath:    sc.Repo.Dir,
				Status:      workspace.StatusActive,
			}
			resolver := &fixedWorkspaceResolver{workspaceID: pooledWorkspaceID, cfg: cfg}

			adminToken := initialToken
			adminTokenRef := &adminToken
			builder := func(ctx context.Context, ss *workspace.ServiceSet) error {
				return auth.BootstrapInternalAdmin(ctx, ss.Store, auth.BootstrapAdminConfig{
					Token: *adminTokenRef,
				})
			}

			pool := workspace.NewServicePool(sc.Ctx, resolver, workspace.PoolConfig{
				Builder:           builder,
				IdleTimeout:       time.Hour,
				IdleCheckInterval: -1,
			})
			sc.ParentT.Cleanup(pool.Close)

			srv := gateway.NewServer(":0", gateway.ServerConfig{
				WorkspaceResolver: resolver,
				ServicePool:       pool,
			})
			ts := httptest.NewServer(srv.Handler())
			sc.ParentT.Cleanup(ts.Close)

			logBuf := &syncBuffer{}
			prevSlog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			})))
			sc.ParentT.Cleanup(func() { slog.SetDefault(prevSlog) })

			sc.Set(statePooledBootstrapEnv, &pooledBootstrapAdminEnv{
				Server:        ts,
				BaseURL:       ts.URL,
				LogBuf:        logBuf,
				Pool:          pool,
				Resolver:      resolver,
				AdminTokenRef: adminTokenRef,
			})
			return nil
		},
	}
}

// drivePooledAuthenticatedRequestExpectingOK issues a GET against the
// auth-protected /api/v1/system/metrics endpoint with both an
// X-Workspace-ID header and a Bearer token. A 200 here proves the
// entire chain succeeded: workspaceMiddleware resolved the workspace,
// servicePool.Get triggered the builder, the builder bootstrapped
// smp-admin into ss.Store, and authMiddleware validated the bearer
// against ss.Auth (the per-workspace auth.Service the pool installed).
//
// Compared to driveAuthenticatedRequestExpectingOK in the direct
// scenario, this helper sets the X-Workspace-ID header — without it,
// workspaceMiddleware would surface ErrInvalidParams via the deferred
// workspace-resolve error path and the test would fail the same way
// as an unbuilt ServiceSet would, blurring the regression signal.
func drivePooledAuthenticatedRequestExpectingOK(bearer, phase string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "pooled-drive-authenticated-request-" + phase,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(statePooledBootstrapEnv).(*pooledBootstrapAdminEnv)
			req, err := http.NewRequestWithContext(sc.Ctx, http.MethodGet,
				env.BaseURL+"/api/v1/system/metrics", http.NoBody)
			if err != nil {
				return fmt.Errorf("build request (%s): %w", phase, err)
			}
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set(gateway.WorkspaceHeader, pooledWorkspaceID)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("issue request (%s): %w", phase, err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("phase %s: bearer=%s got status %d, want 200 (pooled chain broken — workspace resolve / pool.Get / builder / ss.Auth); body=%s",
					phase, redactBearer(bearer), resp.StatusCode, string(body))
			}
			return nil
		},
	}
}

// assertSingleActorAndTokenAfterPooledFirstBootstrap mirrors
// assertSingleActorAndTokenAfterFirstBootstrap for the pool-backed
// scenario. The assertion reads through sc.Runtime.Store (the harness's
// own per-test connection) rather than the workspace's per-pool pgx
// pool — both point at the same Postgres database, and the helper
// pin-points the row-counts contract independently of whatever
// connection happened to write the rows.
func assertSingleActorAndTokenAfterPooledFirstBootstrap() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-actor-and-token-after-pooled-first-bootstrap",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			actor, err := sc.Runtime.Store.GetActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("GetActor(%s): %w", auth.InternalAdminActorID, err)
			}
			if actor.Role != domain.RoleAdmin || actor.Status != domain.ActorStatusActive {
				return fmt.Errorf("smp-admin actor shape: role=%q status=%q, want %q/%q",
					actor.Role, actor.Status, domain.RoleAdmin, domain.ActorStatusActive)
			}
			tokens, err := sc.Runtime.Store.ListTokensByActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("ListTokensByActor: %w", err)
			}
			if len(tokens) != 1 {
				return fmt.Errorf("token count after first pooled bootstrap: got %d, want 1", len(tokens))
			}
			expectHash := auth.HashToken(bootstrapBearerInitial)
			gotActor, gotToken, err := sc.Runtime.Store.GetActorByTokenHash(sc.Ctx, expectHash)
			if err != nil {
				return fmt.Errorf("GetActorByTokenHash(initial): %w", err)
			}
			if gotActor.ActorID != auth.InternalAdminActorID {
				return fmt.Errorf("token hash bound to %q, want %q",
					gotActor.ActorID, auth.InternalAdminActorID)
			}
			wantTokenID := "bootstrap-" + expectHash[:12]
			if gotToken.TokenID != wantTokenID {
				return fmt.Errorf("token_id: got %q, want %q", gotToken.TokenID, wantTokenID)
			}
			return nil
		},
	}
}

// resetPooledLogBufAndEvictWorkspace clears the captured log buffer
// and evicts the cached ServiceSet in one atomic step so the next
// pool.Get forces a fresh builder invocation against an empty buffer.
// Combining the two avoids a "reset → request triggers no rebuild
// because the entry is still cached" silent miss.
//
// pool.Evict is a no-op when refCount > 0 (the entry is just marked
// evicting and freed on the last Release). The previous step's
// http.DefaultClient.Do blocks until the response is read, so by the
// time control returns here, workspaceMiddleware's deferred release
// has fired and refCount is 0 — Evict frees the entry immediately.
func resetPooledLogBufAndEvictWorkspace() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "reset-log-buf-and-evict-pooled-workspace",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(statePooledBootstrapEnv).(*pooledBootstrapAdminEnv)
			env.LogBuf.Reset()
			env.Pool.Evict(pooledWorkspaceID)
			return nil
		},
	}
}

// assertNoRowDuplicationAfterPooledSecondBootstrap is the pool-backed
// counterpart to assertNoRowDuplicationAfterSecondBootstrap: with the
// builder re-invoked on a fresh ServiceSet, the second bootstrap call
// must remain a no-op against the existing auth.tokens row — no extra
// rows, no actor row re-stamping.
func assertNoRowDuplicationAfterPooledSecondBootstrap() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-no-row-duplication-after-pooled-second-bootstrap",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			tokens, err := sc.Runtime.Store.ListTokensByActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("ListTokensByActor: %w", err)
			}
			if len(tokens) != 1 {
				return fmt.Errorf("token count after pooled second bootstrap: got %d, want 1 (idempotency broken through the builder)", len(tokens))
			}
			return nil
		},
	}
}

// assertPooledDebugIdempotentLogPresent locates the same DEBUG line as
// the direct-scenario assertion in the log buffer captured during the
// pool-backed re-resolve. The log emission happens inside
// auth.BootstrapInternalAdmin → observe.Logger(ctx) → slog.Default(),
// which is the buffer-backed handler the setup step installed; the
// pool's internal goroutine that runs the builder picks up the same
// global slog handler. Same record-level level-check as the direct
// variant: split per-record then require level=DEBUG on the matching
// record.
func assertPooledDebugIdempotentLogPresent() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-pooled-debug-idempotent-log-present",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(statePooledBootstrapEnv).(*pooledBootstrapAdminEnv)
			logged := env.LogBuf.String()
			const wantMsg = "internal admin token already configured"
			var matched string
			for _, line := range strings.Split(strings.TrimRight(logged, "\n"), "\n") {
				if strings.Contains(line, wantMsg) {
					matched = line
					break
				}
			}
			if matched == "" {
				return fmt.Errorf("expected log line containing %q in pooled re-resolve output; full log:\n%s",
					wantMsg, logged)
			}
			if !strings.Contains(matched, "level=DEBUG") {
				return fmt.Errorf("expected level=DEBUG on pooled token-already-configured record; got line:\n%s\nfull log:\n%s",
					matched, logged)
			}
			return nil
		},
	}
}

// rotatePooledAdminTokenTo flips the value the builder closure reads
// on its next invocation. The builder was constructed in
// setupPooledBootstrapAdminEnv with a pointer to a token variable, so
// updating *AdminTokenRef here is what a redeployed cmd/spine process
// with a rotated SMP_ADMIN_TOKEN env var would do: same builder shape,
// new token at builder-invocation time.
func rotatePooledAdminTokenTo(newToken string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "rotate-pooled-admin-token",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			env := sc.MustGet(statePooledBootstrapEnv).(*pooledBootstrapAdminEnv)
			*env.AdminTokenRef = newToken
			return nil
		},
	}
}

// assertRotationRowCountsAfterPooledBootstrap mirrors
// assertRotationRowCounts for the pool-backed scenario: the rotated
// bootstrap must leave the initial-bearer hash row intact and add a
// second row bound to the same smp-admin actor. The two HTTP requests
// preceding this step (new bearer then old bearer, both expecting 200)
// already proved the dual-bearer contract authenticates end-to-end;
// this step pins the underlying row shape so a regression that swapped
// row-counts for an unrelated assertion would still surface.
func assertRotationRowCountsAfterPooledBootstrap() scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-rotation-row-counts-after-pooled-bootstrap",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			tokens, err := sc.Runtime.Store.ListTokensByActor(sc.Ctx, auth.InternalAdminActorID)
			if err != nil {
				return fmt.Errorf("ListTokensByActor: %w", err)
			}
			if len(tokens) != 2 {
				return fmt.Errorf("token count after pooled rotation: got %d, want 2 (old + new)", len(tokens))
			}

			oldHash := auth.HashToken(bootstrapBearerInitial)
			newHash := auth.HashToken(bootstrapBearerRotated)
			oldActor, _, err := sc.Runtime.Store.GetActorByTokenHash(sc.Ctx, oldHash)
			if err != nil {
				return fmt.Errorf("GetActorByTokenHash(old): %w", err)
			}
			if oldActor.ActorID != auth.InternalAdminActorID {
				return fmt.Errorf("old hash bound to %q, want %q",
					oldActor.ActorID, auth.InternalAdminActorID)
			}
			newActor, _, err := sc.Runtime.Store.GetActorByTokenHash(sc.Ctx, newHash)
			if err != nil {
				return fmt.Errorf("GetActorByTokenHash(new): %w", err)
			}
			if newActor.ActorID != auth.InternalAdminActorID {
				return fmt.Errorf("new hash bound to %q, want %q",
					newActor.ActorID, auth.InternalAdminActorID)
			}
			return nil
		},
	}
}
