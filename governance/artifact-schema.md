---
type: Governance
title: Artifact Front Matter Schema
status: Living Document
version: "0.1"
---

# Artifact Front Matter Schema

---

## 1. Purpose

This document defines the YAML front matter schema for each artifact type in Spine.

Front matter is the primary mechanism for storing machine-readable metadata in artifacts. It enables tooling, validation, and automated discovery of artifact relationships while keeping artifacts human-readable and self-describing.

This document is the canonical reference for what fields each artifact type must or may contain.

---

## 2. General Rules

### 2.0 Scope

This schema covers governed artifacts authored as Markdown files with YAML front matter — Initiatives, Epics, Tasks, ADRs, and Governance / Architecture / Product documents.

**Workflow definitions are out of scope.** They are pure YAML files (no Markdown body, no front matter) governed by [Workflow Definition Format](/architecture/workflow-definition-format.md), validated by the workflow-specific suite described in [Workflow Validation](/architecture/workflow-validation.md), and managed through dedicated API operations per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md). Nothing in this document applies to workflow definition files.

**Workspace operational governance files are out of scope for the front-matter schema but are governed artifacts.** Pure YAML files under `/.spine/` (e.g., the [repository catalog](#58-repository-catalog) `/.spine/repositories.yaml`) carry no Markdown body and no front matter, but they are committed to the primary Spine repository and validated at workspace load. Their schemas are defined in the architecture documents linked from §5.8.

### 2.1 Format

All artifact metadata is stored as YAML front matter at the top of the Markdown file, delimited by `---` lines:

```yaml
---
id: TASK-001
type: Task
status: In Progress
---
```

The front matter block must be the first content in the file. The Markdown title (`# ...`) follows immediately after.

### 2.2 Field Conventions

- **Required fields** must be present in every artifact of that type
- **Optional fields** may be omitted when not applicable
- Field names use `snake_case`
- Date values use ISO 8601 format: `YYYY-MM-DD`
- Status values use Title Case: `Pending`, `In Progress`, `Completed`, `Superseded`
- String values do not require quotes unless they contain special YAML characters

### 2.3 ID Scope and Uniqueness

- Initiative IDs (`INIT-XXX`) are globally unique within the repository
- Epic IDs (`EPIC-XXX`) are globally unique within the repository
- Task IDs (`TASK-XXX`) are scoped to their parent epic — `TASK-001` under `EPIC-001` is distinct from `TASK-001` under `EPIC-002`
- ADR IDs (`ADR-XXX`) are globally unique within the repository

See [Naming Conventions](/governance/naming-conventions.md) for ID format details.

---

## 3. Artifact Reference Format

### 3.1 Principle

Link targets must use globally unambiguous references so that any artifact can be identified without relying on context or local scope.

### 3.2 Canonical Reference Format

Artifact references use the repository-relative path to the artifact file:

```
/initiatives/INIT-001-foundations/initiative.md
/initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/tasks/TASK-001-domain-model.md
/architecture/adr/ADR-002-events.md
/governance/constitution.md
```

This format is:

- Globally unambiguous within the repository
- Stable (paths do not change once created)
- Resolvable by both humans and tooling
- Compatible with Markdown links

### 3.3 Short References

In prose content (outside front matter), artifacts may be referred to by their ID and title for readability:

- `EPIC-003 — Architecture v0.1`
- `ADR-004`

However, all structured references in front matter must use the canonical path format.

---

## 4. Link Model

### 4.1 Link Types

Links represent governed relationships between artifacts. The following link types are defined:

| Link Type | Inverse | Meaning |
|-----------|---------|---------|
| `parent` | `contains` | This artifact belongs to the specified parent |
| `contains` | `parent` | This artifact contains the specified children |
| `blocks` | `blocked_by` | This artifact blocks progress on the target |
| `blocked_by` | `blocks` | This artifact is blocked by the target |
| `supersedes` | `superseded_by` | This artifact replaces the target |
| `superseded_by` | `supersedes` | This artifact has been replaced by the target |
| `follow_up_to` | `follow_up_from` | This artifact is follow-up work from the target |
| `follow_up_from` | `follow_up_to` | This artifact produced follow-up work in the target |
| `related_to` | `related_to` | Informational relationship (symmetric) |

### 4.2 Link Format in Front Matter

Links are stored as a list of typed entries under a `links` key:

```yaml
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-003-architecture-v0.1/epic.md
  - type: blocks
    target: /initiatives/INIT-001-foundations/epics/EPIC-004-governance-refinement/tasks/TASK-001-artifact-schema-definition.md
  - type: related_to
    target: /architecture/adr/ADR-004-evaluation-and-acceptance-model.md
```

### 4.3 Bidirectional Consistency

For link types that have meaningful inverse semantics (all types except `related_to`), both artifacts should store the corresponding link entry.

Example: if Task A `blocks` Task B, then:

- Task A front matter includes `blocks → Task B`
- Task B front matter includes `blocked_by → Task A`

Tooling should validate that bidirectional links remain consistent across artifacts.

### 4.4 References vs Links

Not all artifact mentions are governed links. The `links` section is for governed, typed relationships. Informational references (such as "see also" pointers in prose) do not require link entries and do not require bidirectional consistency.

---

## 5. Schemas by Artifact Type

The schemas below cover every artifact type governed by this document. Workflow definitions are explicitly **not** listed — see §2.0 and [Workflow Definition Format](/architecture/workflow-definition-format.md).

### 5.1 Initiative

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | yes | string | Initiative ID (e.g., `INIT-001`) |
| `type` | yes | string | Always `Initiative` |
| `title` | yes | string | Human-readable title |
| `status` | yes | enum | `Draft`, `Pending`, `In Progress`, `Completed`, `Superseded` |
| `owner` | optional | string | Responsible person or team |
| `created` | yes | date | Creation date |
| `last_updated` | optional | date | Last modification date |
| `links` | optional | list | Typed artifact links |

Example:

```yaml
---
id: INIT-001
type: Initiative
title: Foundations
status: In Progress
owner: bszymi
created: 2026-03-04
last_updated: 2026-03-12
links:
  - type: related_to
    target: /governance/charter.md
---
```

---

### 5.2 Epic

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | yes | string | Epic ID (e.g., `EPIC-003`) |
| `type` | yes | string | Always `Epic` |
| `title` | yes | string | Human-readable title |
| `status` | yes | enum | `Draft`, `Pending`, `In Progress`, `Completed`, `Superseded` |
| `initiative` | yes | path | Canonical path to parent initiative |
| `owner` | optional | string | Responsible person or team |
| `created` | optional | date | Creation date |
| `last_updated` | optional | date | Last modification date |
| `links` | optional | list | Typed artifact links |

Example:

```yaml
---
id: EPIC-004
type: Epic
title: Governance Refinement
status: Pending
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/initiative.md
---
```

---

### 5.3 Task

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | yes | string | Task ID (e.g., `TASK-001`) |
| `type` | yes | string | Always `Task` |
| `title` | yes | string | Human-readable title |
| `status` | yes | enum | `Draft`, `Pending`, `In Progress`, `Completed`, `Superseded` |
| `epic` | yes | path | Canonical path to parent epic |
| `initiative` | yes | path | Canonical path to parent initiative |
| `work_type` | optional | string | Workflow classification (e.g., `implementation`, `spike`, `bugfix`). Used by the Workflow Engine for [binding resolution](/architecture/task-workflow-binding.md). |
| `acceptance` | optional | enum | `Approved`, `Rejected With Followup`, `Rejected Closed` |
| `acceptance_rationale` | optional | string | Reason for acceptance outcome |
| `repositories` | optional | list of strings | Code repository IDs the task affects (see below). Omit or leave empty for primary-repo-only execution. |
| `created` | optional | date | Creation date |
| `last_updated` | optional | date | Last modification date |
| `links` | optional | list | Typed artifact links |

#### `repositories`

The `repositories` field declares which **code repositories** (registered in the workspace [repository catalog](#58-repository-catalog)) participate in the task. It encodes part of task intent: where implementation work is expected to occur. See [Multi-Repository Integration §4.1](/architecture/multi-repository-integration.md) for the runtime resolution rules.

Rules:

- The field is optional.
- Values are repository IDs (the same IDs declared in `/.spine/repositories.yaml`), not URLs or local paths.
- Omitting the field — or leaving it as an empty list — means the task targets the primary Spine repository only (single-repo execution). Existing pre-INIT-014 tasks remain valid without changes.
- The primary `spine` repository always participates and need not be listed; tasks may omit it even when other repositories are declared.
- The field is a Task-only field. Including `repositories` on any other artifact type is a schema error.

Single-repo example (no field — backward compatible):

```yaml
---
id: TASK-001
type: Task
title: Artifact Schema Definition
status: In Progress
epic: /initiatives/INIT-001-foundations/epics/EPIC-004-governance-refinement/epic.md
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/epics/EPIC-004-governance-refinement/epic.md
---
```

Multi-repo example:

```yaml
---
id: TASK-042
type: Task
title: Add rate limiting
status: In Progress
epic: /initiatives/INIT-005-platform-hardening/epics/EPIC-002-rate-limiting/epic.md
initiative: /initiatives/INIT-005-platform-hardening/initiative.md
repositories:
  - payments-service
  - api-gateway
links:
  - type: parent
    target: /initiatives/INIT-005-platform-hardening/epics/EPIC-002-rate-limiting/epic.md
---
```

---

### 5.4 ADR

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | yes | string | ADR ID (e.g., `ADR-002`) |
| `type` | yes | string | Always `ADR` |
| `title` | yes | string | Human-readable decision title |
| `status` | yes | enum | `Proposed`, `Accepted`, `Deprecated`, `Superseded` |
| `date` | yes | date | Date of decision |
| `decision_makers` | yes | string | Who participated in the decision |
| `links` | optional | list | Typed artifact links |

Example:

```yaml
---
id: ADR-004
type: ADR
title: Evaluation and Acceptance Model
status: Accepted
date: 2026-03-11
decision_makers: Spine Architecture
links:
  - type: related_to
    target: /architecture/domain-model.md
---
```

---

### 5.5 Governance Document

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `type` | yes | string | Always `Governance` |
| `title` | yes | string | Document title |
| `status` | yes | enum | `Living Document`, `Foundational`, `Superseded` |
| `version` | optional | string | Document version |
| `links` | optional | list | Typed artifact links |

Example:

```yaml
---
type: Governance
title: Constitution
status: Foundational
version: "0.1"
links:
  - type: related_to
    target: /governance/charter.md
---
```

Note: Governance documents do not use sequential IDs. They are identified by their canonical path.

---

### 5.6 Architecture Document

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `type` | yes | string | Always `Architecture` |
| `title` | yes | string | Document title |
| `status` | yes | enum | `Living Document`, `Stable`, `Superseded` |
| `version` | optional | string | Document version |
| `links` | optional | list | Typed artifact links |

Example:

```yaml
---
type: Architecture
title: Domain Model
status: Living Document
version: "0.1"
links:
  - type: related_to
    target: /governance/constitution.md
---
```

Note: Architecture documents (excluding ADRs) do not use sequential IDs. They are identified by their canonical path.

---

### 5.7 Product Document

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `type` | yes | string | Always `Product` |
| `title` | yes | string | Document title |
| `status` | yes | enum | `Living Document`, `Stable`, `Superseded` |
| `version` | optional | string | Document version |
| `links` | optional | list | Typed artifact links |

Example:

```yaml
---
type: Product
title: Product Definition
status: Living Document
version: "0.1"
links:
  - type: related_to
    target: /governance/charter.md
---
```

Note: Product documents do not use sequential IDs. They are identified by their canonical path.

---

### 5.8 Repository Catalog

The repository catalog at `/.spine/repositories.yaml` is a governed artifact: it is committed to the primary Spine repository, validated at workspace load, and changed only through governance commits. Unlike the artifact types above, it is a pure YAML file with no Markdown body and no front matter, so the schema rules in §2 and §4 do not apply to it.

| Property | Value |
|----------|-------|
| Canonical path | `/.spine/repositories.yaml` |
| Format | YAML list of catalog entries (no front matter, no Markdown body) |
| Required when | Multi-repo workspaces (workspaces with one or more registered code repositories) |
| Optional when | Single-repo workspaces (file may be omitted; the workspace behaves as if a single `kind: spine` entry existed) |
| Identity authority | This file is the source of truth for which repository IDs exist within the workspace |
| Schema and validation | Defined in [Multi-Repository Integration §2.1](/architecture/multi-repository-integration.md) and [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md) |
| Excluded fields | Operational connection fields (`url`, `clone_url`, `credentials`, `token`, `secret_ref`, `local_path`, `path`, `status`) — these live in the runtime binding, never in Git |

Catalog changes (registering or deregistering a code repository) produce governance commits using the standard commit format defined in [Git Integration §5](/architecture/git-integration.md). Operational connection details (clone URLs, credentials, local paths, active/inactive status) are managed in the runtime binding described in [Multi-Repository Integration §2.2](/architecture/multi-repository-integration.md) and are never written to Git.

---

## 6. Status Enums

Different artifact types use different status values:

| Artifact Type | Allowed Status Values |
|---------------|----------------------|
| Initiative | `Draft`, `Pending`, `In Progress`, `Completed`, `Superseded` |
| Epic | `Draft`, `Pending`, `In Progress`, `Completed`, `Superseded` |
| Task | `Draft`, `Pending`, `Completed`, `Cancelled`, `Rejected`, `Superseded`, `Abandoned` |
| ADR | `Proposed`, `Accepted`, `Deprecated`, `Superseded` |
| Governance | `Living Document`, `Foundational`, `Superseded` |
| Architecture | `Living Document`, `Stable`, `Superseded` |
| Product | `Living Document`, `Stable`, `Superseded` |

---

## 7. Relationship to Markdown Content

Front matter stores structured metadata. The Markdown body following the front matter stores the artifact's content — intent, decisions, deliverables, acceptance criteria, and other narrative information.

Fields that appear in front matter should not be duplicated as bold key-value lines in the Markdown body. When artifacts are migrated to use YAML front matter, the existing bold metadata lines should be removed and replaced by front matter fields.

The Markdown title (`# ...`) remains in the body for readability and should match the `title` field in front matter.

---

## 8. Evolution Policy

This schema is expected to evolve as new artifact types are introduced or existing types require additional metadata.

Changes must:

- Be versioned in Git
- Not contradict the [Charter](/governance/charter.md) or [Constitution](/governance/constitution.md)
- Be reflected in the artifact templates if they affect expected structure
- Maintain backward compatibility where possible — new required fields should be introduced with a migration plan
