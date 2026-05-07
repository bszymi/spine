---
id: TASK-015
type: Task
title: "Drop pool mutex around resolver call"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-015 — Drop pool mutex around resolver call

---

## Purpose

`internal/workspace/pool.go:301`'s `Get()` calls
`p.resolver.Resolve(ctx, workspaceID)` while holding the pool mutex.
In platform-binding mode `Resolve` performs network I/O
(`internal/workspace/platform_binding_provider.go:280`), so a slow
upstream stalls **every** workspace's `Get`/`Release`/`Close`,
defeating the per-workspace isolation the comment at `:285`
promises.

This is a P2 concurrency finding from the 2026-05-07 code review.

## Deliverable

- Restructure `Get()` so the mutex is released around the
  `resolver.Resolve` call. The cache check on entry stays under the
  mutex; the resolver call runs unlocked; the cache mutation on
  return reacquires the mutex.
- Handle the race where two callers miss the cache simultaneously and
  both call `Resolve` — either let both go and have the cache
  insertion deduplicate (last-write-wins), or use the same
  inflight-singleflight pattern that gitpool uses (`pool.go:590`+).
- Confirm `Release` and `Close` are unaffected.

## Acceptance Criteria

- A new unit test in `internal/workspace/pool_test.go` proves the
  isolation: one slow `Resolve` for workspace A does not block a
  fast `Get` for workspace B. Use a fake resolver with a controllable
  delay.
- Existing pool tests continue to pass.
- Race detector clean (`go test -race ./internal/workspace/...`).

## Out of Scope

- Replacing the workspace pool with a different implementation.
- Plumbing per-workspace timeouts through `Resolve` — separate concern.
