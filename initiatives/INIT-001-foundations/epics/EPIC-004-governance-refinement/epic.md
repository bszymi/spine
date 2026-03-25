---
id: EPIC-004
type: Epic
title: Governance Refinement
status: Completed
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/initiative.md
---

# EPIC-004 — Governance Refinement

---

## Purpose

Refine governance conventions to reflect architectural decisions made during EPIC-003 (Architecture v0.1).

EPIC-001 established the baseline governance structure — repository layout, naming conventions, templates, and contribution rules. Since then, the domain model and ADRs have introduced new requirements: YAML front matter for artifact metadata, structured linkage between artifacts, globally unambiguous references, and type-based artifact schemas.

This epic bridges the gap between architectural intent and enforceable governance by defining the concrete schemas, updating templates, migrating existing artifacts to the new conventions, and encoding workflow-embedded cross-artifact validation into the core governance documents.

---

## Key Work Areas

- Define artifact front matter schema per artifact type (required fields, optional fields, link format, reference format)
- Update artifact templates to include YAML front matter
- Migrate existing artifacts to the new front matter format
- Define link target format and bidirectional link conventions
- Update Charter with mission-level alignment language
- Update Constitution with cross-artifact validation rules
- Update Guidelines with practical validation guidance
- Define task lifecycle and terminal outcomes (runtime vs durable state boundary)

---

## Primary Outputs

- `/governance/artifact-schema.md` — front matter schema specification per artifact type
- Updated `/governance/charter.md` — alignment language expressing Spine's role beyond artifact storage
- Updated `/governance/constitution.md` — governing rules for workflow-embedded cross-artifact validation
- Updated `/governance/guidelines.md` — practical validation guidance per artifact type
- Updated templates in `/templates/`
- Migrated existing artifacts with YAML front matter
- `/governance/task-lifecycle.md` — task lifecycle states and terminal outcome definitions

---

## Acceptance Criteria

- Every artifact type has a defined front matter schema with required and optional fields
- Templates reflect the schema and produce valid artifacts when used
- Existing artifacts are migrated and conform to the schema
- Link format is defined, documented, and consistent across artifacts
- Tooling can reliably parse artifact metadata from front matter
- Charter expresses Spine's mission as maintaining cross-layer alignment, not only storing artifacts
- Constitution encodes structural rules requiring cross-artifact validation during workflows
- Guidelines provide actionable validation guidance distinguishing initial creation from ongoing evolution
- Task lifecycle clearly distinguishes runtime-only transitions from durable governance outcomes
- Terminal outcomes are defined with their effect on artifact state in the main branch
