---
id: EPIC-002
type: Epic
title: "Event Subscription Configuration"
status: Pending
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/initiative.md
  - type: depends_on
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
---

# EPIC-002 — Event Subscription Configuration

---

## Purpose

Allow workspaces to configure which events are delivered where. Each workspace can have multiple subscriptions, each targeting a specific URL with a filtered set of event types. Configuration is managed via the Spine API.

## Key Work Areas

- Subscription data model (workspace-scoped, with URL, event filters, signing secret)
- CRUD API endpoints for managing subscriptions
- Event type filtering (include/exclude lists, wildcard patterns)
- Signing secret generation and rotation
- Subscription activation/deactivation
- Internal subscriptions for platform services (e.g., SMP runner dispatch)

## Acceptance Criteria

- Subscriptions are per-workspace
- Each subscription specifies a target URL and event type filter
- Signing secrets are generated securely and stored encrypted
- API supports create, list, get, update, delete, activate, deactivate
- Internal subscriptions (SMP) can be configured via environment variables (no API call needed)
