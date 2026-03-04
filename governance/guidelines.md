# Spine Guidelines

**Project:** Spine
**Version:** 0.1
**Status:** Living Document

---

## 1. Purpose

This document defines recommended practices and evolving standards for creating, structuring, and maintaining artifacts within the Spine repository.

Guidelines are the third tier of the governance hierarchy, as defined in the [Charter](/governance/Charter.md) (Section 6) and the [Constitution](/governance/Constitution.md) (Section 9).

They must align with both the Charter and Constitution. In case of conflict, the Constitution overrides Guidelines, and the Charter defines interpretive intent.

For formatting and style conventions, see the [Style Guide](/governance/style-guide.md).

---

## 2. Artifact Definition

In Spine, an **artifact** is a versioned Markdown document that represents intent, execution definition, or outcome within the system.

Artifacts are the primary units of truth in Spine. Actors (humans, AI agents, automated systems) interact with the system by creating, modifying, and evaluating artifacts.

Every durable element of the system — initiatives, epics, tasks, specifications, and governance documents — must exist as an artifact stored in Git.

Artifacts must be self‑describing and reconstructable from repository history.

---

## 3. Governance Document Relationships

Spine operates under a layered governance model:

1. **Charter** — Defines purpose, philosophy, and structural model. It is the interpretive authority.
2. **Constitution** — Defines non-negotiable system constraints and invariants. It is the enforcement authority.
3. **Guidelines** — Define recommended practices and evolving standards. They are advisory and may evolve as the system matures.

Guidelines translate the principles in the Charter and the constraints in the Constitution into practical, actionable standards for day-to-day work.

---

## 4. Artifact Structure Expectations

Every artifact in Spine must be a Markdown file versioned in Git.

Each artifact must include:

- A top-level heading (`#`) as the artifact title
- A metadata block with relevant fields (project, version, status, parent references)
- Sections using `##` for top-level divisions and `###` for subsections
- Horizontal rules (`---`) between major sections for visual separation

Artifacts must be self-contained and understandable without external context. They should reference related artifacts explicitly rather than relying on implicit knowledge.

---

### 4.1 Artifact Identification

All execution artifacts must use a stable identifier.

Artifact IDs follow the format:

`TYPE-XXX`

Where:

- `TYPE` identifies the artifact class (for example `INIT`, `EPIC`, `TASK`)
- `XXX` is a zero‑padded sequential number (`001`, `002`, `003`, …)

Examples:

`INIT-001`
`EPIC-002`
`TASK-014`

Artifact identifiers must remain stable for the lifetime of the artifact and must never be reused.

---

## 5. Linking Conventions

Artifacts should reference related documents using relative paths from the repository root:

- Reference governance documents: `/governance/Charter.md`
- Reference tasks: `/initiatives/INIT-XXX/.../tasks/TASK-XXX-name.md`
- Reference templates: `/templates/template-name.md`

When referencing another artifact inline, use the artifact ID and title:

> See [TASK-001 — Governance Guidelines](/initiatives/INIT-001-foundations/epics/EPIC-001-governance-baseline/tasks/TASK-001-guidelines.md)

Cross-references should be explicit. Artifacts must not depend on implicit context or assumed knowledge of other documents.

---

## 6. Artifact Lifecycle

Artifacts progress through defined statuses:

- **Pending** — Defined but not yet started
- **In Progress** — Actively being worked on
- **Complete** — Deliverables met and acceptance criteria satisfied
- **Superseded** — Replaced by a newer artifact (must link to successor)

Status changes must be reflected in the artifact's metadata block.

---

## 7. Expectations for Future Artifacts

New artifact types introduced into Spine must:

1. Have a corresponding template in `/templates/`
2. Follow the documentation standards defined in the [Style Guide](/governance/style-guide.md)
3. Include metadata, purpose, and acceptance criteria where applicable
4. Be placed in the correct location within the repository structure

New governance documents must explicitly state their relationship to the Charter, Constitution, and these Guidelines.

---

## 8. Evolution Policy

These Guidelines are a living document and are expected to evolve as the system matures.

Changes to this document must:

- Be versioned in Git
- Follow the standards defined in the [Style Guide](/governance/style-guide.md)
- Not contradict the Charter or Constitution
