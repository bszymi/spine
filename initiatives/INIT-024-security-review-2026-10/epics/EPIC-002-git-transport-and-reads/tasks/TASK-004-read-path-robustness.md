---
id: TASK-004
type: Task
title: "Read-path robustness: streamed listings, no DWIM fallback for reserved names, unforgeable log records, bounded evidence diff"
status: Completed
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-11-04
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
     - ~~Each path must be 4,096 bytes or less.~~ *(Dropped in review round 1, P3-1: no per-path
       limit; the kept-bytes limit bounds memory. See "Review round 1".)*
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
- **Carried to SMP EPIC-005 TASK-018** (review round 1, P4-7; recorded, not done here): SMP's
  `gitread.Client.resolveRevLikeSpine` (`internal/customer/gitread/client.go:333`, SMP `82899ace`)
  documents Spine's resolution rules 1–4 only. Its behaviour is right — a missing reserved or home
  name passes through, is not batch-eligible, and Spine's own `ReadFile` then answers NotFound
  (rule 5) — but the comment should name rule 5 so the two never drift.
- **Carried to SMP EPIC-005 TASK-018** (recorded by TASK-004, not done here): SMP's
  `HistoryReader` formats `git log` with the same fixed separator
  (`internal/customer/spinecore/artifacthistoryreader.go:178` `historyLogFormat`, split at `:228`,
  unchanged at SMP `82899ace`), so a commit message can forge SMP's history entries the way N-11
  forged Spine's. It needs the same fix: a per-call random boundary and a strict parse whose first
  field must be a full object id, failing the call on any record that does not parse.

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
   give `ErrOutputTooLarge`. ~~as does a 4,097-byte path~~ *(amended in review round 1, P3-1: a
   path of any length is listed, kept or dropped, and diffed; only the total binds — a per-path
   refusal was a new projection wedge for embedders. `TestListFiles_KeptBytesOverLimitRefused`
   now pins that 4,096-, 4,097- and 9,000-byte paths list.)*
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

## Implementation Notes

*(Implementation, 2026-10-27. Branch `init/INIT-024-TASK-004-read-path-robustness`, cut from
`init/INIT-024-wave3-base` `55789b28` (integration A with TASK-003 merged). Every line reference
above was re-located on that base: the functions this task changes are byte-identical between
`a560c210` and the base (`Log`, `ListFiles`, `Diff`, `HasCommitWithTrailer`, `parseLogOutput`,
`parseNameStatusZ` hash the same; `refgrammar.go`, `discovery.go`, `sweep.go`, `service.go` and
`output_evidence.go` are unchanged), so a red run at the base is a red run at `a560c210`. Run on
macOS, git 2.54.0, go 1.26.10, and on git 2.39.5 in the local `golang:1.26-bookworm` image for the
`git-floor` job. Scratch: SMP repo `local-scratch/i24/t004/`.)*

### Rulings and owner-question defaults (recorded, pending the owner)

- **OQ1 — default taken: narrow.** Only reserved-namespace and home-branch names lose DWIM;
  `?ref=<tag>`, abbreviated ids and `origin/<n>` resolve as before (AC 5).
- **OQ2 — default taken: `Diff` fallback** for an operator without `ChangedPathCounter` (SMP's
  `RealGitOperator`), byte for byte today's count.

### What was built

