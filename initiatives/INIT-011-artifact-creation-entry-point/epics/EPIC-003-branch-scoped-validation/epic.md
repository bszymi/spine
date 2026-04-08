---
id: EPIC-003
type: Epic
title: "Branch-Scoped Validation"
status: Completed
initiative: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
owner: bszymi
created: 2026-04-08
last_updated: 2026-04-08
links:
  - type: parent
    target: /initiatives/INIT-011-artifact-creation-entry-point/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-011-artifact-creation-entry-point/epics/EPIC-001-id-allocation-and-collision-resolution/epic.md
---

# EPIC-003 — Branch-Scoped Validation

---

## 1. Purpose

Make the validation step in the `artifact-creation` workflow discovery-driven: instead of validating a single artifact, it discovers all artifacts on the planning run's branch and validates them as a set.

This enables two artifact addition paths to coexist:
- **API path**: `POST /artifacts/add` writes artifacts to the branch via the gateway
- **Git-native path**: actors (human or AI) create artifact files directly on the branch

The validation step doesn't care how artifacts got on the branch. It uses `DiscoverChanges(main, branch)` to find everything and validates the full set.

---

## 2. Scope

### In Scope

- Branch-scoped discovery in the validation step: diff branch against main to find all new/modified artifacts
- Individual artifact validation: schema, required fields, status, ID format for each discovered artifact
- Cross-artifact validation across the branch set: parent links resolve (on branch or main), no dangling references, no duplicate IDs
- Updated `artifact-creation.yaml` workflow definition to reflect branch-scoped validation
- Unit tests for discovery-based validation
- Scenario tests for mixed creation paths (some via API, some via direct file write)

### Out of Scope

- Commit-time pre-validation / fast feedback on each push (future enhancement)
- Changes to the discovery engine itself (`DiscoverChanges` already exists)
- Per-type creation workflows

---

## 3. Success Criteria

1. Validation step discovers all artifacts on the branch, not just the initial one
2. Artifacts created via `POST /artifacts/add` and artifacts written directly to the branch are both discovered and validated
3. Cross-artifact links between branch artifacts are validated (e.g., task linking to its epic, both on branch)
4. Validation failure returns details for all failing artifacts, not just the first one
5. Validation passes when all artifacts are individually valid and cross-artifact constraints hold

---

## 4. Key Files

- `internal/engine/step.go` (validation step logic)
- `internal/artifact/discovery.go` (existing `DiscoverChanges`)
- `internal/validation/` (existing validation rules)
- `workflows/artifact-creation.yaml` (workflow definition update)

---

## 5. Dependencies

- EPIC-001 (ID Allocation) — validation must understand ID format and scoping rules
- Existing `DiscoverChanges()` from `internal/artifact/discovery.go`
- Existing `CrossArtifactValidator` from `internal/engine/interfaces.go`
