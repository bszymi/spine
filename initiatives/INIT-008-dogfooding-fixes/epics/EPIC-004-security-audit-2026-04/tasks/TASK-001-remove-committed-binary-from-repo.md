---
id: TASK-001
type: Task
title: "Remove committed spine binary from repository history"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: chore
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-001 — Remove Committed Binary From Repository History

---

## Purpose

A 17 MB compiled Mach-O binary `./spine` is tracked in the repository root. This is a supply-chain risk (binary cannot be audited, may diverge from source) and bloats history. `.gitignore` already lists `spine` but the file is explicitly tracked.

---

## Deliverable

- Remove `./spine` from HEAD via `git rm -f spine`.
- Confirm `.gitignore` continues to exclude the built binary.
- Purge from history on the release branch(es) and force-push (coordinate with team — requires every clone to rebase).
- Add a pre-commit hook or CI check that rejects commits introducing Mach-O/ELF files at repo root.

---

## Acceptance Criteria

- `git ls-files spine` returns nothing on HEAD.
- CI rejects a PR that reintroduces a compiled binary at repo root.
- Team briefed on history rewrite date if performed.
