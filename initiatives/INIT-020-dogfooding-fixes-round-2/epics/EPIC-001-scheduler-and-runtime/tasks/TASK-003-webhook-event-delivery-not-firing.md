---
id: TASK-003
type: Task
title: "Webhook event delivery not firing in platform-binding mode"
status: Pending
epic: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
work_type: bugfix
created: 2026-04-30
last_updated: 2026-04-30
links:
  - type: parent
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-013-external-event-delivery/initiative.md
---

# TASK-003 — Webhook event delivery not firing in platform-binding mode

---

## Purpose

INIT-013 wired Spine's external webhook delivery system: `runtime.event_log`, `runtime.event_subscriptions`, `runtime.event_delivery_queue`, signing-secret-protected delivery, retry-with-backoff, etc. In single-workspace dev stacks this works.

In platform-binding mode (SMP's `WORKSPACE_RESOLVER=platform-binding`, per-workspace runtime DB) it doesn't fire. Observed against SMP on 2026-04-30:

- `SPINE_EVENT_DELIVERY=true` is set on the spine container.
- A subscription is created via `POST /api/v1/subscriptions` with `target_url = http://customer-app:8080/internal/step-events`, `event_types = [step.assigned, step.completed, step.failed, run.completed, run.failed]`, status `active`. Row lands in `runtime.event_subscriptions` correctly.
- A run is started, execute step submitted, validate step is assigned. Spine's gateway logs show:

  ```
  level=INFO msg="assignment delivered" component=gateway
    assignment_id=run-088bfa3e-validate-1 actor_id=actor-9fe8871d
    step_id=validate
  ```

  …but nothing in `runtime.event_log` (`SELECT count(*) → 0`) and nothing in `runtime.event_delivery_queue` (`SELECT count(*) → 0`).

So the engine is producing `assignment delivered` log lines but never writing the corresponding event row, so the delivery worker never enqueues, so the customer-app's `/internal/step-events` endpoint is never called.

Likely causes (in order of suspicion, all subject to verification):

1. The event router / event log writer isn't installed on the per-workspace `ServiceSet` (parallel to TASK-002's gateway-handler issue). The top-level `event.QueueRouter` is the one wired in `cmd/spine/cmd_serve.go`; the per-workspace one in `ServiceSet.Events` may be a different instance that doesn't have the persistent `eventLogWriter` plumbed in.
2. `SPINE_EVENT_DELIVERY` is read at process boot from env, but the subscription/delivery writer is gated on a top-level config flag that doesn't propagate into the per-workspace pool.
3. The `EventDeliverySubscriber` is constructed once at startup and binds to the top-level `event.QueueRouter`, not to each workspace's router, so per-workspace events never reach it.

## Deliverable

- Audit `cmd/spine/cmd_serve.go` and `internal/workspace/pool.go` for how `event.QueueRouter` and `EventDeliverySubscriber` are constructed and which one each domain operation publishes to.
- In platform-binding mode, ensure: every workspace-scoped engine operation publishes to a router that:
  - Persists events into the per-workspace `runtime.event_log` table.
  - Has the delivery subscriber (or per-workspace delivery worker) draining `runtime.event_delivery_queue` against active subscriptions.
- Add an integration-style scenariotest that asserts: after starting a run and submitting `execute=completed` against a multi-workspace stack, a delivery row lands in the workspace's `event_delivery_queue` within N seconds.

## Acceptance Criteria

- After creating a subscription with a webhook target on a platform-binding stack, completing an `execute` step causes a row to land in `runtime.event_log` for `step.completed` and `step.assigned` events.
- The delivery worker dequeues the corresponding rows from `runtime.event_delivery_queue` and posts to the subscription target. SMP's `/internal/step-events` receives the POST with a valid HMAC signature.
- Existing single-workspace event delivery continues to function (regression-free).
- A scenariotest covers the platform-binding event-delivery path end-to-end.

## Out of Scope

- Customer-app's `/internal/step-events` receiver and signature verification (SMP-side).
- The `EventReceiver` enqueue logic that follows `step.assigned` into the runner job queue (SMP-side).
- Webhook host allowlisting policy (`SPINE_WEBHOOK_ALLOWED_HOSTS`) — the validator works correctly; what's missing is the delivery, not the URL acceptance.
