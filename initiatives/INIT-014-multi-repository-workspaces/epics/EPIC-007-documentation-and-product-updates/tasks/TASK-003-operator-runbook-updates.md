---
id: TASK-003
type: Task
title: Operator runbook updates for multi-repo lifecycle
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: documentation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-007-documentation-and-product-updates/tasks/TASK-002-architecture-docs-sync.md
---

# TASK-003 - Operator Runbook Updates for Multi-Repo Lifecycle

---

## Purpose

Give operators the runbook they need to register code repositories, recover from partial-merge runs, rotate credentials, and deregister repos cleanly.

## Deliverable

Add or update operator-facing documentation covering:

- Registering a code repository through the API and CLI, including credential reference setup.
- Inspecting catalog vs runtime binding state.
- Recovering from a partial-merge run via the EPIC-005 manual-resolution and retry path.
- Rotating credentials referenced by a runtime binding without disrupting in-flight runs.
- Deregistering a repository.

## Acceptance Criteria

- Each lifecycle operation has a step-by-step runbook entry with example commands.
- Failure modes are documented (unresolved credential, inactive repo, conflicting merges).
- Runbook entries link to the relevant API/CLI reference and ADRs.
- A validated end-to-end runbook walkthrough exists for the partial-merge recovery flow.
