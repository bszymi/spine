---
id: EPIC-004
type: Epic
title: Supply Chain
status: In Progress
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
owner: bszymi
created: 2026-10-23
last_updated: 2026-11-04
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/initiative.md
---

# EPIC-004 — Supply Chain

---

## 1. Purpose

The supply-chain lane of the 2026-10-22 review (`supply-chain/REPORT.md`) found no committed
credential and no known-compromised dependency in Spine. It did find drift with no alarm:

- Spine builds with an unpinned Go 1.26 that is two security releases behind.
- pgx and x/text carry reachable advisories.
- CI actions use mutable tags, and CI images are unpinned.
- There is no vulnerability scan and no Dependabot.

Because Spine is public, a compromised action tag or base image reaches every contributor's
fork.

---

## 2. Scope

### In scope

- `go.mod` and `go.sum` (`toolchain` line and dependency floors), and the `Dockerfile` (digest
  pins).
- `.github/workflows/ci.yml`: top-level `permissions`, SHA-pinned actions, digest-pinned job and
  service images, a govulncheck job, and a toolchain-parity check.
- `.github/dependabot.yml`, plus the image references in the `docker-compose*.yaml` files.

### Out of scope

- Repository settings: the `main` ruleset, fork-PR approval, allowed actions. These are owner
  actions (initiative §7d).
- SMP's own supply-chain items (SC-01 SMP half, SC-02 … SC-05, SC-07, SC-10 … SC-13).
- SBOM and provenance for Spine (no Spine release pipeline exists).

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | SMP impact |
|---|---|---|---|---|---|
| TASK-009 | Toolchain and dependency bumps, Dockerfile and CI pinning, govulncheck, Dependabot | SC-01 (Spine half), SC-06, SC-08, SC-09 | 2 (first in practice) | — | yes: MVS raises SMP's pgx, x/text and chi |
| TASK-014 | Go 1.27 toolchain: pins, golangci-lint, and JSON that does not depend on the encoding/json engine | Dependabot #598 | follow-up (2026-11-05) | — | yes: SMP's Go 1.27 move needs this commit (explicit non-string-key refusal) |

---

## 4. Acceptance Criteria

- `go.mod` carries `toolchain go1.26.10` (or the newest patch at implementation).
- `govulncheck ./...` at the task's merge commit reports no reachable vulnerability.
- Every `uses:` in `ci.yml` is a full commit SHA with a version comment, and every image is
  pinned by digest. The workflow has `permissions: contents: read` at the top.
- CI fails when the Dockerfile's Go version differs from the `toolchain` line.
- `.github/dependabot.yml` covers `gomod`, `github-actions` and `docker`.
- The SMP compatibility gate passes. The SMP bump note names the MVS-raised modules.
