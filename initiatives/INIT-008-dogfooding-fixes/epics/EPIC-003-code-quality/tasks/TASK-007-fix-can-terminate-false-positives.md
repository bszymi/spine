---
id: TASK-007
type: Task
title: "Fix canTerminate false-positives rejecting valid divergence workflows"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-007 — Fix canTerminate False-Positives Rejecting Valid Divergence Workflows

---

## Purpose

`canTerminate` in `/internal/workflow/parser.go` (lines 272-284) checks each step independently for a path to `"end"` instead of traversing the workflow graph from the entry point. It ignores divergence and convergence edges. Valid workflows using divergence can be rejected as non-terminating.

---

## Deliverable

Rewrite `canTerminate` to traverse the workflow graph from the entry point, following divergence branch `StartStep` edges and convergence `EvaluationStep` edges.

---

## Acceptance Criteria

- Valid workflows with divergence/convergence pass termination validation
- Invalid non-terminating workflows are still rejected
- Existing parser tests pass; add tests for divergence workflows
