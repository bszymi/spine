---
id: INIT-006
type: Initiative
title: Governed Artifact Creation
status: Draft
owner: bszymi
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: related_to
    target: /governance/constitution.md
  - type: related_to
    target: /governance/charter.md
  - type: related_to
    target: /product/product-definition.md
---

# INIT-006 — Governed Artifact Creation

---

## 1. Intent

Enable governed creation of artifacts through Spine workflows instead of writing directly to the authoritative branch.

Currently, creating a new initiative, epic, or task commits directly to `main`. This violates the Constitution (§4: all execution must occur through defined workflows) and prevents governance over what enters the repository.

This initiative introduces **planning runs** — a run mode where the artifact is created on a branch, elaborated with child artifacts, reviewed, and merged to main only after approval.

---

## 2. Scope

### In Scope

- New `RunMode` domain concept (`standard` / `planning`)
- `StartPlanningRun()` engine method that creates artifacts on a branch
- Relaxed `write_context` validation for planning runs (multiple artifacts per branch)
- Branch-aware precondition evaluation for planning runs
- `initiative-lifecycle.yaml` workflow definition
- Database migration for `mode` column on runs
- API and CLI support for starting planning runs
- ADR documenting the design decision
- Architecture documentation updates
- Scenario tests for the full planning run lifecycle
- Unit tests for all changed components

### Out of Scope

- Automatic child artifact scaffolding (future enhancement)
- UI for planning runs (belongs in the management platform)
- Planning run templates or presets
- Changes to existing `StartRun()` behavior

---

## 3. Success Criteria

This initiative is successful when:

1. A planning run can be started for an artifact type that has a governing workflow
2. The artifact is created on a branch, not on main
3. Additional artifacts can be written to the same branch via `write_context`
4. Review approval merges the entire branch to main
5. Review rejection loops back to drafting without affecting main
6. Cancellation cleans up the branch without affecting main
7. Existing `StartRun()` behavior is completely unchanged
8. All scenario tests pass

---

## 4. Constraints

- Must not modify `StartRun()` — separate `StartPlanningRun()` method
- Must reuse existing merge infrastructure (`MergeRunBranch()`)
- Must preserve existing `write_context` validation for standard runs
- Must comply with Constitution §4 (governed execution) and §7 (reproducibility)

---

## 5. Work Breakdown

### Epics

| Epic | Title | Purpose |
|------|-------|---------|
| EPIC-001 | Architecture & ADR | Design the planning run model, write ADR-0006 |
| EPIC-002 | Domain Model & Storage | `RunMode` type, migration, store layer |
| EPIC-003 | Engine: Planning Run Support | `StartPlanningRun()`, branch-aware preconditions |
| EPIC-004 | API, Gateway & CLI | API endpoint, write_context relaxation, CLI flags |
| EPIC-005 | Workflow Definitions | `initiative-lifecycle.yaml` |
| EPIC-006 | Scenario Tests | End-to-end validation of the full lifecycle |

---

## 6. Risks

- **Regression in existing runs** — mitigated by separate method, not modifying `StartRun()`
- **Merge conflicts on planning branches** — mitigated by short-lived branches and existing merge error handling
- **Complexity in precondition evaluation** — mitigated by single `resolveReadRef()` helper

---

## 7. Exit Criteria

INIT-006 may be marked complete when:

- All six epics are complete
- Planning runs work end-to-end through the API
- Scenario tests validate golden path, rejection, and cancellation
- ADR-0006 is accepted
- Architecture docs are updated

---

## 8. Links

- Charter: `/governance/charter.md`
- Constitution: `/governance/constitution.md`
- ADR: `/architecture/adr/ADR-0006-planning-runs.md`
