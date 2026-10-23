---
id: EPIC-005
type: Epic
title: Close-out and SMP Hand-off
status: Pending
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
owner: bszymi
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/initiative.md
---

# EPIC-005 — Close-out and SMP Hand-off

---

## 1. Purpose

This epic holds the initiative's closing task. Its acceptance spans every other epic, so it does
not belong to any of them.

- It integrates the final waves.
- It runs the cross-task review and the gates at the closing commit.
- It reconciles the documentation.
- It names the closing commit that SMP pins.
- It decides when the artifacts and fixes may be pushed to the public repository (initiative
  owner question 1).

---

## 2. Scope

### In scope

- The integration branch, the cross-task review, the gates, `KNOWN-LIMITATIONS.md`,
  `security-model.md`, `CHANGELOG.md`, and initiative §7e progress.
- The status lifecycle of every INIT-024 task and epic.

### Out of scope

- New fixes. A defect found at close becomes a task in the owning epic, or a follow-up.

---

## 3. Task Inventory

| ID | Title | Blocked by | SMP impact |
|---|---|---|---|
| TASK-010 | Initiative close: integration, gates, documentation and the SMP hand-off | TASK-001 … TASK-009 | yes: names the closing pin |

---

## 4. Acceptance Criteria

- The exit criteria in initiative §8 hold, with the evidence recorded in TASK-010's
  Implementation Notes.
