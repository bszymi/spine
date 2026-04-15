---
id: TASK-001
type: Task
title: "Define subscription data model and database schema"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/epic.md
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/epic.md
---

# TASK-001 — Define Subscription Data Model and Database Schema

## Purpose

Define the subscription entity and its database representation. A subscription describes where events should be delivered, which event types to include, and the authentication mechanism.

## Deliverable

Domain model and migration for `event_subscriptions` table:

- subscription_id (UUID)
- workspace_id (text, nullable — null for internal/system subscriptions)
- name (text, human-readable label)
- target_type (text: "webhook", extensible for "kafka", "sse" later)
- target_url (text, the webhook URL)
- event_types (text[], list of event types to deliver, empty = all)
- signing_secret (text, encrypted, for HMAC)
- status (text: active, paused, disabled)
- metadata (jsonb, extensible config: headers, auth type, etc.)
- created_by (text, actor_id)
- created_at, updated_at

Constraints: unique (workspace_id, name), target_url required for webhook type.

## Acceptance Criteria

- Model supports webhook subscriptions with event filtering
- Schema is extensible for future delivery types (Kafka, SSE)
- Signing secrets stored securely (not plaintext)
- Internal subscriptions supported via null workspace_id
