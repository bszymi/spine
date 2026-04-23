---
id: EPIC-001
type: Epic
title: Scheduler & Runtime (Round 2)
status: In Progress
initiative: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
owner: bszymi
created: 2026-04-23
last_updated: 2026-04-23
links:
  - type: parent
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-015-engine-driven-commit-outcome.md
---

# EPIC-001 — Scheduler & Runtime (Round 2)

---

## 1. Purpose

Address scheduler and runtime regressions and incomplete implementations surfaced by ongoing use of Spine against the SMP workspace after INIT-008 closed. The first concrete issue is that the engine-owned `publish` step (introduced by Spine `INIT-008/EPIC-001/TASK-015` and required by SMP `ADR-010`) does not actually advance when the step activates — the merge handler is never invoked, so runs wedge at `status: assigned` on the `publish` step until an operator merges by hand.

TASK-015 was marked Completed on 2026-04-22 but its own acceptance criteria ("A run on `task-default.yaml` completes end-to-end with zero runner dispatch events for the `publish` step" and "the dispatcher MUST NOT emit a runner dispatch event for `spine_only` steps") are not being met today. This epic is the home for closing that gap and any similar regressions found during round-2 dogfooding.

---

## 2. Scope

### In Scope

- Engine handler dispatch for `type: internal` + `execution.mode: spine_only` steps on activation
- Scheduler eligibility path correctness for internal steps (no runner dispatch events, no actor assignment)
- Observability on internal handler invocation and failure
- Other scheduler/runtime regressions that surface under sustained SMP use

### Out of Scope

- New scheduler capabilities (belong in dedicated initiatives)
- Changes to how Spine pushes to git remotes (ADR-009 remains the reference)
- Generalising internal handlers beyond `merge` — that's a future design concern, not a round-2 bugfix

---

## 3. Success Criteria

The epic is successful when:

1. A fresh `task-default` run against the SMP workspace completes end-to-end (`execute → validate → verify → review → publish → end`) without an operator manually merging or flipping status.
2. Scenario tests exercise the `publish` step path and catch a future regression if the handler stops firing.
3. No runner dispatch events are emitted for `spine_only` steps on any workflow in the governed set.

---

## 4. Work Breakdown

| Task | Title |
|------|-------|
| TASK-001 | Fire engine-owned publish handler on step activation |

More tasks will be added as round-2 issues surface.

---

## 5. Links

- Original task: `/initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-015-engine-driven-commit-outcome.md`
- SMP ADR: `smp:architecture/adrs/ADR-010-single-merge-path-spine-driven-commit.md`
- SMP follow-up fix (enqueue filter): `smp:initiatives/INIT-001-build-spine-management-platform/epics/epic-051-runner-execution-improvements/tasks/TASK-014-remove-runner-commit-step.md` (landed 2026-04-23 as a side change on the SMP `TASK-001` merge commit)
