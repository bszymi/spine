---
id: EPIC-001
type: Epic
title: Architecture & ADR
status: In Progress
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
owner: bszymi
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
---

# EPIC-001 — Architecture & ADR

---

## 1. Purpose

Design the planning run model and capture the architectural decision in ADR-006.

This epic must complete before implementation begins to ensure the design is reviewed and consistent with Spine's existing architecture.

---

## 2. Scope

### In Scope

- ADR-006: Planning Runs decision record
- Updates to `api-operations.md` (authoritative vs proposed writes section)
- Updates to `engine-state-machine.md` (run mode field)
- Updates to `git-integration.md` (planning branch semantics)
- Updates to `workflow-definition-format.md` (initiative workflow reference)

### Out of Scope

- Implementation code
- Test code

---

## 3. Success Criteria

1. ADR-006 is written and accepted
2. Architecture docs are updated and internally consistent
3. Design is reviewable by a new contributor

---

## 4. Primary Outputs

- `/architecture/adr/ADR-006-planning-runs.md`
- Updated architecture documents

---

## 5. Related Artifacts

- `/governance/constitution.md` — §4, §6, §7
- `/architecture/api-operations.md`
- `/architecture/engine-state-machine.md`
