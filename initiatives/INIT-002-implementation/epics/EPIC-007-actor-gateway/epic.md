---
id: EPIC-007
type: Epic
title: Actor Gateway
status: Pending
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
---

# EPIC-007 — Actor Gateway

---

## Purpose

Build the Actor Gateway — the component that delivers step assignments to actors and collects results. After this epic, steps can be assigned to and completed by human, AI, and automated actors.

---

## Validates

- [Actor Model](/architecture/actor-model.md) — Registration, selection, gateway protocol
- [Security Model](/architecture/security-model.md) §3.4 — Service accounts
- [System Components](/architecture/components.md) §4.6 — Actor Gateway responsibilities

---

## Acceptance Criteria

- Actors can be registered with type, role, and capabilities
- Actor selection algorithm filters by type, capability, role, availability
- Step assignment request schema matches Actor Model §5.2
- Step result response schema matches Actor Model §5.3
- Response validation rejects invalid/unauthorized results
- AI agent integration works with at least one provider (Anthropic or OpenAI)
- Actor lifecycle (active, suspended, deactivated) is enforced
- Unit tests for selection algorithm; integration tests for assignment flow
