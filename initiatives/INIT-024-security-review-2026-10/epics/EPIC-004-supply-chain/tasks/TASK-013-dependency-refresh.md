---
id: TASK-013
type: Task
title: "Dependency refresh: postgres digest, the gomod group (pgx v5.11.0), LocalStack 4.14.0"
status: Completed
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-11-05
last_updated: 2026-11-06
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

Branch `init/INIT-024-TASK-013-dependency-refresh`, cut from Spine `main` `adeffdf0` and rebased onto
`origin/main` `24136df6` (Dependabot #599, #600 and #603 merged; no conflict). Commits (all prefixed
`INIT-024 EPIC-004 TASK-013:`): `83fd46b9` task artifact; `c031dda7` postgres digest and LocalStack;
`331a55f4` the gomod group, fakes and redaction tests; `bf972013` connection-string hardening;
`97c970c4` docs; `36727f47` codex round 1 fixes; `54ad8e04` CHANGELOG for them; `e0d4ab9e` lint;
then this notes commit. No SMP file was changed. Logs are in local scratch. Nothing is pushed.

### The review's claims, checked

A read-only review of #604 said pgx v5.11.0 breaks four test fakes and makes two redaction tests
vacuous, and that production code builds. All three hold, with one precision:

- With #604's `go.mod`/`go.sum` (taken from the PR branch), `go build ./...` passes; `go vet`
  (plain, integration, scenario) fails on `pgx.Rows` fakes missing `TypeMap`. The compiler names
  three (one per package); the fourth, `adapters/delivery/metadata_store_test.go` `fakeRows`,
  shares a package with `p31Rows` and fails once that one is fixed. No other fake exists.
