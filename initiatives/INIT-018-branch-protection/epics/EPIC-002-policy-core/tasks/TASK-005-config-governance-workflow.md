---
id: TASK-005
type: Task
title: "Decide the edit flow for /.spine/branch-protection.yaml"
status: Completed
work_type: design
created: 2026-04-18
last_updated: 2026-04-20
completed: 2026-04-20
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
---

# TASK-005 — Decide the edit flow for `/.spine/branch-protection.yaml`

---

## Purpose

Resolve ADR-009 §5's "deferred" language: decide how edits to `/.spine/branch-protection.yaml` are authorized, and record the decision in the ADR. The original framing asked for a lifecycle workflow; the resolution below argues one is not needed and records the alternative.

---

## Design Note

Two options were on the table:

1. **Dedicated `/workflows/branch-protection-lifecycle.yaml`.** A new lifecycle workflow, parallel to `workflow-lifecycle.yaml`, governs edits via draft → review → merge.
2. **Bracket under `workflow-lifecycle.yaml`.** Reuse the existing lifecycle workflow by teaching the binding layer that `/.spine/branch-protection.yaml` routes to it.

A third option surfaced during review and won:

3. **No lifecycle workflow. Operator-only direct commit using the existing §4 override surface (`git push -o spine.override=true`).**

### Why option 3

- **The audience is already operator-only.** Branch-protection policy is an operator/admin concern by design. The override-to-edit path (ADR-009 §4) is gated on `operator+` regardless of which flow lands the change. A reviewer-authored draft-review flow produces a review step that only operators are qualified to land — a ceremony whose gate is upstream of the ceremony itself.
- **No production effort to review.** The file is a short list of `(branch, protections[])` entries. Workflows, ADRs, and Tasks benefit from drafting and review because there is a document being produced. A three-line rule change does not.
- **The bootstrap-deadlock recursion disappears.** Under options 1 and 2, every rule change has to merge through a branch covered by the rule it is changing — so self-disable and bootstrap-recovery cases still fall back to the operator-override path. Picking option 3 collapses "normal edit" and "recovery edit" into the same single path and eliminates the distinction.
- **Precedent.** `.spine.yaml`, `.gitignore`, and other root-level config files seeded by `spine init-repo` are not governed by lifecycle workflows. `/.spine/branch-protection.yaml` fits that shape, not the governed-artifact shape.

### What option 3 forecloses

- Non-operators cannot propose a rule change through the API. They escalate out-of-band (file an issue, ask an operator). If dogfooding shows that operators routinely hand-edit complex rule changes and would benefit from a draft/review flow, a future ADR can introduce a lifecycle workflow. This ADR closes v1 without one.

---

## Deliverables — done

1. **ADR-009 §5 rewritten** to record the resolved flow: operator-only direct commit pushed with the existing §4 override surface (`git push -o spine.override=true`), producing a `branch_protection.override` governance event via the same audit path as any other honored override. No new trailer or signaling mechanism is introduced. The previously "deferred" language is gone.
2. **ADR-009 §1** no longer calls the config a "governed artifact"; it now describes `/.spine/branch-protection.yaml` as a Git-tracked operator config.
3. **ADR-009 Consequences** updated: the "governed like any other artifact" positive bullet is reframed as "edits produce a governance event"; the "self-protection recursion" negative bullet is reframed as "every edit requires an operator, by design".
4. **EPIC-002 acceptance criteria** updated to reference the §5 resolution instead of a named governance workflow.
5. **Product doc `/product/features/branch-protection.md`** reconciled: §2.1 now says operators configure protection (previously "reviewers"), and §6 step 3 now describes the operator-only direct-commit flow. The stale "reviewer-authored configuration" phrase in ADR-009's Context paragraph is replaced with "operator-edited configuration".
6. **Seed comment in `internal/cli/initrepo.go`** updated: newly initialized repos no longer point operators at a non-existent "protection-config governance workflow". The seeded `.spine/branch-protection.yaml` header now names the override push path (`git push -o spine.override=true`) and ADR-009 §5.
7. **Self-protection scope** clarified in ADR-009 §1 and §5: the "every edit to the config is an operator override" property holds only while the authoritative branch carries `no-direct-write`. This ADR does not hard-code that as a schema invariant (§6 keeps path-scoped rules out of scope, and hard-coding "authoritative branch cannot be relaxed" would foreclose legitimate configurations).

No workflow YAML, no new artifact type, no new gateway endpoint, no E2E test infrastructure was added — by design of option 3.

---

## Acceptance Criteria

- ADR-009 §5 no longer contains "deferred" language and states the resolved choice.
- EPIC-002 no longer lists a governance-workflow deliverable for branch-protection editing.
- The operator-override edit path referenced in §5 already exists in the policy module (§4); no new code is required for this task.
