---
id: TASK-003
type: Task
title: "Bounded blob reads and NUL-delimited Diff/ListFiles in the git CLI client"
status: Pending
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-06
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
17. **Merge ordering.** The PR merges to Spine `main` only after SMP TASK-003 is on SMP `main`.
    The PR description names the SMP commit.

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
