---
id: TASK-002
type: Task
title: Lookups by target, and the evidence consistency rules
status: Completed
created: 2026-11-10
last_updated: 2026-11-10
epic: /initiatives/INIT-025-assumption-artifacts/epics/EPIC-001-assumption-type-and-links/epic.md
initiative: /initiatives/INIT-025-assumption-artifacts/initiative.md
work_type: feature
links:
  - type: parent
    target: /initiatives/INIT-025-assumption-artifacts/epics/EPIC-001-assumption-type-and-links/epic.md
  - type: related_to
    target: /architecture/adr/ADR-017-assumption-artifact-type.md
  - type: blocked_by
    target: /initiatives/INIT-025-assumption-artifacts/epics/EPIC-001-assumption-type-and-links/tasks/TASK-001-assumption-type-lifecycle-and-links.md
---

# TASK-002 — Lookups by target, and the evidence consistency rules

---

## Purpose

Implement ADR-017 §4 and §5. Today a link's target is stored as written (`/assumptions/…`) and
looked up by projection path (`assumptions/…`), so the lookup finds nothing.

## Deliverable

- `adapters/store/postgres_projections.go`: `QueryArtifactLinksByTarget` matches the target with
  and without a leading slash.
- `core/validation`: rules AL-001 and AL-002 (warnings), registered with the link rules.
- `architecture/validation-service.md`: the two rules.
- `CHANGELOG.md`: the lookup fix and its effect on blocking-transition events.

## Acceptance Criteria

- A link stored with target `/a/b.md` is returned for a lookup of `a/b.md` and of `/a/b.md`.
- A link stored with target `a/b.md` is returned for both forms too.
- A `Supported` assumption with no incoming `supports` link produces AL-001; with one, none.
- A `Contradicted` assumption with no incoming `refutes` link produces AL-002; with one, none.
- Neither rule fires for any other status or type.
- `go test ./...` passes, including the store's database-backed tests where a database is
  available.

## Notes (2026-11-10)

- The defect was confirmed on a live workspace database before the change: 4 of 4 stored links
  had a leading slash, 0 matched an artifact path exactly, 4 matched once trimmed.
- `store.LinkTargetForms` gives the two forms; the lookup matches either and orders its result.
  The validation fake store matches the same way, so the rules are tested against the real
  store's behaviour.
- AL-001 and AL-002 ignore an assumption's link to itself and report nothing when the lookup
  fails: a failed read is not evidence of a missing link.
- Gates: `go test ./...` 41 packages ok; `dev/lint-boundaries.sh` OK;
  `TestQueryArtifactLinksByTarget_BothPathForms` (integration tag) passes against PostgreSQL 18;
  codex review (gpt-6.1-sol) against `main`: no actionable defects.
- Not caused by this task: `TestRunModeDatabaseDefault` (integration tag, `adapters/store`)
  fails on `main` too — `GetRun` cannot scan a NULL `workflow_version_label`.
