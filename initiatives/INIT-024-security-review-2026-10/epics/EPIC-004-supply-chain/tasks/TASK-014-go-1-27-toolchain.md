---
id: TASK-014
type: Task
title: "Go 1.27 toolchain: pins, golangci-lint, and JSON that does not depend on the encoding/json engine"
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
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/tasks/TASK-011-projection-sync-fail-closed.md
---

# TASK-014 — Go 1.27 toolchain: pins, golangci-lint, and JSON that does not depend on the encoding/json engine

---

## Purpose

Dependabot PR #598 proposed moving Spine from Go 1.26.10 to Go 1.27. A read-only review of that
PR built Spine with go1.27.2 and found one test failure, in core/workflow:
`TestProjectionDefinition_RefusesRoundTripFailures/int_key_commit_map`. The owner approved
(2026-11-05) replacing the Dependabot PR with this task.

The toolchain bump itself is mechanical (TASK-009's pins move together). The risk is the JSON
engine. Go 1.27 turns on `GOEXPERIMENT=jsonv2` by default, so the v1 `encoding/json` API now
runs on the v2 implementation. The release notes say v1 behaviour is preserved, but the issue
tracker and a differential probe show several places where it is not. The failing test is one of
them: under Go 1.26 `json.Marshal` refused every `map[interface{}]interface{}`, and the
projection's round-trip check (INIT-023 TASK-011) relied on that refusal for a workflow whose
YAML mapping has a non-string key. Under Go 1.27 the same map marshals to `{"1":"x"}`.

SMP compiles `core/workflow` with its own toolchain, and SMP and Spine may run different Go
versions against the same data. So every JSON use whose bytes or decisions matter must give the
same result under Go 1.26 and Go 1.27.

## Current code (verified at `adeffdf0`)

- `go.mod:3` `go 1.26.0`, `:5` `toolchain go1.26.10`.
- `Dockerfile:8` `golang:1.26.10-bookworm@sha256:632dff73…` (comment at `:2`).
- `.github/workflows/ci.yml`: setup-go `go-version: '1.26.10'` at `:52, :68, :90, :160, :390,
  :438`; the git-floor container `golang:1.26.10-bookworm@sha256:632dff73…` at `:353` (comment
  at `:345`).
- `Makefile:69` `DOCKER_GO` on the same golang digest; `Makefile:77` `GOLANGCI_LINT_VERSION ?=
  v2.11.4` (golangci-lint is not run in CI).
- `core/workflow/projection_definition.go` `encodeProjection`: `yamlsafe.Decode`, decode into
  `any`, `json.Marshal`. yaml.v3 decodes a mapping whose keys are not all plain strings into
  `map[interface{}]interface{}`. Go 1.26 refuses that type in `json.Marshal`
  (`UnsupportedTypeError`), which made `ProjectionDefinition` refuse the file. Nothing in Spine
  refuses it explicitly.

## Design

1. **Pins (TASK-009's rule: they move together).**
   - `go.mod` `toolchain go1.27.3` (the newest 1.27 patch on `go.dev/dl`). The `go` line stays
     `1.26.0`: it is the language and module floor that embedders inherit. Raising it would force
     SMP onto Go 1.27 at its next Spine bump, and Spine uses no Go 1.27 language or library
     feature.
   - `golang:1.27.3-bookworm` by index digest in the Dockerfile, the ci.yml git-floor container
     and the Makefile's `DOCKER_GO`, plus every setup-go `go-version`. The digest is resolved
     fresh, and its config must say `GOLANG_VERSION=1.27.3` on every platform.
   - Every other comment or document that describes the toolchain.
   - `go run ./dev/pincheck` passes, static and with `-resolve`.
2. **golangci-lint v2.15.0** (Go 1.27 support needs v2.13.0 or later) wherever it is pinned. New
   findings of the newer linter are fixed when trivial and safe, otherwise configured or recorded
   with a reason. `--new-from-rev main` reports 0.
3. **The projection refuses non-string mapping keys explicitly.**
   - Before `json.Marshal`, `encodeProjection` walks the decoded value and refuses any
     `map[interface{}]interface{}` with an error that names its location. That is exactly the set
     Go 1.26's `json.Marshal` refused (the refusal was by type, whatever the keys), so the files
     that project are the same under Go 1.26 and Go 1.27, and the stored JSON of an accepted file
     is unchanged.
   - Non-finite floats (`.nan`, `.inf`) are refused in the same walk. Both JSON engines refuse
     them today, but the round-trip property should not rest on the encoder.
   - The check does not use `GOEXPERIMENT=nojsonv2`.
   - Tests pin the refusal for every key shape yaml.v3 can produce (integer, boolean, null,
     float, timestamp-like, tagged string), at every map-valued position `Parse` accepts. The
     walker also has its own test, so the refusal does not depend on `json.Marshal`.
4. **JSON audit.**
   - List every documented 1.26→1.27 difference: the release notes, the v1 package's "Migrating
     to v2" section, and the issues filed against the v1 emulation.
   - Measure them with a differential probe built under go1.26.10, go1.27.3 and go1.27.3 with
     `nojsonv2` as the control.
   - Classify every non-test `encoding/json` call site whose bytes or decisions matter: hashed,
     signed, used as AAD, compared, persisted, an idempotency key, ETag or cache key, an
     allow/deny input, or parsed by SMP.
   - The audit table (site → use → affected? → action) is in the Implementation Notes. Where a
     difference can change bytes or a result, the code is made version-independent or a
     differential test pins it. Tests that pin the v1 behaviours Spine relies on (duplicate names,
     case folding, invalid UTF-8) run in CI.
5. **Gates under go1.27.3** (see the ACs), plus `go test` of every package whose JSON code
   changed under go1.26.10, to prove the version independence.

## SMP impact

- **Required before SMP moves to Go 1.27:** SMP's re-pin must include this task. With an older
  Spine pin, SMP on Go 1.27 compiles `core/workflow` with the v2-backed engine, and the
  projection would accept non-string mapping keys that Spine (and SMP on Go 1.26) refuses. The
  SMP Go 1.27 task (SMP INIT-027 EPIC-005 TASK-019) waits for this commit.
- `go 1.26.0` stays, so SMP can take this commit on Go 1.26 or Go 1.27.
- Spine's `toolchain` line is not inherited by an embedder.

## Decisions

1. **Refuse by type, not by key.** `map[interface{}]interface{}` is refused even when every key
   happens to be a string (a tagged key such as `!!binary aGk=` decodes to one). That is what Go
   1.26 did. Refusing only non-string keys would accept a shape that Go 1.26 SMP refuses.
2. **No `GOEXPERIMENT=nojsonv2`.** It is a temporary opt-out that Go plans to remove, and it
   would not protect SMP, which builds `core/workflow` with its own flags.
3. **`go` line unchanged at `1.26.0`** (Design 1).

## Owner questions

1. **The JSON v2 ruling.** *Default (recommended):* keep refusing non-string YAML mapping keys
   in workflow definitions, explicitly and independently of the JSON engine. *Alternative:*
   accept them, since Go 1.27 renders them as JSON strings. That would differ from SMP on Go 1.26
   and from every row stored so far. **Decided: the default**, under the owner's 2026-11-05
   delegation to Claude's recommendations.

## Acceptance Criteria

1. **Toolchain.** go.mod has `toolchain go1.27.3` and `go 1.26.0`. The Dockerfile, ci.yml
   (git-floor container and every setup-go step) and the Makefile name `golang:1.27.3-bookworm`
   by one index digest. `go run ./dev/pincheck` passes, and so does `-resolve` (the digest's
   `GOLANG_VERSION` is 1.27.3 on every platform).
2. **golangci-lint v2.15.0** is pinned wherever a version is pinned. `--new-from-rev main` reports
   0 issues; any new finding is fixed or recorded with a reason.
3. **Explicit refusal (regression).** `TestProjectionDefinition_RefusesRoundTripFailures` passes
   under go1.27.3, and the new non-string-key tests pass under both go1.27.3 and go1.26.10. The
   refusal names the explicit check, not a JSON-encoder error. Each new test fails on a branch
   without the explicit check under go1.27.3.
4. **Shipped corpus unchanged.** Every shipped workflow is still accepted and stores
   byte-identical JSON (`TestProjectionDefinition_ShippedCorpusUnchanged`).
5. **JSON audit.** The Implementation Notes list every documented difference with its source,
   the probe's measured results, and the audit table. Each site a difference can affect has a
   version-independence fix or a differential test.
6. **Version independence.** `go test` of `core/workflow` and of every package whose JSON code or
   tests changed passes under go1.26.10 too.
7. **Gates under go1.27.3:** `go test ./...`; `-race` on the touched packages; `-tags
   integration` on fresh scratch databases; the ci.yml `scenario-git-http` selection on a scratch
   database; `go vet` plain and with the `integration` and `scenario` tags; `./dev/lint-boundaries.sh`;
   `make build` and the embed-smoketest; pincheck; govulncheck.
8. **Documentation.** CHANGELOG records the toolchain move and the explicit refusal (embedders:
   behaviour unchanged from Go 1.26). README's toolchain section and any other document that
   names the toolchain are current.

## Compatibility

- No runtime schema change. No change to the stored projection JSON of an accepted file.
- Embedders: `ProjectionDefinition` refuses exactly what it refused under Go 1.26, now under
  every Go version. The refusal's error text changes (it names the mapping), and it still wraps
  `ErrProjectionRoundTrip`.

## Out of scope

- The dependency refresh (TASK-013: pgx 5.11, the gomod group, LocalStack).
- SMP's own JSON audit and its Go 1.27 move (SMP INIT-027 EPIC-005 TASK-019).
- Migrating Spine to the `encoding/json/v2` API.

## File overlaps

- TASK-013 edits go.mod's `require` block, go.sum, the compose files, the postgres digest in
  ci.yml and `.github/dependabot.yml`. This task edits go.mod's `toolchain` line, the golang
  references and the setup-go versions. Expect adjacent-line conflicts only.
- `CHANGELOG.md`: each task adds its own section at the top.
- The EPIC-004 table and the initiative's task list: TASK-013 adds its own row.

---

## Implementation Notes

(Filled in as the work lands.)
