---
id: TASK-001
type: Task
title: "Add Actor Registration HTTP Endpoint"
status: Completed
work_type: implementation
created: 2026-04-14
epic: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
---

# TASK-001 — Add Actor Registration HTTP Endpoint

---

## Context

Spine has `actor.Service.Register()` internally but no HTTP endpoint. The Spine Management Platform calls `POST /api/v1/actors` during runner registration — this currently returns 404.

## Deliverable

### Route

```go
r.Post("/actors", s.handleActorCreate)
```

### Request

```json
{
  "actor_id": "runner-abc12345",
  "type": "automated_system",
  "name": "Runner linux_small (abc12345)",
  "role": "contributor"
}
```

- `actor_id` is optional — auto-generate if empty
- `type` must be one of: human, ai_agent, automated_system
- `role` defaults to contributor

### Response

201 Created with the actor object. 409 if actor_id already exists.

### Authorization

Requires `actor.create` permission (admin or operator tokens).

## Acceptance Criteria

- POST /api/v1/actors creates an actor via actor.Service.Register()
- Auto-generates actor_id if not provided
- Returns 409 on duplicate actor_id
- Platform's spineproxy.Client.CreateActor() works against this endpoint