- The two redaction tests do not pass vacuously: each has a precondition ("pgx still leaks this
  fixture") that turns vacuous into red. pgx v5.11.0 masks the query-parameter passwords both
  relied on, so both failed on the precondition, which is the CI `go test ./...` red on #604.

### What changed

1. **postgres (#602).** Every `postgres:18-bookworm` pin (compose ×4, `ci.yml`) is
   `@sha256:5c4783a8cc3f0074912164f94dfa1be4e043438f0f707cf968d7af9a0d31a2c8`, comments "index of
   2026-10-28". Registry check (Docker Hub API, no pull): a manifest list of five linux platforms
   (amd64, arm/v7, arm64/v8, 386, ppc64le), each `PG_VERSION=18.6-1.pgdg12+2`, created
   2026-10-28. #602 itself left `ci.yml` behind, which `toolchain-parity` refuses by design.
   The `18-bookworm` tag has since moved again (`sha256:f1ea2a1a…`, a 2026-11-05 rebuild of the
   same 18.6); that is Dependabot's next PR, after the cooldown.
2. **The gomod group (#604).** `go get` of the ten versions and `go mod tidy`: `go.mod` and `go.sum`
   are byte-identical to the PR branch's. `go mod tidy -diff` empty, `go mod verify` passes.
3. **Fakes.** `TypeMap() *pgtype.Map { return nil }` on `memRows`, `p31Rows`, `fakeRows`,
   `subsRows`. They decode in `Scan`, not through a type map; the interface allows nil for rows
   with no type-map-decoded values.
4. **Redaction tests.** Both keep their precondition, now on key/value fixtures that pgx v5.11.0
   still prints (whitespace around the `=`; probe below). New direct tests build parse errors
   whose reason (which pgconn never masks) carries the string and the password, so they hold
   whatever pgx masks: `TestDescribePreauthErr_WithholdsEveryParseError` (gateway) and
   `TestConnStringFreeError_WithholdsWhateverPgxMasks` (workspace). Mutations (`-overlay`):
   `describePreauthErr` never withholding fails both gateway tests; `connStringFreeError` passing
   parse errors through fails both workspace tests (14 subtests); keeping the reason unmasked is
   caught only by the new direct test (4 subtests).
5. **Connection-string hardening (pgx v5.11.0 impact, below).** The `sslmode=disable` gate, the
   masking of password pieces, and the quoted fragments of parse reasons.
6. **LocalStack.** `docker-compose.test.yaml` pins
   `localstack/localstack:4.14.0@sha256:3ebc37595918b8accb852f8048fef2aff047d465167edd655528065b07bc364a`.
   Registry check: tags `4.14.0`, `4.14` and `4` are that manifest list; amd64 and arm64 both have
   `LOCALSTACK_BUILD_VERSION=4.14.0`, `LOCALSTACK_BUILD_DATE=2026-02-26`; no 4.x tag is newer (the
   next tags are `2026.03`…`2026.10.6`). The open-source repository is archived ("consolidation
   into the unified LocalStack image"), and v4.14.0 is its last release. The image has `curl` (the
   compose healthcheck uses it) and exposes 4566. `.github/dependabot.yml` ignores
   `version-update:semver-major` for `localstack/localstack` in the `docker-compose` ecosystem,
   with the reason. Schema check against SchemaStore `dependabot-2.0.json`: VALID; a negative
   control (`semver-mayor`) is refused at `/updates/3/ignore/0/update-types/0`.
7. **Docs.** CHANGELOG (Security, operators, standalone, embedders, tests); KNOWN-LIMITATIONS
   "Connection Strings Under pgx v5.11.0"; `.env.example` (compose passwords go into a URL
   unencoded).

### pgx v5.11.0: connection strings Spine builds and parses

Spine builds no DSN from a user name and password. It parses operator DSNs at
`adapters/store/postgres.go` (`pgxpool.New`), `service/internal/workspace/db_provider.go`
(`pgxpool.New`), `dbpool.go` `BuildPgxpoolConfig` (`pgxpool.ParseConfig`), `provision.go`
(`pgx.Connect`, and `replaceDatabaseInURL`, which swaps only the path), `testutil/db.go`, and the
notify listener (the pool's own parsed config). It reads DSNs itself in three places:
`requireSecureDBURL` (the startup gate), `connStringSecrets` and `dsnSecrets` (masking).

**Side-by-side probe** (the same table parsed by pgx v5.9.2 and v5.11.0, local scratch): 52 forms,
38 identical. Every Spine-shipped form is identical: compose (`SPINE_DATABASE_URL`, the registry),
`sslmode=require`, the test default, the docs' no-userinfo URL, a provisioned workspace URL, and
key/value. The password spellings the brief names: `+`, `%2B`, `%40`, `:`, `%3A`, `%2F` identical in
the userinfo; raw `/` refused by both. The 14 that differ (v5.9.2 → v5.11.0):

| Form | v5.9.2 | v5.11.0 |
|---|---|---|
| userinfo raw `@` (`u:pa@ss@h`) | host `h`, password `pa@ss` | host `ss@h`, password `pa` (connection fails) |
| userinfo raw `#` / `?` | refused | accepted (password `pa#ss` / `pa?ss`) |
| query `password=pa+ss` | `pa ss` | `pa+ss` |
| query `password=pa=ss` | `pa=ss` | refused (extra `=`) |
| key/value `password=pa\ss` | `pa\ss` | `pass` |
| `sslmode=disable&sslmode=require` | no TLS (first wins) | TLS (last wins) |
| `sslmode=require&sslmode=disable` | TLS | no TLS |
| `?ssl=true` | runtime parameter `ssl`, prefer-style fallback | `sslmode=require` |
| `h1,h2:5433` | both 5433 | h1:5432, h2:5433 |
| `pool_max_conns=7&pool_max_conns=9` | 7 | 9 |
| bare `::1` | host `::1` | refused |
| `?foo` (no `=`) | runtime parameter | refused |
| `?port=` | refused | port 5432 |
| `h1,,h2` | the empty element dropped | default host (Unix socket) |

`TestBuildPgxpoolConfig_DSNForms` pins 38 of these forms against v5.11.0, each differing row
annotated with v5.9.2's reading.

**Three Spine checks changed meaning, and are fixed (each test red before, green after):**

1. **The `sslmode=disable` gate.** v5.11.0 trims spaces around a query key and value and takes the
   last repeated parameter: `?sslmode= disable`, `?sslmode =disable` and
   `?sslmode=require&sslmode= disable` now connect without TLS (v5.9.2 refused the first, ignored
   the second's key, and kept TLS for the third), and the literal check passed all three. Also
   passing with every pgx release: `%64isable`, `ssl%6Dode`, key/value `sslmode = disable`,
   `sslmode='disable'`, `sslmode=dis\able`. `sslModeDisabled` keeps the literal check first (so it
   refuses everything it refused before), then reads URI settings where pgx v5.11.0 finds the
   query and key/value settings with pgx's tokenizer rules, and refuses any `sslmode` that reads
   as `disable`. `TestRequireSecureDBURL_EverySpellingOfDisable` (23 rows) checks each verdict
   against `pgconn.ParseConfig`'s TLS config, so the gate and the driver agree. Red before: 10
   refusals missing (round 0), 3 false refusals (codex round 1).
2. **Password pieces in connect errors.** v5.11.0 ends the userinfo at the first `@`: a raw `@`
   moves the password's tail into the host part, which pgx decodes and splits at `,`, and the
   connect error names each host (`lookup SECRETTAIL…@127.0.0.1: no such host`). Masking listed
   only net/url's reading (the whole password). `workspace.UserinfoPasswordPieces` returns the
   password (raw and decoded) and each piece between URI delimiters; `connStringFreeError` and
   `describeDBError` mask them, longest first. Tests: `TestNewWorkspaceDBPool_RawAtInPasswordLeaksNoPiece`
   and `TestDescribeDBError_RawAtInPasswordLeaksNoPiece` (real pgx connect errors).
3. **Quoted fragments in parse reasons.** v5.11.0's reasons quote the part of a malformed string
   they stopped at: `missing "=" after "SECRETK6"` (an unquoted space in a key/value password),
   `missing key/value separator "=" in URI query parameter: "SECRETU1"` (an unencoded `&`),
   `invalid percent-encoded token: "SECRETU2%zz@127.0.0.1"` and `unexpected spaces found in "…"`
   (a tail moved into the host). `connStringFreeError` keeps pgx's reason, so these reached
   workspace errors and logs. `maskQuotedFragments` masks every quoted segment of 3+ bytes that
   quotes the string, except a parameter name followed by `=`
   (`TestBuildPgxpoolConfig_ReasonQuotesNoPasswordFragment`).

**Which forms still leak through pgx's own parse error** (probe, v5.9.2 / v5.11.0): v5.11.0 masks
the query-parameter forms v5.9.2 printed (A2–A4, A8, `pass%77ord=`); it still prints key/value
passwords with whitespace around `=` (`password = X`, `password= X`, `password =X`, a tab,
`sslpassword = X`), and partial tails (`TOP\ SECRET`, an unquoted space, an unencoded `&` or `@`).
Spine's withholding covers all of them (tests above); the residual is in KNOWN-LIMITATIONS.

**timestamptz.** Ruled out, with evidence. Spine sets no `QueryExecMode` (grep), imports neither
`pgtype` nor `pgx/v5/stdlib`, casts no time column to text (`::text` appears only on `text[]`
literals), and uses no `pgtype.Timestamptz`. Probe on a scratch database (session time zone
Asia/Kolkata, client `TZ=America/Los_Angeles`), both versions:
- extended protocol (Spine's default): timestamptz arrives in binary; scanned into `time.Time`
  it is in `time.Local` with both versions (unchanged);
- scanned into `*string`: both versions refuse (`cannot scan timestamptz (OID 1184) in binary
  format into *string`), so no code can read one as text silently;
- simple protocol only: v5.9.2 returns the server's offset (`+0530`), v5.11.0 `time.Local`. Only
  an operator DSN with `default_query_exec_mode=simple_protocol` or `exec` sees this; the instant
  is unchanged (CHANGELOG).

### Changelogs read (dispositions)

- **pgx v5.10.0:** `require_auth`, `ConnStringAllowedKeys` (not used), decoder hardening, max
  message body ~1 GiB, CancelRequest over TLS, `ErrConnClosed`: no Spine change. Fixes
  (`parseKeywordValueSettings` trailing whitespace, data race on cancelled connect): benign.
- **pgx v5.11.0:** `Rows.TypeMap` (fakes fixed); the URI and key/value parsers (above: gate,
  masking, docs); parse errors masking query-parameter passwords (tests reworked); timestamptz
  text path (ruled out); name-based struct mapping case-insensitive (`RowToStructByName` unused);
  OS-user lookup only when no user, `$HOME` for pgpass/service/TLS files (operator note in
  CHANGELOG); `pool_ping_timeout` key; non-positive `MaxConnLifetime` unlimited (Spine sets none);
  `Begin` keeps the connection after a recoverable error; NUL bytes refused in parameters; pipeline,
  batch and numeric fixes: no Spine dependency.
- **chi v5.2.5 → v5.5.0** (source diff of `chi.go`, `context.go`, `mux.go`, `tree.go`; Spine
  imports no `chi/middleware`): the HTTP `QUERY` method joins `mALL`, so `Handle`/`HandleFunc`
  routes match it. Spine's are `/git/*` and the API wildcards `/artifacts/*`, `/workflows/*`,
  `/tasks/*`; their handlers answer 404 (API) and the auth gate then 403 (git).
  `TestQueryMethod_ReachesNoWildcardAction` fails on chi v5.2.5 (405) and passes on v5.5.0;
  `TestServeHTTP_QueryMethodIsUnsupported` pins the git handler. Also: `Mount` onto itself
  panics (Spine does not), `Context.Clone`, `r.Pattern` always set, `Walk`/`Routes` stub changes
  (unused), `Allow` de-duplicated. CHANGELOG (standalone).
- **cobra v1.9.1 → v1.10.2, pflag v1.0.6 → v1.0.9:** pflag v1.0.8 renamed `ParseErrorsWhitelist`
  (restored in v1.0.9; Spine uses neither), errors became typed structs with the same texts,
  `ErrHelp` no longer printed, re-parse resets args; cobra adds context in `SetHelpFunc` and a
  default completion directive, and moves to `go.yaml.in/yaml/v3` (a `go.sum` line only). The
  CLI tests (`service/cmd/spine`) pass unchanged.
- **AWS SDK** (aws-sdk-go-v2 v1.41.6 → v1.47.5, config, credentials, secretsmanager v1.41.6 →
  v1.50.7, smithy-go v1.25.1 → v1.28.7): new features are opt-in (`AWS_ENABLE_DEFAULT_SOCKET_TIMEOUT_2026`,
  `AWS_NEW_RETRIES_2026`, clock-skew option); credential and STS panics now errors; clock-skew
  logic updated. secretsmanager v1.50.0 moved to schema-based (de)serialization (an internal
  rewrite; the SDK reverted an earlier wave of it for regressions). Spine's unit tests use a fake
  `secretsmanager` API, so only `TestAWSClient_Contract_LocalStack` would exercise the new serde,
  and it is not run here (no containers) and not in CI.

### Gates

At the final head `e0d4ab9e` (rebased onto `24136df6`) unless noted.

| Gate | Result |
|---|---|
| `go run ./dev/pincheck`; `-resolve` | OK (`toolchain go1.26.10; 3 golang reference(s) on one digest; 10 image(s) and 14 action(s) pinned`); `-resolve`: golang digest `GOLANG_VERSION=1.26.10` on 5 platforms, 14 of 14 action pins equal their tag's commit. `-resolve` does not inspect the postgres or LocalStack digests; they were checked in the registry by hand (above). |
| Modules | `go.mod`/`go.sum` byte-identical to #604's; `go mod tidy -diff` empty; `go mod verify` all verified |
| `go build ./...`; `make build`; embed-smoketest | rc 0; rc 0; "engine constructed and exercised; library surface is sound." |
| `go vet` plain / `-tags integration` / `-tags scenario` | rc 0 ×3 |
| `go test -p 4 -count=1 ./...` | rc 0, 41 packages ok (under the heavy-suite lock) |
| `go test -race -p 2` on every touched package (`service/internal/{workspace,gateway,githttp}`, `service/cmd/spine`, `adapters/{store,delivery,secrets/...}`, `testutil`) | rc 0, 9 ok |
| ci.yml `scenario-git-http`, every run step read from `ci.yml`, own database | all 8 steps rc 0, 0 skips: `TestGitHTTP_*` 90 passes, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*` 154, step CAS 12, step evidence 24, status lifecycle 37 + 1, governance 19 + 1, workflow policy 1, multi-scheduler 1; it ran on the new pgx |
| `go test -tags integration -p 1 -count=1 ./...` (fresh database plus a migrated registry database) | 39 packages ok; failures exactly the two known pre-existing: `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`) and `TestAWSClient_Contract_LocalStack` (no LocalStack container here; something else on `localhost:4566` answered `501`, so this is no LocalStack result) |
| `make lint` (golangci-lint v2.11.4) | 249 issues on the branch and the same 249 on a `git archive` of `origin/main` (identical file and message sets): 0 new. `--new-from-rev origin/main` plain / integration / scenario: 0 issues ×3 (four gocritic hits in this task's code were fixed in `e0d4ab9e`). `--enable-only=gosec ./...`: 0 issues |
| `./dev/lint-boundaries.sh` | OK |
| gofmt on changed `.go` files | clean |
| govulncheck v1.9.0 (`GOFLAGS=-mod=readonly`, go1.26.10, DB updated 2026-11-02 22:01 UTC) | no findings at any level (TASK-009's module-level chi advisories are gone with chi v5.5.0) |
| Dependabot schema (SchemaStore `dependabot-2.0.json`); compose `config -q` ×2 | VALID (negative control refused); OK ×2 |
| Databases | `w11_spinedeps_*` only, all dropped; no `ws_*` database or role left by these runs |

**Heavy-suite lock.** The unit leg ran under the lock. The race, scenario and integration legs then
waited about 11 hours behind another agent's chain, whose test binary was still running far past
its own `-timeout 20m`. Another agent's process may not be killed, so those three legs ran without
the lock (race at `-p 2`, integration at `-p 1`). All passed first time, so no re-run was needed.

### SMP compatibility (read-only)

SMP `origin/main` `d46cfe15` in a throwaway detached worktree whose `../spine` was this branch
(rebased onto `0e58a6a6`, before the codex round 1 fix; the later commits change only
`service/cmd/spine`, `service/internal/workspace`, tests and docs, none importable by SMP, and leave
`go.mod` alone). SMP was never committed to; the worktree is removed.

| Step | Result |
|---|---|
| `GOFLAGS=-mod=readonly go build ./...` (SMP unchanged) | refused: `updates to go.mod needed` (expected: MVS) |
| `go mod tidy` (throwaway) | raises aws-sdk-go-v2 v1.41.6 → v1.47.5, config v1.32.16 → v1.33.12, credentials v1.19.15 → v1.20.12, feature/ec2/imds v1.18.22 → v1.20.5, secretsmanager v1.41.6 → v1.50.7, sts v1.42.0 → v1.51.6, smithy-go v1.25.1 → v1.28.7, chi v5.3.0 → v5.5.0, pgx v5.9.2 → v5.11.0, and the indirect configsources v1.5.8, endpoints/v2 v2.8.8, v4a v1.5.8, accept-encoding v1.13.23, presigned-url v1.14.8, signin v1.12.0, sso v1.39.0, ssooidc v1.44.0. x/sync is already v0.24.0; SMP takes neither cobra nor x/time. 17 `go.mod` lines and 34 `go.sum` lines are replaced (diffs in local scratch). |
| `go build ./...` after the tidy | fails: `internal/customer/stepevents/stepeventstest/memdb.go:365` — `*rows` lacks `TypeMap` (a non-test helper package, so the build itself breaks) |
| `pgx.Rows` implementers needing `TypeMap() *pgtype.Map` | 7 types in 6 files: `stepeventstest/memdb.go` `rows`; `admin/tenantadmin/handler_test.go` `fakeRows`; `admin/infrastructure/handler_test.go` `fakeRows`; `customer/auth/login_test.go` `mockSingleMembershipRows`, `mockMultiMembershipRows`; `customer/search/handler_test.go` `mockRows`; `customer/projection/mock_test.go` `mockRows` |
| With those added (`-overlay`, files untouched): `go build ./...`; `go vet` plain, `-tags integration`, `-tags scenario ./scenariotest/...`, `-tags faultinject` | rc 0 ×5 |
| `go test -p 4 -count=1` on the 87 SMP packages importing pgx (under the heavy-suite lock) | 82 ok, 5 without tests, 0 failures |

**SMP finding for its re-pin (not fixed here):** `internal/dbposture/dsn.go` parses DSNs itself and
says it mirrors pgconn's precedence ("its FIRST value when repeated"), with net/url. Against pgconn
v5.11.0 (scratch probe, overlay only) it now disagrees:
- `?sslmode=require&sslmode=disable`: dbposture reads `require`, pgx dials **without TLS**
  (fail-open for the TLS posture check);
- `?sslmode =disable`: dbposture reads no sslmode, pgx dials without TLS;
- `postgres://u:pa@ss@h/…`: dbposture host `h`, password `pa@ss`; pgx host `ss@h`, password `pa`;
- `?password=pa+ss`: dbposture `pa ss`, pgx `pa+ss`.
SMP's DSN builders are unaffected: the stub allocator's passwords are base64 RawURL, and the
Postgres allocator uses `url.UserPassword`.

### Codex

Plain `codex review --base origin/main` from the worktree, no model override. The rules name
`--base main`, but local `main` is behind `origin/main` by #599, #600 and #603, and this task may
not move it; `origin/main` scopes the review to this task's commits. The model was read from the
review sub-session JSONL (`source: {"subagent":"review"}`, cwd = the worktree).

| Round | Head | Review sub-session (model) | Findings | Disposition |
|---|---|---|---|---|
| 1 | `d0aab972` (before the final rebase) | `01a1acc5-c46a-72e2-b1bb-c014dab8d39d` (`gpt-6.1-sol`) | **P1** a password tail split into a host list leaks each host (`lookup TAILONE`); **P2** the key/value sslmode regex matched text inside a quoted password, refusing a TLS string | **Both fixed** in `36727f47` with tests (seven new cases red before, green after; local scratch) |
| 2 | `3255bbea` | `01a1ad31-072a-7b90-9460-1a0c9ab8b1e7` (`gpt-6.1-sol`) | none ("No actionable regressions were identified") | — |
| 3 | `e0d4ab9e` (final, rebased) | `01a1ad5f-38a9-7f43-98a7-abddde55df29` (`gpt-6.1-sol`) | none | — |

### Not run here

- `TestAWSClient_Contract_LocalStack` (needs the LocalStack container; containers are not started
  here). It skips when the endpoint is unreachable; it is not in CI.
- `make test-db-up` / the compose bring-up of the new postgres and LocalStack pins (no pulls).
  GitHub's `scenario-git-http` job pulls and runs the new postgres digest.
- GitHub CI itself (nothing is pushed).

### Acceptance criteria

1–8 and 10 are met as recorded above. 9: every gate passed; the integration run's two failures are
the known pre-existing ones. AC 8's `TestAWSClient_Contract_LocalStack` is recorded as not run.
Status: Completed.

### Follow-ups

- **After the push (GitHub CI):** `go test ./...` and `scenario-git-http` (the two jobs #604 failed)
  must pass; `toolchain-parity` (pincheck with `-resolve`) and `vuln` must pass; `scenario-git-http`
  is the first run of the new postgres digest. Close #602 and #604 as replaced. Dependabot should
  propose no LocalStack major from now on.
- **SMP re-pin** (another task): the `go.mod` raises and the seven `TypeMap` implementers listed
  above, and `dbposture`'s DSN parser brought in line with pgx v5.11.0.
- **Owner, when a host may pull:** bring up `docker-compose.test.yaml` and run
  `make test-integration` with `spine-test-localstack` (4.14.0) for
  `TestAWSClient_Contract_LocalStack`, the only test that exercises secretsmanager v1.50's new
  (de)serialization.
