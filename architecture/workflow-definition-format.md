---
type: Architecture
title: Workflow Definition Format
status: Living Document
version: "0.1"
---

# Workflow Definition Format

---

## 1. Purpose

This document defines the concrete format for workflow definitions in Spine.

Workflow definitions are versioned Git artifacts (per [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md)) that describe how a type of work progresses through states. The [Domain Model](/architecture/domain-model.md) (§3.2, §3.3) defines Workflow Definition and Step Definition as core entities. This document specifies the file format that makes those entities concrete and machine-parseable.

---

## 2. File Format

### 2.1 Format Choice

Workflow definitions use **YAML** as their file format, consistent with how artifact front matter is stored throughout Spine.

YAML is chosen because:

- Already used for artifact metadata across the repository
- Human-readable and editable without special tooling
- Machine-parseable with standard libraries
- Supports the nested structures required for step definitions
- Diffable in Git — workflow changes are reviewable

### 2.2 File Location

Workflow definitions are stored in the repository under:

```
/workflows/<workflow-id>.yaml
```

Example:

```
/workflows/task-execution.yaml
/workflows/adr-review.yaml
/workflows/artifact-creation.yaml
```

### 2.3 Front Matter

Workflow definition files do not use Markdown with YAML front matter. They are pure YAML files. Metadata fields (id, version, status) are top-level keys in the YAML document.

---

## 3. Schema

### 3.1 Top-Level Structure

```yaml
id: <string>              # Stable workflow identifier
name: <string>            # Human-readable workflow name
version: <string>         # Semantic version of this definition
status: <enum>            # Active, Deprecated, Superseded
description: <string>     # What this workflow governs

applies_to:               # What artifact types this workflow applies to
  - <artifact_type>

entry_step: <step_id>     # Step where execution begins

steps:                    # Workflow steps (runtime execution structure)
  - <step_definition>

divergence_points:        # Optional: definitions of parallel branch starts
  - <divergence_definition>

convergence_points:       # Optional: definitions of branch joins
  - <convergence_definition>
```

### 3.2 Step Definition

Steps describe what must happen at each stage of execution. Each step corresponds to a state or transition within the workflow.

```yaml
steps:
  - id: <string>                   # Unique step identifier within workflow
    name: <string>                 # Human-readable step name
    type: <enum>                   # manual, automated, review, convergence

    execution:
      mode: <enum>                 # automated_only, ai_only, human_only, hybrid
      eligible_actor_types:
        - <enum>                   # human, ai_agent, automated_system
      required_capabilities:       # Optional: capabilities needed to execute
        - <string>

    preconditions:                 # Optional: must be true before step starts
      - <condition>

    required_inputs:               # Optional: artifacts or data needed
      - <input_ref>

    required_outputs:              # Optional: artifacts or data that must be produced
      - <output_ref>

    validation:                    # Optional: rules checked before accepting result
      - <validation_rule>

    outcomes:                      # Possible results of this step
      - id: <string>
        name: <string>
        next_step: <step_id|end>   # Step to activate next or 'end' to terminate
        commit:                    # Optional: durable artifact mutation
          status: <string>

    retry:                         # Optional: retry configuration
      limit: <integer>
      backoff: <string>            # fixed, linear, exponential

    timeout: <duration>            # Optional: maximum duration
    timeout_outcome: <string>      # Outcome applied on timeout

    diverge: <divergence_ref>      # Optional: reference to divergence point
    converge: <convergence_ref>    # Optional: reference to convergence point
```

**Step types:**

| Type | Meaning |
|------|---------|
| `manual` | Requires human actor interaction |
| `automated` | Executed by AI or automation — must declare retry limits |
| `review` | Evaluation step — actor reviews artifacts or outcomes |
| `convergence` | Evaluates parallel branch results and selects outcome |

### 3.3 Divergence and Convergence

Divergence and convergence points are declared as named constructs referenced by steps.

**Structured divergence** (static branches declared upfront):

```yaml
divergence_points:
  - id: <string>                   # Unique divergence identifier
    name: <string>
    mode: structured               # Default; branches are predefined
    branches:                      # Parallel branches to create
      - id: <string>
        name: <string>
        start_step: <step_id>      # Which step each branch starts at
```

**Exploratory divergence** (dynamic branches created at runtime):

