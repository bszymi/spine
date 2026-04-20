---
id: TASK-004
type: Task
title: "Seed /.spine/branch-protection.yaml in spine init-repo"
status: Completed
work_type: implementation
created: 2026-04-18
last_updated: 2026-04-19
completed: 2026-04-19
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/tasks/TASK-001-config-schema-and-parser.md
---

# TASK-004 — Seed /.spine/branch-protection.yaml in spine init-repo

---

## Purpose

Make the bootstrap defaults visible and editable the moment a repository is created. The policy engine already handles the "no file" case by falling back to defaults (TASK-002/TASK-003), so the seed is a UX aid — a new operator does not have to synthesize the file from ADR-009 to customize protection.

---

## Context

[ADR-009 §1](/architecture/adr/ADR-009-branch-protection.md):

> `spine init-repo` seeds the file so the defaults become explicit and editable, but the enforcement layer is correct even when the file is missing.

Whatever command currently bootstraps a workspace (the one that creates `.spine.yaml`, the initial `/governance/` docs, etc.) is the one to extend here. Locate it via the existing `cmd/spine/` entry points.

---

## Deliverable

1. **Seed content.** A canonical `/.spine/branch-protection.yaml` matching the bootstrap defaults, with an inline comment pointing at ADR-009 for the full schema and the operator-edit flow resolved in TASK-005 / ADR-009 §5:

   ```yaml
   # Branch-protection rules. See /architecture/adr/ADR-009-branch-protection.md
   # and /architecture/branch-protection-config-format.md for the schema.
   # This file is operator-edited (ADR-009 §5): commit the change directly
   # on the authoritative branch and push with `git push -o spine.override=true`.
   # No lifecycle workflow governs this file; the override emits a
   # branch_protection.override governance event that is the audit record.
   version: 1
   rules:
     - branch: main
       protections: [no-delete, no-direct-write]
   ```

2. **init-repo extension.** The existing repo-bootstrap command writes the file as part of its usual output. Idempotent: if the file already exists, do not overwrite.

3. **Tests.**
   - `init-repo` on a fresh directory: file exists, parses cleanly via TASK-001's parser.
   - `init-repo` on a directory that already has the file: contents preserved.
   - Round-trip check: parsed seed equals `BootstrapDefaults()` from TASK-002.

---

## Acceptance Criteria

- New repositories initialized via `spine init-repo` contain `/.spine/branch-protection.yaml` with the documented defaults.
- Existing repositories are not overwritten.
- A round-trip test pins the seed contents to `branchprotect.BootstrapDefaults()` so the two can never drift silently.
- No new CLI flags introduced — this is a quiet addition to the existing bootstrap flow.
