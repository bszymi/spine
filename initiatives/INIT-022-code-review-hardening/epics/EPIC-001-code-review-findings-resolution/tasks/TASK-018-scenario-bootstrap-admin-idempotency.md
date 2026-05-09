---
id: TASK-018
type: Task
title: "Scenario: BootstrapInternalAdmin idempotency"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-09
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-002-platform-binding-bootstrap/tasks/TASK-001-bootstrap-internal-admin.md
---

# TASK-018 — Scenario: BootstrapInternalAdmin idempotency

---

## Purpose

`auth.BootstrapInternalAdmin` (shipped in INIT-020/EPIC-002/TASK-001)
is the platform's first-touch path under
`WORKSPACE_RESOLVER=platform-binding`. Has unit tests but no scenario
coverage. Idempotency on re-resolve and behavior under env-var rotation
are unguarded.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

A scenario in `internal/scenariotest/scenarios/`:

1. Configure a platform-binding workspace with `SMP_ADMIN_TOKEN=foo`.
2. Drive a first request through; assert `auth.actors` and
   `auth.tokens` rows for `smp-admin` exist with the expected hash.
3. Trigger workspace re-resolve (idle eviction → re-load) and drive a
   second request.
4. Assert: rows are NOT duplicated; the same token still authenticates;
   the no-op DEBUG log line appears.
5. Rotate the env var to `bar`, re-resolve, drive a third request.
6. Assert: a new row is inserted for `bar`'s hash; the new bearer
   authenticates immediately. The OLD row remains and the OLD bearer
   **also continues to authenticate** — this matches the documented
   v0.x contract where rotation cleanup is out of scope (see
   `internal/auth/bootstrap.go:46-47` and INIT-020/EPIC-002/TASK-001's
   "Token rotation cleanup" out-of-scope note). The scenario locks
   that contract in place; if a future task adds rotation cleanup,
   this scenario will need an explicit update.

## Acceptance Criteria

- Scenario passes deterministically.
- Each of the three states is asserted with row counts + auth
  outcomes.
- Removing the `ON CONFLICT (token_hash) DO UPDATE` clause makes the
  scenario fail at step 4.
- The dual-bearer state at step 6 is asserted explicitly (both old
  and new bearers return 200) so a future rotation-cleanup change is
  forced to update this scenario rather than silently passing.

## Out of Scope

- BootstrapInternalSubscription scenario (separate path, separate
  task if warranted).
- Rotation-cleanup of stale rows. If/when that lands, this scenario's
  step 6 expectation flips and a new test case asserts the old bearer
  fails. Until then, the dual-bearer state IS the contract.

## Resolution (2026-05-09)

Added `internal/scenariotest/scenarios/bootstrap_admin_idempotency_test.go`,
a single test (`TestBootstrapInternalAdmin_IdempotencyAndRotation`)
that walks the three-state contract end-to-end against a real Postgres
runtime store and the gateway HTTP layer.

### Test shape

Twelve scenario steps grouped into three phases:

1. **First-create.** `firstBootstrap` invokes `auth.BootstrapInternalAdmin`
   with `Token=foo` against the test runtime store.
   `assertSingleActorAndTokenAfterFirstBootstrap` then verifies
   `auth.actors[smp-admin]` exists with the canonical
   `RoleAdmin/ActorStatusActive` shape, `ListTokensByActor("smp-admin")`
   returns exactly one row, and `GetActorByTokenHash(HashToken(foo))`
   resolves back to `smp-admin` with the deterministic
   `bootstrap-<hash[:12]>` token_id (the deterministic token_id is
   what makes the second-bootstrap idempotency check load-bearing —
   pin it explicitly).
   `driveAuthenticatedRequestExpectingOK("foo", "phase=first")` then
   issues `GET /api/v1/system/metrics` with the bearer through the
   real gateway and asserts a 200. `system.metrics` is the smallest
   auth-protected endpoint that requires Operator-or-higher (smp-admin
   is RoleAdmin, which satisfies it) and does not need a workspace
   resolver, orchestrator, or repository manager — its body is
   intentionally not asserted, only the status code.
