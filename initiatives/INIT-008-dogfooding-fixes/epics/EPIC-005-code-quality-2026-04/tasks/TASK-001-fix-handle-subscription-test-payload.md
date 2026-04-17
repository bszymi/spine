---
id: TASK-001
type: Task
title: "Fix handleSubscriptionTest — attach ping payload and drop orphaned request"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-17
last_updated: 2026-04-17
completed: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-001 — Fix handleSubscriptionTest Payload Wiring

---

## Purpose

`internal/gateway/handlers_subscriptions.go` L340-378 advertises a "ping" subscription test but sends an empty POST. L340-347 constructs `req` with headers, L351 overwrites its body with `nil`, and L352 creates a second `testReq` which is the request actually dispatched (L357). The first `req` is orphaned. `pingPayload` built at L331 is never attached; L371 is `_ = pingPayload` with a comment *"used conceptually"*. Subscribers receive an empty body when operators hit the test endpoint.

---

## Deliverable

1. Remove the orphaned `req` construction at L340-351.
2. Attach `bytes.NewReader(pingPayload)` to `testReq.Body` and set `Content-Length` and `Content-Type: application/json`.
3. Delete the `_ = pingPayload` placeholder line and its stale comment.
4. Add a small test that asserts the dispatched request body decodes to the expected ping shape.

---

## Acceptance Criteria

- `POST /subscriptions/{id}/test` delivers a request whose body is the documented ping payload.
- New unit test covers the dispatched body shape.
- Existing gateway tests pass.
