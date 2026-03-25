---
type: Governance
title: Repository Structure & Artifact Taxonomy
status: Living Document
version: "0.1"
---

# Repository Structure & Artifact Taxonomy

---

## 1. Purpose

This document defines the repository folder structure and artifact taxonomy for the Spine project.

It serves as the canonical reference for where artifacts live and how they are organized. All contributors — human and AI — should follow this structure when creating or locating artifacts.

This document aligns with the [Guidelines](/governance/guidelines.md) and [Style Guide](/governance/style-guide.md).

---

## 2. Root Structure

```
/
├── README.md
├── CONTRIBUTING.md
│
├── governance/
├── initiatives/
├── architecture/
├── product/
├── workflows/
├── templates/
└── tmp/
```

---

## 3. Folder Definitions

### 3.1 `/governance/`

Contains the foundational governance documents that define how Spine operates.

```
governance/
├── Charter.md
├── Constitution.md
├── guidelines.md
├── style-guide.md
├── repository-structure.md
└── naming-conventions.md
```

- **Charter.md** — Purpose, philosophy, and structural model
- **Constitution.md** — Non-negotiable system constraints and invariants
- **guidelines.md** — Recommended practices and evolving standards
- **style-guide.md** — Markdown formatting, metadata, and naming conventions
- **repository-structure.md** — This document
- **naming-conventions.md** — Artifact ID and naming rules

### 3.2 `/initiatives/`

Contains all initiative artifacts. Each initiative has its own folder containing an `initiative.md` and an `epics/` subfolder.

```
initiatives/
└── INIT-XXX-<slug>/
    ├── initiative.md
    └── epics/
        └── EPIC-XXX-<slug>/
            ├── epic.md
            └── tasks/
                ├── TASK-XXX-<slug>.md
                └── TASK-XXX-<slug>.md
```

Folder naming pattern: `<ARTIFACT-ID>-<slug>`

- `INIT-001-foundations/`
- `EPIC-001-governance-baseline/`

Task files live inside their parent epic's `tasks/` directory.

### 3.3 `/architecture/`

Contains architecture documentation and architectural decision records.

```
architecture/
├── architecture.md
├── domain-model.md
├── components.md
├── data-model.md
├── api/
│   └── v0.x.md
└── adrs/
    ├── ADR-0001-<slug>.md
    └── ADR-0002-<slug>.md
```

- **architecture.md** — System architecture overview
- **domain-model.md** — Core entities and relationships
- **components.md** — Services and responsibilities
- **adrs/** — Architectural Decision Records

### 3.4 `/workflows/`

Contains workflow definitions that govern how work progresses through states. Workflow files are pure YAML (not Markdown with front matter). They are discovered by the projection service and used by the engine to drive execution.

```
workflows/
├── task-default.yaml
├── task-spike.yaml
└── adr-review.yaml
```

Per [Workflow Definition Format](/architecture/workflow-definition-format.md):

- **Naming**: `<artifact-type>-<variant>.yaml` (e.g., `task-default.yaml`)
- **Versioning**: Each file has a `version` field; changes are tracked via Git history
- **Status**: `Active`, `Deprecated`, or `Draft`
- **Binding**: The `applies_to` field lists artifact types governed by the workflow
- **Discovery**: Files matching `workflows/*.yaml` are automatically discovered by the projection service

### 3.5 `/product/`

Contains product definition artifacts produced by product-focused epics.

```
product/
├── product-definition.md
├── users-and-use-cases.md
├── non-goals.md
├── success-metrics.md
└── boundaries-and-constraints.md
```

### 3.6 `/templates/`

Contains reusable templates for artifact types.

```
templates/
├── initiative-template.md
├── epic-template.md
├── task-template.md
└── adr-template.md
```

Templates define the expected sections and metadata for each artifact type. New artifact types must have a corresponding template.

### 3.7 `/tmp/`

Scratch space for working documents, drafts, and notes that are not yet formal artifacts. Contents of `tmp/` are not governed and should not be relied upon as durable truth.

---

## 4. Artifact Taxonomy

Spine uses the following artifact types:

| Type | ID Format | Location | Description |
|------|-----------|----------|-------------|
| Initiative | `INIT-XXX` | `/initiatives/INIT-XXX-<slug>/initiative.md` | Top-level work stream with defined scope and exit criteria |
| Epic | `EPIC-XXX` | `.../epics/EPIC-XXX-<slug>/epic.md` | Major deliverable within an initiative |
| Task | `TASK-XXX` | `.../tasks/TASK-XXX-<slug>.md` | Concrete work item with a single deliverable |
| ADR | `ADR-XXXX` | `/architecture/adrs/ADR-XXXX-<slug>.md` | Architectural decision record |
| Governance | — | `/governance/<name>.md` | Charter, Constitution, Guidelines, and related documents |
| Architecture | — | `/architecture/<name>.md` | System design and component documentation |
| Product | — | `/product/<name>.md` | Product definition and scope documents |
| Template | — | `/templates/<name>-template.md` | Reusable artifact structure definitions |

### 4.1 Hierarchy

```
Initiative
└── Epic
    └── Task
```

- An **Initiative** groups related epics around a common goal
- An **Epic** defines a major deliverable and contains tasks
- A **Task** is the smallest unit of tracked work with a single deliverable

Task IDs are scoped to their epic. `TASK-001` under `EPIC-001` and `TASK-001` under `EPIC-002` are distinct artifacts.

### 4.2 ADRs

Architectural Decision Records are standalone artifacts that capture significant design decisions. They are not nested under initiatives — they live in `/architecture/adrs/` and use a four-digit sequential ID (`ADR-0001`).

---

## 5. Folder Naming Conventions

- Folder names use the pattern: `<ARTIFACT-ID>-<slug>`
- Slugs are lowercase, hyphen-separated
- IDs are zero-padded (`001`, `002`, not `1`, `2`)
- Folder names must not change once created (the ID is stable)

Examples:

- `INIT-001-foundations`
- `EPIC-002-product-definition`
- `EPIC-003-architecture-v0.1`

---

## 6. Evolution Policy

This document is expected to evolve as the repository grows.

Changes must:

- Be versioned in Git
- Not contradict the [Charter](/governance/charter.md) or [Constitution](/governance/constitution.md)
- Be reflected in the [Guidelines](/governance/guidelines.md) if they affect artifact structure expectations
