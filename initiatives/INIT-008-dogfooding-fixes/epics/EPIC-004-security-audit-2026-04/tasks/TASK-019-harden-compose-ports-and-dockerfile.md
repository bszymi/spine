---
id: TASK-019
type: Task
title: "Harden compose port bindings and runtime Dockerfile"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: chore
created: 2026-04-16
last_updated: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-019 — Harden Compose Port Bindings And Runtime Dockerfile

---

## Purpose

`docker-compose.yaml:7,37,69` binds service ports to 0.0.0.0 by default. On a workstation with public interfaces this exposes Postgres and the registry to the LAN. `docker-compose.test.yaml:4-5,20-21` has the same issue for test DBs.

`Dockerfile:12,15` runs a full Debian base image and installs `git` + `wget` into the runtime image. `wget` appears unused. The attack surface is bigger than needed for a Go binary.

---

## Deliverable

- Change all published ports in both compose files to bind to `127.0.0.1` (e.g. `"127.0.0.1:8089:8080"`).
- For test compose, drop `ports:` entirely — containers communicate via the internal network.
- Remove `wget` from the runtime image. Evaluate whether `git` is required in the runtime container; if not, drop it. If it is (for push operations), keep + add a comment explaining.
- Consider distroless base as a follow-up (not required for this task).

---

## Acceptance Criteria

- `nmap` or equivalent from a peer host on the same LAN sees no exposed Postgres/registry/spine ports for a fresh `docker compose up`.
- Runtime image size reduced; CI build still passes.
- Test suite still passes under the narrower port binding.
