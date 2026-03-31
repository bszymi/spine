---
id: TASK-001
type: Task
title: Implement .spine.yaml config file parser
status: Pending
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-003-configurable-artifacts-directory/epic.md
---

# TASK-001 — Implement .spine.yaml Config File Parser

---

## Purpose

Create the config file parser that reads `.spine.yaml` from the repository root and provides the `artifacts_dir` setting to all Spine components.

---

## Deliverable

New package `internal/config/` (or add to an existing package):

```go
type SpineConfig struct {
    ArtifactsDir string `yaml:"artifacts_dir"`
}

func Load(repoPath string) (*SpineConfig, error)
```

Behavior:
- Reads `.spine.yaml` from the repository root
- If file doesn't exist, returns defaults (`artifacts_dir: /`)
- If `artifacts_dir` is empty, defaults to `/`
- Normalizes the path (strip trailing slash, handle `/` vs `spine/` vs `./spine/`)
- Validates the directory exists (or will be created by init-repo)

Wire into `cmd/spine/main.go`:
- Load config at startup
- Pass `ArtifactsDir` to artifact service, projection service, workflow loader, CLI commands

---

## Acceptance Criteria

- `.spine.yaml` parsed correctly with `artifacts_dir` field
- Missing file defaults to `artifacts_dir: /`
- Config is available to all services that need path resolution
- Unit tests for parsing, defaults, and path normalization
