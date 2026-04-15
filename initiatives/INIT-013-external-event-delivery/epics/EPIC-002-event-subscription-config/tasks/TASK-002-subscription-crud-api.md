---
id: TASK-002
type: Task
title: "Implement subscription CRUD API"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/epic.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/epic.md
  - type: depends_on
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/tasks/TASK-001-subscription-model-and-schema.md
---

# TASK-002 — Implement Subscription CRUD API

## Purpose

HTTP API for managing event subscriptions. Workspace admins configure where their events go.

## Deliverable

Endpoints under `/api/v1/subscriptions`:

- `POST /` — create subscription (name, target_url, event_types, optional metadata)
- `GET /` — list subscriptions (filtered by workspace from auth context)
- `GET /{id}` — get subscription details (excludes signing_secret)
- `PATCH /{id}` — update subscription (name, target_url, event_types, metadata)
- `DELETE /{id}` — delete subscription
- `POST /{id}/activate` — set status to active
- `POST /{id}/pause` — set status to paused
- `POST /{id}/rotate-secret` — generate new signing secret, return it once
- `POST /{id}/test` — send a test event to the webhook URL and return result

Authorization: workspace-scoped, admin role required.

## Acceptance Criteria

- Full CRUD lifecycle works
- Signing secret returned only on create and rotate (never on GET)
- Test endpoint sends a `ping` event and reports success/failure
- Subscription scoped to workspace (no cross-workspace access)
