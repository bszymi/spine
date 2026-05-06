---
type: Architecture
title: Validation Service Specification
status: Living Document
version: "0.1"
---

# Validation Service Specification

---

## 1. Purpose

This document defines the concrete validation rules and operational contract for the Validation Service in Spine v0.x.

The [Constitution](/governance/constitution.md) §11 mandates that artifacts must be validated against their governed context — not in isolation. The [System Components](/architecture/components.md) §4.8 defines the Validation Service as the component responsible for executing these checks. The [Workflow Definition Format](/architecture/workflow-definition-format.md) §5.2 defines the `cross_artifact_valid` condition that triggers validation during workflow execution.

This document specifies what the Validation Service checks, how it classifies results, and how it integrates with the Workflow Engine.

---

## 2. Validation Scope

### 2.1 What the Validation Service Checks

The Validation Service performs **cross-artifact consistency checks** — validations that require reading multiple artifacts or comparing an artifact against its governed context.

It does **not** perform:

- **Schema validation** of individual artifacts (the Artifact Service handles this using [Artifact Schema](/governance/artifact-schema.md))
- **Workflow definition validation** — the workflow validation suite lives in [Workflow Validation](/architecture/workflow-validation.md) and is exposed as a distinct code path invoked by `workflow.create`, `workflow.update`, and `workflow.validate` per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md). The generic artifact validation path does not evaluate workflow-specific rules.
- **Authentication or authorization checks** (handled by the [Security Model](/architecture/security-model.md))

### 2.2 Governed Context

The governed context for an artifact includes:

| Context Layer | Examples | Defined In |
|---------------|----------|------------|
| Strategic intent | Charter philosophy, initiative goals | `/governance/charter.md`, initiative artifacts |
| Governance rules | Constitution constraints, guidelines | `/governance/constitution.md`, `/governance/guidelines.md` |
| Architecture | Domain model, component design, data model | `/architecture/*.md` |
| Product definition | Boundaries, non-goals, success metrics | `/product/*.md` |
| Parent artifacts | Parent initiative, parent epic | Linked via `parent` links |
| Sibling artifacts | Other tasks in the same epic, related ADRs | Linked via typed links |

Not all context layers are checked for every artifact. The validation rules (§3) define which checks apply to which artifact types.

---

## 3. Validation Rules

### 3.1 Rule Categories

Rules are organized into categories that map to the mismatch classifications from [Components](/architecture/components.md) §4.8:

| Category | Description | Severity |
|----------|-------------|----------|
| Structural integrity | Hierarchical relationships are valid and consistent | Error |
| Link consistency | Bidirectional links are present and correct | Error |
| Status consistency | Artifact status is compatible with related artifact statuses | Warning |
| Scope alignment | Artifact content aligns with parent scope and boundaries | Warning |
| Prerequisite completeness | Required predecessor artifacts exist and are in valid state | Error |

### 3.2 Structural Integrity Rules

These rules ensure the artifact hierarchy is well-formed.

| Rule ID | Description | Applies To | Severity |
|---------|-------------|------------|----------|
| `SI-001` | Parent artifact must exist at the referenced path | Task, Epic | Error |
| `SI-002` | Parent artifact must be of the correct type (Epic for Task, Initiative for Epic) | Task, Epic | Error |
| `SI-003` | Parent artifact must not be in a terminal state (`Completed`, `Superseded`) when child is `In Progress` | Task, Epic | Warning |
| `SI-004` | Initiative path in Task front matter must match the initiative referenced by the parent Epic | Task | Error |
| `SI-005` | Artifact type field must match the expected type for its repository location | All | Error |

### 3.3 Link Consistency Rules

These rules ensure governed relationships are bidirectionally consistent.

| Rule ID | Description | Applies To | Severity |
|---------|-------------|------------|----------|
| `LC-001` | For each `parent` link, the target artifact must have a corresponding `contains` link back (or the relationship must be inferable from file hierarchy) | All with links | Warning |
| `LC-002` | For each `blocks` link, the target must have a corresponding `blocked_by` link | All with links | Warning |
| `LC-003` | For each `supersedes` link, the target must have a corresponding `superseded_by` link | All with links | Warning |
| `LC-004` | Link targets must resolve to existing artifacts | All with links | Error |
| `LC-005` | Link targets must use the canonical path format | All with links | Error |

