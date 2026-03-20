---
id: TASK-007
type: Task
title: Observability Foundation
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-001-core-foundation/epic.md
---

# TASK-007 — Observability Foundation

## Purpose

Implement the foundational observability infrastructure: structured logging, trace ID propagation, and metrics scaffolding.

## Deliverable

- Structured JSON logging using `slog` (per Observability §5)
- Standard log fields: timestamp, level, component, message, run_id, trace_id, step_id, actor_id, artifact_path
- Trace ID generation and propagation through context (per Observability §3)
- Trace ID injection into Git commit trailers (per Git Integration §5.1)
- Log level configuration via `SPINE_LOG_LEVEL`
- Metrics counter scaffolding for future instrumentation (per Observability §7)

## Acceptance Criteria

- All components emit structured JSON logs to stdout
- Trace ID is generated at request entry and propagated through all service calls
- Trace ID appears in log entries, event payloads, and Git commit trailers
- Log levels (debug, info, warn, error) filter correctly
- Log output matches the format defined in Observability §5.1
- Unit tests verify trace propagation through context
