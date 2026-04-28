---
id: TASK-002
type: Task
title: Implement lazy clone and client initialization
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-001-git-client-pool-interface.md
---

# TASK-002 - Implement Lazy Clone and Client Initialization

---

## Purpose

Avoid cloning every registered code repository at workspace startup while still resolving repositories predictably on first use.

## Deliverable

Implement pool behavior that:

- Resolves repository binding.
- Validates local path is inside the workspace repo base.
- Clones the remote if the local repo is absent.
- Reuses an existing local clone when valid.
- Creates and caches a `git.CLIClient`.

## Acceptance Criteria

- First access clones a missing code repo.
- Later accesses reuse the existing client.
- Concurrent first access does not clone the same repo twice (singleflight or equivalent — call out the chosen mechanism in the deliverable, not just the AC).
- Clone errors are surfaced as repository-unavailable errors.
- Tests use temporary repositories and do not require network access.
- Structured logs/metrics record per-repo clone duration, cache hit/miss, and concurrent-coalesce events.

