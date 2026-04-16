---
id: TASK-006
type: Task
title: "Bound YAML decoder in artifact parser"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-006 — Bound YAML Decoder In Artifact Parser

---

## Purpose

`internal/artifact/parser.go:46` calls `yaml.Unmarshal` without depth, alias, or size limits. `yaml.v3` has no defaults for these. A deeply-nested or alias-expanded front-matter block (billion-laughs variant) can consume unbounded memory during parse. The API-level `maxBodySize` does not protect against expansion after decoding begins.

Additionally, `internal/gateway/handlers_helpers.go:16-29` accepts a large JSON payload carrying a YAML-rich `content` field that bypasses the outer content-type check.

---

## Deliverable

- Wrap the front-matter YAML parse in a bounded reader (reject raw front-matter > N KB).
- Reject documents exceeding a maximum alias count and nesting depth (walk the `yaml.Node` tree before unmarshal, or use a custom `UnmarshalYAML` pre-check).
- Add an explicit size cap on the artifact `content` field in the gateway handler, independent of overall request body size.

---

## Acceptance Criteria

- A front-matter block of 10 000 nested maps or 10 000 aliases is rejected with a 4xx, not an OOM.
- Artifact `content` > cap returns 413.
- Unit tests cover both limits with adversarial fixtures.
