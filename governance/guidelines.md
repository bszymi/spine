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

## 8. Cross-Artifact Validation

The Constitution (§11) requires that artifacts are validated against the broader governed system context. This section provides practical guidance for how that validation should be performed.

### 8.1 Foundation Phase vs Evolution Phase

Spine recognizes two distinct phases of work:

**Foundation phase** — used when the project is first established:

```
Charter → Product → Architecture → Tasks → Code
```

In this phase, each layer is created in sequence and validated primarily against the layers above it.

**Evolution phase** — used once the system already exists:

When a new or changed artifact is proposed, it must be validated against the current governed state of all relevant layers — not only the layer directly above it.

Example flow:

```
Change proposed to an artifact
→ validate against current Charter / Constitution
→ validate against current Architecture
→ validate against current Product definition
→ validate against current implementation reality
→ decide whether to accept, revise, or create follow-up work
```

### 8.2 Validation Context per Artifact Type

Each artifact type should be validated against specific upstream layers:

| Artifact Type | Validate Against |
|---------------|-----------------|
| Product artifacts | Charter, Constitution, Architecture constraints |
| Architecture artifacts | Charter, Constitution, Product definition |
| ADRs | Charter, Constitution, Product definition, existing Architecture |
| Epics | Parent Initiative, Product definition, Architecture |
| Tasks | Parent Epic, Architecture, existing implementation |
| Governance documents | Charter, Constitution |

This table describes the primary validation context. Additional validation may be appropriate depending on the nature of the change.

### 8.3 When Validation Should Occur

Validation should occur at governed points in workflow progression:

- **Before approval** — when an artifact is submitted for review or acceptance
- **Before completion** — when a task or epic is marked complete
- **When upstream changes** — when a governing artifact (Charter, Constitution, Architecture, Product) is modified, downstream artifacts that depend on it may need re-evaluation

Validation does not need to occur on every minor edit. It is most important at transition points where artifacts move between lifecycle states.

### 8.4 Types of Mismatches

When validation detects an inconsistency, it should be classified:

**Scope conflict** — the artifact introduces work or capabilities outside the boundaries defined by the Charter, Product definition, or parent artifacts.

**Architectural conflict** — the artifact assumes system capabilities, components, or patterns that contradict or are not supported by the current architecture.

**Implementation drift** — the actual codebase or system state does not match what the architecture or product artifacts describe.

**Missing prerequisite work** — the artifact depends on work that has not yet been completed or on infrastructure that does not yet exist.

### 8.5 Handling Mismatches

When a mismatch is detected:

1. **Surface it explicitly** — document the inconsistency in the artifact, discussion thread, or review
2. **Classify it** — determine which type of mismatch it is
3. **Decide on resolution** — possible outcomes include:
   - Revise the proposed artifact to align with existing governed state
   - Update the upstream artifact if the change is justified (e.g., update architecture to support a new product requirement)
   - Create follow-up tasks to resolve the gap
   - Reject the change with rationale
4. **Never ignore it** — contradictions must not be silently accepted (Constitution §11)

### 8.6 Creating Follow-Up Work

When validation reveals a gap that cannot be resolved immediately:

- Create a new task describing the gap and required resolution
- Link the new task to the artifact that revealed the gap
- Ensure the follow-up task is placed under the appropriate epic
- Do not block the current work unless the gap makes the artifact invalid

---

## 9. Evolution Policy

These Guidelines are a living document and are expected to evolve as the system matures.

Changes to this document must:

- Be versioned in Git
- Follow the standards defined in the [Style Guide](/governance/style-guide.md)
- Not contradict the [Charter](/governance/Charter.md) or [Constitution](/governance/Constitution.md)

New sections may be added as architectural decisions, product changes, or governance refinements introduce new practical requirements. When new guidance is added, it should reference the constitutional or charter principle it supports.
