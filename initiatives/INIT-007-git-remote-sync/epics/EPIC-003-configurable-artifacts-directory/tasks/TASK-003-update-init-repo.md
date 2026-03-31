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

### Branch and push behavior

`init-repo` must not commit directly to main. Instead:

1. Create a branch `spine/init` from the current HEAD
2. Commit `.spine.yaml` and all seed artifacts on that branch
3. Push the branch to origin (if remote exists and auto-push is enabled)
4. Print instructions:
   ```
   Spine workspace initialized on branch 'spine/init'.
   Create a pull request to merge it to main:
     gh pr create --base main --head spine/init
   ```

This ensures the team can review the governance setup before it lands on main. The `spine/init` commit is the one ungoverned operation — after it merges, all future work goes through planning runs.

If `--no-branch` flag is passed, commit directly to the current branch (for bootstrapping Spine's own repo or local-only usage).

### Normalization

`--artifacts-dir .` and `--artifacts-dir /` both normalize to `artifacts_dir: /` in `.spine.yaml`. The literal `.` must never be persisted — it would be treated as a directory component during path resolution. The init-repo flag accepts `.` for convenience but always writes the canonical `/` form.

### Examples

```bash
# Existing project — adds Spine in a subdirectory, on a branch
cd my-project/
spine init-repo . --artifacts-dir spine
# → creates branch spine/init
# → commits .spine.yaml + spine/ directory
# → pushes to origin
# → "Create a PR to merge spine/init to main"

# Same, with artifacts at repo root (e.g., Spine's own repo)
spine init-repo . --artifacts-dir / --no-branch
# → commits directly to current branch (no PR needed)

# New project from scratch
spine init-repo my-new-project
# → creates my-new-project/ with .spine.yaml (artifacts_dir: spine/)
# → initializes git, commits on spine/init branch
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
- Init commits on a `spine/init` branch by default (not main)
- Branch is pushed to origin if remote exists
- `--no-branch` flag commits directly to current branch
- Prints instructions for creating a PR
- Idempotent: running init-repo twice doesn't break anything
