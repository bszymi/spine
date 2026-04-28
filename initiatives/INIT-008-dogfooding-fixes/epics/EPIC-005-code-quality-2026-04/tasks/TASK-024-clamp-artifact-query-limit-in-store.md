---
id: TASK-024
type: Task
title: Clamp artifact query limit in store
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-28
last_updated: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-024 — Clamp artifact query limit in store

---

## Purpose

Code-quality review finding: `internal/store/postgres_projections.go` caps artifact query limits only at the HTTP handler layer. `QueryArtifacts` is a reusable store boundary and directly interpolates `query.Limit + 1` into the SQL after only defaulting non-positive values.

This task makes the store method defensive so internal callers cannot accidentally request unbounded or very large result sets.

## Deliverable

- Add store-level limit clamping for `QueryArtifacts`.
- Keep HTTP pagination behavior unchanged.
- Prefer shared constants or a small helper if the handler and store should share the same default/min/max values.
- Add tests or focused coverage for default, low, high, and normal limits.

## Acceptance Criteria

- `QueryArtifacts` clamps overly large limits before building SQL.
- `QueryArtifacts` still defaults non-positive limits to the existing default.
- HTTP artifact list responses keep the current pagination contract.
- Tests cover the store-level limit behavior.
- `go test ./internal/store ./internal/gateway` passes.
