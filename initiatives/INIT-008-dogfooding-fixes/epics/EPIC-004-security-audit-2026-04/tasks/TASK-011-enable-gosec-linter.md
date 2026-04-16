---
id: TASK-011
type: Task
title: "Enable gosec in golangci-lint config"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: chore
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-011 — Enable gosec In golangci-lint Config

---

## Purpose

`.golangci.yml` does not enable the `gosec` linter. Many common security smells (hardcoded credentials, weak crypto, SQL concat, unsafe file perms) go undetected in review. Cheapest durable improvement in the batch.

---

## Deliverable

- Add `gosec` to `linters.enable` in `.golangci.yml`.
- Triage the initial run: fix trivially, suppress with `//nolint:gosec // reason` on findings that are safe by construction (document each suppression).
- Ensure CI fails on new `gosec` findings.

---

## Acceptance Criteria

- `make lint` runs gosec and passes.
- Every `nolint:gosec` in the codebase has an inline reason.
- CI pipeline runs the updated linter set.
