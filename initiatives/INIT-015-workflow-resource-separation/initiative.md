---
id: INIT-015
type: Initiative
title: Workflow Resource Separation
status: Completed
owner: bszymi
created: 2026-04-17
links:
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
  - type: related_to
    target: /architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md
  - type: related_to
    target: /architecture/workflow-definition-format.md
  - type: related_to
    target: /architecture/workflow-validation.md
---

# INIT-015 — Workflow Resource Separation

---

## Purpose

Workflow definitions are executable schemas whose structural invariants are materially stricter than those of any other artifact type. Today they are writable through the generic `artifact.create` / `artifact.update` / `artifact.add` endpoints, which means workflow-specific validation is a conditional branch inside a general-purpose code path and malformed workflows surface only at Run execution time.

This initiative implements [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) by promoting workflow definitions to a dedicated API resource with its own create/update/read/list/validate operations, and removing the ability to write workflow definitions through the generic artifact endpoints.

## Motivation

- A malformed workflow definition blocks Runs at execution time — the most expensive failure mode. Splitting the resource shifts failure to write time.
- The [Workflow Validation](/architecture/workflow-validation.md) suite (step-reference integrity, cycle detection, divergence/convergence balance, actor/skill resolution) has no natural home in the generic artifact write path.
- Workflow files are pure YAML with no Markdown front matter and share no structural schema with Initiative / Epic / Task / ADR / Document artifacts, so the shared endpoint is convenience, not coherence.

## Scope

### In Scope

- **EPIC-001 — Workflow API Separation**: new `workflow.*` operations, generic artifact endpoints reject workflow writes, documentation and OpenAPI spec updates, handler implementation, CLI surface update.

### Out of Scope

- Workflow versioning / supersession semantics beyond what is already defined in [Workflow Definition Format](/architecture/workflow-definition-format.md). Follow-up work under ADR-007 §Future Work.
- Changes to the Runtime Workflow Engine, Run binding resolution, or execution recording model.
- Migration tooling for any workflow artifacts in historical Git state written through the generic endpoints (backwards compatibility is explicitly not a requirement).

## Success Criteria

1. Workflow definitions can only be created and updated through the dedicated `workflow.*` operations.
2. Generic artifact endpoints reject workflow targets with `400 invalid_params` pointing to the correct operation.
3. `artifact.read` against a workflow path returns summary metadata only; executable bodies are served exclusively by `workflow.read`.
4. The OpenAPI spec, `api-operations.md`, `access-surface.md`, `validation-service.md`, and `artifact-schema.md` all reflect the split, with no remaining references to workflow writes via generic artifact operations.
5. Existing Runs and workflow execution are unaffected.

## Primary Artifacts Produced

- New `workflow.*` handlers in `internal/gateway/`
- Updated generic artifact handlers with workflow-path rejection
- Updated OpenAPI spec (`api/spec.yaml`)
- Updated architecture docs (`api-operations.md`, `access-surface.md`, `validation-service.md`)
- Updated governance doc (`artifact-schema.md`)
- CLI surface updated to target the new endpoints

## Risks

- **Double-booked validation code paths:** if the workflow validation suite is not cleanly extracted, rejection logic may leak workflow concepts into generic artifact handlers.
- **Incomplete rejection:** a workflow path slipping through the generic handlers silently re-opens the failure mode the initiative is meant to close.

### Mitigations

- Extract workflow-path detection into a single helper used by all generic artifact handlers; covered by targeted rejection tests.
- Integration test that asserts every generic artifact write operation rejects `/workflows/*` targets.

## Exit Criteria

INIT-015 may be marked complete when:

- All EPIC-001 tasks are Completed.
- ADR-007 is referenced as the governing decision and Accepted.
- Handler tests and integration tests for rejection and new workflow operations pass.
