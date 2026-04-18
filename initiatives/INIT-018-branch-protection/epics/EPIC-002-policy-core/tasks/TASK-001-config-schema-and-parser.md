---
id: TASK-001
type: Task
title: "Define branch-protection config schema and parser"
status: Pending
work_type: implementation
created: 2026-04-18
epic: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
initiative: /initiatives/INIT-018-branch-protection/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
---

# TASK-001 — Define branch-protection config schema and parser

---

## Purpose

Establish the on-disk format for `/.spine/branch-protection.yaml` and the Go parser that turns it into typed rules the rest of the system consumes. Everything downstream (projection, policy evaluation, `init-repo` seed) depends on a single authoritative schema.

This task is schema + parser only. No policy logic, no projection, no wiring into call sites.

---

## Context

[ADR-009 §1](/architecture/adr/ADR-009-branch-protection.md) specifies the file shape:

```yaml
version: 1
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: "release/*"
    protections: [no-delete, no-direct-write]
```

Follows the workflow-definition precedent: YAML, no front matter, discovered by the Projection Service.

---

## Deliverable

1. **Format specification.** Short doc at `/architecture/branch-protection-config-format.md` describing the schema (fields, allowed values, glob semantics, examples, error cases). Mirror the structure of `/architecture/workflow-definition-format.md`.

2. **Parser package.** `internal/branchprotect/config` (or equivalent) with:
   - `type RuleKind string` — constants `KindNoDelete`, `KindNoDirectWrite`. Any other value is a parse error in v1.
   - `type Rule struct { Branch string; Protections []RuleKind }` — `Branch` may be a literal name or a glob (`release/*`). Regex and negative patterns are rejected per ADR-009 §6.
   - `type Config struct { Version int; Rules []Rule }`.
   - `func Parse(r io.Reader) (*Config, error)` — strict parse, fails on unknown keys, duplicate branch entries, unknown `protections` values.
   - `func (c *Config) MatchRules(branch string) []Rule` — returns every rule whose branch pattern matches (literal or glob); callers decide how to combine them.

3. **Unit tests.** Table-driven, covering: happy path, glob matching, unknown `protections` value, unknown top-level key, duplicate branch entries, empty file, missing `version`.

---

## Acceptance Criteria

- `/architecture/branch-protection-config-format.md` exists and is reachable from ADR-009.
- `internal/branchprotect/config.Parse` rejects every malformed case enumerated above with an error that identifies the offending field/line.
- Glob semantics match filepath.Match (the existing Go convention); this is stated explicitly in the format doc.
- Unit tests pass; no consumer of the package is wired yet (that lands in TASK-002 and TASK-003).
- No runtime-table work, no `init-repo` seed — just schema, parser, and docs.
