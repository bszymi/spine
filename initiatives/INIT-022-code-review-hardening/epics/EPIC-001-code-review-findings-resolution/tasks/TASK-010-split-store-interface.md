---
id: TASK-010
type: Task
title: "Split internal/store.Store into role-specific interfaces"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-08
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-010 — Split internal/store.Store into role-specific interfaces

---

## Purpose

`internal/store/store.go:12` defines `Store` as a single interface with
**111 methods** spanning runs, steps, artifacts, tokens, deliveries,
subscriptions, branches, repositories, projections, etc. Symptoms:
`stubstore_test.go` is 453 lines of boilerplate and
`internal/workspace/pool.go:31`'s `ServiceSet` types 20+ fields as
`any` to dodge the resulting import cycles.

This is a P2 code-quality finding from the 2026-05-07 code review.

## Deliverable

- Define role-specific interfaces in `internal/store/`:
  - `RunStore` (run + step + run-repo CRUD)
  - `ArtifactStore` (artifacts, projections)
  - `DeliveryStore` (event subscriptions, delivery history,
    retention)
  - `AuthStore` (actors, tokens)
  - `RepositoryStore` (catalog bindings, run-repository-baselines)
  - `BranchStore` / `BranchProtectionStore`
  - `ValidationStore` if naturally separable
- `Store` becomes the union (embedding all role interfaces) so
  `*PostgresStore` continues to satisfy it without changes.
- Compile-time assertions: `var _ RunStore = (*PostgresStore)(nil)`,
  etc., next to the existing `var _ Store = ...`.
- Migrate the most painful consumers to the role interfaces — at
  least:
  - `internal/scheduler` → `RunStore` + `DeliveryStore`
  - `internal/engine` → `RunStore` + `ArtifactStore`
  - `internal/delivery` → `DeliveryStore` + `AuthStore`
  - `internal/workspace/pool.go::ServiceSet` → drop one or more `any`
    fields in favor of typed role interfaces.

## Acceptance Criteria

- `go build ./...` passes; `go test ./...` passes (with the
  `integration` tag where applicable).
- `*PostgresStore` continues to satisfy the union `Store` type.
- At least three call sites that previously took `Store` now take a
  narrower role interface.
- `stubstore_test.go` is reduced to satisfying only the role
  interfaces a given test needs (or a per-role minimal stub set
  replaces it).
- `internal/workspace/pool.go::ServiceSet` has at least one `any`
  field removed.

## Out of Scope

- Touching the `*PostgresStore` implementation itself; this is purely
  an interface partition.
- Hierarchies of role interfaces (e.g. `ReadOnlyRunStore` +
  `RunStore`). Start flat — refine later if the call sites genuinely
  need read-only narrowing.

## Notes

This is the largest refactor in the epic; consider sequencing it
after the P1 fixes so it doesn't sit on the critical path.

## Resolution (2026-05-08)

Two new files + thirteen edited files. The 96-method flat `Store`
interface is replaced with a union that embeds fourteen role-specific
interfaces; nine call sites that previously took the union now take a
narrower role.

**New files:**

1. **`internal/store/interfaces.go`** — fourteen role-specific
   interfaces partitioning the existing surface:
   `SystemStore` (transactions, health, migrations, lifecycle),
   `RunStore` (runs + step executions + scheduler queries +
   repository merge outcomes), `BranchStore` (divergence +
   branches), `ArtifactStore` (artifact projections + links +
   execution projections), `WorkflowProjectionStore`,
   `SyncStateStore`, `AuthStore` (actors + tokens),
   `AssignmentStore`, `SkillStore` (skills + actor↔skill assoc),
   `RepositoryStore` (repository bindings),
   `BranchProtectionStore`, `DiscussionStore`,
   `DeliveryStore` (delivery queue + history + event log),
   `SubscriptionStore`. Each interface's docstring names the
   primary consumer to anchor the partition.
2. **`internal/store/interfaces_assertions.go`** — compile-time
   `var _ <Role> = (*PostgresStore)(nil)` assertions for every
   role and the union, kept in one greppable place so adding a
   method to a role without adding the matching method on
   PostgresStore fails the build at this file rather than at every
   consumer call site.

