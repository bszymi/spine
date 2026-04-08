---
type: Governance
title: Naming and ID Conventions
status: Living Document
version: "0.1"
---

# Naming and ID Conventions

---

## 1. Purpose

This document defines the naming and identification conventions for all artifacts in the Spine repository.

Consistent naming ensures artifacts are discoverable, automation-friendly, and resistant to ambiguity.

All path conventions in this document (folder names, file names, ID patterns) apply within the **Spine artifacts directory** configured in `.spine.yaml`. See [Repository Structure §1.1](/governance/repository-structure.md) for details.

---

## 2. Artifact ID Format

Every execution artifact must have a stable, unique identifier.

| Artifact Type | ID Format | Padding | Example |
|---------------|-----------|---------|---------|
| Initiative | `INIT-XXX` | 3 digits | `INIT-001` |
| Epic | `EPIC-XXX` | 3 digits | `EPIC-002` |
| Task | `TASK-XXX` | 3 digits | `TASK-014` |
| ADR | `ADR-XXXX` | 4 digits | `ADR-0001` |

- IDs are always zero-padded
- IDs are assigned sequentially within their scope
- Task IDs are scoped to their parent epic (e.g. `TASK-001` under `EPIC-001` is distinct from `TASK-001` under `EPIC-002`)

---

## 3. ID Rules

1. **Uniqueness** — IDs must be unique within their scope
2. **Immutability** — once assigned, an ID must never change
3. **No reuse** — IDs of deleted or superseded artifacts must not be reassigned
4. **Titles may evolve** — the human-readable title of an artifact may change, but the ID remains stable
5. **Automatic allocation** — the `spine artifact entry` command and `POST /artifacts/entry` endpoint allocate the next ID automatically by scanning the parent directory. Gaps are preserved (max+1, not gap-filling). Follow-up IDs (900-series) are excluded from regular allocation.
6. **Collision resolution** — if two concurrent planning runs allocate the same ID, the collision is detected at merge time. The second artifact is renumbered to the next available ID and the merge is retried.
7. **Document types** — Governance, Architecture, and Product documents use descriptive slugs (e.g., `api-standards.md`) instead of sequential IDs. Duplicate slugs are rejected.

---

## 4. Folder Naming

Folders that represent artifacts follow the pattern:

```
<ARTIFACT-ID>-<slug>
```

Rules:

- Slugs are lowercase
- Slugs are hyphen-separated (no underscores, no spaces)
- Slugs should be short and descriptive

Examples:

| ID | Folder Name |
|----|-------------|
| `INIT-001` | `INIT-001-foundations` |
| `EPIC-001` | `EPIC-001-governance-baseline` |
| `EPIC-002` | `EPIC-002-product-definition` |
| `EPIC-003` | `EPIC-003-architecture-v0.1` |

---

## 5. File Naming

### 5.1 Artifact Definition Files

Initiative and epic definition files use a fixed name inside their folder:

- `initiative.md` — inside `INIT-XXX-<slug>/`
- `epic.md` — inside `EPIC-XXX-<slug>/`

### 5.2 Task Files

Task files include the artifact ID and slug in the filename:

```
TASK-XXX-<slug>.md
```

Examples:

- `TASK-001-guidelines.md`
- `TASK-002-repository-structure.md`
- `TASK-005-naming-conventions.md`

### 5.3 ADR Files

ADR files include the artifact ID and slug in the filename:

```
ADR-XXXX-<slug>.md
```

Example:

- `ADR-0001-git-as-source-of-truth.md`

### 5.4 Governance and Other Documents

Governance, architecture, and product documents use descriptive lowercase hyphen-separated names:

- `guidelines.md`
- `repository-structure.md`
- `naming-conventions.md`
- `domain-model.md`

---

## 6. Branch Naming

Work branches reflect the artifact taxonomy:

```
INIT-XXX/EPIC-XXX/TASK-XXX-<slug>
```

Examples:

- `INIT-001/EPIC-001/TASK-001-governance-guidelines`
- `INIT-001/EPIC-001/TASK-005-naming-conventions`

For initiative-level branches:

```
INIT-XXX-<slug>
```

Example:

- `INIT-001-foundations`

---

## 7. Evolution Policy

This document is expected to evolve as new artifact types are introduced.

Changes must be versioned in Git and must not contradict the [Charter](/governance/charter.md) or [Constitution](/governance/constitution.md).
