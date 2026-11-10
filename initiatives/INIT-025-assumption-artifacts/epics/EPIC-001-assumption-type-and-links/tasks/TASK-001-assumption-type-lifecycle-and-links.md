---
id: TASK-001
type: Task
title: The Assumption type, its lifecycle and the three link types
status: Pending
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
---

# TASK-001 — The Assumption type, its lifecycle and the three link types

---

## Purpose

Implement ADR-017 §1–§3: Core accepts `type: Assumption` and the `depends_on`, `supports` and
`refutes` link types.

## Deliverable

- `core/domain/artifact.go`: `ArtifactTypeAssumption`; statuses `Untested`, `Supported`,
  `Contradicted`; link-type constants.
- `core/domain/artifact_lifecycle.go`: the Assumption lifecycle.
- `core/artifact`: parser and validator required fields (`id`, `created`), ID pattern `ASM-NNN`,
  ID prefix and padding, path building under `assumptions/`.
- `core/validation/rules_structural.go`: SI-005 prefix `assumptions/`.
- `governance/artifact-schema.md` §4.1, new §5.10, §6; `architecture/engine-state-machine.md` §5A table.
- `CHANGELOG.md` entry.

## Acceptance Criteria

- A valid Assumption file parses and validates; one missing `id` or `created`, or with a bad ID
  or status, is refused with a field-level error.
- The lifecycle golden test and the documentation-table test pass with the new rows.
- `Rejected` and `Superseded` have no outbound edge for Assumption.
- A link of each new type validates; an unknown type is still refused.
- An Assumption is discovered (`IsArtifact`) and therefore projected.
- `go test ./...` passes.
