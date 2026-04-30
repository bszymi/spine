---
type: Architecture
title: Execution Evidence Schema
status: Living Document
version: "0.1"
---

# Execution Evidence Schema

---

## 1. Purpose

This document defines the **execution evidence** record — the structured proof that a code repository's contribution to a Run satisfied governed intent.

[EPIC-006](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md) makes one architectural commitment explicit: code repositories produce evidence, they do not become governance authorities. The primary Spine repo remains the ledger; per-repo evidence files are committed into it so the audit trail is durable and queryable.

This document is the canonical reference for the evidence schema. Storage location, validation behavior, query/reporting, and downstream consumers are governed by sibling tasks within EPIC-006.

---

## 2. Scope

### 2.1 In Scope

- The `ExecutionEvidence` record shape (fields, types, invariants)
- Per-check result rows (`CheckResult`) for both human and automated producers
- Validation policy references (`ValidationPolicyRef`) tying evidence to ADR-linked policies
- Deterministic serialization rules (canonical ordering for YAML and JSON)
- The default storage convention on the primary Spine repo

### 2.2 Out of Scope

- The validation policy artifact itself (defined by [TASK-002](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-002-adr-linked-validation-policy-format.md))
- Check runner integration mechanics (defined by [TASK-003](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-003-check-runner-integration-boundary.md))
- Validation rules consumed by the Validation Service (defined by [TASK-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-004-validation-service-evidence-rules.md))
- Query / reporting surfaces over evidence (defined by [TASK-005](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-005-evidence-query-and-reporting.md))
- Source-code indexing or build orchestration (explicitly out of scope per the parent epic)

---

## 3. Identity and Granularity

One `ExecutionEvidence` record per `(RunID, RepositoryID)` tuple.

The primary repo records evidence for itself the same way code repos do — `RepositoryID = "spine"` is a valid identity. Treating the primary repo uniformly with code repos keeps the audit format symmetric and lets dashboards report a single shape.

Why per-repo and not per-run: cross-repo merges are not atomic ([Multi-Repository Integration §4.4](/architecture/multi-repository-integration.md)). Aggregating evidence into a single per-run record would hide the partial states that operators must see and act on. The same per-repo decomposition applied to merge outcomes (EPIC-005) applies here.

---

## 4. Schema

### 4.1 ExecutionEvidence Record

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `schema_version` | yes | string | On-disk schema version. Always set; readers reject unknown versions. Current value: `"1"`. |
| `run_id` | yes | string | The Run whose execution this evidence describes. |
| `task_path` | yes | string | Canonical path to the Task artifact governing the run. |
| `repository_id` | yes | string | Workspace-scoped repository ID. `spine` for the primary repo. |
| `branch_name` | yes | string | The run branch this evidence covers (e.g. `spine/run/run-id-slug-hex`). |
| `base_commit` | yes | string | Git commit SHA the run branched from. |
| `head_commit` | yes | string | Git commit SHA at the tip of the run branch when this evidence was generated. |
| `changed_paths` | yes | object | Deterministic, secret-free summary of the diff between `base_commit` and `head_commit`. See §4.2. |
| `required_checks` | optional | string[] | IDs of **blocking** checks the governing policy / task declared. Failed/missing entries flip aggregate status. Empty when no blocking checks were required. |
| `advisory_checks` | optional | string[] | IDs of **non-blocking** checks (warning severity / advisory interpretation in [validation-policy.md](/architecture/validation-policy.md)). Failures here produce evidence rows but do NOT affect aggregate status. MUST NOT overlap with `required_checks`. |
| `check_results` | optional | object[] | Per-check outcome rows. Each `check_id` MUST appear in either `required_checks` or `advisory_checks`. See §4.3. |
| `validation_policies` | optional | object[] | ADR-linked policies that informed the required-check set. See §4.4. |
| `actor` | yes | string | Principal that owns this evidence record. |
| `trace_id` | yes | string | Observability correlation joining this record to engine / event logs. |
| `status` | yes | enum | Aggregate status (§4.5). MUST equal `DeriveStatus()` over the rows; `Validate` rejects mismatches. |
| `generated_at` | yes | timestamp | When the evidence record was canonicalized for write. |

