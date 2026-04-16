---
id: INIT-008
type: Initiative
title: Dogfooding Fixes
status: In Progress
owner: bszymi
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: related_to
    target: /governance/constitution.md
---

# INIT-008 — Dogfooding Fixes

---

## 1. Intent

Collect and fix bugs, usability issues, and improvements discovered while using Spine to build the Spine Management Platform.

This is a living initiative — new epics and tasks are added as issues are found during real usage. It serves as the single home for all dogfooding feedback rather than creating a new initiative per issue.

---

## 2. Scope

### In Scope

- Bugs found during real Spine usage
- Usability improvements for workflows, CLI, API
- Timeout and scheduler issues
- Default values that don't match real-world usage patterns
- Missing error messages or unclear behavior
- Any issue that blocks or degrades the developer experience

### Out of Scope

- New features (belong in dedicated initiatives)
- Architecture redesigns
- Performance optimization (unless it blocks usage)

---

## 3. Success Criteria

This initiative is successful when:

1. All critical bugs found during dogfooding are fixed
2. Spine is usable for real project governance without workarounds
3. Default configurations work for human-paced workflows

---

## 4. Work Breakdown

Epics are organized by component area. New epics and tasks are added as issues are discovered.

### Epics

| Epic | Title | Area |
|------|-------|------|
| EPIC-001 | Scheduler & Runtime | Timeout defaults, orphan detection, run lifecycle |
| EPIC-002 | Security Hardening | Credential redaction, auth fallback, HTTP timeouts, input validation |
| EPIC-003 | Code Quality | Legacy code removal, duplication reduction, data race fixes |
| EPIC-004 | Security Audit 2026-04 | CORS, SSE DoS, YAML bomb, supply chain, container hardening, workspace scope checks |

---

## 5. Links

- Constitution: `/governance/constitution.md`
- INIT-006: `/initiatives/INIT-006-governed-artifact-creation/initiative.md`
- INIT-007: `/initiatives/INIT-007-git-remote-sync/initiative.md`
