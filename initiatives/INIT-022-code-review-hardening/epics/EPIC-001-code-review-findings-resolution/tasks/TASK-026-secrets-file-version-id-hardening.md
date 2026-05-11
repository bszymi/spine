---
id: TASK-026
type: Task
title: "Harden secrets/file.go VersionID against second-resolution mtime"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-11
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-026 — Harden secrets/file.go VersionID against second-resolution mtime

---

## Purpose

`internal/secrets/file.go:106` derives `VersionID` from
`ModTime().UnixNano()`. On filesystems with second-resolution mtime
(some NFS, FAT, older ext) two rotations within the same second
produce identical VersionIDs, so the gitpool cache (`pool.go:308`)
won't evict and the stale credential lingers.

This is a P3 hardening finding from the 2026-05-07 code review.

## Deliverable

- Mix file size and a content-hash prefix into the VersionID:
  `VersionID = hex(sha256(content))[:12] + "-" + size + "-" + mtimeUnix`
  (or any composition that includes content and is collision-resistant
  within a second).
- Ensure callers that compare VersionIDs as opaque strings are
  unaffected.

## Acceptance Criteria

- A new unit test writes the same secret file twice within the same
  second with different contents and asserts VersionIDs differ.
- Existing `secrets/file_test.go` cases continue to pass.

## Out of Scope

- Introducing a content hash for the AWS provider (it has its own
  version ID from the SDK).

## Resolution (2026-05-11)

Changed `internal/secrets/file.go::FileClient.Get` to compose the
returned `VersionID` from a SHA-256 content prefix, the file size,
and the file's `ModTime().UnixNano()`:

```
VersionID = hex(sha256(rawBytes))[:32] + "-" + size + "-" + mtimeUnixNano
```

Two rotations within the same wallclock second on a coarse-mtime
filesystem (NFS, FAT, older ext) now produce distinct VersionIDs
whenever the content differs. The composite is opaque to callers
(`secrets.VersionID` documents equality-only semantics in
`internal/secrets/client.go:137-140`); no caller inspects the
shape.

Added regression test
`internal/secrets/file_test.go::TestFileClient_VersionDiffersSameSecond`:
pins mtime to the same exact `time.Unix(1_700_000_000, 0)` via
`os.Chtimes` after each `WriteFile` (which otherwise resets mtime
to wallclock now), writes two distinct contents under that pinned
timestamp, and asserts the VersionIDs differ. The test additionally
re-reads the same file twice and asserts identical VersionIDs —
defends against accidentally non-deterministic hash composition
that would defeat any downstream cache keyed on the VersionID.

**Regression-bait verification** (manual, pre-submission):

| Mutation | Result |
| --- | --- |
| Revert `vid` to `fmt.Sprintf("%d", info.ModTime().UnixNano())` | FAIL — `VersionIDs collided across distinct contents with identical mtime`. |

**Codex review progression**

| Pass | Verdict | Action |
| --- | --- | --- |
| 1 | No findings. | — |
| 2 | P2: 48-bit prefix below the cheap defense-in-depth threshold; ~n²/2^49 birthday term is theoretical for our volume but the next-larger-margin option is free. | Bumped prefix from `sum[:6]` (48 bits / 12 hex chars) to `sum[:16]` (128 bits / 32 hex chars). |
| 3 | Clean: *"No remaining actionable correctness or robustness concerns found in the VersionID composition or the same-mtime regression test."* | — |

**Test gates**

- `go build ./...` — clean.
- `go vet ./internal/secrets/...` — clean.
- `gofmt -l internal/secrets/` — clean.
- `go test ./internal/secrets/... -count=1 -race` — green.
- `go test ./internal/secrets/... -run TestFileClient_VersionDiffersSameSecond -count=50 -race` — 50/50 green.
- `go test ./... -count=1 -race` — green.
- `go test -tags=scenario -count=1 ./internal/scenariotest/scenarios/...` — green.
- `make docker-lint` — 206 baseline unchanged.
