---
id: TASK-003
type: Task
title: "Checkout and merge amplification budget, pre-receive tree limits, SMP-facing MeasureTree"
status: In Progress
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
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/tasks/TASK-012-shared-worktree-safety.md
  - type: related_to
    target: /architecture/git-integration.md
---

# TASK-003 — Checkout and merge amplification budget, pre-receive tree limits, SMP-facing MeasureTree

---

## Purpose

This task closes **N-1 (P2)** from the 2026-10-22 review (GT-2). The full write-up and the
measured PoC are in SMP's git-ignored `tmp/security-review-2026-10-22/spine/REPORT.md`; this
task restates only what the fix needs.

- **The defect.** A small pushed commit can expand enormously when a tree is written to disk,
  because git stores a repeated object once in the pack but a checkout materialises every
  occurrence. INIT-023 TASK-003 bounded Spine's git **reads**; it placed no bound on what a
  server-side checkout or merge **writes**.
- **The impact.** The expansion lands on the disk (or tmpfs memory) that every workspace on the
  replica shares, so it is a cross-tenant availability defect.
- **The measurement in the report:** a 4,470-byte pack drove `EnterBranch` to write about 200 MB.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| N-1 | P2 | **Standalone: CONFIRMED.** **Embedded: PLAUSIBLE** through SMP's own checkout and merge code. | **Standalone:** a run-branch author or bound actor, with `SPINE_GIT_RECEIVE_PACK_ENABLED=true` to push. Any later checkout or merge triggers the write, for example a write with `write_context.run_id` or the governed merge. **SMP:** any member or runner job that pushes its run branch through SMP's gateway. |

## Current code (verified at `a560c210`)

**Mutating git with no byte or entry budget.**

- `adapters/git/branchwrite.go:131` `EnterBranch`: looks the branch up (`:144`), then runs
  `git worktree add --detach … <oid>` (`:164`) and `git switch` (`:183`) into a temp directory.
  Reached from `core/artifact/service.go:745` and `core/workflow/service.go:122` for every
  branch-scoped write.
- `adapters/git/cli.go:605` `Checkout` → `switchTo` (`:619`).
- `adapters/git/cli.go:687` `Merge` → `runMerge` (`:750`), and `mergeSHA`
  (`adapters/git/commitbinder.go:471`) and `MergeBound` (`:455`) for the bound governed merge.
- `adapters/git/commitbinder.go:623` `read-tree -m -u` and `treelock.go:1182` `read-tree --reset -u`
  (the take-back and restore paths): both materialise the working tree.
- The `limits.go` header already records this as the open class: mutating commands "get no alloc
  limit (checkout and merge legitimately materialise large files)", and `KNOWN-LIMITATIONS.md`
  ("Not covered") names checkout-time inflation.

**The standalone push gate buffers only the command section.**

- `service/internal/githttp/handler.go:365` `readPushCommands` buffers to the flush, then hands
  the pack straight to `git-http-backend` (`:317`). git's own `receive.maxInputSize` bounds the
  pack bytes, not the tree the pack unpacks to.

**No tree-size primitive exists.** There is no helper to measure a tree's byte and entry totals
before a checkout.

## Design

1. **A tree budget primitive (`adapters/git/treebudget.go`).**
   ```go
   type TreeBudget struct {
       MaxEntries        int64 // distinct tree entries; 0 = default
       MaxTotalBytes     int64 // summed blob sizes; 0 = default
       MaxPathBytesTotal int64 // summed path-name bytes; 0 = default
   }
   func (b TreeBudget) Check(ctx, c *CLIClient, treeish string) error
   func MeasureTree(ctx, c *CLIClient, treeish string) (TreeStats, error) // exported, SMP-facing
   var ErrTreeTooLarge = errors.New("git: tree exceeds the checkout budget")
   ```
   - `MeasureTree` runs `git ls-tree -r -l -z --end-of-options <treeish>` as an `opRead` command
     (so it is bounded and streamed) and sums the entry count and the blob sizes it reports. It
     stops and returns `ErrTreeTooLarge` as soon as a running total crosses a budget, so it never
     holds the whole listing and an over-budget tree is cheap to reject.
   - It reuses TASK-004's streamed listing so a huge path listing does not itself overflow.
   - `Check` wraps `MeasureTree` against a budget.
   - `ErrTreeTooLarge` maps to `*GitError{Kind: ErrKindPermanent}` (not retryable; a retry cannot
     shrink the tree) and names the limit it crossed. It is a distinct error from
     `ErrBlobTooLarge` and `ErrOutputTooLarge`.
