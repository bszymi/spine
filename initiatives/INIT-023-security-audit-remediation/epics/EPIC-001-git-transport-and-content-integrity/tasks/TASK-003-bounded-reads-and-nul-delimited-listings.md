---
id: TASK-003
type: Task
title: "Bounded blob reads and NUL-delimited Diff/ListFiles in the git CLI client"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-14
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: /architecture/event-schemas.md
  - type: related_to
    target: /governance/go-coding-guidelines.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-009-git-argv-sentinels.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-006-bound-yaml-decoder-in-artifact-parser.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/tasks/TASK-016-cascade-commit-status-to-branch-artifacts.md
---

# TASK-003 — Bounded blob reads and NUL-delimited Diff/ListFiles in the git CLI client

---

## Purpose

This task closes the Spine half of two findings from the 2026-10-06 security audit (Spine
`61994fe`, SMP `80995559`):

- **F4 (P2, both surfaces).** Unbounded git reads. A compressible large file can OOM the shared,
  multi-tenant `customer-app`.
- **P3-9 (P3, both surfaces).** `Diff` and `ListFiles` parse git output without `-z`. C-quoted
  paths (non-ASCII or control characters) are silently dropped from the status cascade and from
  projection discovery.

Line numbers refer to Spine `61994fe` unless stated. "SMP" means `spine-management-platform` at
`80995559`. SMP's own git plumbing (`RealGitReader`, `RealGitOperator`, `HistoryReader`,
`gitplumbing.go`) is **not** in this task. SMP TASK-003 (P3-9.smp) and SMP TASK-004
(F4.smp-reads) own it. The SMP side of *this* task is covered in Design §10.

### F4 — the defect

1. **`CLIClient.ReadFile` has no limit** (`adapters/git/cli.go:499-505`).
   - It runs `git show <ref>:<path>` through `run` (`:626-687`).
   - `run` collects stdout in an uncapped `bytes.Buffer` (`:675-686`). It then returns
     `stdout.String()`, and `ReadFile` converts that to `[]byte` (`:504`). The blob is copied
     twice more on top of the buffer's growth.
   - Measured on 2026-10-06: an 8 MiB blob costs **49 MiB** of `TotalAlloc` (about 6×).
2. **Git stores repetitive text at roughly 1/200–1/340 of its size.**
   - SMP's gateway caps only the *total* push body (2 GiB), not a single object.
   - The audit measured a 256 MiB blob (764 KiB pack) at ~1.3 GiB peak heap, and a 1 GiB blob
     (~3 MiB pack) at ~6 GiB.
   - The shipped `customer-app` has a `512Mi` limit and one replica by default.
3. **Every artifact read path reaches it:**
   - discovery: `core/artifact/discovery.go:91`, `:142`, `:151`, `:170`, `:180`, `:200`, `:205`;
   - `Service.Read` (`core/artifact/service.go:288`) and `Service.List` (`:342`);
   - `workflow.Service.Read`/`List` (`core/workflow/service.go:210`, `:250`);
   - the projection (`adapters/projection/service.go:87`, `:190`, `:501`, `:585`);
   - the merge-time status cascade (`core/engine/merge.go:606`);
   - the evidence loader (`core/evidence/loader.go:112`);
   - the ID-collision probe (`core/artifact/renumber.go:28`).
   - In SMP it is reached through every `git.NewCLIClient`: `RunBranchDiffReader.ReadFile`
     (`internal/customer/spinecore/runbranchdiffreader.go:359-361`, used by `ListRunArtifacts`),
     `ArtifactWriter.ReadRaw` (`artifactservice.go:67-69`, used by `GetContent`), the projection
     writer (`projectionwriter/worker.go:312-314`) and the provisioning rebuild
     (`workspace/provision_steps_runtime.go:320-326`).
4. **The same uncapped buffer serves every other command.**
   - `Log` (`cli.go:477-496`) returns full commit bodies. A pushed commit message is
     attacker-sized and compresses just as well. It reaches `main` through any governed merge,
     and standalone `GET /query/history` reads it (`adapters/projection/query.go:150-160`).
   - `ls-tree` and `diff` output grows with the file count of a pushed tree.

> **Design-check note (2026-10-06) — F4 has a second memory pool: git's own.** The `git` child
> runs in `customer-app`'s cgroup (on Kubernetes ≥ 1.28 with cgroup v2 the whole container is
> OOM-killed as a group), so bounding Spine's heap (I1) alone does not close F4. Measured with
> `/usr/bin/time -l` on git 2.54 (`git 2.39.5` ships in `Dockerfile.customer-app`):
> - `git show` holds a **packed** blob in memory whenever it is below `core.bigFileThreshold`
>   (512 MiB). It streams only loose objects and undeltified packed objects above that threshold.
>   A 256 MiB packed blob costs 274 MB RSS undeltified, and 542 MB deltified.
> - **The size probe can be passed without any race, in two ways** (PoC
>   `_work/task003/designcheck/zz_t003dc_deltabase_test.go`, `overlay_deltabase.json`):
>   1. *Small target, large delta base.* `cat-file -s` reports the **target's** size, and
>      `git show` must first inflate the **base**. Stock `pack-objects`, which the pusher's own
>      `git push` runs, deltifies any target ≥ base/32. Measured: a 3 000 000-byte tip blob on a
>      64 MiB base passes a 4 MiB probe, and its read drives the git child to 71 MiB RSS. On a
>      120 MiB base, a 4 000 000-byte tip reached 135 MB. A hand-built pack lifts the 1/32 ratio,
>      so the base can be any size. Four concurrent `GetContent` requests exceed the 512 Mi limit.
>   2. *A gitlink named `*.md`.* `ls-tree -r` lists gitlinks as leaves. If one points at a commit
>      in the repo, `cat-file -s` reports the commit object (118 bytes), and `git show` prints that
>      commit **with its patch**. Measured: 130 MB of stdout and 253 MB RSS, all allocated before
>      the first byte is written, so Spine's stdout cap cannot help.
> - **Fix (§2a below):** run the read commands with git's own per-allocation ceiling,
>   `GIT_ALLOC_LIMIT`. Verified: both bypasses fail at about 5 MB RSS, and there are no false
>   refusals on the SMP or Spine repositories.
> - **A third F4 sink is a worktree read, not a git read.** SMP `WriteContent`
>   (`internal/customer/spineembedded/artifacts_content.go:117`) calls `ReadRaw` first.
>   Once this task makes that call fail cleanly, the request falls through to `Service.Update`,
>   (`:128`), which does `os.ReadFile` on the checked-out file (`core/artifact/service.go:214`) with no
>   bound. The same holds for `workflow.Service` `:163`/`:291` and `addLinkToArtifact`
>   (`successor.go:123`). **Spine TASK-004 I7 bounds these** (`worktreefs.Tree.ReadFile`, same
>   413 mapping). F4.spine is therefore closed only when **both** TASK-003 and TASK-004 are on
>   Spine `main`. `RenumberArtifact`, `UpdateLinksToRenamedArtifact`, `IsIDCollision` and
>   `CLIClient.HasCommitWithTrailer` have no caller in either repo; they are dormant.

### P3-9 — the defect

1. **`ListFiles` and `Diff` parse quoted output.**
   - `ListFiles` runs `ls-tree -r --name-only` (`cli.go:524`).
   - `Diff` runs `diff --name-status` (`:433`).
   - Both split on `\n` after `strings.TrimSpace` (`:530`, `:439`).
2. **Without `-z`, git C-quotes any path containing a byte ≥ 0x80 (under the default
   `core.quotePath`), a control character, `"` or `\`.** For example,
   `"initiatives/INIT-001/epics/EPIC-001-r\303\251sum\303\251/epic.md"`.
   - The quoted string ends in `"`, so `strings.HasSuffix(file, ".md")` is false. Discovery
     (`discovery.go:78`, `:131-132`), the status cascade (`merge.go:707`) and workflow detection
     (`IsWorkflowPath`) drop it **silently**: it is not in `Errors` and not in `Skipped`.
   - `TrimSpace` also mangles a leading or trailing space in the first or last path.
3. **Consequences, all reproduced on 2026-10-06:**
   - a governed artifact under a non-ASCII directory is never projected;
   - `NextID` (`core/artifact/id_allocator.go:64`, `:88`) cannot see `EPIC-001-résumé/` and
     **allocates `EPIC-001` a second time**. SMP's `AddToRun` uses `NextID` through
     `ArtifactWriter.Lister`.

> **Design-check note (2026-10-06) — the platform itself writes non-ASCII paths.**
> - `artifact.Slugify` keeps every `unicode.IsLetter` rune (`id_allocator.go:129-157`).
> - SMP builds artifact paths with it in `CreateEntry` (`artifacts_createentry.go:142`, `:334`),
>   `AddToRun` (`artifacts_addtorun.go:127`) and proposal promotion (`proposals/promote.go:447`).
> - So a title with any non-ASCII letter makes a path git C-quotes, through the ordinary UI. A
>   Polish example is "Wdrożenie" → `…-wdrożenie/`.
> - P3-9 therefore hits ordinary users, not only attackers. A pilot workspace may **already**
>   hold artifacts that were never projected, and IDs that `NextID` allocated twice.
> - The fix will expose that stored state; see §10 "Stored data and upgrade".

### Attackers and reach

- **F4, A1 or A2.** Anyone who can push a run branch: a member, a runner credential or an agent.
  `spine/*` refs skip branch protection.
  - **Pre-merge, per request, no approval needed:**
    - SMP `ListRunArtifacts` (run detail) and `GetContent`;
    - standalone `GET /artifacts/*?ref=<run branch>`.
  - **Post-merge:**
    - every projection sync re-reads the file. Today that means an OOM, a restart and the same
      OOM again: a crash loop for **every workspace on the replica**;
    - standalone `/query/history` buffers a giant commit message.
- **P3-9, A1.** A run author, or anyone landing content on `main`, can keep a governed artifact
  out of the projection, out of ID allocation and out of the status cascade by giving its
  directory a non-ASCII or control-character name.

### Impact

- **F4:** cross-tenant availability only. There is no confidentiality or integrity effect.
- **P3-9:** governance visibility and integrity drift.
  - An artifact can exist on `main` without being projected or status-cascaded.
  - IDs can collide.

### Evidence (scaled PoCs, re-run 2026-10-06 against `61994fe`)

The overlay tests in `tmp/init027-orchestration/spine-init023/_work/task003/` use only the
`61994fe` API. All six **fail on `61994fe`** and are written to pass once this task lands:

| PoC (source finding) | Result on `61994fe` |
|---|---|
| `TestT003_ReadFileOversizedBlobIsRefusedWithoutBuffering` (skeptic-bigblob, verify-r1-9 phase A) | 8 MiB blob returned, no error, `TotalAlloc` 49 191 KiB |
| `TestT003_IncrementalSyncDoesNotBufferOversizedFile` (verify-r1-9 phase B) | `TotalAlloc` 57 448 KiB |
| `TestT003_ListFilesReturnsRawPaths` (P3-9) | quoted forms, and `" lead.md"` trimmed to `"lead.md"` |
| `TestT003_DiffReturnsRawPaths` (P3-9) | quoted forms |
| `TestT003_FullRebuildProjectsNonASCIIArtifactPath` (P3-9) | `EPIC-001-résumé/epic.md` not projected; sync `idle` |
| `TestT003_NextIDSeesNonASCIISlug` (P3-9) | `NextID = EPIC-001` (already taken) |

- **Run them with:** `go test -count=1 -overlay <work>/overlay.json -run TestT003_
  ./adapters/git/ ./adapters/projection/ ./core/artifact/`.
- **The SMP hazard** (§10) is shown by `zz_t003_smp_unquote_test.go` (`overlay_smp.json`). Fed
  raw `-z` paths, `unquoteDiffPaths` refuses `"quoted.md` (a 503 downstream) and silently
  rewrites a raw path `"x"` to `x`.

> **Design-check note (2026-10-06) — re-run, and one more PoC.**
> - All six PoCs above were re-run on `61994fe` on 2026-10-06 and fail as recorded:
>   - `ReadFile`: 49 185 KiB;
>   - `IncrementalSync`: 57 455 KiB, state `idle`;
>   - the quoted and trimmed listings;
>   - the résumé epic not projected;
>   - `NextID = EPIC-001`.
> - **New:** `_work/task003/designcheck/zz_t003dc_deltabase_test.go` (`overlay_deltabase.json`,
>   run with `-run TestT003DC_DeltaBase ./adapters/git/`):
>   - `TestT003DC_DeltaBaseProbeBypass` **fails on `61994fe`, and would fail under the design
>     as first written.** A 3 000 000-byte tip deltified on a 64 MiB base passes a 4 MiB probe;
>     the read succeeds, and the git child reaches 71 MiB RSS.
>   - `TestT003DC_DeltaBaseWithGitAllocLimit` passes. With `GIT_ALLOC_LIMIT=16 MiB` in the
>     environment, which `61994fe`'s `run` already inherits, git refuses at 4 MiB RSS.
> - The gitlink, merge-SIGPIPE and rename-under-limit measurements in §2a and §3 are shell
>   reproductions on git 2.54. AC 18 and AC 19 turn them into tests.

## Design

### 0. Invariants

- **I1 — Bounded memory.** No `CLIClient` method buffers more than its configured limit, plus
  one pre-sized buffer, regardless of repository content.
- **I2 — Error parity.** For every input `61994fe` handled without OOM, `ReadFile` returns the
  same bytes or the same `*GitError` `Kind`. A missing path is still `ErrKindNotFound`, by direct
  type assertion (`go-coding-guidelines.md` §2.4).
- **I3 — No interface change.** `git.GitClient` (`client.go:11-32`), `engine.GitOperator` and the
  gateway `GitReader` keep their signatures. SMP implements them (`stubGitOperator`,
  `divergenceGitClient`, `RealGitOperator`). All changes are additive: constants, options, error
  types, and struct fields on `DiscoveryResult` and `ChangeSet`.
- **I4 — Governance config stays fail-closed.** Workflows and `.spine/branch-protection.yaml` are
  already capped at 64 KiB by `yamlsafe.MaxBytes` (`core/yamlsafe/decoder.go:18`). An oversized
  one stays a sync error with the previous projection retained, exactly as today. Only
  artifact/`.md` discovery skips and advances.

  > **Implementation note (2026-10-14) — I4 amended by the orchestrator ruling (TASK-011 D2).** An
  > oversized *workflow* is TASK-011's `read_bounds` rejection (`git.ErrBlobTooLarge` is
  > registered in `isContentReadError`): its verified previous row is kept quarantined, or it is
  > not projected, and the sync advances. `.spine/branch-protection.yaml` stays a held sync error
  > with the rules unchanged. See Implementation Notes, Deviation 1.
- **I5 — Nothing is dropped silently.** Every file discovery declines to project is recorded,
  with a reason, in a WARN log, a metric and an event.
- **I6 — No runtime schema change.** There are no migrations and no new columns.

### 1. Read limits and typed errors (`adapters/git`)

New in `adapters/git/limits.go`:

```go
const (
    DefaultMaxBlobBytes   int64 = 4 << 20  // one ReadFile (owner question 1 / plan OQ-5)
    DefaultMaxOutputBytes int64 = 16 << 20 // stdout of any other command (owner question 1)
    maxStderrBytes              = 64 << 10 // stderr kept for classification; the rest is dropped
)

func WithMaxBlobBytes(n int64) CLIOption   // n <= 0 keeps the default
func WithMaxOutputBytes(n int64) CLIOption // n <= 0 keeps the default

var ErrBlobTooLarge   = errors.New("git: blob exceeds the read limit")
var ErrOutputTooLarge = errors.New("git: command output exceeds the read limit")

type BlobTooLargeError struct {
    Ref, Path string
    Size      int64 // bytes `git cat-file -s` reported; -1 if the content stream overran Limit
    Limit     int64
}
func (e *BlobTooLargeError) Error() string // "<path> at <ref> is <size> bytes, over the <limit>-byte read limit"
func (e *BlobTooLargeError) Is(t error) bool { return t == ErrBlobTooLarge }

type OutputTooLargeError struct{ Op string; Limit int64 }
func (e *OutputTooLargeError) Is(t error) bool { return t == ErrOutputTooLarge }
```

- **Both are returned inside the existing envelope:**
  `&GitError{Kind: ErrKindPermanent, Op: op, Message: "blob exceeds read limit" | "output exceeds read limit", Err: <typed>}`.
  - Callers that type-assert `*git.GitError` keep working.
  - `IsRetryable()` is false, because a retry cannot shrink the object.
  - `errors.Is` / `errors.As` reach the typed cause through `GitError.Unwrap` (`errors.go:33-35`).
- **`NewCLIClient` sets both limits to the defaults.** `CLIClient` gains `maxBlobBytes` and
  `maxOutputBytes`, plus a `blobSize` function field (default `c.catFileSize`). The field is the
  test seam for AC 4.

  > **Design-check note (2026-10-06).** §2a adds a third limit:
  > - `maxGitAllocBytes`, set by `WithMaxGitAllocBytes(n int64) CLIOption`;
  > - when `n <= 0` or the option is absent, it is `4 × maxBlobBytes`, computed after all
  >   options apply;
  > - it is raised to at least `maxBlobBytes + 1`.
  >
  > It is unexported state, like the other two, so there is still no interface change (I3).

### 2. `CLIClient.ReadFile`: size probe, then one bounded read

This replaces `cli.go:499-505`. TASK-002's ref validation stays first, and its
`--end-of-options` stays where TASK-002 put it (shown here as `[eoo]`).

```go
func (c *CLIClient) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
    // TASK-002: ref validated; never empty (":path" would read the index).
    spec := ref + ":" + path
    size, err := c.blobSize(ctx, spec) // git cat-file -s [eoo] <spec>, via run (op "read_file")
    if err != nil {
        return nil, err // same stderr as `git show` -> same classifyGitError Kind
    }
    if size > c.maxBlobBytes {
        return nil, tooLarge(ref, path, size, c.maxBlobBytes)
    }
    out, err := c.runBounded(ctx, "read_file", c.maxBlobBytes, size, "show", /*[eoo]*/ spec)
    if errors.Is(err, errStdoutLimit) {
        return nil, tooLarge(ref, path, -1, c.maxBlobBytes) // object changed after the probe
    }
    return out, err // no string round trip; a 0-byte blob returns a non-nil empty slice, as today
}
```

- **The probe is `git cat-file -s <spec>`.**
  - It reads only the object header (for a deltified object, the delta header), so git never
    inflates an oversized blob.
  - Its failure messages are byte-identical to `git show`'s for the same spec. Verified on
    2026-10-06:
    - `fatal: path 'x' does not exist in 'HEAD'` → `ErrKindNotFound`;
    - `fatal: invalid object name 'badref'.` → `ErrKindPermanent`;
    - `fatal: path 'x' exists on disk, but not in 'HEAD'` → `ErrKindPermanent`.
  - So I2 holds without new classification code.

  > **Design-check note (2026-10-06).** "Byte-identical" is not true for every input. For a
  > gitlink whose commit is absent, `git show` says `fatal: bad object HEAD:gl.md` and
  > `cat-file -s` says `fatal: git cat-file: could not get object info`. Both classify as
  > `ErrKindPermanent`, so **Kind** parity (I2 as stated) still holds; only the `Message` text
  > differs. Also re-verified on 2026-10-06 (git 2.54): a missing nested path, a path through a
  > blob (`a.md/x`), a symlink blob and a tree path are identical in exit code and stderr. AC 5
  > pins Kind, not message text.
- **The content read stays `git show <spec>`.**
  - For a blob it streams the raw bytes. No textconv or smudge runs without `--textconv` on the
    command line (verified with a `.gitattributes` `diff=`/`filter=` driver).
  - A tree path keeps today's listing output.

  > **Design-check note (2026-10-06).** `git show` "streams" only a **loose** blob, or a packed
  > undeltified blob above `core.bigFileThreshold` (512 MiB). Every other packed blob, and every
  > blob resolved through a delta, is built in git's memory first. For a gitlink to a commit in
  > the repo, `git show` prints that commit with its patch. §2a bounds git's side.
