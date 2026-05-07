---
id: TASK-001
type: Task
title: "Wire gitpool WithRepoBase and validate LocalPath containment"
status: Completed
acceptance: Approved
acceptance_rationale: |
  Plumbed SPINE_CODE_REPO_BASE end-to-end through cmd/spine ->
  workspace.PoolConfig -> gitpool.WithRepoBase. cmd/spine/cmd_serve.go
  loadCodeRepoBase requires the env var when SPINE_ENV=production and
  refuses relative values (codex pass 1: TOCTOU on launch CWD).
  repository.Manager.NewManager takes codeRepoBase and validateLocalPath
  rejects empty, relative (codex pass 2: persisted-string stability),
  out-of-base, and equal-to-base inputs with ErrInvalidParams in both
  Register and Update.

  Per-workspace narrowing: workspace.PerWorkspaceCodeRepoBase joins the
  deployment base with the workspace ID so each pool enforces
  <base>/<workspace_id> as its boundary (codex pass 3: shared-mode
  isolation). Helper hardened against: workspace-dir symlink to
  /etc (pass 4), workspace-dir symlink to a sibling tree (pass 5),
  TOCTOU on missing workspace dirs by mkdir-then-EvalSymlinks check
  (pass 6), traversal-shaped workspace IDs via ValidateID (pass 7),
  regular-file masquerading as workspace base (pass 8), and DB-pool
  leak on derivation failure by hoisting the check above the DB open
  (pass 9). Top-level pool in cmd_serve narrowing is gated to
  single-workspace mode (pass 8) so shared-mode deployments don't
  create an unused <base>/default. Codex pass 10 clean.

  17 new tests across cmd/spine, internal/repository, internal/workspace
  cover the loader (production-required, dev-allows-empty,
  rejects-relative, accepts-and-cleans-absolute), Manager containment
  (6 escape patterns + 3 happy + Update rejection + relative-rejection
  + empty-base-skip), and per-workspace narrowing (empty-disables +
  narrows-distinct + symlink-escape-3-cases + traversal-id +
  regular-file + creates-missing + missing-base). Operator runbook §2.4
  + failure modes table + architecture/multi-repository-integration.md
  §2.5 updated with the four-layer enforcement chain and the
  per-workspace contract. `go test ./...` and `go vet ./...` pass.
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-001 — Wire gitpool WithRepoBase and validate LocalPath containment

---

## Purpose

`RegisterRequest.LocalPath` and `UpdateRequest.LocalPath` at
`internal/repository/manager.go:123-148, 245-248` are accepted from any
actor with `repository.create`/`repository.update` (Operator role) with
**no path validation whatsoever**. Production wiring at
`cmd/spine/cmd_serve.go:692-698` and
`internal/workspace/pool.go:664-670` constructs the gitpool **without
`gitpool.WithRepoBase`**, so `Pool.validateRepoBase` short-circuits to
nil. Net effect: an Operator POSTing
`{"local_path":"/etc"}` lands `GIT_PROJECT_ROOT=/etc` on the next
git-http-backend serve, exposing arbitrary host directories.

This is a P1 security finding from the 2026-05-07 code review.

## Deliverable

**Note on plumbing:** `gitpool.WithRepoBase` exists, but neither
`serveDeps` nor `workspace.PoolConfig` currently carries a code-repo
base path. The primary `RepoPath` (`internal/workspace/pool.go:602`)
is the *primary Spine repo* directory and is NOT the right base for
code repos — every code-repo binding's `local_path` lives at a
sibling location chosen by the operator. This task therefore has a
plumbing prerequisite before any validation can hang off it.

Concrete steps:

1. **Introduce a `CodeRepoBase` (or similarly-named) field**:
   - Add `CodeRepoBase string` to `internal/workspace/PoolConfig`
     (next to the existing `SecretClient`, `DBPolicy` fields).
   - Add the equivalent on whatever `serveDeps`-shaped struct
     `cmd/spine/cmd_serve.go` uses, sourced from a new env var
     `SPINE_CODE_REPO_BASE` (default to a sibling directory next to
     the primary repo, or fail closed in production if unset — pick
     whichever matches the existing `SPINE_ENV=production` strict
     startup philosophy).
   - Document the field's contract in the surrounding Go doc
     comments and in `architecture/multi-repository-integration.md`.
2. **Wire `gitpool.WithRepoBase(cfg.CodeRepoBase)`** into both pool
   constructor paths:
   - The shared-mode pool constructed near
     `cmd/spine/cmd_serve.go:692-698`.
   - The per-workspace pool builder in `internal/workspace/pool.go`
     (`:664-670`), sourced from the new `PoolConfig.CodeRepoBase`.
3. **Add `validateLocalPath` in `repository.Manager.Register` and
   `Manager.Update`** (`internal/repository/manager.go`):
   - Plumb `CodeRepoBase` into `Manager` via a new constructor
     parameter (alongside the existing `Catalog`/`ManagerStore`
     args).
   - On Register/Update: `filepath.Clean` the input, then confirm
     the cleaned path resolves under `CodeRepoBase`. Reject empty
     paths and out-of-base paths with `domain.ErrInvalidParams` so
     the HTTP 400 mapping stays stable.
4. **Cross-link**: update the operator runbook
   (`docs/operator-runbook.md` §2 Registering) to mention the new
   `SPINE_CODE_REPO_BASE` requirement.

## Acceptance Criteria

- `cmd/spine` exposes `SPINE_CODE_REPO_BASE`; behavior is documented
  for both production (must be set) and non-production (default
  applies).
- An Operator request with `local_path` outside `CodeRepoBase`
  returns 400 `invalid_params` with a stable error message; the
  binding row is **not** created or updated.
- An Operator request with a `local_path` that resolves under
  `CodeRepoBase` succeeds end-to-end.
- A unit test in `internal/repository/manager_test.go` covers:
  containment success, containment failure (`/etc`, `..` traversal,
  symlink-out where feasible), empty path.
- A unit test in `internal/gitpool/pool_test.go` confirms
  `validateRepoBase` rejects an out-of-base path even when no
  validation occurred upstream — the pool is the second line of
  defense.
- A scenario test (or the existing `repository_lifecycle_test.go`)
  exercises an end-to-end register+inspect with a valid local_path
  under the workspace's `CodeRepoBase`.

## Out of Scope

- Adding new routes; the gate stays at the existing register/update
  endpoints.
- Hardening symlink containment beyond `filepath.EvalSymlinks` — TOCTOU
  on workspace-base symlinks is the same surface as on every other
  filesystem write and is out of band of this task.

## Notes

Stacks beneath TASK-002 — TASK-001 lands first, TASK-002 layers on
top. They share the registration-intake surface but ship as separate
PRs per the initiative's "No combined PRs" constraint.
