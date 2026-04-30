---
type: Architecture
title: ADR-Linked Validation Policy Format
status: Living Document
version: "0.1"
---

# ADR-Linked Validation Policy Format

---

## 1. Purpose

This document defines the **validation policy** artifact format — the deterministic enforcement recipe an ADR links to so that an architectural decision is enforceable rather than interpreted at runtime.

[EPIC-006](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md) commits Spine to this stance: code repositories produce evidence; ADRs declare governing intent; validation policies are the deterministic bridge between the two. Free-form prose in an ADR is not enforceable. A linked policy file is.

This document is the canonical reference for the policy format. Storage location, governance lifecycle (artifact-schema registration), runner integration mechanics, and validation-service rules are governed by sibling tasks within EPIC-006.

---

## 2. Scope

### 2.1 In Scope

- The `ValidationPolicyDocument` and per-policy `ValidationPolicy` shapes (fields, types, invariants)
- Selector model — which run-and-repository pairs a policy applies to
- Check declaration model — `command` (local execution) and `external` (rendered by an external system)
- Severity model and the deterministic-vs-advisory split
- Deterministic serialization rules (canonical ordering for YAML and JSON)
- Storage convention and ADR linkage shape
- Worked examples for API contract, migration, and lint checks

### 2.2 Out of Scope

- Artifact-schema registration as a governed artifact type — owned by [TASK-007](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-007-validation-policy-governance-update.md)
- Check runner integration mechanics (how runners discover and execute commands) — owned by [TASK-003](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-003-check-runner-integration-boundary.md)
- Validation rules consuming policy + evidence — owned by [TASK-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-004-validation-service-evidence-rules.md)
- Query / reporting surfaces — owned by [TASK-005](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-005-evidence-query-and-reporting.md)
- The execution evidence schema itself — see [Execution Evidence Schema](/architecture/execution-evidence.md)

---

## 3. Identity and Granularity

A **validation policy document** is one YAML file committed to the primary Spine repo. The file MAY hold one or more **validation policies**; each policy has its own `policy_id`, unique within the document.

The execution evidence schema's `ValidationPolicyRef` already commits to this shape — `policy_id` exists on the ref precisely so one path can host several policies without forcing a directory explosion.

A policy's identity is `(canonical document path, policy_id)`. Versioning is two-axis:

- **Document-level**: every commit to the file is a Git version. The full audit trail lives in `git log`.
- **Policy-level**: each policy carries a `version` string for human-readable major-version markers ("1", "2", "2026-04"). Spine treats it as opaque — there is no semver comparison.

When the meaning of a policy changes incompatibly, authors SHOULD bump the `version` and (where the policy is referenced from evidence) typically also rename the `policy_id` so old evidence keeps pointing to the old contract. The `Status` field marks the lifecycle of the policy itself (draft → active → deprecated → superseded).

---

## 4. Schema

### 4.1 ValidationPolicyDocument

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `schema_version` | yes | string | On-disk schema version. Always set; readers reject unknown versions. Current value: `"1"`. |
| `policies` | yes | object[] | One or more `ValidationPolicy` entries. MUST be non-empty. `policy_id` MUST be unique within the document. |
| `generated_at` | yes | timestamp | When `Canonicalize()` was last run on this document. Normalized to UTC. |

The document itself is plain YAML — there is **no Markdown body, no front matter**. This matches the convention already established for governed YAML files like `/.spine/repositories.yaml` (artifact-schema §5.8). Governance registration of the artifact type is covered in TASK-007.

