---
id: TASK-014
type: Task
title: "Verify actor ownership in step-submit handler"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-014 — Verify Actor Ownership In Step-Submit Handler

---

## Purpose

`internal/gateway/handlers_workflow.go:236-270` (`handleStepSubmit`) extracts `executionID` from the URL and calls `resultHandler.IngestResult` without an explicit check that the authenticated actor is the assigned executor. Ownership validation today depends on `resultHandler`. If that contract regresses, any authenticated actor could submit results for another actor's execution.

---

## Deliverable

- Load the step execution in the gateway handler.
- Compare `execution.ActorID` to the authenticated actor ID; return 403 on mismatch.
- Keep the downstream `IngestResult` check as defense-in-depth.

---

## Acceptance Criteria

- Integration test: actor A submits for actor B's execution → 403.
- Happy path (actor submits own execution) unaffected.
