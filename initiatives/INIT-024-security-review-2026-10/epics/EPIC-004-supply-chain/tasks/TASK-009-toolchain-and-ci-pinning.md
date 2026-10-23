---
id: TASK-009
type: Task
title: "Toolchain and dependency bumps, Dockerfile and CI pinning, govulncheck, Dependabot"
status: Pending
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
---

# TASK-009 — Toolchain and dependency bumps, Dockerfile and CI pinning, govulncheck, Dependabot

---

## Purpose

This task closes the Spine half of the supply-chain findings from the 2026-10-22 review
(`tmp/security-review-2026-10-22/supply-chain/REPORT.md`): SC-01 (Spine half), SC-06, SC-08 and
SC-09. Spine is a public repository, so a compromised action tag or base image reaches every
contributor's fork; the fixes remove the drift and add the alarms that were missing.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| SC-01 (Spine half) | P2 (SMP-side) | Spine binaries built at go1.26.8 carry 11 reachable stdlib advisories incl. GO-2026-6809 (distrusted-intermediate TLS). | An attacker with a distrusted intermediate certificate and a network position for outbound TLS. |
| SC-09 | P3 | Standalone Spine image and module | Unpinned `golang:1.26-bookworm` / `debian:bookworm-slim`; no `toolchain` line; pgx v5.8.0 (GO-2026-5004, simple protocol only — Spine never uses it), x/text v0.29.0 (GO-2026-6629/5970). |
| SC-06 | P3 | CI | No govulncheck, npm audit or image scan; no Dependabot. |
| SC-08 | P3 | CI, public repo | `actions/*` on mutable tags; CI images unpinned; no top-level `permissions`; `main` unprotected (owner action). |

## Current code (verified at `a560c210`)

- `go.mod:3`: `go 1.26`, **no `toolchain` line**. `go list -m all` shows `pgx v5.8.0`
  (`go.mod:12`), `x/text v0.29.0` (`:37`, indirect), `chi/v5 v5.2.5` (`:11`). There is **no
  `golang.org/x/net`** in the graph (no `go.sum` line), so the x/net bump is SMP's, not Spine's.
- `Dockerfile:2` `FROM golang:1.26-bookworm` and `:18` `FROM debian:bookworm-slim`, both unpinned.
- `.github/workflows/ci.yml`: `actions/checkout@v4` ×5, `actions/setup-go@v5` ×4 — mutable tags;
  service image `postgres:18-bookworm` (`:94`) and container `golang:1.26-bookworm` (`:280`)
  unpinned; no top-level `permissions:` block; five jobs (`boundary-lint`, `build`, `test`,
  `scenario-git-http`, `git-floor`).
- No `.github/dependabot.yml`. No govulncheck step anywhere.
- `docker-compose.yaml` and `docker-compose.test.yaml` use `postgres:18-bookworm` and
  `localstack/localstack:3` by tag.

## Design

1. **Toolchain and dependencies (SC-01 Spine half, SC-09).**
   - `go.mod`: add `toolchain go1.26.10` (or the newest 1.26 patch at implementation).
   - `go get github.com/jackc/pgx/v5@v5.11.0 golang.org/x/text@v0.43.0`, and
     `github.com/go-chi/chi/v5@v5.5.0` if it clears a reachable advisory; `go mod tidy`.
   - Re-run `govulncheck` and address anything still reachable.
2. **Dockerfile pinning (SC-09).**
   - Pin both `FROM` lines by digest, matching the `toolchain` Go version, with a comment naming
     the tag the digest resolves from. Keep the two-stage build.
3. **CI hardening (SC-06, SC-08).**
   - Add `permissions: contents: read` at the top of `ci.yml`.
   - SHA-pin every `actions/*` use, each with a `# vX.Y.Z` comment.
   - Digest-pin the `postgres` service image and the `golang` container image, so `git-floor`
     keeps testing the shipped git and the version it tests is reproducible.
   - Add a `vuln` job: `GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@<pinned> ./...`.
   - Add a `toolchain-parity` check: the Go version in the pinned `golang` image's `GOLANG_VERSION`
     must equal `go.mod`'s `toolchain`. Because `GOTOOLCHAIN=local`, bumping `go.mod` alone would
     silently ship the old compiler; this check fails when they drift.
