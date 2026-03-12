# TASK-004 — Guidelines Validation Guidance

**Epic:** EPIC-004 — Governance Refinement
**Initiative:** INIT-001 — Foundations
**Status:** In Progress

---

## Purpose

Update the Guidelines to provide practical guidance for workflow-embedded cross-artifact validation.

The Constitution (after TASK-003) will define the structural rules. The Guidelines must explain the practical interpretation: what each artifact type should validate against, when validation occurs in workflows, how mismatches should be reported, and when follow-up work should be created.

## Deliverable

Updated `/governance/guidelines.md`

Content should add practical guidance for:

- What each artifact type should validate against (e.g., Product validates against Charter and Architecture; Architecture validates against Product and implementation reality; Tasks validate against upstream artifacts and code assumptions)
- When validation should occur in workflow progression
- How mismatches should be reported and surfaced
- When new tasks should be created to resolve structural gaps
- How to distinguish between scope conflict, architectural conflict, implementation drift, and missing prerequisite work
- The initial creation sequence (Charter → Product → Architecture → Tasks → Code) vs the evolution phase where changes validate against existing governed state

## Acceptance Criteria

- Guidelines contain actionable validation guidance per artifact type
- Guidance distinguishes initial foundation phase from ongoing evolution phase
- Mismatch handling is described with clear escalation paths
- Content is operational guidance (how to do it), not structural constraints (those belong in the Constitution)
- Existing guidelines content is preserved and the incomplete ending is resolved
