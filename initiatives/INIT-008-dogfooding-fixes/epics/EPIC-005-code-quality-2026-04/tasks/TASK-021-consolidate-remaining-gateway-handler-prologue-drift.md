---
id: TASK-021
type: Task
title: Consolidate remaining gateway handler prologue drift
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-24
last_updated: 2026-04-24
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/tasks/TASK-007-gateway-handler-prologue-helpers.md
---

# TASK-021 — Consolidate remaining gateway handler prologue drift

---

## Purpose

The gateway has prologue helpers for authorization, JSON decoding, and dependency checks, but many handlers still spell out the same sequence manually. That makes new endpoints more likely to drift on authorization order, body-size enforcement, or "service not configured" behavior.

## Deliverable

- Review `internal/gateway/handlers_*.go` for manual `authorize -> decodeBody/decodeJSON -> need service` patterns.
- Replace straightforward cases with `decodeAuthedJSON`, `need*` helpers, or narrowly-scoped new helpers.
- Leave handlers manual only when their flow materially differs, with a short comment explaining why.
- Add or update smoke tests so each route still fails closed when its required dependency is not configured.

## Acceptance Criteria

- Handler prologues use the shared helper shape wherever behavior is identical.
- Dependency-not-configured responses remain consistent with the canonical helper messages.
- Optional-body endpoints still accept empty bodies where intended.
- `go test ./internal/gateway ./cmd/spine` passes.
