---
id: EPIC-002
type: Epic
title: "Create Entry Point"
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

# EPIC-002 — Create Entry Point

---

## 1. Purpose

Provide the CLI command and API endpoint that allow users and agents to create new artifacts through Spine's governed workflow system. This is the user-facing layer that ties together ID allocation (EPIC-001), workflow resolution, and planning run initiation (INIT-006).

---

## 2. Scope

### In Scope

- API endpoint `POST /artifacts/create` that accepts artifact type, parent reference, and title — starts a planning run
- API endpoint `POST /artifacts/add` that adds an artifact to an existing planning run's branch (for UI/management platform use)
- CLI command `spine artifact create --type <type> --epic <epic> --title <title>`
- Wiring: endpoint allocates next ID, resolves creation workflow, starts planning run on a branch
- Input validation (type must be valid, parent must exist, title must be non-empty)
- API spec update for both endpoints
- Integration/scenario tests for the full create flow

### Out of Scope

- ID allocation logic (EPIC-001)
- Branch-scoped validation logic (EPIC-003)
- Changes to planning run engine (INIT-006)
- Per-type creation workflows (future)

---

## 3. Success Criteria

1. `spine artifact create --type Task --epic EPIC-003 --title "Implement validation"` succeeds and starts a planning run
2. `POST /artifacts/add` can add an artifact to an existing planning run's branch
3. The API returns the run ID, allocated artifact ID, and branch name
4. Invalid inputs produce clear error messages
5. Parent artifact existence is validated (against main for `create`, against branch for `add`)
6. The creation workflow resolves correctly via `(artifactType, mode=creation)` binding
7. Scenario test validates the full flow from CLI command to artifact on main

---

## 4. Key Files

- `internal/gateway/handlers_artifacts.go` (new endpoint)
- `internal/cli/cmd_artifact.go` (new or extended)
- `api/spec.yaml` (new schema)

---

## 5. Dependencies

- EPIC-001 (ID Allocation) — `NextID()`, `Slugify()`, `BuildArtifactPath()` must exist
- INIT-006 (Governed Artifact Creation) — `StartPlanningRun()` must exist (already complete)