2. **Budget the client's own checkouts and merges.**
   - `EnterBranch`, `Checkout`, `Merge`, `mergeSHA`/`MergeBound`, and the `read-tree` take-back and
     restore each run `Check` on the tree they are about to write, before the mutating command.
     On refusal they return `ErrTreeTooLarge` and write nothing — `EnterBranch` returns before
     `worktree add`, so no temp directory is created.
   - The client carries one budget, set with `WithTreeBudget(TreeBudget)` (default below), read at
     each call so a later `With*` cannot bypass it.
   - **Argued default-on**, against initiative §5's opt-in rule: the zero `TreeBudget` means the
     defaults, not "unbounded". An availability control whose zero value is "off" would leave the
     P2 open for every embedder that did not opt in, including SMP. The defaults are ~30× the
     largest real tree (owner question 1), and every limit is configurable upward. This is listed
     as a shared-behaviour change in initiative §7b.
3. **Budget the push at pre-receive, and wall-clock the checkout.**
   - When receive-pack is on, after `git-http-backend` has written the objects but before the refs
     are visible, measure each pushed branch tip's tree with `MeasureTree` and refuse the push past
     the budget. This runs as a real pre-receive hook: `ensureReceivePackConfig`
     (`service/internal/githttp/handler.go:779`) installs a `hooks/pre-receive` that calls a new
     hidden subcommand, `spine git-hook pre-receive` (registered in `service/cmd/spine/main.go`),
     which reads the `(old, new, ref)` triples on stdin and measures each `new` tip.
     - The hook refuses before the ref moves, so an over-budget push never lands — this is the
       standalone control that also protects a later checkout a non-push path triggers.
     - A tree the hook cannot measure (timeout, git error) is refused (fail closed).
   - Mutating git commands get a configurable wall-clock deadline
     (`WithMutateTimeout`, default 2 min), so a checkout the budget somehow admits cannot run
     unbounded. The deadline is a context timeout on the command; the existing `newGitCmd` Cancel
     SIGTERMs the child (`adapters/git/gitcmd.go:45`).
