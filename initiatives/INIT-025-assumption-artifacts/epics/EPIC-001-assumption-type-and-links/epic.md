---
id: EPIC-001
type: Epic
title: Assumption Type and Links
status: In Progress
initiative: /initiatives/INIT-025-assumption-artifacts/initiative.md
owner: bszymi
created: 2026-11-10
last_updated: 2026-11-10
links:
  - type: parent
    target: /initiatives/INIT-025-assumption-artifacts/initiative.md
---

# EPIC-001 — Assumption Type and Links

---

## Purpose

Implement ADR-017 in Core.

---

## Key Work Areas

- The `Assumption` artifact type and its declared lifecycle.
- The `depends_on`, `supports` and `refutes` link types.
- By-target link lookups that work for canonical targets.
- Advisory rules AL-001 and AL-002.

---

## Primary Outputs

- Code in `core/domain`, `core/artifact`, `core/validation`, `adapters/store`.
- Updated `/governance/artifact-schema.md` and `/architecture/engine-state-machine.md`.

---

## Acceptance Criteria

- Every decision in ADR-017 §1–§5 has a test that fails without it.
- `go test ./...`, `go vet ./...` and `dev/lint-boundaries.sh` pass.
