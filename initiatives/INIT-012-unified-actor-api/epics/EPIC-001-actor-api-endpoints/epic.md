---
id: EPIC-001
type: Epic
title: "Actor API Endpoints"
status: Pending
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/initiative.md
---

# EPIC-001 — Actor API Endpoints

---

## Purpose

Add HTTP endpoints that the Spine Management Platform needs to support the unified actor model. These enable automated actors (runners, AI agents) to register in Spine and poll for work directly.

---

## Key Work Areas

- Actor registration HTTP endpoint
- Step-execution query filtered by actor
- Per-actor step assignment via eligible_actor_ids

---

## Acceptance Criteria

- Platform can register automation actors via POST /api/v1/actors
- Actors can query for steps assigned to them via GET /api/v1/execution/steps
- Steps can be restricted to specific actors via eligible_actor_ids
