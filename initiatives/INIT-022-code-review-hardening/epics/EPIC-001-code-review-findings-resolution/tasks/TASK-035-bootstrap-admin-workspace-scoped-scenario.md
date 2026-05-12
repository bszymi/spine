---
id: TASK-035
type: Task
title: "Drive bootstrap-admin scenario through workspace-scoped gateway"
status: Pending
acceptance: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-018-scenario-bootstrap-admin-idempotency.md
---

# TASK-035 — Drive bootstrap-admin scenario through workspace-scoped gateway

---

## Purpose

`internal/scenariotest/scenarios/bootstrap_admin_idempotency_test.go:140-143`
wires the gateway with direct `Store`/`Auth` and no
`WorkspaceResolver`/`ServicePool`, then calls
`auth.BootstrapInternalAdmin` manually. In platform-binding mode —
the path this scenario is meant to protect — a request resolves a
workspace, builds a workspace-scoped service set, and bootstraps from
the pooled builder/env-derived `SMP_ADMIN_TOKEN`. If that wiring or
re-resolve path regresses, this scenario still passes.

This is a P2 scenario-harness finding from the 2026-05-12 codex
review of the INIT-022 batch (commit `404742faaa`).

## Deliverable

- Drive the request through a `WorkspaceResolver` / `ServicePool` (or
  the pooled builder used in production) so the HTTP auth assertions
  cover the workspace-scoped first-touch and reload paths.
- Pin the platform-binding shape end-to-end — at least one assertion
  must observe the bootstrap effect through a pooled service set, not
  the direct `Store`/`Auth` handles.

## Acceptance Criteria

- Reverting any wiring step in the pooled-builder bootstrap chain
  (e.g., the env-derived `SMP_ADMIN_TOKEN` plumbing or the resolver's
  service-set construction) fails this scenario.
- Hermeticity preserved — the scenario still runs without a real SMP
  binding by using the same fake binding shape the rest of the
  scenariotest pool uses.
- The original single-workspace fallback assertion remains for
  coverage of the non-platform mode.

## Out of Scope

- Changes to the production bootstrap flow itself.
- New harness primitives for pool wiring (use the helpers that already
  exist; if a gap blocks this task, surface it before adding one).