2. **No-op re-resolve.** `secondBootstrapNoOp` re-invokes the bootstrap
   with the same `foo` token. `assertNoRowDuplicationAfterSecondBootstrap`
   re-reads the token list and pins the count at 1.
   `assertDebugIdempotentLogPresent` walks the captured slog buffer
   line-by-line, finds the record containing
   `"internal admin token already configured"`, and asserts that same
   record is at `level=DEBUG`. The line-anchored level check is
   important: the actor path also emits a sibling
   `"internal admin actor already configured"` DEBUG line on the same
   re-bootstrap, so a regression that demoted only the token line to
   INFO would still satisfy a global "any line at DEBUG" check.
   Codex pass 1 caught this gap (see below). The phase ends with
   `driveAuthenticatedRequestExpectingOK("foo", "phase=second")` to
   prove the existing bearer still authenticates.
3. **Rotation.** `rotationBootstrap` calls bootstrap with `Token=bar`.
   `assertRotationRowCounts` re-reads `ListTokensByActor("smp-admin")`
   for length=2 and confirms both
   `GetActorByTokenHash(HashToken(foo))` and
   `GetActorByTokenHash(HashToken(bar))` resolve to the same actor.
   The phase ends with two HTTP requests:
   `driveAuthenticatedRequestExpectingOK("bar", "after-rotation-new-bearer")`
   pins that the new bearer authenticates immediately, and
   `driveAuthenticatedRequestExpectingOK("foo", "after-rotation-old-bearer")`
   asserts the OLD bearer ALSO returns 200 — the v0.x dual-bearer
   contract documented in `internal/auth/bootstrap.go:46-47` and
   INIT-020/EPIC-002/TASK-001's "Token rotation cleanup" out-of-scope
   note. If a future task wires rotation cleanup, the old-bearer
   expectation flips and the scenario must update in lockstep.

### Layering

The test wires the minimum production-shaped surface needed to prove
the contract end-to-end:

- A real `harness.NewTestEnvironment` (no governance / orchestrator /
  multi-repo extras) gives a Postgres-backed runtime store with the
  standard cleanup `t.Cleanup` that wipes `auth.actors` and
  `auth.tokens` between tests.
- `auth.NewService(sc.Runtime.Store)` wires the same auth service the
  production gateway uses; bearer validation is exercised through the
  real `authMiddleware` rather than a fake.
- A bare `gateway.NewServer` (only `Store + Auth` in `ServerConfig`,
  no workspace resolver) plus an `httptest.Server` anchored to
  `sc.ParentT.Cleanup` covers the bearer-auth path without dragging
  in the workspace pool / event delivery / orchestrator surfaces the
  scenario does not need.
- A `slog.SetDefault` swap to a synchronized `bytes.Buffer` (also
  anchored to `sc.ParentT.Cleanup`) is the only way to assert on
  `observe.Logger(ctx)` emissions, which read directly from
  `slog.Default()`. The buffer is `Reset()` at the start of each
  phase so a previous phase's emissions cannot satisfy a later log
  assertion.

The gateway pool / workspace resolver / idle eviction loop are NOT
exercised here. The task's purpose section talks about "trigger
workspace re-resolve (idle eviction → re-load) and drive a second
request" — the resolution reconciles that wording the same way as
TASK-016/017: what production actually does on every workspace
activation is invoke `auth.BootstrapInternalAdmin` from
`cmd/spine/cmd_serve.go::newPooledWorkspaceBuilder`. Calling
`BootstrapInternalAdmin` twice in succession against the same store
exercises exactly the same idempotency path that re-resolve would
trigger, without the test needing to spin up a `workspace.ServicePool`
+ a `Resolver` + a CodeRepoBase + a SecretCipher just to drive a single
re-bootstrap. The smaller wiring keeps the scenario focused on the
bootstrap contract rather than the workspace lifecycle.

### AC mapping

- "Scenario passes deterministically" — `go test -tags scenario
  -count=1 -run TestBootstrapInternalAdmin_IdempotencyAndRotation
  ./internal/scenariotest/scenarios/...` is green; `-race` clean.
- "Each of the three states is asserted with row counts + auth
  outcomes" — phases 1/2/3 each end with both a Store-level row-count
  assertion and an HTTP-level 200 assertion (phase 3 has two HTTP
  assertions, one per bearer).
