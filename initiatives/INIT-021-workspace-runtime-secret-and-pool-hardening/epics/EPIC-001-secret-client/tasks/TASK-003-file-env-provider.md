---
id: TASK-003
type: Task
title: File-mounted SecretClient provider for dev and test
status: Pending
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/tasks/TASK-001-secret-client-interface.md
---

# TASK-003 — File-mounted SecretClient provider for dev and test

---

## Purpose

Implement `SecretClient` against a directory of JSON files. This
is the dev and test path. It exists so prod-only behaviour gaps
cannot hide.

This is **not** an env-var provider. Env vars conflate config
with secrets and skip the reference indirection.

## Deliverable

`internal/secrets/file.go`. Layout under the configured root:

```
{root}/
  workspaces/
    {workspace_id}/
      runtime_db.json
      projection_db.json
      git.json
```

The provider must:

- Parse `SecretRef` to a path under the root.
- Read the JSON file, return its bytes as `SecretValue`.
- Treat the mount as read-only. Rotation/seeding is out of scope
  (platform-side); to update a dev secret, edit the file on
  disk — the next `Get` picks it up.
- `Invalidate` is a no-op (no remote cache).
- Surface `ErrSecretNotFound`, `ErrAccessDenied` (filesystem
  permission), `ErrSecretStoreDown` (root unreachable).

## Acceptance Criteria

- Passes the contract suite at `internal/secrets/contract/`
  (scaffolded in TASK-001) by calling
  `contract.RunContract(t, newFileClient)` against a temporary
  fixture directory.
- Used by `docker-compose` out of the box.
- Used by Spine integration tests.
- Documented inline and referenced from
  `smp:/architecture/secret-management.md` §8.
