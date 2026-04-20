---
id: EPIC-002
type: Epic
title: "Branch Protection — Policy Core & Configuration"
status: Pending
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
---

# EPIC-002 — Branch Protection — Policy Core & Configuration

---

## Purpose

Build the shared foundation that both enforcement paths (Git and Spine API) depend on: the config artifact at `/.spine/branch-protection.yaml`, the `internal/branchprotect` policy package, the projection into a runtime table, the `spine init-repo` seed, and the governance flow that edits the config.

Both EPIC-003 (API-path enforcement) and EPIC-004 (Git-path enforcement) consume this epic's outputs. Nothing in this epic actually rejects an operation — the policy is inert until a call site wires it in.

---

## Key Work Areas

- Config file schema, parser, and artifact-type registration.
- `internal/branchprotect` package: `Policy`, `Request`, `Decision`, `Evaluate`, bootstrap defaults.
- Projection of the parsed config into `branch_protection_rules` runtime table.
- `spine init-repo` seeds the config with documented defaults.
- Design decision recorded in ADR-009 §5: protection config is operator-edited; no lifecycle workflow.

---

## Primary Outputs

- `internal/branchprotect/` package (policy evaluation, types, tests).
- `.spine/branch-protection.yaml` format + seeded content in new repos.
- `branch_protection_rules` runtime table + projection wiring.
- ADR-009 §5 updated with the resolved edit flow (operator-only direct commit via the existing §4 override surface — `git push -o spine.override=true`).
- Architecture doc updates describing the policy module and its integration points.

---

## Acceptance Criteria

- `branchprotect.Policy.Evaluate` returns correct `Allow`/`Deny` decisions for every rule type and operation kind called out in ADR-009 §2–§4, covered by unit tests.
- A fresh repository evaluates against the bootstrap defaults (`main` protected with `no-delete` + `no-direct-write`) even without a config file.
- `spine init-repo` seeds `/.spine/branch-protection.yaml` with the documented defaults, and existing tests still pass.
- The Projection Service mirrors `/.spine/branch-protection.yaml` into `branch_protection_rules` on merge and on bootstrap; reads are served from the runtime table.
- Editing the config is described in ADR-009 §5 as an operator-only direct commit pushed with the existing §4 override surface (`git push -o spine.override=true`); no lifecycle workflow. The open "deferred" language in §5 is replaced with this resolution.
- No call site actually rejects an operation yet — EPIC-003 and EPIC-004 do that. This epic ends with a wired but dormant policy module.
