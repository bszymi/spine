---
type: Governance
title: Contributing to Spine
status: Living Document
version: "0.1"
---

# Contributing to Spine

---

## 1. Overview

Spine is an artifact-centric system. All contributions take the form of versioned Markdown artifacts stored in Git.

This document defines how contributors — human and AI — should interact with the repository, create artifacts, and submit changes.

Before contributing, familiarize yourself with:

- [Charter](/governance/charter.md) — philosophy and structural model
- [Constitution](/governance/constitution.md) — non-negotiable constraints
- [Guidelines](/governance/guidelines.md) — recommended practices
- [Style Guide](/governance/style-guide.md) — formatting and metadata conventions
- [Repository Structure](/governance/repository-structure.md) — folder layout and artifact taxonomy

---

## 2. Creating Initiatives

An initiative is a top-level work stream with defined scope and exit criteria.

To create an initiative:

1. Assign the next available ID: `INIT-XXX`
2. Create a folder: `/initiatives/INIT-XXX-<slug>/`
3. Copy `/templates/initiative-template.md` to `initiative.md` inside the folder
4. Fill in all required sections
5. Create an `epics/` subfolder for child epics

Example:

```
/initiatives/INIT-002-workflow-engine/
├── initiative.md
└── epics/
```

---

## 3. Creating Epics

An epic is a major deliverable within an initiative.

To create an epic:

1. Assign the next available ID within the initiative: `EPIC-XXX`
2. Create a folder: `/initiatives/INIT-XXX-<slug>/epics/EPIC-XXX-<slug>/`
3. Copy `/templates/epic-template.md` to `epic.md` inside the folder
4. Fill in all required sections
5. Create a `tasks/` subfolder for child tasks

Example:

```
/initiatives/INIT-001-foundations/epics/EPIC-004-new-epic/
├── epic.md
└── tasks/
```

---

## 4. Creating Tasks

A task is the smallest unit of tracked work with a single deliverable.

To create a task:

1. Assign the next available ID within the epic: `TASK-XXX`
2. Create a file: `.../tasks/TASK-XXX-<slug>.md`
3. Copy `/templates/task-template.md` and fill in all required sections
4. Each task must have a single, concrete deliverable

Example:

```
/initiatives/INIT-001-foundations/epics/EPIC-001-governance-baseline/tasks/TASK-006-new-task.md
```

---

## 5. Creating ADRs

Architectural Decision Records capture significant design decisions.

To create an ADR:

1. Assign the next available four-digit ID: `ADR-XXXX`
2. Create a file: `/architecture/adr/ADR-XXXX-<slug>.md`
3. Copy `/templates/adr-template.md` and fill in all required sections
4. Set the initial status to `Proposed`

---

## 6. Naming Conventions

- **Artifact IDs** are zero-padded: `INIT-001`, `EPIC-002`, `TASK-003`, `ADR-0001`
- **Folder names** follow the pattern: `<ARTIFACT-ID>-<slug>`
- **Slugs** are lowercase, hyphen-separated
- **IDs are permanent** — once assigned, an ID must never change or be reused
- **File names** for tasks follow: `TASK-XXX-<slug>.md`

See [Naming Conventions](/governance/naming-conventions.md) for full details.

---

## 7. Branching

Work branches should reflect the artifact taxonomy:

```
INIT-XXX/EPIC-XXX/TASK-XXX-<slug>
```

Examples:

- `INIT-001/EPIC-001/TASK-001-governance-guidelines`
- `INIT-001/EPIC-001/TASK-004-contribution-conventions`

For initiative-level work that spans multiple epics:

```
INIT-XXX-<slug>
```

---

## 8. Pull Request Expectations

Each pull request should:

- Address a single task or closely related set of changes
- Include a summary describing what was done and why
- Reference the relevant task ID in the description
- Ensure all new artifacts follow the [Style Guide](/governance/style-guide.md)
- Not include unrelated changes

---

## 9. Documentation Expectations

All contributions must produce or update versioned artifacts. Specifically:

- New deliverables must follow the appropriate template
- Metadata blocks must be complete (status, parent references)
- Cross-references to related artifacts must use explicit paths
- Task status must be updated when work begins or completes

Undocumented work is invalid within Spine (Constitution, Section 3).

---

## 10. Workflow

The standard contribution workflow:

1. Identify or create the task to work on
2. Create a branch following the naming convention
3. Execute the task and create the deliverable
4. Update the task status to Complete
5. Commit, push, and create a pull request
6. Merge after review

---

## 11. Evolution Policy

This document is expected to evolve as the project matures.

Changes must be versioned in Git and must not contradict the [Charter](/governance/charter.md) or [Constitution](/governance/constitution.md).
