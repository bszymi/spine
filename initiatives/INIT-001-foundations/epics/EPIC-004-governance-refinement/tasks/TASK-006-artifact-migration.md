# TASK-006 — Migrate Artifacts to Front Matter and Align Statuses

**Epic:** EPIC-004 — Governance Refinement
**Initiative:** INIT-001 — Foundations
**Status:** Pending

---

## Purpose

Migrate all existing artifacts (initiatives, epics, tasks) to use YAML front matter as defined in the [Artifact Schema](/governance/artifact-schema.md), and align statuses with the governed states defined in the [Task Lifecycle](/governance/task-lifecycle.md).

Currently, all artifacts use markdown bold metadata (`**Key:** Value`) instead of YAML front matter blocks. Additionally, artifact statuses were assigned before the task lifecycle model was defined and may not reflect the correct governed states.

## Deliverable

Updated artifacts across the repository:

1. **YAML front matter migration** — Replace markdown bold metadata with YAML front matter blocks containing required fields (id, type, status, parent links) as defined in the artifact schema
2. **Status rename** — Rename `Complete` to `Completed` across all governance and architecture documents (artifact schema, task lifecycle, and any other references)
3. **Status alignment** — Audit and correct statuses to match governed states:
   - Tasks: Draft, Pending, In Progress, Completed, Cancelled, Rejected, Superseded, Abandoned
   - Epics: Draft, Pending, In Progress, Completed, Superseded
   - Initiatives: Draft, Pending, In Progress, Completed, Superseded
4. **Template updates** — Ensure templates in `/templates/` produce artifacts with valid YAML front matter

## Acceptance Criteria

- All initiative, epic, and task artifacts use YAML front matter as defined in the artifact schema
- Required fields (id, type, status, parent references) are present in all artifacts
- `Complete` renamed to `Completed` in artifact schema, task lifecycle, and all artifact statuses
- Statuses match the governed states from the task lifecycle document
- Templates produce valid front matter when used
- No functional content is lost during migration
