---
id: TASK-006
type: Task
title: Documentation Alignment
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
---

# TASK-006 — Documentation Alignment

## Purpose

Update architecture and governance documentation to reflect the current implementation state. Several areas have diverged where implementation moved ahead of docs.

## Deliverable

### Observability Documentation
- Document Prometheus metrics names and types (counters, gauges, histograms)
- Document audit log format and which operations produce audit entries
- Document trace ID propagation lifecycle through the execution loop

### Event Schema Documentation
- Update event-schemas.md with all 16 event types (10 domain + 6 operational)
- Document event payload structure for each type
- Document which code paths emit each event

### Permission Matrix
- Document the full RBAC permission matrix as implemented in auth/permissions.go
- Document the 5 roles and their capabilities

### Actor Selection
- Document the actor selection algorithm in actor/selection.go
- Document capability matching and exclusion logic

### API Documentation
- Document the /system/metrics endpoint
- Document the divergence branch creation and window close endpoints
- Update KNOWN-LIMITATIONS.md to reflect what has been fixed

## Acceptance Criteria

- All architecture docs are consistent with current implementation
- No undocumented public API endpoints
- KNOWN-LIMITATIONS.md reflects current state (items fixed should be removed)
- Event schema documentation covers all emitted events
- Observability documentation covers metrics, audit, and tracing
