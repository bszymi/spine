---
id: TASK-004
type: Task
title: "Read-path robustness: streamed listings, no DWIM fallback for reserved names, unforgeable log records, bounded evidence diff"
status: Pending
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-003-bounded-reads-and-nul-delimited-listings.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-002-ref-grammar-and-argv-hardening.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-008-submit-and-state-write-integrity.md
---

# TASK-004 — Read-path robustness: streamed listings, no DWIM fallback for reserved names, unforgeable log records, bounded evidence diff

---

## Purpose

This task closes four P3 findings from the 2026-10-22 review (N-6, N-10, N-11, N-12; detail in
SMP's git-ignored `tmp/security-review-2026-10-22/spine/REPORT.md`). Each is an edge of the
read path that INIT-023 TASK-002/TASK-003 hardened. The fixes stay inside `adapters/git` and one
engine call site.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| N-6 | P3 | Both; **SMP ships it**. SMP builds Spine's `CLIClient` through `gitread.NewClient` (SMP `internal/customer/gitread/client.go:123`). | A tree with a very large path listing reaches the authoritative branch. Every later `FullRebuild`/`IncrementalSync` then fails until the change is reverted. This fails closed for governance, but it is a persistent per-workspace outage. |
| N-10 | P3 | Both | A non-branch ref named like a deleted branch, typically a run branch after cleanup. Planting one needs operator push rights (INIT-023 TASK-016) or a ref that pre-dates the upgrade. |
| N-11 | P3 | Standalone (`/query/history`) | Any author of a commit whose history is queried |
| N-12 | P3 | Standalone (wave 4 output evidence) | A run-branch author who chooses the commits the evidence diff compares. The 3 s timeout bounds the time but not the git child's memory. |

## Current code (verified at `a560c210`)

- **N-6.**
  - `ListFiles` (`adapters/git/cli.go:1452`) and `Diff` (`cli.go:881`) collect the whole output
    under the per-command cap, `DefaultMaxOutputBytes` = 16 MiB (`adapters/git/limits.go:80`).
    Past the cap they fail with `ErrOutputTooLarge`.
  - `artifact.DiscoverAll` and `DiscoverWorkflows` list everything and filter afterwards
    (`core/artifact/discovery.go:90`, `:439`).
  - Callers: `FullRebuild` (`adapters/projection/service.go:146`), `IncrementalSync`'s `Diff`
    (`:504`), the branch-protection lookup (`:933`, `ListFiles` with a pattern) and the sweep
    (`adapters/projection/sweep.go:146, :168`).
- **N-10.** `resolveRev` (`adapters/git/refgrammar.go:274`) rule 4: a bare valid branch name is
  looked up exactly (`:292`). When `refs/heads/<n>` is missing, the name passes through to git's
  DWIM (`:299`), so `ReadFile`, `RefSHA`, `Diff`, `ListFiles` and `Log` resolve a same-named tag or
  `refs/<n>`. One caller that hits this is the evidence querier, which reads `run.BranchName` for
  terminal runs whose branch is usually deleted (`core/evidence/querier.go:146-147`).
- **N-11.** `Log` formats records with a literal `---END---` (`cli.go:1015`), and `parseLogOutput`
  splits on it (`cli.go:1881, :1884`). A message containing the separator changes the record
  boundaries. It feeds `QueryHistory` (`adapters/projection/query.go:155`) →
  `GET /query/history` (`service/internal/gateway/handlers_query.go:80`). The dormant
  `HasCommitWithTrailer` (`cli.go:1559-1566`) uses the same pattern.
- **N-12.** `branchChangeAtSubmit` diffs `PreviousSHA..HeadSHA` with `o.git.Diff`
  (`core/engine/output_evidence.go:161`, the call at `:206`). `diff` is class `opDiff`, so it runs with no
  `GIT_ALLOC_LIMIT` because rename detection loads blob pairs (`cli.go:1657, :1676`;
  `limits.go` header). `evidenceDiffTimeout` is 3 s (`output_evidence.go:145`).

## Design

1. **N-6: listings bound what they keep, not what git prints.**
   - `ListFiles` and `Diff` parse their NUL-delimited stdout as a stream (new
     `adapters/git/listing.go`) instead of collecting it.
     - Kept bytes (the paths retained) stay under the per-command limit.
     - Each path must be 4,096 bytes or less.
     - Exceeding either is the same `ErrOutputTooLarge`, so a refusal still fails closed.
     - The raw output size is no longer a limit.
   - `ListFiles` applies `pattern` while streaming.
   - A new optional capability, found by type assertion so no interface SMP implements grows a
     method (initiative §5):
     `type PathFilterLister interface { ListFilesFunc(ctx, ref string, keep func(path string) bool) ([]string, error) }`.
   - `DiscoverAll`, `DiscoverWorkflows` and the sweep pass their existing predicates through it,
     falling back to `ListFiles`.
   - A tree with a huge listing but few artifact paths then projects normally. A tree with too
     many artifact paths still fails closed.
   - The checkout budget that TASK-003 defines for N-1 caps the total path bytes of any tree Spine
     merges at or below this limit, so nothing a governed merge brings in can wedge the projection.
2. **N-10: no DWIM fallback for reserved or home names.**
   - In `resolveRev` rule 4, when `refs/heads/<n>` is missing and `<n>` is in the reserved
     namespace (first component folds to `spine`, the `refadmit.Classify` rule) or is the client's
     home branch, return `ErrKindNotFound`. No passthrough.
   - Every other bare name keeps today's passthrough (tags, abbreviated ids, `origin/main`), so the
     public `?ref=` reads keep working (Decision 1).
3. **N-11: unforgeable record boundaries.**
   - `Log` and `HasCommitWithTrailer` use a per-call random boundary, 128 bits from
     `crypto/rand`, in the `--format` string.
   - `parseLogOutput` takes the boundary and requires the first field of every record to be a
     full object id (`IsFullObjectName`).
   - A record that fails the check fails the call (`ErrKindPermanent`, "unparseable log output").
     It is never skipped.
4. **N-12: a diff that loads no blobs.**
   - New optional capability
     `type ChangedPathCounter interface { CountChangedPaths(ctx, from, to string) (int, error) }`,
     implemented by `CLIClient` as `git diff --name-only -z --no-renames <from> <to> --`, class
     `opRead` (so `GIT_ALLOC_LIMIT` and the pinned delta-base cache apply; with no rename
     detection, no blob is read).
   - `branchChangeAtSubmit` type-asserts `o.git`. It falls back to `Diff` when the capability is
     absent, which is unchanged behaviour for operators without it.

## SMP impact

- **Shared at the bump** (initiative §7b Rule 4):
  - N-6 relaxes listings: what failed may now succeed, and a large kept set still fails.
  - N-10: a bare reserved-namespace name with no branch is NotFound.

  The SMP companion task re-runs SMP's `gitread`, `projectionwriter` and `spinecore` suites.
- **SMP-side follow-up:** SMP's own history reader uses the same separator
  (`internal/customer/spinecore/artifacthistoryreader.go:178, :228` at SMP `3addd44b`). It needs
  its own fix.
- N-11 (Spine `Log`) and N-12 are inert for SMP: SMP's divergence client stubs `Log`, and its
  operator falls back to `Diff`.

## Decisions

1. **N-10 is narrow.** Only reserved and home names lose DWIM. Removing DWIM for every bare name
   would break `?ref=<tag>` and abbreviated-id reads on the public API (`api/spec.yaml:207`,
   `:465`).
2. **N-6 keeps `ErrOutputTooLarge`.** Callers and SMP already map it. There is no new error type.
3. **No per-repository listing-size knob.** The existing `WithMaxOutputBytes` bounds kept bytes.

## Owner questions

1. **N-10 scope.** *Default:* narrow (Decision 1). *Alternative:* every branch-shaped name resolves
   through `refs/heads` only, with tags required as `refs/tags/<n>` and abbreviated ids refused.
2. **N-12 fallback.** *Default:* `Diff` when the capability is absent. *Alternative:* record
   `diff_failed:unsupported` rather than run an unbounded diff.

## Acceptance Criteria

1. **Regression: N-6 wedge** (`adapters/projection`, `TestFullRebuild_LargeListingFewArtifactsProjects`).
   - This ports `scratch/secrets-delivery-projection/projection/zz_outputcap_wedge_test.go`: a
     4 KiB output cap, and a commit whose listing is over the cap but holds two artifacts.
   - `FullRebuild` and `IncrementalSync` succeed and project both artifacts. Both fail at
     `a560c210`.
2. **N-6 still fails closed** (`TestListFiles_KeptBytesOverLimitRefused`). Kept paths over the cap
   give `ErrOutputTooLarge`, as does a 4,097-byte path.
3. **N-6 stream contract** (`TestListFiles_StreamedEqualsBuffered`). Over the existing `-z`
   corpus (C-quoted, leading-space, non-ASCII paths), the output is byte-identical to the
   `a560c210` implementation.
4. **Regression: N-10** (`adapters/git`, `TestResolveRev_DeletedRunBranchNeverResolvesShadow`).
   - This ports the residual lines of `scratch/git/f4/zz_rv_f10_test.go`: delete `spine/run/x`,
     then plant `refs/tags/spine/run/x` and `refs/spine/run/x`.
   - `ReadFile` and `RefSHA` of `spine/run/x` return `ErrKindNotFound`. At `a560c210` they
     return the shadow's content and SHA.
5. **N-10 controls** (`TestResolveRev_TagsAndAbbrevStillResolve`). `v1.0`, a 10-hex abbreviated
   id and `origin/main` resolve as at `a560c210`.
6. **Regression: N-11** (`TestLog_MessageCannotForgeRecords`).
   - This ports `scratch/git/f4/zz_rv_log_test.go`: two commits whose messages embed
     `---END---` plus a fake record.
   - `Log` returns exactly two records with the true SHAs. At `a560c210` it returns three.
7. **N-11 fails closed** (`TestParseLogOutput_RejectsMalformedRecord`). A record whose first
   field is not a full object id is an error.
8. **Regression: N-12** (`core/engine`, `TestOutputEvidence_DiffUsesNoRenameBoundedCounter`).
   - A fake git client that implements `ChangedPathCounter` is called with the author's pair, and
     `Diff` is never called.
   - `adapters/git` `TestCountChangedPaths_UnderAllocLimit`: a rename pair of two 8 MiB blobs
     under a 1 MiB blob limit is counted without error and without reading the blobs. The
     command runs as `opRead`, which the test asserts through the env hook.
9. **Existing suites.** `go test ./adapters/... ./core/...`, the `git-floor` job (git 2.39.x) and
   the scenario suite pass, except the known pre-existing failures.
10. **SMP compatibility gate** (initiative §5), including the SMP `gitread` and
    `projectionwriter` suites, passes with no SMP change.
11. **Documentation.** `KNOWN-LIMITATIONS.md` "Git read bounds" (kept-bytes rule, N-10 rule) and
    `CHANGELOG.md`.

## Compatibility

- There is no API break. The new capabilities are optional interfaces.
- `ListFiles`/`Diff`: strictly fewer refusals.
- `resolveRev`: NotFound for missing reserved and home names.

## Out of scope

- SMP's history reader, which is SMP-side.
- Rename-aware evidence (renames count as delete plus add).

## File overlaps

- `adapters/git/cli.go`: this task touches `ListFiles`, `Diff`, `Log`, `parseLogOutput`,
  `HasCommitWithTrailer` and the new `CountChangedPaths`. TASK-003 touches `Checkout`, `Merge`
  and `runMerge`. They are different functions, and this task rebases on TASK-003.
- `adapters/git/limits.go`: additive.
- `refgrammar.go`, `listing.go`, `core/artifact/discovery.go`, `adapters/projection/sweep.go` and
  `core/engine/output_evidence.go`: this task only.
