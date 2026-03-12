# TASK-003 — Constitution Validation Rules

**Epic:** EPIC-004 — Governance Refinement
**Initiative:** INIT-001 — Foundations
**Status:** Complete

---

## Purpose

Update the Constitution to encode governing rules for workflow-embedded cross-artifact validation.

The Constitution currently defines structural constraints (Git truth, governed execution, actor neutrality, etc.) but does not explicitly require that artifacts are validated against the broader governed system state during workflow progression. This task adds rules ensuring that validation is part of workflows, not an ad-hoc activity.

## Deliverable

Updated `/governance/Constitution.md`

Content should add explicit rules around:

- Cross-artifact dependency awareness — artifacts do not exist in isolation when their meaning depends on other governed artifacts or current system state
- Validation as part of workflow progression — workflow steps may require cross-artifact consistency checks before approval or completion
- Artifact approval based on governed context, not isolated content only
- Contradictions and mismatches must be surfaced explicitly and create follow-up work rather than being silently ignored
- AI and human actors participate in the same validation-governed system

## Acceptance Criteria

- Constitution contains explicit principles requiring cross-artifact validation during workflows
- Rules are structural constraints (what must be true), not operational guidance (how to do it)
- Existing constitutional principles are preserved and not contradicted
- New rules are consistent with the domain model and existing ADRs