### 3.4 Status Consistency Rules

These rules detect status combinations that indicate governance issues.

| Rule ID | Description | Applies To | Severity |
|---------|-------------|------------|----------|
| `SC-001` | A Task marked `Completed` should have `acceptance` field set | Task | Warning |
| `SC-002` | An Epic marked `Completed` should have all child Tasks in terminal state | Epic | Warning |
| `SC-003` | An Initiative marked `Completed` should have all child Epics in terminal state | Initiative | Warning |
| `SC-004` | A `Superseded` artifact should have a `supersedes`/`superseded_by` link to its successor | All | Warning |
| `SC-005` | An artifact with `blocked_by` links should not be `In Progress` if the blocker is not in terminal state | Task | Warning |

### 3.5 Scope Alignment Rules

These rules check that artifact content aligns with its governed scope. Scope alignment rules are more subjective and are implemented as warnings.

| Rule ID | Description | Applies To | Severity |
|---------|-------------|------------|----------|
| `SA-001` | Task deliverables should reference artifact paths consistent with the repository structure conventions | Task | Warning |
| `SA-002` | ADR decisions should reference the architectural context they affect | ADR | Warning |

Scope alignment rules are intentionally limited in v0.x. As the system matures, AI-assisted scope analysis may extend these checks.

### 3.6 Prerequisite Completeness Rules

These rules ensure required predecessor work is in place.

| Rule ID | Description | Applies To | Severity |
|---------|-------------|------------|----------|
| `PC-001` | Artifacts referenced in `blocked_by` links must exist | All with links | Error |
| `PC-002` | When a Task transitions to `In Progress`, its parent Epic must be at least `In Progress` | Task | Warning |
| `PC-003` | When an Epic transitions to `In Progress`, its parent Initiative must be at least `In Progress` | Epic | Warning |

### 3.7 Execution Evidence Rules

These rules validate the multi-repository execution-evidence side of a Run before publication. They activate when the engine is wired with the resolvers documented in §6.4. They are skipped for planning runs and for tasks with no associated Run.

| Rule ID | Description | Applies To | Severity |
|---------|-------------|------------|----------|
| `EV-001` | Every affected repository in `Run.AffectedRepositories` must produce execution evidence (one record per `(run_id, repository_id)` per [execution-evidence.md](/architecture/execution-evidence.md) §3) | Task | Error |
| `EV-002` | Evidence's `branch_name` must match `Run.BranchName` (or the per-repo override in `Run.RepositoryBranches`); evidence's `base_commit` must match `Run.RepositoryBaselines[repo]` | Task | Error |
| `EV-003` | For every applicable validation policy (per `PolicySelector`), every check declared with `severity: blocking` must appear in evidence's `check_results` | Task | Error |
| `EV-004` | Every blocking check's `check_results[*].status` must be `passed` or `skipped`; warning-severity check failures emit warnings (do not block publish) per TASK-004 AC #4 | Task | Error / Warning |
| `EV-005` | Evidence's `head_commit` must match the live branch tip when a `BranchTipResolver` is wired (no-op without it); a mismatch indicates new commits landed since evidence was produced | Task | Error |

EV-* findings are classified `execution_evidence` (a new classification added with TASK-004; see §4). Each finding carries structured `repository_id`, `policy_id`, and `check_id` fields on `ValidationError` so audit dashboards can filter without text-parsing the message — TASK-004 AC #5: "Validation output names repo ID, policy ID, and failing check."

EV-001 treats a resolver error as "evidence missing". The runner's evidence-write path is the authoritative producer; if the resolver cannot read it back, validation cannot let publication proceed without a verifiable record. Transient resolver errors are surfaced via `observe.Logger` so the operator can debug without losing the publish-block guarantee.

EV-002 does not double-emit on absent evidence — that's EV-001's responsibility. The two rules are deliberately separate so a run that publishes evidence on the wrong branch surfaces as an EV-002 finding (regenerate from the right ref) rather than as an EV-001 (regenerate at all).

