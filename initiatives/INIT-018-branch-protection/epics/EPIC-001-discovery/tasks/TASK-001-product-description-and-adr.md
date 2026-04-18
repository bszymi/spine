---
id: TASK-001
type: Task
title: "Author branch-protection product description and ADR-009"
status: Pending
work_type: documentation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-001-discovery/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-001-discovery/epic.md
---

# TASK-001 — Author branch-protection product description and ADR-009

---

## Context

Spine exposes a Git interface that lets actors push directly to the repository. There is no enforced protection for:

1. **Deletion** of designated long-lived branches (e.g. `staging`, release branches).
2. **Direct writes** to the authoritative branch (e.g. `main`), which should only advance via a Spine-governed merge.

Forge-level protection (GitHub/GitLab) does not help here — Spine is the Git server in this deployment model. The rules must be enforced at the Spine boundary and must survive an operator escape hatch for recovery.

Before any implementation, we need a product-level description of the feature and an architectural decision record that answers the open questions listed in `/initiatives/INIT-018-branch-protection/initiative.md`.

---

## Deliverable

### 1. Product description — `/initiatives/INIT-018-branch-protection/product.md`

A feature-level product doc (not code-design) covering:

- **Problem statement**: what goes wrong today without branch protection (concrete scenarios).
- **Users / personas**: who configures protection (reviewer? operator?), who is protected *from* (regular actors, agents), and who can override.
- **What is protected**: enumerate the protection types in scope — at minimum `no-delete` and `no-direct-write` — with crisp definitions of what each blocks.
- **What is not protected** (explicit non-goals): e.g. status checks, required review counts, per-path rules. Keep the feature focused.
- **User-visible behavior**: example flows — "I push to `main` as a regular actor → rejected with message X", "I push to `main` as an operator with override flag → accepted, audited as Y", "I try to delete `staging` → rejected", "Spine's own merge operation on `main` → accepted".
- **Configuration UX**: how a team declares a branch as protected. Don't fix the technical answer yet (that's the ADR) but describe the authoring experience — who edits it, how often it changes, how it is reviewed.
- **Override model**: which role can bypass protection, what audit trail is produced, whether override is per-operation or a mode flag.

Target length: roughly the density of other governance docs in `/product/`. Written from the user's perspective, not the implementation's.

### 2. ADR — `/architecture/adr/ADR-009-branch-protection.md`

Follow `/templates/adr-template.md`. Status begins as `Proposed`; must be `Accepted` before any implementation task lands.

Cover:

- **Context**: reference ADR-001 (Git as source of truth), ADR-007 (resource separation), ADR-008 (workflow lifecycle governance — includes its own operator-bypass model, which is a reference point). Explain why forge-level protection is not an option for Spine.
- **Decision — configuration storage**: Git-versioned file (e.g. `spine/branch-protection.yaml`) vs. runtime database. Resolve the trade-offs noted in the initiative (auditability + branch scope vs. runtime mutability). If Git-stored, specify the bootstrap rule — what protects the protection file itself.
- **Decision — enforcement point**: git-receive path, Spine HTTP/RPC merge endpoints, or a shared policy module. Describe how Spine-owned operations (planning-run merges, divergence branch lifecycle, scheduler recovery) are exempted without becoming a blanket bypass.
- **Decision — protection types**: confirm the initial set (`no-delete`, `no-direct-write`) and what each means concretely. Call out anything explicitly deferred.
- **Decision — override model**: the role allowed to override (operator, aligned with ADR-008), the mechanism (per-operation flag? header? write_context?), and the audit artifact produced on each override.
- **Decision — interaction with run branches**: how planning-run branches, divergence branches, and similar system-created branches interact with protection rules. Either by naming scope ("protection applies only to explicitly-listed branches") or by implicit permission for internal operations.
- **Consequences**: what becomes easier, what becomes harder, what operators need to know on day one.
- **Cross-references**: ADRs above, governance docs (charter, task-lifecycle), relevant architecture docs.

---

## Acceptance Criteria

- Both files exist at the paths above and pass artifact schema validation.
- The ADR is marked `Accepted` and answers every open question listed in `INIT-018-branch-protection/initiative.md`.
- An implementer reading only the ADR can tell *where* enforcement lives, *what* the config shape is, and *who* can override — without re-deriving those answers.
- The product description is written for a non-implementer audience: a reviewer who cares about governance, not a Go developer looking for an API contract.
- Non-goals are explicit enough that a future contributor will not silently expand scope (e.g. by adding GitHub-parity rules without a new ADR).
