---
id: TASK-006
type: Task
title: Cross-repo evidence scenario tests
status: Completed
acceptance: Approved
acceptance_rationale: |
  internal/scenariotest/scenarios/cross_repo_evidence_test.go ships
  six scenario tests, each mapped 1:1 to a deliverable bullet
  (multi-repo all-pass; missing evidence blocks; failed blocking
  check blocks; warning-only allows publish; stale-commit blocks;
  evidence visible in run inspection). Each scenario stands up real
  on-disk primary + two code repositories (AC #1), wires the
  multi-repo orchestrator, calls StartRun (real branch fanout +
  baseline capture), commits real ExecutionEvidence YAML at the
  canonical path on the run branch, and exercises both the EV-*
  validation rules with all four resolvers wired (RunResolver via
  store.ListRunsByTask, EvidenceResolver via evidence.Load on the
  primary git client, PolicyResolver from in-memory fixtures
  matching the "code" role selector per AC #2, BranchTipResolver
  via per-repo CLIClient.RefSHA) and the production
  evidence.Querier. Validation result.Status drives the
  publish-block / publish-allow decision per AC #3; querier output
  is grouped by repo with raw logs referenced via evidence_uri (not
  embedded) and missing evidence visible before publish per AC #4.
  Scenarios use temporary repos via scenariotest framework so
  evidence remains auditable on the run branch after the scenario
  completes (AC #4 echo). Closes EPIC-006. Codex 2 passes clean
  back-to-back — pattern-match iteration (test-only PR with
  scenariotest framework integration).
last_updated: 2026-05-06
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-005-evidence-query-and-reporting.md
---

# TASK-006 - Cross-Repo Evidence Scenario Tests

---

## Purpose

Prove that governed intent can require and validate code-repo evidence end to end.

## Deliverable

Add scenario tests covering ADR-linked policy, check execution, evidence recording, validation, and publish blocking.

Scenarios:

- Multi-repo task with all checks passing.
- Missing evidence blocks publish.
- Failed blocking check blocks publish.
- Warning-only policy allows publish with warnings.
- Evidence tied to stale commit blocks publish.
- Evidence is visible in run inspection output.

## Acceptance Criteria

- Scenario tests use temporary primary and code repositories.
- Required policy checks are linked from governed artifacts.
- Publish proceeds only when required evidence is valid.
- Evidence remains auditable after the run completes.