EV-003 only flags missing **blocking** checks. Advisory checks are optional by definition; their absence is acceptable. EV-004 carries the symmetric rule: a present-but-failed advisory check emits a warning, not an error.

EV-005 is the only EV-* rule that requires an external state source (the live branch tip). It is intentionally a no-op when no `BranchTipResolver` is wired, because the rule slot exists for the future production path that has access to a git client; until then EV-001 / EV-002 / EV-003 / EV-004 already cover the AC surface, and a false-positive staleness flag would block valid publishes.

### 3.8 Skill Eligibility Rules

These rules validate actor skill eligibility during workflow execution.

| Rule ID | Description | Applies To | Severity |
|---------|-------------|------------|----------|
| `SE-001` | Actor must possess all skills declared in `execution.required_skills` on the workflow step | Step assignment | Error |
| `SE-002` | `EventAssignmentFailed` is emitted with missing skill details when skill eligibility check fails | Step assignment | N/A (event) |

Skill eligibility is validated via `actor.ValidateSkillEligibility()`. When an actor is explicitly assigned to a step and lacks required skills, the error message identifies the specific missing skills. When no eligible actors are found during pool-based selection, the assignment fails with `EventAssignmentFailed` including the failure reason in the event payload.

---

## 4. Mismatch Classification

When validation fails, each failure is classified to guide resolution:

| Classification | Meaning | Resolution Path |
|---------------|---------|-----------------|
| `structural_error` | Artifact hierarchy is broken (missing parent, invalid reference) | Fix the artifact's front matter or create the missing artifact |
| `link_inconsistency` | Bidirectional links are incomplete or broken | Add the missing inverse link or fix the target path |
| `status_conflict` | Status combination indicates a governance issue | Update the conflicting artifact's status or create follow-up work |
| `scope_conflict` | Artifact content may not align with governed scope | Review and adjust scope, or create an ADR documenting the deviation |
| `missing_prerequisite` | Required predecessor artifact is missing or not ready | Complete the prerequisite or remove the dependency |
| `execution_evidence` | Multi-repo execution evidence is missing, mismatched, or stale (TASK-004 EPIC-006) | Regenerate evidence by re-running the runner against the current run branch, or reconcile branch/commit drift |

---

## 5. Validation Contract

### 5.1 Invocation

The Validation Service is invoked in three ways:

**From workflow preconditions:**

When a step has a `cross_artifact_valid` precondition, the Workflow Engine calls the Validation Service before the step begins:

```yaml
preconditions:
  - type: cross_artifact_valid
```

In this context, the Validation Service validates the Run's governed artifact against all applicable rules.

**From system operations:**

The `system.validate_all` operation (per [Access Surface](/architecture/access-surface.md) §3.5) triggers validation across all artifacts in the repository. This is an administrative operation for detecting drift.

**From workflow definition operations:**

Workflow definition writes (`workflow.create`, `workflow.update`) and `workflow.validate` invoke a distinct workflow validation suite rather than this service. That suite is defined in [Workflow Validation](/architecture/workflow-validation.md) and enforces workflow-specific structural invariants (step-reference integrity, cycle detection, divergence/convergence balance, actor/skill resolution). Failure produces `validation_failed` responses with the same shape as described in §5.3.

Per [ADR-008](/architecture/adr/ADR-008-workflow-lifecycle-governance.md), this structural suite runs at every commit regardless of dispatch path (planning run, branch-scoped write, or operator bypass). Domain-logic review (wrong step sequence, inappropriate actor type for a step, subtle retry/timeout policy) is a separate concern handled by the `review` step of the `workflow-lifecycle` workflow — not by this validation service.

### 5.2 Input

The Validation Service receives:

```yaml
validation_request:
  artifact_path: <string>        # Canonical path of the artifact to validate
  rule_categories: [<string>]    # Optional: restrict to specific categories (default: all)
  context:
    run_id: <string|null>        # Associated Run (if invoked from workflow)
    trace_id: <string|null>      # Observability correlation
```

### 5.3 Output

The Validation Service returns:

