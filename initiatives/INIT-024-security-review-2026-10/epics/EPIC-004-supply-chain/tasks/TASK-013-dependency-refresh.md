---
id: TASK-013
type: Task
title: "Dependency refresh: postgres digest, the gomod group (pgx v5.11.0), LocalStack 4.14.0"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-11-05
last_updated: 2026-11-05
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/epic.md
  - type: related_to
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/tasks/TASK-009-toolchain-and-ci-pinning.md
---

# TASK-013 — Dependency refresh: postgres digest, the gomod group (pgx v5.11.0), LocalStack 4.14.0

---

## Purpose

TASK-009 pinned Spine's images and actions by digest and SHA and turned Dependabot on. Its first
weekly run opened PRs that cannot merge as they are. The owner ruled on 2026-11-05 to replace two
of them with a fixed change of our own, and to move LocalStack to 4.14.0:

- **#602** moves the compose `postgres:18-bookworm` digest but not the `ci.yml` postgres service.
  `dev/pincheck` (the `toolchain-parity` job) requires CI's postgres to equal the compose stack's,
  so the PR fails that job by design (TASK-009 KNOWN-LIMITATIONS: "a compose postgres bump ... must
  be moved to the same ref by hand").
- **#604** (the `gomod-minor-patch` group, 10 modules) fails `go test ./...` and the
  `scenario-git-http` job. pgx v5.11.0 adds `TypeMap()` to the `pgx.Rows` interface, so Spine's
  test fakes no longer compile, and two password-redaction tests lose their precondition (pgx now
  masks the passwords their fixtures relied on). Production code builds.
- **#601** (LocalStack `3` → `2026.10.4`, closed) showed that Dependabot follows LocalStack's new
  date-numbered tags. Those images are LocalStack's consolidated image, which needs an account
  token; the open-source line ended at 4.14.0.

This task replaces #602 and #604 and moves LocalStack, and checks what the new pgx, chi, cobra
and AWS SDK versions change for Spine and its operators.

## Current code (verified at `adeffdf0`)

- `docker-compose.yaml:59, :105`, `docker-compose.test.yaml:3, :48` and `.github/workflows/ci.yml:139`
  pin `postgres:18-bookworm@sha256:bbf83594…` ("index of 2026-10-21"; comment also at `ci.yml:137`).
- `docker-compose.test.yaml:27` pins `localstack/localstack:3@sha256:b279c01f…` (LocalStack 3.8.1).
  It is the only LocalStack reference. `.github/dependabot.yml` has no `ignore` rule.
- `go.mod`: aws-sdk-go-v2 v1.41.6, config v1.32.16, credentials v1.19.15, secretsmanager v1.41.6,
  smithy-go v1.25.1, chi v5.2.5, pgx v5.9.2, cobra v1.9.1, x/sync v0.23.0, x/time v0.15.0.
- `pgx.Rows` fakes: `adapters/store/subscription_secrets_test.go` (`memRows`),
  `adapters/delivery/p31_sweep_test.go` (`p31Rows`), `adapters/delivery/metadata_store_test.go`
  (`fakeRows`), `service/internal/workspace/subscription_secrets_test.go` (`subsRows`).
- Redaction tests with a "pgx still leaks this fixture" precondition:
  `service/internal/gateway/preauth_serviceset_test.go` `TestPreauthLog_WithholdsConnectionString`
  and `service/internal/workspace/dbpool_credentials_test.go`
  `TestBuildPgxpoolConfig_ParseErrorCarriesNoConnectionString`.
- DSN parse sites: `adapters/store/postgres.go:63` and `service/internal/workspace/db_provider.go:56`
  (`pgxpool.New`), `service/internal/workspace/dbpool.go:103` (`pgxpool.ParseConfig`),
  `service/internal/workspace/provision.go:57` (`pgx.Connect`) and its `replaceDatabaseInURL`,
  `testutil/db.go:30`, `adapters/delivery/notify_listener.go:233` (the pool's own config). Spine
  builds no DSN from a user name and password; it only swaps the database in an operator's URL.
- Spine sets no `QueryExecMode`, imports neither `pgtype` nor `pgx/v5/stdlib`, and uses no
  `RowToStructByName`.

## Design

1. **postgres digest (#602).** Move every pin above to the PR's digest
   `sha256:5c4783a8cc3f0074912164f94dfa1be4e043438f0f707cf968d7af9a0d31a2c8`, after checking in the
   registry that it is `postgres:18-bookworm` with `PG_VERSION=18.6-1.pgdg12+2`. Fix the "index of
   2026-10-21" comments. `go run ./dev/pincheck` (and `-resolve`) must pass.
2. **The gomod group (#604).** `go get` exactly the PR's ten versions, `go mod tidy`, and compare
   `go.mod`/`go.sum` with the PR's diff.
   - Add `TypeMap() *pgtype.Map` to every `pgx.Rows` fake the compiler names.
   - Rewrite the two redaction tests so they test Spine's withholding, not pgx's redaction: use a
     connection-string form whose parse error still carries the secret under pgx v5.11.0, keep
     the precondition, and add a direct test with a parse error that carries the secret whatever
     pgx's redaction does.
3. **pgx v5.11.0 impact.** pgx v5.11.0 parses connection URIs with a libpq-compatible parser:
   `+` is literal in query values, the userinfo ends at the first `@`, the last repeated query
   parameter wins, `#` is data, interior spaces are an error. Find every DSN Spine builds or
   parses, show that the DSNs Spine and its operators use parse the same, and pin it with a test
   (passwords with `+`, `%2B`, `@`, `:` and `/`, and each other changed form). Rule out or fix
   timestamptz values scanned as text. Read the pgx 5.10 and 5.11 changelogs, and the chi
   5.2.5 → 5.5.0, cobra 1.10 / pflag 1.0.9 and AWS SDK changes, for anything Spine depends on.
   An operator-visible change gets a CHANGELOG entry, and a limitation a KNOWN-LIMITATIONS entry.
4. **LocalStack.** Pin `localstack/localstack:4.14.0@sha256:3ebc3759…` after checking it in the
   registry. Fix the "3.8.1" comment. In `.github/dependabot.yml`, ignore semver-major updates of
   `localstack/localstack` in the `docker-compose` ecosystem, with a comment saying why.
5. **SMP (read-only).** Build, vet and test SMP at `origin/main` against this branch and report
   what SMP's re-pin needs.

## Decisions

1. **The PR's digests and versions, exactly.** The owner approved replacing the PRs, not choosing
   new targets. A newer postgres digest that appears meanwhile is Dependabot's next PR.
2. **LocalStack majors are ignored, not the dependency.** Patch and minor updates of a 4.x tag
   would still be proposed; none exist after 4.14.0, so in practice the pin stays until a human
   moves it.
3. **The redaction tests keep a precondition.** A fixture that pgx no longer leaks makes a test
   pass for the wrong reason; the precondition fails loudly instead, and the direct test keeps the
   guarantee covered whatever pgx does.

## Acceptance Criteria

1. **postgres.** Every `postgres:18-bookworm` pin is `@sha256:5c4783a8…`; the digest is verified in
   the registry (PG 18.6, all platforms); comments say "index of 2026-10-28". `go run
   ./dev/pincheck` passes; `-resolve` passes or the notes say why it could not run.
2. **Modules.** `go.mod` and `go.sum` equal PR #604's; `go mod tidy -diff` is empty and `go mod
   verify` passes.
3. **Fakes.** Every `pgx.Rows` fake implements `TypeMap()`; `go vet` plain, `-tags integration`
   and `-tags scenario` pass.
4. **Redaction tests.** Both tests pass on pgx v5.11.0 and still fail when Spine's withholding is
   removed (a mutation run is recorded). Each has a direct case that does not depend on pgx's
   redaction.
5. **DSN parity.** A test pins how the DSN forms Spine builds and documents parse (passwords with
   `+`, `%2B`, `@`, `:`, `/`), and every form whose parse changed between pgx v5.9.2 and v5.11.0
   is listed in the notes with its operator impact; operator-visible changes are in CHANGELOG and,
   where they limit operators, KNOWN-LIMITATIONS.
6. **timestamptz.** No timestamptz/timestamp value is scanned through the text format, or each one
   is fixed; the notes give the evidence.
7. **Changelogs.** The notes list every pgx 5.10/5.11, chi, cobra/pflag and AWS SDK change relevant
   to Spine, with its disposition.
8. **LocalStack.** `docker-compose.test.yaml` pins `localstack/localstack:4.14.0@sha256:3ebc3759…`,
   verified in the registry; Dependabot ignores semver-major LocalStack updates, with a comment;
   `TestAWSClient_Contract_LocalStack` is recorded as not run here.
9. **Gates.** pincheck; `go build ./...`; `go vet` (plain, integration, scenario); `go test ./...`;
   `-tags integration` on fresh scratch databases for store, delivery, projection, workspace and
   `cmd/spine` (known failure: `TestRunModeDatabaseDefault`); the CI `scenario-git-http` selection
   on a scratch database; `make lint` with 0 new issues; `./dev/lint-boundaries.sh`;
   embed-smoketest; codex with no open P1/P2.
10. **SMP.** The notes say exactly what SMP's re-pin needs (go.mod raises, `TypeMap` on SMP's
    fakes, anything else found).

## Compatibility

- No Spine API, schema or runtime-default change is intended. The pgx parser change is upstream
  behaviour; this task documents it and pins what Spine relies on.
- MVS raises an embedder's floors to these versions when it bumps its Spine pin.

## Out of scope

- PR #598 (golang 1.27) and the Go 1.27 move: TASK-014.
- PRs #599, #600, #603 (debian digest, checkout, setup-go): merged on GitHub separately.
- SMP's own re-pin (another task).

---

## Implementation Notes

(In progress.)
