---
id: TASK-004
type: Task
title: Store Interface, PostgreSQL Implementation, and Migrations
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
---

# TASK-004 — Store Interface, PostgreSQL Implementation, and Migrations

## Purpose

Implement the database access layer and create initial migrations for all runtime and projection tables.

## Deliverable

- `internal/store/store.go` — Store interface (per Implementation Guide §3.4)
- `internal/store/postgres.go` — PostgreSQL implementation using pgx
- `internal/store/testutil.go` — Test helpers (test DB, transaction rollback per test)
- `migrations/001_initial_schema.sql` — All tables from Runtime Schema §3-4
- Transaction support (WithTx)
- Connection pooling and health check

## Acceptance Criteria

- All Store interface methods implemented
- Migrations create all tables matching runtime-schema.md (projection + runtime schemas)
- Integration tests pass against real PostgreSQL
- Each test runs in a rolled-back transaction (no shared state)
- Connection pool handles concurrent access
- `spine migrate` CLI command applies migrations