```yaml
validation_result:
  artifact_path: <string>
  status: <enum>                 # passed, failed, warnings
  timestamp: <timestamp>
  checks_executed: <integer>     # Total rules evaluated
  errors:
    - rule_id: <string>          # e.g., "SI-001"
      category: <string>         # e.g., "structural_integrity"
      classification: <string>   # e.g., "structural_error"
      message: <string>          # Human-readable description
      artifact_path: <string>    # Artifact where the issue was detected
      related_artifact: <string|null> # Other artifact involved (if applicable)
  warnings:
    - rule_id: <string>
      category: <string>
      classification: <string>
      message: <string>
      artifact_path: <string>
      related_artifact: <string|null>
```

### 5.4 Status Determination

| Condition | Status |
|-----------|--------|
| No errors and no warnings | `passed` |
| No errors but warnings present | `warnings` |
| One or more errors | `failed` |

### 5.5 Effect on Workflow Execution

When invoked from a `cross_artifact_valid` precondition:

- **`passed`** — precondition is satisfied; the step may proceed
- **`warnings`** — precondition is satisfied; warnings are logged and emitted as events
- **`failed`** — precondition is not satisfied; the step cannot proceed

When a step is blocked by validation failure, the Workflow Engine:

1. Records the failure in the step execution
2. Emits an operational event with the validation result
3. The step remains in `waiting` status until the issues are resolved and validation is re-run

### 5.6 Branch-Scoped Validation

For planning runs, the validation step operates on the entire branch rather than a single artifact. This is implemented by the `ValidateBranch()` function in the engine:

1. **Discovery**: `DiscoverChanges(main, branch)` finds all new and modified artifacts on the planning run's branch
2. **Individual validation**: each discovered artifact is validated for schema correctness, required fields, and ID format
3. **Aggregate result**: if all artifacts pass, the step outcome is `valid`; if any fail, the outcome is `invalid` with per-artifact error details

This supports two artifact addition paths:
- **API path**: artifacts added via `POST /artifacts/add` during the draft step
- **Git-native path**: actors create artifact files directly on the branch (e.g., AI agents writing markdown files)

The validation step is agnostic to how artifacts arrived on the branch — it validates whatever `DiscoverChanges` finds.

---

## 6. Data Access

### 6.1 How the Validation Service Reads Artifacts

The Validation Service must read artifact content and metadata to evaluate rules. It accesses artifacts through:

| Data Need | Source | Rationale |
|-----------|--------|-----------|
| Artifact front matter (current state) | Projection Store | Fast access to parsed metadata; acceptable for validation |
| Artifact existence check | Projection Store | Fast lookup by path |
| Artifact content (if needed for scope rules) | Artifact Service (Git) | Authoritative content from Git |
| Link graph traversal | Projection Store | Efficient graph queries across artifacts |

### 6.2 Staleness Tolerance

Validation reads from the Projection Store, which may be slightly behind Git HEAD. This is acceptable because:

- Validation is a governance check, not a real-time consistency guarantee
- The Projection Service syncs on artifact changes, so lag is typically minimal
- For critical validation (e.g., during activation), the Validation Service may request a Projection sync before validating

### 6.3 Validation During Projection Rebuild

If the Projection Store is being rebuilt, validation should:

- Wait for the rebuild to complete if invoked from a workflow precondition
- Return an error indicating temporary unavailability if invoked as a system operation

### 6.4 Evidence Resolvers (EV-* rules)

The EV-* execution-evidence rules read state outside the projection store. The engine accepts four resolver types as construction-time options (see `WithRunResolver` / `WithEvidenceResolver` / `WithValidationPolicyResolver` / `WithBranchTipResolver` in `internal/validation/engine.go`):

| Resolver | Returns | Used by | Behavior when nil |
|----------|---------|---------|-------------------|
| `RunResolver` | `*Run` for a Task path | every EV-* rule | every EV-* rule is a no-op |
| `EvidenceResolver` | `*ExecutionEvidence` per `(run_id, repository_id)` | every EV-* rule | every EV-* rule is a no-op |
| `ValidationPolicyResolver` | applicable policies per `(task, repository)` | EV-003, EV-004 | EV-003 / EV-004 are no-ops; EV-001 / EV-002 / EV-005 still run |
| `BranchTipResolver` | live head SHA per `(repository, branch)` | EV-005 | EV-005 is a no-op; other EV-* rules still run |

