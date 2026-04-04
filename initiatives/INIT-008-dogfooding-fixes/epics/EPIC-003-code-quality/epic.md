---
id: EPIC-003
type: Epic
title: Code Quality
status: Pending
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
owner: bszymi
created: 2026-04-04
last_updated: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/initiative.md
---

# EPIC-003 — Code Quality

---

## 1. Purpose

Address code quality and duplication findings from a full codebase review. Focus on reducing maintenance burden, eliminating error-prone duplicated logic, and fixing data races.

---

## 2. Scope

### In Scope

- Duplicated engine logic (StartRun/StartPlanningRun, event emission, run lifecycle)
- Legacy gateway step submit fallback removal
- Store row scanning helpers
- Data race in rebuild state
- Dead code removal

### Out of Scope

- Store interface splitting (larger refactor, separate initiative)
- GlobalMetrics singleton injection (requires per-workspace metrics design)
