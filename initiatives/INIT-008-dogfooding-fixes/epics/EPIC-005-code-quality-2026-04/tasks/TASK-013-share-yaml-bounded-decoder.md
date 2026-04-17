---
id: TASK-013
type: Task
title: "Share bounded YAML decoder between artifact and workflow parsers"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
completed: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-013 — Share Bounded YAML Decoder

---

## Purpose

`internal/artifact/parser.go` L271-320 (`decodeBoundedYAML`) enforces size/node/alias/depth bounds (64KB, 10k nodes, 100 aliases, depth 64) when decoding artifact YAML. `internal/workflow/parser.go` L27-41 does a plain `yaml.Unmarshal(content, &wf)` with no bounds, even though workflow YAML arrives through the same `workflow.create` / `workflow.update` HTTP endpoints as artifact bodies. Asymmetric hardening is a maintenance hazard and the documentation-vs-code mismatch is confusing.

---

## Deliverable

1. Create `internal/yamlsafe/decoder.go` exposing `Decode(data []byte, out any) error` with the bounds currently in `decodeBoundedYAML`.
2. Replace `artifact/parser.go` `decodeBoundedYAML` call with `yamlsafe.Decode`.
3. Replace `workflow/parser.go` `yaml.Unmarshal` with `yamlsafe.Decode`.
4. Keep the bound values as package-level constants so any future tuning is a single edit.

---

## Acceptance Criteria

- Both parsers go through `yamlsafe.Decode`.
- Existing artifact and workflow parser tests pass unchanged.
- Add one workflow parser test asserting that a >64KB YAML or a deeply-aliased document is rejected.
