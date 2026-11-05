---
id: TASK-009
type: Task
title: "Toolchain and dependency bumps, Dockerfile and CI pinning, govulncheck, Dependabot"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-11-04
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

---

## Implementation Notes

Implemented on branch `init/INIT-024-TASK-009-toolchain-and-ci-pinning`, cut from
`init/INIT-024-scaffold` @ `12376b55`. No SMP file was changed. No Spine package that SMP imports
changed; the only new Go code is the dev-only `dev/pincheck`. The orchestrator closes the task.

Commits (all prefixed `INIT-024 EPIC-004 TASK-009:`): `de79857b` toolchain and dependency
floors; `c2bd62fe` pins, CI jobs, `dev/pincheck`, Dependabot; `66cd0915` docs; then this notes
commit.

### Versions and digests (resolved 2026-10-23)

| Item | Value | How it was resolved |
|---|---|---|
| Go toolchain | `toolchain go1.26.10` | newest 1.26 patch per `https://go.dev/dl/?mode=json` (`go1.27.3`, `go1.26.10`) |
| `golang:1.26.10-bookworm` | `sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af` (index) | `docker buildx imagetools inspect` (no pull). Config on all 5 platforms: `GOLANG_VERSION=1.26.10`, `GOTOOLCHAIN=local`, created 2026-10-21 |
| `debian:bookworm-slim` | `sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8` (index, 2026-10-20) | imagetools inspect |
| `postgres:18-bookworm` | `sha256:bbf83594798e144bff365a9c191c5d779f181fb3135902064ab8ff11918020fc` (index; `PG_VERSION=18.6-1.pgdg12+2`, 2026-10-21) | imagetools inspect |
| `localstack/localstack:3` | `sha256:b279c01f4cfb8f985a482e4014cabc1e2697b9d7a6c8c8db2e40f4d9f93687c7` (manifest list; LocalStack 3.8.1, 2024-10-08) | imagetools inspect; equals the local image's `RepoDigests[0]` |
| `actions/checkout` | `11d5960a326750d5838078e36cf38b85af677262` `# v4.4.0` | `git ls-remote --tags https://github.com/actions/checkout 'refs/tags/v4*'`: lightweight tags (no `^{}` peel); `v4` and `v4.4.0` point at this commit |
| `actions/setup-go` | `40f1582b2485089dde7abd97c1529aa768e1baff` `# v5.6.0` | same; `v5` = `v5.6.0` |
| govulncheck | `golang.org/x/vuln` v1.9.0 (2026-10-11, newest) | `go list -m -versions` |

No digest needed a pull, so the artifact has no digest TODO left.

### Deviations from the design (with reasons)

1. **Dependency floors: pgx v5.9.2 and x/text v0.42.0, not v5.11.0 and v0.43.0 (owner question 1).**
   The default targets were applied first, and the SMP gate failed with SMP unchanged:
   `go build` under `-mod=readonly` (the gate's mode) reports
   `go: updates to go.mod needed, disabled by -mod=readonly`. Minimal version selection does
   raise SMP's floors, but SMP's own go.mod must then be edited. That breaks initiative success
   criterion 3 ("at every bump SMP compiles unchanged") and the RULES hard rule (2). It is not a
   go.sum-only adaptation: x/text v0.43.0 also drags x/sync to v0.24.0.
   - The fixed versions are the closest safe alternative. They clear every reachable advisory
     (GO-2026-5004 is fixed in pgx v5.9.2; GO-2026-4771/4772 in v5.9.0; GO-2026-6629 in x/text
     v0.41.0; GO-2026-5970 in v0.39.0). They also equal SMP's own selections at 3addd44b: pgx
     v5.9.2, x/text v0.42.0, x/sync v0.23.0 (x/text v0.42.0 requires exactly x/sync v0.23.0).
   - SMP's module build list is therefore identical before and after this change. Moving to the
     newer versions is routine Dependabot work, done in Spine and SMP together.
   - The exact SMP effect of the default targets is recorded under "SMP handoff", in case the
     owner wants them.
2. **chi is not bumped.** The design bumps chi only "if it clears a reachable advisory".
   GO-2026-5774/5775/5777 are module-level only (`middleware.RealIP`; Spine does not import
   `chi/v5/middleware`), at the base and on the branch.
3. **`go 1.26` → `go 1.26.0`.** `go get` raised it, because x/text v0.42.0 declares `go 1.26.0`.
   SMP already says `go 1.26.0`.
4. **Action majors kept** (checkout v4, setup-go v5), each pinned to the newest release of that
   major, as the orchestrator directed. setup-go v5.6.0 reads only the `go` line (its
   `parseGoVersionFile` regex is `^go (\d+(\.\d+)*)`, read from the source at the pinned SHA), so
   it installs go1.26.0. The go command then switches to the `toolchain` line under the default
   `GOTOOLCHAIN=auto`. The `vuln` job asserts `go env GOVERSION` equals the toolchain before it
   scans, so a silent mismatch cannot pass. Dependabot will propose the newer majors.
   *Superseded in review round 1 (below):* every setup-go step now sets `go-version: '1.26.10'`,
   and pincheck enforces it.
5. **Additions beyond the design**, each small and inside the task's files or adjacent to them:
   - The Makefile's `DOCKER_GO` image is pinned to the same golang digest. It was a third
     floating `golang:1.26-bookworm` that ran `make docker-test` on whatever that tag named.
   - Makefile targets `vuln` and `toolchain-parity`.
   - `persist-credentials: false` on every checkout.
   - An `image-build` CI job, because AC 4's Dockerfile build cannot run locally (images must not
     be built here) and nothing in CI built the Dockerfile before.
   - The `docker-compose` Dependabot ecosystem, since compose files are not covered by `docker`.
   - A 7-day Dependabot cooldown.
   - `dev/` added to `dev/lint-boundaries.sh`'s SMP-import tiers.
