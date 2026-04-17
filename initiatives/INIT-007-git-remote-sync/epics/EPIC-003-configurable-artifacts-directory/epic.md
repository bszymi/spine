---
id: EPIC-003
type: Epic
title: Configurable Artifacts Directory
status: Completed
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
owner: bszymi
created: 2026-03-31
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/initiative.md
---

# EPIC-003 — Configurable Artifacts Directory

---

## 1. Purpose

Allow Spine to coexist with existing projects by placing all Spine artifacts in a configurable subdirectory rather than requiring the repo root.

An existing project may already have `governance/`, `architecture/`, or `docs/` directories. Spine must not collide with them. A `.spine.yaml` config file at the repo root defines where Spine artifacts live.

---

## 2. Design

### .spine.yaml

A configuration file at the repository root:

```yaml
# .spine.yaml
artifacts_dir: spine/    # where Spine artifacts live (default: spine/)
```

Result for an existing project:
```
my-project/
├── src/                     # existing code
├── .spine.yaml              # Spine config (artifacts_dir: spine/)
└── spine/                   # all Spine artifacts
    ├── governance/
    ├── initiatives/
    ├── architecture/
    ├── product/
    ├── workflows/
    └── templates/
```

Result for Spine's own repo:
```yaml
# .spine.yaml
artifacts_dir: /             # artifacts at repo root (Spine core)
```

### Path resolution

All artifact paths inside Spine artifacts (links, front matter references like `initiative:`, `epic:`) remain **relative to the Spine artifacts directory**, not the repo root. Example:

- Artifact link: `target: /governance/charter.md`
- With `artifacts_dir: spine/`, resolves to: `<repo>/spine/governance/charter.md`
- With `artifacts_dir: /`, resolves to: `<repo>/governance/charter.md`

This keeps artifact content portable — moving the artifacts directory doesn't require rewriting every link.

---

## 3. Scope

### In Scope

- `.spine.yaml` config file format and parser
- `artifacts_dir` setting with default `spine/`
- All path resolution (artifact service, projection, workflow loader, CLI) uses configured root
- `spine init-repo` creates `.spine.yaml` and places artifacts in the configured directory
- Link resolution prefixes the artifacts directory
- Update governance docs to clarify paths are relative to Spine root, not repo root
- Backward compatibility: absent `.spine.yaml` or `artifacts_dir: /` behaves as today

### Out of Scope

- Other `.spine.yaml` settings (future — this epic only defines the file format and `artifacts_dir`)
- Migration tool for moving artifacts from root to subdirectory
- Multiple Spine workspaces in one repo

---

## 4. Success Criteria

1. `spine init-repo` creates `.spine.yaml` with `artifacts_dir: spine/`
2. All artifact operations (create, read, update, validate) respect the configured directory
3. Projection sync discovers artifacts in the configured directory
4. Workflow loader reads workflows from the configured directory
5. Spine's own repo works with `artifacts_dir: /` (no change to existing behavior)
6. An existing project can adopt Spine without path collisions
7. All existing tests pass with `artifacts_dir: /` (backward compatible)

---

## 5. Key Files

- New: `.spine.yaml` parser (new package or in `internal/config/`)
- `internal/artifact/service.go` — path resolution
- `internal/projection/` — file discovery
- `internal/workflow/` — workflow file discovery
- `internal/cli/initrepo.go` — init-repo creates `.spine.yaml`
- `cmd/spine/main.go` — load config at startup
- `governance/repository-structure.md` — document artifacts directory
- `governance/guidelines.md` — clarify linking conventions
- `governance/naming-conventions.md` — clarify path references

---

## 6. Related Artifacts

- `/governance/repository-structure.md`
- `/governance/naming-conventions.md`
- `/governance/guidelines.md`
- `/architecture/git-integration.md`
