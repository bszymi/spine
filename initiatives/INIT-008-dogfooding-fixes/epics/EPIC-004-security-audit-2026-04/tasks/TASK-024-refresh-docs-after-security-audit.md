---
id: TASK-024
type: Task
title: "Refresh docs after EPIC-004 security audit changes"
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

# TASK-024 — Refresh docs after EPIC-004 security audit changes

---

## Purpose

EPIC-004 landed 23 PRs between 2026-04-14 and 2026-04-16. User-facing
behaviour changed in ways that aren't yet reflected in `README.md`,
`docs/integration-guide.md`, the task-execution playbook, or the
deployment sample. Operators reading current docs will miss required
env vars (encryption key, operator token, trusted-proxy CIDRs) and
deprecated modes (env-resident push token).

---

## Deliverable

Audit and update the following surfaces for changes shipped in
TASK-002, TASK-006, TASK-007, TASK-008, TASK-009, TASK-010, TASK-011,
TASK-015 … TASK-023:

- `README.md` — quick-start: highlight new required env vars for
  production deployment (`SPINE_SECRET_ENCRYPTION_KEY`,
  `SPINE_OPERATOR_TOKEN`, `SPINE_ENV`), and note the refusal to start
  with dev-mode + production.
- `docs/integration-guide.md` — section on push auth already covers
  the helper-recommended path (TASK-008); confirm the list of
  required env vars, add a dedicated "secrets at rest" subsection
  for TASK-007, document the per-subscription TLS block for webhooks
  (TASK-018), and list the new `GET /health` `env` + `dev_mode`
  response fields (TASK-020).
- Any deploy sample in `docs/` (compose snippets, Kubernetes
  manifests, systemd unit) — add encryption-key env var, update push
  token example to the helper form, remove stale references to
  `SPINE_GIT_PUSH_TOKEN` as the recommended mode.
- `Makefile` — already has `lint-security`; README may need a one-liner
  pointing at it and at the `make test-integration` prerequisite for
  TASK-007's roundtrip.
- `.env.example` — already updated through the epic; re-read for
  drift against current main and fix any remaining stale comments.
- `CHANGELOG.md` or equivalent release-notes surface, if one exists.

---

## Acceptance Criteria

- A reader following README + integration-guide from scratch can
  bring up `spine serve` in production without encountering an
  undocumented startup-gate refusal.
- Every env var introduced by TASK-007/008/010/016/019/020 is listed
  in the integration-guide env-var table with purpose and default.
- Deprecated/discouraged modes (`SPINE_GIT_PUSH_TOKEN` without
  helper, `SPINE_DEV_MODE=1` in prod, `sslmode=disable` without
  `SPINE_INSECURE_LOCAL=1`) are called out explicitly.
- Any doc changes compile cleanly under the repo's usual markdown
  lint / link-check tooling (if configured).
