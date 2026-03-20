---
id: EPIC-001
type: Epic
title: Core Foundation
status: Pending
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/initiative.md
---

# EPIC-001 — Core Foundation

---

## Purpose

Build the shared infrastructure that all Spine components depend on: Go module setup, domain types, database access, Git client, queue, and development environment.

After this epic, developers can build, test, and run Spine locally with Docker Compose, and all foundational interfaces are available for component implementation.

---

## Validates

- [Implementation Guide](/architecture/implementation-guide.md) — Package layout, interfaces, build, testing
- [Runtime Schema](/architecture/runtime-schema.md) — Database tables and migrations
- [Docker Runtime](/architecture/docker-runtime.md) — Containerization and dev environment
- [ADR-005](/architecture/adr/ADR-005-technology-selection.md) — Technology choices

---

## Acceptance Criteria

- Go module compiles with all internal packages
- Domain types cover all core entities (Run, StepExecution, Artifact, Event, etc.)
- GitClient interface + CLI implementation passes integration tests against real Git repos
- Store interface + PostgreSQL implementation passes integration tests
- Queue interface + in-process implementation passes unit tests
- Database migrations create all tables from runtime-schema.md
- `docker compose up` starts a working environment
- `make test` and `make test-integration` pass
- Test helpers (temp Git repos, test DB transactions) are reusable
