---
id: TASK-002
type: Task
title: "Exclude Workflow Definitions from Artifact Schema Governance Doc"
status: Pending
work_type: documentation
created: 2026-04-17
epic: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
initiative: /initiatives/INIT-015-workflow-resource-separation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
---

# TASK-002 — Exclude Workflow Definitions from Artifact Schema Governance Doc

---

## Context

[Artifact Front Matter Schema](/governance/artifact-schema.md) documents the schemas for every artifact type in the system. Workflow definition files are pure YAML and are not covered by front-matter schemas, but the current document does not say so explicitly.

## Deliverable

Update `/governance/artifact-schema.md`:

- Add a short clause in §2 General Rules stating that workflow definitions are governed by a separate schema ([Workflow Definition Format](/architecture/workflow-definition-format.md)) and do not use Markdown front matter.
- Add a note in §5 listing workflow definitions as explicitly out of scope for this document, with a pointer to the workflow definition format and to [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md).

## Acceptance Criteria

- A reader of `artifact-schema.md` cannot mistake the document's scope as covering workflow definitions.
- Cross-links to the workflow definition format and ADR-007 are present.
