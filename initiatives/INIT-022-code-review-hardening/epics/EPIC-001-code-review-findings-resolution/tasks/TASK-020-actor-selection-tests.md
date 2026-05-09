---
id: TASK-020
type: Task
title: "Unit tests for actor selection strategies"
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
---

# TASK-020 — Unit tests for actor selection strategies

---

## Purpose

`internal/actor/service.go`, `selection.go`, `assignment.go`,
`prompt.go`, `ai_provider.go` lack direct unit-test coverage.
`actor/service.go:14`'s `validActorID` regex and `selection.go`'s
selection strategies (including `StrategyRoundRobin` with its
`sync.Mutex`) are critical for assignment fairness and have only
indirect coverage via gateway/engine tests.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

- `internal/actor/service_test.go`: validActorID regex matrix
  (valid, invalid characters, length bounds, leading/trailing
  whitespace), CRUD happy paths against a stub store.
- `internal/actor/selection_test.go`:
  - For each strategy in the registry, a focused test exercising the
    happy path.
  - For `StrategyRoundRobin`, a test running 1000 picks across N
    actors and asserting fair distribution within tolerance.
  - Concurrency test: parallel `Pick` calls under race detector.
- `internal/actor/assignment_test.go`: minimal happy-path coverage of
  the assignment lifecycle if not already covered indirectly.

## Acceptance Criteria

- Tests pass without the `integration` tag.
- Round-robin determinism test fails if the mutex protecting the
  cursor is removed.

## Out of Scope

- AI-provider integration tests — separate concern, integration tag.
- prompt.go (mostly templating) unless trivial gaps surface.

## Resolution (2026-05-09)

**Files**

- `internal/actor/service_test.go` (NEW) — `Service.Register` regex
  matrix (27 cases) + `ErrInvalidParams` error-code pin + status
  override pin.
- `internal/actor/selection_test.go` (NEW) — round-robin fairness +
  concurrency + pool-key isolation + wrap + AnyEligible determinism +
  default-strategy / unknown-strategy branches. Includes a tiny
  `orderedFakeStore` wrapper that sorts `ListActorsByStatus` output.

**Coverage gap addressed**

The pre-existing `internal/actor/actor_test.go` covers happy-path CRUD
and one loose round-robin distribution check ("both humans appear in 6
picks"). The TASK-020 ACs needed:

- A regex matrix on `validActorID` (service.go:14). Existing tests
  only hit the empty-string branch.
- A round-robin determinism test that fails if the `rrMu` mutex is
  removed.

**Service test shape**

- `TestRegister_ValidActorIDMatrix` — table with 27 cases. Accepts:
  alnum lower/mixed/single, hyphen/underscore/dot in body,
  128-char max, mixed punct tail. Rejects: leading hyphen/underscore/
  dot/space, trailing space/newline, embedded space/slash/colon/at/
  percent/quote/unicode, empty, 129-char overflow. Each case
  cross-checks both the error and the persistence side: rejected
  IDs must not land in the store.
- `TestRegister_RejectErrorIsInvalidParams` — pins the error code as
  `*domain.SpineError` with `Code == ErrInvalidParams`. The gateway
  maps that to HTTP 400, so a regression to a generic internal error
  would silently change client-visible behaviour.
- `TestRegister_StatusForcedActive` — pins that `Register` overrides
  any caller-supplied Status with `Active`. A regression that
  honoured the caller's value would let pre-suspended/deactivated
  actors slip into the store.

**Selection test shape**

- `TestSelectRoundRobin_FairDistribution` — 1000 picks across 4
  actors, asserts exactly 250 each. The implementation
  (`idx := count % N; count++`) gives perfect distribution; any
  tolerance window would mask off-by-one or modulo-skew bugs.
- `TestSelectRoundRobin_ConcurrentSafe` — **the AC's "fails if the
  mutex is removed" regression bait.** 1000 goroutines × 1 pick each
  → exactly 1000 picks total, 250 per actor. With `rrMu` removed,
  `-race` flags the unsynchronised read+write on `rrIndices` and the
  fairness assertion drifts. Verified during development by
  temporarily deleting the lock and re-running with `-race`: race
  detector fires AND the per-actor counts skewed
  (251/252/234/263 vs the expected 250/250/250/250).
- `TestSelectRoundRobin_PoolKeyIsolation` — pins that two
  `SelectionRequest`s with different pool keys hold independent
  cursors. A regression that collapsed the pool-key construction
  (e.g. dropping `EligibleActorTypes`) would let one pool's rotation
  bleed into the other and the assertion below would break.
- `TestSelectRoundRobin_WrapsAroundOnSizeChange` — drives the cursor
  past pool size; pins the `idx % len(actors)` modulo so a regression
  that dropped it would panic on out-of-bounds.
- `TestSelectAnyEligible_ReturnsFirstEligible` — pins that
  `StrategyAnyEligible` is deterministic across 50 calls (no
  random selection added on top of the eligible slice).
- `TestSelectActor_DefaultStrategyIsAnyEligible` — pins the
  `case StrategyAnyEligible, "":` empty-string branch. A regression
  that fell through to the default branch would error on unset
  Strategy.
- `TestSelectActor_UnknownStrategyRejected` — pins the explicit
  `ErrInvalidParams` error type/code so a regression that
  silently fell through to AnyEligible would not mask malformed
  input.

**Helper: `orderedFakeStore`**

`fakeStore.ListActorsByStatus` iterates a Go map (non-deterministic).
The strict round-robin assertions need stable eligible-slice ordering;
sorted-by-ActorID is a faithful proxy for production stores
(Postgres orders by an index). The wrapper is colocated in
`selection_test.go` rather than mutating the shared `actor_test.go`
fixture so existing tests (which don't depend on order) keep their
established behaviour.

**Out-of-scope confirmation**

- `assignment.go` — purely struct definitions (AssignmentRequest /
  Context / Constraints / Result). No logic to test.
- `ai_provider.go` — interface + DTOs only. Provider implementations
  live in adapters and are separate concerns.
- `prompt.go` — already covered by `gateway_test.go`'s
  `TestBuildPrompt` / `TestBuildPromptMinimal`.

**Coverage**

`go tool cover -func` for `internal/actor`:

- `service.go` Register: 100%, Suspend/Deactivate/Get/AddSkill/
  ListSkills/FindEligibleActors: 100%; Reactivate/RemoveSkill/
  updateStatus 67-83% (untouched by this task — covered indirectly).
- `selection.go` SelectActor: 91.7%, selectExplicit: 77.8%,
  selectRoundRobin: 100%, contains: 100%.
- Total package: 91.2% — clears the AC's ≥85% threshold.

**Test gates**

- `go test ./internal/actor/... -count=1 -race` — green.
- `go test ./...` — green except the pre-existing
  `internal/secrets/file_test.go::TestFileClient_VersionChangesOnEdit`
  flake (TASK-026 territory).
- `gofmt -l ./internal/actor/` — clean.
- `make docker-lint` — 206 baseline unchanged; no new findings on
  the added test files.
- Codex review pass 1 — clean: "no discrete correctness issues in
  the added code."
- **Mutex-removal verification**: temporarily deleted `rrMu.Lock/
  Unlock` from `selection.go:169-170` and re-ran the suite under
  `-race`. The race detector fired on `rrIndices` AND the fairness
  assertion failed with skewed counts. Restored after verification.
