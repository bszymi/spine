---
id: INIT-020
type: Initiative
title: Dogfooding Fixes Round 2
status: In Progress
owner: bszymi
created: 2026-04-23
last_updated: 2026-04-23
links:
  - type: related_to
    target: /governance/constitution.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/initiative.md
---

# INIT-020 — Dogfooding Fixes Round 2

---

## 1. Intent

Collect and fix bugs, regressions, and usability issues discovered while using Spine to govern the Spine Management Platform repository. This is a continuation of `INIT-008 — Dogfooding Fixes` (Completed 2026-04-22), which found and fixed the first wave of issues during bootstrap and early use.

INIT-020 exists because closing INIT-008 reflected the state of its specific bug backlog, not "no further dogfooding issues will ever be found." As new issues surface during ongoing day-to-day use — particularly in areas that weren't exercised end-to-end before closure — they land here.

This is a living initiative: new epics and tasks are added as issues are found. It serves as the single home for round-2 dogfooding feedback rather than creating a new initiative per issue.

---

## 2. Scope

### In Scope

- Regressions in features marked Completed under INIT-008 but whose acceptance criteria were not exercised end-to-end before closure
- New bugs found during real Spine usage against the SMP workspace
- Usability improvements for workflows, CLI, API
- Timeout, scheduler, and runtime edge cases that surface only on sustained use
- Default values that don't match real-world usage patterns
- Unclear or missing error messages observed in production flows

### Out of Scope

- New features (belong in dedicated initiatives)
- Architecture redesigns
- Work that properly belongs in `INIT-019-interface-agnostic-core` (Draft)
- Performance optimization (unless it blocks usage)

---

## 3. Success Criteria

This initiative is successful when:

1. The round-2 bugs documented in its epics and tasks are all addressed.
2. Spine-governed SMP task runs complete end-to-end via the authoritative paths Spine already claims to support (e.g. engine-owned publish step merging automatically after review acceptance), without operators needing to fall back to manual git merges or status-flip commits.
3. Closure of a Completed task is grounded in a green scenario-test or observed production run, not on landing the PR alone.

---

## 4. Work Breakdown

Epics are organized by component area. New epics and tasks are added as issues are discovered.

### Epics

| Epic | Title | Area |
|------|-------|------|
| EPIC-001 | Scheduler & Runtime | Engine-owned step activation, internal handler dispatch, publish-step wedge |

---

## 5. Links

- Constitution: `/governance/constitution.md`
- Predecessor: `/initiatives/INIT-008-dogfooding-fixes/initiative.md`