```yaml
divergence_points:
  - id: <string>                   # Unique divergence identifier
    name: <string>
    mode: exploratory              # Branches created at runtime
    branch_step: <step_id>         # Step template each new branch starts at
    min_branches: <integer>        # Minimum branches before convergence is allowed
    max_branches: <integer>        # Optional cap on branch count
```

**Convergence points:**

```yaml
convergence_points:
  - id: <string>                   # Unique convergence identifier
    name: <string>
    branches: [<branch_id>, ...]   # For structured; omit for exploratory
    strategy: <enum>               # select_one, select_subset, merge, require_all, experiment
    entry_policy: <policy>         # When convergence may begin (default: all_branches_terminal)
    evaluation_step: <step_id>     # Step that performs the evaluation
```

**Convergence strategies:**

| Strategy | Meaning |
|----------|---------|
| `select_one` | One branch outcome is selected; others are preserved but not applied |
| `select_subset` | A subset of branch outcomes is selected for further processing |
| `merge` | Multiple branch outcomes are combined into a single result |
| `require_all` | All branches must complete successfully before proceeding |
| `experiment` | Branch outcomes are packaged into a controlled experiment for data-driven evaluation |

**Convergence entry policies:**

| Policy | Meaning |
|--------|---------|
| `all_branches_terminal` | Default — all branches must complete or fail |
| `minimum_completed_branches` | Convergence may begin after a minimum number of branches complete |
| `deadline_reached` | Convergence begins after a defined time threshold |
| `manual_trigger` | An authorized actor explicitly starts convergence |

The detailed execution semantics for divergence and convergence are defined in the [Divergence and Convergence](/architecture/divergence-and-convergence.md) architecture document.

### 3.4 Applies-To Clause

The `applies_to` field declares which artifact types this workflow applies to. The Workflow Engine uses this to determine which workflow definition to activate when a Run is started for a given artifact.

The clause accepts either a simple string (matches all artifacts of that type) or a structured object with a `work_type` filter:

```yaml
# Governs all Tasks regardless of work_type
applies_to:
  - Task

# Governs only Tasks with work_type: spike
applies_to:
  - type: Task
    work_type: spike

# Governs Tasks with work_type: implementation or bugfix
applies_to:
  - type: Task
    work_type: [implementation, bugfix]
```

An artifact type may be governed by at most one active workflow definition at a time per `work_type` value. A general workflow (no `work_type` filter) and a specific workflow (with `work_type` filter) may coexist for the same artifact type, as long as they do not overlap on the same `work_type` value.

The full resolution algorithm and precedence rules are defined in the [Task-to-Workflow Binding Model](/architecture/task-workflow-binding.md).

---

## 4. Versioning

### 4.1 Version Field

Each workflow definition includes a `version` field. Versions follow a simple major.minor scheme:

- **Major version change** — incompatible structural change (states removed, steps reordered, transitions altered)
- **Minor version change** — backward-compatible addition (new optional steps, additional outcomes)

### 4.2 Version Resolution

When the Workflow Engine starts a Run, it resolves the workflow definition version as follows:

