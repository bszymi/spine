---
type: Product
title: Multi-Repository Workspaces
status: Living Document
version: "0.1"
---

# Multi-Repository Workspaces

---

## 1. Purpose

This document defines the product model for multi-repository workspaces — extending Spine from a single-repository system to one that governs execution across multiple Git repositories within a single workspace.

It grounds the extension in real team structures and workflow problems, and defines how multi-repo support integrates with the existing product model defined in the [Product Definition](/product/product-definition.md).

---

## 2. The Problem

Spine's current product model assumes all governed artifacts and implementation code live in a single Git repository per workspace. This is a structural limitation that prevents adoption by teams with polyrepo architectures.

**Real-world patterns that don't fit the current model:**

- A platform team with 8 microservices in separate repos, but one product vision and one set of initiatives
- A team where infrastructure (Terraform), backend (Go), and frontend (React) each have their own repos with different CI pipelines and release cadences
- An organization adopting Spine incrementally — they want to govern execution without reorganizing their repository structure
- A shared library team whose work is triggered by tasks in a consuming service's Spine workspace

These teams cannot use Spine today without consolidating into a monorepo, which is often impractical or organizationally impossible.

---

## 3. Product Model Extension

### 3.1 Repository Types

Spine introduces two repository types within a workspace:

| Type | Count | Contains | Managed By |
|------|-------|----------|------------|
| **Primary** (`spine`) | Exactly 1 | Governance artifacts, product docs, architecture, workflows, initiatives, epics, tasks | Spine (authoritative) |
| **Code** (`code`) | 0 to N | Implementation code, configs, infrastructure | Teams (Spine creates branches during execution) |

The primary repository is the existing Spine repo. It is always present and is where all governance truth lives. Code repositories are registered by teams and contain the code that tasks produce or modify.

### 3.2 How It Works for Users

**Registering a code repository:**

A technical lead or platform engineer registers code repos in their workspace:

```
POST /api/v1/repositories
{
  "repository_id": "payments-service",
  "url": "https://github.com/acme/payments-service.git",
  "default_branch": "main"
}
```

**Authoring tasks that span repos:**

When creating a task, the author specifies which repositories the task affects:

```yaml
---
id: TASK-042
title: "Add rate limiting to payments API"
repositories:
  - payments-service
  - api-gateway
---
```

If `repositories` is omitted, the task operates only on the primary repo (backward compatible).

**Execution:**

When a run starts for TASK-042:
1. Spine creates branch `spine/run/task-042-rate-limiting-abc123` in both `payments-service` and `api-gateway`
2. The runner for each step clones the relevant repo: `git clone http://spine:8080/git/ws-1/payments-service`
3. Work is committed to the run branch in each repo
4. On completion, Spine merges the run branch in each repo independently

**Viewing results:**

The task in the primary Spine repo records the outcome per repo:

```yaml
run_outcomes:
  payments-service: merged
  api-gateway: merged
```

If one repo's merge fails (conflict), the task remains open and surfaces the failure.

### 3.3 Relationship to Existing Product Concepts

| Concept | Single-Repo (current) | Multi-Repo (extended) |
|---------|----------------------|----------------------|
| Workspace | 1 repo | 1 primary + N code repos |
| Artifacts | All in one repo | Governance in primary, code in code repos |
| Task | Implicit single repo | Explicit `repositories` field (optional) |
| Run branch | Created in the one repo | Created in each affected repo |
| Merge | One merge to main | Per-repo merge, outcomes recorded in primary |
| Git HTTP serve | `/git/{workspace_id}` | `/git/{workspace_id}/{repo_id}` |

---

## 4. Use Cases

### 4.1 Microservice Team Adopts Spine

**Persona:** Technical Lead

**Scenario:** A team runs 5 microservices, each in its own GitHub repo. They adopt Spine for product-to-execution traceability. They create a new Spine primary repo for governance artifacts, then register each service repo. Tasks reference the specific services they affect. Runners clone from Spine's git endpoint during execution.

**Without multi-repo:** The team must consolidate into a monorepo or maintain Spine artifacts disconnected from the code they govern.

**With multi-repo:** Governance lives in one place. Code stays where it is. Spine bridges the two.

### 4.2 Cross-Service Feature Development

**Persona:** Software Engineer, AI Agent

**Scenario:** A feature requires changes in `payments-service` (new endpoint) and `api-gateway` (new route). A single task is created with `repositories: [payments-service, api-gateway]`. The run creates branches in both repos. An AI agent implements the payments endpoint; a human implements the gateway route. Both commit to their respective run branches. Spine merges both when the task is accepted.

**Without multi-repo:** The work is tracked in two separate systems or informally coordinated.

**With multi-repo:** One task, one run, two repos — coordinated through Spine governance.

### 4.3 Incremental Adoption Without Repo Reorganization

**Persona:** Platform Engineer

**Scenario:** An organization wants to adopt Spine but cannot reorganize its 20+ repositories. The platform engineer creates a Spine workspace, registers the most critical repos, and starts governing new initiatives through Spine. Existing repos continue their normal workflows. Over time, more repos are registered and more work flows through Spine.

**Without multi-repo:** Adoption requires all-or-nothing repo restructuring.

**With multi-repo:** Spine wraps around the existing repo structure.

---

## 5. Constraints

1. **One primary repo per workspace** — the primary repo is the governance authority. There is no "primary-less" workspace.
2. **No cross-workspace repo sharing** — a code repo registered in workspace A is not visible to workspace B. Workspace isolation (Product Definition §5.2) is preserved.
3. **No distributed transactions** — merges happen per-repo. If repo A merges but repo B conflicts, the system records this state. It does not roll back repo A.
4. **Code repos are not governance authorities** — governance artifacts (initiatives, epics, tasks, workflows, ADRs) live only in the primary repo. Code repos contain implementation.
5. **Backward compatible** — existing workspaces with no registered code repos behave identically to today.

---

## 6. Non-Goals

- **Repository synchronization** — Spine does not mirror or sync repos. It creates branches and merges them.
- **Per-repo access control** — v0.x uses workspace-level RBAC. Per-repo permissions may come later.
- **Monorepo migration** — Spine does not help teams consolidate repos.
- **Cross-repo dependency management** — Spine does not resolve build dependencies between repos.
- **Code repo artifact discovery** — Spine does not scan code repos for governance artifacts. Only the primary repo is scanned.

---

## 7. Evolution Policy

This document is expected to evolve as multi-repo support is implemented and real teams provide feedback.

Changes must be versioned in Git and must not contradict the [Charter](/governance/charter.md) or [Constitution](/governance/constitution.md).
