---
id: INIT-007
type: Initiative
title: Git Remote Sync, Branch Usability, and Workspace Portability
status: Pending
owner: bszymi
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: related_to
    target: /governance/constitution.md
  - type: follow_up_from
    target: /initiatives/INIT-006-governed-artifact-creation/initiative.md
---

# INIT-007 — Git Remote Sync, Branch Usability, and Workspace Portability

---

## 1. Intent

Make Spine a full participant in remote Git workflows by automatically pushing all local Git changes to origin, and improve branch usability by using human-readable names that identify the artifact being worked on.

Currently Spine operates on a local Git repository without pushing to a remote. This means:
- Collaborators cannot see branches or changes until manually pushed
- Planning run branches are invisible to reviewers
- Actors cannot pull a branch to start working on it
- The system depends on a single local copy

Additionally, planning run branches use opaque names like `spine/run/run-0a5d0f6d` which provide no context about what artifact is being created.

---

## 2. Scope

### In Scope

- Auto-push to origin after every Spine Git operation (commit, branch create, merge)
- Generic implementation — works with any Git hosting (GitHub, Bitbucket, GitLab, self-hosted)
- Workspace setup assumes the user has already cloned a repo (origin is configured)
- Human-readable branch naming: `spine/plan/INIT-001-slug` instead of `spine/run/run-XXXXXXXX`
- Standard run branches also get readable names: `spine/run/TASK-003-slug`
- Branch name format configurable or convention-based
- Error handling for push failures (network, auth, conflicts)
- Update existing tests and scenario tests
- `.spine.yaml` config file at repo root for workspace configuration
- Configurable artifacts directory (`artifacts_dir`, default: `spine/`)
- Path resolution relative to Spine artifacts directory (not repo root)
- Backward compatibility: `artifacts_dir: /` behaves as current Spine (repo root)
- Update governance and architecture docs for path conventions

### Out of Scope

- Git hosting provisioning (creating repos on GitHub/Bitbucket)
- SSH key or token management for Git auth
- Webhook-based sync (pull from remote)
- Multi-remote support
- Other `.spine.yaml` settings beyond `artifacts_dir` (future work)
- Migration tool for moving existing artifacts to a subdirectory

---

## 3. Success Criteria

1. Every Spine commit is automatically pushed to origin
2. Every branch creation is pushed to origin
3. Branch merges and deletions are reflected on origin
4. Planning run branches use artifact ID and slug: `spine/plan/INIT-001-build-spine-management-platform`
5. Standard run branches use artifact ID and slug: `spine/run/TASK-003-implement-start-planning-run`
6. Push failures are surfaced clearly, not silently ignored
7. Works with any Git remote that supports push (no provider-specific code)
8. `.spine.yaml` config file defines `artifacts_dir` for each workspace
9. Existing projects can adopt Spine without path collisions
10. Spine's own repo works with `artifacts_dir: /` (backward compatible)
11. All existing tests continue to pass

---

## 4. Constraints

- Must not assume any specific Git hosting provider
- Must not store or manage Git credentials — relies on the system Git credential helper
- Must handle push failures gracefully (transient network errors should not fail the run)
- Branch names must be valid Git ref names (no spaces, special chars)

---

## 5. Work Breakdown

### Epics

| Epic | Title | Purpose |
|------|-------|---------|
| EPIC-001 | Auto-Push to Remote | Push all Git operations to origin automatically |
| EPIC-002 | Human-Readable Branch Names | Use artifact ID and slug in branch names |
| EPIC-003 | Configurable Artifacts Directory | `.spine.yaml` with `artifacts_dir` for existing project adoption |

---

## 6. Risks

- **Auth failures** — Git push requires credentials; if not configured, every operation fails. Mitigated by clear error messages and documentation.
- **Network latency** — Push after every commit adds latency. Mitigated by async push option if needed.
- **Conflict on push** — Remote may have diverged. Mitigated by Spine being the only writer to its branches.

---

## 7. Exit Criteria

INIT-007 may be marked complete when:

- All three epics are complete
- Spine pushes all changes to origin automatically
- Branch names are human-readable
- Scenario tests validate remote push behavior

---

## 8. Links

- Constitution: `/governance/constitution.md`
- INIT-006 (predecessor): `/initiatives/INIT-006-governed-artifact-creation/initiative.md`
- Git Integration: `/architecture/git-integration.md`
