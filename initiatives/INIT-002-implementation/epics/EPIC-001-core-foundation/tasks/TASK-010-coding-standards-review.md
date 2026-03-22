---
id: TASK-010
type: Task
title: Coding Standards Review and Guidelines Update
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
---

# TASK-010 — Coding Standards Review and Guidelines Update

---

## Purpose

Review all implemented Go code to identify patterns, conventions, and standards that have emerged during EPIC-001 through EPIC-004 development. Document these as coding guidelines so future tasks follow consistent patterns from the start, reducing review cycles and Codex findings.

## Scope

Analyze all existing code under `internal/` and `cmd/` to extract:

### 1. Patterns to Document

- **Error handling patterns** — when to use `domain.SpineError` vs `fmt.Errorf`, when to wrap vs return, error classification conventions
- **Testing patterns** — test file naming, test helper conventions, integration test tags, fixture creation, coverage expectations
- **Git operations** — when to use `GIT_LITERAL_PATHSPECS`, `--` separator, path validation via `safePath`
- **Context propagation** — which values to propagate (trace_id, run_id, actor_id, etc.), when to use `observe.Logger(ctx)`
- **Store/DB patterns** — query building, NULL handling with `nilIfEmpty`, transaction usage, migration conventions
- **Event emission** — fire-and-forget with error logging, event ID generation, payload structure
- **Validation patterns** — `ValidationResult` structure, error vs warning severity, composable validation
- **YAML/JSON serialization** — dual `json:` + `yaml:` tags on domain types, JSONB column handling

### 2. Anti-patterns Found During Reviews

- Silenced errors (fixed multiple times via Codex review)
- Path traversal risks (fixed with `safePath` + symlink resolution)
- Bare `git commit` capturing unrelated staged files (fixed with `gitCommitPath`)
- Idempotency key poisoning on failed operations (fixed in queue)
- Missing `--` separator in git commands (fixed with `GIT_LITERAL_PATHSPECS`)
- Sync state advancement on partial failures (fixed in projection service)

### 3. Deliverable

Update or create a Go coding guidelines document that covers:

- File and package naming conventions (already partially in Implementation Guide)
- Error handling standards
- Testing requirements (coverage threshold, integration test patterns)
- Git operation safety checklist
- Context and observability requirements
- Validation pattern templates
- Common review findings to avoid

This should be a practical reference that new code can follow to pass spine-validate-task on the first attempt.

## Acceptance Criteria

- All recurring patterns from existing code are identified and documented
- All recurring Codex review findings are documented as anti-patterns to avoid
- Guidelines are actionable (developer can follow them to write compliant code)
- Guidelines reference specific architecture documents where relevant
- Existing code is checked for consistency with the new guidelines
- Any inconsistencies in existing code are noted (fix deferred unless trivial)