### 4.2 ValidationPolicy

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `policy_id` | yes | string | Document-scoped unique identifier. Single-line. |
| `version` | yes | string | Opaque version label (`"1"`, `"2"`, `"2026-04"`). Single-line. |
| `title` | yes | string | Human-readable label. Single-line. |
| `description` | optional | string | Free-form description. The only multi-line field on the policy. |
| `status` | yes | enum | `draft`, `active`, `deprecated`, `superseded` (§4.6). |
| `adr_paths` | yes | string[] | Canonical ADR paths the policy enforces. MUST be non-empty (AC #2 — "ADRs can reference policies through typed links"). |
| `selector` | yes | object | `PolicySelector` (§4.3). |
| `checks` | yes | object[] | `PolicyCheck` rows (§4.4). MUST be non-empty. |

### 4.3 PolicySelector

```yaml
selector:
  repository_ids:
    - payments-service
  repository_roles:
    - code
  path_patterns:
    - cmd/*
    - internal/*
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `repository_ids` | conditional | string[] | Catalog repository IDs (matching `/.spine/repositories.yaml`). |
| `repository_roles` | conditional | string[] | Role labels (`spine`, `code`). |
| `path_patterns` | optional | string[] | POSIX-style globs (Go `path.Match` semantics) evaluated against `ChangedPathsSummary.Paths`. Empty means "always applies regardless of changed paths". |

**At least one of `repository_ids` / `repository_roles` MUST be set.** Validate enforces this so a policy is never silently global by accident. The match rule is OR across IDs and roles — explicit ID listing or role listing both qualify.

`path_patterns` are gating, not over-restrictive: an empty list means the policy is applicable to every run on a matching repo. This pairs naturally with the `CheckStatusSkipped` lifecycle in execution evidence — a check declared by a path-gated policy that did not match returns `skipped`, which counts as success in `IsSuccess()`.

**Truncated path summaries are treated conservatively.** When `ChangedPathsSummary.Truncated` is `true`, the visible `Paths` slice is incomplete (the producer dropped entries to fit a size budget — see [execution-evidence.md §4.2](/architecture/execution-evidence.md)). `PolicySelector.MatchesAnyPath` returns `true` whenever the summary is truncated, regardless of whether any visible path matched. The cost is at most a redundant check execution; the alternative — silently skipping a path-gated blocking policy on a large diff — would defeat EPIC-006 AC #4 (missing required evidence blocks publication).

### 4.4 PolicyCheck

```yaml
checks:
  - check_id: unit-tests
    name: Unit tests
    kind: command
    command: go test ./...
    interpretation: deterministic
    severity: blocking
    timeout_seconds: 600
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `check_id` | yes | string | Policy-scoped unique identifier. Matches `ExecutionEvidence.CheckResults[*].check_id`. Single-line. |
| `name` | optional | string | Human label. Single-line. |
| `description` | optional | string | Free-form description. Multi-line allowed. |
| `kind` | yes | enum | `command` or `external` (§4.4.1). |
| `command` | conditional | string | Shell command when `kind=command`. MUST be empty when `kind=external`. Single-line. |
| `interpretation` | yes | enum | `deterministic` or `advisory` (§4.4.2). |
| `severity` | yes | enum | `blocking` or `warning` (§4.4.3). |
| `timeout_seconds` | optional | integer | Max command runtime. Zero means "no policy-declared timeout"; negative values are rejected. Stored as integer seconds for round-trip determinism. |

#### 4.4.1 kind

| Value | Meaning |
|-------|---------|
| `command` | The runner executes a shell command in a cloned repo working tree. Result rows are produced by the runner. |
| `external` | The result row is produced by an external system (CI, human reviewer, security scanner). Whoever fills the row is out of scope for this schema. |

The boundary between local commands and external integrations is split at the policy schema level (this document) and the runner integration boundary level (TASK-003). This document commits only to the **declaration** shape; runner mechanics are TASK-003.

#### 4.4.2 interpretation

| Value | Meaning |
|-------|---------|
| `deterministic` | Same inputs produce the same verdict. Blocking severity is permitted. |
| `advisory` | The verdict is interpretive (LLM review, heuristic, judgment call). MUST be paired with `warning` severity. |

The split realizes EPIC-006 AC #4: "AI-assisted interpretation is explicitly non-blocking unless converted into a deterministic policy." `Validate` rejects an `advisory + blocking` combination — the type system, not runtime, is the gate. To make an interpretive check blocking, the author must rewrite it as a deterministic check (e.g. wrap the LLM in a validator that scores against fixed thresholds and exposes a deterministic pass/fail output).

#### 4.4.3 severity

| Value | Effect on publication | Lands in evidence as |
|-------|----------------------|----------------------|
| `blocking` | A non-success terminal result blocks publish (per EPIC-006 AC #4, missing/failed required evidence blocks publication). | `ExecutionEvidence.RequiredChecks` |
| `warning` | A non-success terminal result is visible in evidence and dashboards but does not block publish. | `ExecutionEvidence.AdvisoryChecks` |

Both severities still produce `CheckResult` rows in evidence; the gate behavior is the only difference. The split between `RequiredChecks` and `AdvisoryChecks` in [execution-evidence.md](/architecture/execution-evidence.md) is what makes the warning contract enforceable: `DeriveStatus` aggregates only over `RequiredChecks`, so a failed advisory check leaves the aggregate at `passed`.

### 4.5 ValidationPolicyStatus

| Status | Terminal? | Meaning |
|--------|-----------|---------|
| `draft` | no | Authored but not yet enforced. Validation rules MAY skip; reporting SHOULD show. |
| `active` | no | Currently enforced. Default state. |
| `deprecated` | no | Still enforced, but slated for removal. Operators should migrate. |
| `superseded` | yes | Replaced by a newer policy. MAY be skipped during validation; treat as historical. |

The lifecycle borrows the artifact-style `Draft → Active → Deprecated → Superseded` shape rather than the run/task `Pending → Completed` shape because policies are durable governance artifacts, not units of work.

### 4.6 Single-Line Field Discipline

Single-line fields reject `\n` and `\r`. Path patterns reject all `unicode.IsSpace` runes (including NBSP and U+2028 line separator). The list:

- `policy_id`, `version`, `title`
- Each `adr_paths[*]`
- Each `selector.repository_ids[*]`, `selector.repository_roles[*]`
- Each `selector.path_patterns[*]` (whitespace-rejecting, stricter than newline-rejecting)
- Each `checks[*].check_id`, `checks[*].name`, `checks[*].command`

`description` (on both the policy and per-check) is the only field that permits multi-line text. This is the trailer-injection / log-bleed defense pattern established by EPIC-005 TASK-006: policy files are committed into the primary repo and may be parsed by tools that treat newlines as record separators. The defense is cheap to add and free at runtime.

---

## 5. Determinism

`ValidationPolicyDocument.Canonicalize()` sorts every keyed slice and normalizes the document timestamp to UTC so two semantically-identical documents marshal byte-identically to JSON or YAML.

Sort keys:

- `policies[]` — by `policy_id`
- Per policy: `adr_paths[]` lexicographic
- Per policy: `selector.repository_ids[]`, `selector.repository_roles[]`, `selector.path_patterns[]` — lexicographic
- Per policy: `checks[]` — by `check_id`

Timestamp:

- `generated_at` → UTC

After `Canonicalize`, JSON and YAML output are byte-identical across timezones and across slice insertion order. AC #3 ("Policy execution is deterministic") starts with deterministic on-disk representation — if the file diff churns on every commit, the audit trail is meaningless.

Producers MUST call `Canonicalize` immediately before `Validate` and the marshal step. Readers MAY treat `Validate` as authoritative — it enforces sort-derived invariants like "no duplicate `check_id`."

---

## 6. Storage Convention

### 6.1 Primary-Repo Default

By default, validation policy documents live in:

```
/governance/validation-policies/{policy-name}.yaml
```

The `governance` namespace is correct because policies enforce governance decisions; their authority flows from ADRs, which `adr_paths` makes explicit. The naming is intent-bearing — `code-quality.yaml`, `migration-safety.yaml`, `api-contract.yaml` — not sequential. There are no policy IDs in filesystem paths because one file may host multiple policies.

A directory layout for a workspace with three policy documents:

```
governance/
  validation-policies/
    api-contract.yaml
    code-quality.yaml
    migration-safety.yaml
```

### 6.2 Format Choice (YAML vs JSON)

The committed default is **YAML** to match the rest of `/governance/` and the artifact-schema convention. JSON is supported equivalently — every domain field carries both `json` and `yaml` struct tags — for API responses and tooling pipelines that prefer JSON. Producers and consumers MUST treat the two as interchangeable.

### 6.3 ADR Linkage

ADRs reference policies through typed links in the ADR's front matter. The reverse direction is recorded inside the policy's `adr_paths` field. The reference is by canonical path (artifact-schema §3.2):

```yaml
# In /architecture/adr/ADR-014-evidence.md
links:
  - type: related_to
    target: /governance/validation-policies/code-quality.yaml
```

For the duration of EPIC-006, ADR → policy linkage uses the existing `related_to` type so this iteration does not silently introduce a new link type ahead of governance registration. [TASK-007](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-007-validation-policy-governance-update.md) will register the validation policy as a governed artifact type and MAY introduce a more specific `enforces` link type at that point; this format will switch to it in lockstep.

Bidirectional consistency is the responsibility of the validation service (TASK-004) — every `adr_paths` entry on a policy SHOULD have a corresponding link entry on the ADR.

The ADR is the durable governance authority. The policy is the deterministic recipe. Evidence carries the verdict. Each artifact has one job; together they realize EPIC-006's "code repositories produce evidence, they do not become governance authorities" stance.

---

## 7. Validation Invariants Enforced by `Validate()`

| Invariant | Failure mode caught |
|-----------|--------------------|
| `schema_version == "1"` | Unknown shape; readers cannot guess. |
| `generated_at` is non-zero | Forgotten Canonicalize / construction bug. |
| At least one policy in document | Empty document is meaningless. |
| `policy_id` unique within document | Two policies with the same ID would conflict on evidence references. |
| `check_id` unique document-wide (across all policies in the file) | Evidence rows are keyed by `check_id` alone — two policies declaring the same `check_id` would collapse onto a single evidence row, letting one policy's verdict silently satisfy or fail the other. |
| `check_id` unique across the entire policy set (workspace-level via `ValidateAcrossDocuments`) | Same risk extends across files. Document-level `Validate` is necessary but NOT sufficient; the workspace loader / validation service MUST also call `ValidateAcrossDocuments` to catch collisions across files. |
| `policy_id`, `version`, `title` non-empty and single-line | Required identity / labeling. |
| `status` ∈ `ValidValidationPolicyStatuses()` | Typo / unknown enum. |
| At least one `adr_paths` entry | AC #2 — every policy must trace back to an ADR. |
| `adr_paths` entries non-empty, single-line, unique | Sloppy refs would orphan governance trail. |
| Selector has at least one `repository_id` or `repository_role` | A "global" policy is a foot-gun; require explicit scope. |
| Selector entries unique, single-line, non-empty | Sloppy refs. |
| `path_patterns` are valid globs and contain no whitespace | Bad glob silently never matches; whitespace would defeat path matching. |
| At least one check | A policy with no checks asserts nothing. |
| `check_id` unique within policy | Conflicting verdicts on the same check. |
| `kind`, `interpretation`, `severity` ∈ valid enums | Typos / unknown values. |
| `interpretation=advisory` ⇒ `severity=warning` | AC #4 — advisory cannot be blocking. |
| `kind=command` ⇒ `command` non-empty | An empty command is unrunnable. |
| `kind=external` ⇒ `command` empty | External checks cannot also carry a runner command. |
| `timeout_seconds >= 0` | Negative timeouts are not meaningful. |
| Single-line fields reject `\n` / `\r` | Trailer-injection / log-bleed defense. |

`Validate` returns `domain.SpineError{Code: ErrInvalidParams}`, mapped to HTTP 400 by the gateway error layer.

---

## 8. Worked Examples

### 8.1 API Contract

```yaml
schema_version: "1"
generated_at: 2026-04-30T10:00:00Z
policies:
  - policy_id: api-contract
    version: "1"
    title: API contract compatibility
    description: |
      OpenAPI diff must show no breaking changes for /v1/* routes.
      Author MAY add new optional fields and new endpoints; removing or
      narrowing existing routes blocks publish.
    status: active
    adr_paths:
      - /architecture/adr/ADR-021-api-versioning.md
    selector:
      repository_roles:
        - code
      path_patterns:
        - openapi/*
    checks:
      - check_id: openapi-diff
        name: OpenAPI diff
        kind: command
        command: scripts/openapi-diff.sh
        interpretation: deterministic
        severity: blocking
        timeout_seconds: 120
```

### 8.2 Migration

A migration check is the canonical case for `kind=external` plus human gating: a tool can compute the diff, but the gate is a human reviewer's signoff.

```yaml
schema_version: "1"
generated_at: 2026-04-30T10:00:00Z
policies:
  - policy_id: migration-safety
    version: "1"
    title: Database migration safety
    description: |
      Every change touching db/migrations/ must be reviewed by a
      designated migration reviewer before publish.
    status: active
    adr_paths:
      - /architecture/adr/ADR-008-migrations.md
    selector:
      repository_roles:
        - code
      path_patterns:
        - db/migrations/*
    checks:
      - check_id: migration-review
        name: Manual migration review
        kind: external
        interpretation: deterministic
        severity: blocking
```

The corresponding evidence row carries `producer: human` and `produced_by: user/<reviewer-id>`. `interpretation: deterministic` is appropriate even though a human renders the verdict — the verdict is an explicit signoff, not an interpretive judgment.

### 8.3 Lint

```yaml
schema_version: "1"
generated_at: 2026-04-30T10:00:00Z
policies:
  - policy_id: lint
    version: "1"
    title: Lint
    status: active
    adr_paths:
      - /architecture/adr/ADR-006-code-style.md
    selector:
      repository_roles:
        - code
    checks:
      - check_id: golangci-lint
        name: golangci-lint
        kind: command
        command: golangci-lint run ./...
        interpretation: deterministic
        severity: blocking
        timeout_seconds: 300
```

### 8.4 Advisory AI Review

The advisory shape exists explicitly so authors can declare interpretive checks without violating AC #4. A failed advisory check shows on dashboards but does not block publish.

```yaml
schema_version: "1"
generated_at: 2026-04-30T10:00:00Z
policies:
  - policy_id: ai-readability
    version: "1"
    title: AI readability review
    status: active
    adr_paths:
      - /architecture/adr/ADR-014-evidence.md
    selector:
      repository_roles:
        - code
    checks:
      - check_id: llm-review
        name: LLM readability review
        kind: external
        interpretation: advisory
        severity: warning   # MUST be warning when interpretation=advisory.
```

To convert this into a blocking gate, the author would replace the LLM with a deterministic linter (e.g. a docstring-coverage tool with a fixed threshold) and switch `interpretation` to `deterministic`.

---

## 9. Acceptance Criteria Mapping

| Task / Epic AC | Realized by |
|---------------|-------------|
| TASK-002 AC #1: ADRs can reference policies through typed links | §6.3 — ADR → policy via typed `enforces` link; reverse via `adr_paths` on policy. |
| TASK-002 AC #2: Policies are versioned in the primary repo | §3 — file lives in `/governance/validation-policies/`; commits provide history; `version` field marks human-readable major-version markers. |
| TASK-002 AC #3: Policy execution is deterministic | §5 — `Canonicalize()` + lexicographic ordering + UTC normalization produce byte-identical output. `kind=command` + `kind=external` are both deterministic in their result shape. |
| TASK-002 AC #4: AI-assisted interpretation is explicitly non-blocking unless converted into a deterministic policy | §4.4.2 + Validate — `interpretation=advisory + severity=blocking` is rejected at write time. |
| TASK-002 AC #5: Documentation includes examples for API contract, migration, and lint checks | §8.1 / §8.2 / §8.3. |
| TASK-002 AC #6: Format design is consistent with the governance update delivered in TASK-007 | §6.1 storage and §4.1 file shape (pure YAML, no front matter) follow the artifact-schema §5.8 (`repositories.yaml`) precedent that TASK-007 will register. |
| EPIC-006 AC #1: A task can require evidence for each affected repository | Selectors bind policies to repository IDs/roles; evidence ties the resulting `check_id` rows to the run-and-repository tuple via the schema in `architecture/execution-evidence.md`. |
| EPIC-006 AC #2: ADRs can link to deterministic validation policies | §6.3 + `adr_paths` required field. |
| EPIC-006 AC #3: Required checks produce structured results tied to repo, branch, and commit | Joint with execution-evidence: `check_id` here is the same `check_id` there, and the evidence record carries repo/branch/commit anchors. |

Downstream tasks (TASK-003, TASK-004, TASK-005, TASK-006, TASK-007) consume this format; their ACs are realized in their own files.

---

## 10. Cross-References

- [EPIC-006 — Cross-Repo Execution Evidence](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md)
- [Execution Evidence Schema](/architecture/execution-evidence.md) — the matching evidence record shape; `check_id` here is the join key.
- [Multi-Repository Integration](/architecture/multi-repository-integration.md) — repository catalog and role conventions.
- [Artifact Front Matter Schema §5.8](/governance/artifact-schema.md) — pure-YAML governed-file precedent (`repositories.yaml`).
- [Constitution](/governance/constitution.md) §2 (Source of Truth), §7 (Reproducibility).

---

## 11. Evolution Policy

This document evolves with EPIC-006. Areas expected to require refinement:

- TASK-007 will register the policy artifact type in `governance/artifact-schema.md`. Selector, kind, and interpretation models defined here are the input the registration consumes; they are NOT expected to bump `schema_version` when registered.
- New `kind` values (e.g. `event`, `webhook`) for richer external integrations may be introduced as additive changes under the same `schema_version`.
- New `interpretation` or `severity` values would bump `schema_version`.
- New optional fields on `PolicyCheck` (e.g. `tags`, `runner_constraints`) may be introduced as additive changes under the same `schema_version`.

Schema changes that alter validation invariants or storage conventions are captured as ADRs.
