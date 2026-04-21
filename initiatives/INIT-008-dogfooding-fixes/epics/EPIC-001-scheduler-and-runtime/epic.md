---
id: EPIC-001
type: Epic
title: Scheduler & Runtime
status: Completed
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
owner: bszymi
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/initiative.md
---

# EPIC-001 — Scheduler & Runtime

---

## 1. Purpose

Fix scheduler and runtime issues found during real Spine usage. The first issue: the orphan detection threshold is destructively short, killing planning runs while humans are still reviewing.

---

## 2. Scope

### In Scope

- Scheduler timeout defaults
- Orphan detection behavior
- Run lifecycle edge cases
- Environment variable configuration for timeouts

### Out of Scope

- Scheduler architecture changes
- New scheduler capabilities
