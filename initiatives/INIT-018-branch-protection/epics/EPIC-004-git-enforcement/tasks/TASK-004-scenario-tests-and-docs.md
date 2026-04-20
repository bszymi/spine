---
id: TASK-004
type: Task
title: "Git-path scenario tests and documentation sweep"
status: Completed
work_type: testing+documentation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/tasks/TASK-001-enable-receive-pack.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/tasks/TASK-002-pre-receive-enforcement.md
  - type: depends_on
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/tasks/TASK-003-push-option-override.md
---

# TASK-004 — Git-path scenario tests and documentation sweep

---

## Purpose

Close EPIC-004 with end-to-end tests that exercise a real Git client against a running `internal/githttp` instance, plus the doc edits operators need to actually use push.

---

## Deliverable

1. **Scenario suite.** Tests that drive real pushes (via the `git` binary or a Go Git client) against a test `internal/githttp` server:
   - Clone → commit → push to unprotected branch: succeeds.
   - Push to `no-direct-write` branch: rejected with named rule and branch.
   - Delete `no-delete` branch: rejected.
   - Operator push with `-o spine.override=true` to `no-direct-write` branch: succeeds; governance event emitted; client-produced commit byte-identical on the server.
   - Non-operator push with the same option: rejected.
   - Push whose push-option signals override on a ref that does not need override: succeeds, no event.
   - Mixed push with one denied ref: entire push rejected, no partial application.

2. **Docs sweep.**
   - `/architecture/git-integration.md`: document the enabled push surface, the config flag (EPIC-004 TASK-001), the pre-receive check, and the `-o spine.override=true` override. Retire the "push is on the roadmap" prose.
   - Operator-facing doc (or new one under `/product/` if none exists): "How to push to a Spine-hosted repo" + "How to override protection as an operator" with copy-pasteable `git` commands.
   - Client-facing error-message catalogue: the exact wire-protocol messages users will see when rejected, so support teams can map them to remediation.

3. **Release notes.** Extend whatever EPIC-003 TASK-004 seeded with the Git-path story: push is now enabled by flag, protection rules apply, override is `-o spine.override=true`.

---

## Acceptance Criteria

- Scenario suite runs in CI against a real `internal/githttp` instance and covers every case listed.
- `/architecture/git-integration.md` describes push as a first-class, enforced surface — no "roadmap" language remains.
- An operator can copy the documented `git push -o spine.override=true …` command and use it without reading the source.
- EPIC-004 is closeable: a reader can audit ADR-009 §3 and §4 compliance empirically, push + API paths inclusive.
