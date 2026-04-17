---
id: TASK-002
type: Task
title: "Extract notFoundOr / mustAffect / queryAll helpers in internal/store"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-002 — Extract Store NotFound / Affect / QueryAll Helpers

---

## Purpose

`internal/store/postgres.go` has 15 `if err == pgx.ErrNoRows { return domain.NewError(ErrNotFound, ...) }` sites and 14 `if tag.RowsAffected() == 0` sites (L139, L159, L272, L295, L405, L475, L610, L968, L1069, L1203, L1419, L1638, L1828, L1892, plus others). "List by X" queries re-implement the same `pool.Query → defer rows.Close → for rows.Next → scan → append` loop at every site (e.g. `ListTokensByActor`, `ListAssignmentsByActor`, `ListComments`, `ListBranchesByDivergence`). EPIC-003/TASK-004 already introduced per-concern `scanRun/scanRuns` helpers; this task extends that pattern to the remaining three shapes.

---

## Deliverable

1. Add `internal/store/pgutil.go` (same package) with:
   - `notFoundOr(err error, entity string) error` — maps `pgx.ErrNoRows` to `domain.ErrNotFound`, passes through otherwise.
   - `mustAffect(tag pgconn.CommandTag, entity string) error` — returns `ErrNotFound` when `RowsAffected()==0`.
   - `queryAll[T any](ctx, pool, sql string, args []any, scan func(pgx.Rows, *T) error) ([]T, error)` — generic rows loop.
2. Rewrite all matching sites in `postgres.go` to use the helpers. Keep error-message entity strings stable (grep and compare).
3. Ensure no behaviour change: identical error codes and messages for existing store integration tests.

---

## Acceptance Criteria

- `postgres.go` drops at least 100 LOC.
- No site in `postgres.go` does manual `pgx.ErrNoRows` mapping or `RowsAffected()==0` checks outside `pgutil.go`.
- Existing store integration tests pass unchanged.
- Scenario tests pass.