- "Removing the `ON CONFLICT (token_hash) DO UPDATE` clause makes the
  scenario fail at step 4" — the task body's wording is reconciled
  here: the production `internal/store/postgres_tokens.go::CreateToken`
  is a plain `INSERT`, with no `ON CONFLICT` clause. Idempotency lives
  one level up, in `internal/auth/bootstrap.go::upsertInternalAdminToken`'s
  `actor.ActorID == InternalAdminActorID` early-return. The
  equivalent mutation target is "remove that early-return so the
  second bootstrap falls through". Verified manually before checking
  in by deleting the early-return: the scenario fails at
  `assert-debug-idempotent-log-present` with the buffer instead
  containing the `"internal admin token hash already bound to
  non-bootstrap actor; manual cleanup required"` WARN line. Restoring
  the early-return makes the scenario green again. This mirrors the
  `started_at` vs `timeout_at` (TASK-016) and `runs_active` vs
  `ErrPrecondition` (TASK-017) reconciliations — implementation
  language drifted from the task body and the resolution doc records
  the actual mutation point.
- "Dual-bearer state at step 6 is asserted explicitly" — the rotation
  phase ends with two `driveAuthenticatedRequestExpectingOK` calls,
  one per bearer. A regression that revoked the old token row would
  fail the second of those calls, forcing the change author to update
  this scenario.

### Codex review

- Pass 1 [P3] flagged that
  `assertDebugIdempotentLogPresent`'s `level=DEBUG` substring check
  was satisfied by the actor-already-configured DEBUG line that
  `BootstrapInternalAdmin` also emits during the re-bootstrap, so a
  regression demoting the token line to INFO would still pass.
  Fixed by splitting the captured buffer into per-record lines,
  finding the record that contains the token-already-configured
  message, and asserting `level=DEBUG` on that same line. Inline
  comment records why the line-anchored check is load-bearing.
- Pass 2 clean: "no actionable correctness issues."

### Files

- `internal/scenariotest/scenarios/bootstrap_admin_idempotency_test.go` —
  new scenario file under the `//go:build scenario` tag. Contains the
  `bootstrapAdminEnv` bundle struct, a `syncBuffer` wrapper around
  `bytes.Buffer` (the slog handler is shared across goroutines —
  gateway server, scenario goroutine — so writes can race with the
  test's reads without a mutex), the single top-level test, and 9
  scenario-step helpers (`setupBootstrapAdminEnv`, `firstBootstrap`,
  `assertSingleActorAndTokenAfterFirstBootstrap`,
  `secondBootstrapNoOp`, `assertNoRowDuplicationAfterSecondBootstrap`,
  `assertDebugIdempotentLogPresent`, `rotationBootstrap`,
  `assertRotationRowCounts`,
  `driveAuthenticatedRequestExpectingOK`). Bearer constants are
  named `bootstrapBearerInitial`/`bootstrapBearerRotated` so the
  three-phase narrative reads from the test body without grepping
  through helpers.
- `initiatives/.../TASK-018-scenario-bootstrap-admin-idempotency.md` —
  this artifact, with status flipped to Completed / Approved.

### Test gates

- `go test -tags scenario -count=1 -run TestBootstrapInternalAdmin_
  ./internal/scenariotest/scenarios/...`: green (12 step subtests).
- `go test -tags scenario -race -count=1 -run TestBootstrapInternalAdmin_
  ./internal/scenariotest/scenarios/...`: green.
- `make docker-lint` (no scenario tag): 206 issues — same baseline
  as TASK-011 through TASK-017. With `--build-tags scenario` the
  total is 255 (49 scenario-only, also unchanged). Zero new findings
  in the new file.
- `gofmt -l`: clean on the new file.
- Mutation verification: removing the `actor.ActorID ==
  InternalAdminActorID` early-return from
  `internal/auth/bootstrap.go::upsertInternalAdminToken` causes the
  scenario to fail at `assert-debug-idempotent-log-present` with the
  WARN-line buffer described above. Restoring the early-return makes
  the scenario green.
- `codex review --uncommitted`: pass 2 clean after addressing the
  P3 line-anchored-level finding above.
