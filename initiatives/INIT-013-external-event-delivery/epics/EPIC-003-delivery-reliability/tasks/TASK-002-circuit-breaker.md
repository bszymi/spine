---
id: TASK-002
type: Task
title: "Implement circuit breaker for failing endpoints"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-003-delivery-reliability/epic.md
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-003-delivery-reliability/epic.md
---

# TASK-002 — Implement Circuit Breaker for Failing Endpoints

## Purpose

When a webhook endpoint fails consistently, stop hammering it. Circuit breaker prevents wasted requests and gives the endpoint time to recover.

## Deliverable

- Track consecutive failures per subscription
- After 10 consecutive failures: trip circuit (stop delivery attempts)
- After 60 seconds: half-open (try one delivery)
- On half-open success: close circuit (resume normal delivery)
- On half-open failure: re-trip (wait another 60s)
- Circuit state stored in memory (reset on restart — conservative, not a problem)
- Log circuit state transitions

## Acceptance Criteria

- Circuit trips after 10 consecutive failures
- No deliveries attempted while circuit is open
- Half-open probe after 60 seconds
- Successful probe closes circuit