| Commit | Change |
|---|---|
| `9e51a03b` | Status In Progress. |
| `7de79a2b` | **N-6.** `adapters/git/listing.go` (new): `nulRecords`, the streamed NUL-record reader (bounded carry, stop + cancel at the first refusal, unterminated check), moved out of TASK-003's `treeListingSink`, which now embeds it (handoff 1); `pathCollector` (ListFiles), `nameStatusCollector` (Diff), `pathCounter` (counts, keeps nothing), `runListing` (runSink's read/diff environment and error order). `ListFiles` applies its pattern while git lists; new `ListFilesFunc`; kept bytes (path + NUL) held to `listingLimit()`, each kept path to `MaxListingPathBytes` (4096), refusal = the same permanent `ErrOutputTooLarge` envelope (`OutputTooLargeError.PerPath` new). `limits.go`: `DefaultMaxListingBytes` = `DefaultTreeMaxPathBytesTotal + DefaultTreeMaxEntries + 1 MiB`, `listingLimit()` (explicit `WithMaxOutputBytes`, else the default, never below a carried budget's path bytes + entries + 1 MiB). `client.go`: `PathFilterLister`, `FileLister`, `ListFilesKeeping` (fallback: filtered `ListFiles`). `DiscoverAll` / `DiscoverWorkflows` (`discoveryCandidate`), the workflow-row sweep (`isRowPath`) and `NextID` list through it. |
| `bc68c80d` | **N-10.** `resolveRev` rule 5: a missing `refs/heads/<n>` for a name `refadmit.Classify` does not call an ordinary branch, or for the tree's home branch, is `branchNotFound` (ErrKindNotFound). |
| `fa5a3308` | **N-11.** `newLogBoundary` (`spine-log-` + 128 bits of `crypto/rand`) before every record of `Log` and `HasCommitWithTrailer`; `logRecords` / `parseLogOutput(output, boundary)` require five fields and a full object id first, else permanent "unparseable log output"; argv test normalises the boundary. |
| `29ad195c` | **N-12.** `CLIClient.CountChangedPaths` (`diff --name-only -z --no-renames --ignore-submodules=none [eoo] <from'> <to'> --`, op `count_changed_paths`, opRead, streamed count); `core/engine.ChangedPathCounter`; `branchChangeAtSubmit` counts through `countChangedPaths` (capability, else `len(Diff)`). Op-class table test pins the op and now scans `runListing` calls. |
| `2b6cf56a` | **Handoff 3.** `Diff` passes `--ignore-submodules=none`. |
| `e7120481` | **Handoff 4.** `service/internal/githttp/protocol_path.go` (new): git-http-backend's routes anchored at the repository root; `isInfoRefs`, `isReadOnly`, `IsReceivePack` match them exactly (only these functions change in `handler.go`). Gateway: `withGitPath`, push/read classified on the git path the handler is handed. |
| `5c326da5` | Docs: KNOWN-LIMITATIONS ("Git Read Bounds", "Git Ref Arguments", "Git HTTP"), CHANGELOG, the `CLIClient` header. |
| (this commit) | Implementation Notes; SMP impact carried item. |

### Acceptance criteria — evidence

"Red" = the test run with `go test -overlay` mapping every changed `.go` file back to its base
content (new files deleted) and keeping only the test under test, worktree untouched
(`t004/mkbase-overlay.sh`, logs `t004/red/red-*.log`); for the engine test a compile-only shim
declares `ChangedPathCounter` with the base's behaviour (nothing asserts it). "Green" = the branch.

| AC | Status | Evidence |
|---|---|---|
| 1 | done | `TestFullRebuild_LargeListingFewArtifactsProjects` (4 KiB cap; 300 long-named files, a listing of ~2-3x the cap; two artifacts and a workflow). FullRebuild and IncrementalSync (a fresh instance whose first sync sweeps the stored workflow row and lists at head) both end idle at head with both artifacts and the workflow. **Red:** FullRebuild "discover artifacts: git list_files: output exceeds read limit"; IncrementalSync state `error` "1 sync errors" (the sweep's listing). A third subtest pins the residual: a window whose own diff is over the cap fails closed (`ErrOutputTooLarge`, state error at the previous commit), an operator FullRebuild recovers, the next window syncs (red at the base too: the rebuild itself failed). |
| 2 | done | `TestListFiles_KeptBytesOverLimitRefused`: kept paths over the cap (`ListFiles("")`, `ListFiles("bulk/")`, `ListFilesFunc(nil)`, a predicate keeping the bulk, `Diff`) are the permanent `ErrOutputTooLarge` with `Limit` 4096, `PerPath` false; the same listing with `*.md` or a `docs/` predicate returns the two files; a 4,096-byte path lists, a 4,097-byte path is refused (`PerPath`, Limit 4096) by `ListFiles("")`, `ListFiles("*.md")` and `Diff`; a 4,097-byte path the predicate drops is no refusal (Deviation 2). New behaviour of new API: no red. |
| 3 | done | `TestListFiles_StreamedEqualsBuffered`: the `-z` corpus (INIT-023's C-quoted, leading-space, tab, newline, non-ASCII paths plus nesting, a trailing space, CJK) through 8 patterns: `ListFiles` equals the a560c210 parse (copied verbatim into the test) of the same `ls-tree` output, and the collector fed in every chunk size 1..len equals it; malformed streams give the same refusal text. `TestDiff_StreamedEqualsBuffered`: a real rename, modify, delete, add, type change over the corpus, with and without copy detection, in every chunk size, equals the a560c210 `parseNameStatusZ`; ten malformed shapes give the same error text, each matching `errMalformedDiff`. `TestNULRecords_CarryIsBoundedByTheOwner`. |
| 4 | done | `TestResolveRev_DeletedRunBranchNeverResolvesShadow`: `spine/run/x` deleted, shadow at `refs/tags/spine/run/x`, at `refs/spine/run/x`, and both (git's own `rev-parse` resolves the shadow, asserted): `ReadFile`, `RefSHA`, `Diff` (both sides), `ListFiles`, `TreeEntryType`, `MergeBase`, `Log{Since}` are ErrKindNotFound; every reserved shape (`spine/plan/…`, `spine/init`, `spine/run-…`, a divergence shape, `SPINE/run/y`) and a missing home branch (two clients of the tree) likewise; an existing branch still wins. **Red:** every read returned the shadow's content / id (`red-ac4.log`). |
| 5 | done | `TestResolveRev_TagsAndAbbrevStillResolve`: `v1.0`, a 10-hex abbreviated id, `origin/main`, `spine-tools` (first component not the namespace), `release/r1`, `t1` resolve (RefSHA, ReadFile, ListFiles) as at the base (the test passes at the base too, in `red-ac4.log`). |
| 6 | done | `TestLog_MessageCannotForgeRecords`: two commits whose messages carry the old separator, a record-shaped tail (and a boundary-shaped line): `Log` (plain, with path and limit, with since) returns exactly the true commits with their ids, author and full messages; `HasCommitWithTrailer` of the injected trailer returns a real commit's id. **Red:** "returned 4 entries for 2 commits" (`red-ac6.log`). |
| 7 | done | `TestParseLogOutput_RejectsMalformedRecord`: a non-id, abbreviated id, id with a trailing space, a later bad record, too few fields, text before the first boundary, a record not on its own line, the old separator — each an error (`errMalformedLog`, never echoing the output) and no commits; well-formed output parses as before (message, trailers, author, date; empty message); boundaries differ per call. New parser signature: no red. |
| 8 | done | `TestOutputEvidence_DiffUsesNoRenameBoundedCounter` (core/engine): a fake operator with the counter — changed (count 4), moved head with nothing changed (unchanged, `head_moved_tree_unchanged`, flagged), failed count (`diff_failed:permanent`): `CountChangedPaths(previous, head)` once, `Diff` never. **Red:** `Diff` ran (`red-ac8.log`). `TestCountChangedPaths_UnderAllocLimit` (adapters/git): two 8 MiB blobs renamed with one change, plus an edit, under a 1 MiB blob limit and a ~1 MiB `GIT_ALLOC_LIMIT`: count 3, no error; the env hook shows `count_changed_paths` ran with `GIT_ALLOC_LIMIT=<limit>` and the pinned cache (opRead, also pinned in the class table); control: the same pair with `-M` under the same limit dies on git's allocation refusal; `Diff` (opDiff) still finds the rename without the limit; grammar and N-10 resolution as Diff's. `TestCountChangedPaths_StreamContract`. |
| 9 | done | Gates below (`go test ./...`, `git-floor` on 2.39.5, the scenario-git-http job). |
| 10 | done | SMP gate below; no SMP change. |
| 11 | done | KNOWN-LIMITATIONS "Git Read Bounds" (listings bound kept paths, the two residuals, the evidence counter, Diff and submodule settings, log records), "Git Ref Arguments" (N-10 rule; the `?ref=` residual narrowed), "Git HTTP" (exact protocol paths); CHANGELOG (Security / Changed (operators) / Changed (embedders)). |

### Carried handoffs

1. **TASK-003's listing core** — done. `nulRecords` (listing.go) is the one reader; `treeListingSink`
   embeds it with its own per-record bound (the remaining path budget + record meta) and its
   record parser unchanged; every TASK-003 test passes unedited (`go test ./adapters/git/`).
   `TestListingLimit_AdmitsEveryBudgetedTree` reads both constants: `DefaultMaxListingBytes` >=
   `DefaultTreeMaxPathBytesTotal + DefaultTreeMaxEntries + 1 MiB` and > `DefaultMaxOutputBytes`;
   a client with a raised `WithTreeBudget`, or under a raised `SetDefaultCheckoutLimits`, lists at
   least that budget's path bytes + entries + 1 MiB, even with a small explicit
   `WithMaxOutputBytes`.
2. **Overlaps in adapters/git** — kept additive: `runSink` and `commandEnv` untouched (the
   listings call `commandEnv` from `runListing`); `opClasses` gains one entry after a comment
   line, so gofmt realigned no neighbour; `limits.go` gains a new const block after TASK-003's and
   one comment paragraph of `DefaultTreeMaxPathBytesTotal` is rewritten (it described this task's
   requirement); `WithMaxOutputBytes` records that it was set.
3. **Submodule ignore settings** — `CLIClient.Diff` in a non-bare tree DID honour them: verified
   on git 2.54 (`submodule.<name>.ignore = all` in the checked-out `.gitmodules`, and separately
   `diff.ignoreSubmodules = all`, each made `git diff --name-status <c1> <c2>` print nothing for a
   moved gitlink; a bare clone of the same repository printed `M`; `ignore = dirty/untracked` did
   not hide it) and on 2.39.5 (the test asserts the premise in the git-floor run). Fixed:
   `--ignore-submodules=none` on `Diff` and `CountChangedPaths` (`TestDiff_SubmoduleIgnore…`, red
   at the base: an empty diff; `TestCountChangedPaths_SubmoduleIgnore…`). `ListFiles` and
   `ls-tree`-based reads are not affected (verified). SMP's own `diffNameStatusZ` and allocator
   already pass the flag (SMP TASK-010).
4. **githttp classification** — done inside the classification functions (`isInfoRefs`,
   `isReadOnly`, `IsReceivePack`; the route table is a new file, `protocol_path.go`, so the
   `handler.go` diff touches nothing TASK-005 changes — no import, no `ServeHTTP` line). The
   gateway had to change too: it classified push/read on the full `/git/<ws>/…` URL with the
   same suffix rules, and an exact match there would have made every push a token-less read for
   a trusted peer (mutation M18 shows a trusted peer getting the receive-pack advertisement
   without a token). TASK-006's `..` residual: there was no KNOWN-LIMITATIONS line for it (it was
   recorded only in TASK-006's artifact "Follow-ups"); a new "Git HTTP" bullet records the fix,
   and TASK-006's artifact is left as merged.
5. **SMP history reader** — recorded under "SMP impact" for SMP EPIC-005 TASK-018; SMP untouched.
6. **Owner questions** — defaults kept (above).

### Adversarial self-review

| # | Attempt | Result |
|---|---|---|
| 1 | A huge listing whose paths the caller drops (N-6's shape) | Read and dropped; memory = the kept paths + one carried record (bounded by the room left, never less than 4,096 bytes) + that record's transient string. AC 1/2. |
| 2 | One enormous dropped path (bigger than the room left) | Refused (fail closed) when it exceeds the room left; such a path cannot be checked out, and in standalone the budget bounds path bytes. |
| 3 | Many governed candidates (`.md` / workflow paths) at the repository root | Still fails closed past the listing limit — the "more governed candidates" residual. Not reachable through a budgeted merge (16 MiB of path bytes fits the limit); possible on SMP, whose listing limit is its `SMP_GIT_MAX_OUTPUT_BYTES`. |
| 4 | A sync window whose diff is over the limit | Fails closed; an operator rebuild recovers (AC 1 third subtest). Automatic fallback: open question 1. |
| 5 | *(superseded in review round 1: no per-path bound)* A kept path > 4,096 bytes through a governed merge | The guard's `Diff` refuses it (every path kept), so the merge fails closed; a dropped one never refuses a listing (Deviation 2). |
| 6 | N-10 via qualified names | `refs/heads/<n>` is exact (rule 2, unchanged); `refs/tags/<n>` is what a caller who wants the tag names; `~ ^ : ..` are refused by `checkRev`. Case-fold spellings of the namespace are reserved (refadmit's fold), covered by `SPINE/run/y`. A reflog suffix (`<n>@{0}`) is not a branch name and keeps git's resolution — no Spine caller passes one; the public `?ref=` residual covers it. |
| 7 | N-11: a message holding the boundary | Unpredictable (128 bits, drawn after the commits exist); author name and email cannot carry a newline (commit headers are line-based); any malformed record fails the call instead of being skipped. |
| 8 | N-12: other ways `--name-only --no-renames` could load a blob | Tree-to-tree name-only output compares object ids; textconv and `diff.external` apply to patches only; `--no-renames` also turns off copy detection; GIT_ALLOC_LIMIT applies regardless (mutation M14 shows the limit catching rename detection). |
| 9 | githttp: encodings, case, methods | Anything not byte-equal to a route is refused: `%2F`/`%2E` forms, upper case, doubled/trailing slashes, `HEAD`/other methods; the gateway and the handler classify the same path (mutation M18). |
| 10 | githttp: smart-protocol clients | `git clone/fetch/push` (v0 and v2) send exactly `/info/refs?service=…`, `/git-upload-pack`, `/git-receive-pack` under the remote URL (remote-curl appends them to a URL it ends with one slash); the dumb protocol's files are git-http-backend's own patterns. The scenario-git-http job (real clone/fetch/push through the gateway) passes. |

### Deviations

1. **The default listing limit is above `DefaultMaxOutputBytes`** (Design 1 said kept bytes stay
   under the per-command limit). Handoff 1 requires the cap to be at least the checkout budget's
   path bytes + entries + a margin, and a near-budget tree lists to ~16.2 MiB, so a client without
   `WithMaxOutputBytes` keeps up to `DefaultMaxListingBytes` (~17.2 MiB), and a budgeted client
   never less than its budget admits. Decision 3 holds: no new knob — an explicit
   `WithMaxOutputBytes(n)` is the listing limit (SMP passes one), exactly the old threshold for an
   unfiltered `ListFiles` (kept bytes == raw bytes there).
2. *(Superseded in review round 1, P3-1: the per-path bound was dropped entirely.)* **The
   4,096-byte bound applies to kept paths only.** Holding every listed path to it would let
   one long path anywhere in a tree (outside every governed directory) refuse discovery — a
   cheaper wedge than N-6 itself. A dropped path is read and dropped whatever its length (pinned by
   AC 2). The artifact's "strictly fewer refusals" holds except for a kept path over 4,096 bytes,
   which AC 2 requires refusing and which cannot be checked out on Linux (PATH_MAX) or macOS.
3. **Diff's kept bytes are its paths plus a NUL each** (the status fields are not kept), so a
   `Diff` refuses at a larger raw size than before.
4. **`NextID` also lists through the predicate** (the design named discovery and the sweep); the
   same wedge otherwise blocked artifact creation in such a tree.
5. **More exported API than the design listed** (additive): `ListFilesKeeping`, `FileLister`,
   `DefaultMaxListingBytes`, `MaxListingPathBytes`, `OutputTooLargeError.PerPath`.
6. **N-11 boundary placement:** before each record (`<boundary>%n%H…%B`), so text before the first
   boundary is itself a refusal; `HasCommitWithTrailer` fails on a malformed record too (the
   design named only `Log`'s parse). The random-source error path is `ErrKindTransient` (it cannot
   happen on Go >= 1.24).
7. **`CountChangedPaths` argv** adds `--ignore-submodules=none` (handoff 3) and `--end-of-options`
   (the grammar's rule) to the design's argv.
8. **Handoff 4 touches the gateway** (`handlers_git.go`, `withGitPath`), not only githttp; the
   refusal for a non-protocol path is the handler's existing 403 "unsupported git operation"
   (no `ServeHTTP` change). Two rows of `TestIsReadOnly` / `TestIsReceivePack` now use a real
   pack name (`pack-abc.pack` is no path git-http-backend serves) and the old row is pinned as
   refused.
9. **Test updates:** `TestRefGrammar_ArgvShape` normalises the per-call log boundary (and checks
   two calls drew different ones) and pins Diff's new flag; `TestEveryOpHasAnOutputClass` pins
   `count_changed_paths` and also scans `runListing` calls.

### Gates

All at `5c326da5` (the code head; the only later change is this artifact). Heavy legs under the
shared heavy-suite lock. Logs: `t004/gates/r1/`, `t004/gates/ci-r1/`, `t004/gates/smp-r1/`,
`t004/gitfloor/out-r1.log`.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | rc=0 (adapters/git now imports core/refadmit — adapters may import core) |
| `make build`; `go build ./cmd/embed-smoketest && ./embed-smoketest` | rc=0; rc=0 |
| `go vet ./...` plain / `-tags integration` / `-tags scenario` | rc=0 / 0 / 0 |
| `go test -p 4 ./...` | rc=0, no failures |
| `scenario-git-http` job: `TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*`, own DB `s24004_ci_r1` | rc=0 (90 / 26 / 154 passes, 0 skips) |
| integration `-tags integration`: `adapters/git`, `gitpool`, `projection`, `githttp`, `gateway`, `cmd/spine`, `core/artifact`, `core/workflow`, `core/engine`, `core/evidence` | rc=0 |
| `-race`: `adapters/git`, `gitpool`, `projection`, `githttp`, `gateway`, `core/artifact`, `core/evidence`, `core/engine` | rc=0 |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`, read-only mounts): both CI steps + every TASK-004 test verbose | STEP1 / STEP2 / EXTRA rc=0 (27 TASK-004 and neighbouring tests PASS on 2.39.5, the submodule-ignore premise included) |
| golangci-lint v2.11.4 `--new-from-rev=init/INIT-024-wave3-base` on the touched packages | 0 issues; `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files (toolchain gofmt) | clean except `core/artifact/id_allocator.go`, unformatted at the base already (its map literals; not reformatted here) |
| red runs at the base | AC 1, 4, 6, 8, the gitlink test and the three githttp/gateway reachability tests (`t004/red/`) |
| mutations (below) | 19 / 19 caught |

### SMP compatibility gate

`smp` = SMP local main `82899ace` (detached, clean), `../spine` = this worktree at `5c326da5` (its
one uncommitted file was this artifact). `go build ./...` rc=0; `go vet` plain / integration /
scenario / faultinject rc=0; `go test -race -p 4` over the initiative §5 packages
(`cmd/customer-app`, `internal/customer/{githttp,gitread,projectionwriter,spinecore/...,
spineembedded/...,stepevents,workspace/...,run/...}`, `internal/engine/...`) rc=0 with **no
failure at all** (the two by-design gitread contract failures TASK-003 recorded against SMP
`3addd44b` are gone at `82899ace`); platform and spine-runtime migrations applied to own databases
(116 / 26 versions); `dev/testgate` Spine-runtime leg PASS (2,447 passed, 0 failed, 3 allowed
skips) and githttp leg PASS (590 passed, 1 allowed skip); `ws_*` databases and roles 0 before and
after; `s24004_*` databases dropped. No SMP change.

### Codex (`codex review --base init/INIT-024-wave3-base`, plain; model `gpt-6.1-sol` confirmed in the review sub-session)

| Round | Review session | Findings | Disposition |
|---|---|---|---|
| 1 | `01a17ee9-06d5-7d82-a171-38625cf3b49d` (on `5c326da5`) | none: "No actionable regressions were identified … streaming listings, revision resolution, history parsing, and protocol-path restrictions appear consistent with their intended behavior and regression coverage" (confidence 0.84) | clean; no further round needed |

### Mutations (`go test -overlay`, worktree untouched; `t004/mutate.py`, logs `t004/mut/`)

| # | Mutation | Caught by |
|---|---|---|
| M1 | the collector ignores the predicate | AC 1 (FullRebuild, IncrementalSync) |
| M2 | kept-bytes cap off | AC 2 |
| M3 | per-path bound off *(the bound itself was dropped in review round 1)* | AC 2 (4,097-byte path) |
| M4 | discovery lists unfiltered | AC 1 FullRebuild |
| M5 | the sweep lists unfiltered | AC 1 IncrementalSync |
| M6 | default listing limit = `DefaultMaxOutputBytes` | `TestListingLimit_AdmitsEveryBudgetedTree` |
| M7 | the listing limit ignores the budget | `TestListingLimit_AdmitsEveryBudgetedTree` |
| M8 | rule 5 removed | AC 4 |
| M9 | home branch dropped from rule 5 | AC 4 home branch |
| M10 | the namespace matched case-sensitively (`spine/` prefix) | AC 4 every reserved shape (`SPINE/run/y`) |
| M11 | the fixed `---END---` separator back | AC 6 |
| M12 | the record id check off | AC 7 |
| M13 | evidence ignores the counter | AC 8 (engine) |
| M14 | the counter with rename detection | AC 8 (adapters/git: git's allocation refusal) |
| M15 | the counter as a diff-class op | AC 8 (env hook: no GIT_ALLOC_LIMIT) |
| M16 | Diff without `--ignore-submodules=none` | `TestDiff_SubmoduleIgnoreSettingsHideNoGitlinkChange` |
| M17 | any GET a read again | `TestProtocolPaths_…`, `TestServeHTTP_OnlyTheServedRepositoryIsReachable` |
| M18 | the gateway classifies the `/git` URL | `TestHandleGit_PushOnTheGitPathNeedsABearer` (a trusted peer got the receive-pack advertisement without a token) |
| M19 | info/refs matched by suffix again | `TestProtocolPaths_…`, `TestServeHTTP_OnlyTheServedRepositoryIsReachable` |

### Not run

- The full `make test-scenario` with a registry DB (only the CI job's three scenario groups; known
  ~31 pre-existing failures).
- GitHub CI (nothing is pushed).
- A projection rebuild of a real 160k-file tree at the default limits (disk and time; every
  fixture is scaled down to a 4 KiB cap, as the review's reproduction was).
- A second codex round (round 1 was clean).

### SMP adoption and carried items (for SMP EPIC-005 TASK-018 / the bump)

- **History reader (N-11):** SMP's `HistoryReader` needs the same fix (SMP impact, above).
- **Listing limit:** `gitread.NewClient` always passes `WithMaxOutputBytes(SMP_GIT_MAX_OUTPUT_BYTES)`,
  so SMP's listing limit is that value (16 MiB by default), not `DefaultMaxListingBytes`. SMP's
  discovery and sweep now keep only governed paths, so N-6's shape no longer wedges SMP's
  projection; but if SMP adopts Spine's default checkout budget (TASK-003 adoption notes:
  16 MiB of path bytes), an unfiltered listing or a `Diff` of a near-budget tree can exceed 16 MiB.
  Either set `SMP_GIT_MAX_OUTPUT_BYTES` >= `git.DefaultMaxListingBytes`, or pass
  `git.WithTreeBudget` (the listing limit then follows the budget).
- **Shared at the bump** (CHANGELOG "Changed (embedders)"): fewer listing refusals; a kept path over
  4,096 bytes is refused *(dropped in review round 1)*; reserved/home names with no branch are NotFound; `Diff` reports gitlink
  changes the repository's submodule settings hid (SMP's own diff already passes the flag); Log's
  strict parse. Re-run SMP's `gitread`, `projectionwriter` and `spinecore` suites at the bump (done
  here against this branch: green).
- **Optional SMP parity:** SMP's `RealGitOperator` has no `CountChangedPaths`, so SMP's evidence
  keeps counting through its own `Diff` (OQ2 default).

### Open questions (for the owner)

1. Should `IncrementalSync` fall back to `FullRebuild` on its own when its window's `Diff` is
   refused as too large (`ErrOutputTooLarge`)? Today the state stays `error` at the previous
   commit until an operator rebuild, which now succeeds (residual in KNOWN-LIMITATIONS). Default
   taken: no automatic fallback (not in the design).
2. *(Answered in review round 1: no — the cap is dropped.)* Is a 4,096-byte cap on kept paths the right bound for `Diff` too (where every path is kept)? A
   governed merge carrying such a path is refused by the guard's `Diff` (fail closed); such a path
   cannot be checked out on Linux anyway.

### Review round 1 (2026-10-28; correctness + security review of `d1f5e6e1`: 0 P1/P2, one P3, P4s)

Report: SMP repo `local-scratch/i24/t004-reviews/REVIEW.md`; probes `i24/t004-rev/`.
Fixes on this branch, rulings by the coordinator:

| Ruling | Finding | What was done | Tests (red at `d1f5e6e1` / mutation) |
|---|---|---|---|
| 1 | **P3-1** the 4,096-byte refusal of kept listing paths and of every Diff path was a new persistent projection wedge for embedders (a governed `.md` at a long path failed every FullRebuild; a long path anywhere in a sync window failed every IncrementalSync; reachable on SMP, whose own merges can admit such a path) | The per-path refusal is dropped for listings and Diff alike; memory is bounded by the kept-bytes limit. Every listing owner holds one unfinished record to `max(4 KiB, the room left)` (`listingBounds.carryBound`), and a record completed inside one write is held to the same bound, so the verdict does not depend on how the pipe chunked it. `OutputTooLargeError.PerPath` and `MaxListingPathBytes` are removed (nothing needed them). AC 2's 4,097-byte clause is amended in place; Deviation 2 is superseded. CHANGELOG "Changed (embedders)" names it. | `TestFullRebuild_LongGovernedPathProjects` (a 5,000-byte governed `.md` path projects) and `TestIncrementalSync_LongPathInTheWindowSyncs` (a 5,000-byte path in the window, the artifact change applied) — **red at `d1f5e6e1`**: "output holds a path longer than 4096 bytes" (`red/red-rr1.log`). `TestListFiles_KeptBytesOverLimitRefused` now pins 4,096-, 4,097- and 9,000-byte paths listing, kept or dropped, and diffing; only the total binds. Mutation Mo (per-path refusal back): caught. |
| 2 | **P4-2** Diff kept both trees' paths under the tree-listing limit | `DefaultMaxDiffBytes` = 2 x (path budget + entries) + 1 MiB (~33.4 MiB); `diffLimit()` = explicit `WithMaxOutputBytes`, else that default, never below twice what a carried budget admits + margin. Diff and its collector use it. OQ1's automatic IncrementalSync → FullRebuild fallback is recorded as a **follow-up for TASK-010** (not built). | `TestListingLimit_AdmitsEveryBudgetedTree` reads the constants (default, explicit, budgeted, process default); `TestDiff_HoldsBothTreesWorthOfPaths` (~2.4 MB of paths between a ~2.1 MiB listing limit and a ~3.1 MiB diff limit: Diff returns all 25,000 records, the listing is refused). Mutations Mi (Diff on the listing limit), Mi2 (diff default = listing default): caught. |
| 3 | **P4-3** three reviewer mutations survived: the carry bound removed (Ma), Diff's `s.finish()` removed (Mc), runListing's end-of-stream check removed (Md); the shims re-implemented the end-of-stream checks | `endOfStream` is the one end-of-stream verdict: runListing and every shim (`ParseNameStatusZChunkedForTest`, `ListedForTest`, `CountedForTest`) call it, and the shims end exactly as Diff/ListFiles do. A fake-git seam (a `git` on PATH that prints crafted output) drives the real `ListFiles`, `ListFilesFunc`, `Diff` and `CountChangedPaths`. | `TestListing_CarryOfADroppedPathIsBounded` (5, 12, 20, 40 KiB dropped paths in 1, 4 and 7 KiB writes under a 16 KiB limit; the carry never exceeds the limit; the 4 KiB floor), `TestListings_EndOfStreamThroughTheRealRunner`. Mutations Ma, Ma2 (completed-record bound off), Mb (floor off), Mc, Md, Me (endOfStream accepts a truncated stream): all caught. |
| 4 | **P4-4** concrete request paths for the githttp defect in public text; an inaccurate "doubled slash refused" sentence; a garbled CHANGELOG intro; the embedders section missed HasCommitWithTrailer's strict parse | KNOWN-LIMITATIONS "Git HTTP", the CHANGELOG, `protocol_path.go` and both test headers now state the defect at defect level ("a path prefix inside the served tree reached git-http-backend as a repository"). The doubled-slash sentence now says what happens: inside the git path it is refused; through the gateway an empty segment where the repository segment goes is an empty repository id, which names the primary repository as it always has. CHANGELOG intro rewritten; the embedders bullet names `HasCommitWithTrailer`'s strict parse. The notes above carried no concrete request path. | — |
| 5 | **P4-5** `untrackedObstruction`'s diff (`treelock.go`) lacked `--ignore-submodules=none` | Flag added (one argv element plus a three-line comment; TASK-003's lock-order comment untouched). | `TestUntrackedObstruction_SubmoduleIgnoreSettingsHideNoGitlink` (an untracked file where a forced switch writes a gitlink, under `ignore = all` and under `diff.ignoreSubmodules = all`) — **red at `d1f5e6e1`**: "" (not reported). Mutation Ml: caught. |
| 6 | **P4-6** with the raw-size bound gone, nothing bounded the work of a listing: a few small objects expanding to 10^8 paths took 7.75 s per filtered listing | **Record ceiling:** a tree listing fails closed (`ErrOutputTooLarge`, new `OutputTooLargeError.Records`) past 4 x the client's budget entries (`DefaultTreeMaxEntries` without a budget: 800,000), kept or dropped; Diff and `CountChangedPaths` past twice that. **Pathspec:** new `ListFilesUnder(ctx, ref, dir, keep)` on `PathFilterLister` (`--literal-pathspecs ls-tree -r --name-only -z --full-tree [eoo] <rev> <dir>`; a dir that is not a clean relative path is never passed to git, the whole tree is listed and filtered instead); `ListFilesKeeping` takes the directory. Discovery with an artifacts directory, workflow discovery (`<dir>/workflows`, `workflows`) and `NextID` (its parent directory) scope their listings; repository-root discovery and the sweep rely on the ceiling. KNOWN-LIMITATIONS "Listings stop at a record ceiling". | `TestListing_RecordCeiling` (MaxEntries 50 / 20: listing ceiling 200 / 80, diff and count 400 / 160; refused or admitted as computed; a scoped listing under an 80-record ceiling), `TestListFilesUnder_WalksOneDirectory` (literal, not a glob; prefix exactness; unclean dirs; the fallback without the capability), `TestDiscovery_ScopesTheListingToItsDirectory` (the dirs discovery and NextID pass); argv rows in `TestRefGrammar_ArgvShape`. Mutations Mf, Mg, Mh (each ceiling off), Mj (no pathspec), Mk (unclean dirs as pathspecs), Mm (discovery unscoped), Mn (NextID unscoped): all caught. |
| 7 | **P4-7** SMP `gitread.resolveRevLikeSpine` documents rules 1–4 only | Recorded under SMP impact as a TASK-018 carry item; SMP untouched. | — |

Mutations (`t004/mutate_rr1.py`, overlay, worktree untouched; `t004/mut-rr1/summary.txt`): **17 / 17
caught**, including the three the reviewer saw survive (Ma, Mc, Md).

Deviations in this round:
- `MaxListingPathBytes` (exported in round 0) is removed rather than kept: after P3-1 nothing
  bounds a path by it; the 4 KiB carry floor is the unexported `listingCarryFloor`.
- The ceiling follows the client's budget entry count (not a fixed constant), so a small budget
  gives a small ceiling — the test's "small cap" — and an operator who raises
  `SPINE_GIT_TREE_MAX_ENTRIES` raises it.
- `PathFilterLister` gained `ListFilesUnder`; it is new in this task, so no shipped interface grows a
  method (`gitread.Client` gets it by embedding).

**Codex (second round; `codex review --base init/INIT-024-wave3-base`, plain; `gpt-6.1-sol` confirmed):**

| Round | Review session | Findings | Disposition |
|---|---|---|---|
| 2 | `01a1828d-d186-7a40-8cc9-1be279c28e2c` (on `758fb405`; the lint-only commit `6456ab51` followed) | none: "No actionable regressions were identified in the changes relative to the specified merge base" (confidence 0.81) | clean |

**Gates after review round 1** (code head `6456ab51`; the artifact was the only uncommitted file;
heavy legs under the shared heavy-suite lock; logs `t004/gates/r2/`, `ci-r2/`, `smp-r2/`,
`gitfloor/out-r2.log`):

| Gate | Result |
|---|---|
| lint-boundaries, `make build`, embed-smoketest, `go vet` plain / integration / scenario | rc=0 |
| `go test -p 4 ./...` | rc=0, no failures |
| `scenario-git-http` (`TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*`), own DB `s24004_ci_r2` | rc=0 (90 / 26 / 154 passes, 0 skips) |
| integration `-tags integration` (11 touched packages) | rc=0 |
| `-race` (`adapters/git`, `gitpool`, `projection`, `githttp`, `gateway`, `core/artifact`, `core/evidence`, `core/engine`) | rc=0, no data race |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`): both CI steps + every TASK-004 test verbose, round-1 tests included | STEP1 / STEP2 / EXTRA rc=0 (36 verbose PASS lines) |
| golangci-lint v2.11.4 `--new-from-rev=init/INIT-024-wave3-base`, touched packages | 0 issues (3 found and fixed in `6456ab51`: 2 gocritic initClause, 1 staticcheck QF1001); `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files | clean except the pre-existing `core/artifact/id_allocator.go` |
| SMP compatibility gate (SMP `82899ace`, `../spine` = `6456ab51`) | build and vet x4 rc=0; §5 `-race` packages rc=0, no failure; testgate runtime PASS (2,447 passed, 0 failed, 3 allowed skips), githttp PASS (590, 1 allowed skip); `ws_*` 0 before and after; `s24004_*` dropped. No SMP change. |
| red runs at `d1f5e6e1` | `TestFullRebuild_LongGovernedPathProjects`, `TestIncrementalSync_LongPathInTheWindowSyncs`, `TestUntrackedObstruction_SubmoduleIgnoreSettingsHideNoGitlink` (`red/red-rr1.log`) |
| mutations | 17 / 17 caught (`mut-rr1/summary.txt`) |

**Commits (review round 1):** `9550dbc6` (P3-1, P4-2, P4-3, P4-6), `13808885` (P4-5), `758fb405`
(P4-4 and the docs of the new bounds), `6456ab51` (lint), and this notes commit (AC 2 amendment,
superseded marks, P4-7 carry item, this section).

**Follow-up for TASK-010 (recorded, not built):** OQ1 — an `IncrementalSync` whose window `Diff` is
refused as too large could fall back to `FullRebuild` on its own instead of holding the sync in
error until an operator rebuild (reviewer: yes, as a follow-up).

**Not run (review round 1):** a third codex round (round 2 was clean; the brief caps codex at three);
the full `make test-scenario` with a registry DB; GitHub CI; a real 10^8-path tree (the record
ceiling is tested with a small budget, and the reviewer's probe measured the unbounded case).

### Integration A (2026-10-30)

Merged into `init/INIT-024-integration-a` on top of phase 4 (`d21a55e2`, marker
`init/INIT-024-integration-a-p4`): TASK-004 `d18a9471` as `557e78cb` (no conflict: git merged
`treebudget.go`, `treelock.go`, `CHANGELOG.md` and `KNOWN-LIMITATIONS.md` on its own; the shared
record reader replaces TASK-003's sink loop in `measureStoredTree` as it did in `measureTree`), then
TASK-005 `80240088` as `e1f24c05` (conflict: `CHANGELOG.md` only, TASK-005's section stacked on top;
`githttp/handler.go` merged on its own: TASK-004's exact protocol paths in `isReadOnly`,
`IsReceivePack` and `isInfoRefs`, TASK-005's probe/slot/re-check order in `ServeHTTP`, both kept).
Coordination commits (prefix "INIT-024 integration A — coordination: "):
- `649dec71`: TASK-005's push gate over TASK-003's phase-4 neutral-attributes step.
  - One hook process measures the pushed trees, then asks for the re-check.
  - A check that fails before the line refuses the push at the hook's install ("push gate
    unavailable") before the re-check is armed; nothing is written
    (`TestRecheck_NoLineRefusesThePushBeforeTheRecheck`).
  - A renormalisation pending past the line still lets the re-check refuse a frozen push.
    `TestRecheck_RunsWithRenormalisationPending`'s fixture now fails the rewrite with a stale
    `index.lock`, because since phase 4 its untracked-file obstruction no longer leaves anything
    pending.
  - The storage probe runs before the install, so a refused repository never gets the line
    (`TestStorageProbe_RefusedRepositoryNeverGetsTheLine`).
  - A first push that waits for the tree's hold keeps its slot and is not cut by the command-read
    deadline; another principal is served meanwhile; no lock-order inversion between slot, tree
    hold and alignment lock (`TestPushSlot_TreeWaitIsNotCutByTheCommandDeadline`).
  - Mutations of the order and of the deadline are caught.
- `39f3d271`: the renormalisation's status is streamed through TASK-004's record reader under a tree
  listing's bounds (listing limit, record ceiling). Phase 4's `statusLimit` and `runOpts.limit` are
  removed, so each limit has one definition (`TestStatusCollector_ParsesAndBounds`,
  `TestNeutralAttributes_StatusIsBoundedByTheBudget`).
- `1ca7d63e`: git slots are keyed to the request's workspace through the shared-mode gateway
  (TASK-004 exact paths × TASK-005 slots × TASK-006/007 gate). `TestHandleGit_SlotsAreKeyedToTheRequestsWorkspace`
  is red when the gateway drops the workspace from the context or classifies on the URL.
The confirming review of phase 4 (round 6: 0 P1–P3, 3 P4) was then fixed in `5bf328ec` (the held
tree's status writes the index; the mark is cleared only on a clean second comparison; the
quarantine's stash runs with the monitor off), `48a0621a` (a marker left without the line is
recorded again; docs) and `fcf54411` (the over-budget logs name the variable and the restart;
`git.MeasureStoredTree`), each with red/green tests.

Gates at `fcf54411`:
- Cheap gates: lint-boundaries, `make build`, embed-smoketest, `go vet` ×3 rc 0;
  golangci-lint `--new-from-rev main` plain / integration / scenario and gosec 0; gofmt clean
  except two files already unformatted on main (`core/artifact/id_allocator.go`,
  `service/scenariotest/scenarios/planning_run_test.go`); pincheck OK; `go mod tidy -diff` clean,
  `verify` OK.
- `-race`: `adapters/git`, `gitpool`, `githttp`, `workspace`, `gateway`, `cmd/spine` and
  `core/worktreefs` rc 0.
- `go test -p 4 ./...` rc 0.
- `-tags integration` (TASK-003/004/005 sets plus `workspace`, `projection`, `engine`) rc 0.
- scenario-git-http: every step rc 0, 0 skips (90 / 26 / 154 / 12 / 24 / 37 + 1 / 19 + 1 / 1).
- git-floor (git 2.39.5, Linux, the real storage probe): every step rc 0.
- TASK-004's and TASK-005's AC tests by name: every one passes.
- SMP (`82899ace`, 0 dirty files): build and vet ×4 rc 0; `-race` on the SMP importers of
  `adapters/git` and `githttp` all ok; testgate Spine-runtime PASS (2447, 3 allowed skips) and
  githttp PASS (590, 1 allowed skip); no `ws_*` or `s24ia_*` database left.

Codex (`codex review --base init/INIT-024-integration-a-p4`, `gpt-6.1-sol`): round 1, session
`01a18ee8-725f-7db2-81ac-f88c8ee60e53` on `fcf54411`: no findings. No second round was needed.
Statuses stay as they are; nothing is pushed.

### Status after GitHub CI (2026-11-04)

- **2026-11-04: Completed.** GitHub CI run 41875102030 at Spine `main` `ce0b9c9e` (this initiative's
  closing commit `fd35f802`, pushed on 2026-11-04 with the owner's approval, plus TASK-010's
  git-identity fix) passed all 8 jobs. Every AC is met. The owner questions stay at their recorded
  defaults, and open question 1 (an automatic `FullRebuild` fallback) stays a recorded follow-up
  (TASK-010).