4. **Defaults** (owner question 1): `MaxEntries` 200,000; `MaxTotalBytes` 2 GiB;
   `MaxPathBytesTotal` 16 MiB (so no admitted tree can wedge TASK-004's listings). Measured at
   `a560c210`: Spine is 1,624 entries / 21 MB, SMP 3,708 / 33 MB (`git ls-tree -r -l HEAD`).

## SMP impact

- **SMP's own N-1 half is required** (initiative §7b, "SMP owner"): SMP runs its own checkouts and
  merges, so a byte and entry budget must be added to `gitread.CheckTreeDiff`
  (`internal/customer/gitread/treediff.go`), to `boundRunBranchCheckout`
  (`internal/customer/spinecore/artifactservice.go:347`) and to `RealGitOperator.MergeBound`.
  SMP's `CheckTreeDiff` today caps changed paths (`SMP_GIT_MAX_DIFF_PATHS=100000`) and tree-object
  size but never sums blob bytes, so it admits ~400 GiB. SMP may call `git.MeasureTree`, or keep
  its own measure plus a differential test against `git.TreeBudget.Check`.
- **Shared at the bump:** Spine's `CLIClient` refuses an over-budget tree. In practice SMP reaches
  Spine's `EnterBranch` only in place (branch `""`, a measured no-op), so the behaviour SMP picks
  up is the availability of `git.MeasureTree`; its own checkouts stay SMP's responsibility.
- The SMP companion task owns the receiver-side budget at SMP's `/git/{workspace}` gateway.

## Decisions

1. **The client budget is default-on.** See Design 2.
2. **The gate is a real pre-receive hook, not in-process parsing.** The handler already shells to
   `git-http-backend`; a hook sees exactly the refs git will apply, after objects are written and
   before refs move, and cannot be bypassed by a pack shape the in-process command parser does not
   model (the N-9 ordering concern TASK-005 also addresses). The hook is a hidden subcommand of the
   same binary, so no extra artifact ships.
3. **Measure, do not inflate to check.** `MeasureTree` reads object headers (`ls-tree -l`), never
   object contents, so measuring an over-budget tree is itself bounded.

## Owner questions

1. **Default limits.** *Default:* 200k entries, 2 GiB, 16 MiB of path bytes. *Alternatives:* tighter
   (50k / 512 MiB) or looser. All configurable.
2. **Mutate deadline.** *Default:* 2 min. *Alternative:* off by default, on for `spine serve`.

## Acceptance Criteria

Each "regression" test fails at `a560c210` and passes on the branch; the Implementation Notes
record both runs.

1. **Regression: checkout is bounded** (`adapters/git`,
   `TestEnterBranch_RefusesOverBudgetTree`). This ports
   `scratch/git/f4/zz_rv_bomb_test.go` (`TestRV_CheckoutAmplification`) with a small budget: the
   PoC's amplifying commit makes `EnterBranch` return `ErrTreeTooLarge`, and the worktree temp
   parent does not exist afterward. At `a560c210` it writes the expanded tree.
2. **Merge is bounded** (`TestMerge_RefusesOverBudgetTree`, `TestMergeBound_RefusesOverBudgetTree`).
   The same commit as a merge source is refused before the working tree changes; `main` does not
   move.
3. **`MeasureTree` is accurate and cheap** (`TestMeasureTree_CountsEntriesAndBytes`,
   `TestMeasureTree_StopsAtFirstBudgetCrossing`). Counts match `git ls-tree -r -l`; an
   over-budget tree returns before the full listing is read (asserted through the command hook).
4. **Legitimate trees pass** (`TestTreeBudget_RealRepositoryPasses`). The repository's own HEAD
   tree passes the default budget with headroom.
5. **Regression: pre-receive** (`service/internal/githttp`,
   `TestPrereceive_RefusesOverBudgetTreePush`). With receive-pack on and the hook installed, the
   amplifying push is refused with a `remote:` message naming the budget, and the ref did not
   move. At `a560c210` the push lands.
6. **`spine git-hook pre-receive`** (`service/cmd/spine`, `TestGitHookPreReceive_*`). The
   subcommand measures each `new` tip on stdin, exits non-zero on an over-budget or unmeasurable
   tip, and zero otherwise.
7. **Mutate deadline** (`TestCLIClient_MutateTimeoutCancels`). A mutating command past the
   deadline is cancelled and the child receives SIGTERM.
8. **Error mapping** (`TestErrTreeTooLarge_IsPermanent`). `ErrTreeTooLarge` is
   `*GitError{ErrKindPermanent}`, is not `IsRetryable`, and is distinct from `ErrBlobTooLarge`
   and `ErrOutputTooLarge`.
9. **Existing suites.** `go test ./adapters/git/... ./core/... ./service/...`, the `git-floor`
   job, and `scenario-git-http` pass, except the known pre-existing failures (INIT-023 §7f).
10. **SMP compatibility gate** (initiative §5) passes with no SMP change; the note records that
    SMP's own N-1 half is a companion task.
11. **Documentation.** `KNOWN-LIMITATIONS.md` ("Not covered" becomes a bounded entry),
    `docs/integration-guide.md` (the budget env vars and the pre-receive hook) and `CHANGELOG.md`.

## Compatibility

- **Shared behaviour change** (initiative §7b): Spine's git client now refuses an over-budget
  checkout or merge, by default.
- New env vars at `spine serve`: `SPINE_GIT_TREE_MAX_ENTRIES`, `SPINE_GIT_TREE_MAX_BYTES`,
  `SPINE_GIT_TREE_MAX_PATH_BYTES`, `SPINE_GIT_MUTATE_TIMEOUT`.
- `MeasureTree`, `TreeBudget`, `WithTreeBudget`, `WithMutateTimeout` and `ErrTreeTooLarge` are
  new exported symbols. No interface SMP implements gains a method.

## Out of scope

- SMP's own checkout/merge budget and SMP's gateway receiver budget (SMP companion task).
- Receive-time pack inflation (`index-pack`); `receive.maxInputSize` bounds the pack and is an
  operator setting, documented.
- Per-tenant disk quotas.

## File overlaps

- `adapters/git/cli.go`: this task touches `Checkout`, `Merge`, `runMerge`; TASK-004 touches
  `ListFiles`, `Diff`, `Log`. Different functions. TASK-004 rebases on this task and adopts
  `MaxPathBytesTotal`.
- `adapters/git/limits.go`: budget constants beside the read caps (additive; TASK-004 adds the
  streamed sink).
- `service/internal/githttp/handler.go`: this task adds the hook install to
  `ensureReceivePackConfig` (`:779`); TASK-005 adds a storage probe to the same function and
  touches the semaphore. TASK-005 rebases on this task.
- `service/cmd/spine/main.go`: this task registers `git-hook`.
- `adapters/git/branchwrite.go`, `treebudget.go`: this task only.

---

## Implementation Notes

*(Implementation, 2026-10-23. Branch `init/INIT-024-TASK-003-checkout-merge-amplification-budget`, cut
from `init/INIT-024-scaffold` `12376b55` (= `a560c210` + the INIT-024 artifacts; no code differs). Run
on macOS, git 2.54.0, go 1.26.5, and on git 2.39.5 in the local `golang:1.26-bookworm` image for the
`git-floor` job. Scratch logs: SMP repo `tmp/init027-orchestration/i24/t003/`.)*

### Rulings and owner-question defaults (recorded, pending the owner)

- **Cross-repo rule (2) binds (orchestrator ruling), so the library budget is opt-in** — Design 2
  argued default-on; see Deviation 1. `spine serve` turns it on by default.
- **OQ1 — default taken:** 200,000 entries / 2 GiB / 16 MiB of path bytes, all configurable
  (`git.TreeBudget`; `SPINE_GIT_TREE_MAX_ENTRIES`, `SPINE_GIT_TREE_MAX_BYTES`,
  `SPINE_GIT_TREE_MAX_PATH_BYTES`).
- **OQ2 — default taken as the `spine serve` value:** a 2-minute mutate deadline
  (`SPINE_GIT_MUTATE_TIMEOUT`); the library default is none (rule 2), which is the OQ2
  alternative's shape for embedders.
- **INIT-024 OQ3 (standalone defaults) — applied:** no off switch; an unset variable is the
  default, any non-positive or unparsable value refuses to start. The pre-receive hook is on
  whenever receive-pack is.

### What was built

| Commit | Change |
|---|---|
| `83c43684` | `adapters/git/treebudget.go` (new): `TreeBudget` (`Effective`, `Measure`, `Check`), `DefaultTreeBudget`, `MeasureTree`, `TreeStats`, `TreeLimit*`, `ErrTreeTooLarge` + `*TreeTooLargeError` in `*GitError{Permanent, TreeTooLargeMessage}`, `*MutateTimeoutError` in `*GitError{Permanent, MutateTimeoutMessage}`, `CheckoutLimits`, `SetDefaultCheckoutLimits` / `DefaultCheckoutLimits` (process default, `atomic.Pointer`), `WithTreeBudget`, `WithMutateTimeout`, `CLIClient.CheckoutLimits()`; the streamed `ls-tree -r -t -l -z --full-tree` parser (`treeListingSink`: running totals only, a partial record bounded by the path budget, stops and SIGTERMs git at the first crossing; read-class env via the extracted `commandEnv`); the `.gitattributes` expansion scan (Adversarial 1). `limits.go`: `DefaultTree*`, `DefaultMutateTimeout`. `cli.go`: `Checkout` (branch tree), `Merge` (target + source, before the switch), `Clone` (Adversarial 3), `runSink` deadline for local `opMutate` ops (not push), `measure_tree` op class. `commitbinder.go`: `mergeSHA` (MergeBound / Merge with SourceSHA), `ResetBranch` (`to` before `update-ref`), `runWith` deadline. `treelock.go`: restore switch to the home branch, `resetTreeOverRefusedSwitch`. `branchwrite.go`: `EnterBranchWithLimits` (`EnterBranch` = it with the process default): measure before `MkdirTemp`; with a deadline `worktree add --no-checkout` + own `reset --hard` (Adversarial 2); `enterInPlace` client carries the limits. `service/internal/githttp/budget_hook.go` (new): hook script, `installBudgetHook` (per receive-pack POST, atomic, never replaces a foreign hook or follows a link), `hooksPathEnv` (`core.hooksPath` pinned via `GIT_CONFIG_PARAMETERS`), `PreReceiveHookMain`, the package-`init` hook entry (`HookProcessEnv`, Deviation 4); `handler.go`: `Config.TreeBudget`, hook built in `NewHandler` (receive-pack on), installed + pinned in `ServeHTTP`. `service/cmd/spine`: hidden `git-hook pre-receive` (`cmd_git_hook.go`), `serve_checkout_limits.go` (`loadCheckoutLimits`, `installCheckoutLimits` before any client), `processTreeBudget` into the git handler. `core/worktreefs/scan_test.go` allowlist (function names moved). Docs: `KNOWN-LIMITATIONS.md` ("Checkout Budget"; the "Not covered" line), `docs/integration-guide.md` (§7 hook paragraph; four env rows), `CHANGELOG.md`, `architecture/git-integration.md` §8.1. Tests below. |
| `8407fb74` | Codex round 1 (2 x P2): the clone's separate `reset --hard` runs with the clone's own `-c` args and environment; Clone splits whenever a budget or a deadline is set. |
| `121bd781` | Codex round 2 (P2): a failed or stopped clone checkout empties a pre-existing destination again. |
| `34bdab7a` | Codex round 3 (P2): the shared tree's restore measures HEAD before its `reset --hard` (HEAD's ref can move under the tree with no measured checkout); docs. |
| (this commit) | Implementation Notes; status In Progress; integration-guide wording of the hook script. |