**Single-line fields reject newlines.** `run_id`, `task_path`, `repository_id`, `branch_name`, `base_commit`, `head_commit`, `actor`, `trace_id`, every check result's `summary`, `produced_by`, `evidence_uri`, every changed-path entry, and every `validation_policies[*]` field must not contain `\n` or `\r`. This is a defense against trailer-injection / log-bleed attacks — the evidence file is committed into the primary repo and may be parsed in contexts that treat newlines as record separators.

### 4.2 ChangedPathsSummary

```yaml
changed_paths:
  files_changed: 4
  insertions: 92
  deletions: 17
  paths:
    - cmd/main.go
    - internal/api/handler.go
  truncated: false
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `files_changed` | yes | integer | Total files changed in the diff. Non-negative. |
| `insertions` | yes | integer | Total inserted lines. Non-negative. |
| `deletions` | yes | integer | Total deleted lines. Non-negative. |
| `paths` | optional | string[] | List of changed paths (capped to keep evidence small). |
| `truncated` | optional | bool | `true` when the producer dropped paths to fit a size budget. |

**Raw diff content is NEVER part of the schema.** This is a deliberate design choice: code under change can contain secrets that may be added or removed in a single commit. Storing only counts and path names lets evidence be committed publicly without leaking sensitive content.

### 4.3 CheckResult

```yaml
check_results:
  - check_id: unit-tests
    name: Unit tests
    status: passed
    producer: automated
    produced_by: ci/github-actions
    summary: "go test ./... passed (231 cases)"
    evidence_uri: https://ci.example.com/runs/123
    started_at: 2026-04-30T09:58:00Z
    completed_at: 2026-04-30T09:59:30Z
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `check_id` | yes | string | Policy-defined identifier; MUST match an entry in `required_checks`. |
| `name` | optional | string | Human-readable label. UI falls back to `check_id` when empty. |
| `status` | yes | enum | `pending`, `running`, `passed`, `failed`, `skipped`, `error`. See §4.3.1. |
| `producer` | conditional | enum | `human` or `automated`. Required once `status` leaves `pending`. |
| `produced_by` | conditional | string | Actor or runner identity. Required once `status` leaves `pending` — anonymous evidence is not auditable. |
| `summary` | optional | string | Single-line, human-readable description. NEVER raw logs. |
| `evidence_uri` | optional | string | Pointer to detailed logs / artifacts (object storage, CI URL). Never the content itself. |
| `started_at` | optional | timestamp | When the producer began the check. |
| `completed_at` | optional | timestamp | When the producer terminated the check. |

#### 4.3.1 CheckStatus Lifecycle

| Status | Terminal? | Counts as Success? | Meaning |
|--------|-----------|--------------------|---------|
| `pending` | no | no | Declared but no producer has reported yet. |
| `running` | no | no | Producer claimed the check; in flight. |
| `passed` | yes | yes | Terminal authoritative pass. |
| `failed` | yes | no | Terminal policy violation. |
| `skipped` | yes | yes | Terminal non-applicability (e.g. no relevant paths changed). Counts as a satisfied requirement. |
| `error` | yes | no | Terminal infrastructure failure. The runner could not produce a verdict. |

`skipped` is intentionally a success: a declared-and-not-applicable check is a satisfied requirement, not a missing one. `error` is intentionally a failure: a runner crash means we have no verdict, so the policy cannot be cleared.

#### 4.3.2 Producer Kinds

Both `human` and `automated` producers fill the same `CheckResult` shape. The schema deliberately does not privilege automation — a human signoff (e.g. "security review approved") is a legitimate piece of execution evidence.

For `producer: human`, `produced_by` is the actor ID (e.g. `user/alice`).
For `producer: automated`, `produced_by` is the runner identity (e.g. `ci/github-actions` or `automation/spine-runner`).

### 4.4 ValidationPolicyRef

