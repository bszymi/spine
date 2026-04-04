---
id: TASK-005
type: Task
title: "Fix data race in rebuild state goroutine"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-04
last_updated: 2026-04-04
completed: 2026-04-04
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-005 — Fix Data Race in Rebuild State Goroutine

---

## Purpose

`handleSystemRebuild` in `/internal/gateway/handlers_system.go` (lines 88-99) launches a goroutine that mutates `rebuildState` struct fields (`Status`, `CompletedAt`, `ErrorDetail`) without synchronization. `handleSystemRebuildStatus` reads these fields via `sync.Map.Load`. The `sync.Map` protects the map entry itself, but not the pointed-to struct fields — this is a data race.

Additionally, `rebuilds` is a package-level `sync.Map` global (line 27) rather than a field on `Server`, preventing test isolation.

---

## Deliverable

1. Add a `sync.Mutex` to `rebuildState` to protect field mutations
2. Lock before writing fields in the goroutine and before reading in the status handler
3. Move `rebuilds` from a package-level global to a field on `Server`

---

## Acceptance Criteria

- No data race on `rebuildState` fields (verifiable with `-race` flag)
- `rebuilds` is a field on `Server`, not a global
- Existing tests pass