1. Read the active workflow definition for the governed artifact type
2. Pin the Run to the current version (recorded in the Run's `workflow_version` field per the [Data Model](/architecture/data-model.md))
3. The Run uses this pinned version for its entire lifetime — it does not migrate to newer versions mid-execution

### 4.3 Version Lifecycle

| Status | Meaning |
|--------|---------|
| `Active` | Current version — new Runs use this version |
| `Deprecated` | Still valid for in-progress Runs; new Runs should use a newer version |
| `Superseded` | Replaced by a successor; no new Runs permitted |

Only one version of a workflow for a given artifact type may be `Active` at a time.

---

## 5. Conditions and Validation Rules

### 5.1 Condition Format

Preconditions, transition conditions, and validation rules use a declarative expression format:

```yaml
preconditions:
  - type: artifact_status
    artifact: parent_epic
    status: In Progress

  - type: field_present
    field: acceptance_criteria

  - type: links_exist
    link_type: parent
```

The `cross_artifact_valid` condition invokes the standard cross-artifact validation
rules for the current artifact in its governed context (for example: parent
relationships, linked artifacts, or structural integrity checks). In its basic
form it requires no additional parameters and delegates validation to the
Validation Service.

### 5.2 Condition Types

| Type | Meaning |
|------|---------|
| `artifact_status` | Referenced artifact must be in the specified status |
| `field_present` | Specified front matter field must exist and be non-empty |
| `field_value` | Specified front matter field must have the given value |
| `links_exist` | Artifact must have at least one link of the specified type |
| `cross_artifact_valid` | Invokes the Validation Service to validate the artifact against its governed context (linked artifacts, structural rules, or parent relationships) |
| `custom` | Custom validation function (for extensibility) |

### 5.3 Inputs and Outputs

`required_inputs` and `required_outputs` define logical workflow data references.
They do not represent raw file paths or repository locations. Instead they refer
to named artifacts, deliverables, or data objects that are consumed or produced
by a step during execution.

The Workflow Engine resolves these references at runtime by binding them to
concrete artifacts, files, or structured outputs generated by earlier steps.
This allows workflow definitions to remain stable even if the underlying storage
structure evolves.

---

## 6. Example: Task Execution Workflow

Note: This execution workflow does not necessarily expose every governed terminal
status defined for Task artifacts. Administrative outcomes such as `Superseded`
or `Abandoned` may occur through governance actions outside the normal execution
workflow and therefore are not required to appear as step outcomes here.

```yaml
id: task-execution
name: Task Execution
version: "1.0"
status: Active
description: Standard workflow for executing a Task artifact from pending to terminal outcome.

applies_to:
  - Task

entry_step: assign

steps:
  - id: assign
    name: Assign Actor
    type: automated
    required_outputs:
      - actor_assignment
    outcomes:
      - id: assigned
        name: Actor Assigned
        next_step: execute
        commit:
          status: In Progress
      - id: assignment_timeout
        name: Assignment Timed Out
        next_step: end
    retry:
      limit: 3
      backoff: exponential
    timeout: "24h"
    timeout_outcome: assignment_timeout

  - id: execute
    name: Execute Work
    type: manual
    preconditions:
      - type: artifact_status
        artifact: self
        status: In Progress
    required_outputs:
      - deliverable
    outcomes:
      - id: submitted
        name: Work Submitted for Review
        next_step: review
      - id: cancelled
        name: Work Cancelled
        next_step: end
    timeout: "30d"

  - id: review
    name: Review Deliverable
    type: review
    preconditions:
      - type: cross_artifact_valid
    required_inputs:
      - deliverable
    outcomes:
      - id: accepted
        name: Deliverable Accepted
        next_step: end
        commit:
          status: Completed
      - id: needs_rework
        name: Needs Rework
        next_step: execute
      - id: rejected
        name: Deliverable Rejected
        next_step: end
        commit:
          status: Rejected
      - id: review_timeout
        name: Review Timed Out
        next_step: end
    timeout: "7d"
    timeout_outcome: review_timeout
```

---

## 7. Relationship to Runtime

Workflow definitions are static declarations. At runtime:

- The **Workflow Engine** parses the definition and enforces its rules
- **Runs** are created from a pinned workflow version
- **Step executions** are runtime instances of Step Definitions

Step outcomes determine which step executes next and whether durable artifact
mutations are committed to Git. Workflow execution state lives entirely in the
runtime store, while durable artifact status changes occur only when an
outcome declares a `commit` effect.

Multiple outcomes may route to the same next step, allowing loops and rework
cycles within a Run. When a step is re-entered, the Workflow Engine creates a
new execution attempt for that step while preserving the Run history.

---

## 8. Cross-References

- [Domain Model](/architecture/domain-model.md) — Workflow Definition (§3.2), Step Definition (§3.3)
- [ADR-001](/architecture/adr/ADR-001-workflow-definition-storage-and-execution-recording.md) — Workflow definitions stored in Git, durable outcomes only
- [Data Model](/architecture/data-model.md) — Runtime Store schema for Runs and step executions
- [System Components](/architecture/components.md) — Workflow Engine (§4.3)
- [Task Lifecycle](/governance/task-lifecycle.md) — Governed states vs runtime states
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) — Parallel execution model
- [Workflow Validation](/architecture/workflow-validation.md) — Validation rules and lifecycle

---

## 9. Evolution Policy

This format is expected to evolve as workflows are implemented and operational experience is gained.

Changes to the workflow definition format should be versioned and backward-compatible where possible. Breaking changes should be captured as ADRs.

New condition types and step types may be introduced without changing the core schema structure.
