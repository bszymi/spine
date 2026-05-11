---
id: TASK-029
type: Task
title: "Per-handler typed minimal-store for gateway tests"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-11
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-010-split-store-interface.md
---

# TASK-029 — Per-handler typed minimal-store for gateway tests

---

## Purpose

`internal/gateway/handlers_tokens_test.go:21-24` (and similar tests in
the package) embed the `store.Store` interface directly so unimplemented
methods panic at runtime — a refactor that touches an ancillary store
call in the same handler will break tests in surprising ways instead
of failing the typecheck.

This is a P3 test-quality finding from the 2026-05-07 code review.

## Deliverable

- After TASK-010 lands the role-interface split, migrate gateway
  handler tests so each test takes the narrowest role interface its
  handler under test actually depends on (e.g. `AuthStore` for
  token tests).
- Replace the `store.Store`-embedded stubs with typed minimal stubs
  (one per role-interface, ideally generated or hand-coded once).
- This change might land alongside TASK-010 or as its follow-up
  cleanup PR.

## Acceptance Criteria

- All gateway tests pass.
- Refactoring an unrelated `store.Store` method (renaming a parameter,
  for example) only breaks the tests that exercise that method, not
  every gateway test in the package.

## Out of Scope

- Until TASK-010 lands, this task remains blocked on the role-interface
  partition. If TASK-010 is descoped, this task is descoped as well.

## Resolution (2026-05-11)

Four files modified, one new file added; gateway test stubs now embed
typed per-role stubs instead of the kitchen-sink `store.Store`
interface.

**New file:**

1. **`internal/gateway/stubstore_test.go`** (package `gateway_test`) —
   per-role no-op stubs mirroring `cmd/spine/stubstore_test.go`:
   `stubSystem`, `stubRunStore`, `stubBranchStore`, `stubArtifactStore`,
   `stubWorkflowProjectionStore`, `stubSyncStateStore`, `stubAuthStore`,
   `stubAssignmentStore`, `stubSkillStore`, `stubRepositoryStore`,
   `stubBranchProtectionStore`, `stubDiscussionStore`,
   `stubDeliveryStore`, `stubSubscriptionStore`, plus a `stubTx`
   no-op transaction and a `stubRoleStore` union that composes all
   fourteen. Every role stub has a `var _ store.X = stubX{}` assertion
   so adding a method to a role interface fails the build at this file
   rather than at the first test that happens to exercise the new
   method.

**Modified files:**

2. **`internal/gateway/gateway_test.go`** — `fakeStore` drops the
   `store.Store` interface embed and embeds `stubRoleStore` instead.
   All existing concrete overrides (Ping, GetActor, CreateToken, …)
   continue to shadow the per-role stub methods. Same satisfiability
   (`*PostgresStore` still passes; `fakeStore` still satisfies
   `store.Store`), tighter compile-time enforcement.
3. **`internal/gateway/handlers_discussions_test.go`** — `discussionStore`
   migrated the same way.
4. **`internal/gateway/handlers_skills_test.go`** — `skillStore`
   migrated the same way.
5. **`internal/gateway/handlers_tokens_test.go`** — `tokenStubStore`
   migrated differently: this stub lives in `package gateway` (the
   only `Store`-touching fake in the internal test package) and is
   only ever passed to `auth.NewService`, which takes
   `store.AuthStore`. So `tokenStubStore` now implements
   `store.AuthStore` *directly* (no embed) with the three existing
   overrides plus six explicit no-op methods for the remaining
   `AuthStore` surface. The single `var _ store.AuthStore = (*tokenStubStore)(nil)`
   assertion captures the contract end-to-end.

**Acceptance criteria satisfied:**

- *All gateway tests pass.* ✓ — `go test ./internal/gateway/... -race`
  green; full `-race` suite across 38 packages green.
- *Refactoring an unrelated `store.Store` method only breaks the tests
  that exercise that method, not every gateway test in the package.* ✓
  — Adding a method to a role interface (e.g., `RotateToken` on
  `AuthStore`) now fails the `var _` assertion in `stubstore_test.go`
  for `stubAuthStore`, and the `var _ store.AuthStore = (*tokenStubStore)(nil)`
  assertion in `handlers_tokens_test.go` — one update each. Fakes that
  don't depend on the role still compile.

**Regression-bait verification** (manual, pre-submission):

| Mutation | Result |
| --- | --- |
| Add `RotateToken(ctx, tokenID) error` to `store.AuthStore` (+ a no-op on `*PostgresStore` so the production assertion passes) | FAIL — `internal/gateway/handlers_tokens_test.go:76: cannot use (*tokenStubStore)(nil) as store.AuthStore value … missing method RotateToken`. The compile-time assertion catches the omission, mirroring the same failure expected at `var _ store.AuthStore = stubAuthStore{}` in `stubstore_test.go` for the external test package. |

**Test gates**

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l internal/gateway/{stubstore_test.go,handlers_tokens_test.go,handlers_discussions_test.go,handlers_skills_test.go,gateway_test.go}` — clean. (Pre-existing repo-wide gofmt drift in 6 other gateway files is unchanged.)
- `go test ./... -count=1 -race` — green (38 packages).
- `go test -tags=scenario ./internal/scenariotest/scenarios/...` — failure set unchanged vs. clean main (`validation_failed` failures pre-date this PR).
- `make docker-lint` — 207 issues (down 1 from the 208 baseline because gofmt fixed pre-existing drift in `gateway_test.go`); no new gocritic, staticcheck, errcheck, gosec, or unused findings introduced by my edits.
- `golangci-lint --enable-only=gosec ./internal/gateway/...` — 1 issue (`handlers_repositories_test.go:205 G101`); same as baseline, unrelated test file.
- `codex review` — clean: *"The per-role stubs appear to satisfy their corresponding store interfaces, and the fake stores' overrides use matching signatures so they shadow the embedded no-op methods. The token stub's added AuthStore methods are not used by the tested create/revoke paths, and no concrete regression from the nil-dispatch to no-op behavior is evident in the changed code."*

**Behavioural note**: switching from `store.Store` interface embed to
per-role no-op stubs replaces "runtime nil-dispatch panic when a fake
hits an unimplemented method" with "silent zero/nil return". Each
existing fake already explicitly overrides every method its test paths
exercise, so today's panic behaviour is unreachable in practice. The
new pattern matches `cmd/spine/stubstore_test.go` and keeps repo
idioms consistent. Future tests that add new handler paths must
explicitly override the relevant role-stub method just as they would
have to today.
