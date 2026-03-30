---
id: EPIC-004
type: Epic
title: "API, Gateway & CLI"
status: Pending
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
owner: bszymi
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
---

# EPIC-004 — API, Gateway & CLI

---

## 1. Purpose

Expose planning runs through the HTTP API and CLI so users and agents can start governed artifact creation.

This epic connects the engine capability (EPIC-003) to the external interface and updates the OpenAPI specification.

---

## 2. Scope

### In Scope

- Extend `runStartRequest` with `mode` and `artifact_content` fields
- Route planning mode requests to `StartPlanningRun()` in the handler
- Relax `resolveWriteContext()` for planning runs (no `task_path` required)
- Wire `ArtifactWriter` dependency in server setup (`cmd/spine/main.go`)
- Update `api/spec.yaml` with new schemas and parameters
- Update CLI `run start` command with `--mode` and `--content` flags
- Gateway handler tests

### Out of Scope

- Engine implementation (EPIC-003)
- Scenario tests (EPIC-006)

---

## 3. Success Criteria

1. `POST /runs` accepts `mode: "planning"` with `artifact_content`
2. Planning run artifacts can be created via `write_context` without `task_path`
3. Standard run behavior is completely unchanged
4. OpenAPI spec accurately describes the new parameters
5. CLI supports `spine run start --mode planning --content <file>`
6. Handler tests cover planning mode, validation errors, and standard mode preservation

---

## 4. Key Files

- `internal/gateway/handlers_workflow.go`
- `internal/gateway/handlers_artifacts.go`
- `cmd/spine/main.go`
- `cmd/spine/cmd_run.go`
- `api/spec.yaml`

---

## 5. Dependencies

- EPIC-003 (Engine) — `StartPlanningRun()` must exist