### Acceptance criteria — evidence

"Red" = the test run against a `git archive` of `init/INIT-024-scaffold` (code = `a560c210`) with a
compile-only shim declaring the new names with today's behaviour (`zz_redshim.go`: every option a
no-op, `Measure`/`Check` nil, `EnterBranchWithLimits` = `EnterBranch`); for githttp the base variant
of the test drops the two lines that configure a hook the base does not have
(`tmp/.../t003/base/`, logs `red-*.log`). "Green" = the branch.

| AC | Status | Evidence |
|---|---|---|
| 1 | done | `TestEnterBranch_RefusesOverBudgetTree` (PoC shape: one 4 KiB blob x 50 entries, budget 64 KiB; explicit limits and the process default). **Red:** "EnterBranch admitted the amplifying tree and wrote 51 files, 204950 bytes" (both subtests). **Green:** `ErrTreeTooLarge` (total_bytes), `TMPDIR` empty, one worktree registered; a small branch still enters. Also `TestEnterBranch_RefusesEntryAndPathBombs` (2,000 empty-blob entries; depth-150 tree; one 200-byte path) — red: all three "admitted". |
| 2 | done | `TestMerge_RefusesOverBudgetTree` (branch source, `SourceSHA`, `fast-forward`, over-budget *target*, plus a within-budget merge), `TestMergeBound_RefusesOverBudgetTree`, `TestCheckout_RefusesOverBudgetTree`: refused, `main` at base, HEAD on main, `status --porcelain` empty, no `docs/`. **Red:** "err = <nil>, want ErrTreeTooLarge" in every subtest. |
| 3 | done | `TestMeasureTree_CountsEntriesAndBytes`: blobs (file, executable, symlink), gitlink, directories, bytes and path bytes equal `git ls-tree -r -l` / `-r -t -z --name-only` through a branch, a commit and a tree id; a blob, a missing ref and an option-shaped tree-ish are errors. `TestMeasureTree_StopsAtFirstBudgetCrossing`: a 20,000-entry tree (listing > 1 MiB); for each limit the measure hook (`SetMeasureHookForTest`) shows the parser was handed < 1/4 of the listing and parsed <= 400 records; the default budget measures it to the end. New API, so no red. |
| 4 | done | `TestTreeBudget_RealRepositoryPasses`: HEAD = 1,961 entries, 21,420,845 bytes, 133,291 path bytes, expansion 1 (git 2.39.5 container; >= 100x headroom on entries and path bytes, ~100x on bytes). |
| 5 | done | `TestPrereceive_RefusesOverBudgetTreePush` (real `git push` through `Handler.ServeHTTP`): `remote: spine: push refused: "refs/heads/feature-bomb": tree exceeds the checkout budget (the tree's files total more than 65536 bytes (checkout budget MaxTotalBytes)); raise SPINE_GIT_TREE_MAX_BYTES to admit it` and `[remote rejected] (pre-receive hook declined)`; ref absent; the commit is not in the repository (quarantine discarded); a within-budget push lands. **Red:** `* ...:refs/heads/feature-bomb [new branch]` (the push lands). Also `TestPrereceive_BudgetHookCannotBeRedirected` (repo `core.hooksPath`, an edited Spine hook, a deleted hook, a foreign hook — red: lands), `TestBudgetHook_ScriptQuoting`, `TestBudgetHook_ProcessEntryRefusesMisuse`. |
| 6 | done | `TestGitHookPreReceive_RefusesOverBudgetTip` (bytes and entries, each naming its variable), `_AdmitsWithinBudget` (two creates of one tip, a delete, default flags, empty input), `_RefusesUnmeasurable` (blob tip, missing object, malformed line, abbreviated id, bad flag, stray argument), `_IsHidden`. New command (absent at the base). |
| 7 | done | `TestCLIClient_MutateTimeoutCancels` (300 ms deadline, a 5 s smudge filter): `Checkout` (runSink), `read-tree -m -u` (runWith) and `EnterBranchWithLimits` each return the permanent `*MutateTimeoutError` (`errors.Is` `context.DeadlineExceeded`) within 4 s, git's exit status is "terminated" (SIGTERM, macOS and Linux), no `index.lock`; without a deadline the same checkout completes. **Red:** "a command stopped at its deadline reported success" x 3. The worktree subtest first failed on the branch (5.1 s): Adversarial 2. Clone: `TestClone_MutateTimeoutBoundsTheCheckout` (codex round 1). |
| 8 | done | `TestErrTreeTooLarge_IsPermanent`: permanent, not retryable, matches only `ErrTreeTooLarge`; read-bound errors never match it; the message names `MaxTotalBytes` and the limit and never echoes the tree-ish. |
| 9 | done | Gates below: `go test ./...` all ok; `git-floor` rc=0; `scenario-git-http` rc=0. |
| 10 | done | SMP gate below: no SMP change; only the two by-design gitread contract failures that SMP main has against `a560c210`. SMP's own N-1 half (CheckTreeDiff byte sum, `boundRunBranchCheckout`, `RealGitOperator.MergeBound`, the gateway receiver) is the SMP companion task (INIT-027 EPIC-005 TASK-011). |
| 11 | done | `KNOWN-LIMITATIONS.md` "Checkout Budget" (bounds, opt-in/standalone, pre-receive, attribute expansion, non-atomicity, deadline, refusals) and the "Not covered" line; `docs/integration-guide.md` §7 + env table; `CHANGELOG.md` (Security / Changed (operators) / Changed (embedders)); `architecture/git-integration.md` §8.1. |

