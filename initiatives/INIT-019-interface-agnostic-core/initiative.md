---
id: INIT-019
type: Initiative
title: Interface-Agnostic Governance Core
status: Draft
owner: bszymi
created: 2026-04-18
links:
  - type: related_to
    target: /product/boundaries-and-constraints.md
  - type: related_to
    target: /product/non-goals.md
---

# INIT-019 — Interface-Agnostic Governance Core

---

## Purpose

Today Spine's native HTTP/RPC API is the only interface through which the governance engine is addressed. Every other surface — the CLI, the Git endpoint, future forge integrations, a native UI — either lives *inside* the monolithic server process (e.g. `internal/gateway`) or is absent. That shape is adequate for v0.x but it has a structural consequence: the "governance engine" and "the primary interface" are conflated. When we want to surface Spine state through a GitHub adapter, an IDE plugin, or a CLI running on a different machine, we have no clean seam.

This initiative introduces one: a governance core with a stable, interface-neutral command surface, and multiple interface adapters (native API, Git protocol, forge adapter, CLI, UI) that translate user intent *into* that surface and Spine state *out of* it. The governance engine becomes the invariant; interfaces become substitutable clients.

The motivation is spelled out in [Boundaries §2.3](/product/boundaries-and-constraints.md): forges like GitHub are clients of Spine's governance engine, not authorities over it. For that framing to be more than aspiration, the code has to admit more than one interface.

---

## Motivation

- **Forges as clients, not authorities.** [Boundaries §2.3](/product/boundaries-and-constraints.md) and [Non-Goals §3.3](/product/non-goals.md) assert that forge UIs integrate as clients. That requires an adapter layer where a forge event (e.g. "reviewer approved PR #42 on GitHub") translates into a call on Spine's governance surface, rather than into a direct state mutation the engine has no record of. Without this initiative, "forge as client" remains documentation.
- **Clean separation of governance state and presentation.** [Product Definition §4.1](/product/product-definition.md) now commits Spine to owning PR-shaped governance state (planning runs, review discussions, approval outcomes, merge authority) while delegating presentation. That separation is only enforceable when the governance surface is explicit and interface-free.
- **Reduced coupling to `internal/gateway`.** A thick HTTP gateway that knows about every service, every permission, and every workflow detail is the structural shape that makes "one more interface" a large undertaking. Extracting the command surface makes alternative interfaces cheaper to build and easier to keep behaviorally identical.
- **Testability and evolvability.** A well-defined command surface is easier to version, test, and evolve than an ad-hoc HTTP API. Scenario tests can exercise the governance engine directly without spinning up HTTP.

---

## Scope

### In Scope (to be refined by follow-up ADRs and epics)

- **ADR defining the interface-agnostic core**: the command surface shape, its relationship to the existing HTTP API, the extension mechanism for new interfaces, and the compatibility story during the transition.
- **Forge-adapter prototype**: a minimal GitHub (or GitLab) adapter that demonstrates a forge event (PR opened, review submitted, merge requested) mapping to the governance surface. The adapter must round-trip: Spine state changes surface back to the forge.
- **Command-surface documentation**: a developer-facing document that lists the commands, their inputs/outputs, and how each existing interface (native API, CLI, Git) maps onto them. This is the contract future interfaces build against.
- **Gateway refactor**: where it is cheap, reshape the HTTP gateway to delegate to the command surface rather than owning its own logic. Where it is expensive, document the gap and defer.

### Out of Scope

- Replacing the existing HTTP API. The native API remains a first-class interface; this initiative makes it *one of several*, not obsolete.
- Building a native Spine UI. A UI is a consumer of this work but its own product effort.
- GitHub Enterprise or self-hosted GitLab specifics — start with the simplest adapter target and generalise from there.
- Workspaces or shared-mode runtime changes — this initiative is a logical-architecture change, not a deployment-shape change.

---

## Open Questions (for the driving ADR)

- **Surface shape.** Is the command surface a Go interface (in-process), an RPC protocol (language-agnostic), or both? The trade-off is adapter locality vs. transport cost.
- **Authorization.** Today authorization lives in the HTTP gateway middleware. Does it move into the command surface (and every adapter gets it for free), or does each adapter re-enforce its own rules? The first is cleaner; the second matches how HTTP gateways normally work.
- **State subscription.** Forge adapters need to *react* to Spine state changes (a run completed → update the PR status). Does the surface expose a push subscription, or do adapters poll/webhook? This is the direction that most differs from today's model.
- **Idempotency and identity.** An adapter that forwards a forge event must carry enough identity (actor, trace, operation key) that retries are safe. How is that modeled at the command surface?
- **Versioning.** The native API is a living interface. Committing to a stable command surface implies a versioning policy — we need to decide how strict and how early.

---

## Relationship to Other Initiatives

- **[INIT-018 — Branch Protection](/initiatives/INIT-018-branch-protection/initiative.md)** — depends on the "Spine hosts the Git repo" boundary already settled. Does *not* block on INIT-019, but its override and audit story will be cleaner once the command surface is explicit (today, override lives in HTTP-layer plumbing; tomorrow, it is a field on a command).
- **[INIT-017 — Workflow Lifecycle Governance](/initiatives/INIT-017-workflow-lifecycle/initiative.md)** — established that workflow edits are themselves governance; INIT-019 generalises that shape to every governance operation by making the surface explicit.
- **[INIT-013 — External Event Delivery](/initiatives/INIT-013-external-event-delivery/initiative.md)** — covers the outbound direction (Spine → external, via webhooks). INIT-019 is the inverse: external intent being accepted into Spine via adapters, with state round-tripping back outward through the INIT-013 pipeline.

---

## Status

**Draft.** No epics, no tasks, no commitment date. This stub exists to capture the thesis so that INIT-018 and the updated product boundaries have a concrete thing to point to. The first step after promotion out of Draft is an ADR — likely ADR-010 — defining the command surface.
