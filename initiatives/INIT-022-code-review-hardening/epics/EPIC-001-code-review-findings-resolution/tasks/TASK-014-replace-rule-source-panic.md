---
id: TASK-014
type: Task
title: "Replace branchprotect rule_source panic with returned error"
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

# TASK-014 — Replace branchprotect rule_source panic with returned error

---

## Purpose

`internal/branchprotect/projection/rule_source.go:43` panics on a nil
constructor argument. The panic is reachable from production wiring:
a missed nil-check in `cmd/spine` becomes a process kill at startup
or later under platform-binding load.

This is a P2 error-handling finding from the 2026-05-07 code review.

## Deliverable

- Change the constructor (or guarded helper) to return an error rather
  than panic on the nil-arg branch.
- Update the one or two callers in `cmd/spine` and any wiring helper
  to surface the error (typically via `cobra` command-init failure or
  `SPINE_ENV=production` strict-startup).
- Confirm there is no remaining production-reachable panic in the
  package via `git grep "panic(" internal/branchprotect/`.

## Acceptance Criteria

- A unit test passes a nil arg to the constructor and asserts the
  returned error is non-nil and matches the expected sentinel
  (`ErrInvalidParams` is a fine choice).
- `go build ./...` passes; existing wiring continues to work.

## Out of Scope

- Other panics in the codebase. The CIDR-init panic in
  `internal/delivery/targeturl.go:212` is package-init only and
  defensible.

## Resolution (2026-05-08)

Changed `bpprojection.New(r ListReader)` from `*RuleSource` to
`(*RuleSource, error)`. The nil-arg branch now returns
`domain.NewError(domain.ErrInvalidParams, "branchprotect/projection:
nil ListReader")` — `errors.As` against `*domain.SpineError` with
`Code == domain.ErrInvalidParams` is the canonical Spine match shape.

The error is threaded through the three production call sites so a
missed nil-check surfaces as a startup or per-workspace builder
failure rather than a process kill:

- `cmd/spine/cmd_serve.go::buildBranchProtectPolicy` now returns
  `(branchprotect.Policy, error)`; its callers in
  `workspaceOrchestratorBuilder`, `buildServerConfig`,
  `buildGitPushResolver`'s closure, and `buildOrchestrator` (which
  follows the existing log-and-return-nil pattern alongside
  `engine.New`) bubble the error.
- `cmd/spine/cmd_serve.go::buildArtifactService` now returns
  `(*artifact.Service, error)`; the single caller in
  `buildServerConfig` propagates.
- `internal/workspace/pool.go::buildServiceSet` (already
  error-returning) wraps both `bpprojection.New` calls (artifact and
  divergence policies) with `fmt.Errorf("...: %w", err)`.

Audit: `git grep "panic(" internal/branchprotect/` returns zero
matches.

Files:

- `internal/branchprotect/projection/rule_source.go` — signature
  change; imports `internal/domain`.
- `internal/branchprotect/projection/rule_source_test.go` — replaced
  `TestNew_NilReaderPanics` with `TestNew_NilReaderReturnsError`,
  which asserts `errors.As(err, &spineErr)` and
  `spineErr.Code == domain.ErrInvalidParams`. Threaded the new
  signature through every other test.
- `cmd/spine/cmd_serve.go` — signature changes for
  `buildBranchProtectPolicy` and `buildArtifactService`; bubbled
  errors at all five call sites.
- `internal/workspace/pool.go` — wrapped both `bpprojection.New`
  calls in `buildServiceSet`.

Test gates:

- `go test ./internal/branchprotect/... ./cmd/spine/...
  ./internal/workspace/...`: green.
- Full unit suite (with `-skip TestFileClient_VersionChangesOnEdit`,
  the TASK-026 flake): green.
- `make docker-lint`: 206 issues — same baseline as TASK-011/012/013.
- `codex review --uncommitted`: two consecutive clean passes.