```yaml
validation_policies:
  - adr_path: /architecture/adr/ADR-014-evidence.md
    policy_path: /governance/validation-policies/code-quality.yaml
    policy_id: code-quality-v1
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `adr_path` | yes | string | Canonical path to the ADR that governs the policy. Satisfies EPIC-006 AC #2. |
| `policy_path` | optional | string | Canonical path to the deterministic policy artifact (defined by TASK-002). |
| `policy_id` | optional | string | Policy identifier within the artifact when one path holds multiple policies. |

The `ValidationPolicyRef` is intentionally thin in this iteration. The concrete shape of the policy artifact itself is owned by TASK-002 (ADR-linked validation policy format) and TASK-007 (register validation policy as a governed artifact type). What this schema commits to is the **relationship**: every required check can be traced back to an ADR.

### 4.5 EvidenceStatus

| Status | Terminal? | Meaning |
|--------|-----------|---------|
| `pending` | no | At least one required check is still pending or running, or has no result row yet. |
| `passed` | yes | Every required check has a successful terminal result (`passed` or `skipped`). |
| `failed` | yes | At least one required check terminated in `failed` or `error`. |

`status` is computed by `DeriveStatus()`:

1. If any required check has no terminal result yet (no row, or row in `pending`/`running`) → `pending`.
2. Else if any required check terminated in `failed` or `error` → `failed`.
3. Else → `passed`.

This rule is deliberately stricter than "any failure → failed": a missing required check is also a blocker (EPIC-006 AC #4 — missing OR failed required evidence blocks publication). `status` is stored explicitly for read-side efficiency, but `Validate` cross-checks it against the rows so the file cannot silently disagree with itself.

---

## 5. Determinism

`ExecutionEvidence.Canonicalize()` sorts every slice that has a natural deterministic key:

- `required_checks` — lexicographic
- `advisory_checks` — lexicographic
- `check_results` — by `check_id`
- `validation_policies` — by `(adr_path, policy_path, policy_id)`
- `changed_paths.paths` — lexicographic

After `Canonicalize`, two semantically-identical evidence values produce **byte-identical** YAML and JSON output. This is required by AC "Evidence is serializable as deterministic YAML or JSON" and it is what lets producers committing evidence into Git get clean diffs.

Producers MUST call `Canonicalize` immediately before `Validate` and the marshal step. Readers MAY treat `Validate` as the authoritative consistency check — it enforces sort-derived invariants like "no duplicate `check_id`" and "stored status equals derived status."

---

## 6. Storage Convention

### 6.1 Primary-Repo Default

By default, evidence files are committed to the primary Spine repo at:

```
/.spine/runs/{run_id}/evidence/{repository_id}.yaml
```

This convention follows the `/.spine/` operational-governance pattern already used by [`repositories.yaml`](/architecture/multi-repository-integration.md#21-governed-catalog-spinerepositoriesyaml) — files committed to the primary repo, governing operational behavior, but not part of the artifact-with-front-matter schema. Evidence is plain YAML with no Markdown body.

The full directory layout for a run with three affected repositories:

```
.spine/
  runs/
    run-abc123/
      evidence/
        spine.yaml
        payments-service.yaml
        api-gateway.yaml
