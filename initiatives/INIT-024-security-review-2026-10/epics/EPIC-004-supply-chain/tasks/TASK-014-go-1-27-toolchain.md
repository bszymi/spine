---
id: TASK-014
type: Task
title: "Go 1.27 toolchain: pins, golangci-lint, and JSON that does not depend on the encoding/json engine"
status: Completed
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

Implemented on branch `init/INIT-024-TASK-014-go-1-27-toolchain`, cut from Spine `main` at
`adeffdf0`, then rebased onto `origin/main` `24136df6`, which adds Dependabot #600 (checkout
v7.0.1), #603 (setup-go v7.0.0) and #599 (the debian digest). The rebase had no conflict. No SMP
file was changed. Commits (prefix `INIT-024 EPIC-004 TASK-014:`):
- the task artifact;
- toolchain go1.27.3 and golangci-lint v2.15.0;
- the explicit refusal and the JSON-engine tests;
- CHANGELOG and README;
- the ProjectionDefinition step comment;
- these notes.

### Versions and digests (resolved 2026-11-05)

| Item | Value | How it was resolved |
|---|---|---|
| Go toolchain | `toolchain go1.27.3` | newest release on `https://go.dev/dl/?mode=json` (`go1.27.3`, `go1.26.10`) |
| `golang:1.27.3-bookworm` | `sha256:b08bc99468058b6d8a0ada17fa54996b910d5e82a7e95bee0372e3bd98415258` (OCI index) | `docker buildx imagetools inspect` (no pull). Config on all 5 platforms: `GOLANG_VERSION=1.27.3`, `GOTOOLCHAIN=local`. Created 2026-11-05 (linux/ppc64le 2026-10-28). Base `buildpack-deps:bookworm-scm`, so git-floor still tests bookworm's git 2.39.5 |
| golangci-lint | v2.15.0 (newest, `go list -m -versions`) | built locally with go1.27.3 for the gates. v2.11.4 (built with Go 1.26) refuses the module: "the Go language version (go1.26) used to build golangci-lint is lower than the targeted Go version (1.27.3)" |
| govulncheck | v1.9.0 (still the newest) | unchanged |

**The `go` line stays `go 1.26.0`.** It is the language and module floor an embedder inherits.
Raising it would force SMP onto Go 1.27 at its next Spine bump. Spine uses no Go 1.27 language
or library feature: the whole module builds and vets under go1.26.10, and the touched packages'
tests pass there (Gates).

### JSON: documented differences, Go 1.26.10 → Go 1.27.3 (v1 `encoding/json` API)

