---
type: Governance
title: Spine Style Guide
status: Living Document
version: "0.1"
---

# Spine Style Guide

---

## 1. Purpose

This document defines the Markdown formatting, metadata conventions, and writing standards for all artifacts in the Spine repository.

It is a companion to the [Guidelines](/governance/guidelines.md), which define the philosophy and structural expectations for artifacts. This document focuses on how artifacts should be written and formatted.

---

## 2. Formatting

- Use Markdown for all documentation
- Use proper heading hierarchy: one `#` per document, `##` for sections, `###` for subsections
- Use bold (`**text**`) for metadata labels
- Use code formatting (`` ` ``) for file paths, IDs, and technical references
- Use lists for enumerable items
- Use horizontal rules (`---`) between major sections
- Avoid inline HTML

---

## 3. Metadata

All artifacts should include a YAML front matter block at the top of the file, before the Markdown title.

Metadata fields should appear in a consistent order to make documents predictable for both humans and automated tooling. See [Artifact Schema](/governance/artifact-schema.md) for full field definitions.

Governance artifacts:

```yaml
---
type: Governance
title: "[Document Title]"
status: Living Document
version: "0.1"
---
```

Execution artifacts (initiatives, epics, tasks):

```yaml
---
id: TASK-001
type: Task
title: "[Task Title]"
status: Pending
epic: /initiatives/INIT-XXX/epics/EPIC-XXX/epic.md
initiative: /initiatives/INIT-XXX/initiative.md
---
```

---

## 4. Content

- Write in clear, declarative prose
- State intent before detail
- Prefer short paragraphs and lists over long blocks of text
- Avoid ambiguity — if something can be interpreted multiple ways, clarify it

---

## 5. File Naming

Artifact files should follow a consistent naming pattern:

`<artifact-id>-<slug>.md`

Example:

`TASK-001-governance-guidelines.md`

This convention keeps filenames readable while preserving the canonical artifact identifier.

---

## 6. Evolution Policy

This Style Guide is a living document and may evolve as the system matures.

Changes must not contradict the [Charter](/governance/charter.md), [Constitution](/governance/constitution.md), or [Guidelines](/governance/guidelines.md).
