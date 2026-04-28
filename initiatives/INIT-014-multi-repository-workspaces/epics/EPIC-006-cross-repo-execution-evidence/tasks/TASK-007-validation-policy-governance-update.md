---
id: TASK-007
type: Task
title: Register validation policy as a governed artifact type
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: design
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-002-adr-linked-validation-policy-format.md
---

# TASK-007 - Register Validation Policy as a Governed Artifact Type

---

## Purpose

The validation policy format defined in TASK-002 introduces a new first-class artifact type that does not currently exist in `governance/artifact-schema.md`. Without explicit governance registration, the validation service has no canonical schema to enforce and Spine's "everything is a typed governed artifact" invariant is broken.

## Deliverable

1. New ADR (next available number under `architecture/adr/`) recording the decision to make validation policies a governed artifact type, the relationship to ADRs, and lifecycle rules (versioning, deprecation, ownership).
2. Update `governance/artifact-schema.md` to add the new artifact type with its frontmatter schema, required fields, and link types.
3. Update validation service rules so unknown frontmatter on a validation policy file is rejected the same way as other governed artifacts.
4. Cross-link the new artifact type from the multi-repo integration architecture doc.

## Acceptance Criteria

- ADR is committed and referenced from `governance/artifact-schema.md`.
- Validation policy artifact type is documented with required fields, link types, and an example.
- The validation service rejects malformed validation policy files with the same error shape as other artifacts.
- ADRs can declare typed `links` to validation policies and validation catches dangling links.
- Existing artifact types are unaffected.
- TASK-002 deliverable references this task as the governance prerequisite for shipping the format.
