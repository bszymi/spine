---
id: TASK-004
type: Task
title: "API-path scenario tests and documentation sweep"
status: Pending
work_type: testing+documentation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/tasks/TASK-001-artifact-service-integration.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/tasks/TASK-002-orchestrator-integration.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-003-api-enforcement/tasks/TASK-003-write-context-override.md
---

# TASK-004 — API-path scenario tests and documentation sweep

---

## Purpose

Close out EPIC-003 with end-to-end scenario coverage plus the doc edits that turn the "not allowed" language scattered across architecture docs into references to the enforced policy.

---

## Deliverable

1. **Scenario tests.** A new test suite that exercises real API calls (not mocks of the policy) against a real workspace:
   - Direct commit to `main` via `artifact.create` without `run_id` → rejected, no ref advanced.
   - Governed merge via `Orchestrator.MergeRunBranch` after an authorizing Run outcome → allowed.
   - Deletion of `no-delete`-protected branch → rejected.
   - Operator override on a protected branch → allowed; governance event and commit trailer both present.
   - Non-operator override attempt → rejected with role-specific reason.
   - Bootstrap-defaults repo (no `branch-protection.yaml`): direct commit to `main` still rejected.

2. **Docs sweep.** Rewrite the "not allowed" documentation so it reflects the enforced reality:
   - `/architecture/git-integration.md` §6.3: replace the "direct manual merges of `spine/*` branches are not allowed" prose with a pointer to ADR-009's enforcement and a summary of what the policy denies.
   - `/architecture/security-model.md`: add a short subsection on branch protection, linking ADR-009.
   - Any `governance/` doc that repeats the "Spine requires governed merges" invariant: update the cross-reference.

3. **Changelog / release notes.** Add a line to whatever the repo uses for release notes (or create one under `tmp/` if nothing exists yet — user can promote later) describing the new rejection behavior and the `write_context.override` field.

---

## Acceptance Criteria

- Scenario suite runs in CI and covers every case listed.
- No doc in `/architecture/` or `/governance/` still describes authoritative-branch protection as documentary-only.
- The API change (`write_context.override`) is discoverable from at least one top-level doc without grepping the code.
- EPIC-003 is closeable: a reader of the scenario suite can verify every ADR-009 §3–§4 decision empirically.