- **`runBounded` pre-sizes the stdout buffer to `size`.** One allocation of exactly the blob, with
  no doubling. Its `io.LimitReader`-equivalent cap at `maxBlobBytes` is the second line of
  defence.
  - The probe and the read are two processes, so a *named* ref can move between them. The limit
    still bounds Spine's heap.
  - The residual is git's own transient memory if a deltified oversized blob lands in that
    window. It does not apply to SHA-addressed reads (all projection reads).
  - TASK-005 pins the engine cascade to a SHA, and SMP TASK-008 pins `RunBranchDiffReader`
    (§10).
- **Cost: one extra `git` process per `ReadFile`.**
  - Measured at ~9 ms per spawn on macOS (200 × `cat-file -s` = 1.84 s); Linux is cheaper.
  - `TestRepoArtifacts_ParseCleanlyUnderSpine` (SMP, 1 184 `.md` files) takes 12.7 s today, and
    a full rebuild of that repo roughly doubles.
  - `IncrementalSync` reads only changed files. A batched probe is a follow-up.

> **Design-check note (2026-10-06) — the named-ref window is not the only residual.** The bullet
> above names one residual: git's memory during a named-ref race. Two more need no race, and
> SHA-pinning does not help: a small target deltified on a large base, and a gitlink `*.md`
> (Purpose, F4 note). The bound for git's memory is §2a.

### 2a. Bound git's own memory on read commands (design-check amendment, 2026-10-06)

- **Mechanism.** `runBounded` sets `GIT_ALLOC_LIMIT=<c.maxGitAllocBytes>` in the child's
  environment for the **read** ops: `read_file` (both `cat-file -s` and `show`), `list_files`,
  `log`, `head`, `ref-sha`, `rev-parse` and `merge-base`.
  - git checks it on every heap allocation (`wrapper.c` `memory_limit_check`) and dies with
    `attempting to allocate N over limit M` instead of allocating.
  - `cmd.Env` becomes `os.Environ()` plus the variable. Today `run` leaves `Env` nil for
    non-push ops, so they inherit the parent environment; the new value keeps that.
  - Push ops keep their existing env construction (`:639-673`) unchanged.
- **Default and option.**
  - `maxGitAllocBytes` defaults to `4 × maxBlobBytes`, which is 16 MiB with the defaults.
  - New `WithMaxGitAllocBytes(n int64) CLIOption`, where `n <= 0` keeps the derived default.
  - The value must be ≥ `maxBlobBytes + 1`, because the in-core read of an at-limit packed blob
    allocates `size+1`.
  - At 4×, a legitimate artifact deltified on a base of up to 16 MiB is still readable (owner
    question 4).
- **Not applied to `diff`.**
  - Rename detection loads both blobs of a candidate pair. The size-ratio pre-check
    (`estimate_similarity`) skips pairs more than 2× apart, so a large load needs a large deleted
    blob on the diff base, which is content already on `main`.
  - Under the limit, a legitimate rename of a large binary on `main` (more than 16 MiB, at least
    50 % similar) would fail `Diff`. That fails `DiscoverChanges` and wedges `IncrementalSync`.
    So `diff` stays unlimited, and the residual is recorded in KNOWN-LIMITATIONS.
- **Not applied to mutating ops** (`checkout`, `merge`, `commit`, `add`, `branch`, `config`,
  `push`) or to `Clone`. Checkout and merge materialise legitimate large files.
- **Typed mapping before classification.** If the stderr kept by `runBounded` contains
  `attempting to allocate` and `over limit`, it returns before `classifyGitError` runs:
  - for `read_file`, the same `GitError{Permanent, "read_file", "blob exceeds read limit",
    &BlobTooLargeError{Size: -1}}` as the stdout overflow;
  - for other ops, `OutputTooLargeError`.
  - Without the mapping, git's own text (`fatal: packed object … is corrupt`) reaches logs and
    API messages as a corruption report. Discovery would also file the read under `Errors` rather
    than `Refused`.
- **Verified 2026-10-06** (git 2.54, macOS; the variable has been in git since well before the
  image's 2.39.5, and AC 18 pins it on CI's git):
  - The delta-base PoC is refused at 4 MiB child RSS, against 71 MiB without the limit.
  - The gitlink case is refused at 5 MB, against 253 MB.
  - A **loose** 20 MiB blob under a 16 MiB limit still streams, with exit 0 and 4.9 MB RSS.
  - With `GIT_ALLOC_LIMIT=16 MiB`, these show no false refusal: SMP `ls-tree -r -z`,
    `log -n 500 --format=…%B`, the full-history `diff -z`, SMP's largest blob (345 KB) and
    Spine's 6 MiB `embed-smoketest` blob.
- **Interplay with SMP.**
  - SMP TASK-004's quarantine hook refuses pushed **blobs** over 8 MiB (`SMP_GIT_MAX_PUSH_OBJECT_BYTES`).
    Below the 16 MiB alloc limit, no newly pushed SMP blob can trip it.
  - The hook does not check commit or tree objects. It also does not cover standalone Spine,
    Spine used as a library, or objects stored before the hook, so this bound is still
    required.

### 3. A ceiling on every other command's output

This changes `run` (`cli.go:626-687`).

- **The core moves into `runBounded(ctx, op, limit, sizeHint, args...) ([]byte, error)`.**
  `run` becomes `runBounded(ctx, op, c.maxOutputBytes, 0, args...)` plus `string(...)`.
- **Stdout uses a `cappedBuffer`.** Its `Write` appends up to `limit`, then sets `overflow` and
  returns `errStdoutLimit`.
  - `os/exec`'s copy goroutine stops and closes the pipe (`pr.Close() // in case io.Copy stopped
    due to write error`).
  - git dies on EPIPE, and `Wait` returns.
  - `runBounded` checks `overflow` before classifying stderr, and returns
    `GitError{Permanent, op, "output exceeds read limit", &OutputTooLargeError{…}}`.
  - `ReadFile` maps `errStdoutLimit` to `BlobTooLargeError{Size: -1}` instead.
- **Stderr uses a truncating `cappedBuffer` (64 KiB).** It keeps the head, which is all
  `classifyGitError` reads, and never errors.
- **Push-environment handling** (`:639-673`) is unchanged.
- **Headroom.** 16 MiB is about 70× SMP's own `ls-tree -r` output (235 690 bytes for 3 537
  files) and about 170× Spine's (98 456 bytes).

> **Design-check note (2026-10-06) — scope the failing ceiling to read ops.**
> - **Mutating ops must not fail on output size.** git performs the side effect *before* it
>   prints. Verified: `git merge --ff-only run`, with its reader closed after 4 KiB, died of
>   SIGPIPE (exit 141) **and `main` had moved**. 3 000 files with 240-byte paths already give
>   about 1 MB of merge diffstat and summary, so roughly 50k files, or 4k files with 4 KiB paths,
>   cross 16 MiB.
>   - With a failing ceiling, standalone `CLIClient.Merge` would report a governed merge as
>     failed after it had landed.
>   - SMP is not exposed here (its merges go through `RealGitOperator`), but SMP's `Commit` and
>     `Push` also go through `run`.
>   - **Amendment:** `checkout`, `merge`, `commit`, `add`, `branch`, `config` and `push` capture
>     stdout through a **draining** writer. It keeps the first `maxOutputBytes`, discards the
>     rest, and its `Write` always returns `len(p), nil`, so git never sees EPIPE.
>   - `Merge` reads only `Fast-forward` near the head of the output, so the kept head is enough.
>   - The failing `cappedBuffer` applies to the read ops listed in §2a, plus `diff`.
> - **Stderr keeps head *and* tail.** It keeps the first 32 KiB and the last 32 KiB, with a
>   `…[truncated]…` marker between them. The deciding `fatal:` or `error:` line is usually the
>   last one, and `classifyGitError` searches the whole string.
> - **Implementation pitfall.** `cappedBuffer` must not embed `bytes.Buffer` or implement
>   `io.ReaderFrom`. `io.Copy` would use `ReadFrom`, which grows by `MinRead` *before* it sees
>   EOF and so doubles a pre-sized buffer. AC 3's 1.25 MiB bound catches this.
> - **What a read-op ceiling hit does downstream (deterministic, per workspace).**
>   - When `DiscoverChanges`' `Diff` fails, `IncrementalSync` has already written `syncing` and
>     returns the error without recording `error` (`service.go:185-193`). The row stays
>     `syncing` at the old commit, and every tick fails again.
>   - `FullRebuild` fails the same way on `ListFiles`, and `NextID` fails too.
>   - To get there, an attacker must land a commit on `main` whose listing exceeds 16 MiB, which
>     takes a governed merge.
>   - This is accepted as fail-closed, and better than the OOM it replaces. It is recorded in
>     KNOWN-LIMITATIONS, pinned by AC 20, and its visibility (an `error` state) is TASK-011's
>     (P3-14 class).

### 4. NUL-delimited `ListFiles` and `Diff`

- **`ListFiles`** (`cli.go:523-539`) runs `ls-tree -r --name-only -z [eoo] <ref>`.
  - The output is split on `\x00`, and only the empty terminator element is dropped. There is no
    `TrimSpace`.
  - `matchSimpleGlob` filtering is unchanged.
- **`Diff`** (`cli.go:426-474`) runs `diff --name-status -z [eoo] <from> <to>`. The output goes to
  a new `parseNameStatusZ(out string) ([]FileDiff, error)`:
  - Records are `<status>\0<path>\0`. For status letters `R` and `C` they are
    `<status>\0<src>\0<dst>\0` (verified: `R100|old|new|`, `C100|src|dst|`).
  - Mapping:

    | git status | FileDiff |
    |---|---|
    | `A` | `added` |
    | `D` | `deleted` |
    | `M`, `T` | `modified` |
    | `R*` | `renamed` (`OldPath`=src, `Path`=dst), as today |
    | `C*` | `added` at `Path`=dst (the source is unchanged) |
    | anything else | `modified` at the single path, as today's `default` |

    *Today `C` falls into `default` and reports the copy **source** as modified. That only
    happens when `diff.renames=copies` is configured.*
  - **A truncated or malformed stream returns an error.** It never returns a partial list: one
    mis-split would shift every later record. The error does not echo paths.
  - Empty output → `nil, nil`, as today (`TestDiffEmptyResult`).
- **Kept as is:** porcelain `git diff` (rename detection still follows `diff.renames`, default on)
  and the existing `ValidateRef` gates (`:427-432`).

### 5. `core/artifact`: discovery skip-and-record, `Read` mapping, ID collision

**Types** (additive, `discovery.go:12-31`):

```go
type RefusalReason string
const (
    RefusedOversized  RefusalReason = "oversized"
    RefusedUnsafePath RefusalReason = "unsafe_path"
)
type RefusedFile struct {
    Path   string
    Reason RefusalReason
    Size   int64 // oversized only (-1 = unknown)
    Limit  int64 // oversized only
}
// DiscoveryResult gains: Refused []RefusedFile
// ChangeSet       gains: Refused []RefusedFile

