---
id: TASK-022
type: Task
title: "Allowlist git credential helpers"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-022 — Allowlist Git Credential Helpers

---

## Purpose

`cmd/spine/main.go:311-316` accepts `SPINE_GIT_CREDENTIAL_HELPER` and validates it via `git.ValidateCredentialHelper`. The validator screens for obvious shell metacharacters but does not restrict the helper name. Because git treats an arbitrary helper string as "run a program," a misconfigured value is a latent RCE surface.

---

## Deliverable

- Replace the current free-form validator with an allowlist of known-safe helper names: `cache`, `store`, `osxkeychain`, `manager`, `pass`, plus any internal helpers Spine ships.
- Reject anything outside the allowlist at startup.

---

## Acceptance Criteria

- `SPINE_GIT_CREDENTIAL_HELPER=pass` starts successfully.
- `SPINE_GIT_CREDENTIAL_HELPER=/tmp/evil.sh` fails startup.
- Unit tests cover both cases.