### Adversarial self-review (ways around the budget)

| # | Attempt | Result |
|---|---|---|
| 1 | **`.gitattributes` expansion** inside the byte budget: `ident` (4 → 47 bytes per `$Id$`, 71 with SHA-256), `text eol=crlf` (2x), `working-tree-encoding=UTF-32` (4x) | **Real, fixed.** Measured on git 2.54: 50,000 stored bytes under `* ident text eol=crlf` wrote 490,000; 16,008 under `* ident` wrote 188,148. The measure reads the tree's `.gitattributes` (<= 64 files, <= 64 KiB each; more, larger or a symlinked one = worst case 188x / 284x) and applies the worst-case product of the classes they mention (loose substring match, so macros count); merges use the union of both sides. `TestTreeBudget_AttributeExpansionIsCounted` (ident, macro, UTF-32 refused; eol-only and `binary` admitted; the factor bounds what git really wrote) — **red:** "admitted a tree that wrote 188148 bytes" (ident), 188169 (macro), 64178 (UTF-32). `TestTreeBudget_AttributeFilesTheScanCannotReadAreWorstCase`. |
| 2 | **`worktree add` vs the deadline**: worktree add writes the tree in a child `git reset --hard` the SIGTERM never reaches | **Real, fixed** (the AC 7 worktree subtest took 5.1 s against a 300 ms deadline): with a deadline the worktree is added `--no-checkout` and filled by our own deadlined `reset --hard`. |
| 3 | **`Clone`** writes the remote's default branch unmeasured (a code repository registered by a workspace admin) | **Real, fixed:** `--no-checkout`, measure HEAD, `reset --hard`; a refusal removes the clone. `TestClone_RefusesOverBudgetTree` — **red:** fails at the base. Codex rounds 1–2 hardened this path. |
| 4 | **Hook recursion**: the hook execs `os.Executable()`, which in any test binary is the test binary — handed `git-hook pre-receive` it ran its whole suite inside git, recursively (found by the first full githttp run: 660 s, nested `remote: --- FAIL` lines) | **Fixed** by the package-`init` entry (`HookProcessEnv`, Deviation 4): every binary that can serve a push runs the hook before `main`/`TestMain`. The full githttp suite (every existing push test now passes through the real hook) runs in ~10 s. |
| 5 | merge vs checkout vs worktree add vs read-tree vs restore | All measured before the command: `TestTreeBudget_ResetBranchIsBounded`, `TestTreeBudget_InPlaceRestoreIsBounded` (red: both fail at the base); the restore's own `reset --hard` of a moved HEAD was codex round 3's find. `CommitFilesOnto`'s private-index `read-tree` (no `-u`) writes only an index of a bound, already-measured commit. |
| 6 | many small files / one huge blob / deep trees / long paths | entries / bytes / path bytes (AC 1 tests); a deep tree's paths sum quadratically, so depth is bounded too. |
| 7 | packed vs loose, deltas | Sizes come from object headers (for a delta, its result-size header), independent of representation; AC 5 measures objects as pushed (pack, in quarantine), the adapter tests loose objects. |
| 8 | symlinks / gitlinks | Counted as entries (`TestTreeBudget_GitlinksAndSymlinksCount`); a symlinked `.gitattributes` is the worst case. |
| 9 | sparse checkout | The full tree is measured: an over-estimate, safe. |
| 10 | pre-receive limit vs post-receive mutate path | Both measure (same budget in `spine serve`). Residual: a branch moved between the client's measure and git's switch is written at its new tip; in standalone every tip passed pre-receive. `MergeBound` merges the exact measured commit (KNOWN-LIMITATIONS). |
| 11 | repeated small pushes that each pass | Every measure is of the whole tree, never a delta: `TestTreeBudget_RepeatedSmallPushesAreMeasuredWhole`. Residual: concurrent worktrees of different branches each get the full budget (no per-tenant quota; out of scope). |
| 12 | redirecting or removing the hook | `core.hooksPath` pinned per receive-pack; hook rewritten each push; a foreign hook refuses pushes (AC 5 tests). |
| 13 | not covered | smudge filter *drivers* the host configures (git-lfs), the host's `core.autocrlf`, package-level `StageAndCommit` (not deadlined; one staged file), a smudge filter left running after its git is stopped — all in KNOWN-LIMITATIONS. |