Sources:
- the Go 1.27 release notes ("New encoding/json/v2 and encoding/json/jsontext packages");
- the Go 1.27.3 v1 package documentation (its "Migrating to v2" section, and its `Marshal`,
  `Unmarshal` and `Decoder` docs compared line by line with Go 1.26.10's);
- the golang/go issues filed against the v1 emulation (searched 2026-11-05).

Each was measured with a differential probe (local scratch, about 250 cases), built under
go1.26.10, go1.27.3, and go1.27.3 with `GOEXPERIMENT=nojsonv2`. The `nojsonv2` control was
byte-identical to go1.26.10 on every line.

| # | Difference | Source | Measured in go1.27.3 | Spine exposure |
|---|---|---|---|---|
| J1 | **Non-string map keys.** v1 rejected `map[interface{}]interface{}` (and float-keyed maps) by type; the emulation encodes any key that marshals to a JSON string | issue 79938 (not planned), 81373 (fixed on tip, not in 1.27.3) | `map[any]any{1:"x"}` → `{"1":"x"}`; string keys accepted; bool, float, nil and struct keys refused with `UnsupportedValueError`; `map[float64]int` → `{"1.5":1}` | **core/workflow `encodeProjection`: fixed** (below) |
| J2 | **Invalid UTF-8 output.** Coerced to U+FFFD in both, but written as the raw 3-byte character, not the `\ufffd` escape | issue 82685 (fixed on tip, not in 1.27.3), 75163 | `"\xff"` → `22efbfbd22` (1.26: `225c756666666422`), in values and keys | bytes only: every persisted JSON is jsonb (re-serialised by PostgreSQL) and every reader decodes the same string; see the audit |
| J3 | Unmarshal: a float overflow leaves `0` in an `any` (1.26: `nil`); both return the error | issue 81062, 81084 | as stated | none: every caller discards the value on error |
| J4 | Unmarshal: object name `"null"` accepted as numeric map key 0; `map[float64]V` now decodes | issue 82872 (open), 81373 | as stated | none: Spine decodes no JSON into a numeric-keyed map |
| J5 | Error text and some error types (`json.Number` → `MarshalerError`, base64 → `UnmarshalTypeError`; offsets) | release notes; issues 79659, 80641, 82870 | measured (texts and types differ) | none decide on them: no `errors.As` on a json error type, no text match. The one text match, `"http: request body too large"`, is the reader's error, passed through unchanged (J8) |
| J6 | `json.RawMessage` is an alias of `jsontext.Value` (gains `String()`, `Clone`, …); `fmt` `%v` prints JSON text | v1 docs; issue 81050 | `%#v` shows `jsontext.Value` | logs only; pgx's `json.RawMessage` case is the same type |
| J7 | New v2 tags honoured by v1 (`embed`, `case:`, single-quoted names, `omitzero`), `format` and `unknown` removed; tag-name parsing changed | release notes; issues 79985, 79921, 81383, 80962 | — | none: Spine's tags are plain names and `omitempty` only |
| J8 | Decoder streaming edges: `Token` after `More` on truncated input (SyntaxError, not EOF); a reader error returned with bytes reported later; `InputOffset`/`Buffered` values | issues 81033 (open), 81853, 79919, 75468, 79498, 80999 | `Token` after `More` differs; a reader error with bytes gives the *next* error | none: Spine never calls `More`, `InputOffset` or `Buffered`. The request decoder's `MaxBytesReader` returned `*http.MaxBytesError` at all 19 cut points probed, the same in both |
| J9 | Methods: `MarshalerTo`/`UnmarshalerFrom` and `encoding.TextAppender` are honoured; the v1 "nil pointer Marshaler" wording is gone | v1 docs; issue 79852 | `SecretValue` redacts in field, pointer, interface, map and slice positions; nil-pointer marshalers print `null` | none: Spine defines no such methods (one value-receiver `MarshalJSON`, one `MarshalText`) |
| J10 | Panics and edge cases: embedded pointer `Unmarshaler` (81244, open), named pointer recursion (75361, open), nil pointer in a Marshaler-embedding interface (82682), embed fallback (81297), `Indent` hang with empty indent and a prefix (81547; backport pending for 1.27.4), unmarshal into `func` (79948), `,string` quoted null (81061, fixed in 1.27.1) | issues | — | none: Spine has none of these shapes, and calls `Indent`/`MarshalIndent` nowhere (the CLI's `SetIndent("", "  ")` has a non-empty indent) |
| J11 | The "Migrating to v2" list (case-insensitive names, `omitempty`, `string`, nil slices and maps, array length, byte arrays, method addressability, map-key methods, deterministic order, HTML and JS escaping, invalid UTF-8 on input, duplicate names, null and merge semantics, `time.Duration`, structural errors) | v1 docs | **identical** under the v1 API (DefaultOptionsV1) | — |

Measured identical (both versions, the probe):
- duplicate names (struct, map, `any`, `map[string]json.RawMessage`; the last wins; objects merge
  into structs);
- case-insensitive matching (ASCII, U+017F and U+212A fold; `_` and `-` are significant; the last
  matching key wins even after an exact match), and `DisallowUnknownFields` with case variants;
- invalid UTF-8 and lone surrogates on input (replaced by U+FFFD) and `\u0000`;
- HTML escaping, U+2028/U+2029 escapes, float formatting (13 float64, 5 float32 values), `-0`;
- nil slices and maps as `null`, `omitempty`, `time.Time`, `[]byte` and `[N]byte`;
- `RawMessage` compaction and HTML escaping, pointer-receiver marshalers, `TextMarshaler` keys
  (the 1.27.2 backport of issue 81355 is in);
- `Encoder` with `SetEscapeHTML` and `SetIndent`, and `MarshalIndent`;
- `Compact`, `Indent`, `Valid` and `HTMLEscape` on 12 inputs (duplicates, invalid UTF-8,
  trailing data, BOM);
- `UseNumber`, `Token` after `Decode`, trailing-data errors, partial results after a type error,
  null into filled values, merge semantics, and array lengths.

### The fix (J1)

yaml.v3 decodes a mapping whose keys are not all plain strings into `map[interface{}]interface{}`.
That covers integer, boolean, null, float and timestamp keys, and any key with a tag other than
`!!str`: a `!!binary` key decodes to a string, but the mapping is still built that way. Go
1.26's `json.Marshal` refused that type whatever its keys. `refuseUnencodable` now refuses it
explicitly, before `json.Marshal`, together with NaN and ±Inf. The error names the location in
sorted key order and never echoes a value.

`TestRefuseUnencodable_EqualsLegacyEncoder` (built only without `goexperiment.jsonv2`) shows that
the walker's verdict equals the legacy engine's `json.Marshal` verdict on every case. It passed
under go1.26.10 and under go1.27.3 with `nojsonv2`.

Shapes found while writing the tests:
- `!!binary ZGVzY3JpcHRpb24=: d` at the top level passes strict Parse as `description`. Under Go
  1.27 without the fix, it projects as `"description":"d"`, a key no YAML viewer shows.
- `!k a: x` (a custom tag) projects as `"a"`.
- The `validation[].config` and `preconditions[].config` maps take non-string keys just as
  `outcomes[].commit` does.

### Red before green

The new tests ran with the base `projection_definition.go` (through `go test -overlay`, the
walker tests mapped out):

| Test | go1.27.3, base code | go1.26.10, base code | Branch, both versions |
|---|---|---|---|
| `TestProjectionDefinition_RefusesRoundTripFailures/int_key_commit_map` (existing) | **FAIL**: accepted, `{"commit":{"1":"x"}}` | pass | pass |
| `TestProjectionDefinition_RefusesNonStringMappingKeys` (13 cases) | **FAIL**: 9 accepted (int, negative int, mixed, block-style int, `!!binary`, custom tag, top-level `!!binary`, validation config, precondition config); timestamp refused only by the DeepEqual; bool, null and float refused only by the v2 encoder | **FAIL**: all 13 refused, but by the encoder (`unsupported type: map[interface {}]interface {}`), not the explicit check | pass |
| `TestProjectionDefinition_RefusesNonFiniteNumbers` (3) | **FAIL**: refused by the encoder only | **FAIL**: the same | pass |
| `TestRefuseUnencodable`, `…_EqualsLegacyEncoder` | n/a (new function) | n/a | pass (`EqualsLegacyEncoder` is built only for the legacy engine) |

### JSON audit: site → use → affected? → action

The audit covers all 58 non-test files that import `encoding/json` (95 call sites, listed with
grep and read one by one), plus pgx's implicit `json.Marshal`/`Unmarshal` of Go values bound to
jsonb columns, and slog's JSON handler. Two facts settle most rows:
- **Every persisted JSON value is a jsonb column** (migrations 001–025). PostgreSQL re-serialises
  it, so J2's byte difference never reaches storage, and a stored value has unique keys and valid
  UTF-8. The one exception is the evidence record, stored inside a jsonb string value: see row 12.
- **No JSON bytes are hashed, compared or used as a key.** The only HMAC covers the exact bytes
  sent. The other `sha256` and `fnv` inputs are file content, tokens, certificate keys, paths,
  commits and run IDs, never encoder output (a secret file's bytes are hashed as read).
  ETags come from a response header, and AAD is length-delimited binary (`crypto.AssociatedData`).

| # | Site | Use (bytes or decision) | Affected? | Action |
|---|---|---|---|---|
| 1 | `core/workflow/projection_definition.go` `encodeProjection` (`json.Marshal` of the yaml-decoded `any`) | stored definition (jsonb, read raw by SMP); the accept/refuse decision of INIT-023 TASK-011; compiled by SMP with its own toolchain | **Yes, J1**: 9 of 13 shapes accepted under 1.27.3. J2 only for a `!!binary` value, which both engines refuse at the DeepEqual | **Fixed**: `refuseUnencodable` + tests (above) |
| 2 | `core/workflow/projection_definition.go` `decodeStoredJSON` (`Decoder`, `UseNumber`, `Token` must be EOF) | `VerifyProjectionDefinition`: keep or delete a stored row | No: the input is our own jsonb; `More` is never called; trailing data is refused in both (probe) | none |
| 3 | `core/workflow/projection_provider.go` `UnmarshalProjectionDefinition` (`DisallowUnknownFields`, case-insensitive) | every projection loader; the round-trip DeepEqual | No: matching, folding (U+017F, U+212A), the order of exact and folded keys, and unknown-field refusal are identical (J11) | none. The TASK-011 smuggling and `VerifyProjectionDefinition` tests pass under 1.27.3 |
| 4 | `core/workflow/skill_references.go:50` | does a stored workflow reference a skill | No (jsonb; as row 3) | none |
| 5 | `adapters/projection/sweep.go` `decodeExactJSON`, `sameWorkflowRow` | upsert or keep a row | No: compares decoded values, not bytes | none |
| 6 | `adapters/projection/workflow_rows.go:167` (`applies_to`), `:335` (rejection event); `service.go:746` (sync event) | jsonb; event payloads | J2 bytes only (a path with invalid UTF-8), normalised by jsonb. The rejection event ID hashes path, commit and action, not JSON | none |
| 7 | `adapters/projection/service.go:807/811` (artifact metadata `map[string]string`, links), `:984/:1004` (protections `[]string`) | jsonb rows; protections feed branch protection | J2 only, normalised | none |
| 8 | `core/branchprotect/projection/rule_source.go:75` | **deny decisions** (protection kinds) | No: jsonb `[]string` | none |
| 9 | `adapters/delivery/signature.go` `parseMetadataObject`, `signatureVersionFromObject`, `tlsFromObject`, `foldedKey` | **allow/deny**: the TLS pin and CA bundle, the signature version | No (J11: duplicates and folding identical). But the code's correctness rests on two engine properties: struct fields match exactly the keys `strings.EqualFold` matches, and a repeated key keeps the last value | **Differential test** `TestMetadataParseMatchesTheJSONEngine` checks both against the engine in use (U+017F in a field and in the block key, delimiters, fullwidth digit, NUL, repeated keys) |
| 10 | `adapters/delivery/signature.go` `WithSignatureVersion` (`json.Marshal` of `map[string]json.RawMessage`) | stored metadata (jsonb) | No: sorted keys, `RawMessage` compaction and escaping identical | none (existing `TestWithSignatureVersion`) |
| 11 | `adapters/delivery/webhook_dispatcher.go:351` `signRequest` over `entry.Payload` (v1 and v2 HMAC) | **HMAC** | No: the MAC covers the exact bytes the request carries, read back from jsonb, never re-marshalled | none (golden vectors in `TestSignatureGoldenVectors`) |
| 12 | `core/engine/output_evidence.go` `evidenceMetaEntry`/`storedEvidence`/`StepOutputEvidence` | evidence JSON stored verbatim inside a commit_meta (jsonb) string, decoded for run detail | No: ASCII content only (SHAs, IDs, fixed reasons); decoding is identical, so a mixed-version read sees the same record | none |
| 13 | `adapters/delivery/subscriber.go:70`, `core/event/router_impl.go:40/64`, `service/cmd/spine/serve_governance_guard.go:158` (override provenance), `service/internal/githttp/handler.go:869`, every event payload in `core/engine/*`, `core/divergence/*`, `core/actor/gateway.go:56/144`, `adapters/scheduler/*`, `adapters/projection/protection_guard.go:258`, `refused.go:115`, `service/internal/gateway/handlers_discussions.go:450`, `handlers_system.go:248`, `core/artifact/service.go:852` | event log, delivery queue (jsonb), webhook bodies; provenance read back for an allow decision | J2 only (git paths or branch names with invalid UTF-8): normalised by jsonb before it is read, signed or compared; receivers decode the same string | none |
| 14 | `adapters/delivery/notify_listener.go:342/365` | which event to fetch | No | none |
| 15 | `core/crypto/envelope.go` (enc:v2) | AAD and key ids | No JSON: AAD is length-delimited bytes; `Envelope.MarshalText` is for logs | none |
| 16 | `adapters/secrets/file.go:102` | the secret value | No: the decoded string is identical (invalid UTF-8, escapes, BOM and trailing data were probed); `VersionID` hashes the raw file | none |
| 17 | `adapters/secrets/client.go` `SecretValue.MarshalJSON` | redaction | No (J9): redacted in every position probed | none (existing tests) |
| 18 | `service/internal/workspace/platform_binding_provider.go:309` | DB, secret and git refs from the platform | No (duplicates, folding, UTF-8 identical) | none |
| 19 | `service/internal/gateway/handlers_helpers.go` `decodeJSON`/`mapDecodeErr` | request admission; 400 vs 413 | No (J8): `MaxBytesError` passes through unchanged at every cut point probed; a second value and trailing data are refused the same | none (existing `TestDecodeJSON_*`, incl. the oversized-tail cases) |
| 20 | `service/internal/gateway/handlers_subscriptions.go:56/66` (`[]byte` base64, `Compact`) | metadata bytes to store, validated by row 9 | No: `Compact` output identical on duplicates and invalid UTF-8; base64 strictness identical (only the error type differs) | none |
| 21 | `service/internal/gateway/handlers_events_stream.go:183` (live SSE) | SSE frame safety | No: U+2028/U+2029 still escaped, and U+0085 and line breaks are handled by the frame writer, not the encoder | none |
| 22 | `service/internal/gateway/response.go` `WriteJSON`, `handlers_discussions.go:310` | API responses, including any an SMP client parses | J2 bytes only: every client decodes the same values | none |
| 23 | `core/engine/candidates.go:123/149` | **eligibility** (required skills, actor types) | No: on any decode error both versions return nil; input is jsonb (J3 cannot change the result) | none |
| 24 | `core/engine/consumer.go:99`, `core/actor/gateway.go:39` | in-process queue round trip | No: one binary marshals and unmarshals | none |
| 25 | `core/validation/engine.go:353/363` | validation decisions | No (jsonb) | none |
| 26 | `core/domain` `ErrorDetail.Scan` and the `json.RawMessage` fields (`divergence.go`, `discussion.go`, `event.go`) | jsonb | No for bytes and decisions (J6: pgx's `json.RawMessage` case is the same type) | none |
| 27 | pgx implicit JSON: `map[string]string`/`[]string` bound to jsonb (`commit_meta`, `repository_baselines`, `repository_branches`, …) and scanned back; `adapters/store/execution_projection.go` | jsonb | J2 only, normalised | none |
| 28 | `core/observe/logger.go` slog JSON handler | logs | J2, J6 in log text | none |
| 29 | `service/internal/cli/*`, `service/cmd/spine/main.go`, `cmd_workspace.go` | CLI display | J5, J6 text only | none |
| 30 | `dev/pincheck/resolve.go`; `service/scenariotest/assert/artifact.go` | dev tool; test helper | No | none |

**Mixed versions.** SMP on Go 1.26 and Spine on Go 1.27, or the reverse, read and write the same
jsonb values. Every decision above decodes identically under both engines. The only stored bytes
that differ by engine are normalised by PostgreSQL. The projection now refuses the same files
under both.

### golangci-lint v2.15.0: new findings

A full v2.15.0 run and a full v2.11.4 run (with go.mod's toolchain set back to 1.26.10 for that
run only) were compared on identical code. Excluding the 249 pre-existing findings both report
(gofmt, gocritic, staticcheck, errcheck), the newer linter found two:
1. **gofmt 1.27 formats one file differently** (`service/cmd/spine/stubstore_test.go`: a
   multi-line composite literal in a multi-value return). gofmt 1.26 refuses the 1.27 layout and
   the reverse, so the values are now built as variables, which both versions leave unchanged.
   Across the repository this is the only file the two gofmt versions disagree on (`gofmt -l`
   with each).
2. SA4026 on a `-0.0` literal in a new test: replaced with `math.Copysign(0, -1)`.

### Gates (go1.27.3 unless stated; head `81ddc6e7`, rebased on `origin/main` `24136df6`; logs in local scratch)

| Gate | Command | Result |
|---|---|---|
| Unit | `go test -p 4 -count=1 ./...` with ci.yml's hermetic git environment | rc=0, 41 packages ok |
| Race | `go test -race -count=1 ./core/workflow/ ./adapters/delivery/ ./service/cmd/spine/` | rc=0 |
| Integration | `go test -tags integration -p 1 -count=1 ./...`, fresh scratch databases (runtime, plus a registry migrated with `migrations/registry/*.sql`) | 39 packages ok; only the known pre-existing `TestAWSClient_Contract_LocalStack` and `TestRunModeDatabaseDefault` fail (the same set as TASK-009) |
| CI `scenario-git-http` | every `run:` step of the job, as GitHub runs it (`bash -eo pipefail`), on a fresh scratch database | all 8 steps rc=0, 0 skips: `TestGitHTTP_*` 90 passes, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*` 154, step CAS 12, step evidence 24, status lifecycle 37 + 1, governance 19 + 1, the multi-workspace scheduler 1 |
| Vet | `go vet ./...`, `-tags integration`, `-tags scenario` | rc=0 ×3 |
| Lint | golangci-lint v2.15.0 `--new-from-rev origin/main ./...`, plain, `--build-tags integration` and `--build-tags scenario`; `--enable-only=gosec ./...` | 0 issues ×4 |
| gofmt | gofmt 1.27.3 and 1.26.10 `-l` on every changed and new Go file | clean under both |
| Boundaries, build, embed | `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; rc=0; "library surface is sound" |
| Pins | `go run ./dev/pincheck`; `-resolve`; `go test ./dev/pincheck/` | OK (3 golang references on one digest, 10 images, 14 actions); `GOLANG_VERSION=1.27.3` on 5 platforms, 14 of 14 action pins equal their tags (checkout v7.0.1, setup-go v7.0.0); ok |
| Modules | `go mod tidy -diff`; `go mod verify` | no diff; all modules verified |
| govulncheck | `GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` | rc=0, no reachable vulnerability. Module-level only: chi GO-2026-5774/5775/5777 (as TASK-009). DB modified 2026-11-02T22:01:43Z, Go go1.27.3 |
| **go1.26.10** | `go build ./...`, `go vet ./...` (plain, integration, scenario); `go test -count=1 ./core/workflow/ ./adapters/delivery/ ./service/cmd/spine/` | build and vet rc=0; tests rc=0, including all five new tests and `TestRefuseUnencodable_EqualsLegacyEncoder` |
| go1.27.3 `GOEXPERIMENT=nojsonv2` | `go test -run TestRefuseUnencodable ./core/workflow/` | ok (the legacy-engine equality test runs here too) |

**Not run here:**
- The `git-floor` and `image-build` jobs. Both need the pinned golang 1.27.3 image, which is not
  present locally, and this task may not pull, build or run containers. GitHub CI is the first
  run of both.
- `TestAWSClient_Contract_LocalStack` (needs a container; pre-existing).

### Codex

| Round | Head | Review sub-session (model) | Findings | Disposition |
|---|---|---|---|---|
| 1 | `ba29ce56` (code complete; base `0e58a6a6`) | `01a1acc2-e2c9-77e3-bc45-aacd16d2ebe2` (`gpt-6.1-sol`, `source: {"subagent":"review"}`, cwd = this worktree) | none. "patch is correct", confidence 0.88. Its sandbox could not bind test ports, so it ran the touched tests only | nothing to fix |
| 2 | `7116dbcf` (final code and these notes; base `24136df6`) | `01a1ad4d-2511-7081-bfcb-58743a9d10e1` (`gpt-6.1-sol`, review sub-session, cwd = this worktree) | none. "patch is correct", confidence 0.90. The same sandbox limit on port binding | nothing to fix |

### SMP handoff

- **SMP's Go 1.27 move must pin a Spine commit that contains this task** (SMP INIT-027 EPIC-005
  TASK-019 waits for it). At SMP's current pin, `core/workflow` compiled by Go 1.27 projects the
  workflows listed under "Red before green".
- Nothing else to adapt. The go line stays 1.26.0, no exported API changed, and
  `ErrProjectionRoundTrip` still wraps every refusal (SMP's `workflow_backfill.go` matches it with
  `errors.Is`). Only the refusal's text changes.
- SMP's own JSON audit is SMP's task. The engine differences measured here (J1–J11, with the
  probe) apply to SMP's code unchanged.
- When TASK-013 (pgx v5.11.0) and this task are both merged, the combined head needs one gate run
  under Go 1.27: the pgx `Rows` fakes and TASK-013's connection-string tests have not run under
  Go 1.27 together.

### After the push

- Watch every ci.yml job, and especially these, which run under Go 1.27.3 for the first time:
  - `toolchain-parity` (pincheck `-resolve` on the runner);
  - `vuln` (its GOVERSION assertion: setup-go v7.0.0 must install 1.27.3);
  - `git-floor` (the pinned 1.27.3 image, bookworm git 2.39.5);
  - `image-build` (the Dockerfile on the new builder);
  - `scenario-git-http`;
  - `test`.
- Dependabot #598 is replaced by this task and can be closed after the merge.
