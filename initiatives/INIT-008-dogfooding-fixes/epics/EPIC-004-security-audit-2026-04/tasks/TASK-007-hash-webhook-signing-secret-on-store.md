---
id: TASK-007
type: Task
title: "Hash webhook signing secret before storage"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-16
last_updated: 2026-04-16
completed: 2026-04-16
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md
---

# TASK-007 — Hash Webhook Signing Secret Before Storage

---

## Purpose

`SMP_INTERNAL_TOKEN` flows from env (`cmd/spine/main.go:469-476`) into `EventSubscription.SigningSecret` (`internal/delivery/bootstrap.go:19,53,62-88`) and is persisted to the database in plaintext. A DB compromise would yield the secret needed to forge webhook payloads.

Note: webhook signatures are computed server-side, so the stored secret must be reversible for signing. A pure hash won't work — this requires symmetric encryption with a key separate from the DB (KMS, env-sourced key).

---

## Deliverable

- Introduce `SPINE_SECRET_ENCRYPTION_KEY` (32-byte base64) used with `crypto/aes` + GCM.
- Encrypt `SigningSecret` at rest; decrypt on load into memory only.
- Migrate existing rows (encrypt in place or rotate via admin command).
- Refuse to start in production without the key set.

---

## Acceptance Criteria

- Fresh `event_subscriptions` rows have ciphertext, not plaintext.
- Existing rows migrated via one-shot migration or admin command.
- Tests cover encrypt/decrypt roundtrip and missing-key startup failure.
