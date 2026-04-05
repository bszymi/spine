---
type: Architecture
title: Actor Model
status: Living Document
version: "0.1"
---

# Actor Model

---

## 1. Purpose

This document defines the actor model for Spine at v0.x — how actors are registered, configured, selected for step execution, and how they interact with the system through the Actor Gateway.

The [Domain Model](/architecture/domain-model.md) (§3.4) defines Actor as a core entity. The [System Components](/architecture/components.md) (§4.6) defines Actor Gateway as the uniform interface. The [Security Model](/architecture/security-model.md) defines authentication, authorization, and capabilities. This document makes the operational aspects concrete — how actors are managed, how the system selects actors for steps, and the protocol actors follow when executing work.

---

## 2. Actor Types

Spine recognizes three actor types (per Constitution §5):

| Type | Description | Examples |
|------|-------------|---------|
| `human` | A person interacting through CLI, API, or GUI | Developer, product manager, reviewer, architect |
| `ai_agent` | An AI system that receives prompts and returns structured results | LLM-based code generator, AI reviewer, summarization agent |
| `automated_system` | A deterministic system that executes predefined operations | CI/CD pipeline, linter, test runner, migration script |

**Key distinction:** AI agents are non-deterministic — they may produce different outputs given the same input. Automated systems are deterministic or near-deterministic. This distinction matters for reproducibility (Constitution §7) but does not affect governance — all actor types operate under identical workflow constraints.

---

## 3. Actor Registration

### 3.1 Actor Records

Actors are registered in runtime configuration (not in Git). Each actor has a record containing:

```yaml
actor_id: <string>              # Stable unique identifier
name: <string>                  # Human-readable name
type: <enum>                    # human, ai_agent, automated_system
role: <enum>                    # reader, contributor, reviewer, operator, admin
capabilities:                   # Domain-specific execution capabilities
  - <string>
status: <enum>                  # active, suspended, deactivated
created_at: <timestamp>
```

Capabilities are defined at the system level (not per actor) and represent a shared vocabulary used by workflow definitions and actor configuration.

Actors declare which capabilities they possess; workflows declare which capabilities are required. The Workflow Engine performs matching based on these values.

#### Skill Registry

Capabilities are formalized through the **Skill** entity (`auth.skills` table). A Skill is a workspace-scoped, first-class entity with:

- `skill_id` — unique identifier
- `name` — unique within workspace, matches against `required_capabilities` in workflow steps
- `description` — human-readable explanation
- `category` — grouping (e.g. "development", "review", "operations")
- `status` — active or deprecated

When `required_capabilities` values on a workflow step match registered skill names, the system resolves them through the skill registry. When no matching skill entity exists, bare string matching against the actor's `capabilities` field is used as a fallback. This provides backward compatibility during migration from opaque capability strings to formal skills.

#### Actor-Skill Associations

Skills are assigned to actors via a many-to-many relationship (`auth.actor_skills` junction table). The actor service provides:

- `AddSkill(actorID, skillID)` — assign a skill to an actor
- `RemoveSkill(actorID, skillID)` — remove a skill from an actor
- `ListSkills(actorID)` — list all skills assigned to an actor

During actor selection, if an actor has skills assigned, the skill-based matching takes precedence over the legacy `capabilities` field. If no skills are assigned, the system falls back to the legacy field.

### 3.2 Registration Rules

- Every actor must be registered before interacting with the system
- Actor IDs are stable and never reused
- Actor type is set at registration and cannot be changed (create a new actor record instead)
- Role and capabilities may be updated by an admin
- Deactivated actors cannot authenticate or be assigned to steps

### 3.3 Why Actors Are Not Git Artifacts

Actor records are runtime configuration, not governed artifacts, because:

- Actor credentials are attached to actor records and must never be in Git (per [Security Model](/architecture/security-model.md) §5)
- Actor availability and status change frequently (operational concern, not governance)
- The set of actors is an operational deployment detail, not a product definition

However, actor identity is preserved in Git through commit attribution — every commit records which actor produced it. This provides durable auditability without storing actor records in Git.

---

## 4. Actor Selection

### 4.1 Selection Context

When a step is ready for execution, the Workflow Engine must select an actor to assign it to. The selection process uses the step's execution constraints from the workflow definition:

```yaml
execution:
  mode: <enum>                   # automated_only, ai_only, human_only, hybrid
  eligible_actor_types:
    - <enum>                     # human, ai_agent, automated_system
  required_capabilities:
    - <string>                   # e.g., architecture_review, code_generation
```

### 4.2 Selection Algorithm

The Workflow Engine selects an actor through the following process:

1. **Filter by type** — restrict candidates to actors matching `eligible_actor_types`
2. **Filter by capability** — restrict candidates to actors possessing all `required_capabilities`
3. **Filter by role** — restrict candidates to actors whose role grants sufficient permissions for the operation
4. **Filter by availability** — restrict candidates to actors with `active` status
5. **Select** — from the eligible set, select an actor using the configured selection strategy

When multiple eligible actors are available, the selection strategy may consider operational factors such as cost, latency, reliability, or historical performance. These factors are not part of workflow definitions and are configured at the Workflow Engine level.

### 4.3 Selection Strategies

| Strategy | Behavior | Use Case |
|----------|----------|----------|
| `explicit` | A specific actor is named in the step assignment request | Manual assignment by operators or workflow rules |
| `any_eligible` | Any actor from the eligible set may be assigned | Default for automated steps |
| `round_robin` | Distribute assignments evenly across eligible actors | Load balancing for high-volume steps |

The selection strategy is not declared in the workflow definition — it is an operational configuration of the Workflow Engine. Workflow definitions declare constraints (who *may* execute); the engine decides who *will* execute.

### 4.4 Assignment Failures

If no eligible actor exists for a step:

- The step remains in `waiting` status
- An operational event (`step_assignment_failed`) is emitted
- The Workflow Engine retries assignment periodically or when actor availability changes
- If the step has a timeout, the timeout applies from when the step became ready, not from when assignment succeeds

If assignment to a selected actor fails repeatedly, the Workflow Engine may attempt reassignment to another eligible actor before failing the step.

---

## 5. Actor Gateway Protocol

### 5.1 Purpose

The Actor Gateway provides a uniform interface between the Workflow Engine and actors. It abstracts the differences between actor types so that the engine does not need actor-type-specific logic.

### 5.2 Step Assignment Request

When the Workflow Engine assigns a step to an actor, the Actor Gateway delivers a step assignment:

```yaml
assignment:
  assignment_id: <string>        # Unique assignment identifier
  run_id: <string>               # Associated Run
  trace_id: <string>             # Observability correlation
  step_id: <string>              # Step within the workflow
  step_name: <string>            # Human-readable step name
  step_type: <string>            # manual, automated, review, convergence
  actor_id: <string>             # Assigned actor

  context:                       # Information the actor needs to execute
    task_path: <string>          # Path to the governed task artifact
    workflow_id: <string>        # Governing workflow identifier
    required_inputs: [<string>]  # Artifact paths or data references
    required_outputs: [<string>] # Expected artifact paths or data
    instructions: <string|null>  # Step-specific instructions from workflow definition

  constraints:
    timeout: <duration|null>     # Maximum execution time
    expected_outcomes: [<string>] # Valid outcome IDs the actor may return
```

### 5.3 Step Result Response

When an actor completes a step, it returns a result through the Actor Gateway:

```yaml
result:
  assignment_id: <string>        # Must match the assignment
  run_id: <string>               # Must match the assignment
  trace_id: <string>             # Must match the assignment
  actor_id: <string>             # Must match the assigned actor

  outcome_id: <string>           # One of the expected_outcomes from the assignment
  output:                        # Step-specific output
    artifacts_produced: [<string>] # Paths of artifacts created or modified
    data: <object|null>          # Structured data output (step-specific)
    summary: <string|null>       # Human-readable summary of what was done
```

### 5.4 Response Validation

All actor responses are untrusted input (per [Security Model](/architecture/security-model.md) §6.3). The Actor Gateway and Workflow Engine validate:

- `assignment_id`, `run_id`, and `actor_id` match the active assignment
- `outcome_id` is one of the declared `expected_outcomes`
- Any artifacts referenced in `artifacts_produced` exist and conform to the artifact schema
- The response arrives within the step timeout
- Duplicate or replayed responses for the same assignment must be detected and handled idempotently

Invalid responses are rejected. The step may be retried or failed based on error classification (per [Error Handling](/architecture/error-handling-and-recovery.md)).

### 5.5 Delivery Mechanisms

The Actor Gateway uses different delivery mechanisms per actor type:

| Actor Type | Delivery | Response |
|------------|----------|----------|
| `human` | Notification (email, Slack, GUI task queue) | Human submits result through CLI, API, or GUI |
| `ai_agent` | API call to AI provider with structured prompt | Synchronous or callback-based response |
| `automated_system` | Webhook, message queue, or direct invocation | Synchronous or callback-based response |

The Actor Gateway is responsible for translating the uniform assignment format into the actor-type-specific delivery mechanism and translating the actor's response back into the uniform result format.

---

## 6. AI Actor Configuration

### 6.1 AI Agent Definition

AI agents are configured as actor records with additional configuration for their AI provider integration:

