---
id: TASK-002
type: Task
title: "Execution Candidate Discovery API"
status: Completed
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-06
completed: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/tasks/TASK-001-dependency-blocking-detection.md
---

# TASK-002 — Execution Candidate Discovery API

---

## Purpose

Provide an API to query tasks that are ready for execution based on actor type, skill eligibility, and dependency status. This API will be consumed by AI execution engines and human dashboards.

---

## Deliverable

1. Implement service-level query:
   ```go
   FindExecutionCandidates(ctx, filter ExecutionCandidateFilter) ([]ExecutionCandidate, error)
   ```

2. `ExecutionCandidateFilter` supports:
   - `ActorType` — filter by allowed actor type
   - `Skills` — filter by required skill match
   - `WorkspaceID` — scope to workspace
   - `IncludeBlocked` — optionally include blocked tasks (default: exclude)

3. `ExecutionCandidate` includes:
   - Task path, ID, title, status
   - Required skills
   - Blocked status and blockers
   - Workflow step context

4. Expose via gateway REST endpoint:
   - `GET /api/v1/execution/candidates?actor_type=ai_agent&skills=backend_development`

5. Update documentation:
   - Update `/architecture/api-operations.md` to document the execution candidates endpoint
   - Update `/architecture/access-surface.md` to include the candidates discovery API in the access surface
   - Update `/architecture/actor-model.md` to describe how actors discover available work

---

## Acceptance Criteria

- API returns only tasks that are ready for execution (not blocked, correct state)
- Filtering by actor type excludes tasks with incompatible execution modes
- Filtering by skills excludes tasks where the actor lacks required skills
- Empty filters return all ready tasks
- Response includes enough context for an actor to decide whether to claim
- Integration tests cover filter combinations
- Architecture documentation is updated to reflect execution candidate discovery
