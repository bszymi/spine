---
type: Architecture
title: Branch Protection Config Format
status: Living Document
version: "0.1"
---

# Branch Protection Config Format

---

## 1. Purpose

This document defines the on-disk format for Spine's branch-protection ruleset.

The config is a versioned Git artifact (per [ADR-009](/architecture/adr/ADR-009-branch-protection.md) §1) that enumerates which branches are protected and what kind of mutation is blocked on each. The [Projection Service](/architecture/components.md) mirrors the parsed file into a runtime table; enforcement always reads the runtime projection, not the file directly.

This document specifies the file format. Policy semantics (when a rule denies vs allows, how override interacts with roles) are the domain of ADR-009 §2–§4.

---

## 2. File Format

### 2.1 Format Choice

Branch-protection rules use **YAML**, matching the precedent set by [workflow definitions](/architecture/workflow-definition-format.md). The rationale is identical — diffable in Git, human-readable, parseable with the same hardened decoder (`internal/yamlsafe`) that already guards every YAML input Spine ingests.

### 2.2 File Location

The config lives at a single fixed path on the authoritative branch:

```
/.spine/branch-protection.yaml
```

The path is not configurable. Spine-internal state lives under `/.spine/` and the config is in that namespace for the same reason workflows live under `/workflows/`: a single well-known location makes the projection trivial and prevents ambiguity about which file governs.

### 2.3 Front Matter

The file is pure YAML — no Markdown, no YAML front matter block. Metadata (`version`) is a top-level key.

---

## 3. Schema

### 3.1 Top-Level Structure

```yaml
version: <int>          # Schema version. v1 is the only defined version.
rules:                  # Ordered list of branch-protection rules.
  - <rule>
```

### 3.2 Rule

Each entry in `rules` is:

```yaml
- branch: <string>         # Branch pattern (literal name or glob — §3.3)
  protections:             # Non-empty list of rule kinds (§3.4)
    - <rule_kind>
```

Both fields are required. Unknown keys cause a parse error.

### 3.3 Branch Patterns

- A literal name (`main`, `staging`) matches exactly one branch.
- A glob using `*` / `?` / `[...]` matches a branch name per Go's `path.Match` semantics. Notably, `*` matches a sequence of non-`/` characters — it does **not** cross a slash. So `release/*` matches `release/1.0` but not `release/1.0/patch`, and a bare `*` matches branches with no `/` at all.
- To protect a nested prefix, list explicit segments (`release/*/patch`) or add one rule per depth. A `**`-style recursive match is not supported in v1.
- Regex, negative patterns (`!release/*`), and nested alternations are **not** supported. v1 explicitly forecloses them; adding richer matching is a new-ADR question.
- The parser rejects `**` and any character Git itself disallows in ref names (`^`, `~`, `:`, space, control chars — see `git-check-ref-format`). Regex-adjacent characters that Git permits (`$`, `(`, `)`, `{`, `}`, `|`, leading `!`) are accepted as literals, since some teams genuinely use them in branch names. A pattern like `{main,staging}` therefore parses successfully but matches only a branch literally named `{main,staging}` — check the projection layer's match count if a rule appears inert.
- Full `git-check-ref-format` parity (e.g. rejecting `foo..bar`, `.lock` suffixes, `@{` sequences) is **not** performed at parse time. These produce inert rules rather than errors; a future enhancement may move this check into the projection layer, where "zero matches" can also warn on simple typos.

Duplicate `branch` entries (two rules with the same pattern string) are a parse error. To apply multiple protection kinds to one branch, list them in the same `protections:` array.

Overlap between patterns that match the same branch (e.g. `main` and `*`) is allowed; the effective protection is the union.

### 3.4 Rule Kinds

Only two kinds are defined in v1:

| Kind | Blocks |
|------|--------|
| `no-delete` | Any operation that removes the ref. |
| `no-direct-write` | Any advance of the ref that is not a Spine-governed merge (see [ADR-009 §2](/architecture/adr/ADR-009-branch-protection.md)). |

Unknown values in `protections:` are a parse error. The ruleset is closed for extension — additional kinds require a new ADR.

---

## 4. Versioning

The `version` field is required and must be `1`. Future schema changes bump the integer; the parser refuses any version it does not understand rather than silently accepting unknown fields. This keeps deployments and config files in lock-step.

---

## 5. Example

```yaml
version: 1
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: staging
    protections: [no-delete]
  - branch: "release/*"
    protections: [no-delete, no-direct-write]
```

Behavior of this config (per ADR-009 §2–§4):

- `git push --delete origin main` — rejected by `no-delete`.
- `git push origin local:main` (advance) — rejected by `no-direct-write` unless the pusher is an operator using `-o spine.override=true`.
- A Spine-governed merge to `main` via `Orchestrator.MergeRunBranch` — allowed (the policy classifies governed merges as distinct from direct writes).
- Deleting any `release/*` branch — rejected.
- Pushing to a branch not listed (e.g. `feat/x`) — allowed.

---

## 6. Relationship to Runtime

The Projection Service watches `/.spine/branch-protection.yaml` on the authoritative branch. On every merge:

1. Read the file content.
2. Parse via `internal/branchprotect/config.Parse`.
3. If parsing succeeds: replace the workspace's rows in `branch_protection_rules` atomically.
4. If parsing fails: retain the previous ruleset, emit an error event, and log. Never fall back to empty — that would silently disable protection.

When the file is absent (newly-imported repository, pre-seed deployment), the policy layer applies bootstrap defaults specified in [ADR-009 §1](/architecture/adr/ADR-009-branch-protection.md).

---

## 7. Cross-References

- [ADR-009 — Branch Protection](/architecture/adr/ADR-009-branch-protection.md) — the normative decision this format serves.
- [Workflow Definition Format](/architecture/workflow-definition-format.md) — format precedent for Spine's other pure-YAML governed artifact.
- [Go Coding Guidelines §9](/governance/go-coding-guidelines.md) — tagging conventions the domain types follow (`json:` + `yaml:`).

---

## 8. Evolution Policy

This document evolves in lock-step with the `branch-protection.yaml` schema version. A schema change:

1. Requires a new ADR (protection kinds are explicitly closed for extension in ADR-009 §6).
2. Bumps the `version` field. Old parsers refuse the new file; new parsers refuse old files only when backwards-incompatible fields were removed.
3. Updates §3 of this document with the new schema and §4 with the new version number.
