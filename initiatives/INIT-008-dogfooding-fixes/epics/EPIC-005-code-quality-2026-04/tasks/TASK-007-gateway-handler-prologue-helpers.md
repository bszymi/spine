---
id: TASK-007
type: Task
title: "Extract gateway handler prologue helpers (needStore, needArtifacts, decodeAuthedJSON)"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-007 — Gateway Handler Prologue Helpers

---

## Purpose

Every gateway handler opens with the same 4-5 line prologue: authorize, check that the relevant service is wired, decode JSON. Grep counts across `internal/gateway/handlers_*.go`: `authorize` 72, `decodeJSON` 24, `storeFrom(...)==nil` 16, `ErrUnavailable` 77. The "not configured" message has 10+ variants ("store", "auth", "artifact service", "projection service", "planning run starter", "git reader", "result handler", "event delivery"). Handlers are visibly noisier than they need to be and inconsistency in messages slows operator debugging.

---

## Deliverable

1. Add to `internal/gateway/server.go` or a new `gateway/prologue.go`:
   - `func (s *Server) needStore(w http.ResponseWriter, r *http.Request) (store.Store, bool)`
   - `func (s *Server) needArtifacts(...)`
   - `func (s *Server) needWorkflows(...)`
   - `func (s *Server) needAuth(...)`
   - `func decodeAuthedJSON[T any](s *Server, w http.ResponseWriter, r *http.Request, op string) (T, bool)` — combines `authorize` + `decodeJSON` into one call, writing the response and returning `(zero, false)` on failure.
2. Migrate handlers incrementally; each helper returns `(svc, true)` or writes the 503/400 and returns `(nil, false)`.
3. Normalise the "not configured" message to `"<service> not configured"` with a stable list of service names.

---

## Acceptance Criteria

- Every handler in `handlers_*.go` uses the helpers; no handler contains a raw `storeFrom(...)==nil` check or manual `decodeJSON` + error write.
- "Not configured" messages follow the new canonical form.
- Gateway tests pass unchanged.