```yaml
actor_id: ai-code-reviewer
name: AI Code Reviewer
type: ai_agent
role: contributor
capabilities:
  - code_review
  - style_review
status: active

ai_config:
  provider: <string>             # e.g., anthropic, openai
  model: <string>                # e.g., claude-sonnet-4-6, gpt-4
  temperature: <float|null>      # Model temperature (null for provider default)
  max_tokens: <integer|null>     # Max response tokens
  system_prompt: <string|null>   # Base system prompt for this agent
```

### 6.2 Context Injection

When the Actor Gateway delivers a step assignment to an AI agent, it constructs a prompt that includes:

1. **System context** — the agent's base system prompt (from `ai_config`)
2. **Step instructions** — the `instructions` field from the workflow step definition
3. **Artifact content** — the content of `required_inputs` artifacts, retrieved from Git
4. **Constraints** — expected outcomes, output format requirements
5. **Governance context** — relevant workflow rules and acceptance criteria

The Actor Gateway is responsible for assembling this context. The Workflow Engine provides the structured assignment; the Actor Gateway translates it into an AI-provider-specific request.

### 6.3 Output Parsing

AI agent responses are unstructured text. The Actor Gateway must:

- Parse the response into the structured `result` format
- Extract the `outcome_id` from the response
- Extract any artifact content or structured data
- Handle parsing failures as transient errors (retry with clarification if possible)

### 6.4 Variability Declaration

AI agents are non-deterministic (Constitution §7 requires declaring variability boundaries). The system handles this through:

- **Workflow governance** — AI-produced artifacts pass through review steps before becoming durable outcomes
- **Step type declaration** — steps executed by AI agents declare their step type, making non-determinism explicit
- **Audit trail** — AI actor identity is recorded on commits, distinguishing AI-produced from human-produced artifacts

---

## 7. Actor Lifecycle

### 7.1 Status Transitions

```
active → suspended → active (re-enabled by admin)
active → deactivated (permanent, cannot be reactivated)
```

| Status | Can Authenticate | Can Be Assigned | Active Assignments |
|--------|-----------------|----------------|-------------------|
| `active` | Yes | Yes | Continue normally |
| `suspended` | No | No | In-progress assignments remain; no new assignments |
| `deactivated` | No | No | In-progress assignments are reassigned or failed |

### 7.2 Availability

Actor availability is tracked at the operational level:

- **Human actors** — available when authenticated and active; no heartbeat required
- **AI agents** — available when the underlying provider is reachable; the Actor Gateway may perform periodic health checks
- **Automated systems** — available when the endpoint is reachable; the Actor Gateway may perform periodic health checks

Availability affects actor selection (§4.2) but does not change actor status. An unreachable AI agent remains `active` — the assignment simply fails and is retried or reassigned.

Availability may include additional operational signals such as error rates, latency, or rate limiting status. These signals may influence actor selection but do not change actor status.

### 7.3 Concurrent Assignments

- An actor may be assigned to multiple steps concurrently
- The system does not enforce single-assignment constraints by default
- Workflow definitions may declare step-level constraints that limit concurrent assignments if needed (e.g., a human reviewer should not review their own work)

---

## 8. Constitutional Alignment

| Principle | How the Actor Model Supports It |
|-----------|--------------------------------|
| Actor Neutrality (§5) | All actor types share the same registration model, selection algorithm, gateway protocol, and governance constraints |
| Governed Execution (§4) | Actors can only execute steps assigned through workflows; responses are validated against workflow constraints |
| Reproducibility (§7) | AI variability is declared through step types; actor identity is preserved in commit attribution |
| Source of Truth (§2) | Actor records are runtime configuration; durable actor identity is in Git commit history |

---

## 9. Cross-References

- [Domain Model](/architecture/domain-model.md) §3.4 — Actor entity definition
- [System Components](/architecture/components.md) §4.6 — Actor Gateway component
- [Security Model](/architecture/security-model.md) §3 — Authentication, §4 — Authorization and capabilities
- [Workflow Definition Format](/architecture/workflow-definition-format.md) §3.2 — Step execution block
- [Error Handling](/architecture/error-handling-and-recovery.md) — Step failure and retry handling
- [Observability](/architecture/observability.md) §3 — Trace ID propagation through actor requests
- [Event Schemas](/architecture/event-schemas.md) §4.4 — `step_assigned` event
- [Constitution](/governance/constitution.md) §5 — Actor Neutrality

---

## 10. Evolution Policy

This actor model is expected to evolve as the system is implemented and actor integration patterns emerge.

Areas expected to require refinement:

- Actor capability discovery and negotiation
- AI agent prompt engineering patterns and templates
- Actor performance metrics and quality tracking
- Multi-agent collaboration patterns
- Actor marketplace or registry for third-party integrations
- Conflict-of-interest rules (e.g., an actor cannot review their own work)

Changes that alter the Actor Gateway protocol, selection algorithm, or trust model should be captured as ADRs.
