---
id: TASK-004
type: Task
title: "Wire delivery system into Spine server"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
  - type: depends_on
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/tasks/TASK-003-webhook-dispatcher.md
---

# TASK-004 — Wire Delivery System into Spine Server

## Purpose

Connect the delivery subscriber and webhook dispatcher to the Spine server startup. The system should start automatically and begin delivering events to configured webhooks.

## Deliverable

- In `cmd/spine/main.go`: initialize delivery subscriber, start webhook dispatcher goroutine
- Subscribe to EventRouter for all event types
- Start dispatcher as a background goroutine (shutdown-aware via context)
- Log delivery system startup and any configuration issues
- Feature flag: `SPINE_EVENT_DELIVERY=true` (default false until stable)

## Acceptance Criteria

- Spine starts with delivery system when flag is enabled
- Events flow from EventRouter → subscriber → queue → dispatcher → webhook
- Graceful shutdown: dispatcher finishes in-flight deliveries before exit
- No impact on Spine startup time or core performance when disabled