Resolver errors are surfaced via `observe.Logger` and degrade gracefully:

- A `RunResolver` error skips evidence rules for that artifact (no false-positive blocks during transient errors).
- An `EvidenceResolver` error in EV-001 is treated as "evidence missing" — the producer side is the authority, so a read-side outage cannot let publish proceed.
- `ValidationPolicyResolver` and `BranchTipResolver` errors silently skip the affected check (their absence already represents an unwired surface).

Production wiring of these resolvers lands in subsequent EPIC-006 tasks (TASK-005 evidence query / reporting wires the evidence + policy resolvers; the branch-tip resolver follows the existing `git.GitClient` plumbing). Until then the rule slots exist and tests provide stub resolvers.

---

## 7. Extensibility

### 7.1 Adding New Rules

New validation rules are added by:

1. Defining the rule in this document with an ID, description, applicable types, and severity
2. Implementing the rule in the Validation Service
3. Existing workflows with `cross_artifact_valid` preconditions automatically pick up new rules

Rules should not be added silently. New rules that could block workflow execution (severity: Error) should be announced and documented before activation.

### 7.2 Custom Validation Rules

The `custom` condition type (per [Workflow Definition Format](/architecture/workflow-definition-format.md) §5.2) allows workflow-specific validation logic. Custom rules:

- Are defined as named functions registered with the Validation Service
- Receive the same validation request context as built-in rules
- Return the same validation result format
- Are invoked by name from workflow preconditions:

```yaml
preconditions:
  - type: custom
    function: validate_architecture_alignment
```

Custom rules are an extension point for domain-specific validation that doesn't belong in the core rule set.

### 7.3 AI-Assisted Validation

In future versions, scope alignment rules (§3.5) may leverage AI actors to assess whether artifact content aligns with governed context. This would:

- Use the Actor Gateway to invoke an AI agent with the artifact and its context
- Return a structured validation result
- Be classified as a warning (not error) since AI assessments are non-deterministic

This is out of scope for v0.x but the validation contract is designed to support it.

---

## 8. Constitutional Alignment

| Principle | How the Validation Service Supports It |
|-----------|---------------------------------------|
| Cross-Artifact Validation (§11) | Implements the mandatory validation checks with concrete rules and classifications |
| Structural Integrity (§12) | Ensures hierarchical consistency, link integrity, and scope alignment across all artifact layers |
| Governed Execution (§4) | Validation is embedded in workflows via preconditions, not ad-hoc |
| Reproducibility (§7) | Validation results are structured, logged, and emitted as events for traceability |

---

## 9. Cross-References

- [Constitution](/governance/constitution.md) §11 — Cross-Artifact Validation, §12 — Structural Integrity
- [System Components](/architecture/components.md) §4.8 — Validation Service component
- [Workflow Definition Format](/architecture/workflow-definition-format.md) §5.2 — `cross_artifact_valid` and `custom` conditions
- [Artifact Schema](/governance/artifact-schema.md) — Front matter schemas and link model
- [Access Surface](/architecture/access-surface.md) §3.5 — `system.validate_all` operation
- [Workflow Validation](/architecture/workflow-validation.md) — Workflow-specific validation suite (distinct from this service)
- [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) — Workflow definitions as a separate API resource
- [Observability](/architecture/observability.md) §4 — Audit trail for validation events
- [Error Handling](/architecture/error-handling-and-recovery.md) — Step failure when validation blocks execution

---

## 10. Evolution Policy

This validation specification is expected to evolve as the system is implemented and governance patterns emerge.

Areas expected to require refinement:

- Additional structural integrity rules as new artifact types are introduced
- AI-assisted scope alignment validation
- Performance optimization for large repositories (incremental validation, caching)
- Validation rule versioning (rules may change; in-progress Runs should use the rules active at Run creation)
- Validation bypass for emergency workflows (with audit trail)

New rules should be added to this document before implementation. Changes that alter the validation contract or severity classifications should be captured as ADRs.
