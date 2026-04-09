---
id: TASK-007
type: Task
title: "Redact Git URL credentials from logs"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-002-security-hardening/epic.md
---

# TASK-007 — Redact Git URL Credentials from Logs

---

## Purpose

`/internal/workspace/provision.go` (line 178) logs Git URLs in plaintext via `log.Info("cloning remote repo", "git_url", gitURL)`. If the URL contains embedded credentials (e.g., `https://token@github.com/...`), the full token is written to structured logs and potentially forwarded to log aggregators.

---

## Deliverable

Scrub the userinfo segment before logging using `url.Parse` + `url.Redacted()`.

---

## Acceptance Criteria

- Git URLs logged without credentials
- Log output shows redacted form (e.g., `https://***@github.com/...`)
- Existing provision tests pass
