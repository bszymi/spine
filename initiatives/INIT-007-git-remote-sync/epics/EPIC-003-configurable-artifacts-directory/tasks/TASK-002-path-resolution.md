---
id: TASK-002
type: Task
title: Update path resolution across all services
status: Completed
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

# TASK-002 — Update Path Resolution Across All Services

---

## Purpose

Make all Spine services resolve artifact paths relative to the configured `artifacts_dir` instead of the repo root.

This is the core change — every place Spine reads or writes artifact files must prefix with `artifacts_dir`.

---

## Deliverable

Add a path resolver helper:

```go
func (c *SpineConfig) ResolvePath(artifactPath string) string {
    // /governance/charter.md → <repo>/spine/governance/charter.md
    // when artifacts_dir = "spine/"
}
```

Update these services to use the resolver:

### Artifact Service (`internal/artifact/service.go`)
- `Create()` — file write path
- `Read()` — file read path
- `Update()` — file path
- Git commit pathspecs

### Projection Service (`internal/projection/`)
- File discovery (listing `.md` files for sync)
- Workflow file discovery (listing `.yaml` files)

### Workflow Loader (`internal/workflow/`)
- Workflow file paths

### CLI Commands (`internal/cli/`)
- All commands that accept artifact paths
- Path display in output

### Validation (`internal/validation/`)
- Link target resolution (link targets are relative to Spine root, must resolve to repo path for file existence checks)

---

## Acceptance Criteria

- `artifacts_dir: spine/` — all operations work with artifacts in `<repo>/spine/`
- `artifacts_dir: /` — all operations work at repo root (backward compatible)
- Artifact paths in front matter and links remain Spine-root-relative (no rewriting needed when changing directory)
- All existing tests pass with `artifacts_dir: /`
- New tests verify `artifacts_dir: spine/` works end-to-end
