---
id: TASK-002
type: Task
title: AWS Secrets Manager provider
status: Completed
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
  - type: blocked_by
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/tasks/TASK-001-secret-client-interface.md
---

# TASK-002 — AWS Secrets Manager provider

---

## Purpose

Implement `SecretClient` against AWS Secrets Manager.

## Deliverable

`internal/secrets/aws.go`. The provider must:

- Use AWS SDK v2 with IAM-role credentials.
- IAM scope is read-only: `secretsmanager:GetSecretValue` and
  `secretsmanager:DescribeSecret` only. No write actions —
  rotation is platform-side.
- Map `SecretRef` (`secret-store://workspaces/{ws}/runtime_db`)
  to a Secrets Manager ARN deterministically based on configured
  region, account, and env prefix.
- Distinguish `ErrSecretNotFound`, `ErrAccessDenied`, and
  `ErrSecretStoreDown` from the SDK's error shapes.
- Log only the reference and version ID. Never the value.
- `Invalidate` is a no-op against AWS (the cache is on Spine's
  side; AWS itself does not need invalidation).

## Acceptance Criteria

- Provider passes the contract suite at
  `internal/secrets/contract/` (scaffolded in TASK-001) by
  calling `contract.RunContract(t, newAWSClient)` from a test
  file that targets a localstack-backed Secrets Manager.
- Integration test exercises the provider against a real
  staging AWS account.
- IAM denial returns `ErrAccessDenied`, not `ErrSecretNotFound`.
- No secret value appears in any log, trace, or error message —
  asserted by automated test.
