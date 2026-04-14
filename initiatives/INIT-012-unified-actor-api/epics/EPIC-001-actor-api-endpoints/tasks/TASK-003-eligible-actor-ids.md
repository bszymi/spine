---
id: TASK-003
type: Task
title: "Add eligible_actor_ids to Step Executions"
status: Pending
work_type: implementation
created: 2026-04-14
epic: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
  - type: depends_on
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/tasks/TASK-002-step-execution-query.md
---

# TASK-003 — Add eligible_actor_ids to Step Executions

---

## Context

Workflow steps have `eligible_actor_types` which controls what type of actor can claim them. With the unified actor model, workspaces can have multiple automation actors (different sizes/capabilities). When a step is assigned to a specific actor, other actors of the same type should not be able to claim it.

## Deliverable

### Domain model

Add `EligibleActorIDs` to step execution or workflow step:

```go
type StepExecution struct {
    // ... existing fields ...
    EligibleActorIDs []string `json:"eligible_actor_ids,omitempty"`
}
```

### Claim enforcement

When claiming a step (POST /execution/claim):
- If `eligible_actor_ids` is empty: any actor of the right type can claim (backward compatible)
- If `eligible_actor_ids` is set: only those actors can claim
- Reject claims from actors not in the list (403)

### Step query filtering

The step-execution query (TASK-002) must also filter by `eligible_actor_ids`:
- Return steps where `eligible_actor_ids` is empty OR contains the requested actor_id

### How it gets set

The platform sets `eligible_actor_ids` when the automation definition specifies an `actor_id`. This is passed through the run start or step creation API.

## Acceptance Criteria

- Step executions can have eligible_actor_ids
- Claims rejected if actor not in eligible list
- Step query filters by actor_id correctly
- Empty eligible_actor_ids = any actor of the right type (backward compatible)
