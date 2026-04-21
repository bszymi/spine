---
id: EPIC-005
type: Epic
title: Code Quality 2026-04
status: In Progress
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
owner: bszymi
created: 2026-04-17
last_updated: 2026-04-21
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/initiative.md
---

# EPIC-005 — Code Quality 2026-04

---

## 1. Purpose

Address findings from a full-repo code-quality review conducted 2026-04-17. Focus on duplicated code, god files, parallel implementations of the same concern, layer leaks in the gateway, and one correctness defect found during review. Complements EPIC-003 (closed 2026-04) which covered an earlier round; this epic targets the pain that remains after that work.

---

## 2. Scope

### In Scope

- Store layer: extract row-scan/NotFound/mustAffect helpers and split the 1966-line `postgres.go` along its existing concern boundaries.
- Artifact + Workflow services: extract shared git branch-write plumbing (`enterBranch`, `stageAndCommit`, `gitAdd/Commit/Reset`), consolidate Create/Update into a single `writeAndCommit` and fix the artifact.Update rollback asymmetry.
- Engine: extract `loadPreconditionArtifact`, decompose `ActivateStep` and `SubmitStepResult`, adopt the orchestrator for workflow step-assign.
- Gateway: handler-prologue helpers, generic `resolve[T]` for the 13 `xxxFrom(ctx)` accessors, fix broken `handleSubscriptionTest` payload wiring.
- Cross-cutting: standardise context keys on empty-struct, share the fire-and-forget event-emit helper, replace direct `slog.*` calls with `observe.Logger`, share YAML bounded decoder between artifact and workflow parsers.
- CLI: extract `PostFileAsBody` helper.

### Out of Scope

- `cmd/spine/cmd_serve.go` split and orchestrator-wiring dedup — owned by INIT-016.
- Env-var parsing move into `internal/config` — owned by INIT-016.
- `api/` directory restructuring.
- New abstractions beyond those above; this epic is purely dedup + layering cleanup.

---

## 3. Success Criteria

1. All tasks completed and validated via the standard spine-validate-task flow.
2. `internal/store/postgres.go` below 1000 lines after split; no duplicated scan/NotFound boilerplate.
3. `internal/artifact/service.go` and `internal/workflow/service.go` no longer carry parallel git-write implementations.
4. `handleSubscriptionTest` sends the advertised ping payload.
5. Gateway handlers share one auth/store-check/decode prologue.
6. No behaviour changes — full test suite still passes.
