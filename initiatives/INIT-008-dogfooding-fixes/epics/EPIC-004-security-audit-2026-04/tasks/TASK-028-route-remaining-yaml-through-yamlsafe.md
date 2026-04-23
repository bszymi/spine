---
id: TASK-028
type: Task
title: Route remaining repo-controlled YAML through yamlsafe
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: security
created: 2026-04-24
last_updated: 2026-04-24
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/tasks/TASK-013-share-yaml-bounded-decoder.md
---

# TASK-028 — Route remaining repo-controlled YAML through yamlsafe

---

## Purpose

Artifact and workflow parsing use `internal/yamlsafe`, but several repo-controlled YAML paths still call `yaml.Unmarshal` directly. A malicious repository or pushed workflow can use oversized, deeply nested, or alias-heavy YAML to stress config loading or projection rebuilds.

## Deliverable

- Update `internal/config.Load` to read `.spine.yaml` with an explicit size cap and decode via `yamlsafe`.
- Update `internal/projection.Service.projectWorkflow` to reuse `workflow.Parse` or `yamlsafe.Decode` instead of raw `yaml.Unmarshal`.
- Review CLI-only workflow inspection paths and either route them through `yamlsafe` or document why they only process local trusted files.
- Add regression tests for oversized config/workflow YAML and alias-heavy projection input.

## Acceptance Criteria

- `rg "yaml\\.Unmarshal|yaml\\.NewDecoder" internal cmd workflows -g "*.go"` shows only intentionally reviewed uses.
- Oversized `.spine.yaml` and oversized projected workflow YAML fail with a bounded, actionable error.
- Projection still stores the workflow definition JSONB for valid workflow files.
- Existing artifact, workflow, projection, and config tests pass.
