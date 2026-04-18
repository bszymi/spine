---
id: TASK-005
type: Task
title: "Define governance workflow for editing branch-protection config"
status: Pending
work_type: design+implementation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
---

# TASK-005 — Define governance workflow for editing branch-protection config

---

## Purpose

Pick and land the workflow that governs edits to `/.spine/branch-protection.yaml`. ADR-009 deferred this to a follow-up: either a dedicated `branch-protection-lifecycle.yaml` or inclusion of the file under the existing workflow-lifecycle scheme.

Without this, the config is technically editable only via operator override — which defeats the goal of "the rules are governed by the same machinery as any other artifact."

---

## Context

[ADR-009 §5](/architecture/adr/ADR-009-branch-protection.md):

> The specific governing workflow — a dedicated `branch-protection-lifecycle.yaml`, or just bracketing the file under the existing `workflow-lifecycle.yaml`-style scheme — is an implementation decision for the follow-up epic, not this ADR.

[ADR-008](/architecture/adr/ADR-008-workflow-lifecycle-governance.md) sets the precedent: changes to a governance file flow through a named workflow with review + merge approval.

---

## Deliverable

1. **Short design note** (in the task body or as a tmp/ draft) comparing the two options:
   - Dedicated `/workflows/branch-protection-lifecycle.yaml` — easier to evolve, distinct review step.
   - Bracket under workflow-lifecycle — fewer moving parts, leverages existing machinery.

   Pick one, state why, note what the loser forecloses.

2. **Workflow definition.** Whichever option is chosen, land the YAML under `/workflows/` following the existing format (see `/architecture/workflow-definition-format.md`). Requires at minimum: a reviewer approval step before merge.

3. **Wiring.** The path-to-workflow binding that associates `/.spine/branch-protection.yaml` with this workflow. Mirrors how `/workflows/*.yaml` changes already route to workflow-lifecycle.

4. **Test.** End-to-end: edit to `branch-protection.yaml` on a planning-run branch triggers the chosen workflow, requires reviewer approval, and only then becomes merge-eligible.

5. **ADR update.** Edit ADR-009 §5 to replace the "deferred" language with the resolved choice, plus a link to the new workflow file.

---

## Acceptance Criteria

- A named workflow governs edits to `/.spine/branch-protection.yaml`; the choice is recorded in ADR-009.
- End-to-end test demonstrates that an edit lands only through the governing workflow (absent operator override).
- The bootstrap-deadlock escape hatch from ADR-009 §5 (operator override) still works — this task must not inadvertently close it.
- Workflow file passes the existing workflow-definition validation.
