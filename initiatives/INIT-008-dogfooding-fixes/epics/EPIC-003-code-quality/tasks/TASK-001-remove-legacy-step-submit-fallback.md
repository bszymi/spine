---
id: TASK-001
type: Task
title: "Remove legacy inline step submit fallback from gateway"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: implementation
created: 2026-04-04
last_updated: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-001 — Remove Legacy Inline Step Submit Fallback from Gateway

---

## Purpose

`handleStepSubmit` in `/internal/gateway/handlers_workflow.go` (lines 256-344) has a legacy inline fallback that reimplements core engine logic when `resultHandler` is nil. This fallback:

- Lacks required_outputs validation
- Swallows run transition and status update errors (lines 310-328)
- Has no event emission
- Has no retry logic, divergence handling, or rework cycle limits
- Creates a TOCTOU race on step status

This is the highest-risk duplication in the codebase because the legacy path silently produces different behavior than the engine path.

---

## Deliverable

1. Verify all deployments configure `resultHandler` (engine path)
2. Remove the legacy fallback code (lines 256-344)
3. Return 503 (service unavailable) when `resultHandler` is nil, matching the pattern used for `runStarter`
4. Remove the now-unused helper methods: `resolveNextStep`, `isReviewStep`, `resolveStepDef`
5. Remove `resolveWorkflowBinding` which is already dead code

---

## Acceptance Criteria

- `handleStepSubmit` always uses the engine path (`resultHandler.IngestResult`)
- Returns 503 when engine is not configured (not a silent fallback)
- Dead helper methods are removed
- All existing tests pass (update any tests that relied on the legacy path)
