---
id: INIT-013
type: Initiative
title: External Event Delivery
status: Completed
owner: bszymi
created: 2026-04-15
links:
  - type: related_to
    target: /architecture/adr/ADR-002-events.md
  - type: related_to
    target: /architecture/event-schemas.md
---

# INIT-013 — External Event Delivery

---

## Purpose

Spine emits rich domain and operational events internally (step transitions, artifact changes, run lifecycle, discussions), but these events never leave the process boundary. External systems — the Spine Management Platform, customer CI/CD pipelines, monitoring dashboards, audit systems — cannot react to Spine events in real-time.

This initiative builds a generic event delivery system that routes internal Spine events to external destinations. The system supports multiple delivery mechanisms (webhooks now, streaming and pull later) with per-workspace configuration, reliable delivery, and security.

## Motivation

**Immediate need:** The Spine Management Platform's runner dispatch system (SMP ADR-007) currently polls the Spine projection DB every 5 seconds to detect automated step transitions. A push-based model where Spine notifies the platform on `step_assigned` would eliminate this latency and reduce unnecessary API calls.

**General value:** Customers building on top of Spine need real-time event notifications for:
- CI/CD triggers (run started → deploy pipeline)
- Slack/Teams notifications (step completed, review needed)
- Audit logging to external SIEM systems
- Custom dashboards and metrics
- Integration with third-party project management tools
- Automated workflows triggered by artifact changes

## Scope

### In Scope

- **EPIC-001: Webhook Delivery** — HTTP POST to configured URLs on events, with HMAC signing
- **EPIC-002: Event Subscription Configuration** — per-workspace webhook configuration via API, event type filtering, URL routing
- **EPIC-003: Delivery Reliability** — persistent queue, retry with exponential backoff, dead letter handling, delivery logging
- **EPIC-004: Streaming and Pull** — future delivery mechanisms (SSE endpoint, Kafka connector, pull-based event log API)

### Out of Scope

- Transforming event payloads (consumers receive the canonical Spine event schema)
- Building specific integrations (Slack, Jira, etc.) — those are consumer-side
- Modifying the internal event system (ADR-002) — we subscribe to it, don't change it

## Event Types Available for Delivery

All events defined in `architecture/event-schemas.md`:

### Domain Events (at-least-once delivery)
| Event | Description |
|-------|-------------|
| `artifact_created` | New artifact committed to Git |
| `artifact_updated` | Artifact metadata or content changed |
| `run_started` | Workflow run initiated |
| `run_completed` | Run finished successfully |
| `run_failed` | Run terminated with failure |
| `run_cancelled` | Run cancelled by actor |
| `run_paused` | Run paused |
| `run_resumed` | Run resumed from pause |
| `run_timeout` | Run exceeded timeout |
| `step_assigned` | Step assigned to actor (key event for runner dispatch) |
| `step_started` | Actor acknowledged step (in_progress) |
| `step_completed` | Step completed with outcome |
| `step_failed` | Step failed |
| `step_timeout` | Step exceeded timeout |
| `retry_attempted` | Step retry initiated |

### Operational Events (best-effort delivery)
| Event | Description |
|-------|-------------|
| `projection_synced` | Projection database updated |
| `thread_created` | Discussion thread created |
| `comment_added` | Comment posted to discussion |
| `thread_resolved` | Discussion thread resolved |
| `validation_passed` | Artifact validation passed |
| `validation_failed` | Artifact validation failed |
| `step_assignment_failed` | Auto-assignment could not find eligible actor |
| `task_unblocked` | Task dependency resolved |
| `task_released` | Task released from actor |

## Design Principles

1. **Subscribe, don't modify** — The external delivery system subscribes to the existing EventRouter. No changes to event emission logic.
2. **At-least-once for domain events** — Domain events are persisted and retried until acknowledged. Consumers must be idempotent.
3. **Best-effort for operational events** — Operational events are delivered if possible but not retried indefinitely.
4. **Per-workspace isolation** — Each workspace configures its own subscriptions. No cross-workspace event leakage.
5. **Security first** — Webhook payloads are HMAC-signed. Secrets are per-subscription. TLS required for non-localhost URLs.
6. **Future-proof** — The subscription model is delivery-mechanism-agnostic. Adding Kafka or SSE later doesn't require reconfiguring subscriptions.

## Success Criteria

1. SMP runner dispatch receives `step_assigned` events within 1 second of Spine step transition
2. Webhooks are configurable per workspace via the API with event type filtering
3. Failed deliveries are retried with exponential backoff (max 5 retries)
4. Delivery history is queryable for debugging
5. Event payloads match the canonical schemas in `event-schemas.md`
6. No degradation of Spine's core performance from event delivery