4. **Dependabot (SC-06).**
   - Add `.github/dependabot.yml` with `gomod`, `github-actions` and `docker` ecosystems, weekly,
     grouped minor/patch.
5. **Compose images.** Digest-pin `postgres` and `localstack` in both compose files, with a tag
   comment. (These are dev/test only; the digest keeps the local stack reproducible.)

## SMP impact

- **MVS at the bump** (initiative §7b Rule 4). When SMP bumps `.spine-revision` past this task,
  `go mod tidy` in SMP raises any module whose Spine floor is now higher. SMP already ships pgx
  v5.9.2 and x/text v0.42.0 (SMP `go.mod:24, :122`); if Spine's floors exceed those, SMP's tidy
  raises them. The SMP bump task records the raised modules.
- No code change in SMP.

## Decisions

1. **No `golang.org/x/net` bump.** It is not in Spine's module graph (verified). The x/net
   advisories in SC-01 are SMP's.
2. **Pin by digest with a tag comment**, so Dependabot can still propose the next digest and a
   reviewer can read the version.
3. **Keep `GOTOOLCHAIN=local`** and enforce parity, rather than switching to `auto`: a hermetic
   build is the goal, and the parity check removes the silent-downgrade trap.
4. **No SBOM or image signing for Spine.** Spine has no release/publish pipeline; adding one is
   out of scope (the review put SBOM under SC-13, SMP-side).

## Owner questions

1. **pgx and chi bump targets.** *Default:* the review's suggestion (pgx v5.11.0, x/text v0.43.0,
   chi v5.5.0 if needed). *Alternative:* the minimal fixed versions (pgx v5.9.2, x/text v0.41.0).
2. **govulncheck failure policy.** *Default:* the `vuln` job fails CI on any reachable advisory.
   *Alternative:* it reports and a maintainer triages (non-blocking) to avoid a new upstream
   advisory blocking unrelated PRs.

## Acceptance Criteria

1. **Toolchain** (`TestGoModHasToolchain` or a CI assertion). `go.mod` has `toolchain go1.26.10`
   (or newer 1.26 patch). `go build ./...` and the embed-smoketest build pass on it.
2. **govulncheck clean.** The new `vuln` job reports no reachable advisory at the merge commit.
   Recorded in the Implementation Notes with the DB date.
3. **Dependencies.** `go list -m all` shows the bumped pgx, x/text (and chi if bumped). `go mod
   tidy` leaves the tree unchanged.
4. **Dockerfile.** Both `FROM` lines are digests with tag comments; the Go digest's
   `GOLANG_VERSION` equals the `toolchain` line. A build from the Dockerfile succeeds.
5. **Toolchain parity** (`toolchain-parity` job). It fails on a deliberately mismatched fixture
   and passes on the real pair.
6. **CI pinning.** Every `uses:` in `ci.yml` is a 40-hex SHA with a version comment; the
   `postgres` and `golang` images are digests. `ci.yml` has `permissions: contents: read` at the
   top. All five existing jobs still run.
7. **Dependabot.** `.github/dependabot.yml` validates and lists `gomod`, `github-actions`,
   `docker`.
8. **Compose.** `postgres` and `localstack` are digest-pinned in both compose files; `make
   dev`/test still bring the stack up.
9. **SMP compatibility gate** (initiative §5) passes; the note records any MVS-raised SMP module.
10. **Documentation.** `CHANGELOG.md` records the bumps and the CI changes; the owner-action list
    (initiative §7d) is cross-referenced for the `main` ruleset and allowed-actions settings this
    task cannot set.

## Compatibility

- Build and CI only; no runtime behaviour change.
- MVS raises SMP's dependency floors at the bump (initiative §7b).

## Out of scope

- Repository settings: the `main` ruleset, fork-PR approval, "allowed actions: selected". Owner
  actions (initiative §7d).
- SMP's own supply-chain items.
- SBOM/provenance and image publishing for Spine (no pipeline).

## File overlaps

- `docker-compose*.yaml`: this task pins image digests; TASK-007 sets `SPINE_ENV`. Different keys.
- `go.mod`, `go.sum`, `Dockerfile`, `.github/*`: this task only.