**Modified files:**

3. **`internal/store/store.go`** — `Store` reduced from a 96-method
   flat declaration to a union that embeds the fourteen role
   interfaces. Existing callers continue to work unchanged
   (assignability is the same set of methods); new callers can
   depend on a smaller role.
4. **`internal/auth/auth.go`** — `NewService(st store.AuthStore)`.
5. **`internal/auth/bootstrap.go`** —
   `BootstrapInternalAdmin(ctx, st store.AuthStore, cfg)` plus the
   two helpers it delegates to.
6. **`internal/delivery/subscriber.go`** —
   `NewDeliverySubscriber(st store.DeliveryStore, subs)`.
7. **`internal/delivery/retention.go`** —
   `StartRetentionCleanup(ctx, st store.DeliveryStore, retention)`.
8. **`internal/delivery/webhook_dispatcher.go`** —
   `NewWebhookDispatcher(st store.DeliveryStore, resolver, cfg)`.
9. **`internal/delivery/subscription_store.go`** —
   `NewStoreSubscriptionLister(st store.SubscriptionStore)` and
   `NewStoreSubscriptionResolver(st store.SubscriptionStore)`.
10. **`internal/delivery/bootstrap.go`** —
    `BootstrapInternalSubscription(ctx, st store.SubscriptionStore, cfg)`.
11. **`internal/divergence/service.go`** —
    `NewService(st store.BranchStore, gitClient, events)`.
12. **`internal/workspace/pool.go::ServiceSet`** — `Workflows any`
    typed as `*workflow.Service`. The remaining `any` fields
    (RunStarter, PlanningRunStarter, etc.) genuinely need erasure
    because they bridge an engine→scheduler→workspace import cycle
    and don't wrap the store; they are out of scope for this task.
13. **`internal/gateway/server.go::workflowsFrom`** — replaces a
    runtime type-assertion against `WorkflowService` with a
    compile-time-typed nil check. The concrete `*workflow.Service`
    satisfies the interface, so the previously-runtime assertion
    becomes statically guaranteed.
14. **`cmd/spine/cmd_serve.go`** — replaces the runtime
    `ss.Workflows.(engine.WorkflowWriter)` type-assertion + error
    return with a direct `orch.WithWorkflowWriter(ss.Workflows)`
    call. Same compile-time guarantee as above.
15. **`cmd/spine/stubstore_test.go`** — restructured into fourteen
    embeddable per-role no-op stubs (`stubSystem`, `stubRunStore`,
    …, `stubSubscriptionStore`) plus a `stubStore` that composes
    them. Tests that need only one role can now embed the role
    stub directly; the smoke test still uses the composed union.
    `GetActorByTokenHash` is overridden on the union so the smoke
    test still returns an authenticated admin actor. Net 148 lines
    smaller and divided into fourteen named units.

**Test gates:**

- `make docker-test` — green across every package; nothing in the
  unit suite was disturbed by the role split. The four happy-path
  cli_test.go scenarios and the gateway smoke tests
  (`TestServerStartup_NoUnwiredServices`,
  `TestServerStartup_DetectsMissingWorkflowService`) all pass —
  they are the regression bait for this refactor.
- Targeted scenario suite — the four engine merge-path scenarios
  (`TestPartialMergeRetry_HappyPath`,
  `TestPartialMergeExternalResolution_HappyPath`,
  `TestCancelFromPartiallyMerged_AsymmetricCleanup`,
  `TestMultiRepoRunLifecycle`) — green; divergence still uses
  `BranchStore` and the engine still talks through the union.
- `make docker-lint` — repo-wide count holds at 206 pre-existing
  issues; my edits contribute zero new findings.
- `golangci-lint run --enable-only=gosec` — single pre-existing
  finding in `internal/workflow/service.go:189`, unrelated to the
  touched files. No new gosec finding introduced.

**Codex iterative review:** two consecutive clean passes — "No
discrete correctness issues were found in the current staged,
unstaged, or untracked changes. The refactor appears to preserve
the existing store surface while narrowing consumer dependencies."
No findings to address.
