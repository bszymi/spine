---
id: TASK-003
type: Task
title: "Support internal subscriptions via environment config"
status: Completed
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/epic.md
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/epic.md
  - type: blocked_by
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/tasks/TASK-001-subscription-model-and-schema.md
---

# TASK-003 — Support Internal Subscriptions via Environment Config

## Purpose

The Spine Management Platform needs to receive step_assigned events without manually creating subscriptions through the API. An internal subscription should be bootstrapped from environment variables on Spine startup.

## Deliverable

- On startup, if `SMP_EVENT_URL` is set, create/update an internal subscription:
  - workspace_id: from `SMP_WORKSPACE_ID`
  - target_url: `SMP_EVENT_URL` (e.g., `http://customer-app:8080/internal/step-events`)
  - event_types: `[step_assigned, step_completed, step_failed, run_completed, run_failed, run_cancelled]`
  - signing_secret: derived from `SMP_INTERNAL_TOKEN`
  - status: active
- Internal subscriptions have null workspace_id (system-level) or specific workspace_id
- Idempotent: re-running startup doesn't create duplicates

## Acceptance Criteria

- SMP receives step_assigned events without manual API configuration
- Internal subscription survives Spine restarts (persisted in DB)
- Environment-driven config matches existing SMP_* pattern (SMP_CREDENTIAL_URL, SMP_INTERNAL_TOKEN)
