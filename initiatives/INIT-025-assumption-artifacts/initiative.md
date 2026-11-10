---
id: INIT-025
type: Initiative
title: Assumption Artifacts
status: In Progress
owner: bszymi
created: 2026-11-10
last_updated: 2026-11-10
links:
  - type: related_to
    target: /architecture/adr/ADR-017-assumption-artifact-type.md
  - type: related_to
    target: /governance/artifact-schema.md
---

# INIT-025 — Assumption Artifacts

---

## 1. Intent

Let a repository record what its decisions rely on, and whether anyone has tested it. Core gains
one artifact type (`Assumption`), three link types (`depends_on`, `supports`, `refutes`), two
advisory rules, and a fix so that links can be looked up by their target.

The embedder work that reads this model (an assumption ledger API and view) is SMP INIT-028.

---

## 2. Scope

### In scope

- The `Assumption` type: parsing, validation, ID allocation, path rule, status lifecycle.
- The three link types.
- Rules AL-001 and AL-002.
- `QueryArtifactLinksByTarget` matching both path forms.
- Schema and state-machine documentation.

### Out of scope

- An `Evidence` type, per-link attributes, time-based staleness.
- The standalone service's HTTP handlers beyond what the shared code changes for free.
- Anything in SMP.

---

## 3. Success Criteria

1. An `Assumption` Markdown file parses, validates and is projected.
2. The lifecycle in ADR-017 §2 is declared, golden-tested and documented.
3. A lookup by projection path returns links stored with a leading slash.
4. `go test ./...` is green and the boundary lints pass.

---

## 4. Primary Artifacts Produced

- `/architecture/adr/ADR-017-assumption-artifact-type.md`
- `core/domain`, `core/artifact`, `core/validation`, `adapters/store` changes
- `/governance/artifact-schema.md`, `/architecture/engine-state-machine.md` updates

---

## 5. Constraints and Non-Negotiables

- No migration and no change to stored rows.
- Core imports nothing from an embedder.
- Changing the lifecycle table moves its golden test and its documentation table with it.

---

## 6. Risks

- **Behaviour change in blocking transitions:** dependents that were never found are now found,
  so `task_unblocked` events begin to fire. Mitigation: a test that pins the new behaviour and a
  CHANGELOG entry for embedders.
- **Concept creep in Core:** mitigated by ADR-017 §6, which names what is deliberately left out.

---

## 7. Work Breakdown

### EPIC-001 — Assumption type and links

- TASK-001 — The Assumption type, its lifecycle and the three link types
- TASK-002 — Lookups by target, and the evidence consistency rules

---

## 8. Exit Criteria

- Both tasks Completed, merged to `main`, and pinned by SMP.

---

## 9. Links

- ADR: `/architecture/adr/ADR-017-assumption-artifact-type.md`
- SMP initiative: `smp:/initiatives/INIT-028-assumption-ledger/initiative.md`
