---
id: TASK-012
type: Task
title: "Add CPU/memory limits to docker-compose services"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: chore
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-012 — Add CPU/Memory Limits To docker-compose Services

---

## Purpose

`docker-compose.yaml` services have no `deploy.resources` or `mem_limit`/`cpus`. A runaway Postgres, registry, or spine process can starve the host, producing availability issues and making DoS bugs worse than they need to be.

---

## Deliverable

- Add conservative `cpus` and `memory` limits to each service in `docker-compose.yaml` and `docker-compose.test.yaml`.
- Keep values overridable via compose override for sizing experiments.
- Document the defaults in the README ops section.

---

## Acceptance Criteria

- `docker compose config` shows resource limits on every service.
- Tests still pass under the new limits.
