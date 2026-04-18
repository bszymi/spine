---
id: EPIC-003
type: Epic
title: "Branch Protection — Spine API-Path Enforcement"
status: Pending
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
---

# EPIC-003 — Branch Protection — Spine API-Path Enforcement

---

## Purpose

Wire `branchprotect.Policy.Evaluate` into every Spine API code path that advances a ref. After this epic, an `artifact.create` or `workflow.create` call that tries to advance `main` without a `write_context.run_id` (i.e. a direct write) is rejected; a governed merge still goes through.

This is the half of enforcement that does not depend on the Git push path being enabled, so it ships value standalone.

---

## Key Work Areas

- Artifact Service commit helpers consult `Policy.Evaluate` for every advance.
- Orchestrator merge path and divergence-branch lifecycle classify requests correctly (GovernedMerge when a Run authorizes the merge; DirectWrite otherwise).
- `write_context.override` field: parsing, role gate, audit.
- Scenario tests across the decision matrix; docs sweep to replace the "not allowed" language in Git Integration Contract §6.3 with "enforced via ADR-009".

---

## Primary Outputs

- Updated Artifact Service (`internal/artifact`) and Orchestrator (`internal/engine`) with branch-protection checks.
- `write_context.override` field parsed, validated, and honored only for operator+ actors.
- `branch_protection.override` governance event emitted on every honored override.
- `Branch-Protection-Override: true` commit trailer on API-path overrides (per ADR-009 §4).
- Updated `/architecture/git-integration.md` and `/architecture/security-model.md`.

---

## Acceptance Criteria

- Direct commit attempts to `main` (no `write_context.run_id`) via Spine API are rejected with a clear error unless `write_context.override` is set and the actor is operator+.
- Governed merges (`write_context.run_id` present and tied to an authorizing Run outcome) on `main` are allowed.
- Every honored override produces both a `branch_protection.override` governance event and the `Branch-Protection-Override: true` trailer on the resulting commit (API path only — ADR-009 §4).
- Git Integration Contract §6.3 is rewritten to describe enforcement via `branchprotect.Policy` rather than documentary prohibition.
- Scenario tests cover: allowed governed merge, rejected direct write, rejected delete of `no-delete` branch, allowed override by operator, rejected override by non-operator.
- EPIC-004 (Git-path enforcement) is unaffected — the same policy module backs both paths.