6. **The parity check is a Go tool (`dev/pincheck`) with table tests**, not a shell step. It also
   enforces AC 6 and AC 8 (every image digest-pinned, every `uses:` a 40-hex SHA with a
   `# vX.Y.Z` comment) as a regression guard. Its `TestRepository` runs in `go test ./...`.

### Owner-question defaults

- OQ1: see deviation 1. The floors equal SMP's selections, which the alternative also allows
  ("minimal fixed versions"). x/text is v0.42.0 rather than v0.41.0, so that SMP is unchanged.
- OQ2: default. The `vuln` job fails CI on any reachable advisory; module- and package-level
  matches are printed and do not fail (govulncheck's own exit semantics).

**Evidence for deviation 1, gathered after the fact.** The review's pgx v5.11.0 does not just
need an SMP go.mod edit. It **breaks compilation**: v5.11.0 adds `TypeMap()` to the `pgx.Rows`
interface (absent in v5.9.2 and v5.10.0).
- In a scratch copy of SMP, tidied onto the default targets, `go build ./...` fails at
  `internal/customer/stepevents/stepeventstest/memdb.go:365` (`*rows` lacks `TypeMap`). `go vet`
  also fails in five SMP test packages: `customer/projection`, `admin/infrastructure`,
  `admin/tenantadmin`, `customer/auth` and `customer/search`.
- Spine's own test fakes fail the same way under v5.11.0: `adapters/store/subscription_secrets_test.go:138`,
  `adapters/delivery/p31_sweep_test.go:80` and `service/internal/workspace/subscription_secrets_test.go:91`.

### Toolchains in use

- **Spine worktree:** `go.mod` `toolchain go1.26.10`. The host Go is go1.26.5 with
  `GOTOOLCHAIN=auto`, so every Spine gate below ran on go1.26.10 (downloaded once,
  `go: downloading go1.26.10 (darwin/arm64)`). The scaffold-base comparison runs were pinned to
  the same toolchain (`GOTOOLCHAIN=go1.26.10`), so that only the dependency diff differs.
- **SMP builds:** SMP's own `go.mod` `toolchain go1.26.8` governs. The SMP gate ran on go1.26.8.
  Spine's toolchain line is not inherited by an embedder.
- **Local git-floor container:** the present `golang:1.26-bookworm` (`sha256:dc9ad6c0…`,
  `GOLANG_VERSION=1.26.8`, `GOTOOLCHAIN=local`). It compiled with go1.26.8 even though go.mod
  says go1.26.10. That is exactly the drift the parity check exists for. CI runs the pinned
  1.26.10 image.

### Red before green

| Check | Scaffold base `12376b55` | Branch |
|---|---|---|
| `TestRepository` (`dev/pincheck`, copied into a `git archive` of the base) | **FAIL**: 19 refusals: no toolchain line; 9 `uses:` on `@v4`/`@v5`; 9 images without a digest (both golang refs, debian, the CI postgres, 5 compose images). Parity is not evaluated without a toolchain line. The test also fails with "found nothing to compare" (`red-base-TestRepository.log`) | PASS |
| `go run ./dev/pincheck -root <base tree>` | rc=1, 19 problems | rc=0: `OK — toolchain go1.26.10; 3 golang reference(s) on one digest; 9 image(s) and 14 action(s) pinned` |
| `go run ./dev/pincheck -resolve` | n/a | rc=0: `docker.io/library/golang@sha256:632dff73…: GOLANG_VERSION=1.26.10 on linux/386, linux/amd64, linux/arm/v7, linux/arm64, linux/ppc64le` |
| `GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` | exit 3. At go1.26.8: 14 reachable (11 stdlib incl. GO-2026-6809, x/text GO-2026-6629/5970, pgx GO-2026-5004). At go1.26.10: 3 reachable (x/text ×2, pgx) plus package-level pgx GO-2026-4771/4772 | exit 0: "No vulnerabilities found" (symbol and package level). Module-level only: chi GO-2026-5774/5775/5777. **DB updated 2026-10-21 00:27:29 UTC**, scanner govulncheck@v1.9.0, Go go1.26.10 |
| AC 5 mismatched fixtures (`TestCheck`) | n/a (new) | PASS. Each case refuses: "toolchain bumped without the images" (3 refusals: Dockerfile, ci.yml, Makefile), "Dockerfile image bumped without go.mod", floating/minor-only tags, two digests, unpinned refs, mutable action tags, short SHA, missing or major-only version comment. The real pair passes. `TestResolveGolang` refuses a digest whose `GOLANG_VERSION` differs on one platform. |

Also informational: `govulncheck -tags integration,scenario -test ./...` on the branch reports
0 reachable, the same 3 module-level chi IDs.

### Gates (branch head before this notes commit, `66cd0915`; logs in SMP repo `tmp/init027-orchestration/i24/t009/`)

| Gate | Command | Result |
|---|---|---|
| Boundary lint | `./dev/lint-boundaries.sh` (now including `dev:smp`) | `lint-boundaries: OK` |
| Build | `make build` | rc=0 |
| Embed smoke | `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | rc=0, "engine constructed and exercised; library surface is sound." |
| Vet | `go vet ./...`, `-tags integration`, `-tags scenario` | rc=0 ×3 |
| Unit | `go test -p 4 -count=1 ./...` | rc=0, 41 packages ok, 0 failures |
| Race | `go test -race -p 4 -count=1` on `dev/pincheck` plus every importer of a bumped module (`adapters/{delivery,gitpool,store}`, `service/cmd/spine`, `service/internal/workspace`, `testutil`) | rc=0, 7 ok |
| CI `scenario-git-http` job | every step of the job against `s24009_ci_br` | all rc=0, 0 skips. TestGitHTTP_* 87 passes, TestBootstrapInternalAdmin_* 26, TestWorkflowLifecycle_* 130, step-CAS 12, step-evidence 24, status-lifecycle 37+1, governance 19+1, workflow-policy 1. All 29 required `--- PASS:` names present. |
| Integration | `go test -tags integration -p 1 -count=1 ./...` (with a migrated registry DB), branch vs base | Identical failure sets: only the known `TestAWSClient_Contract_LocalStack` and `TestRunModeDatabaseDefault`. Branch 39 ok, base 38 (the extra one is `dev/pincheck`). |
| Scenario | `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (migrated registry), branch vs base | Identical: 31 top-level and 59 total `--- FAIL` lines (the known pre-existing set), 1711 `--- PASS` lines on each |
| git-floor job | `docker run --rm --pull=never` of the present `golang:1.26-bookworm` (go1.26.8, git 2.39.5), worktree and module cache read-only, `GOPROXY=off`: both CI steps plus `./dev/pincheck/` | git 2.39.5; STEP1 rc=0; STEP2 rc=0; pincheck ok on linux. Container removed. |
| golangci-lint v2.11.4 | `--new-from-rev 12376b55` on `./dev/... ./cmd/... ./adapters/delivery/ ./adapters/store/`, plain and `--build-tags integration`; full run on `./dev/...` | 0 issues ×3 |
| gosec | `--enable-only=gosec` on `./dev/...` and on `./...` (branch and base) | 0 / 0 / 0 issues |
| gofmt | `gofmt -l dev/` | clean |
| Modules | `go mod tidy` (no diff), `go mod verify` | unchanged; "all modules verified"; `go list -m` shows pgx v5.9.2, x/text v0.42.0, x/sync v0.23.0, chi v5.2.5 |
| Workflow | actionlint v1.7.12 (`go run …@v1.7.12 .github/workflows/ci.yml`), plus a Ruby YAML parse | rc=0, no findings; base also rc=0. shellcheck is not installed, so run-script linting did not run. 8 jobs: the five existing ones plus `vuln`, `toolchain-parity`, `image-build`; `permissions: {contents: read}` at the top |
| Dependabot | schema validation of `.github/dependabot.yml` against SchemaStore `dependabot-2.0.json` (scratch Go validator, santhosh-tekuri/jsonschema v6.0.3) | VALID. A negative control with ecosystem `gomodx` is INVALID |
| Compose | `docker compose -f docker-compose.yaml config -q`, same for `docker-compose.test.yaml` | both OK |
| `spine --help` | the image-build job's smoke command, run on a host build with an empty environment | rc=0 |

### SMP compatibility gate (SMP `3addd44b`, `../spine` = this worktree, `GOFLAGS=-mod=readonly`, SMP untouched: dirty=0 before and after)

| Step | Result |
|---|---|
| `go build ./...` | rc=0, with SMP unchanged |
| `go vet ./...` plain, `-tags integration`, `-tags scenario ./scenariotest/...`, `-tags faultinject` | rc=0 ×4 |
| `go test -race -p 4` on `cmd/customer-app`, `internal/customer/{githttp,gitread,projectionwriter,spinecore/...,spineembedded/...,stepevents,workspace/...,run/...}`, `internal/engine/...` | Every package ok except `gitread`'s `TestClient_ErrorContractEqualsCLIClient` (`dangling_gitlink`) and `TestClient_SpineReadFileRendersTheGitlinksPatch`. These are the two by-design gitread contract tests, and **they fail identically in a scratch SMP copy against the scaffold base** (`smp-gitread-base.log`), so this is not a compatibility break. |
| Migrations | platform set 116 versions; spine-runtime set 26 versions (`s24009_smp_it`, `s24009_spine_runtime_it`) | rc=0 |
| `dev/testgate` Spine-runtime leg (`spinecore`, `spineembedded`, `runner`, `executionreadiness`; -race) | PASS: 2143 passed, 0 failed, 3 allowed skips |
| `dev/testgate` githttp (`-tags integration`, -race) | PASS: 588 passed, 0 failed, 1 allowed skip |
| ws_* databases and roles | 0 before, 0 after |
| Module build list | `go list -m all`: 480 modules, **identical** between SMP against the scaffold base and SMP against this branch. `go mod tidy` in a scratch copy changes neither go.mod nor go.sum. So no SMP module is MVS-raised (AC 9). |

### SMP handoff (for SMP INIT-027 EPIC-005 TASK-012)

- **At this branch: nothing to adapt.** SMP's go.mod and go.sum need no change, and SMP's build
  list is identical. The §7b row ("MVS raises SMP's pgx, x/text and chi") does not happen: the
  floors equal SMP's own (pgx v5.9.2, x/text v0.42.0, x/sync v0.23.0), and chi stays at v5.2.5.
- **If the owner later takes the review's targets** (pgx v5.11.0, x/text v0.43.0), SMP's tidy
  changes go.mod lines `pgx/v5 v5.9.2 → v5.11.0`, `x/sync v0.23.0 → v0.24.0 // indirect` and
  `x/text v0.42.0 → v0.43.0 // indirect`. In go.sum, six lines are replaced:
  - `github.com/jackc/pgx/v5 v5.11.0 h1:IzBBtyK9AHqf98cctWFifYSci2hgQR/cd56wB4p+ogg=`
  - `github.com/jackc/pgx/v5 v5.11.0/go.mod h1:mal1tBGAFfLHvZzaYh77YS/eC6IX9OWbRV1QIIM0Jn4=`
  - `golang.org/x/sync v0.24.0 h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA=`
  - `golang.org/x/sync v0.24.0/go.mod h1:sUUOizhqBxiL6pEWpqNLUiaJn1ShEbZ6BBqskPbjZm0=`
  - `golang.org/x/text v0.43.0 h1:1QivrlhwAsnMOcqgOdl9my7s9OyZl8O5/UGUTJVXoWc=`
  - `golang.org/x/text v0.43.0/go.mod h1:hbSIYA/amXcRXTFSZqh8tWHEoqRvvbKPJ2uz/KU+a+s=`

  These replace the v5.9.2 / v0.23.0 / v0.42.0 lines. SMP also needs a `TypeMap() *pgtype.Map`
  method on its `pgx.Rows` fakes, at the six sites listed above. Spine would need the same on
  its three fakes.
- SC-01's SMP half (SMP's own `toolchain go1.26.8` and the x/net bump) stays SMP's; this task
  does not touch it.

