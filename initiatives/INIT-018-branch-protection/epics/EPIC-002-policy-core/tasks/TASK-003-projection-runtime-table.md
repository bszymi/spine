---
id: TASK-003
type: Task
title: "Project branch-protection config into branch_protection_rules runtime table"
status: Completed
work_type: implementation
created: 2026-04-18
last_updated: 2026-04-19
completed: 2026-04-19
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/tasks/TASK-001-config-schema-and-parser.md
---

# TASK-003 — Project branch-protection config into branch_protection_rules runtime table

---

## Purpose

Give the policy engine a cheap, in-memory-backed rule source instead of parsing `/.spine/branch-protection.yaml` on every request. The Projection Service already mirrors Git-stored state into runtime tables for workflows and artifacts — extend it for the protection config.

---

## Context

[ADR-009 §1](/architecture/adr/ADR-009-branch-protection.md):

> Git is the source of truth; the Projection Service mirrors the parsed ruleset into a runtime table (`branch_protection_rules`) the same way it does for workflow projections, and the enforcement path reads the projection (not the file) so evaluation is an in-memory lookup.

The projection layer already knows how to watch a Git path for changes (workflow YAMLs live at `/workflows/*.yaml`). Reuse that pattern.

---

## Deliverable

1. **Schema.** `branch_protection_rules` table (or equivalent runtime store shape):
   - Columns: `workspace_id`, `branch_pattern`, `protections []string`, `source_sha` (commit that produced the projection), `projected_at`.
   - Primary key `(workspace_id, branch_pattern)`; rewriting on each projection is acceptable (projections are derived state).

2. **Projection handler.** New handler or branch in the Projection Service that:
   - Watches `/.spine/branch-protection.yaml` on the authoritative branch.
   - On change: parses via TASK-001's parser, validates, replaces the rows for this workspace atomically.
   - On parse error: logs and emits an event; retains the previous ruleset rather than falling back to empty (empty would silently disable protection).
   - On missing file: emits `BootstrapDefaults()` from TASK-002 as the effective ruleset — persisted to the table the same as a real config, tagged with `source_sha = ""` or a sentinel so operators can see "defaults active".

3. **Rule-source adapter.** A small type that reads from `branch_protection_rules` and satisfies the rule-source interface TASK-002 defined. This is the glue that bridges the policy package and the runtime table without importing the DB layer into `branchprotect`.

4. **Tests.**
   - Fresh repo: table populated with bootstrap defaults after projection runs.
   - Edit to `branch-protection.yaml` lands: table reflects the new rules within the projection window.
   - Malformed YAML: previous rules retained, error surfaced.
   - File deleted: table reverts to bootstrap defaults.

---

## Acceptance Criteria

- `branch_protection_rules` table exists and is populated by the Projection Service for any workspace that projects an authoritative branch.
- A call to the rule-source adapter for a workspace returns the current rules (or the bootstrap defaults) without reading the filesystem or Git.
- Behavior on parse error, missing file, and config edit all covered by tests.
- No coupling: `internal/branchprotect` has no dependency on the DB layer added in this task.
