# EPIC-004 — Governance Refinement

**Initiative:** INIT-001 — Foundations
**Status:** Pending

---

## Purpose

Refine governance conventions to reflect architectural decisions made during EPIC-003 (Architecture v0.1).

EPIC-001 established the baseline governance structure — repository layout, naming conventions, templates, and contribution rules. Since then, the domain model and ADRs have introduced new requirements: YAML front matter for artifact metadata, structured linkage between artifacts, globally unambiguous references, and type-based artifact schemas.

This epic bridges the gap between architectural intent and enforceable governance by defining the concrete schemas, updating templates, and migrating existing artifacts to the new conventions.

---

## Key Work Areas

- Define artifact front matter schema per artifact type (required fields, optional fields, link format, reference format)
- Update artifact templates to include YAML front matter
- Migrate existing artifacts to the new front matter format
- Define link target format and bidirectional link conventions

---

## Primary Outputs

- `/governance/artifact-schema.md` — front matter schema specification per artifact type
- Updated templates in `/templates/`
- Migrated existing artifacts with YAML front matter

---

## Acceptance Criteria

- Every artifact type has a defined front matter schema with required and optional fields
- Templates reflect the schema and produce valid artifacts when used
- Existing artifacts are migrated and conform to the schema
- Link format is defined, documented, and consistent across artifacts
- Tooling can reliably parse artifact metadata from front matter
