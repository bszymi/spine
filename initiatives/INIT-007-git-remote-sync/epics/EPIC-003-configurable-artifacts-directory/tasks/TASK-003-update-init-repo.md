---
id: TASK-003
type: Task
title: Update init-repo to create .spine.yaml and use artifacts_dir
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
  - type: blocked_by
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/tasks/TASK-001-spine-yaml-config.md
---

# TASK-003 — Update init-repo to Create .spine.yaml

---

## Purpose

Update `spine init-repo` to create `.spine.yaml` at the repo root and place all seed artifacts in the configured directory.

---

## Deliverable

`internal/cli/initrepo.go`

Changes:
- Accept `--artifacts-dir` flag (default: `spine/`)
- Create `.spine.yaml` at repo root with the configured `artifacts_dir`
- Create all seed directories (`governance/`, `initiatives/`, etc.) inside the artifacts directory
- Seed files (charter, constitution, templates) written inside the artifacts directory
- If `.spine.yaml` already exists, read it and use the existing `artifacts_dir`

Normalization: `--artifacts-dir .` and `--artifacts-dir /` both normalize to `artifacts_dir: /` in `.spine.yaml`. The literal `.` must never be persisted — it would be treated as a directory component during path resolution. The init-repo flag accepts `.` for convenience but always writes the canonical `/` form.

Example:
```bash
spine init-repo my-project                          # creates my-project/spine/ with .spine.yaml (artifacts_dir: spine/)
spine init-repo my-project --artifacts-dir .         # artifacts at root, writes artifacts_dir: /
spine init-repo my-project --artifacts-dir /         # same as above
spine init-repo my-project --artifacts-dir .spine    # creates my-project/.spine/ (hidden)
```

For Spine's own repo, the `.spine.yaml` would be:
```yaml
artifacts_dir: /
```

---

## Acceptance Criteria

- `spine init-repo` creates `.spine.yaml`
- Default `artifacts_dir` is `spine/`
- `--artifacts-dir` flag overrides the default
- Seed artifacts are placed in the correct directory
- Existing `.spine.yaml` is respected (no overwrite)
- Idempotent: running init-repo twice doesn't break anything