```

Committing under the primary repo satisfies AC "Evidence is auditable from primary-repo history" — every state change to evidence is a Git commit visible in the ledger.

### 6.2 Run-Branch Artifact Variant

Producers MAY also generate evidence as an artifact on the run branch (in the relevant code repo) before the merge step. This is appropriate when the producer is the runner that just completed the work and the primary repo's evidence file is written from the run-branch artifact during the merge step.

Both placements use the identical schema and identical determinism rules. The merge-time write into the primary repo is the durable record; any run-branch copy is a producer-side staging artifact and is not relied on after the run completes.

### 6.3 Format Choice (YAML vs JSON)

The committed default is **YAML** to match the rest of `/.spine/` and the artifact frontmatter convention. JSON is supported equivalently — every domain field carries both `json` and `yaml` struct tags — for API responses and tooling pipelines that prefer JSON. Producers and consumers MUST treat the two as interchangeable.

---

## 7. Secret and Log Exclusion

The schema is engineered to keep secrets and raw logs OUT of evidence files:

- `changed_paths` records counts plus capped path names — never raw diff content.
- `check_results[*].summary` is a single-line description — embedded newlines are rejected by `Validate`.
- `check_results[*].evidence_uri` is a pointer to detailed logs (object storage, CI run URL) — never inline content. URIs that contain whitespace or newlines are rejected.
- No field carries unredacted log streams, command output, or credential material.

Producers wishing to attach detailed logs publish them to an external store and reference them via `evidence_uri`. Storing the logs themselves in Git would violate the "Evidence excludes secrets and raw logs by default" AC and would bloat the audit ledger.

---

## 8. Validation Invariants Enforced by `Validate()`

| Invariant | Failure mode caught |
|-----------|--------------------|
| Required fields present | Schema cannot be partially populated. |
| `status` is one of `ValidEvidenceStatuses` | Typo or unknown enum value. |
| `status` equals `DeriveStatus()` | Stored aggregate cannot disagree with the rows. |
| `check_results[*].check_id` ∈ `required_checks` ∪ `advisory_checks` | Orphan results cannot show rows for undeclared checks. |
| `check_results[*].check_id` is unique | No conflicting verdicts for the same check. |
| `required_checks` has no duplicates | One declaration per check. |
| `advisory_checks` has no duplicates | One declaration per check. |
| `required_checks` ∩ `advisory_checks` is empty | A check is either blocking or advisory, not both. |
| `producer` and `produced_by` set once status leaves pending | Anonymous non-pending evidence is rejected. |
| `validation_policies[*].adr_path` non-empty | Every policy ref ties back to an ADR. |
| Single-line fields reject `\n` / `\r` | Trailer-injection / log-bleed defense. |
| `evidence_uri` rejects whitespace and newlines | URI must be a single token. |
| `changed_paths` counts are non-negative | Bad arithmetic from producer. |

`Validate` returns a `domain.SpineError` with code `invalid_params`, mapped to HTTP 400 by the gateway error layer.

---

## 9. Acceptance Criteria Mapping

| Task / Epic AC | Realized by |
|---------------|-------------|
| TASK-001: Evidence is tied to repository, branch, and commit | Required `repository_id`, `branch_name`, `base_commit`, `head_commit` fields. |
| TASK-001: Evidence is serializable as deterministic YAML or JSON | `Canonicalize()` plus equivalent `json` and `yaml` struct tags. |
| TASK-001: Evidence can be committed to the primary repo ledger | Storage convention §6.1 (`/.spine/runs/{run_id}/evidence/{repository_id}.yaml`). |
| TASK-001: Evidence excludes secrets and raw logs by default | §7 — schema admits no raw log / diff fields; only counts + URIs. |
| TASK-001: Schema supports both human and automated check producers | `CheckProducerKind` enum covering both, identical row shape (§4.3.2). |
| EPIC-006 AC #1: A task can require evidence for each affected repository | Per-repo `ExecutionEvidence` record + `required_checks` slice; one record per `(run_id, repository_id)`. |
| EPIC-006 AC #2: ADRs can link to deterministic validation policies | `ValidationPolicyRef.adr_path` is required for every entry. |
| EPIC-006 AC #3: Required checks produce structured results tied to repo, branch, and commit | `CheckResult` + parent `ExecutionEvidence` carry repo/branch/commit context together. |
| EPIC-006 AC #4: Missing or failed required evidence blocks publication | `DeriveStatus` returns `pending` for missing checks and `failed` for any failure/error — a non-`passed` status is the gate signal for downstream rules. |
| EPIC-006 AC #5: Evidence is auditable from primary-repo history | Primary-repo storage (§6.1) means every change is a Git commit. |

Downstream tasks (TASK-002…007) consume this schema; their ACs are realized in their own files.

---

## 10. Cross-References

- [EPIC-006 — Cross-Repo Execution Evidence](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md)
- [Multi-Repository Integration](/architecture/multi-repository-integration.md) — per-repo decomposition, primary-repo-as-ledger model
- [Validation Service Specification](/architecture/validation-service.md) — downstream consumer of evidence (TASK-004)
- [Artifact Front Matter Schema](/governance/artifact-schema.md) §2.0 — operational governance files under `/.spine/` are governed but use a different schema family
- [Constitution](/governance/constitution.md) §2 (Source of Truth), §7 (Reproducibility)

---

## 11. Evolution Policy

This document evolves with EPIC-006. Areas expected to require refinement:

- Additional `CheckStatus` values (e.g. timed-out distinct from error) once runner integration matures.
- Optional inline structured details on `CheckResult` (counts, sub-result rows) — added as new optional fields under the same `schema_version`.
- A non-additive change (renaming a field, removing one, narrowing an enum) bumps `schema_version`.

Schema changes that alter validation invariants or storage conventions are captured as ADRs.
