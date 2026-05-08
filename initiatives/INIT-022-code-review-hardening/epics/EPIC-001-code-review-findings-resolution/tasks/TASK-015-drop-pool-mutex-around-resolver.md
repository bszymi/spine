---
id: TASK-015
type: Task
title: "Drop pool mutex around resolver call"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-08
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

## Resolution (2026-05-08)

`ServicePool.Get` now releases `p.mu` before calling
`resolver.Resolve` and reacquires it for the cache work that follows.
The flow:

1. Brief lock to fail-fast on a closed pool (avoids needless network
   I/O when Get races shutdown).
2. Unlock; call `resolver.Resolve(ctx, workspaceID)` with no pool
   mutex held.
3. Re-lock and re-check `p.closed` (covers Close-races-with-Resolve).
4. Continue with the existing cache lookup loop, unchanged.

Two concurrent Gets for the same workspace ID may both call
`Resolve` — they converge on the same `canonicalID` afterward, where
the existing entry-level singleflight (`entry.ready` channel) still
deduplicates the actual `initializeEntry` work. The redundant
Resolve cost is bounded to one extra call per simultaneous miss,
which the AC explicitly accepts as an alternative to a second
singleflight layer; the gitpool clone-coalescer is a different
concern (clones racing on the filesystem) and not warranted here.

### Invalidation-race regressions caught and fixed (codex passes 1-3)

Dropping the pool mutex around Resolve created an invalidation-race
regression: an `Evict(workspaceID)` (called by the binding-
invalidation webhook via `CombinedBindingInvalidator`) that fires
while a `Get`'s unlocked Resolve is in flight could be silently
lost, leaving Get to cache pre-invalidation cfg. Pre-TASK-015, the
pool mutex held during Resolve forced Evict to wait until Get had
inserted the placeholder, implicitly serializing the race away.

Three codex passes converged on the final design:

- **Pass 1** flagged the cold-miss case. First attempt: a
  `pendingEvict` set marking workspace IDs whose Evict found no
  live entry, consumed by the next Get's cache-insert.
- **Pass 2** rejected pendingEvict for two reasons: (a) under an
  overlapping multi-Get race the marker was consumed by whichever
  Get won the insert, so an older pre-invalidation Get could
  later insert stale cfg without seeing any marker; (b) Evicts
  for unknown / cold / mistyped workspace IDs (misrouted webhooks)
  left a permanent map entry that grew for the pool's lifetime and
  would mark a future workspace of the same ID as evicting on its
  first Get.
- **Pass 3** flagged that the gen+retry replacement only bumped
  `evictGen` on the cold-miss path (no entry); a hot-cache Evict
  racing an in-flight Resolve would still take the entry-exists
  branch and skip the bump, letting that Get cache stale cfg
  after the existing entry was torn down.

Final design (codex pass 4 clean): a per-workspace **eviction
generation** + bounded **retry**, gated on an in-flight resolve
counter, with the gen bump applied on every Evict path that runs
while a Resolve is in flight.

- `ServicePool.activeResolves map[string]int` counts in-flight
  `resolver.Resolve` calls per workspaceID. Get increments at
  attempt entry, decrements when Resolve returns.
- `ServicePool.evictGen map[string]uint64` is bumped by
  `Evict(workspaceID)` whenever `activeResolves[workspaceID] >
  0` — both for the cold-miss case (no entry yet) and the
  hot-cache case (entry exists; an in-flight Get's Resolve must
  still observe the invalidation). When no Resolve is in flight,
  no stale state could be in the resolver pipeline and the Evict
  is a clean no-op (matching the pre-TASK-015 contract for
  unknown / cold workspaces).
- Both maps are deleted for a workspaceID as soon as the last
  resolver drains, so deployments receiving invalidations for
  unknown workspaces don't grow either map.
- `Get` snapshots `evictGen[workspaceID]` before Resolve. After
  Resolve returns and `p.mu` is reacquired, Get rechecks; on
  mismatch the entire Get retries (bounded to `maxGetAttempts =
  3`). The retry's fresh Resolve sees the post-invalidation
  Provider cache and fetches current platform state.
- The overlapping multi-Get race is correct under this design:
  whichever Resolve completes first inserts a fresh post-bump
  entry; the older pre-bump Get observes the gen mismatch on its
  recheck and retries — no path lets a stale cfg slip into the
  cache.
- After `maxGetAttempts` consecutive races (a hot workspace seeing
  >3 invalidations during a single Resolve) the call returns a
  descriptive error rather than livelocking.

`Release`, `EvictIdle`, and `Close` are otherwise unaffected.

Files:

- `internal/workspace/pool.go` — added `activeResolves` and
  `evictGen` fields with initialization in `NewServicePool`;
  replaced `Get`'s flat flow with a bounded retry loop that
  snapshots gen, drains the in-flight counter, and retries on
  mismatch; updated `Evict` to bump the gen on every path while
  a Resolve is in flight (cold miss + hot cache); updated the
  doc comments with the full invalidation-race contract.
- `internal/workspace/pool_test.go` — extended `slowResolver` to
  count per-ID Resolve calls and only block the FIRST call so
  retries can complete; added an `evictTriggerResolver` that
  invokes `pool.Evict` from inside its Nth Resolve to drive the
  hot-cache race deterministically; added four regression tests:
  `TestServicePool_Get_SlowResolveDoesNotBlockOtherWorkspaces`
  (the original isolation contract);
  `TestServicePool_Evict_DuringColdResolve_RetriesResolve`
  (Evict during cold Resolve forces Get to retry);
  `TestServicePool_Evict_DuringResolve_HotCache_RetriesResolve`
  (Evict racing an in-flight Resolve when an entry already
  exists also forces Get to retry — codex pass 3 regression);
  `TestServicePool_Evict_NoActiveResolve_NoMapGrowth`
  (100 Evicts on an unknown ID followed by Get on a different
  known ID and on the previously-Evicted ID — both must succeed
  with no phantom evicting state).

Test gates:

- `go test -race -count=20` on the four new tests:
  green (20 consecutive runs, race clean).
- `go test -race ./internal/workspace/...`: green.
- Full unit suite (with `-skip TestFileClient_VersionChangesOnEdit`,
  the TASK-026 flake): green.
- `make docker-lint`: 206 issues — same baseline as
  TASK-011/012/013/014. Zero new findings in `internal/workspace/`.
- `codex review --uncommitted`: clean after pass 4 (passes 1-3
  flagged regressions caught above).
