---
id: TASK-026
type: Task
title: "Harden secrets/file.go VersionID against second-resolution mtime"
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
