---
id: TASK-003
type: Task
title: Implement check runner integration boundary
status: Completed
acceptance: Approved
acceptance_rationale: |
  internal/checkrunner ships the boundary EPIC-006 needed: a narrow
  Runner interface (Request, Result, four-outcome enum
  pass/fail/timeout/unavailable) plus LocalCommandRunner for
  kind=command checks and a Normalize helper that round-trips into
  ExecutionEvidence.Validate. Logs flow through a separate LogSink
  interface so the type system enforces "preserve raw logs as
  references, not inline evidence". The Runner interface does not
  switch on kind itself — adding an external-CI runner is a new type
  implementing the same contract, no shared code touched, satisfying
  AC #4 ("does not assume a specific CI provider"). Unit tests cover
  pass / fail / timeout / unavailable (5 sub-cases) plus log routing
  / truncation / sink failures / concurrent runs / caller-deadline
  vs policy-timeout / leader-exit-through-pipe-drain / shell-126/127
  / timeout overflow. classify_internal_test.go pins the decision
  matrix directly. Architecture/check-runner.md documents the
  classification precedence and log-reference contract. Codex 17
  passes; passes 16 + 17 clean back-to-back. 13 P1/P2 findings
  fixed across the iteration (process-group kill, sink-write
  surfacing, ctx-err snapshotting, empty LogRef on no-sink, caller
  deadline distinction, log-ref uniqueness, EvidenceURI carry-
  through, ErrWaitDelay preserves verdict, leader exit beats
  deadline, shell 126/127 → Unavailable, timeout overflow guard,
  policy-vs-caller deadline ordering, Windows Kill ExitCode).
last_updated: 2026-05-06
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-002-adr-linked-validation-policy-format.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-005-runner-clone-context.md
---

# TASK-003 - Implement Check Runner Integration Boundary

---

## Purpose

Provide a narrow interface for executing or collecting validation checks from code repositories.

## Deliverable

Add a check runner boundary that can support local commands first and external CI integrations later.

Interface responsibilities:

- Receive repository ID, branch, commit, and policy requirements.
- Execute or request checks.
- Return structured results.
- Preserve raw logs as references, not inline evidence.
- Classify check failures.

## Acceptance Criteria

- Local command checks can run against a cloned repository branch.
- Check results are normalized into the evidence schema.
- Timeouts and execution failures are classified.
- The interface does not assume a specific CI provider.
- Unit tests cover pass, fail, timeout, and unavailable states.

