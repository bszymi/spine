//go:build scenario

package scenarios_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/gateway"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
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