### Deviations

1. **Library opt-in, not default-on** (orchestrator ruling: INIT-024 rule 2 binds). The zero value
   of a client (no `WithTreeBudget` / `WithMutateTimeout`, no `SetDefaultCheckoutLimits`) measures
   nothing, starts no extra process and keeps every argv (`TestTreeBudget_OffStartsNoProcess`
   records the argv of Checkout/Merge/MergeBound/EnterBranch: no `ls-tree`). `spine serve` sets the
   process default. So the initiative §7b Rule 4 row for TASK-003 ("Spine's client refuses an
   over-budget tree") is **not** a shared-behaviour change at the bump — SMP behaviour is unchanged
   until it opts in. TASK-010 should reconcile §7b and §5.
2. **Mutate deadline:** library default none, `spine serve` 2 min; it covers local mutating
   commands only (not `push` or a clone's fetch), and an expiry is *permanent*
   (`MutateTimeoutMessage`).
3. **Hook install point:** on every receive-pack POST in `ServeHTTP` (not cached in
   `ensureReceivePackConfig`, which is untouched — smaller TASK-005 overlap; a removed hook is put
   back before the next push), plus the `core.hooksPath` pin the design did not mention.
4. **Hook entry:** besides the hidden `spine git-hook pre-receive`, githttp's package `init` runs
   the hook when `SPINE_INTERNAL_GIT_HOOK=pre-receive` (the script exports it) — needed because the
   hook execs the *installing* binary (Adversarial 4).
5. **More exported API** than the design listed (additive): `CheckoutLimits`,
   `SetDefaultCheckoutLimits`, `DefaultCheckoutLimits`, `CLIClient.CheckoutLimits`,
   `EnterBranchWithLimits`, `TreeStats`, `TreeLimit*`, `TreeTooLargeError`, `TreeTooLargeMessage`,
   `MutateTimeoutError`, `MutateTimeoutMessage`, `DefaultTree*`, `DefaultMutateTimeout`,
   `MeasureTrace`, `TreeBudget.Effective/Measure`; githttp `Config.TreeBudget`,
   `PreReceiveHookMain`, `Env*`, `HookProcessEnv`. No interface gained a method.
6. **Entries include directories** (`ls-tree -r -t`); AC 3 compares files+gitlinks with
   `ls-tree -r -l` and Entries with `ls-tree -r -t`.
7. **Own streamed listing**, not TASK-004's (wave 3, not landed); TASK-004 may fold its sink into
   `treeListingSink` when it rebases.
8. **Beyond the design** (adversarial): attribute expansion, `Clone`, the worktree split.
9. **`buildGitHTTPHandler`** takes the budget from the process default (no signature change; less
   TASK-006 overlap in `serve_wiring.go`).
10. A tag pointing at a blob is now refused at pre-receive (not measurable; operator-only refs).

### Gates

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | rc=0 |
| `make build`; `go build -o … ./cmd/embed-smoketest && ./embed-smoketest` | rc=0; rc=0 |
| `go vet ./...` plain / `-tags integration` / `-tags scenario` | rc=0 / 0 / 0 |
| `go test -p 4 ./...` | rc=0 at `34bdab7a` (an earlier run's only failure, `core/worktreefs TestNoUnconfinedWorktreeIO` naming the moved functions, was fixed in the allowlist) |
| touched packages `-race` (`adapters/git`, `githttp`, `cmd/spine`) | rc=0 at `34bdab7a` |
| integration `-tags integration` (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `core/artifact`, `core/workflow`), own DB `s24003_ci_final` (dropped) | rc=0 at `34bdab7a` |
| `scenario-git-http` job: `TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*` | rc=0 at `34bdab7a` (87 / 26 / 130 passes, 0 skips) |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`, read-only mounts): both CI steps + this task's githttp/cmd tests | STEP1 / STEP2 / EXTRA rc=0 at `34bdab7a`; a verbose run (at `83c43684`) showed every TASK-003 test PASS on 2.39.5, the SIGTERM exit status included |
| lint-boundaries, `make build`, embed-smoketest, `go vet` plain / integration / scenario | rc=0 at `34bdab7a` |
| golangci-lint v2.11.4 on touched packages vs the scaffold | 0 new issues (7 found and fixed: 5 errcheck, 2 builtinShadow); `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files | clean (`cmd_migrate.go` is pre-existing) |
| codex | see below |

### SMP compatibility gate

Run twice — at `8407fb74` and at the final code commit `34bdab7a` (the worktree's only uncommitted
files then were this artifact and a docs sentence), with identical results. `smp` (SMP main
`3addd44b`, `../spine` = this worktree): `go build ./...` rc=0;
`go vet` plain / integration / scenario / faultinject rc=0; `go test -race -p 4` over the
initiative §5 packages (`cmd/customer-app`, `internal/customer/{githttp,gitread,projectionwriter,
spinecore/...,spineembedded/...,stepevents,workspace/...,run/...}`, `internal/engine/...`): only
`gitread` `TestClient_ErrorContractEqualsCLIClient/*/dangling_gitlink` and
`TestClient_SpineReadFileRendersTheGitlinksPatch` — the two by-design contract tests SMP main fails
against `a560c210` (RULES; identical in the TASK-001 gate); `dev/testgate` Spine-runtime leg PASS
(2,143 passed, 0 failed, 3 allowed skips) and githttp leg PASS (588 passed, 1 allowed skip); no
`ws_*` database or role left, and the `s24003_*` databases were dropped. No SMP change.

### Codex (`codex review --base init/INIT-024-scaffold`, plain; model `gpt-6.1-sol` confirmed in each review sub-session)

| Round | Review session | Findings | Disposition |
|---|---|---|---|
| 1 | `01a1682b-c43d-7b81-81b0-59ea78b92ae0` | P2 the clone's `reset --hard` lost `WithoutAmbientCredentials` isolation (host smudge filters/credentials); P2 a deadline-only client left the clone checkout undeadlined | Both real, fixed `8407fb74`; `TestClone_CheckoutKeepsTheCloneEnvironment`, `TestClone_MutateTimeoutBoundsTheCheckout` — red at `83c43684` (filter ran; 5.05 s), green |
| 2 | `01a1690a-b77a-7c33-95ce-6ce0749eb1aa` | P2 a failed checkout into an existing empty directory left partial files | Real, fixed `121bd781`; `TestClone_FailedCheckoutLeavesTheDirectoryEmpty` — red at `8407fb74` (left `.gitattributes a.md`), green |
| 3 | `01a16989-5013-74e1-8166-5e2c9a0fb172` | P2 `restoreHeld`'s `reset --hard` wrote an over-budget HEAD before (or without) the budgeted switch home — reproduced with `update-ref` of the checked-out home branch | Real, fixed `34bdab7a`; `TestTreeBudget_RestoreMeasuresHEADBeforeItsReset` — red at `121bd781` ("restore over an over-budget HEAD: <nil>"), green; KNOWN-LIMITATIONS: an over-budget home branch blocks the shared tree (fail closed) |
| 4 | `01a169b6-8117-7eb1-82a9-f7f48dea370b` | none ("No actionable defects") | — clean at `34bdab7a` |

### Not run

- The full `make test-scenario` with a registry DB (only the CI job's three scenario groups; known
  ~31 pre-existing failures). Command: `SPINE_TEST_DATABASE_URL=… SPINE_REGISTRY_TEST_DATABASE_URL=…
  make test-scenario`.
- GitHub CI (nothing is pushed; INIT-024 OQ1).
- A Linux-host measurement of a real terabyte-class bomb: deliberately not run (disk safety); every
  fixture expands to <= ~200 KiB even when not refused.

### SMP adoption (for the SMP companion, INIT-027 EPIC-005 TASK-011)

- **API.** Before each of its own checkouts and merges — `boundRunBranchCheckout`
  (`internal/customer/spinecore/artifactservice.go`), `RealGitOperator.MergeBound`, and at its
  `/git/{workspace}` receiver — SMP calls
  `git.TreeBudget{MaxEntries: e, MaxTotalBytes: b, MaxPathBytesTotal: p}.Check(ctx, client.CLIClient, sha)`
  with a `gitread.NewClient(clonePath)` client (its embedded `*git.CLIClient`), or
  `git.MeasureTree` / `TreeBudget.Measure` for the `TreeStats`. A refusal is
  `errors.Is(err, git.ErrTreeTooLarge)`; `errors.As(err, &*git.TreeTooLargeError)` gives `Limit`,
  `Max`, `Seen`, `Expansion` for the message (it never contains the sha). Measure a commit or tree id
  SMP already resolved, so the measured object is the one it then checks out. For a merge, check
  both sides (`TreeBudget.Check` each; the union-of-attributes rule is internal to the client path).
  The measure inherits the process environment, so it also works inside a pre-receive hook (git's
  quarantine variables).
- **Optional:** `gitread.WithCLIOptions(git.WithTreeBudget(b), git.WithMutateTimeout(d))` turns the
  budget and deadline on for every Spine `CLIClient` path SMP reaches (an in-place `EnterBranch` has
  no home branch in SMP, so it measures nothing; `Clone` would split and measure);
  `git.SetDefaultCheckoutLimits` does it process-wide. Either is opt-in; nothing changes at the bump.
- **Knob names (suggested, mirroring Spine's):** `SMP_GIT_TREE_MAX_ENTRIES`,
  `SMP_GIT_TREE_MAX_BYTES`, `SMP_GIT_TREE_MAX_PATH_BYTES`, `SMP_GIT_MUTATE_TIMEOUT`; same parsing
  rule (unset = default, non-positive or unparsable refuses to start).
- **Recommended SMP limits:** Spine's defaults — 200,000 entries, 2 GiB (2147483648), 16 MiB
  (16777216), 2 min — except `MaxEntries` no higher than `SMP_GIT_MAX_DIFF_PATHS` (100,000 today) so
  the two caps agree; SMP's own tree is 4,393 entries / 33 MB. A hosted multi-tenant pilot may take
  OQ1's tighter alternative (50k / 512 MiB).
- **Differential test:** if SMP keeps its own byte sum in `gitread.CheckTreeDiff`, pin it against
  `git.TreeBudget.Check` on the same fixtures (the PoC bomb, an `ident` tree, a deep tree).
