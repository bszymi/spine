---
id: INIT-012
type: Initiative
title: Unified Actor API for Direct Communication
status: Pending
owner: bszymi
created: 2026-04-14
links:
  - type: related_to
    target: /architecture/adr/ADR-006-planning-runs.md
---

# INIT-012 — Unified Actor API for Direct Communication

---

## Purpose

The Spine Management Platform has adopted a unified actor model (ADR-003) where automated systems (runners) and AI agents are first-class actors that communicate with Spine directly (ADR-005). Spine needs three API capabilities to support this:

1. **Actor registration** — platform creates actors in Spine during runner/agent registration
2. **Step-execution query** — actors poll for steps assigned to them within active runs
3. **Per-actor step assignment** — enforce which specific actor can claim a step

## Motivation

Platform runners currently cannot:
- Register as actors in Spine (no HTTP endpoint, only internal service method)
- Discover steps waiting for them in active runs (candidates API returns tasks, not step executions)
- Be exclusively assigned to specific workflow steps (only actor type filtering, not actor ID)

## Epics

1. **EPIC-001 — Actor API Endpoints**: actor registration, step-execution query, per-actor assignment
