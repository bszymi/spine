---
id: TASK-002
type: Task
title: Update architecture documentation
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-001-architecture-and-adr/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-001-architecture-and-adr/epic.md
---

# TASK-002 — Update Architecture Documentation

---

## Purpose

Update the architecture documents that are affected by the planning run feature.

---

## Deliverable

Updates to the following files:

- `architecture/api-operations.md` — add planning runs to the authoritative vs proposed writes section (§2.3). Document that planning runs produce proposed writes that include artifact creation.
- `architecture/engine-state-machine.md` — add note that runs have a `mode` field. Planning runs follow the same state machine but artifacts are branch-local until merge.
- `architecture/git-integration.md` — add section on planning run branch semantics: branch contains the artifact creation commit as its first commit, followed by child artifact writes.
- `architecture/workflow-definition-format.md` — document `initiative-lifecycle` as a reference workflow. Mention that workflows can govern artifact creation via planning runs.

---

## Acceptance Criteria

- All four documents are updated
- Updates are consistent with ADR-0006
- No contradictions with existing content
- Changes are minimal and focused — no rewriting of unrelated sections
