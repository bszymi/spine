---
id: TASK-010
type: Task
title: "Split internal/store.Store into role-specific interfaces"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-07
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
