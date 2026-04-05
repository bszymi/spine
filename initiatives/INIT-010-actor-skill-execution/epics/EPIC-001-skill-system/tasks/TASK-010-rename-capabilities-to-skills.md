---
id: TASK-010
type: Task
title: "Rename all capabilities references to skills across codebase"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-009-filter-deprecated-skills-and-doc-fixes.md
---

# TASK-010 — Rename All Capabilities References to Skills Across Codebase

---

## Purpose

The system now uses skills exclusively, but the codebase still uses "capabilities" terminology in struct fields, function names, YAML tags, workflow definitions, API spec, and architecture docs. This creates confusion — readers encounter "capabilities" and "skills" referring to the same concept.

This task completes the vocabulary unification: everything becomes "skills".

---

## Deliverable

### Go Code Renames

1. **Domain model** (`internal/domain/workflow.go`):
   - `ExecutionConfig.RequiredCapabilities` → `RequiredSkills`
   - JSON/YAML tag `required_capabilities` → `required_skills`

2. **Actor selection** (`internal/actor/selection.go`):
   - `SelectionRequest.RequiredCapabilities` → `RequiredSkills`
   - `actorHasCapabilities()` → `actorHasSkills()`
   - Update all comments referencing "capability/capabilities" → "skill/skills"

3. **Actor service** (`internal/actor/service.go`):
   - `ValidateSkillEligibility` param `requiredCapabilities` → `requiredSkills`

4. **Workflow validation** (`internal/workflow/capability_validation.go`):
   - Rename file to `skill_validation.go`
   - `ValidateCapabilities()` → `ValidateSkills()`
   - Update rule ID `capability_registry` → `skill_registry`
   - Update warning messages

5. **Tests**: Update all `RequiredCapabilities:` field references in:
   - `internal/actor/actor_test.go`
   - `internal/workflow/capability_validation_test.go` (rename to `skill_validation_test.go`)

### Workflow YAML Renames

6. **Workflow definitions** (`workflows/*.yaml`):
   - `required_capabilities` → `required_skills` in all step execution blocks

7. **Workflow parser** (`internal/workflow/parser.go`):
   - Update any validation that references `required_capabilities` field name

### API Spec

8. **API spec** (`api/spec.yaml`):
   - `required_capability` → `required_skill`

### Architecture Docs

9. Update all architecture documents to use "skills" instead of "capabilities" when referring to the actor skill system:
   - `actor-model.md` — replace "capabilities" language with "skills" throughout
   - `domain-model.md` — update `required capabilities` references
   - `workflow-definition-format.md` — `required_capabilities` → `required_skills`
   - `workflow-validation.md` — update field references and §7.3 title
   - `validation-service.md` — update rule descriptions
   - `security-model.md` — update §4.6 to reference skills
   - `event-schemas.md` — update `required_capabilities` in event payloads
   - `error-handling-and-recovery.md` — update `required_capabilities` references
   - `access-surface.md` — update where relevant
   - `api-operations.md` — update descriptions
   - `runtime-schema.md` — update comments

Note: "capabilities" used in non-skill contexts (e.g. "dashboard capabilities", "Git capabilities") should NOT be renamed — only references to the actor skill/capability matching system.

---

## Acceptance Criteria

- No Go code uses "Capabilities" or "capabilities" to refer to the skill system
- Workflow YAML field is `required_skills`
- All architecture docs use "skills" terminology for the actor skill system
- `go build ./...` passes
- `go test ./...` passes
- All workflow YAML files parse correctly with the new field name
- grep for `required_capabilities` in Go code returns zero results
- grep for `RequiredCapabilities` in Go code returns zero results
