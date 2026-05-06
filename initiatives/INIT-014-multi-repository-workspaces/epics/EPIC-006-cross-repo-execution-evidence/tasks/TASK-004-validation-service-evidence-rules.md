---
id: TASK-004
type: Task
title: Add evidence rules to validation service
status: Completed
acceptance: Approved
acceptance_rationale: |
  internal/validation/rules_evidence.go ships the EV-* family
  EPIC-006 needed: EV-001 missing-evidence per affected repo,
  EV-002 evidence branch + base commit match the run, EV-003 every
  blocking policy check has a result row, EV-004 every blocking
  check terminates as passed/skipped (warning-severity failures
  emit warnings, not errors), EV-005 stale evidence detection via
  BranchTipResolver. Four resolver options on validation.Engine
  (WithRunResolver, WithEvidenceResolver,
  WithValidationPolicyResolver, WithBranchTipResolver) gate the
  rules — workspaces not yet wired see no emissions.
  domain.ValidationError gains structured RepositoryID / PolicyID
  / CheckID fields plus a new ViolationExecutionEvidence
  classification (AC #5). architecture/validation-service.md §3.7
  documents the rule family and §6.4 the resolver contract. Codex
  2 passes clean back-to-back; the additive option-based design
  stayed inside the tight pattern-match heuristic.
last_updated: 2026-05-06
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-003-check-runner-integration-boundary.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-007-validation-policy-governance-update.md
---

# TASK-004 - Add Evidence Rules to Validation Service

---

## Purpose

Block publication when required multi-repo evidence is missing or failed.

## Deliverable

Extend validation with evidence-aware rules.

Rules:

- Every affected code repository must produce evidence.
- Evidence must match the run branch and head commit.
- Required policy checks must be present.
- Blocking checks must pass.
- Stale evidence must fail validation.

## Acceptance Criteria

- Missing evidence blocks publish.
- Evidence from the wrong branch or commit blocks publish.
- Failed blocking policy checks block publish.
- Warning-only policy checks do not block publish.
- Validation output names repo ID, policy ID, and failing check.