### Not run here (owner or CI follow-ups)

1. **AC 4 Dockerfile build** and **AC 8 compose bring-up** were not run locally. The rules
   forbid building or pulling images, and neither the pinned golang 1.26.10 image nor the pinned
   postgres image is present locally (the debian slim one is not either). Both digests resolve
   in the registry with the expected config, and `docker compose config` validates. The new
   `image-build` CI job, plus `make test-db-up` on a machine that may pull, are the evidence
   still to collect.
2. **CI itself has not run** (nothing is pushed). The first run on the PR is the first proof of
   four things: setup-go installing go1.26.10 (`vuln`'s GOVERSION assertion),
   `pincheck -resolve` on a GitHub runner (needs `docker buildx` and `git`, both present on
   `ubuntu-24.04`), `image-build`, and the git-floor job on the pinned 1.26.10 image.
3. **Repository settings (INIT-024 §7d owner actions; AC 10 cross-reference):** a `main` ruleset
   that requires PRs and these CI jobs and forbids force pushes; approval for all outside
   contributors; "allowed actions: selected" with SHA pinning required. Every `uses:` is now
   SHA-pinned, so turning that last setting on will not break CI.
4. **pgx v5.11.0 is an interface break** for `pgx.Rows` fakes. A Dependabot pgx PR needs Spine's
   three test fakes fixed, and SMP must fix its six sites before it can take that floor.
5. LocalStack stays on the `3` line (3.8.1, 2024-10). Moving to 4.x is a separate decision.
6. Owner question 1 is answered by deviation 1 (`pgx v5.9.2`, `x/text v0.42.0`), pending the
   owner. Owner question 2 took its default (fail closed).

### Codex

| Round | Head | Review sub-session (model) | Findings | Disposition |
|---|---|---|---|---|
| 1 | `66cd0915` | `01a16831-92d1-7e93-9594-fd0067c77f41` (`gpt-6.1-sol`, `source: {"subagent":"review"}`, cwd = this worktree) | none. "patch is correct", confidence 0.82. Its sandbox had no network, so it could not run `-resolve`. | nothing to fix |
| 2 | `5cf302c6` (final code; adds only these notes) | `01a1684a-6ade-7671-9706-51fd21e6ac39` (`gpt-6.1-sol`, review sub-session, cwd = this worktree) | none. "patch is correct", confidence 0.85. It did not verify registry resolution or image builds. | nothing to fix |

### Review round 1 (2026-10-23, head `9e166eb2`): five P3s fixed

The orchestrator saved the review at SMP repo `tmp/init027-orchestration/i24/t009-reviews/REVIEW.md`.
It had no P1/P2 and five P3s, each fixed per the orchestrator's rulings. Commits:
- `b2fff876`: pincheck rework, ci.yml, dependabot.yml comments;
- `36465947`: docs;
- `29489ef8`: codex round 3 fix;
- then this notes commit.

1. **setup-go cache collision (P3-1).** setup-go stays on v5.6.0 (no major bump).
   - Every setup-go step sets `go-version: '1.26.10'`, replacing `go-version-file`, so no
     toolchain download runs before the cache restore.
   - pincheck refuses a setup-go step whose `with.go-version` is missing or differs from go.mod's
     toolchain, and refuses `go-version-file`. Tests: "setup-go installs another Go", "setup-go
     reads go-version-file", "setup-go without with", and the mismatched-toolchain fixture, which
     now also refuses at `ci.yml:15`.
   - **Follow-up:** setup-go v6, which reads the toolchain directive.
2. **pincheck bypasses (P3-2).**
   - Workflow, composite-action and compose files are parsed with yaml.v3 (`yamlscan.go`). Merge
     keys are expanded, aliases are followed, and a file that does not parse is refused.
   - The walk covers `jobs.*.steps[*].uses`, `jobs.*.uses`, `jobs.*.container` (string or
     mapping), `jobs.*.services.*.image`, `runs.steps`, `runs.image: docker://…` and compose
     `services.*.image`.
   - Dockerfiles (`Dockerfile`, `Dockerfile.*`, `*.Dockerfile`), compose files, Makefiles and
     `.github/actions/**/action.y*ml` are found with WalkDir, skipping `.git/`, `vendor/`,
     `node_modules/` and `testdata/`.
   - Dockerfile scan (`dockerfile.go`):
     - `# syntax=` (parser-directive position only) and `RUN --mount=…,from=<ref>` are image
       references;
     - continuation lines are joined, honouring `# escape=`;
     - `ONBUILD` instructions are scanned too.
   - Any `<registry>/<path>/golang` repository is golang for parity, and `-resolve` inspects a
     mirror path as written.
   - The best-effort `docker run|create|pull` shell scan (`shell.go`) covers `run:` scripts and
     Makefiles. Its residuals are documented in KNOWN-LIMITATIONS ("The scan of shell commands is
     best-effort").
   - Every bypass shape the reviewer listed has a TestCheck case: flow-style step, quoted key,
     `uses :`, flow `container`, flow `services`, `run:` images, Makefile recipe image,
     `RUN --mount from`, `# syntax=`, subdirectory Dockerfiles, composite and Docker actions,
     continued `FROM`, golang mirror path. Two cases go beyond the list: a subdirectory compose
     file, and a reusable workflow on a tag.
   - The already fail-closed shapes stay refused, each with a test: folded `uses: >-`,
     `@main # v4.4.0`, ARG-computed `FROM`, `${{ env }}` image, anchors, aliases and merge keys,
     `FROM --platform`, unparseable YAML, non-literal image.
   - `TestShellScan` covers the shell parser on its own, including an unknown boolean flag
     failing closed.
3. **Tag comment vs SHA (P3-3).** `-resolve` now runs
   `git ls-remote -- https://github.com/<repo> refs/tags/<tag> refs/tags/<tag>^{}` for every
   distinct (repo, comment tag). It requires the peeled commit to equal the pinned SHA, and
   refuses a missing tag or an unreadable remote.
   - Unit tests with a fake resolver (`TestResolveActions`) cover a match, a SHA under another
     tag's comment, a missing tag, a remote error, and one lookup per distinct tag.
     `TestParseLsRemote` covers lightweight, annotated (peeled) and absent tags.
   - Live: the repository passes ("14 of 14 action pin(s) equal their tag's commit").
   - A scratch fixture pinning v4.3.1's commit under `# v4.4.0` and a `# v4.9.9` comment was
     refused twice ("is not v4.4.0: that tag is commit 11d5960a…", "has no tag v4.9.9").
   - Network use is still only in `-resolve`.
4. **Test gap (P3-4).** Added "one golang digest under two tags"
   (`golang:1.26.10-bookworm@D` plus `golang:1.26.10-trixie@D`). It refuses through the `tag !=
   firstTag` arm of `golangParity`, so that mutation is now killed.
5. **Floating refs (P3-5).**
   - Every job runs on `ubuntu-24.04`. pincheck refuses a `*-latest` label (scalar, list or
     `labels:` mapping) and a computed one.
   - pincheck requires every workflow or compose image to equal the compose stack's ref for the
     same repository, with Docker Hub spellings folded together.
   - The `ci.yml` postgres comment now says so. Tests cover a CI/compose mismatch, two compose
     files disagreeing, and a `docker.io/library/` spelling.

**AC 4 / AC 8 unchanged:** not run here, because no docker build or pull is allowed. The
commands for the owner or CI are `docker build --tag spine:ci .` (the CI `image-build` job),
`make docker-build`, and `make test-db-up` / `docker compose -f docker-compose.test.yaml up -d`
followed by `docker compose -f docker-compose.test.yaml ps`, on a machine that may pull.

**Gates for the round** (on `29489ef8`; logs in SMP repo `tmp/init027-orchestration/i24/t009/r1/`):

| Gate | Result |
|---|---|
| `go test -count=1 ./dev/...` | ok |
| `go test -race -count=1 ./dev/pincheck/` | ok |
| `go run ./dev/pincheck` | rc=0: `OK — toolchain go1.26.10; 3 golang reference(s) on one digest; 10 image(s) and 14 action(s) pinned` (the Makefile's `DOCKER_GO` now counts as an image) |
| `go run ./dev/pincheck -resolve` (network) | rc=0: golang digest `GOLANG_VERSION=1.26.10` on 5 platforms; 14 of 14 action pins equal their tag's commit |
| `go mod tidy -diff` | no diff |
| `./dev/lint-boundaries.sh` | OK |
| `go vet ./dev/...` plain / `-tags integration` / `-tags scenario` | rc=0 |
| golangci-lint v2.11.4 `--new-from-rev 12376b55 ./dev/...` and a full `./dev/...` run | 0 issues |
| gosec-only `./dev/...` | 0 issues |
| `gofmt -l dev/` | clean |
| actionlint v1.7.12 on `ci.yml`; Dependabot schema validation | clean; VALID |
| git-floor container (present `golang:1.26-bookworm`, `--pull=never`) incl. `./dev/pincheck/` on linux | all steps rc=0 |
| SMP compat: `GOFLAGS=-mod=readonly go build ./...` in `smp`; `go list -m all` | rc=0; 480 modules, identical to the scaffold-base list; SMP untouched |
| Red before green for the codex round 3 fix | With the committed `b2fff876` code and the new tests, the three new TestCheck cases and two TestShellScan cases FAIL (`red-codex-r3.log`). With the fix they pass. |

**Codex** (plain `codex review --base init/INIT-024-scaffold`, model read from the review
sub-session JSONL):

| Round | Head | Review sub-session (model) | Findings | Disposition |
|---|---|---|---|---|
| 3 | `36465947` | `01a16a26-53ab-73f2-a395-2ebee3158b14` (`gpt-6.1-sol`) | **P2**: local-build exemptions were shared across a workflow's jobs. A tag built in one job exempted a `docker run` of that tag in another job, which runs on a separate runner and pulls it. | **Fixed** in `29489ef8`. The shell scope is now one job (or one action or Makefile). An exemption applies only after the build in text order, and never to `docker pull`. Tests: "a tag built in another job is pulled there", "run before the build, and a pull after it, in one job", and TestShellScan "a run before the build is not exempt" / "a pull of a built tag is not exempt". All are red before the fix. |
| 4 | `29489ef8` | `01a16a29-4c87-7003-a2c7-1d8e359f8c0c` (`gpt-6.1-sol`) | none. "patch is correct". | nothing to fix |

### Integration A (2026-10-27)

Branch `init/INIT-024-integration-a`, cut from Spine local main `83d82409` (= `a560c210` + the
INIT-024 scaffold + TASK-001), in the TASK-008 group's worktree. Merge order (each `--no-ff`,
"Merge <branch> into the INIT-024 integration A branch"): TASK-009 `b8dc7bb3` → `899cd774`;
TASK-002 `07d6ca8a` → `c6bad6c0`; TASK-006 `96dcea55` → `000c5461`; TASK-007 `55a67a45` →
`e6ab7e81`; TASK-008 `c6541bd5` → `8a0a1b29`; TASK-003 `02334613` → `4f502b7e`. Coordination
commits (prefix "INIT-024 integration A — "): `7d0e8522` (TASK-006 × TASK-007), `69c15fb4`
(TASK-007 × TASK-008), `55789b28` (TASK-003 × TASK-007/TASK-008), `377c3343` (TASK-007 ×
TASK-008 logging). Nothing is pushed; the status stays In Progress until GitHub CI has run after a
push.

`CHANGELOG.md` conflicted at every merge (each task adds its own section at the top): every
section is kept once, newest merge on top, each followed by a `---` line.

**This task.** Merged first (`899cd774`), so every later merge and gate ran on `toolchain
go1.26.10` with pgx v5.9.2, x/text v0.42.0 and x/sync v0.23.0. Conflict: `CHANGELOG.md` only
(with TASK-001's section). `docker-compose.yaml` auto-merged with TASK-007's `SPINE_ENV` line
(different keys), and `.github/workflows/ci.yml` with TASK-008's new scenario-git-http step;
`go run ./dev/pincheck` passed after every merge (`OK — toolchain go1.26.10; 3 golang
reference(s) on one digest; 10 image(s) and 14 action(s) pinned`). No coordination commit.
`go mod tidy -diff` is clean and `go mod verify` passes at the head; nothing re-ran `go mod tidy`.
SMP: against the integration head, SMP `82899ace` builds and vets unchanged under
`-mod=readonly` and selects the same pgx v5.9.2 / x/text v0.42.0 / x/sync v0.23.0 / chi v5.2.5,
so **no SMP go.mod or go.sum change** is needed (the handoff above holds).

Gates at `377c3343` (logs in the SMP repo, `tmp/init027-orchestration/i24/int-a/`; heavy legs under
the shared heavy-suite lock):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`, `make build`, embed-smoketest | OK / rc 0 / "library surface is sound" |
| `go vet` plain / `-tags integration` / `-tags scenario` | rc 0 ×3 |
| `go test -p 4 -count=1 ./...` | rc 0 |
| `go test -race -p 4 -count=1 ./...` | rc 0 |
| ci.yml `scenario-git-http`, every run step, own database | every step rc 0, 0 skips: `TestGitHTTP_*` 90 passes, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*` 154, step CAS 12, step evidence 24, status lifecycle 37 + 1, governance 19 + 1, the shared-mode scheduler test; every required `--- PASS` present |
| `go test -tags integration -p 1 ./...` (migrated registry database) | 39 packages ok; only the two known pre-existing failures (`TestAWSClient_Contract_LocalStack`, `TestRunModeDatabaseDefault`) |
| ci.yml `git-floor` (local `golang:1.26-bookworm`, git 2.39.5, go1.26.8 with `GOTOOLCHAIN=local`, `--pull=never`; the pinned 1.26.10 image is not present locally) | both CI steps rc 0, plus TASK-003's budget/hook tests and the integration coordination tests rc 0 |
| golangci-lint v2.11.4 `--new-from-rev main ./...` plain / integration / scenario; `--enable-only=gosec` | 0 issues ×4 |
| `gofmt -l` on the files changed since main | clean, except `service/scenariotest/scenarios/planning_run_test.go`, already unformatted on main (left as is) |
| `go run ./dev/pincheck`; `go mod tidy -diff`; `go mod verify` | OK; no diff; all modules verified |
| SMP compatibility (SMP `82899ace` detached, `../spine` = the integration head, `GOFLAGS=-mod=readonly`, 0 dirty files before and after) | `go build ./...` and `go vet` plain / integration / scenario / faultinject rc 0; `go test -race -p 4 ./...` 151 packages ok, 0 failures; testgate Spine-runtime leg PASS (2447 passed, 3 allowed skips); testgate githttp leg (`-tags integration`, -race) PASS (590 passed, 1 allowed skip); no `ws_*` database or role left |

Codex (`codex review --base main`, plain, from the integration worktree): round 1 at `377c3343`,
review sub-session `01a17e99-5b95-71f3-995c-e5f81a1d37b2`, model `gpt-6.1-sol`: no findings ("No
actionable regressions were identified"). It covered every merged task and coordination commit,
including TASK-003's `5636c02d` and `e57a6741`, which had no task-level codex round.

### Status after GitHub CI (2026-11-04)

- **2026-11-04: stays In Progress.** GitHub CI run 41875102030 at Spine `main` `ce0b9c9e` (this
  initiative's closing commit `fd35f802`, pushed on 2026-11-04 with the owner's approval, plus
  TASK-010's git-identity fix) passed all 8 jobs. That closes what waited for CI: AC 4's Dockerfile
  build (`image-build`: the pinned bases build, and the image runs with the shipped git), AC 2 and
  AC 5 on GitHub (`vuln`, `toolchain-parity`), AC 6 (every pinned job ran), and `git-floor` on the
  pinned `golang:1.26.10-bookworm` image. GitHub's Dependabot runs at `fd35f802` and `ce0b9c9e`
  succeeded (AC 7). Remaining:
  - **AC 8 (a live check):** bring both compose stacks up on a host that may pull the pinned images:
    `make test-db-up`, then `docker compose -f docker-compose.test.yaml ps`, and `make docker-up`.
    GitHub's scenario job pulled and ran the same pinned `postgres:18-bookworm` digest; the compose
    stacks and the pinned LocalStack have not been brought up anywhere.
