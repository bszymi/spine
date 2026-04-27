---
id: EPIC-001
type: Epic
title: SecretClient abstraction and providers
status: Pending
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-010-secret-client-abstraction.md
---

# EPIC-001 — SecretClient abstraction and providers

---

## Purpose

Introduce the `SecretClient` interface in Spine plus two
providers — AWS Secrets Manager and file-mounted JSON — and a
contract test suite that exercises both against the same
scenarios.

After this epic, Spine has exactly one path to obtain workspace
credentials. Direct env-var or file reads for per-workspace
credentials are forbidden.

---

## Key Work Areas

- `internal/secrets` package with the `SecretClient` interface
  and `SecretRef`, `SecretValue`, `VersionID` types.
- AWS Secrets Manager provider (AWS SDK v2, IAM role auth).
- File-mounted JSON provider (read-only mount, deterministic
  layout). Rotation/seeding is platform-side and out of scope
  here, so the provider does not need write capability.
- Provider selection via configuration (`SECRET_STORE_PROVIDER`
  env var).
- Logger redaction rule for `SecretValue`.
- Contract test suite under `internal/secrets/contract/`.
- ADR-010 written and accepted.
- Single-workspace `WorkspaceResolver` (file/env provider from
  INIT-009) migrated to `SecretClient` so the "no direct env-var
  reads of workspace credentials" rule applies without a
  carve-out (TASK-008).

---

## Primary Outputs

- `internal/secrets/` package
- `architecture/adr/ADR-010-secret-client-abstraction.md`
- Contract test suite

---

## Acceptance Criteria

- AWS and file providers pass the same contract tests.
- Logger redaction is verified by a regression test.
- A grep across the Spine codebase for direct env-var reads of
  workspace credentials returns zero results after the migration
  (lint rule or CI check).
- ADR-010 explains the interface, the rejected alternatives, and
  the consistency contract with the platform side.