// UnsafePath reports a repository path discovery will not project: not valid UTF-8, or
// containing a byte < 0x20 or 0x7f (owner question 2).
func UnsafePath(p string) bool
```

- **Invalid UTF-8 must be refused whatever owner question 2 decides.**
  `projection.artifacts.artifact_path` is a `text` primary key (`migrations/001_initial_schema.sql:22`).
  A UTF8 Postgres rejects such a parameter (SQLSTATE 22021).
  - Before this task, git quoted those bytes, so the path never reached the store.
  - With raw `-z` paths it would. `IncrementalSync` would then count a sync error and never
    advance (`adapters/projection/service.go:313-320`), wedging the workspace's projection.

> **Design-check note (2026-10-06) — what "control character" covers (owner question 2).**
> Today git's quoting kept every byte ≥ 0x80 out of the projection. Raw `-z` paths let through
> valid-UTF-8 runes that are as hazardous for line-oriented and display consumers as C0 bytes.
> The recommended default for `UnsafePath` is therefore broader:
> - `unicode.IsControl(r)`: C0, DEL **and C1** (U+0080–U+009F; U+0085 NEL is a line break for
>   several parsers);
> - U+2028 and U+2029, which JavaScript and JSON-in-HTML consumers treat as line terminators;
> - the bidi embedding, override and isolate controls U+202A–U+202E and U+2066–U+2069, which can
>   make a displayed path, such as a `.spine/…` protected-path warning, read as a different path.
>
> `Slugify` keeps only letters, digits and `-`, and none of these are letters. So no
> platform-created path is refused; only hand-pushed paths are. AC 9 gains a U+0085, U+2028 and
> U+202E row. Unicode normalisation (NFC vs NFD spellings of the same name) is not an integrity
> issue and is not refused.

**`DiscoverAll`** (`discovery.go:59-115`):

- **Workflow candidate** (`IsWorkflowPath`): if `UnsafePath(file)` → `Refused{unsafe_path}`;
  otherwise as today.
- **`.md` candidate, after the template check:**
  - if `UnsafePath(file)` → `Refused{unsafe_path}`, and **no `ReadFile` call**;
  - otherwise read it. On `errors.As(err, *git.BlobTooLargeError)` →
    `Refused{oversized, Size, Limit}`.
  - Every other read error → `Errors`, as today.
- **`Refused` entries are not `Errors`**, so they do not make a rebuild `error`.

**`DiscoverChanges`** (`discovery.go:122-243`). A local helper classifies each read as
content, oversized, or other error. Other errors keep today's `continue`; see Out of scope.

| diff status | change |
|---|---|
| `added` | unsafe or oversized new path → `Refused`; otherwise unchanged |
| `deleted` | old content oversized → `Deleted` (idempotent store delete, `postgres_projections.go:136-139`), so no stale row survives; unsafe → skipped (never projected) |
| `renamed` | old side oversized → `Deleted`; new side unsafe or oversized → `Refused` |
| `modified` | unsafe → `Refused`. New content oversized → `Deleted` **and** `Refused`: the governed row is removed, mirroring a parse failure at `:226-231`. Old content oversized → treated as "was an artifact", so a now-non-artifact file is `Deleted` |

**`Service.Read`** (`service.go:282-305`): after the unchanged `ErrKindNotFound` branch,
`errors.As(err, *git.BlobTooLargeError)` → `domain.NewError(domain.ErrPayloadTooLarge, "artifact
<path> at ref <ref> is <n> bytes, over the <limit>-byte read limit")`.

- The standalone gateway maps that to 413 (`service/internal/gateway/response.go:94-95`).
- SMP's `wrapEmbeddedError` → `httpStatusForSpineErrorCode` does too
  (`internal/customer/spineembedded/proxy.go:1623-1624`).

**`Service.List`** (`:342-345`) is unchanged. It already skips any unreadable file.

**`IsIDCollision`** (`renumber.go:26-35`): `errors.Is(err, git.ErrBlobTooLarge)` → `true, nil`.
The file exists. Today any read error means "no collision", which would be wrong once a large
existing file stops being readable.

**`NextID`** needs no code change. The raw paths from §4 fix it.

### 6. `core/workflow`

`Service.Read` (`core/workflow/service.go:201-226`): the same `ErrPayloadTooLarge` mapping after
the `ErrKindNotFound` branch. `List` (`:239-258`) is unchanged; it already skips read errors.

### 7. `adapters/projection`: skip, record, advance

- **`FullRebuild`** (`service.go:57-157`):
  - after `DiscoverAll`, call `s.recordRefused(ctx, head, result.Refused)`;
  - the completion log (`:147`) gains `"refused", len(result.Refused)`;
  - `Refused` entries are not added to `projErrors` (`:94`), so a refused file never makes the
    rebuild end in `error`. `DeleteAllProjections` (`:78`) has already removed any stale row.
- **`IncrementalSync`** (`:161-360`):
  - `recordRefused(ctx, head, changeset.Refused)` runs after the changeset is applied.
  - In the workflow diff loop (`:284-306`), a diff whose `Path` is `UnsafePath` is skipped and
    recorded as `Refused{unsafe_path}` for `added`/`modified`/`renamed`. An unsafe `deleted` is
    ignored.
  - Refused files never increment `syncErrors`, so the sync advances to `head`.
  - The `projection_synced` payload (`:345-352`) gains `"refused": n`.

  > **Implementation note (2026-10-14, review round 2) — timing superseded.** Both entry points
  > report refusals as soon as discovery has decided them, before anything that can hold the sync
  > or fail the rebuild, not after the changeset is applied (Deviation 19).
- **`projectWorkflow` and `projectBranchProtection`** (`:500`, `:569`) are unchanged.
  - An oversized workflow or config now fails at `ReadFile` instead of at `yamlsafe.Decode`.
  - It is still a sync error with the old projection retained (I4).
- **`recordRefused`**, for each entry:
  - `observe.Logger(ctx).Warn("projection skipped file", "path", p, "reason", r, "size_bytes", n,
    "limit_bytes", l, "commit", shortSHA(head))`;
  - `observe.GlobalMetrics.ProjectionFilesSkipped.Inc()`;
  - when `s.events != nil`: `event.EmitLogged` of `domain.EventProjectionFileSkipped`, with
    `ArtifactPath = p`, payload `{"path", "reason", "size_bytes", "limit_bytes", "commit"}`
    (sizes `null` for `unsafe_path`), and the deterministic
    `EventID = "projskip-" + head[:12] + "-" + hex(sha256(reason + "\x00" + path))[:12]`. A
    rebuild at the same commit re-emits the same ID, which the delivery queue deduplicates.
- **The pre-existing dropped `Diff` error at `:254` is TASK-011's (P3-14).** This task does not
  change that line.

### 8. Event, metric and documentation

- **Event.** `core/domain/event.go`, operational block (`:31-78`):
  `EventProjectionFileSkipped EventType = "projection_file_skipped"`, with a doc comment naming
  the payload.
  - It is added to `allEventTypes` (`adapters/delivery/subscriber.go:137-176`).
  - It is operational, so `isDomainEvent` stays false (`adapters/delivery/retry_test.go:123-136`
    pattern).
- **Metric.** `core/observe`: a `ProjectionFilesSkipped` counter, exported as
  `spine_projection_files_skipped_total` (next to `spine_projection_syncs_total`,
  `export.go:87`).
- **`architecture/event-schemas.md`:**
  - a new §4.13 `projection_file_skipped` (envelope, payload, when it fires, idempotency);
  - a row in the operational emission map (`:579-590`).
- **`architecture/git-integration.md`:**
  - §8.1: discovery reads at most the per-blob limit; it records oversized files and files with
    unsafe names (`projection_file_skipped`) and does not project them;
  - §8.2: paths are read NUL-delimited, so non-ASCII names are discovered;
  - a modified artifact that becomes oversized has its projection removed.
- **`KNOWN-LIMITATIONS.md` "Security Notes":** a new "Git read bounds" note:
  - the per-blob and per-command limits and their options;
  - the named-ref probe/read window from §2;
  - receive-time inflation is not covered (see Out of scope).
- **`CHANGELOG.md` staging section:** a "Security" entry for the limits, the typed errors, the
  new event, and raw-path `Diff`/`ListFiles`.

> **Design-check note (2026-10-06) — documentation additions.**
> - **`KNOWN-LIMITATIONS.md` "Git read bounds"** also states:
>   - the `GIT_ALLOC_LIMIT` bound (§2a), and that `diff` runs without it, so rename detection
>     can still inflate large blobs that are already on the diff base;
>   - that a read-op output over the ceiling fails closed, so `IncrementalSync`, `FullRebuild`
>     and `NextID` stop for that workspace until the content is removed (§3 note);
>   - that a legitimate artifact stored as a delta on a base larger than the alloc limit is
>     refused as oversized.
> - **`CHANGELOG.md`** gains two more entries:
>   - an **"Upgrade note"**: run one projection rebuild after upgrading (standalone:
>     `POST /api/v1/system/rebuild`), because artifacts and workflows at non-ASCII paths already
>     on `main` are not picked up by `IncrementalSync` until they next change; rebuilt
>     projections may show artifact IDs that the old `NextID` allocated twice;
>   - a **"Behaviour"** line: the new `projection_file_skipped` type reaches existing
>     subscriptions that receive all event types.

### 9. What does not change

- **`core/engine/merge.go`.**
  - `applyCommitStatus` already logs and skips an unreadable cascade path (`:606-613`). For the
    primary path it returns an error, which `preflightCheck` logs before proceeding with the
    merge (`:101-106`).
  - An oversized cascade file therefore keeps its status. The merge is not blocked, which is the
    existing best-effort contract.
  - `cascadePaths` (`:688-713`) gains non-ASCII child paths through §4.
- **`core/evidence/loader.go:112-118`.** An oversized evidence file is an error, not "not
  found", so it fails closed. This is correct and unchanged.
- **`service/internal/gateway/handlers_tasks.go:131`.** It is reached only after `artSvc.Read`
  (`:115`), which now returns 413 first.
- **`Clone`, `Commit`, `Merge`, branch and push methods** are unaffected, beyond the stdout
  ceiling.

  > **Design-check note (2026-10-06).** Their stdout is now captured through the draining writer
  > (§3 note). They can never *fail* on output size, and they get no `GIT_ALLOC_LIMIT` (§2a).
  > So they are unaffected, full stop.

### 10. SMP: effect, compatibility gate, and what the dependent SMP tasks must do

**SMP's only direct `CLIClient.Diff` consumer** is `RunBranchDiffReader.Changes`
(`runbranchdiffreader.go:254-267`).

- It C-unquotes the quoted output (`unquoteDiffPaths` / `unquoteGitPath`, `:271-352`).
- After §4, a raw path that begins with `"` would be decoded a second time. Verified with
  `zz_t003_smp_unquote_test.go`:
  - **It fails closed.** `"quoted.md` → "no closing quote", which the dispatch layer turns into a
    503 for the whole run-detail Artifacts section.
  - **Or it is silently mis-decoded.** A raw path `"x"` becomes `x`. A directory named `".spine`
    holding `x.md"` is reported as `.spine/x.md`, which spoofs the protected-path warning
    (INIT-027/EPIC-002/TASK-011).
- **SMP TASK-003 (P3-9.smp) is therefore a hard precondition.** It moves this reader to an
  SMP-owned NUL-delimited diff and deletes `unquoteDiffPaths`. `unquoteGitPath` and
  `strconv.Unquote` have no other path users in SMP (grep, 2026-10-06).

**Everything else SMP gets on the bump compiles unchanged (I3).** Behaviour changes:

1. **Every SMP `CLIClient` gets the limits.**
   - `ListRunArtifacts` skips an oversized file through its existing skip-with-debug-log
     (`artifacts_runartifacts.go:237-241`).
   - `artifact.Service.Read` and `workflow.Service.Read` surface 413 through `wrapEmbeddedError`.
   - `GetContent` → `ReadRaw` returns a non-`NotFound` `*GitError`. That falls through to
     `wrapEmbeddedError`'s default **502 `EMBEDDED_FAULT`** (`wrap_error.go:81-87`) until SMP
     TASK-008 maps it. Before this task it was an OOM.
2. **Projection** (worker and provisioning):
   - oversized and unsafe-named files are not projected and are logged at WARN;
   - non-ASCII artifacts and workflows **appear for the first time**;
   - SMP passes a **nil event router** (`worker.go:314`, `provision_steps_runtime.go:322`), so in
     the pilot the record is the WARN log and the metric only.
3. **`NextID` through `ArtifactWriter.Lister`** stops re-allocating IDs hidden behind non-ASCII
   slugs.
4. **Provisioning `FullRebuild` takes longer** (§2 cost).

> **Design-check note (2026-10-06) — stored data and upgrade.**
> - **The backfill is not automatic.**
>   - `IncrementalSync` only processes `LastSyncedCommit..HEAD`.
>   - SMP's worker runs `FullRebuild` only when there is no `sync_state` row
>     (`worker.go:319-323`).
>   - So an artifact or workflow already on `main` at a non-ASCII path, created for example by
>     `Slugify` from a non-English title (Purpose, P3-9 note), stays unprojected after the bump
>     until that file changes.
>   - SMP already has the hook: `Provisioner.RebuildProjection`
>     (`provision_steps_runtime.go:256-265`, used by the INIT-020 boot backfill).
> - **Duplicate IDs surface.** Neither repo has a uniqueness constraint on
>   `projection.artifacts.artifact_id` (only `idx_artifacts_id`), so a rebuild does not fail. But
>   IDs that `NextID` allocated twice before the fix (`EPIC-001` alongside `EPIC-001-résumé/`)
>   then show up as two rows with the same ID.
> - **Previously invisible workflows become active.**
>   - A `workflows/*.yaml` with a non-ASCII file name on `main` was never projected.
>   - After the bump it is projected, either on rebuild or the next time it changes.
>   - Before the bump, list such paths with
>     `git ls-tree -r -z --name-only main -- workflows .spine | LC_ALL=C grep -z '[^ -~]'`.
> - **Default (owner question 3):** SMP TASK-008 runs one `RebuildProjection` per existing
>   workspace after the bump. It logs any `artifact_id` that appears on more than one path, and
>   any newly projected non-ASCII workflow path, for the operator. Spine ships only the CHANGELOG
>   upgrade note (§8 note).

**Compatibility gate.** Run it on the task branch before merge, and record it in Implementation
Notes:

1. Take SMP `main` **with SMP TASK-003 merged**, and point `replace ../spine` at this branch.
2. Run `go build ./...` and `go vet ./...`.
3. Run `go test ./internal/customer/spinecore/... ./internal/customer/projectionwriter/...
   ./internal/customer/spineembedded/... ./internal/customer/workspace/...`. It must include:
   - SMP TASK-003's raw-path `RunBranchDiffReader` test with a path that **starts with `"`**;
   - `TestRepoArtifacts_ParseCleanlyUnderSpine` (`projectionwriter/repo_artifacts_test.go:36`);
   - `TestWorker_Integration_*` (`worker_test.go:469`, `:490`);
   - `artifactservice_test.go`.
4. Check that `grep -rn "unquoteDiffPaths\|unquoteGitPath" internal/` is empty.

**What SMP TASK-008 (F4.smp-projection) must do after this merge:**

1. **Map `spinegit.ErrBlobTooLarge` to 413 in `GetContent`** (`artifacts_content.go:180-191`).
   Optionally flag refused items in `ListRunArtifacts`.
2. **Give SMP TASK-004's own too-large error the same identity.** Wrap it or add an `Is`, so that
   `errors.Is(err, spinegit.ErrBlobTooLarge)` holds through `RealGitOperator.ReadFile`. Then
   `core/artifact` and `core/engine` classify both back ends alike.
3. **Pin `RunBranchDiffReader` reads to the run-branch commit SHA** resolved once in
   `BuildRunBranchDiffReader`, instead of `refs/heads/<run>` (`:177`). This closes §2's
   named-ref window on SMP's one per-request read path.
4. **Extend `TestRepoArtifacts_ParseCleanlyUnderSpine` to fail on `result.Refused`.**
5. **Decide whether to surface refused files** (an event router, or the metric on SMP's health
   surface). This is a follow-up, not required.

> **Design-check note (2026-10-06) — two more items for SMP TASK-008 (`E4-TASK-008-content-integrity-bump.md`).**
> 6. **Post-bump backfill** (stored-data note above, owner question 3): run `RebuildProjection`
>    once per existing workspace, and report duplicate `artifact_id`s and non-ASCII workflow
>    paths.
> 7. **Know that `WriteContent` needs Spine TASK-004 too.** After this task, `ReadRaw` on an
>    oversized run-branch file fails cleanly, and `WriteContent` falls through to
>    `Service.Update`'s worktree `os.ReadFile` (Purpose, F4 note). The bump must include Spine
>    TASK-004 (I7) for F4 to be closed on that path. SMP TASK-008 already lists TASK-004 as a
>    dependency.
>
> For SMP TASK-004 (out of this task's scope, recorded so it is not lost): its quarantine hook
> checks `blob` objects only. A huge **commit** object, meaning an attacker-sized message that
> `HistoryReader.Log` buffers, or a huge **tree** object passes it. Checking every object type
> against the cap is a one-word change in its `--batch-check` filter.

### 11. Coordination and ordering

1. **Spine TASK-002 (F10, P3-32, P3-36) lands first.**
   - It rewrites the same three functions' argv (ref grammar, qualified refs,
     `--end-of-options`).
   - This task keeps TASK-002's validation and terminator placement, and changes only the
     subcommand, flags and parsing. Tests use qualified refs (`refs/heads/spine/run/x`).
2. **SMP TASK-003 must be on SMP `main` before this task merges to Spine `main`.**
   - Any SMP `.spine-revision` bump that includes a Spine `main` commit containing this task
     (for example SMP TASK-006's secrets bump) would otherwise break `RunBranchDiffReader`.
   - The PR may be opened and reviewed earlier.
   - After that, any SMP bump may include this task. SMP TASK-008 adopts it, and SMP TASK-012
     makes the final bump.
3. **Spine TASK-011 (P3-13/14/22/23)** edits the same `IncrementalSync`/`FullRebuild` region in
   wave 2.
   - This task rebases on it.
   - If TASK-011 reshapes `DiscoverChanges`' result, `Refused` hangs off whatever type it
     returns.
   - The `:254` dropped-error fix and swallowed non-size read errors stay TASK-011's.
4. **Spine TASK-005 (F2 merge binding)** reads the cascade at the validated SHA. That removes the
   named-ref window for the engine. There is no textual overlap: this task does not edit
   `merge.go`.
5. **Spine TASK-012 (F9)** serialises stateful `CLIClient` operations. Its mutex wraps the call
   sites; `runBounded` is compatible.
6. **Spine TASK-004 (F5)** edits only `WriteAndStageFile`. There is no overlap.

   > **Design-check note (2026-10-06) — superseded.** Spine TASK-004, as amended by its own design
   > check, adds `core/worktreefs` and rewrites the write paths in `core/artifact/service.go`
   > (`writeAndCommit`/`Update`), `acceptance.go`, `successor.go`, `renumber.go` and
   > `core/workflow/service.go`.
   > - **Same files.** This task edits `Read` in `core/artifact/service.go` and in
   >   `core/workflow/service.go`, and `IsIDCollision` in `renumber.go`. These are different
   >   functions in the same files, so whichever task lands second rebases. There is no semantic
   >   conflict: both map oversized reads to `ErrPayloadTooLarge`.
   > - **Shared limit.** TASK-004's I7 `worktreefs.DefaultMaxReadBytes` must equal
   >   `git.DefaultMaxBlobBytes`; owner question 1 governs both. The task that lands **second**
   >   adds the parity test (AC 21) in `core/artifact`, which may import both packages.
   >   `core/worktreefs` is stdlib-only, so the test cannot live there.
   > - **F4 closure needs both tasks** (Purpose, F4 note). There is still no ordering dependency
   >   between them.
7. **Dependencies:** spine:TASK-002 and smp:TASK-003. Wave 3. Risk: medium. The parser and
   discovery semantics change, and the cross-repo ordering above applies.

## Decisions

1. **DECIDED — Size probe (`cat-file -s`), then one bounded `git show`.**
   - *Rejected: `io.LimitReader` alone (one process).* It bounds Spine's heap, but git still
     reconstructs a deltified blob in its own memory before writing a byte (code-traced: git's
     streaming read covers only loose and undeltified packed objects). That happens on every
     read, inside the same 512 Mi cgroup. Packs of ≥ `receive.unpackLimit` (100) objects keep the
     pusher's deltas.
   - *Rejected: one `git cat-file --batch` process (header first, kill on oversize).* "missing"
     covers both an absent path and a bad ref, so it breaks I2. A bad ref would become `NotFound`,
     which the evidence loader reads as "no evidence". Paths containing `\n` also need the batch
     `-z` flag, which older gits lack.
   - *Rejected: `ls-tree -l` for oid + size.* It brings in pathspec semantics (normalisation,
     trailing `/`, magic) where `<ref>:<path>` is an exact tree lookup.
   - *Rejected: three processes* (`rev-parse` → `-s <oid>` → `show <oid>`). It would close the
     named-ref window at 3× the spawn cost, while the window is narrow and is closed at its two
     real sources (TASK-005, SMP TASK-008).

   > **Design-check note (2026-10-06).** The decision stands, but its rationale needs two
   > corrections.
   > - git's streaming read covers loose objects, and packed **undeltified objects above
   >   `core.bigFileThreshold`** (512 MiB). It does not cover every undeltified packed object
   >   (measured: 274 MB RSS for a 256 MiB packed blob).
   > - The probe bounds only the **target** object. A delta base or a gitlink's patch is
   >   unbounded by it.
   >
   > The probe is still worth its spawn: it refuses most oversized reads before git allocates
   > anything, and it yields the exact `Size` for the warning. Decision 12 bounds what is left.
2. **DECIDED — Keep `git show` for the content.** It is byte-identical for blobs (no textconv or
   filters without `--textconv`), and it keeps tree output. *Rejected: `cat-file blob`.* It
   changes the non-blob behaviour for no gain.
3. **DECIDED — `*GitError{Permanent}` wrapping a typed cause with `Is`.**
   - *Rejected: a new `GitErrorKind`.* Code that switches on `Kind`, in both repositories, would
     need a new case to stay correct.
   - *Rejected: a bare sentinel.* It loses the size and limit the warning needs.
4. **DECIDED — Limits are `CLIClient` options with package defaults.** There is no environment
   variable and no per-call variant. SMP inherits the defaults without code, and tests set small
   limits explicitly.
5. **DECIDED — Skip, record and advance for `.md` discovery; governance config stays
   fail-closed** (I4).
   - An oversized file can never become readable at that commit, so blocking the sync buys
     nothing.
   - Blocking would also let one member freeze a workspace's projection.
6. **DECIDED — A modified artifact that becomes oversized loses its projection row.** This
   mirrors the existing parse-failure rule (`discovery.go:226-231`). *Rejected: keep the last
   good row.* The projection would then serve content that is no longer on `main`.
7. **DECIDED — `-z` on porcelain `git diff`, with a strict record parser.** *Rejected:
   `git diff-tree`.* It changes rename-detection semantics (it ignores `diff.renames`) and widens
   TASK-002's argv surface. The strict parser handles `R`/`C` two-path records, because a single
   mis-split under `-z` would corrupt every later record.
8. **DECIDED — Refuse and record unsafe paths at discovery.** Invalid UTF-8 is refused because
   Postgres rejects it (§5). Control characters are refused by default (owner question 2).
9. **DECIDED — Put a stdout ceiling on every command, not only `ReadFile`.** It is the exact code
   F4 cites (`cli.go:675-686`), it costs a few lines, and it closes the commit-message vector in
   `Log`.

   > **Design-check note (2026-10-06) — narrowed.**
   > - Every command still gets a bounded buffer.
   > - Only **read** ops fail when they exceed it.
   > - Mutating ops drain and truncate, because git applies the side effect before it prints
   >   (§3 note, AC 19).
   > - `Log`'s commit-message vector also needs §2a: git builds the commit object in memory
   >   before it writes anything.
10. **DECIDED — A new operational event, plus a `refused` count on `projection_synced`.**
    *Rejected: payload-only on `projection_synced`.* `FullRebuild` never emits it, and
    subscribers want a per-file signal they can filter on.
11. **DECIDED — No interface change** (I3). SMP implements `git.GitClient` and
    `engine.GitOperator`.
12. **DECIDED (design check, 2026-10-06) — `GIT_ALLOC_LIMIT` on read commands (§2a).**
    - *Rejected: `rev-parse` the blob OID, then probe and read by OID.* That closes the
      named-ref race but neither the delta-base nor the gitlink case.
    - *Rejected: `ulimit -v` through `sh -c`.* It is not portable to macOS, it counts mapped
      packfile windows (git mmaps up to 1 GiB per window), and it adds a shell.
    - *Rejected: `-c core.bigFileThreshold=<limit>`.* It makes undeltified packed blobs stream,
      but git never streams a deltified object, so it does not cover the base.
    - *Rejected: applying it to `diff` and mutating ops.* That would fail legitimate large
      renames, merges and checkouts (§2a).
    - `GIT_ALLOC_LIMIT` is not documented in git(1), but it is the variable git's own
      large-object tests use. AC 18 pins its effect on CI's git, so a git that drops it fails
      the build instead of silently removing the bound.
13. **DECIDED (design check, 2026-10-06) — Mutating ops drain stdout and never fail on its
    size** (§3 note). *Rejected: `merge --no-stat`.* It fixes only `Merge` and changes
    TASK-002's argv.

## Owner questions

The design works without answers to these, using the stated defaults.

1. **Default read limits (plan owner question 5).**
   - *Default:* 4 MiB per blob, 16 MiB per command's stdout.
   - The largest tracked `.md` is about 158 KiB in SMP and 34 KiB in Spine.
   - Spine's API refuses artifact writes over 512 KiB (`handlers_helpers.go:16`).
   - With the pre-sized buffer, a read now costs about 1× the blob instead of about 6×.
   - *Alternatives:*
     - 1 MiB per blob: 2× the API write cap and tighter memory, but a pushed document of 1–4 MiB
       would drop out of the projection, with a warning;
     - 16 MiB per blob.
   - Only the constants change; the tests set limits explicitly or derive them from the
     constants.
2. **Paths with control characters** (tab, newline, other C0, DEL).
   - *Default:* refuse and record. They are not projected, and are logged and evented.
   - *Alternative:* project them raw. They are valid UTF-8, and JSON and `slog` escape them.
   - They can reach line-oriented consumers, for example SMP's LBD-8b path headers, which SMP's
     importer already refuses (`internal/import/repository_import.go:224-228`).
   - Invalid UTF-8 is refused either way.

   > **Design-check note (2026-10-06).** The recommended default widens "control characters" to
   > `unicode.IsControl` (adding C1, U+0080–U+009F), U+2028/U+2029, and the bidi controls
   > U+202A–U+202E and U+2066–U+2069 (§5 note). No path the platform creates is affected, because
   > `Slugify` drops all of them.
3. **(Design check, 2026-10-06) Post-upgrade backfill of non-ASCII artifacts.**
   - *Default:* Spine ships a CHANGELOG upgrade note (run one rebuild). SMP TASK-008 runs
     `Provisioner.RebuildProjection` once per existing workspace after the bump. It logs
     duplicate `artifact_id`s and newly visible non-ASCII `workflows/` paths (§10 notes).
   - *Alternative:* no backfill. Such artifacts appear the next time they change, and duplicate
     IDs stay hidden until then.
   - The pilot may already hold such artifacts, because `Slugify` keeps non-ASCII letters.
4. **(Design check, 2026-10-06) Git-side allocation limit (`maxGitAllocBytes`, §2a).**
   - *Default:* 4 × the per-blob limit, which is 16 MiB.
   - *Alternatives:*
     - 1× the per-blob limit: tightest, but more legitimate artifacts stored as deltas on a
       larger historic base become refused;
     - a fixed 64 MiB: fewer refusals, but each concurrent read may cost git about 64 MiB plus
       its delta-base cache.
   - Only the derivation changes. AC 18 sets the limit explicitly.

## Acceptance Criteria

Tests run with no `t.Parallel()` where they measure `runtime.MemStats.TotalAlloc`. Sizes are
explicit options, or derived from the exported defaults, so owner question 1 does not break them.

1. **PoCs become regression tests.**
   - The six overlay PoCs in `_work/task003/` are committed with their assertions unchanged:
     - `adapters/git/cli_bounds_regression_test.go`: the `ReadFile`, `ListFiles` and `Diff`
       PoCs;
     - `adapters/projection/oversized_regression_test.go`: `IncrementalSync` and the non-ASCII
       `FullRebuild`;
     - `core/artifact/id_allocator_regression_test.go`: `NextID`.
   - Run with `-overlay` against `61994fe`, each fails as recorded in Purpose/Evidence. On the
     task branch, each passes.
   - If owner question 1 sets the blob default ≥ 8 MiB, the PoC blob is raised to 2× the default
     before committing.
2. **Oversized blob refused before reading** (`adapters/git`; skeptic-bigblob, verify-r1-9 phase
   A). Default client, with a blob of `DefaultMaxBlobBytes+1` and one of 8 MiB on
   `refs/heads/spine/run/x`:
   - `errors.Is(err, git.ErrBlobTooLarge)`;
   - `errors.As` gives `Size` equal to the blob size, `Limit` equal to the default, and the
     `Ref`/`Path` passed in;
   - `err.(*git.GitError).Kind == ErrKindPermanent`, and `IsRetryable()` is false;
   - `TotalAlloc` during the call is < 1 MiB.
   - *Mutation:* deleting the probe comparison makes `TotalAlloc` ≥ the limit.
3. **At-limit read.** A blob of exactly `WithMaxBlobBytes(1 MiB)` is returned byte-identical,
   with `TotalAlloc` ≤ 1.25 MiB (one pre-sized buffer, no string round trip). A 0-byte blob
   returns a non-nil, zero-length slice.
4. **Second line of defence.**
   - Set the `blobSize` seam to report 1 byte, with an 8 MiB blob and `WithMaxBlobBytes(1 MiB)`.
     Result: `BlobTooLargeError{Size: -1}`, `TotalAlloc` < 2 MiB, and the call returns, so no git
     process is left behind.
   - *Mutation:* removing the stdout cap makes `TotalAlloc` ≥ 8 MiB.

   > **Design-check note (2026-10-06).** Read the bound as **`TotalAlloc` < 4 MiB**. From a 1-byte
   > pre-size, a correct capped buffer grows in steps (32 KiB, 64 KiB, … 1 MiB), so it allocates
   > about 2 MiB in total, plus the `io.Copy` buffer and exec overhead. "< 2 MiB" would fail or
   > flake on a correct implementation. 4 MiB still separates it cleanly from the mutation, which
   > is ≥ 8 MiB.
5. **Error-kind parity (I2).**
   - `TestReadFile` and `TestReadFileNotFound` (`cli_test.go:198-233`) pass unchanged.
   - A new table test gives:
     - missing path → `ErrKindNotFound` by direct `*GitError` assertion;
     - unknown ref → `ErrKindPermanent`;
     - path present in the working tree but not at the ref → `ErrKindPermanent`;
     - tree path → output byte-identical to `git show <ref>:<dir>`.
   > **Implementation note (2026-10-14) — pending the owner's amendment.** The unknown-ref and
   > on-disk-only rows were right on `61994fe` (`invalid object name`, `exists on disk, but not
   > in`: both `ErrKindPermanent`) and are superseded by TASK-002 on Spine `main` (its AC 10 and
   > review row R3): with its trailing `--`, every `ReadFile` miss is `fatal: bad revision`
   > (`ErrKindNotFound`). The test pins Kind and Message parity with the unbounded read on `main`
   > instead (Deviation 2). The proposed replacement text is in Implementation Notes, Review
   > round 2.
6. **Output ceiling.** With `WithMaxOutputBytes(4 KiB)`:
   - `ListFiles` over 300 files → `errors.Is(err, git.ErrOutputTooLarge)`;
   - `Log` over a commit whose message is 8 KiB → the same.
   - With the default ceiling, both succeed.
   - Stderr kept by `runBounded` is ≤ 64 KiB, checked through a helper fed 1 MiB.
   - *Mutation:* removing the ceiling fails the first two cases.

   > **Design-check note (2026-10-06).** The stderr helper is fed 1 MiB of `warning:` lines
   > followed by a final `fatal: Unable to create '/r/.git/index.lock': File exists.` It must
   > keep that last line, and `classifyGitError` on the kept text must give `ErrKindTransient`.
   > *Mutation:* a head-only buffer gives `ErrKindPermanent`.
7. **Defaults are sane** (unit test).
   - `DefaultMaxBlobBytes ≥ 512 KiB`, which is Spine's API write cap, cited in a test comment
     because `service/internal` cannot be imported here.
   - `DefaultMaxBlobBytes ≥ yamlsafe.MaxBytes`, and `DefaultMaxOutputBytes ≥ DefaultMaxBlobBytes`.
   - `WithMaxBlobBytes(0)` and `(-1)` keep the default.
   - *(Design-check note, 2026-10-06:)* without `WithMaxGitAllocBytes`, the git alloc limit is
     `4 × maxBlobBytes`, including after `WithMaxBlobBytes(1 MiB)`. A value below
     `maxBlobBytes + 1` is raised to it. This is checked through an export_test accessor.
8. **Raw listings.** `ListFiles` and `Diff` over `" lead.md"`, `"quoted.md`, `réview.md`,
   `tab\tfile.md`, `new\nline.md` and `plain.md` return exactly those paths.
   - A rename to `re\nnamed.md` gives `renamed` with raw `OldPath`/`Path`.
   - With repo config `diff.renames=copies`, a copy gives `added` at the destination.
   - A type change gives `modified`.
   - `parseNameStatusZ` fed `"R100\x00only-one\x00"` or `"A\x00"` missing its path returns an
     error and no partial slice.
   - Existing `TestDiff`, `TestDiffEmptyResult`, `TestDiffModifiedAndDeleted`, `TestListFiles` and
     `TestListFilesAllAndGlobPatterns` pass unchanged.
   - *Mutation:* dropping `-z` from either command fails the first case.
9. **Discovery** (`core/artifact`, with a counting fake `GitClient` plus a real repo).
   - **`DiscoverAll`:**
     - an oversized `.md` lands in `Refused{oversized, Size, Limit}`, not in `Errors` or
       `Artifacts`;
     - `a\tb.md`, `x\ny.md` and `\xff.md` land in `Refused{unsafe_path}` **with zero `ReadFile`
       calls for them**;
     - `EPIC-001-résumé/epic.md` is parsed into `Artifacts` under its raw path.
     - *(Design-check note, 2026-10-06, under owner question 2's widened default:)*
       `a\u0085b.md`, `a b.md` and `a‮b.md` land in `Refused{unsafe_path}`, and
       `wdrożenie/x.md`, a `Slugify` output, is **not** refused.
   - **`DiscoverChanges`:** a table test covers every row of the §5 table, including
     modified-to-oversized → `Deleted` + `Refused`, and deleted-with-oversized-old → `Deleted`.
   - *Mutation:* removing the `Deleted` on modified-to-oversized fails its row.
10. **Typed read errors.**
    - `artifact.Service.Read` and `workflow.Service.Read` on an oversized file return
      `domain.ErrPayloadTooLarge`.
    - A gateway test (`service/internal/gateway`) gets **413** `payload_too_large` from
      `GET /api/v1/artifacts/<path>`.
    - `IsIDCollision` returns `true` for an oversized existing file.
11. **Projection** (`adapters/projection`; verify-r1-9 phases B/C and
    `store-projection/poc_bigblob`, scaled to `WithMaxBlobBytes(1 MiB)` and 8 MiB blobs):
    1. `IncrementalSync` after a non-artifact `docs/notes.md` lands:
       - the state is `idle` at the new head;
       - `TotalAlloc` < 2 MiB;
       - a recording router got exactly one `projection_file_skipped`, with the payload fields
         from §7;
       - `ProjectionFilesSkipped` rose by 1.
    2. `FullRebuild` with an oversized **artifact** at `HEAD`: not projected, state `idle`, one
       event, `TotalAlloc` < 2 MiB.
    3. A projected artifact modified past the limit: its row is deleted, and the state is `idle`
       at the new head.
    4. An oversized `workflows/x.yaml`: the state is `error`, `LastSyncedCommit` does not advance,
       and the previous workflow projection stays (I4). `TotalAlloc` < 2 MiB.
       > **Implementation note (2026-10-14) — pending the owner's amendment.** Superseded by
       > TASK-011 D2 and its design-check note on this task (orchestrator ruling): an oversized
       > workflow is a `read_bounds` rejection and the sync advances; only
       > `.spine/branch-protection.yaml` stays a held sync error (Deviation 1). The proposed
       > replacement text is in Implementation Notes, Review round 2.
    5. `FullRebuild` over `initiatives/\xff/x.md`: `UpsertArtifactProjection` is **never called
       with invalid UTF-8**. The fake store fails the test if it is.
    6. An unsafe-named `workflows/a\tb.yaml` added on `main` is recorded and not projected.
12. **Event registered.**
    - `domain.EventProjectionFileSkipped == "projection_file_skipped"`.
    - It is in `allEventTypes`, and `isDomainEvent` is false for it.
    - It is documented in `event-schemas.md` §4.13 and in the emission map.
13. **Documentation.** `git-integration.md` §8.1/§8.2, `KNOWN-LIMITATIONS.md` "Git read bounds"
    and `CHANGELOG.md` are updated as in §8, including the §8 design-check additions: the alloc
    limit, the `diff` residual, the fail-closed read ceiling, the upgrade note and the new event
    type.
14. **No interface or schema change.**
    - `go build ./cmd/embed-smoketest ./service/...` passes with no edit to `stubGitOperator`,
      the scenario `codeRepoBehavior` or any `GitClient` implementation.
    - `git diff --stat 61994fe -- migrations/` is empty.
    - `make lint-boundaries` passes with no new allowlist entry.
15. **Spine gates.** These pass and are recorded in Implementation Notes:
    - `go build ./...`;
    - `go vet ./...`;
    - `go test ./...`;
    - `go test -tags scenario ./service/scenariotest/...`;
    - `make lint`;
    - `make lint-security`, with no new finding.
    - Also record the FullRebuild time of SMP's repository (`TestRepoArtifacts_ParseCleanlyUnderSpine`
      wall time) before and after.
16. **SMP compatibility gate** (§10) passes on SMP `main`, with SMP TASK-003 merged and
    `replace ../spine` at this branch. Record the SMP commit, the commands and the results.
    *(Design-check note, 2026-10-06:)* also record that `TestRepoArtifacts_ParseCleanlyUnderSpine`
    reports `len(result.Refused) == 0` on SMP's own repository. That shows §2a's alloc limit and
    §5's unsafe-path rule refuse nothing real.
    > **Implementation note (2026-10-14) — for the orchestrator to record.** It passes except for
    > three SMP tests that fail identically with Spine `main` (SMP TASK-004's tests pin SMP's
    > pre-wave-2 Spine; SMP TASK-008 / TASK-012 adapt them at the bump). See Review round 2.
17. **Merge ordering.** The PR merges to Spine `main` only after SMP TASK-003 is on SMP `main`.
    The PR description names the SMP commit.
    > **Implementation note (2026-10-14) — the orchestrator's.** The precondition holds: SMP
    > TASK-003 is on SMP `main` `f5c6fd6d`. The PR and its description are not made from the
    > branch.

*ACs 18–22 were added by the 2026-10-06 design check.*

18. **Git's own memory is bounded (§2a).** Commit `_work/task003/designcheck/zz_t003dc_deltabase_test.go`
    as `adapters/git/cli_gitmem_regression_test.go`. Peak RSS is measured in a re-exec of the test
    binary through `RUSAGE_CHILDREN`, on linux and darwin; on other platforms only the error
    assertions run. Default client unless stated:
    1. **Delta base.** A 3 000 000-byte tip blob, stored by stock `git repack -adf` as a delta on
       a 64 MiB base. `ReadFile` returns `errors.Is(err, git.ErrBlobTooLarge)` with `Size == -1`,
       the error text contains `read limit` and not `corrupt`, and the git child's peak RSS is
       < 32 MiB. On `61994fe` the read succeeds, and the child reaches about 71 MiB.
    2. **Gitlink.** A gitlink `g.md` points at an in-repo commit that adds a 64 MiB blob. The
       assertions are the same as in (1).
    3. **No false refusal.**
       - A packed undeltified blob of exactly `WithMaxBlobBytes(4 MiB)` is returned
         byte-identical.
       - A 3 MiB tip deltified on a 12 MiB base, which is within 4× the limit, is returned
         byte-identical.
    4. **`diff` is exempt.** `main` holds a 20 MiB `a.bin`. A branch renames it to `b.bin` with
       one changed line. Under `WithMaxGitAllocBytes(16 MiB)`, `Diff` still returns `renamed`.
    - *Mutation:* dropping `GIT_ALLOC_LIMIT` from the read env makes (1) and (2) succeed with
      RSS > 64 MiB. Adding it to `diff` makes (4) fail.
19. **Mutating ops never fail on output size (§3 note).** With `WithMaxOutputBytes(4 KiB)`:
    - `Merge` with `Strategy: "fast-forward"` of a branch adding 300 files (stdout over 4 KiB)
      returns `err == nil`, `SHA` equal to the branch tip, and `FastForward == true`;
    - `Commit` of 300 staged files returns `err == nil`.
    - *Mutation:* applying the failing `cappedBuffer` to `merge` makes `Merge` return an error
      even though `refs/heads/main` has moved (asserted).
20. **A read-ceiling hit fails closed and leaves the projection untouched (§3 note).** With
    `WithMaxOutputBytes(4 KiB)`, `main` gains a commit that adds 300 `.md` files. Then
    `IncrementalSync`:
    - returns an error with `errors.Is(err, git.ErrOutputTooLarge)`;
    - leaves `LastSyncedCommit` unchanged;
    - makes the fake store record no upsert or delete.
    If TASK-011 has landed and records an `error` state, assert that state as well.
21. **Limit parity with TASK-004** (§11 note).
    - A `core/artifact` test asserts `worktreefs.DefaultMaxReadBytes == git.DefaultMaxBlobBytes`.
    - Whichever of TASK-003 and TASK-004 merges second adds it.
    - If TASK-004 is not on `main` when this task merges, record "carried by TASK-004" in
      Implementation Notes.
22. **The upgrade note is pinned by behaviour.** Starting from an `idle` state synced at a commit
    that already contains `EPIC-001-résumé/epic.md`, and with that file unchanged:
    - an `IncrementalSync` after an unrelated commit does **not** project it;
    - a `FullRebuild` does.
    This is the reason the CHANGELOG upgrade note and owner question 3 exist. If a later change
    makes `IncrementalSync` backfill, this test is what forces the note to be revisited.

## Out of scope / follow-ups

- **SMP-owned reads and diffs:** `RealGitReader.ReadFile`, `RealGitOperator.ReadFile`/`Diff`,
  `HistoryReader` log and `gitplumbing.parseDiffNameStatus`. SMP TASK-004 (F4.smp-reads) and
  SMP TASK-003 (P3-9.smp) own them.
- **Receive-time inflation.**
  - `index-pack` must reconstruct a pushed delta to hash it. This happens in the gateway's git
    child during the push.
  - `receive.maxInputSize` limits the pack, not the inflated object.
  - The SMP gateway side belongs with SMP TASK-004. The standalone gateway has no task in this
    plan, so this is recorded as a follow-up.
- **`DiscoverChanges` swallows other read errors** (transient git failures), which silently skips
  a changed artifact while the sync advances. This is the same class as P3-14. It is left to
  TASK-011 or a follow-up, because a blanket "propagate" would wedge on deterministic errors
  (for example a gitlink named `*.md`).
- **A batched size probe for bulk discovery.** One `ls-tree -r -l -z <sha>` per rebuild, cached
  by tree SHA, would remove §2's extra spawn on `FullRebuild`.
- **Surfacing refused files in SMP's UI or health checks,** and wiring an event router into SMP's
  projection writer (SMP TASK-008, optional item 5).
- **Wall-clock timeouts on git commands.** Size limits bound time indirectly.
- *(Design-check additions, 2026-10-06)*
  - **Worktree reads** (`Service.Update`, `AcceptTask`/`addLinkToArtifact`, `workflow.Service`
    `readFromDisk`/`Update`, renumber): Spine TASK-004 I7 owns them. F4.spine is closed only
    with both tasks on `main` (Purpose, F4 note; AC 21).
  - **Checkout-time inflation.** `git worktree add`/`checkout` of a branch that holds an oversized
    packed blob builds it in git's memory and writes it to disk. This covers SMP's per-request run
    worktrees (`gitclone/cli_worktree.go:59`, used by `GetContent`/`WriteContent`) and Spine's
    standalone `branchwrite.go:75`. The alloc limit is deliberately not applied there (§2a),
    because it would break legitimate large files.
    - On SMP, TASK-004's 8 MiB per-blob push hook bounds new content.
    - Standalone and pre-existing content are a follow-up, for example `core.bigFileThreshold`
      on checkout, or a sparse checkout of only the paths written.
  - **`diff` rename-detection inflation** of large blobs that are already on the diff base
    (§2a, KNOWN-LIMITATIONS).
  - **SMP TASK-004's hook checks blobs only.** Commit and tree objects pass it (§10 note); this
    is SMP TASK-004's to fix.
- **Artifact *bodies* that are not valid UTF-8.** Postgres rejects them today as well. This is
  pre-existing and unchanged.

## Design check (2026-10-06)

This was an adversarial check against Spine `61994fe` and SMP `80995559`, both read-only. The
six PoCs were re-run with `-overlay` and fail as recorded. Every changed passage above is marked
**Design-check note (2026-10-06)**; the designer's text is otherwise unchanged.

**Verified as correct:**
- the cited `cli.go`, discovery, projection, merge-cascade and error-mapping line references in
  both repos;
- I3: additive fields only, and SMP builds no `DiscoveryResult`/`ChangeSet` literal;
- the `ErrPayloadTooLarge` → 413 mapping in both gateways;
- the SMP TASK-003 precondition: it deletes `unquoteDiffPaths`;
- `event_log.event_id` is `text`, with a deduplicating primary key;
- no tracked path in either repo is non-ASCII or contains a control byte, and SMP's largest
  `.md` is 158 KiB;
- `cli.go:433`/`:524` are Spine's only line-based path parsers.

**Changed, and why:**
1. **F4 was not closed for the container (Purpose, §2, new §2a, Decisions 1 and 12, AC 18).**
   The probe bounds only the target object.
   - A small blob deltified on a large base made the git child reach 71 MiB for a 3 MB read.
     Stock git allows a base up to 32× the target; a crafted pack allows any size.
   - A gitlink `*.md` made it reach 253 MB.
   - Neither case needs a race, and SHA-pinning does not help.
   - Amendment: `GIT_ALLOC_LIMIT` on read ops (not `diff`, not mutating ops), mapped to the typed
     too-large errors before `classifyGitError`. Both bypasses then fail at about 5 MB RSS, with
     no false refusals on either real repo.
2. **A fail-closed stdout ceiling broke mutating ops (§3, §9, Decisions 9 and 13, AC 19).**
   `git merge` updates the ref before it prints its diffstat. Verified: SIGPIPE, exit 141, and
   `main` had moved. Mutating ops now drain and truncate instead.
3. **Stderr head-only truncation could misclassify** a final `fatal: … .lock` line. It now keeps
   head and tail (AC 6 note).
4. **The `WriteContent` bypass is a worktree `os.ReadFile`** (`service.go:214`), which this task
   cannot bound. The task now states that F4.spine is closed only together with Spine TASK-004
   I7, adds the limit-parity AC 21, and replaces the stale §11 "no overlap" claim.
5. **P3-9 hits ordinary users.** `Slugify` keeps Unicode letters, and SMP builds paths with it.
   Stored-data consequences were added, with a default backfill (owner question 3, §8, §10,
   AC 22):
   - `IncrementalSync` never backfills;
   - duplicate IDs surface;
   - invisible non-ASCII workflows become active.
6. **`UnsafePath` default widened** to C1 controls, U+2028/U+2029 and bidi controls (owner
   question 2 note, AC 9 note). No `Slugify` output is affected.
7. **AC 4's `< 2 MiB` would fail on a correct implementation** (buffer growth from a 1-byte hint);
   it is now `< 4 MiB`. AC 20 pins the deterministic fail-closed behaviour of a read-ceiling hit
   downstream (sync stuck at `syncing`), which the design had not stated.
8. **Smaller corrections:**
   - probe/show stderr parity is Kind parity, not byte parity (gitlink to a missing commit);
   - git streams packed blobs only above `core.bigFileThreshold`;
   - `IsIDCollision`, `RenumberArtifact`, `UpdateLinksToRenamedArtifact` and
     `HasCommitWithTrailer` are dormant;
   - SMP TASK-004's push hook checks blobs only;
   - checkout-time inflation is recorded as a follow-up.

## Implementation Notes

*(Implementation, 2026-10-13/14. Branch `init/INIT-023-TASK-003-bounded-reads-and-nul-delimited-listings`,
cut from Spine local `main` `d23b897` (`61994fe` + the INIT-023 scaffold + waves 1 and 2, including
TASK-002, TASK-004 and TASK-011). Run on macOS, git 2.54.0, go 1.26.5 (toolchain 1.26.8 in SMP), and
on git 2.39.5 in the local `golang:1.26-bookworm` image for CI's `git-floor` job and the Linux-only
measurements.)*

### Orchestrator rulings and owner-question defaults (recorded, pending the owner)

- **Scope:** audit F4 (Spine half) and P3-9 (Spine half). SMP TASK-003 (raw-path `diffNameStatusZ`,
  `unquoteDiffPaths` deleted) is on SMP `main` `f5c6fd6d`, so `Diff -z` is safe for SMP; the
  compatibility gate below shows no double decode.
- **Handoffs honoured:** `git.DefaultMaxBlobBytes` is defined as `worktreefs.DefaultMaxReadBytes`
  (TASK-004's 4 MiB constant), and `git.ErrBlobTooLarge` is registered in the projection's
  `isContentReadError` (TASK-011's hook), so an oversized workflow is a rejection, not a stuck sync.
- **OQ1 — default taken:** 4 MiB per blob, 16 MiB per read command's stdout.
- **OQ2 — default taken, widened per the design check:** refuse and record a path that is not
  valid UTF-8, or holds `unicode.IsControl` (C0, DEL, C1), U+2028/U+2029, or a bidi control
  U+202A–U+202E / U+2066–U+2069. No `Slugify` output is refused (tested).
- **OQ3 — default taken:** a CHANGELOG upgrade note ("run one projection rebuild"); SMP TASK-008 runs
  `RebuildProjection` per workspace. Pinned by AC 22's test.
- **OQ4 — default taken:** `GIT_ALLOC_LIMIT` on read commands = 4 x the per-blob limit (16 MiB),
  with a 1 MiB floor (Deviation 3).

### What was built

| Commit | Change |
|---|---|
| `17457dd` | `adapters/git/limits.go` (new): `DefaultMaxBlobBytes`, `DefaultMaxOutputBytes`, `WithMaxBlobBytes`, `WithMaxOutputBytes`, `WithMaxGitAllocBytes`, `ErrBlobTooLarge`, `ErrOutputTooLarge`, `BlobTooLargeError`, `OutputTooLargeError`, the failing/draining `stdoutSink` (only `io.Writer`, growth by doubling, never past the limit), the head+tail `stderrSink` (64 KiB), the alloc-refusal matcher. `cli.go`: `runBounded` with an explicit per-op output class (read: failing ceiling + `GIT_ALLOC_LIMIT`; `diff`: failing ceiling only; mutating: drain), `ReadFile` = `cat-file -s` probe + one pre-sized bounded `git show`, `ListFiles` `-z`, `Diff` `-z` + strict `parseNameStatusZ`. PoC regression tests (`cli_bounds_regression_test.go`, `cli_gitmem_regression_test.go`), `cli_bounds_test.go`, argv pins in `refgrammar_test.go` updated (probe before `show`; `-z`) |
| `df689a4` | `GIT_ALLOC_LIMIT` floor of 1 MiB (Deviation 3) |
| `b1279bf` | `core/artifact/refused.go` (new): `RefusalReason`, `RefusedFile`, `UnsafePath`, `ReadLimitPhrase`; additive `Refused` on `DiscoveryResult` and `ChangeSet`; `DiscoverAll` / `DiscoverChangesFromDiffs` skip-and-record per the §5 table; `Service.Read` and `workflow.Service.Read` map `ErrBlobTooLarge` to `ErrPayloadTooLarge`; `IsIDCollision`; `Service.List` / `workflow.Service.List` leave unsafe paths out (Deviation 6); `core/domain/repo_path.go` `UnsafeRepositoryPath` (Deviation 5). NextID PoC regression test; AC 21 parity test |
| `863f308` | `domain.EventProjectionFileSkipped`, `allEventTypes()` registration, `observe.Metrics.ProjectionFilesSkipped` → `spine_projection_files_skipped_total` |
| `83d35fc` | `adapters/projection/refused.go` (new): `recordRefused` (Warn, metric, deterministic `projskip-…` event); both entry points record refusals and never count them; unsafe workflow paths refused before any read or store call; `isContentReadError` = `errors.Is(err, git.ErrBlobTooLarge)`; `projection_synced` gains `refused`. PoC regression tests (`oversized_regression_test.go`) and `refused_test.go` |
| `00b21c6` | Gateway 413 test (`handlers_artifacts_readlimit_test.go`) |
| `ff98809` | Docs: `event-schemas.md` §4.15 + emission map + `projection_synced.refused` + `read_bounds` note; `git-integration.md` §8.1/§8.2; `KNOWN-LIMITATIONS.md` "Git Read Bounds"; `CHANGELOG.md` |
| `7f76d9e`, `ba2e1d2` | Lint: literal bidi / separator / ZWJ characters in source replaced by Go escapes (gosec G116, ST1018), `rangeValCopy`, nolint reasons, PoC fixture's unused mutex, a builtin shadow |
| `9e5e38c` | Codex round 1 P2: read commands run in the C locale (Deviation 4) |
| `b31da04` | These notes (first version) |
| `7aa8689` | Codex round 3 P2: the per-command output limit is never below 4 KiB (Deviation 12) |
| `f7e6ae6` | Codex round 4: `Clone`'s stderr bounded, the stderr tail a fixed ring (Deviation 13); `engine.ValidateBranch` fails each refused file (Deviation 14) |
| `a7d0478` | gofmt of a test |

No migration, column, table, configuration key, environment variable or endpoint was added
(`git diff --stat main...HEAD -- migrations go.mod go.sum` and `git diff --stat 61994fe -- migrations`
are empty). No interface gained a method; no `GitClient` implementation, `stubGitOperator` or the
scenario `codeRepoBehavior` was edited.

### Per-AC evidence

1. **PoCs.** Committed with assertions unchanged: `adapters/git/cli_bounds_regression_test.go`
   (`TestT003_ReadFileOversizedBlobIsRefusedWithoutBuffering`, `…ListFilesReturnsRawPaths`,
   `…DiffReturnsRawPaths`), `adapters/projection/oversized_regression_test.go`
   (`TestT003_IncrementalSyncDoesNotBufferOversizedFile`, `…FullRebuildProjectsNonASCIIArtifactPath`;
   its fixture store gained `ListActiveWorkflowProjections`, which TASK-011's sweep calls, and lost
   an unused mutex), `core/artifact/id_allocator_regression_test.go` (`TestT003_NextIDSeesNonASCIISlug`).
   **On `61994fe`** (a `git archive` export with only these three files copied in): all six fail as
   recorded — `ReadFile` returns 8 388 687 bytes with 49 191 KiB `TotalAlloc`; quoted listings;
   `IncrementalSync` 57 450 KiB; the résumé epic not projected; `NextID = EPIC-001`. **On Spine
   `main` `d23b897`:** the same six fail the same way (49 243 KiB, 57 511 KiB, …). **On the branch:**
   all pass (`ReadFile` 135 KiB, `IncrementalSync` 320 KiB).
2. `TestReadFile_OversizedBlobIsRefusedBeforeReading`: `DefaultMaxBlobBytes+1` and 8 MiB blobs on
   `refs/heads/spine/run/x` — `errors.Is(ErrBlobTooLarge)`, `errors.As` gives the probed size, the
   default limit and the passed `Ref`/`Path`, direct `*GitError` with `ErrKindPermanent`, not
   retryable, not `ErrOutputTooLarge`, `TotalAlloc` < 1 MiB. Mutation (probe comparison removed):
   4 310 KiB, fails.
3. `TestReadFile_AtLimitIsOnePreSizedBuffer`: a 1 MiB blob under `WithMaxBlobBytes(1 MiB)` is
   byte-identical with `TotalAlloc` <= 1.25 MiB; a 0-byte blob is a non-nil empty slice.
4. `TestReadFile_ReadCapIsTheSecondLineOfDefence`: probe seam reports 1 byte, 8 MiB blob, 1 MiB limit
   → `BlobTooLargeError{Size: -1}`, `TotalAlloc` < 4 MiB (the design-check bound), returns well before
   its 30 s context deadline. Mutation (no stdout cap): the read succeeds, fails the test.
5. `TestReadFile_ErrorKindParity`: Kind **and** Message equal those of today's unbounded
   `git show [eoo] <spec> --` for a missing path, a missing nested path, a path through a blob, an
   unknown ref, an unknown object id, a path present only on disk, and a gitlink to an absent commit;
   a tree path is byte-identical. `TestReadFile`, `TestReadFileNotFound` and TASK-002's
   `TestRefShadow_ReadFileMissIsNotFound` pass unchanged. See Deviation 2 for the unknown-ref and
   on-disk rows.
6. `TestReadCommands_OutputCeiling`: `WithMaxOutputBytes(4 KiB)` → `ListFiles` over 300 files and
   `Log` over an 8 KiB message return `ErrOutputTooLarge` (typed `Op`/`Limit`); with the defaults
   both succeed. `TestStderr_KeepsHeadAndTail`: 1 MiB of `warning:` lines plus a final
   `index.lock` fatal, in four chunk sizes — kept text <= 64 KiB, keeps the last line, classifies
   transient. `TestStderr_RingMatchesReference` checks the ring against a reference at every
   head/tail boundary length and seven chunk sizes. Mutation (no ceiling): both cases succeed,
   fail the test.
7. `TestReadBounds_Defaults`: `DefaultMaxBlobBytes` >= 512 KiB (API write cap, cited) and >=
   `yamlsafe.MaxBytes`; `DefaultMaxOutputBytes` >= `DefaultMaxBlobBytes`; `0`/`-1` keep the
   defaults; alloc = 4 x blob by default and after `WithMaxBlobBytes(1 MiB)` in either option order;
   below blob+1 raised; the 1 MiB alloc floor; the 4 KiB output floor (`WithMaxOutputBytes(1)`); a
   zero-value `CLIClient` is bounded too (export_test accessor). `TestReadBounds_SmallBlobLimitKeepsReadsWorking` exercises the floor.
8. The two PoCs (raw `" lead.md"`, `"quoted.md`, `réview.md`, `tab\tfile.md`, `new\nline.md`,
   `plain.md`) plus `TestDiff_RawRenameCopyAndTypeChange` (rename to `re\nnamed.md`; with
   `diff.renames=copies` a `C100` record is `added` at the destination and the 4 records stay 4
   distinct paths; a file-to-symlink type change is `modified`) and `TestParseNameStatusZ` (the AC's
   `"R100\x00only-one\x00"` and `"A\x00"` plus eight more malformed streams: error, no records, no
   path echoed). `TestDiff`, `TestDiffEmptyResult`, `TestDiffModifiedAndDeleted`, `TestListFiles`,
   `TestListFilesAllAndGlobPatterns` pass unchanged. Mutation (`ls-tree` without `-z`): the PoC and
   `TestListFiles*` fail.
9. `core/artifact/discovery_refused_test.go`. `TestDiscoverAll_RefusesAndRecords` (a real repo whose
   tree is written through `update-index -z`, so invalid UTF-8 and newlines are real paths, and a
   counting `GitClient`): the oversized `.md` is `Refused{oversized, Size, Limit}`; `a\tb.md`,
   `x\ny.md`, `\xff.md`, the widened U+0085, U+2028, U+202E rows, plus U+2029, U+2066, DEL and a
   control byte in a directory name, and `workflows/a\tb.yaml`, are `Refused{unsafe_path}` with
   **zero** reads; nothing refused is in `Errors`, `Artifacts` or `Skipped`; the résumé epic, a
   `wdrożenie` epic and `wdrożenie/x.md` (Slugify outputs) are artifacts under raw paths.
   `TestUnsafePath` (29 rows; normalisation, emoji, ZWJ, Cyrillic, CJK not refused; Slugify of
   hostile titles never refused). `TestDiscoverChangesFromDiffs_RefusalTable`: 17 rows covering
   every §5 row, including modified-to-oversized → `Deleted`+`Refused` and deleted-with-oversized-old
   → `Deleted`; no unsafe path ever reaches `Deleted`. Mutation (no `Deleted` on modified-to-oversized):
   its two rows fail.
10. `TestRead_OversizedIsPayloadTooLarge`: `artifact.Service.Read` and `workflow.Service.Read` →
    `ErrPayloadTooLarge` with the refusal as cause and "read limit" in the message; small and missing
    reads keep 200/404; `IsIDCollision` true for an oversized existing file, false for a missing one.
    `service/internal/gateway` `TestGateway_ReadOversizedArtifact_413`: **413** `payload_too_large`
    from `GET /api/v1/artifacts/<path>` on `main` and with `?ref=<run branch>`.
11. `adapters/projection/refused_test.go`, scaled to `WithMaxBlobBytes(1 MiB)` and 8 MiB blobs:
    (1) `TestIncrementalSync_OversizedFileIsSkippedRecordedAndAdvances`: `idle` at the new head,
    `TotalAlloc` < 2 MiB, exactly one `projection_file_skipped` with exactly the §7 payload fields and
    the deterministic id, the counter +1, `projection_synced.refused == 1`;
    (2) `TestFullRebuild_OversizedArtifactIsSkippedAndRecorded`: not projected, `idle`, one event,
    `TotalAlloc` < 2 MiB, and a second rebuild re-emits the same id;
    (3) `TestIncrementalSync_ArtifactModifiedPastTheLimitLosesItsRow`: row deleted, `idle@head`
    (mutation: fails);
    (4) **amended (Deviation 1)** `TestProjection_OversizedGovernanceFiles`: an oversized
    `workflows/task-default.yaml` keeps its verified row quarantined at `c1`, a new oversized
    `workflows/new.yaml` is not projected, both `read_bounds`, `idle@c2`, `TotalAlloc` < 2 MiB, no
    skip event; an oversized `.spine/branch-protection.yaml` holds `error` at the previous commit
    over two ticks with the rules unchanged (I4). Mutation (`isContentReadError` false): the workflow
    subtest fails;
    (5)/(6) `TestProjection_UnsafePathsNeverReachTheStore` (both entry points, a store that fails the
    test on any invalid-UTF-8 path): `initiatives/\xff/x.md`, a U+202E artifact and
    `workflows/a\tb.yaml` are recorded (Go-quoted paths, null sizes) and not projected; deleting them
    later is a no-op. Mutation (no unsafe check in the incremental workflow loop): fails.
12. `domain.EventProjectionFileSkipped == "projection_file_skipped"`;
    `TestAllEventTypes_IncludesProjectionFileSkipped` (in `allEventTypes` once, `isDomainEvent`
    false); `event-schemas.md` §4.15 and the emission map (Deviation 9 for the number);
    `TestExportPrometheus_ProjectionFilesSkipped`.
13. `git-integration.md` §8.1/§8.2, `KNOWN-LIMITATIONS.md` "Git Read Bounds" (limits and options, the
    alloc limit and its floor, the C locale, the fail-closed ceiling, the `diff` residual, the
    named-ref window, deltas on a large base, what is not covered, cost, refused paths, wrapping
    embedders) and `CHANGELOG.md` (security, behaviour incl. the new event type, upgrade note,
    embedders).
14. `go build ./cmd/embed-smoketest ./service/...` ok with no `GitClient` implementation edited;
    `git diff --stat 61994fe -- migrations/` empty; `./dev/lint-boundaries.sh` OK, no allowlist
    entry.
15. Gates below. `TestRepoArtifacts_ParseCleanlyUnderSpine` wall time: **13.5–13.7 s with Spine
    `main`** (SMP test run with every file this branch changed overlaid back to its `main` version),
    **28.0–28.4 s on this branch** (25.9 s at `a7d0478`; about 2x, the extra `cat-file -s` per
    read; production SMP already pays it in its own `gitread.Bounded`, which overrides `ReadFile`).
16. SMP gate below. `len(result.Refused) == 0` on SMP's repository (1 112 artifacts, 10 workflows,
    0 errors) and on Spine's (715 artifacts, 0 refused; its one discovery error is the pre-existing
    `INIT-014 initiative.md` "missing required field: created", an ASCII path, identical on `main`),
    via an uncommitted overlay test calling `DiscoverAll` with a default client.
17. Merge ordering is the orchestrator's: SMP TASK-003 is on SMP `main` `f5c6fd6d` (verified:
    `grep -rn "unquoteDiffPaths\|unquoteGitPath" internal/` is empty there).
18. `adapters/git/cli_gitmem_regression_test.go` (re-exec child, `RUSAGE_CHILDREN`; linux/darwin
    helpers, error assertions only elsewhere): (1) 3 000 000-byte tip deltified by stock
    `repack -adf` on a 64 MiB base (the fixture asserts it is a delta and that `cat-file -s` sees only
    the tip): `ErrBlobTooLarge`, `Size -1`, "read limit" and no "corrupt", git child peak RSS
    4 MiB (macOS) / 9 MiB (git 2.39.5, Linux); (2) gitlink `g.md` to an in-repo commit adding 64 MiB:
    same, 5 / 9 MiB; (3) a packed undeltified 4 MiB blob under `WithMaxBlobBytes(4 MiB)` and a 3 MiB
    tip on a 12 MiB base are byte-identical; (4) a 20 MiB rename with one changed line under
    `WithMaxGitAllocBytes(16 MiB)` is `renamed`. Mutations: no `GIT_ALLOC_LIMIT` → (1) succeeds at
    71 MiB RSS and (2) reaches 131 MiB (fail); the limit on `diff` → (4) fails.
19. `TestMutatingCommands_DrainOutput`: under `WithMaxOutputBytes(4 KiB)`, `Commit` of 300 staged
    files and a fast-forward `Merge` of them succeed; `SHA` is the tip, `FastForward` true, and
    `refs/heads/main` is asserted to have moved. Mutation (failing sink for mutating ops): fails.
    `TestMerge_TinyOutputLimitKeepsTheResult` (codex round 3): a fast-forward under
    `WithMaxOutputBytes(1)` is reported as one (mutation without the output floor: fails).
20. `TestIncrementalSync_ReadCeilingHitFailsClosed`: `WithMaxOutputBytes(4 KiB)` and a commit adding
    300 `.md` files → `errors.Is(err, git.ErrOutputTooLarge)`, state `error` at the old commit (TASK-011
    records it), zero store writes.
21. `core/artifact` `TestReadLimitParityWithWorktreefs` (this task merges second, so it adds it).
22. `TestUpgrade_NonASCIIArtifactNeedsARebuild`: a store `idle` at a commit that already holds the
    résumé epic does not project it on an unrelated `IncrementalSync`; `FullRebuild` does.

### Deviations (closest safe alternative, with reasons)

1. **I4 / AC 11.4 amended by the orchestrator ruling.** TASK-011's D2 makes every workflow content
   failure a rejection that advances the sync, and its design check required this task to register
   its sentinel in `isContentReadError`. An oversized workflow is therefore a `read_bounds` rejection
   (row kept quarantined, or not projected; `idle`), not a sync error; the oversized
   branch-protection config stays a held sync error (`projectBranchProtection` unchanged). It is
   reported through `projection_workflow_rejected`, not also through `projection_file_skipped`.
2. **A failed size probe falls back to the `git show`.** The design said the probe's failure
   messages are byte-identical to `git show`'s. That held at `61994fe`, but TASK-002 added the
   trailing `--` to `show`, and since then every miss reads `fatal: bad revision '<rev>:<path>'`
   (`ErrKindNotFound`), while `cat-file -s` says `invalid object name` / `exists on disk, but not in`
   (`ErrKindPermanent`). Returning the probe's error would have turned an unknown ref and an on-disk-only
   path from 404 into 500/502 and broken `TestRefShadow_ReadFileMissIsNotFound`. So any non-size probe
   failure runs the (bounded) `git show` and returns its result: every error is exactly today's, at
   one extra process on a miss. AC 5's "unknown ref → Permanent" and "present on disk → Permanent"
   rows are superseded by TASK-002's reclassification; the test pins parity with the unbounded read
   instead. `cat-file` takes no trailing `--` ("too many arguments") and needs none: its argument is
   always one object name.
3. **`GIT_ALLOC_LIMIT` never below 1 MiB.** git makes a fixed 524 256-byte allocation in `ls-tree`,
   `log`, `merge-base` and `diff` regardless of content (measured on 2.39.5 and 2.54, even in an
   empty repository), so `4 x WithMaxBlobBytes(64 KiB)` broke every listing. Found by the discovery
   tests; the defaults are unaffected.
4. **Read commands run in the C locale** (`LC_ALL=C`, `LANGUAGE=`), codex round 1 P2: git translates
   the `fatal: `/`error: ` prefix of its alloc refusal. `lookupBranch` already does this. On a host
   with a translated git, read errors are now English (documented in CHANGELOG); read stdout does not
   depend on the locale. Test `TestReadCommands_RunInTheCLocale` (a wrapper that answers like a
   translated git unless `LC_ALL=C`; mutation fails it).
5. **The path predicate lives in `core/domain`** (`UnsafeRepositoryPath`); `artifact.UnsafePath` is
   the design's API and wraps it. `core/workflow` needs it for `List` and does not import
   `core/artifact`; adding that edge for one predicate was not worth the coupling.
6. **`artifact.Service.List` and `workflow.Service.List` skip unsafe paths** (the design said `List`
   is unchanged). With raw listings those paths would newly appear in API listings; skipping them
   keeps both outputs exactly as before for them while non-ASCII paths now appear.
7. **`tree_entry` (TASK-011's `TreeEntryType`, written after the design) is a read op** and gets the
   failing ceiling, `GIT_ALLOC_LIMIT` and the C locale. Every op name has an explicit class
   (`TestEveryOpHasAnOutputClass` scans the package source); an unlisted op gets the failing
   ceiling without the alloc limit, so output is never silently truncated.
8. **Event and record shape.** `RefusedFile.Path` is the repository-relative path on both entry points
   (`ChangeSet` only knows repository paths; a refused file has no projection key). In logs and the
   event, an unsafe path is `strconv.Quote`d (one line, valid UTF-8, every control and bidi character
   escaped) — the envelope's `artifact_path` too, which would otherwise carry invalid UTF-8 into the
   event log. `commit` is the full SHA. Refusals are reported on every sync attempt that reaches
   them (a sync held for another reason re-reports; ids de-duplicate, the counter counts again), and
   `FullRebuild` reports after its writes rather than straight after `DiscoverAll`. Nested
   `workflows/**` unsafe files are not reported: they are never projected, refused or not.
   *(Review round 2: the reporting point is superseded by Deviation 19.)*
9. **`event-schemas.md` section number:** TASK-011 took §4.13 (`projection_synced`) and §4.14, so the
   new event is §4.15.
10. **Database.** The shared PostgreSQL on 55630 was not running; other sessions run private
    instances, which were not touched. Every database step ran on a private, tmpfs-backed container
    from the already-present `pgvector/pgvector:pg18` image (PostgreSQL 18.4, `--pull=never`,
    127.0.0.1:55931), with `s003_*` databases plus the SMP CI roles (`platform`,
    `platform_audit_retention`, `smp_prov`). That made SMP's `TEST_PROVISIONING_DSN` suites runnable
    (TASK-011 could not). The container and everything in it were removed at the end.
11. **Commit attribution** follows the two trailer lines on every commit (the first commit's message
    was amended once, before any later commit, to add the session line).
12. **`WithMaxOutputBytes` is floored at 4 KiB** (codex round 3, P2). Below that, `git merge`'s drained
    head lost its "Fast-forward" line (a fast-forward was reported as a merge) and the
    `rev-parse HEAD` after every commit and merge could not fit one object id. No production caller
    sets the option. `TestMerge_TinyOutputLimitKeepsTheResult`.
13. **`Clone`'s stderr is bounded** (codex round 4, flagged P1 by codex). The design (§9) said `Clone`
    was unaffected, but I1 says no method buffers more than its limit and `Clone`'s stderr relays the
    remote's sideband into a plain buffer. It now uses the same sink, whose tail is a fixed ring
    allocated once the head is full (arbitrarily long stderr costs O(1) memory). Local single-path
    helpers outside `CLIClient` (`branchwrite.go`, `lookupBranch`, `CheckGitVersion`) keep their
    buffers: their output is one line and not remote-controlled. `TestClone_StderrIsBounded`
    (8 MiB of stderr → < 1 MiB allocated, final line still classified; mutation keeps 8 390 943
    bytes and fails).
14. **`engine.ValidateBranch` fails each refused file** (codex round 4, P2). It inspected only
    `Created` and `Modified`, so a refused file (oversized, or at an unsafe path) would merge
    unvalidated next to a valid artifact and then never be projected. Each refusal is now a failed
    entry ("not validated: …", unsafe paths Go-quoted). The function has no caller in Spine or SMP
    (dormant), so nothing observable changes today. `TestValidateBranch_RefusedFilesFail` (mutation:
    fails); gofmt of the file also fixed a pre-existing import-order drift.

### Gates (final code `a7d0478`; every row below was re-run there)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | ok; `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` | exit 0, 40 packages ok |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all clean |
| `go test -race -p 4 -count=1` on `adapters/delivery`, `adapters/git/...`, `adapters/projection`, `core/artifact`, `core/domain`, `core/engine`, `core/observe`, `core/workflow`, `service/internal/gateway`, `core/evidence` | 11 ok |
| `SPINE_TEST_DATABASE_URL=…/s003_proj go test -race -p 1 -count=1 -tags integration ./adapters/projection/`; same with `s003_deliv` for `./adapters/delivery/` | projection 101 top-level / 74 sub pass, 0 skip; delivery 139 / 176 pass, 0 skip |
| `go test -p 1 -count=1 -tags scenario ./service/scenariotest/...` on `s003_scen`, and in a `git archive main` export on `s003_scen_main` | both 191 pass / 30 fail / 6 skip (subtests 1288 / 27), **0 outcome differences**; the 30 are the known pre-existing failures. CI's `scenario-git-http` set (`TestGitHTTP_*`, `TestBootstrapInternalAdmin_*`): 22 pass |
| CI `git-floor` job in `golang:1.26-bookworm` (git 2.39.5, `--pull=never`, `--rm`, read-only mounts, `GOPROXY=off`) | step 1 (`-run 'RefShadow|RefGrammar|Checkout|EnterBranch|CurrentBranch'` on `adapters/git/...`, `core/engine`) ok; step 2 (`adapters/git/...`, `core/artifact`, `core/workflow`) ok, and `core/engine` too |
| `golangci-lint run` (v2 config) on the 9 touched packages, plain / `--build-tags integration` / `--build-tags scenario`, branch vs a `main` export, compared as (linter, file, message) multisets | **0 new issues** in each (188 vs `main`'s 189: gofmt of `branch_validation.go` removed one) |
| `golangci-lint run --enable-only=gosec` (plain and integration), same packages | 0 issues |
| `gofmt -l` on every Go file in `git diff main...HEAD` | empty |
| `codex review --base main` (`-c model=gpt-5.6-sol`: the configured default model is refused for this account) | Round 1 (at `7f76d9e`): one P2 — locale (fixed `9e5e38c`, Deviation 4). Round 2 (`9e5e38c`): no findings. Round 3 (`b31da04`): one P2 — tiny output limit loses the merge result (fixed `7aa8689`, Deviation 12). Round 4 (`7aa8689`): a P1 — `Clone` stderr unbounded — and a P2 — refused files bypass `ValidateBranch` (both fixed `f7e6ae6`, Deviations 13, 14). Round 5 (`f7e6ae6`): no findings. Every finding was real; none rejected |

Mutation runs (`go test -overlay`, worktree untouched; each made the named tests fail): no
`GIT_ALLOC_LIMIT` (AC 18 (1)(2)); `GIT_ALLOC_LIMIT` on `diff` (AC 18 (4)); failing sink for mutating
ops (AC 19); no probe comparison (AC 2 and the PoC); no stdout cap (AC 4, AC 6); `ls-tree` without
`-z` (PoC, `TestListFiles*`); no C locale (`TestReadCommands_RunInTheCLocale`); no output floor
(`TestMerge_TinyOutputLimitKeepsTheResult`); a plain buffer for `Clone`'s stderr
(`TestClone_StderrIsBounded`); refusals not counted by `ValidateBranch`
(`TestValidateBranch_RefusedFilesFail`); `isContentReadError`
false (AC 11.4); no `Deleted` on modified-to-oversized (AC 9 table, AC 11.3); no unsafe check in the
incremental workflow loop (AC 11.6).

### SMP compatibility gate

`/Users/bartoszszymichowski/42sid/i23/s-T003/smp` detached at SMP `main` **`f5c6fd6d`** (EPIC-004 wave 1
incl. SMP TASK-003, and TASK-004), `../spine` = this worktree, no `vendor/`, no `tmp/`; `git status`
clean after every run. Final run at `a7d0478` (fresh, re-migrated databases); the earlier runs at
`ff98809` and `ba2e1d2` gave the same results.

| Command (in the SMP checkout) | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...` | all ok |
| `go test -race -p 4 -count=1` on the 36 packages that depend on a changed Spine package (`go list` deps, test imports included; `core/engine` included), no DB env | 2 350 pass / 3 fail / 246 skip — and **with every Spine file this branch changed overlaid back to its `main` version, exactly the same: 0 outcome differences** |
| The 3 failures (pre-existing, identical with Spine `main`): `gitread.TestBounded_ErrorContractEqualsCLIClient` (SMP's probe messages vs TASK-002's `bad revision`), `projectionwriter.TestBoundedProjection_IncrementalAddAndModifyOversized` (TASK-011 holds the sync on SMP's own too-large error, which does not match `ErrBlobTooLarge`), `projectionwriter.TestBoundedProjection_OversizedWorkflowStalls` (its fake store lacks `GetWorkflowProjection`, which TASK-011's rejection path calls; the panic aborts the package binary) | SMP TASK-004's tests pin SMP's current pin, not Spine `main`; the next SMP bump past wave 2 must adapt them (SMP TASK-008 / TASK-012). Not caused by this task |
| `go run ./dev/apply-migrations -set platform -dir migrations` → `s003_smp_it` (pgvector); `-set spine-runtime` → `s003_spine_rt` | 115 and 26 versions |
| CI's Spine-runtime step: `TEST_SPINE_DB=…s003_spine_rt TEST_PLATFORM_DB=…s003_smp_it TEST_PROVISIONING_DSN=…smp_prov go run ./dev/testgate -- -race -count=1 -timeout 15m ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` | 1 835 passed, 0 failed; the only 2 skips are Linux-only by design (`ru_maxrss` in KiB): `TestDiffChildMemory_MillionPaths` and `TestReceiveTimeMemory_RealGitReaderFetch`, both **PASS** on Linux in `golang:1.26-bookworm` |
| The shared platform-DB step's members that depend on a changed Spine package, plus `docindex`: `go run ./dev/testgate -allow-skip 'TEST_MINIO' -allow-skip 'TEST_AWS_OBJECT_STORE_' -- -race -p 4 -count=1 -skip '^(TestBoundedProjection_OversizedWorkflowStalls|TestBoundedProjection_IncrementalAddAndModifyOversized)$' ./dev/apply-migrations/ ./internal/automation/ ./internal/customer/discovery/ ./internal/customer/eventdelivery/ ./internal/customer/profileinventory/ ./internal/customer/projectionwriter/ ./internal/customer/stepevents/ ./internal/customer/workspace/ ./internal/customer/workspace/workspacetest/ ./internal/import/ ./internal/customer/docindex/` (with `TEST_PROVISIONING_DSN`) | `testgate: PASS` — 1 182 passed, 0 failed, 6 allowed skips (MinIO, live AWS) |
| `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `testgate: PASS` — 246 passed, 0 failed; the 1 allowed skip (`TestIntegration_ReceiveTimeMemoryBounded`, Linux-only) **PASS**es on Linux in the image |
| §10 named tests: `TestRealGitOperator_Diff_RawPaths_Integration` (raw `".spine/x.md"`, not decoded), `…_CascadeWrite_RawPaths_Integration`, `…_Diff_RefNamedFileInClone_Integration` (includes `RunBranchDiffReader.Changes`), `TestParseDiffNameStatusZ`, `TestBoundedReads_RunBranchDiffReader`, `TestRepoArtifacts_ParseCleanlyUnderSpine`, `TestWorker_Integration_ProjectsSeededRepo`, `…_SurfacesMalformedArtifact` (with `TEST_PROVISIONING_DSN`), `artifactservice_test.go` (in the runtime step) | all pass |
| `grep -rn "unquoteDiffPaths\|unquoteGitPath" internal/` | empty |

No SMP edit was needed to compile or pass. For SMP TASK-008 (in addition to §10's list): SMP's
`gitread.Bounded.ReadFile` overrides `CLIClient.ReadFile`, so in production SMP the new Spine probe
and its typed error are not used until TASK-008 gives `gitread`'s too-large error the
`git.ErrBlobTooLarge` identity (§10 item 2); and the three SMP TASK-004 tests above must be
adapted at the first bump past Spine wave 2.

### Not run, with the exact command

- The full shared platform-DB step (all 41 packages) and the MinIO-backed tests:
  `go run ./dev/testgate -allow-skip 'TEST_AWS_OBJECT_STORE_' -- -race -count=1 -timeout 20m <the 41 packages of SMP ci.yml "Platform-DB integration suites">`
  with `TEST_MINIO_*` — no MinIO was started; the packages run were the ones that depend on a
  changed Spine package (above).
- SMP's `faultinject` and RDS-emulation jobs — they exercise SMP-side provisioning, not a changed
  Spine package path.

### Follow-ups (recorded, not done here)

- A batched size probe (`ls-tree -r -l -z <sha>` per rebuild) to remove the extra process per read.
- Receive-time and checkout-time inflation; `diff` rename-detection inflation (KNOWN-LIMITATIONS).
- SMP TASK-008: §10 items 1–7, plus the two SMP points above.

### Review round 1 (2026-10-14)

Three reviewers (correctness and compatibility, adversarial security, completeness and tests)
examined the branch at `8ac951e`. Every finding was verified against the code first. All six were
real, and all six are fixed with a test that fails on the old code (overlay mutations, worktree
untouched). Commits: `fceaf76` (git adapter), `fa83c8e` (downstream), `a86ddc3` (docs) and this
note.

**Findings**

| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | P2 | `GIT_ALLOC_LIMIT` checks one allocation, not git's total. A 3 MB tip on a delta chain of sub-limit bases was read with git's delta-base cache (96 MiB default) full. The docs overstated the bound. | **FIXED.** Reproduced: plain `git show` at 134 MB; through `CLIClient` at 127 MiB (macOS, git 2.54) and 124 MiB (Linux, git 2.39.5). Read and diff commands now pin `core.deltaBaseCacheLimit` to the per-blob limit (`deltaBaseCacheEnv`), giving 52 MiB and 49 MiB. New AC 18 row `TestGitMem_DeltaChainOfSubLimitBasesStaysBounded`: a fast-import chain with `fastimport.unpackLimit=1` and 8 x 15 MiB bases; the fixture asserts tip depth >= 8 and that `cat-file -s` sees only the tip. The read must be byte-identical with git-child RSS < 3 x alloc + 16 MiB (64 MiB); without the pin it fails at 127 MiB. The remaining bound, about 3 x the alloc limit per concurrent read (one chain step: base, result, cache), is now what `cli.go`, `limits.go`, CHANGELOG, KNOWN-LIMITATIONS and `git-integration.md` say. Lowering it means lowering the alloc limit (owner question 4). |
| 2 | P3 | CHANGELOG and KNOWN-LIMITATIONS said `diff` runs in the C locale; it did not. | **FIXED in code, not the docs.** `diff`'s stdout is locale-independent (`-z`, fixed status letters), and its errors are classified by English text. A translated `bad revision` was `ErrKindPermanent` instead of not-found. `TestReadCommands_RunInTheCLocale` now also asserts that a translated `diff` miss is `ErrKindNotFound` (mutation without the locale: fails). |
| 3 | P3 | A git alloc refusal became `BlobTooLargeError{Size: -1, Limit: blob}`, so a 3 MB file read as "exceeds the 4 MiB read limit". | **FIXED.** `BlobTooLargeError.GitAllocLimit` carries the cause: the message names the git allocation limit, and the envelope says "blob read exceeds git allocation limit". The cause propagates to `RefusedFile.GitAllocLimit`, to the Warn line and `projection_file_skipped` (`git_alloc_limit_bytes`, null otherwise; event-schemas §4.15), to the artifact and workflow 413 messages (`ReadLimitPhrase`), and to `ValidateBranch`'s report. Tests: `TestFullRebuild_GitAllocRefusalNamesItsLimit` (a gitlink `*.md`, real git), `TestGitAllocRefusalKeepsItsCause`, `TestRefusalReport_NamesTheLimitThatRefused`, and the gitmem refusals assert the 16 MiB cause. Mutations: dropping the cause in `readOverrun` or in `readForDiscovery` fails them. |
| 4 | P3 | `run()` copied stdout (`string(out)`), so ListFiles, Diff and Log held their output twice. I1 held only for ReadFile. | **FIXED, differently from the suggestion.** Parsing `[]byte` and converting each path would not have lowered the peak for an unfiltered listing: the per-path strings are the same bytes again, in many size-class-rounded allocations. Instead, read commands write into a text-mode sink (`strings.Builder`), whose `String()` hands the buffer over without a copy; the parsed paths and messages are substrings of it. Growth is in powers of two from 512 bytes, capped at the limit, so the cost depends only on output size. `ListFiles` and `parseNameStatusZ` scan instead of `strings.Split` (no intermediate field slice). `TestRun_OutputIsHeldOnce`: a 3.5 MiB Log allocates 8 241 KiB against a bound of 9 984 KiB; with a copy it fails. `TestStdoutSink_GrowthAndHandOver`: kept bytes, overflow, power-of-two caps, and zero allocations on hand-over, in both modes and both sink kinds. |
| 5 | P3 | Only `read_file` was proven to get `GIT_ALLOC_LIMIT` and the C locale. Reclassifying `log`, `list_files`, `merge-base` or `tree_entry` passed every test. | **FIXED.** `TestEveryOpHasAnOutputClass` compares the whole op-to-class table. `TestCommands_RunUnderTheirClassEnvironment` has a git wrapper record every invocation's argv and env across all public methods (ReadFile through Push and DeleteBranch). It pins read (alloc limit, C locale, cache pin, inherited `GIT_CONFIG_PARAMETERS` kept), diff (no alloc limit), mutate (inherited env untouched) and the exact branch lookup, and requires every class to be seen. `TestReadCommands_GitAllocLimitBoundsEveryReadOp` (real git): a 1.5 MB tree and a 2 MiB commit message under a 1 MiB alloc limit make ListFiles, TreeEntryType, Log, HasCommitWithTrailer, MergeBase and ReadFile's probe refuse with `GitAlloc`, while the defaults read them all. `TestGitMem_LogCommitMessageIsRefusedInGit`: a 64 MiB message is refused at 4 MiB (macOS) / 7 MiB (Linux) git RSS. Mutations: `log` to diff fails 4 tests, and `list_files` to diff fails 3. |
| 6 | P3 | `spine_projection_files_skipped_total` was missing from the observability A.1 metrics table. | **FIXED.** Row added under `spine_projection_syncs_total`. |

**Acceptance-criteria gaps raised**

- **AC 16 (three failing SMP tests): not caused by this task, not fixable here.** Re-verified at
  this round: SMP `f5c6fd6d` gives 2 350 pass / 3 fail / 246 skip (top level) across the 36
  dependent packages. With every Spine file this branch changes overlaid back to `main`, the
  result is identical: 0 outcome differences across 5 132 test outcomes, subtests included. The
  three are `gitread.TestBounded_ErrorContractEqualsCLIClient`,
  `projectionwriter.TestBoundedProjection_IncrementalAddAndModifyOversized` and
  `…_OversizedWorkflowStalls`. They pin SMP's current Spine pin against wave 2 (TASK-002's
  `bad revision`, TASK-011's rejection path). The SMP checkout must not be edited, so SMP
  TASK-008 / TASK-012 adapt them at the bump. **Open for the orchestrator:** AC 16 is "passes
  except for these pre-existing three".
- **AC 5 (unknown ref, on-disk-only rows) and AC 11.4 (oversized workflow):** these are
  Deviations 2 and 1, already recorded. **Open:** they need the owner's amendment and sign-off.
- **AC 17:** the PR and its description are the orchestrator's. The precondition holds: SMP
  TASK-003 is on SMP `f5c6fd6d`, and `grep -rn "unquoteDiffPaths\|unquoteGitPath" internal/` is
  empty.
- **AC 18 (delta chain):** closed by finding 1, which adds a fifth row. The other reviewer
  comments on AC 18 confirmed the existing rows; no action was needed.
- Other comments were confirmations (all 22 ACs re-verified, the PoCs re-run on `d23b897`, the
  diff residual correctly scoped). They needed no action.

**Deviations added this round**

15. **The cache pin goes through `GIT_CONFIG_PARAMETERS`, not `git -c`.** It is the same channel
    `-c` uses (highest config precedence; repository, global and system values cannot loosen it).
    The argv stays exactly as TASK-002 pinned it (`TestRefGrammar_ArgvShape`, and wrappers that
    match `"$1"`). The value uses the original `'key=value'` form, which the 2.30 floor reads
    (`GIT_CONFIG_COUNT` needs 2.31). An inherited value of either form is kept, with ours appended
    last so it wins (`TestDeltaBaseCacheEnv` checks this against real git). The value is the
    per-blob limit: 0, 4 MiB and 16 MiB give the same peak for the chain (55 MB), and a non-zero
    cache keeps tree reads fast. On the SMP repository (best of 4): `ls-tree -r` 27 ms → 15 ms,
    `log -n 500` 40 ms → 33 ms, a 300-commit `diff` 101 ms → 101 ms. Interleaved over 900
    processes: 14.0 ms vs 12.9 ms per process, so no measurable cost.
16. **`diff` gets the C locale and the cache pin, but still no `GIT_ALLOC_LIMIT`.** Both refuse
    nothing, so §2a's rename-detection exemption is untouched (`TestGitMem_DiffIsExempt` passes).
17. **`projection_file_skipped` carries a sixth payload field, `git_alloc_limit_bytes`,** beyond
    §7's five. It is null unless git refused an allocation. The event is new in this task, so the
    field is always present rather than optional.
18. **Stdout sink growth is in powers of two** (from 512 bytes, capped at the limit), instead of
    doubling from the first write. What a command's output costs is then a function of its size,
    which makes the no-copy bound testable. Pre-sized ReadFile buffers are unchanged
    (AC 3 / AC 4 pass).

**Gates at this round (code `a86ddc3`; every row re-run)**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `lint-boundaries: OK`; ok; `engine constructed and exercised; library surface is sound.` |
| `go build ./...`; `go vet ./...`, `-tags integration`, `-tags scenario` | all clean |
| `go test -p 4 -count=1 ./...` | exit 0, 40 packages ok |
| `go test -race -p 4 -count=1` on the 11 touched packages (`adapters/delivery`, `adapters/git/...`, `adapters/projection`, `core/artifact`, `core/domain`, `core/engine`, `core/observe`, `core/workflow`, `service/internal/gateway`, `core/evidence`) | all ok |
| `SPINE_TEST_DATABASE_URL=…/s003_proj go test -race -p 1 -count=1 -tags integration ./adapters/projection/`; `…/s003_deliv … ./adapters/delivery/` | projection 102 top-level / 74 sub pass (one new test), delivery 139 / 176 pass; 0 fail, 0 skip |
| `go test -p 1 -count=1 -tags scenario ./service/scenariotest/...` on `s003_scen`, and in a `git archive main` export on `s003_scen_main` | both 191 pass / 30 fail / 6 skip (subtests 1 288 / 27); **0 outcome differences**. CI's `scenario-git-http` set: 22 pass |
| CI `git-floor` steps in `golang:1.26-bookworm` (git 2.39.5, `--pull=never`, `--rm`, read-only mounts, `GOPROXY=off`), plus `core/engine` and `adapters/projection` | all ok. gitmem rows on Linux: delta base 9 MiB, gitlink 11 MiB, chain 49 MiB, log 7 MiB |
| `golangci-lint run` on the 9 touched packages, plain / `--build-tags=integration` / `--build-tags=scenario`, branch vs a `main` export as (linter, file, message) multisets | **0 new issues** in each (188 vs 189) |
| `golangci-lint run --enable-only=gosec`, all three tag sets | 0 issues |
| `gofmt -l` on every changed Go file | empty |
| `codex review --base main` (`-c model=gpt-5.6-sol`; the configured default `gpt-6.1-sol` is refused for this account) | "no actionable correctness regression was identified": no findings |

Database: the shared server on 55630 was down again. Every DB step ran on a private,
tmpfs-backed `pgvector/pgvector:pg18` container (PostgreSQL 18.4, `--pull=never`,
127.0.0.1:55933). It held `s003_*` databases and the SMP CI roles (`platform`,
`platform_audit_retention`, `smp_prov`, posture `f`). The container and everything in it were
removed at the end.

**SMP compatibility gate at this round** (`/Users/bartoszszymichowski/42sid/i23/s-T003/smp`
detached at **`f5c6fd6d`**, `../spine` = this worktree; `git status` clean after every run):

| Command (in the SMP checkout) | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...` | all ok |
| `go test -race -p 4 -count=1 -json` on the 36 dependent packages, no DB env, then the same with every changed Spine file overlaid back to `main` | 2 350 / 3 / 246 both ways; **0 outcome differences** (the three above) |
| `go run ./dev/apply-migrations -set platform -dir migrations` (`s003_smp_it`, pgvector); `-set spine-runtime` (`s003_spine_rt`) | 115 and 26 versions |
| Spine-runtime step: `TEST_SPINE_DB=… TEST_PLATFORM_DB=… TEST_PROVISIONING_DSN=…smp_prov go run ./dev/testgate -- -race -count=1 -timeout 15m ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` | 1 835 passed, 0 failed. The 2 skips are the Linux-only `ru_maxrss` tests (`TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch`), the same two as at `a7d0478`. testgate allows no skip in this step, so it reports `FAIL (2)` on macOS for them alone |
| Platform-DB members that depend on a changed Spine package, plus `docindex` (the command in "SMP compatibility gate" above, with the same `-skip` of the two failing SMP TASK-004 tests) | 1 181 passed, 1 failed, 6 allowed skips. The failure was `workspace.TestPostgresAllocator_LockdownRefusesForeignPublicOwner`, in its cleanup (`DROP DATABASE … permission denied to terminate process`) under `-p 4`. It passes 3/3 standalone, and the whole `./internal/customer/workspace/` package then passes under testgate (586 passed, 6 allowed skips). It is a timing-sensitive DB-cleanup race in an SMP allocator test, with no Spine git path involved |
| `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `testgate: PASS`, 246 passed |
| AC 16 note: `DiscoverAll` with a default client (overlay-only test file, no tree touched) | SMP 1 112 artifacts, 10 workflows, 0 errors, **0 refused**; Spine 715 artifacts, 0 refused, plus the existing ASCII `INIT-014 initiative.md` parse error |
| `TestRepoArtifacts_ParseCleanlyUnderSpine` (AC 15 timing) | 32–37 s on a loaded machine. Back to back with only this round's env change reverted: 32.4 / 31.7 s vs 36.0 / 32.6 s, within noise (see Deviation 15's per-process measurement) |

### Review round 2 (2026-10-14)

A second-round reviewer examined the branch at `d92e8cf`, after the round 1 fixes. They raised
**no findings** and four acceptance-criteria gaps. Each gap was checked against the code, real git
and the sibling artifacts. None is a code defect, and none can be closed from the branch: ACs 5
and 11.4 need the owner's amendment, AC 16 needs the orchestrator's record, and AC 17 belongs to
the orchestrator. Each of these ACs now carries a one-paragraph pointer to this section. This
round's `codex review --base main` raised one P2. It was real and is fixed in `02000ff`; the
re-review at `02000ff` raised nothing.

**Codex finding**

| # | Sev | Finding | Disposition |
|---|---|---|---|
| C1 | P2 | `IncrementalSync` reported refusals only after applying the changeset. When the same diff held a refused file and a changed artifact whose read `classifyReadErrors` treats as an infrastructure failure, the `read changed artifact` early return came first, so no Warn line, counter or `projection_file_skipped` was produced. A persistent read failure suppressed them indefinitely. | **FIXED** (`02000ff`). Verified first: a sync held by an error *inside* the apply loop (a failed upsert, a workflow infrastructure error) did report its refusals, but one held by the read-error return did not. `FullRebuild` had the same gap: it reported after its writes, so a failed plan or wipe reported nothing. Both entry points now report as soon as discovery has decided. In `IncrementalSync` that is after `DiscoverChangesFromDiffs` and before `classifyReadErrors`. The changeset's refusals are joined by each unsafe-named workflow definition that the diff adds, modifies or renames to (`unsafeWorkflowRefusals`, exactly the set the workflow loop skips, so nothing is reported twice). In `FullRebuild` it is after `DiscoverAll` and `workflowPathsAt`, before planning and the wipe. A discovery that fails decides nothing, so it reports nothing. Test `TestRefusals_ReportedWhenTheSyncIsHeld` puts an 8 MiB `docs/notes.md` and `workflows/a\tb.yaml` in two settings. (1) Next to a changed task whose git read is killed (TASK-011's `killingGit` wrapper): the sync is held `error@c1` with nothing applied, both refusals are reported once, and the counter rises by 2. After the failure clears, the retry is `idle@c2` and reports both again under the same ids; `projection_synced.refused` is 2. (2) Next to a failing `DeleteAllProjections`: the same, through `FullRebuild`. On the previous code (overlay, worktree untouched) both subtests fail with zero events. Docs: CHANGELOG, event-schemas §4.15 and the observability metric row. |

**Acceptance-criteria gaps raised**

- **AC 5 (unknown ref and on-disk-only rows): confirmed. It cannot be met as written without
  regressing TASK-002, so it needs the owner's amendment.** Real git 2.54 shows the following.
  - `61994fe`'s argv (`git show <ref>:<path>`) prints `invalid object name 'badref'.` and
    `path 'disk-only.md' exists on disk, but not in 'HEAD'`. Neither matches a not-found pattern
    in `61994fe`'s `classifyGitError`, so both were `ErrKindPermanent`, as the AC says.
  - TASK-002's `git show --end-of-options <spec> --` prints `fatal: bad revision '<spec>'` for
    both, which is `ErrKindNotFound`.
  - `cat-file -s` still prints the old two texts, so the size probe's own error would bring
    `ErrKindPermanent` back. That is why a non-size probe failure falls back to the `git show`
    (Deviation 2). TASK-002's AC 10 requires `ReadFile` on an unknown ref to be `ErrKindNotFound`,
    and its `TestRefShadow_ReadFileMissIsNotFound` pins the on-disk case. Returning the probe's
    error would fail both.

  `TestReadFile_ErrorKindParity` asserts both rows by name. Their Kind (`ErrKindNotFound`) and
  Message must equal the unbounded `main` read's; changing the expected kind fails it. Proposed
  amendment:

  > - unknown ref → `ErrKindNotFound`, and path present in the working tree but not at the ref →
  >   `ErrKindNotFound` (both as on Spine `main` after TASK-002: `fatal: bad revision`), with Kind
  >   and Message equal to the unbounded `git show [eoo] <spec> --`;

  The amendment also re-bases I2 from "every input `61994fe` handled" to "every input Spine
  `main` (after TASK-002) handled". *Observation for the orchestrator, not changed here:*
  TASK-002's AC 10 ("as at `61994fe`") and its CHANGELOG line "Every other read miss was already
  `not_found`" do not hold for an unknown ref, which was `ErrKindPermanent` on `61994fe`. The
  change itself is TASK-002's, and its effect is the correct 404.
- **AC 11.4 (oversized workflow): confirmed. Its behaviour is set by TASK-011, so the AC needs the
  owner's amendment.** The authority for the change sits in TASK-011's artifact: its D2 ("A
  content rejection is not a sync error"), its design-check note "Spine TASK-003 contradicts this
  task and must be amended" (I4 reworded, the sentinel registered in `isContentReadError`, and a
  new AC), and row 6 of its Design check table. It is recorded here as an orchestrator ruling. This
  task's own design check did not amend AC 11.4, so the text still reads `error`. If the AC were
  implemented as written, `isContentReadError` would lose `ErrBlobTooLarge`. That would bring
  back the wedge that TASK-011 D2 exists to prevent: an oversized workflow would hold the sync in
  `error` on every tick. `TestProjection_OversizedGovernanceFiles` pins the amended behaviour. Its
  mutation (`isContentReadError` false) fails the test. Proposed amendment (it is the AC that
  TASK-011's design-check note prescribes for this task):

  > 4. An oversized `workflows/x.yaml` is a `projection_workflow_rejected` with check `read_bounds`
  >    (TASK-011 D2): a verified previous row stays quarantined at its commit, a new file is not
  >    projected, no `projection_file_skipped` is emitted, and the state is `idle` at the new head.
  >    An oversized `.spine/branch-protection.yaml` holds the state at `error`, `LastSyncedCommit`
  >    does not advance, and the rules stay (I4). `TotalAlloc` < 2 MiB.

- **AC 16 (three SMP tests): re-verified at `02000ff`, and the disposition is unchanged.** The
  three tests fail identically with this branch and with Spine `main`, and nothing in the 43
  dependent SMP packages differs between the two runs (gate table below). Proposed record for the
  orchestrator: *"AC 16 passes on SMP `f5c6fd6d` except
  `gitread.TestBounded_ErrorContractEqualsCLIClient`,
  `projectionwriter.TestBoundedProjection_IncrementalAddAndModifyOversized` and
  `projectionwriter.TestBoundedProjection_OversizedWorkflowStalls`. All three fail identically
  with Spine `main` `d23b897`. SMP TASK-004's tests pin SMP's pre-wave-2 Spine (TASK-002's
  `bad revision`, TASK-011's rejection path), and SMP TASK-008 / TASK-012 adapt them at the
  bump."*
- **AC 17 (merge ordering, PR description): the orchestrator's.** The precondition still holds:
  SMP TASK-003 is on SMP `main` `f5c6fd6d`, and `grep -rn "unquoteDiffPaths\|unquoteGitPath"
  internal/` there is empty.

**Deviation added this round**

19. **Refusals are reported once discovery has decided them, not after the changeset is applied**
    (codex C1). This supersedes §7's "runs after the changeset is applied" and Deviation 8's
    "`FullRebuild` reports after its writes". A held sync or a failed rebuild re-reports on each
    retry under the same deterministic event id. The delivery queue de-duplicates the events, and
    the counter counts each report. A sync held by an in-loop error already behaved this way, so
    the rule is now the same for every held sync.

**Gates at this round (code `02000ff`; every row re-run after the fix)**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `lint-boundaries: OK`; ok; `engine constructed and exercised; library surface is sound.` |
| `go build ./...`; `go vet ./...`, `-tags integration`, `-tags scenario` | all clean |
| `go test -p 4 -count=1 ./...` | exit 0, 40 packages ok |
| `go test -race -p 4 -count=1` on the 11 touched packages (`adapters/delivery`, `adapters/git/...`, `adapters/projection`, `core/artifact`, `core/domain`, `core/engine`, `core/observe`, `core/workflow`, `service/internal/gateway`, `core/evidence`) | all ok |
| `SPINE_TEST_DATABASE_URL=…/s003_proj go test -race -p 1 -count=1 -tags integration ./adapters/projection/`; `…/s003_deliv … ./adapters/delivery/` (fresh databases) | projection 103 top-level / 76 sub pass (the new test and its two subtests), delivery 139 / 176 pass; 0 fail, 0 skip. The pre-fix run of the same command, at load average 12.9, failed `TestQueuePG_CircuitOpenDoesNotStarve`. That is TASK-010's breaker test, which has a 10 s budget. It passed 3/3 standalone (2.4 s each) and passed in the post-fix run. The only change this task makes in `adapters/delivery` is the event-type registration |
| `go test -p 1 -count=1 -tags scenario ./service/scenariotest/...` on `s003_scen`, against the same suite in a `git archive main` export on `s003_scen_main` | both 191 pass / 30 fail / 6 skip (subtests 1 288 / 27), **0 outcome differences**; the 30 are the known pre-existing failures. CI's `scenario-git-http` set: 20 `TestGitHTTP_*` + 2 `TestBootstrapInternalAdmin_*` pass, no skip |
| CI `git-floor` steps in `golang:1.26-bookworm` (git 2.39.5, `--pull=never`, `--rm`, read-only mounts, `GOPROXY=off`), plus `core/engine` and `adapters/projection` | all ok |
| `golangci-lint run` on the 9 touched packages, plain / `--build-tags=integration` / `--build-tags=scenario`, branch vs a `main` export as (linter, file, message) multisets | **0 new issues** in each (188 vs 189: the `branch_validation.go` gofmt fix) |
| `golangci-lint run --enable-only=gosec`, all three tag sets | 0 issues |
| `gofmt -l` on every changed Go file | empty |
| `codex review --base main` | Plain, it stops with `The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account`, as in every earlier round. With `-c model=gpt-5.6-sol`, the run at `d92e8cf` raised one P2 (C1, fixed in `02000ff`), and the run at `02000ff` said "No actionable correctness regressions were identified" |
| `git diff --stat 61994fe -- migrations/`; `git diff --stat main...HEAD -- migrations go.mod go.sum` | empty (no schema change) |

**SMP compatibility gate at this round** (`/Users/bartoszszymichowski/42sid/i23/s-T003/smp`
detached at **`f5c6fd6d`**, `../spine` = this worktree; `git status` clean after every run):

| Command (in the SMP checkout) | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...` | all ok |
| `go test -race -p 4 -count=1 -json` on the **43** SMP packages that depend on a changed Spine package (`go list -test` deps, test imports included; round 1 counted 36), with no DB env. Then the same run with `-modfile` pointing `replace` at a `git archive main` export of Spine `d23b897`. The modfile lives in the scratchpad, so the SMP checkout is not touched | 2 493 pass / 3 fail / 291 skip at top level (subtests 2 645 / 5 / 14) **both ways: 0 outcome differences** |
| The three failures, run verbose against both | The messages are the same both ways. `gitread.TestBounded_ErrorContractEqualsCLIClient` fails `missing_path`, `bad_ref`, `bad_sha` and `path_beyond_a_symlinked_dir`: SMP's own `Bounded` reader still says `invalid object name` / `exists on disk, but not in` / `does not exist in` where Spine's `CLIClient` says TASK-002's `bad revision`, the AC 5 point above. `…_IncrementalAddAndModifyOversized` fails with `read changed artifact: … gitread: blob of 8388608 bytes exceeds the 1048576-byte read limit`: TASK-011 holds the sync on SMP's own error type. `…_OversizedWorkflowStalls` panics with a SIGSEGV in `bpStore.GetWorkflowProjection` (the fake's nil embedded store), called from TASK-011's `applyRejection` |
| `go run ./dev/apply-migrations -set platform -dir migrations` (`s003_smp_it`, pgvector); `-set spine-runtime` (`s003_spine_rt`) | 115 and 26 versions |
| Spine-runtime step: `TEST_SPINE_DB=… TEST_PLATFORM_DB=… TEST_PROVISIONING_DSN=…smp_prov go run ./dev/testgate -- -race -count=1 -timeout 15m ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` | 1 835 passed, 0 failed. The 2 skips are the Linux-only `ru_maxrss` tests, so testgate (which allows no skip here) says `FAIL (2)` on macOS for them alone. Both **PASS** in `golang:1.26-bookworm`: `TestDiffChildMemory_MillionPaths` and `TestReceiveTimeMemory_RealGitReaderFetch` |
| Platform-DB members that depend on a changed Spine package (17 packages: `dev/apply-migrations`, `automation`, `customer/{brain,discovery,eventdelivery,notification,profileinventory,projectionwriter,provenance,spineruntime,stepevents,workspace,workspace/workspacetest,docindex}`, `import`, `observability/opshealth`, `recovery`), with `TEST_PROVISIONING_DSN`, `-allow-skip 'TEST_MINIO' -allow-skip 'TEST_AWS_OBJECT_STORE_'` and the `-skip` of the two failing SMP TASK-004 tests | `testgate: PASS`: 1 524 passed, 0 failed, 7 allowed skips (MinIO, live AWS) |
| `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `testgate: PASS`, 246 passed. The allowed skip, `TestIntegration_ReceiveTimeMemoryBounded`, **PASS**es in the image |
| CI's verify-children step (`-tags integration -run '^TestVerifyChildrenProfile_ThroughGateway$' ./internal/automation/dockerexec/`) | `testgate: PASS`, 13 passed |
| `TestRepoArtifacts_ParseCleanlyUnderSpine` (AC 15 timing) | PASS, 31.7 s on a loaded machine (load average above 12) |
| AC 16 `len(result.Refused) == 0` | Not re-run. `core/` and `adapters/git/` are unchanged since round 1 (`git diff --stat a86ddc3 HEAD -- core/ adapters/git/` is empty), and this round's change only moves when the projection reports. The round 1 result stands |
| `grep -rn "unquoteDiffPaths\|unquoteGitPath" internal/` | empty |

Database: the shared server on 55630 was down again (`pg_isready`: no response). Every DB step ran
on a private, tmpfs-backed `pgvector/pgvector:pg18` container (PostgreSQL 18.4, `--pull=never`,
127.0.0.1:55934). It held `s003_*` databases and the SMP CI roles (`platform`,
`platform_audit_retention`, `smp_prov`, posture `f`). The container and everything in it were
removed at the end, as were the two `golang:1.26-bookworm` containers (`--rm`).

### Integration (wave 3 merge) (2026-10-14)

Integration branch `init/INIT-023-wave3-integration` (worktree `i23/s-int3/spine`), cut from Spine
local `main` `d23b897` (origin `61994fe` + the INIT-023 scaffold + waves 1 and 2). Merge order, each
`git merge --no-ff`, built and tested on its touched packages before the next: TASK-005 `06ccca2`
(merge `1f7af13`, clean), TASK-003 `b5ad640` (merge `88df8bb`; conflicts `CHANGELOG.md`, the
`adapters/git/cli.go` imports), TASK-014 `9c1778a` (merge `4ed5195`; `CHANGELOG.md`), TASK-015
`2096c1f` (merge `5029321`; `CHANGELOG.md`, the `CLIClient` fields in `adapters/git/cli.go`, the
`workflow.create` / `workflow.update` descriptions in `api/spec.yaml`). Every conflict kept both
sides: each task's CHANGELOG section (newest merge on top), both import sets, both field sets, and
TASK-015's identifier rule beside TASK-014's dispatch text. Coordination commits: `70d6c8e` (the
merge-binding plumbing runs under TASK-003's read bounds) with `f5b4347` (its lint), `94017aa`
(docs: review-time writes and the push window), `b911c04` (codex round 2: a captured binding is
stored before the step completes), `70bed62` (codex round 3: rows a pre-TASK-003 projection holds
at a refused path are retired). Nothing was pushed; `main` and the task branches are unchanged.

**This task in the integration.** Merge `88df8bb` (conflicts: `CHANGELOG.md`, both sections kept;
`adapters/git/cli.go` imports, `sort` from TASK-005 and `strconv` from this task).

- *With TASK-005 — a coordination fix (`70d6c8e`, `f5b4347`).* TASK-005's `CommitBinder` methods
  start git through their own `runWith`, outside `runSink`, so this task's I1 and its "stderr keeps
  64 KiB for every command" did not hold for them, and they ran in the host's locale. `runWith` now
  takes the C locale, the per-command output limit and the head-and-tail stderr sink; not
  `GIT_ALLOC_LIMIT` or the pinned delta-base cache (residual added to KNOWN-LIMITATIONS "Git Read
  Bounds": the take-back's `read-tree -m -u` materialises the tree like checkout, and the reads
  parse the commit the merge then reads without a limit). `TestEveryOpHasAnOutputClass` covers
  `run`/`runBounded` op names only; the new `adapters/git/commitbinder_bounds_test.go` covers the
  `runWith` commands' environment and bounds. The named-ref-window bullet now says TASK-005 reads
  the cascade at the bound commit with binding on (always in `spine serve`).
- *Codex round 3, P2 — fixed (`70bed62`), with TASK-011's sweep.* This task refused an
  `UnsafePath` unread and assumed no row could exist at one. Before it, git listed paths in its
  quoted form, which leaves every byte >= 0x80 raw under `core.quotePath=false` (repository, global
  or system config), so a bidi-control, U+2028/U+2029 or C1-control name could be projected. After
  the upgrade a deletion, rename or modification skipped its row: a stale artifact and its links
  stayed for good, and a stale workflow row stayed Active and bindable — TASK-011's sweep even
  re-read such a file and kept the row. Now, for `artifact.StoredUnsafePath` (an `UnsafePath` that
  is valid UTF-8, so the store can hold it): discovery puts a deleted, renamed-away or modified one
  in `Deleted` (still unread); the incremental workflow loop deletes the row of a deleted or
  renamed-away one and retires the row of an added or modified one as `applyRejection` does (kept
  quarantined when it verifies, else removed), reported as `projection_workflow_rejected` with the
  new check `unsafe_path`; the sweep rejects a stored row at an unsafe path the same way instead of
  re-reading it. Invalid UTF-8 still never reaches the store. Under the default
  `core.quotePath=true` no such row exists and nothing changes but an idempotent delete call.
  Residual (KNOWN-LIMITATIONS): an artifact row at such a path whose file never changes stays until
  the upgrade's projection rebuild (the CHANGELOG note; SMP TASK-008 runs it). Tests:
  `adapters/projection/stored_unsafe_rows_test.go` (pre-seeded rows; each of the discovery, loop
  and sweep parts fails against its previous code by `-overlay`, the rename branch separately);
  the refusal table in `core/artifact/discovery_refused_test.go` now expects `Deleted` for
  valid-UTF-8 unsafe paths (`a\u202eb.md` renamed away, `a\u0085b.md` modified, a new
  deleted-bidi row) and still never for invalid UTF-8 (two new rows). This amends design §5's table
  and the design-check premise that "git's quoting kept every byte ≥ 0x80 out of the projection"
  (true only under the default `core.quotePath=true`). Docs: `event-schemas.md` (check `unsafe_path`),
  CHANGELOG, `git-integration.md` §8.1, the `UnsafeRepositoryPath` comment.
- *With TASK-011 (read errors) and TASK-004 (worktree cap).* As built: `git.ErrBlobTooLarge` is in
  `isContentReadError` (an oversized workflow is a `read_bounds` rejection), and the AC 21 parity
  test (`DefaultMaxBlobBytes` = `worktreefs.DefaultMaxReadBytes`) passes on the merged tree.
- *With TASK-015.* `Clone` keeps both this task's bounded stderr and TASK-015's ambient-credential
  reset; `runSink`'s push environment keeps TASK-015's reset (push is `opMutate`, so it never gets
  the read-class environment); `CLIClient` carries both field sets. The new
  `spine_projection_files_skipped_total` is process-global and unlabelled, so in shared mode it is
  in `GET /api/v1/platform/metrics`, not a tenant's `/system/metrics` (TASK-015's scoping), which
  the docs already say for every process-global series. `projection_file_skipped` event ids are hex
  (`projskip-<12>-<12>`) and its path is quoted for display, so TASK-015's SSE writer never omits
  them.
- *With TASK-014.* `workflowPlanningPreCheck` reads the existing definition with this task's
  bounded `ReadFile`: a definition over the read limit (workflows are capped at 64 KiB by
  `yamlsafe` anyway) fails closed, as any non-not-found read error does.

**Gates on the integration branch** (code at `70bed62`; only these artifact notes were committed
afterwards):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 17 non-scenario packages the four tasks and the coordination commits touched | all ok |
| `-tags integration -race -p 1` (`SPINE_TEST_DATABASE_URL` = `s3i_it`; `SPINE_REGISTRY_TEST_DATABASE_URL` = `s3i_reg`, migrated from `migrations/registry`) on the touched packages with integration tests | `adapters/delivery` 315, `adapters/projection` 180, `service/cmd/spine` 184, `service/internal/workspace` 225 PASS (subtests counted), 0 FAIL, 0 SKIP |
| CI `scenario-git-http` job, its four steps run verbatim (`bash -eo pipefail`) on a fresh `s3i_ci3` | `TestGitHTTP_*` 20 PASS (8 `TestGitHTTP_Push*`), 0 SKIP; `TestBootstrapInternalAdmin_*` 2 PASS; `TestWorkflowLifecycle_*` 4 PASS (incl. `_UpdateExisting`, `_UpdateExisting_DraftAuthorCannotReview`), with the harness's merge binding on; `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS; 0 SKIP |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` (`s3i_scen3`, registry `s3i_reg3` migrated) vs a `git archive main` export on its own databases | 31 top-level FAIL, the **same set as `main`**: the 30 known pre-existing failures plus `TestWorkspace_ScopedServices` (pre-existing with the registry URL set). With an unmigrated registry database both trees also fail `TestWorkspace_BatchMigration` and `TestWorkspace_Lifecycle` (33 each, identical sorted `--- FAIL` lists, 70 lines with subtests); `main` passes those two with the registry migrated (checked) |
| `golangci-lint` v2.11.4 (`tmp/init027-orchestration/bin`) `run --new-from-rev main` on the touched packages: plain, `--build-tags=integration`, `--build-tags=scenario` (with `service/scenariotest/...`) | 0 issues each (one gocritic `appendAssign` on the first `runWith` change was fixed in `f5b4347`) |
| `--enable-only=gosec` on the touched packages (`integration,scenario` tags) and on `./...` | 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| CI `git-floor` steps in the local `golang:1.26-bookworm` image (git 2.39.5; `--pull=never --rm`, read-only mounts, `GOPROXY=off`), plus `adapters/projection` | all ok; no container left |
| `codex review --base main` (codex-cli 0.162.1, the configured default model, **no `-c model=` override**; each review session's `session_meta` has `source: {"subagent":"review"}` and `"model":"gpt-6.1-sol"`, the newest `rollout-2026-10-14T13-49-08-…`) | round 1 (`f5b4347`): "No actionable regressions were identified". Round 2 (`f5b4347`): one P2, TASK-005 — fixed in `b911c04`. Round 3 (`b911c04`): one P2, TASK-003 with TASK-011's sweep — fixed in `70bed62`. Three rounds is the cap, so `70bed62` itself was not reviewed by codex again (open question for the orchestrator). The earlier task rounds that ran on older models (`-c model="gpt-5.5"`, `gpt-5.6-sol`, `gpt-6-astra` because the default was refused then) are superseded by these gpt-6.1-sol rounds over the whole wave-3 diff |

**SMP compatibility** (`i23/s-int3/smp` = SMP `main` `f5c6fd6d`, detached, `../spine` = this
worktree, `GOFLAGS=-mod=readonly`; never modified, `git status` clean after every step):

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 13 SMP packages importing a changed Spine package (`go list` with test imports) plus `internal/customer/workflow` | 11 ok, `projectionsvc` has no tests; `gitread` (`TestBounded_ErrorContractEqualsCLIClient`: `bad_ref`, `bad_sha`, `missing_path`, `path_beyond_a_symlinked_dir`) and `projectionwriter` (`TestBoundedProjection_IncrementalAddAndModifyOversized`, `…_OversizedWorkflowStalls`, a nil-pointer panic) fail — the **identical** `--- FAIL` list against a Spine `main` `d23b897` export through a scratch `-modfile`: pre-existing, SMP TASK-004 tests that SMP TASK-006 adapts at the bump |
| SMP TASK-006 tip `935852b5` (a temporary detached SMP worktree beside this one, removed afterwards) against this Spine | `gitread` and `projectionwriter` **pass**; `go build ./...`, `go vet` plain and `-tags integration` OK. TASK-006's adaptations fix the two pre-existing failures, also with wave 3 |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s3c_smp_it` with `vector`, `s3c_spine_runtime_it`) | 115 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 25m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov` as in CI) | 1835 passed, 0 failed, 2 allowed skips (the Linux-only `ru_maxrss` tests; macOS host); `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -p 4 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed Linux-only skip; `testgate: PASS` |

Environment: the shared PostgreSQL 18.6 container on `localhost:55630`. Only `s3i_*` (Spine) and
`s3c_*` (SMP) databases were created, and all were dropped afterwards; the SMP suites' own `ws_*`
databases and roles were cleaned up by the suites (none left, checked). The only containers
started (the `git-floor` runs, `--rm`) were removed; the Docker VM kept at least 6.1 GB free. Logs:
`spine-management-platform/tmp/init027-orchestration/wave3-int/`.

### Cross-task review (wave 3) (2026-10-14)

The wave 3 cross-task review ran on the integration branch at `63e2318` (each P1/P2 put to two
skeptics, whose votes are summarised below; P3s had none). Every fix is on `init/INIT-023-wave3-integration`
(worktree `i23/s-int3/spine`), not on the task branches, which are unchanged: `f346c9e` (cascade
replay), `dcdfff5` (recovery serialisation) with `475e342` (its lint), `a6dbd67` (projection
refusal order), `7aedeca` (info/refs oracle), `3aa24b9` (docs), `cdd33ee` (codex round 1) with
`a02580e` (its lint). Each fix's test was checked to fail against the previous code with
`go test -overlay` (the old file swapped in), and to pass on the fix. Nothing was pushed.

| # | Finding (as raised) | Skeptic votes | Disposition |
|---|---|---|---|
| 1 | P2, `adapters/projection/service.go` `FullRebuild`: with `artifacts_dir` a subdirectory, `workflowPathsAt` lists the tree again for the root `workflows/`; a failure there returned `discover workflows` before any refusal `DiscoverAll` had already decided was reported (no Warn, no `projection_file_skipped`, no metric) | P3 ("only a transient fault fits the window"), not a bug (the second listing repeats the first over an immutable commit, so every lasting cause fails `DiscoverAll` first; the failed rebuild is itself recorded and the retry reports; SMP never sets `artifacts_dir`) | **Fixed** as cheap and clearly right (`a6dbd67`); the severity is the skeptics' (at most one sync interval's delay). `DiscoverAll`'s kept refusals are reported right after it, the root workflows' own refusals right after their listing; the retry reports both under the same ids. Test: `TestFullRebuild_RefusalsReportedBeforeTheRootWorkflowListing` (the second `ListFiles` fails once: the oversized `spine/docs/notes.md` is reported once and the metric rises by one; the retry reports it again and the unsafe root workflow once), failing on the previous code. CHANGELOG |
| 2 | Codex round 1, P2 (this task with TASK-005): the status cascade (`appendAddedMarkdown`, both binding modes) took every added `.md` path from the diff. With this task's raw `-z` `Diff`, a path with a control character was no longer C-quoted (whose read failed, so it was skipped) but read and rewritten; under merge binding `CommitFilesOnto` refuses a control byte in a path, so the whole cascade commit failed and the run merged with its task's status never updated | — (codex reproduced it; reproduced here on the real CLIClient) | **Fixed** (`cdd33ee`, lint `a02580e`): a path `domain.UnsafeRepositoryPath` refuses is never a cascade path. Binding on gets its task status back; binding off (SMP today) is back to `main`'s behaviour for such a file — skipped, not rewritten — so SMP sees no change; such a file is never a projected artifact either. Tests: `TestCascade_SkipsAnAddedFileAtAnUnsafePath` (real CLIClient, binding on and off: task and safe child Completed, the tab-named file merged as reviewed) and `TestAppendAddedMarkdownSkipsUnsafePaths` (control, tab, bidi, U+2028, invalid UTF-8 skipped; safe non-ASCII kept), both failing on the previous `merge.go`. CHANGELOG |

**Gates** (code at `a02580e`; `final-basic`, `final-gotest` and `final-race` ran on `cdd33ee`,
whose only difference is two `\u` escapes in one test's string literals):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 17 packages the four tasks touched (`adapters/{delivery,git,gitpool,projection,repository}`, `core/{artifact,domain,engine/...,observe,refadmit,workflow,worktreefs}`, `service/cmd/spine`, `service/internal/{gateway,githttp,workspace}`) | all ok |
| `-tags integration -race -p 1` on `adapters/delivery`, `adapters/projection`, `service/cmd/spine`, `service/internal/workspace` (`SPINE_TEST_DATABASE_URL` = fresh `s3f_it`, `SPINE_REGISTRY_TEST_DATABASE_URL` = `s3f_reg`, migrated from `migrations/registry`) | 905 PASS (subtests counted), 0 FAIL, 0 SKIP |
| CI `scenario-git-http` job, its four steps verbatim (`bash -eo pipefail`) on a fresh `s3f_ci` (code `cdd33ee`) | `TestGitHTTP_*` 20 PASS (incl. `TestGitHTTP_Push*`), `TestBootstrapInternalAdmin_*` 2 PASS, `TestWorkflowLifecycle_*` 4 PASS, `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS; 0 SKIP |
| `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (`s3f_scen`, registry `s3f_reg2` migrated; code `cdd33ee`) | the same 31 top-level `--- FAIL` (59 lines with subtests) as the integration run's `scen-branch-3` (identical sorted lists): the 30 known pre-existing failures plus `TestWorkspace_ScopedServices`. `TestPartialMergeRetry_HappyPath`, `TestPartialMergeExternalResolution_HappyPath`, `TestCancelFromPartiallyMerged_AsymmetricCleanup` and `TestPlanningRun_StatusCascadesToChildren` pass |
| `golangci-lint` v2.11.4 `run --new-from-rev main` on `core/engine/...`, `adapters/projection/...`, `service/internal/{gateway,githttp}/...`, `service/scenariotest/...`: plain, `--build-tags=integration`, `--build-tags=scenario` | 0 issues each (a gocritic `builtinShadow` was fixed in `475e342`, a gosec G116 / staticcheck ST1018 in `a02580e`) |
| `--enable-only=gosec` on the touched packages (`integration,scenario`) and on `./...` | 0 issues |
| `gofmt -l` (the Go 1.26.5 toolchain's) on every `.go` file changed since `63e2318` | empty |
| `codex review --base main` (codex-cli 0.162.1, the configured default model, no `-c model=` override) | round 1 (`475e342`; session `rollout-2026-10-14T15-20-33-01a13ac9-4f2b-…`, `source: {"subagent":"review"}`, `"model":"gpt-6.1-sol"`): one P2, fixed in `cdd33ee`. Round 2 (`cdd33ee`; `rollout-2026-10-14T15-30-35-01a13ad2-7e47-…`, review source, `gpt-6.1-sol`): "No actionable regressions were identified relative to the merge base." `a02580e` changes only test escapes, so no third round |

**SMP compatibility** (`i23/s-int3/smp` = SMP `main` `f5c6fd6d`, detached, `../spine` = this
worktree, `GOFLAGS=-mod=readonly`; never modified, `git status` clean after every step):

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` (on `a02580e`) | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on the 13 SMP packages importing a changed Spine package (test imports included) plus `internal/customer/workflow` (on `a02580e`) | 11 ok, `projectionsvc` has no tests; `gitread` and `projectionwriter` fail with the **identical** `--- FAIL` list as the integration run (and Spine `main` `d23b897`): pre-existing SMP TASK-004 tests that SMP TASK-006's branch adapts (the integration run checked that TASK-006's tip passes them) |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s3f_smp_it` with `vector`, `s3f_spine_runtime_it`) | 115 / 26 versions recorded |
| SMP CI "Spine runtime integration suites" via `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 25m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov`; code `cdd33ee`) | 1835 passed, 0 failed, 2 allowed Linux-only skips; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -p 4 -tags integration ./internal/customer/githttp/` | 246 passed, 0 failed, 1 allowed skip; `testgate: PASS` |

Not run: the CI `git-floor` job (git 2.39.5 in `golang:1.26-bookworm`) — not in this gate list, and
the Docker VM had 6.2 GB free; the new real-git tests use only porcelain any git has
(`update-ref`, a tab in a file name). Environment: the shared PostgreSQL 18.6 container on
`localhost:55630`; only `s3f_*` databases were created and all were dropped afterwards; the SMP
suites' `ws_*` databases and roles were cleaned up by the suites (none left, checked). No container
was started apart from `docker run --rm alpine:3 df` disk checks. Logs:
`spine-management-platform/tmp/init027-orchestration/wave3-fix-spine-s3f/`.
