---
id: INIT-018
type: Initiative
title: Branch Protection
status: Pending
owner: bszymi
created: 2026-04-18
links:
  - type: related_to
    target: /architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md
---

# INIT-018 — Branch Protection

---

## Purpose

Spine exposes a Git interface so actors can push directly to the repository. Today any branch can be force-pushed or deleted, and the authoritative branch (typically `main`) has no enforced "merge through Spine only" rule. Teams that use Spine to govern merges need the same guarantees GitHub/GitLab give them at the forge level, but enforced at the Spine boundary.

This initiative scopes the decision work for **branch protection** as a first-class Spine concept — what it protects, how it is configured, where the configuration lives, and how it is overridden.

---

## Motivation

- **Deletion protection.** Long-lived branches (e.g. `staging`, release branches) should not be deletable by accident or by an actor that lacks that authority.
- **Direct-write protection.** The authoritative branch should only advance via a governed merge. Ad-hoc pushes bypass the approval/merge machinery the rest of Spine depends on.
- **Consistent with Spine's governance story.** Spine already governs *what* work happens and *how* it is reviewed; protecting the branches those reviews land on closes the loop.
- **Operator escape hatch.** Recovery scenarios (a stuck merge, a broken `main`) must remain possible for a privileged role without disabling protection repo-wide.

---

## Scope

### In Scope (EPIC-001 — Discovery)

- Product-level description of the feature: problem, target users, what is protected, what is not, override model.
- ADR covering the technical decision: configuration storage (Git file vs. database), enforcement point (git-receive hook vs. Spine API layer vs. both), override surface, interaction with planning runs and divergence branches.

### Out of Scope (for now)

- Implementation — deferred until the ADR is Accepted.
- Mirroring GitHub/GitLab's full branch-protection ruleset (status checks, required review counts, etc.). Spine's initial scope is deletion + direct-write only; richer rules can be layered later.

---

## Success Criteria

INIT-018's discovery phase is complete when the product description and the ADR are both produced and the ADR is Accepted, giving a clear go/no-go for implementation epics.

---

## Primary Artifacts Produced (Discovery Phase)

- `/initiatives/INIT-018-branch-protection/product.md` — feature-level product description.
- `/architecture/adr/ADR-009-branch-protection.md` — governance + storage + enforcement decision.

---

## Open Questions (to be resolved by the ADR)

- **Configuration storage.** Spine's source of truth is Git. Should branch-protection rules live in a versioned file (e.g. `spine/branch-protection.yaml`) that is itself committed and reviewable, or in the runtime database for fast dynamic updates? Trade-off: Git-stored rules are auditable and branch-scoped by definition but create a bootstrap question ("who protects the file that defines protection?"); DB-stored rules are mutable at runtime but weaken the "Git is source of truth" invariant.
- **Enforcement point.** The git-receive path and the Spine HTTP/RPC merge endpoints are two distinct entry points. Protection must apply to both; the ADR should pick whether enforcement is centralized in a policy module or duplicated (and how to keep them in sync).
- **Override model.** Which role (operator?) can bypass protection, under what audit requirements, and whether override is per-push or a mode flag.
- **Interaction with run branches.** Planning runs and divergence features create and delete branches programmatically. The protection model must not break those flows — either by scope (protection applies only to named branches) or by implicit permission (Spine-owned operations are pre-authorized).
