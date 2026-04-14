---
id: TASK-008
type: Task
title: "Event replay and reconstruction validation"
status: Pending
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
  - type: blocked_by
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-007-resilience-testing/epic.md
---

# TASK-008 — Event replay and reconstruction validation

---

## Purpose

Events are emitted as a side effect of all domain operations but no scenario verifies that replaying the event log produces the same final state as direct service calls. The Git-as-source-of-truth guarantee covers artifacts but the event log is the authoritative record for run and step transitions — this is untested.

## Deliverable

Scenario tests covering:

- **Event log completeness**: run a golden-path workflow (start → step → result → merge); capture all emitted events; verify that the event types and ordering match the expected sequence for the workflow definition
- **State from events matches state from Git**: after a full workflow completes, compare the run/step state derived from the event log against the state derived from Git artifacts; they must agree
- **No phantom events**: cancelled runs emit a cancellation event but no subsequent step events; verify the event log does not contain step-completion events after the run is cancelled
- **Event ordering invariant**: events for a single run are always emitted in monotonically increasing sequence; verify no events arrive out-of-order for a given run ID

## Acceptance Criteria

- Event sequence for a completed workflow matches the expected type sequence
- State derived from events agrees with state derived from Git
- Cancelled run: no step events after cancellation event
- Event sequence numbers within a run are strictly increasing
