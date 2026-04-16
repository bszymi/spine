---
id: INIT-014
type: Initiative
title: Multi-Repository Workspaces
status: Pending
owner: bszymi
created: 2026-04-16
links:
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: /product/product-definition.md
---

# INIT-014 — Multi-Repository Workspaces

---

## Purpose

Spine currently operates under a strict one-workspace-one-repository model. All artifacts — governance, product, architecture, initiatives, and code — live in a single Git repository. This works well for monorepo-structured projects, but breaks down for teams building microservices or any architecture where code is distributed across multiple repositories.

A team building a payments platform may have `payments-service`, `api-gateway`, `notification-service`, and `shared-libs` as separate repositories, but wants a single Spine workspace to govern the product intent, track execution, and coordinate work across all of them.

This initiative introduces **multi-repository support** within a single Spine workspace — allowing one primary Spine repository for governance artifacts alongside N registered code repositories where implementation work happens.

## Motivation

**Real-world teams use multiple repos.** Microservices, polyrepo architectures, and separately-versioned libraries are the norm in most organizations. Forcing all code into a single repo to use Spine is a non-starter for adoption.

**Spine's value is coordination, not co-location.** Spine governs the structural integrity between intent and execution. That governance doesn't require all code to live in one place — it requires Spine to know where code lives and be able to create branches, commit artifacts, and merge results across repositories.

**Runner containers need to clone the right repo.** With INIT-009 (Workspace Runtime) building container-based execution, runners need to clone the specific repository where a task's code changes will be made. The git HTTP serve endpoint (TASK-006) already needs repository routing.

## Scope

### In Scope

- **Repository as a domain concept** — a first-class entity within a workspace, with ID, URL, default branch, and metadata
- **Repository registry** — CRUD API for registering and managing code repositories within a workspace
- **Multi-repo branch lifecycle** — run branch creation, commit, and merge across multiple repositories
- **Task-to-repository binding** — tasks declare which repository (or repositories) they affect
- **Git HTTP endpoint extension** — serve registered code repositories via `/git/{workspace_id}/{repo_id}`
- **Product documentation** — extending the product definition and use cases for multi-repo teams
- **Architecture documentation** — extending the git integration contract and component model

### Out of Scope

- Cross-repository atomic transactions (distributed two-phase commit)
- Monorepo-to-polyrepo migration tooling
- Repository mirroring or synchronization between external Git hosts
- Repository-level access control (v0.x uses workspace-level RBAC; per-repo permissions are a future concern)
- Submodule or subtree integration

## Key Concepts

### Primary vs Code Repositories

Every workspace has exactly one **primary repository** (`kind: spine`). This is the existing Spine repo — it contains governance artifacts (initiatives, epics, tasks, ADRs, workflows, product docs, architecture docs). This repository is always present and cannot be removed.

**Code repositories** (`kind: code`) are registered on top. These contain implementation code — microservices, libraries, infrastructure configs. They are managed by teams through their normal Git workflows, and Spine creates branches in them during governed execution.

### Spine-Repo-as-Ledger Coordination Model

The primary Spine repository is the coordination ledger. When a run spans multiple repos:

1. The task in the Spine repo records which code repos are affected
2. The run creates branches in each affected code repo
3. Step execution happens in the relevant code repo
4. On completion, code repo branches are merged independently
5. The Spine repo records the outcome — success, partial failure, or conflict

If a merge succeeds in repo A but fails in repo B, the run stays open and the failure is recorded in the Spine repo. Code repos are "eventually consistent" with the Spine ledger. This avoids the complexity of distributed transactions while maintaining governance traceability.

## Epics

- **EPIC-001: Repository Domain Model and Registry** — Define the Repository entity, storage, and CRUD API
- **EPIC-002: Multi-Repo Git Client Pool** — Replace single git client per workspace with per-repo client resolution
- **EPIC-003: Multi-Repo Run Lifecycle** — Branch creation, step execution routing, and merge across repositories
- **EPIC-004: Product and Architecture Documentation** — Extend product definition, git integration contract, and component model for multi-repo support

## Design Principles

1. **Primary repo is always present** — removing multi-repo support must leave a working single-repo workspace. The primary repo is never optional.
2. **Additive, not breaking** — single-repo workspaces continue to work unchanged. Multi-repo is opt-in per workspace.
3. **Spine-repo-as-ledger** — the primary repo is the coordination authority. Code repos are execution targets. Governance truth lives in one place.
4. **Independent repo lifecycles** — code repos have their own branches, merge cadence, and default branches. Spine adapts to them, not the reverse.
5. **Fail-open per repo** — a merge failure in one code repo does not block merges in other repos. Failures are recorded and surfaced, not cascaded.

## Success Criteria

1. A workspace can register N code repositories via the API
2. A task can declare which repositories it affects (defaulting to primary repo if unspecified)
3. `spine serve` creates run branches in all affected repos when a run starts
4. Runner containers can clone any registered repo via `git clone http://spine:8080/git/{workspace_id}/{repo_id}`
5. Run completion merges branches in each affected repo independently, recording per-repo outcomes
6. Existing single-repo workspaces are fully backward compatible — no migration required
7. Product and architecture documentation is updated to reflect multi-repo support
