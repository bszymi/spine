---
id: TASK-008
type: Task
title: Production Binary Wiring
status: In Progress
epic: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
---

# TASK-008 — Production Binary Wiring

## Purpose

Connect all implemented interfaces and services in the production binary (cmd/spine/main.go) so that features are actually usable in deployed environments, not just in tests.

## Deliverable

- Wire DivergenceHandler into engine orchestrator in serve command
- Wire ConvergenceHandler into engine orchestrator
- Wire CrossArtifactValidator into engine orchestrator
- Wire BranchCreator into gateway ServerConfig
- Wire EventEmitterGW into gateway ServerConfig
- Wire StepRecoveryFunc and RunFailFunc into scheduler
- Wire workflow timeout into gateway run creation (ResolvedWorkflow.Timeout)
- Verify all optional interfaces are connected when services are available

## Acceptance Criteria

- All engine optional handlers wired in production serve command
- Gateway receives all optional services
- Scheduler recovery functions connected to engine orchestrator
- Divergence API endpoints functional in production
- Validation events emitted from production system.validate endpoint
- Run-level timeout works end-to-end in production
