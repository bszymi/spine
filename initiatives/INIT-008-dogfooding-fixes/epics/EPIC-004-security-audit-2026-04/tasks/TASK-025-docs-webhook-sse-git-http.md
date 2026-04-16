---
id: TASK-025
type: Task
title: "Document webhook delivery, SSE stream, pull event log, and Git HTTP endpoint"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: chore
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
  - type: related_to
    target: /initiatives/INIT-013-external-event-delivery/initiative.md
  - type: related_to
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-001-core-runtime/tasks/TASK-006-git-http-serve-endpoint.md
---

# TASK-025 — Document webhook delivery, SSE stream, pull event log, Git HTTP endpoint

---

## Purpose

TASK-024 refreshed documentation for the EPIC-004 security audit, but two major feature groups shipped in the last week remained undocumented outside their source tasks:

1. **External event delivery (INIT-013)** — webhook subscriptions CRUD, HMAC payload signing, retry/circuit-breaker behavior, SSE event stream, and pull-based event log.
2. **Git HTTP serve endpoint (INIT-009 / TASK-006)** — read-only git-HTTP backend for runner containers, with trusted-CIDR auth bypass.

Integrators building on either surface had no public reference; `docs/integration-guide.md` still claimed "Spine does not currently expose a real-time event stream."

---

## Deliverable

- Replace the stub §6 of `docs/integration-guide.md` with four subsections covering webhook CRUD + payload + retry + circuit breaker, SSE, pull event log, and the projection-polling fallback.
- Add §7 covering the Git HTTP serve endpoint — URL patterns (shared vs single mode), auth (trusted-CIDR + bearer), limits, observability.
- Update the environment-variable reference table with `SPINE_EVENT_DELIVERY` and refresh `SPINE_SSE_MAX_CONN_PER_ACTOR` default.
- Add an "Integrations" pointer block to the README so readers can jump from the front page.

---

## Acceptance Criteria

- Integration guide §6 documents every subscription route mounted in `internal/gateway/routes.go` and accurately describes the webhook headers, retry schedule, and circuit-breaker thresholds that match `internal/delivery/webhook_dispatcher.go` and `internal/delivery/circuit_breaker.go`.
- Integration guide §7 documents the `/git/{workspace_id}/*` endpoint shape, the `SPINE_GIT_HTTP_TRUSTED_CIDRS` bypass, and the concurrency/timeout defaults.
- `SPINE_EVENT_DELIVERY=true` is listed as the feature flag for the delivery system.
- README points readers at the new sections.
