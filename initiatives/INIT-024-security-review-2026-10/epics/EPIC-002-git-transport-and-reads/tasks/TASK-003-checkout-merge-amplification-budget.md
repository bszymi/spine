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
- **Nothing changes at the bump** (as built; Deviation 1): the budget is library opt-in — a
  `CLIClient` without `WithTreeBudget` / `WithMutateTimeout`, in a process that never calls
  `git.SetDefaultCheckoutLimits`, behaves exactly as before — and default-on only in `spine
  serve`. What SMP picks up is the availability of `git.MeasureTree` / `git.TreeBudget.Check`; its
  own checkouts stay SMP's responsibility.
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

- **Not a shared behaviour change** (as built; Deviation 1): the library budget is opt-in, with
  the zero value = today's behaviour; it is on by default only in `spine serve`, which refuses an
  over-budget checkout, merge (measured by its result tree), clone or push. The initiative §7b
  row for TASK-003 should say so (TASK-010).
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
| 1 | *(Superseded by review round 2: the scanner described here was removed; attributes are now neutralised — see "Review round 2".)* **`.gitattributes` expansion** inside the byte budget: `ident` (4 → 47 bytes per `$Id$`, 71 with SHA-256), `text eol=crlf` (2x), `working-tree-encoding=UTF-32` (4x) | **Real, fixed.** Measured on git 2.54: 50,000 stored bytes under `* ident text eol=crlf` wrote 490,000; 16,008 under `* ident` wrote 188,148. The measure reads the tree's `.gitattributes` and applies the worst-case product of the expanding attributes they set. *(Superseded in review round 1: the first version's loose substring match and its jump to the worst case above 64 files refused legitimate trees; see "Review round 1", ruling 2.)* `TestTreeBudget_AttributeExpansionIsCounted` (ident, macro, UTF-32 refused; eol-only and `binary` admitted; the factor bounds what git really wrote) — **red:** "admitted a tree that wrote 188148 bytes" (ident), 188169 (macro), 64178 (UTF-32). `TestTreeBudget_AttributeFilesTheScanCannotReadAreWorstCase`. |
| 2 | **`worktree add` vs the deadline**: worktree add writes the tree in a child `git reset --hard` the SIGTERM never reaches | **Real, fixed** (the AC 7 worktree subtest took 5.1 s against a 300 ms deadline): with a deadline the worktree is added `--no-checkout` and filled by our own deadlined `reset --hard`. |
| 3 | **`Clone`** writes the remote's default branch unmeasured (a code repository registered by a workspace admin) | **Real, fixed:** `--no-checkout`, measure HEAD, `reset --hard`; a refusal removes the clone. `TestClone_RefusesOverBudgetTree` — **red:** fails at the base. Codex rounds 1–2 hardened this path. |
| 4 | **Hook recursion**: the hook execs `os.Executable()`, which in any test binary is the test binary — handed `git-hook pre-receive` it ran its whole suite inside git, recursively (found by the first full githttp run: 660 s, nested `remote: --- FAIL` lines) | **Fixed** by the package-`init` entry (`HookProcessEnv`, Deviation 4): every binary that can serve a push runs the hook before `main`/`TestMain`. The full githttp suite (every existing push test now passes through the real hook) runs in ~10 s. |
| 5 | merge vs checkout vs worktree add vs read-tree vs restore | Checkouts, worktrees, read-trees and restores are measured before the command: `TestTreeBudget_ResetBranchIsBounded`, `TestTreeBudget_InPlaceRestoreIsBounded` (red: both fail at the base); the restore's own `reset --hard` of a moved HEAD was codex round 3's find. **Merges were not** (corrected in review round 1): each side was measured, never the union a merge leaves, so a result reached ~2x every limit and two merges from one base walked `main` over the budget — now the merge's result tree is measured (review round 1, ruling 1). `CommitFilesOnto`'s private-index `read-tree` (no `-u`) writes only an index of a bound, already-measured commit. |
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
   nothing, starts no extra process and keeps every argv (the argv itself is pinned by the
   existing `TestRefGrammar_ArgvShape`, which passes unchanged; `TestTreeBudget_OffStartsNoProcess`
   and `TestMerge_NoBudgetRunsNoMergeTree` only assert that no `ls-tree` / `merge-tree` runs —
   corrected in review round 1). `spine serve` sets the
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
  `Max`, `Seen` for the message (it never contains the sha). The measure requires the repository to
  carry `git.NeutralCheckoutAttributes` last in `info/attributes` (`git.ErrNeutralAttributesMissing`
  otherwise); SMP's own writer must write exactly that constant (TASK-018). Measure a commit or tree id
  SMP already resolved, so the measured object is the one it then checks out. For a merge, check
  its RESULT: after switching to the target, compute it with `git merge-tree --write-tree
  <target-tip> <source>` (git >= 2.38) under `GIT_ALLOC_LIMIT` and `TreeBudget.Check` that tree —
  the client path does exactly this (review rounds 1-2).
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

### Review round 1 (2026-10-26; security + correctness lenses on `9263fbd1`; 0 P1)

| Ruling | Finding | What was done | Tests (mutation / red) |
|---|---|---|---|
| 1 | P2 (both): merges measured by side, never by result — ~2x each limit; two admitted merges from one base push `main` over budget and everything wedges, deletes can't repair it | `treebudget_merge.go`: `checkMergeBudget` computes the result with `git merge-tree --write-tree --no-messages --name-only` on the target tip read **inside the working-tree hold** the merge already takes (`lockTree`; a governed merge's `WithWorkingTree` sequence) and measures that tree on all three limits before the switch; `sameMergeBase` re-reads HEAD after the switch and refuses (transient) a tip that moved. The target is measured alone only when the switch to it is a checkout (HEAD elsewhere); a result equal to the target's tree (up-to-date replay) is not measured; the source is never measured alone (it is never what a merge writes; on a fast-forward it *is* the result). Wired into `Merge` and `mergeSHA` (MergeBound / Merge with SourceSHA). Ordering guarantee: every in-process writer of a tree takes the same per-path hold, so a second merge waits for the first and computes on its result (`TestMerge_ConcurrentMergesSeeEachOther`); an out-of-process writer is caught by the post-switch tip check, except one that moves the checked-out branch between that check and `git merge` (KNOWN-LIMITATIONS). `git.CheckBudgetGitVersion` (>= 2.38); `spine serve` refuses to start below it. Startup warning: `workspace.WarnIfOverCheckoutBudget` measures HEAD once — at `spine serve` start (single mode) and when a shared-mode workspace's services are built — in a goroutine, and logs one Warn naming the limit, its variable and the remedy. KNOWN-LIMITATIONS "Bound", new "A merge is measured by its result", "Not atomic…", "An over-budget home branch…" rewritten; self-review row 5 corrected. | `TestMerge_ResultTreeIsMeasured` (correctness repro, Merge + MergeBound: two 61-entry branches from one base, MaxEntries 100 → second refused, main ≤ 100, no `d2/`, tree clean) — **red at `9263fbd1`** ("err = <nil>, want ErrTreeTooLarge", both paths; `red-rr1.log`); `TestMerge_DisjointSidesAreMeasuredTogether` (security repro: two disjoint 60 KiB sides, 64 KiB → refused; and path bytes); `TestMerge_RepairOfAnOverBudgetMainIsAdmitted` (121-entry main, MaxEntries 100: growing merge refused, up-to-date replay admitted, deleting merge admitted → ≤ 100) — the disjoint-sides, repair and concurrent tests also fail at `9263fbd1`; `TestMerge_SwitchToAnOverBudgetTargetIsMeasured`; `TestMerge_ConcurrentMergesSeeEachOther`; `TestMerge_TargetMovedAfterTheMeasureIsRefused` (both paths); `TestMerge_NoBudgetRunsNoMergeTree`; `TestCheckBudgetGitVersion`; `TestWarnIfOverCheckoutBudget`. Mutations, each caught: drop the result measure (6 tests fail), measure the target always (repair fails), drop the switch measure (2 fail), drop the up-to-date skip (repair fails), drop the moved-tip check (moved-tip fails). |
| 2 | P2: `.gitattributes` heuristic (substring match; 188x above 64 files) | `treebudget_attrs.go`: every `.gitattributes` read in one `git cat-file --batch` (read bounds applied) up to 1 MiB of attribute text (each file at size + 64); parsed: comments, C-quoted patterns, `-a`/`!a`/`a=false`-style values ignored, whole names only (`text`, `text=auto`, `eol=crlf`, legacy `crlf`, `ident`, `working-tree-encoding` ≠ UTF-8, `filter`), `[attr]` macros expanded recursively (depth 8, cycles cut; every definition), built-in `binary`. Past the cap or for a symlinked `.gitattributes` → worst case with the reason. A `filter=<name>` whose smudge/process driver the git config defines → refused at any limit (`Expansion` +Inf), read with the clone's own environment for a budgeted clone (`measureEnv`). The refusal names the stored size, the factor and the attributes (`TreeTooLargeError.Stored/Reason`); the hook names the size it needs. | `TestTreeBudget_AttributeParsing` (26 cases incl. the "accidentally" comment, macros, cycles, quoted patterns, unsets), `TestTreeBudget_ReviewReproductionsAreAdmitted` (65 dirs of `* text=auto` → x2 admitted; "accidentally" + `*.bin -text` → x1 admitted — **red at `9263fbd1`**: refused "up to 188x" and "up to 23.5x"), `TestTreeBudget_AttributeFilesTheScanCannotReadAreWorstCase` (70 harmless files read; > 1 MiB and symlink → worst case + reason), `TestTreeBudget_RefusalNamesTheFactor`, `TestTreeBudget_FilterDriverIsUnbounded`, `TestTreeBudget_AttributeExpansionIsCounted` (real `ident`, UTF-32 refused; `eol=crlf` x2 admitted) unchanged. |
| 3 | P3: `limits.go` path-budget comment false; tree object > `GIT_ALLOC_LIMIT` unmeasurable | Comment corrected (one NUL per entry; ~16.2 MiB listing at both defaults; N-6); 16 MiB kept (OQ1). The alloc refusal now returns `TreeObjectTooLargeMessage` (says raising `SPINE_GIT_TREE_*` does not help; the allocation limit is 4x the per-blob limit; no `spine serve` knob, `WithMaxBlobBytes` / `WithMaxGitAllocBytes` for embedders); residual in KNOWN-LIMITATIONS. | `TestTreeBudget_TreeObjectOverTheAllocLimitSaysWhatItIs` |
| 4 | P3: hook refuses blob tags; dedupe by commit id | `tipTrees`: one `git cat-file --batch-check` types every tip (`<oid>`, `<oid>^{}`, `<oid>^{tree}`); commit or tag-of-commit → its tree measured once per tree id; blob/tree tips admitted. | `TestGitHookPreReceive_AdmitsWithinBudget` (raw blob + tag of a blob), `TestBudgetHook_TagsArePeeledAndTreesMeasuredOnce` (`--tags` push with a GPG-key blob tag through the handler; 5 tags + a branch of one commit → 1 measure) |
| 5 | P3: hooksPath override silent | `warnOverriddenHooks`: once per repo per handler, `git rev-parse --git-path hooks` (repo/global/system `core.hooksPath`) vs the pinned dir → one Warn naming the executable, non-sample hooks that will not run. | `TestBudgetHook_WarnsOnceWhenHooksPathIsOverridden` |
| 6 | P3: refused clone refetched on every access | `gitpool`: an `ErrTreeTooLarge` clone failure → `domain.ErrPayloadTooLarge` (permanent, wraps the git error), remembered for 10 min keyed by clone URL + process budget; expired entries dropped; another remote or a changed budget is a fresh attempt. | `TestClient_CheckoutBudgetRefusalIsRememberedForATime` (3 accesses → 1 clone; after TTL, after a budget change, another remote → new attempts) |
| 7 | P3 docs | README env table, `.env.example` (4 vars), `docs/git-push-guide.md` §5 (`spine: push refused …`, unmeasurable, `500 push gate unavailable`), this artifact's Compatibility and SMP impact (library opt-in; default-on only in `spine serve`), integration guide §7 (blob tips, hooksPath Warn, git >= 2.38, startup Warn), CHANGELOG. | — |
| 8 | P3 (security P3-2): clone fetch neither size-bounded nor deadlined | **Documented, not changed**: git has no fetch-side `maxInputSize`, and a fixed deadline on the network part would fail legitimate large clones (the mutate deadline is 2 min). KNOWN-LIMITATIONS names the operator controls (code-repo base on its own filesystem/quota, egress policy, who may register code repos) and the refusal cache. | — |
| 9 | false claims | Deviation 1 now cites `TestRefGrammar_ArgvShape`; the limits.go entry figures say which count (1,961 budget entries / 21,420,845 bytes for Spine HEAD, 1,624 files at a560c210). | — |

**Handoffs (recorded, not implemented):**
- **TASK-004:** collides in `cli.go` `runSink` (deadline block; env moved to `commandEnv`), the
  `opClasses` map (gofmt realigned every line; `measure_tree` added), the `limits.go` const block
  after `minSinkGrowth` and its header. It should NOT reuse `treeListingSink` as is (`ls-tree -l`
  specific, keeps no paths): move its NUL-record core — bounded carry, stop + cancel on the first
  refusal, unterminated-output check — into `listing.go` and build both on it (per-record caps
  differ: 4096 vs the remaining path budget). Its kept-bytes / listing cap must be >= the path
  budget + one byte per entry + a margin, or be derived from the budget (the 16 MiB path default
  plus 200,000 NULs is ~16.2 MiB). Its premise "budget-bounded listings" does not hold for SMP
  (the budget is opt-in there).
- **TASK-005:** collides in `handler.go` `ServeHTTP` (hook install before `prereceive`, the CGI
  `Env` append), `NewHandler` (`newBudgetHook`) and the `Handler` struct (`budgetHook`,
  `hooksWarned`); `ensureReceivePackConfig` is untouched. The hook is a fresh process whose CGI
  environment is built from scratch and that carries only budget flags (no DB, actor or run), so
  N-9 needs a callback channel; per-push data must go through `cgiHandler.Env`, because the
  per-repo script is shared and rewritten on every push (concurrent pushes would race on it).
  Moving the semaphore slot after the command read would put the hook install outside the slot.

**Commits (review round 1):** `30af374b` (rulings 1–9), `1480414c`, `3a3d15b3`, `366fc202` (codex
below), `04cde224` (gosec G118: the start-of-life warning takes a detached context), and the notes
commit.

**Codex after review round 1** (`codex review --base init/INIT-024-scaffold`, plain; `gpt-6.1-sol`
confirmed in each review sub-session; 3 rounds, the maximum):

| Round | Review session | Findings (codex's rating) | Disposition |
|---|---|---|---|
| A1 | `01a17876-bdfa-7482-9465-5d7bc1a088df` | P1 a fan-out self-referencing macro (`[attr]loop loop …`, 20 ways) cost ~20^8 visits in Go, beyond any timeout; P1 the macro depth limit (8) hid the end of a longer chain (`ident`, a filter); P1 attributes past the scan cap could name a configured smudge driver, which the finite worst case does not bound | All real, fixed `1480414c`: macros are a worklist, each expanded at most once, no depth limit; past the cap (or a symlinked file) with any smudge/process driver configured on the host → refused (+Inf). `TestTreeBudget_MacrosAreExpandedOnceAndFully`, `TestTreeBudget_UnscannedAttributesWithAFilterDriverAreRefused` — **red at `30af374b`** ("did not finish in 5 s"; "err = <nil>") |
| A2 | `01a1787a-6980-7dc3-a929-8c5fcbe21b72` | P1 `* ident\0hidden` sets ident in git (C-string line) but parsed as an unknown attribute: 480,000 bytes written under a 100,000-byte budget | Real, fixed `3a3d15b3`: a NUL ends the line and the scan reads on (superset of git's blob reader, which stops the file, and its worktree reader, which stops the line). Parsing cases + an end-to-end `nul-terminated` case in `TestTreeBudget_AttributeExpansionIsCounted` — **red at `1480414c`** ("admitted a tree that wrote 188155 bytes") |
| A3 | `01a1788a-c263-7111-9407-715dde9221af` | P1 a macro named like a built-in attribute (`[attr]text ident` + `* text`) is expanded by git but was only classified | Real, fixed `366fc202`: every set name with a definition is expanded, whatever it also means. With it, checked against git 2.54: CR is a blank between pattern and attributes (`*\rident` sets ident — git wrote 48 bytes for `$Id$\n`), and an unclosed quoted pattern is split at the first blank (git drops the line; counting it only over-estimates). Cases in `TestTreeBudget_AttributeParsing` and `built-in-named macro` end to end — **red at `3a3d15b3`** ("wrote 188164 bytes"; "factor 2, want 23.5"; `*\rident` 1; filter behind the macro not found). No fourth codex round (the limit); these last changes were not codex-reviewed. |

**Adversarial note (attribute parser vs git) — withdrawn in review round 2.** It claimed the scan
was a superset of what git applies. It was not: a bare `[attr]` line (a glob pattern to git) and a
quoted pattern git fails to unquote were still read differently, and a merge was measured under the
current working tree's attributes. The scanner was removed (Review round 2).

**Gates after review round 1** (all at `04cde224`; heavy legs under the shared heavy-suite lock):

| Gate | Result |
|---|---|
| lint-boundaries, `make build`, embed-smoketest, `go vet` plain / integration / scenario | rc=0 |
| `go test -p 4 ./...` | rc=0, no failures |
| `scenario-git-http` (`TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*`), own DB | rc=0 (87 / 26 / 130 passes, 0 skips) |
| integration (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `core/artifact`, `core/workflow`) | rc=0 |
| `-race`: `adapters/git`, `githttp`, `cmd/spine` (ci.sh), `gitpool`, `workspace` (separately) | rc=0 |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`): both CI steps, the githttp/cmd/gitpool/workspace budget tests, and a verbose run of `TestMerge_*` (`merge-tree --write-tree` on 2.39.5), `TestCheckBudgetGitVersion`, the attribute tests | STEP1 / STEP2 / EXTRA / EXTRA2 rc=0 |
| golangci-lint v2.11.4 (touched packages incl. `gitpool`, `workspace`) vs the scaffold | 0 new (one new gosec G118 fixed in `04cde224`); `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files | clean |
| SMP compatibility gate (`../spine` at `04cde224`; only this artifact uncommitted) | build and vet x4 rc=0; `-race` §5 packages: only the two by-design `gitread` contract tests; testgate runtime PASS (2,143 passed) and githttp PASS (588 passed); no `ws_*` / `s24003_*` databases left. No SMP change. |

**Not run (review round 1):** a fourth codex round (the limit is three); GitHub CI (nothing is
pushed); the full `make test-scenario` with a registry DB (command as above).

### Review round 2 (2026-10-27; delta review of `1a2a8898`: 3 P1) — design change

**Deviation 11 (design change, ruled by the coordinator on the reviewer's recommendation): the
attribute scanner is removed and checkouts are made attribute-neutral instead.** The scanner tried
to predict how git would expand a tree's content from its `.gitattributes`. Each round found inputs
it read differently from git — codex rounds A1–A3 five, and this review two more (P1-A: a bare
`[attr]` line is a glob pattern to git; P1-B: a quoted pattern git fails to unquote splits
elsewhere) — and it could never see the operator's attribute files or config. So enforcement no
longer predicts git: `git.NeutralCheckoutAttributes` (`* -text -ident -filter !eol
!working-tree-encoding !conflict-marker-size`), the last line of `$GIT_COMMON_DIR/info/attributes`
(highest precedence), turns every content conversion off for every checkout, merge and linked
worktree, and the measure is plain stored bytes. `!merge` is deliberately not in the line: it would
change `binary` and `merge=union` results (`TestNeutralAttributes_MergeDriversUnchanged`).

| Ruling | What was done | Tests (red at `1a2a8898` / mutation) |
|---|---|---|
| 1 neutral attributes | `adapters/git/neutral_attributes.go`: `NeutralCheckoutAttributes`; `EnsureNeutralCheckoutAttributes` (locates the common dir, keeps operator lines in order, moves an earlier copy of the line to the end, idempotent — no rewrite when already last — atomic temp+rename, refuses a non-regular file / link); `HasNeutralCheckoutAttributes`; the measure's `requireNeutralCheckoutAttributes` (fail closed, permanent `ErrNeutralAttributesMissing`; the LAST attribute line must be the neutral one — comments and blanks after it are fine). Called by `spine serve` at start (single mode), in `buildServiceSet`, on every receive-pack POST (`installBudgetHook`, so the hook's measure and the served repo agree), in a budgeted `Clone` between `--no-checkout` and `reset --hard`, and by the gitpool when it reuses a clone in a budgeted process. The library stays opt-in: nothing writes the file unless the host calls the writer (or turns a budget on, which makes the measure require it). | `TestNeutralAttributes_EveryShapeWritesStoredBytes` — 17 shapes (the reviewer's combo; `ident`; `[attr]` as a pattern, with space and tab; `[attr]` + UTF-32; both failed-unquote shapes; NUL; CR; macro named like a built-in; 12-macro chain; UTF-32; `text eol=crlf`; `crlf`; `filter=` with a required host driver; attributes past 1 MiB; host config only) under a hostile global config (`core.attributesFile` `* ident text eol=crlf`, `core.autocrlf`, `core.eol=crlf`, `filter.x.smudge` `required=true`, diff3): a budgeted linked worktree and a budgeted main checkout each write exactly the measured stored bytes, on git 2.54 and 2.39.5 — **red at `1a2a8898`**: every shape wrote 98 KB–393 KB for ~10 KB stored, or was refused outright (`red-r2.log`); `TestNeutralAttributes_MergeDriversUnchanged`; `TestNeutralAttributes_EnsureKeepsOperatorLines`; `TestNeutralAttributes_MeasureFailsClosedWithoutTheLine`; line present after a budgeted clone, a push, a reused pool clone. Mutations: writer no-op → 12 tests fail; fail-closed check removed → `…MeasureFailsClosedWithoutTheLine` fails. |
| 2 delete the scanner | `treebudget_attrs.go` deleted (356 lines); the listing sink's attribute fields, `TreeStats.Expansion/ExpansionReason/AttributeFiles`, `TreeTooLargeError.Expansion/Stored/Reason`, `bytesOver`, the `+Inf` host-filter refusal, `filterDrivers` and `measureEnv` removed; the measure is stored bytes, factor 1. The scanner's own tests were deleted (`AttributeParsing`, `…ScanCannotReadAreWorstCase`, `RefusalNamesTheFactor`, `FilterDriverIsUnbounded`, `MacrosAreExpandedOnceAndFully`, `UnscannedAttributes…`) or folded into the neutral shape table (`AttributeExpansionIsCounted`, `ReviewReproductionsAreAdmitted`, the codex A1–A3 shapes). | — |
| 3 P1-C | `checkMergeTarget` (target tip; the target's tree when the switch is a checkout) runs before `switchTo`; `checkMergeResult` (merge-tree) runs AFTER it, in the working tree `git merge` sees, and the tip is re-checked after the switch and again after the measure. merge-tree and the measured `git merge` run under `GIT_ALLOC_LIMIT` = max(`MaxTotalBytes`, the read allocation limit) — 2 GiB by default — and an allocation refusal is a budget refusal (`MergeAllocRefusedMessage`). **Deviation 12:** the ruling said to reuse the existing tree-object limit (16 MiB); measured on git 2.54, `git merge` of a packed 20 MB file fails under it ("attempting to allocate 20000001 over limit 16777216 … packed object … is corrupt"), and `spine serve` has no setting to raise it, so the merge limit is the budget's own byte limit, floored at the read limit: no allocation a merge within the budget needs is larger than the bytes the budget admits. | `TestMerge_ConvergenceMarkersAreBoundedByTheNeutralLine` (HEAD main, run branch with `* conflict-marker-size=100000`, conflicting sides, 64 KiB budget: markers are 7 characters and the conflicted files stay within the budget — **red at `1a2a8898`**: 100,000-character markers); `TestMerge_ResultIsComputedInTheTargetsWorkingTree` (target-only `merge=union`: the measured tree is the committed tree, both paths — **red at `1a2a8898`**, and the "result before the switch" mutation fails it); `TestMerge_RunsUnderTheMergeAllocLimit` (a PATH wrapper records `GIT_ALLOC_LIMIT` for merge-tree and merge; none without a budget); `TestMerge_AllocRefusalIsABudgetRefusal` (a ~1.3 MB file changed on both sides under a 1 MiB limit). Mutation: dropping the limit fails both alloc tests. `TestMerge_TargetMovedAfterTheMeasureIsRefused` now moves the tip during the result measure and is caught by the second check. |
| 4 docs | KNOWN-LIMITATIONS "Checkout Budget" rewritten ("Checkouts write stored bytes", "A merge is measured by its result" with the order and the allocation limit, the attribute bullet deleted, "Not atomic" names an out-of-process `info/attributes` edit); CHANGELOG "Changed (operators)": stored-byte checkouts (LFS pointers stay pointers), commit-side normalisation off, 7-character markers, dirty-once on upgrade, operator `merge.<name>.driver` and `pull.twohead` not neutralised; embedder API list updated; integration guide §7 and env table, git-push guide, architecture doc; code comments (`treebudget.go`, `treebudget_merge.go`, `neutral_attributes.go`). | — |

**Handoffs (re-recorded after the deletion; these replace the round-1 handoffs):**
- **TASK-004:** collides in `cli.go` `runSink` (the mutate-deadline block, the merge
  `GIT_ALLOC_LIMIT` env and its refusal mapping; read env moved to `commandEnv`), the `opClasses`
  map (`measure_tree` added; gofmt realigned it), the `limits.go` const block after `minSinkGrowth`
  and its header. `treeListingSink` is now attribute-free (`budget`, `stop`, `stats`, `carry`,
  `delivered`, `over`, `parseErr`): do not reuse it as is (it parses `ls-tree -l` and keeps no
  paths); move its NUL-record core — the bounded carry, stop + cancel on the first refusal, the
  unterminated-output check — into TASK-004's `listing.go` and build both on it (per-record caps
  differ: 4096 vs the remaining path budget). Its kept-bytes / listing cap must be >= the path
  budget + one byte per entry + a margin, or be derived from the budget (the 16 MiB path default
  plus 200,000 NULs is ~16.2 MiB). The budget is opt-in for SMP, so "listings bounded by the
  budget" does not hold there.
- **TASK-005:** collides in `handler.go` `ServeHTTP` (hook install — which now also writes the
  neutral attributes — before `prereceive`, the CGI `Env` append), `NewHandler` (`newBudgetHook`)
  and the `Handler` struct (`budgetHook`, `hooksWarned`); `ensureReceivePackConfig` is untouched.
  The hook is a fresh process with a from-scratch CGI environment and only budget flags (no DB,
  actor or run): N-9 needs a callback channel, and per-push data must go through `cgiHandler.Env`,
  because the per-repo script is shared and rewritten on every push. Moving the semaphore slot
  after the command read would put the hook install outside the slot.
- **TASK-018 (SMP bump/adoption):** SMP INIT-027 EPIC-005 TASK-011 writes an equivalent line into
  SMP's pool clones with its own writer, which replaces the file whenever it differs (and its line
  lacks `!conflict-marker-size`, SMP P2-1). TASK-018 must make Spine's exported
  `git.NeutralCheckoutAttributes` the single source of the line, and make sure the two writers
  never manage the same file (SMP's replace-on-difference writer and Spine's append-and-keep
  writer would otherwise flip-flop). Until then a Spine budgeted measure on an SMP clone whose file
  ends in SMP's line refuses (`ErrNeutralAttributesMissing`) — SMP does not call it today.

**Codex budget.** This is a new design, so it gets a fresh codex budget of at most 2 rounds
(deliberate, per the coordinator's ruling); rounds A1–A3 above reviewed the scanner, which no
longer exists.

**Commits (review round 2):** `87bc4680` (rulings 1–5) and this notes commit. Net for the round
(`1a2a8898..87bc4680`): production Go +453 / −567 (−114; the 356-line scanner deleted, the
~200-line neutral-attributes writer added), tests +613 / −363, docs the rest (+1,186 / −976 in all).

**Codex (fresh budget for the new design, at most 2 rounds; `gpt-6.1-sol` confirmed):**

| Round | Review session | Findings | Disposition |
|---|---|---|---|
| B1 | `01a17cdf-ef4c-7cf2-afef-6c57f85cbb74` | none ("No actionable regressions were identified"; it ran the adapters/git, gitpool, githttp, workspace and cmd/spine tests) | clean at `87bc4680`; no B2 needed |

**Gates after review round 2** (all at `87bc4680`; heavy legs under the shared heavy-suite lock):

| Gate | Result |
|---|---|
| lint-boundaries, `make build`, embed-smoketest, `go vet` plain / integration / scenario | rc=0 |
| `go test -p 4 ./...` | rc=0, no failures |
| `scenario-git-http` (`TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*`), own DB | rc=0 (87 / 26 / 130 passes, 0 skips) |
| integration (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `core/artifact`, `core/workflow`) | rc=0 |
| `-race` (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `workspace`) | rc=0 |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`): both CI steps, the budget/hook tests, a verbose run of `TestMerge_*`, `TestCheckBudgetGitVersion` and `TestNeutralAttributes_*` (every shape: written == stored on 2.39.5) | STEP1 / STEP2 / EXTRA / EXTRA2 rc=0 |
| golangci-lint v2.11.4 vs the scaffold | 0 new (3 new test-file issues fixed before the commit); `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files | clean |
| mutations (`go test -overlay`, worktree untouched): writer no-op; fail-closed check removed; result measured before the switch; merge `GIT_ALLOC_LIMIT` removed | each caught (12 tests; `…MeasureFailsClosedWithoutTheLine`; `…ResultIsComputedInTheTargetsWorkingTree`; both alloc tests) |
| red at `1a2a8898` (`git archive` + a compile-only shim, `red-r2.log`) | `TestNeutralAttributes_EveryShapeWritesStoredBytes` (17/17 subtests: 98–393 KB written for ~10 KB stored, or refused), `TestMerge_ConvergenceMarkersAreBoundedByTheNeutralLine` (100,000-character markers), `TestMerge_ResultIsComputedInTheTargetsWorkingTree` (both paths) |
| SMP compatibility gate (`../spine` at `87bc4680`, clean) | build and vet x4 rc=0; `-race` §5 packages: only the two by-design `gitread` contract tests; testgate runtime PASS (2,143) and githttp PASS (588); no `ws_*` / `s24003_*` databases left. No SMP change. |

**Not run (review round 2):** a second codex round (B1 was clean); the system gitattributes file
and system git config (the hostile-config test uses the global ones; the line's precedence is the
same over both); `pull.twohead` (Spine never pulls); GitHub CI (nothing is pushed); the full `make
test-scenario` with a registry DB.

### Review round 3 (2026-10-27; confirming review of `37103f52`: redesign holds, 0 P1/P2)

| Ruling | What was done | Tests (red at `37103f52` / mutation) |
|---|---|---|
| 1 P3 upgrade | `CLIClient.EnsureNeutralWorkTree`: the writer, and — only when it adds the line to a repository with a work tree, an existing HEAD and tracked differences — HEAD's tree measured against the client's budget, an untracked-obstruction check, the tracked state quarantined (`git stash create`, an unreferenced commit, logged at Warn — what the shared tree's restore already does), then `reset --hard`. It takes the tree's hold only when it changes the file. Used for every pre-existing work tree: `spine serve` start (single mode), `buildServiceSet`, the gitpool's reuse of a clone, the git HTTP endpoint per push; a fresh budgeted clone keeps the plain writer (nothing is checked out yet). State a reset could lose: code-repository clones carry no uncommitted work between Spine's operations (a conflicted code-repo merge is aborted at once, `multi_repo_merge.go`), and anything tracked is quarantined, not discarded. Checked: the shared tree's restore does cover the home-branch case on its own (`TestNeutralAttributes_SharedTreeRestoreRenormalises`, green at `37103f52` too); code-repository clones have no home branch, so no restore — that was the defect. The CHANGELOG upgrade bullet now says exactly which trees are brought to stored bytes, when. | `TestClient_ReusedConvertedCloneIsRenormalised` (gitpool; `* text eol=crlf`, and the clone's own `core.autocrlf`; first merge after the upgrade succeeds) — **red at `37103f52`**: "Your local changes … would be overwritten by merge" (both); `TestNeutralAttributes_EnsureNeutralWorkTreeRenormalises` — **red**: "M a.txt" (both). Mutation (no reset): both fail. |
| 2 P4 blank rules | `isNeutralLine` trims only git's blanks (space, tab, CR); `endsNeutral` treats a line as blank or a comment only after those blanks. | `TestNeutralAttributes_BlankRulesAreGits` (VT-/NBSP-led line after the line, VT-/NBSP-prefixed line, FF-suffixed line, VT-led "comment": each refused by the measure and repaired by Ensure; git-blank variants accepted) — **red at `37103f52`** for the four shapes `bytes.TrimSpace` accepted. Mutation (TrimSpace back): fails. |
| 3 P4 nits | `limits.go` comment rewritten (stored bytes; the merge allocation limit); CHANGELOG commit side adds `-filter` (no clean filter on add; an LFS path written through Spine on a git-lfs host is stored as its full content) and `!working-tree-encoding` (no re-encoding on add); serve-start Ensure only in single mode (shared mode: `buildServiceSet` per workspace); the writer refuses a directory that is not the top of its repository (`requireRepositoryTop`: `--show-toplevel`, or `--absolute-git-dir` for a bare repository), so it never writes into a repository that merely contains the cwd; the measured `git merge` runs with `LC_ALL=C` beside its `GIT_ALLOC_LIMIT`. | `TestNeutralAttributes_EnsureWritesOnlyAtTheTop` — **red at `37103f52`** (the parent repository's file was written). Mutation (top check off): fails. |
| 4 P4 merge memory | KNOWN-LIMITATIONS: `GIT_ALLOC_LIMIT` caps single allocations, not the git child's RSS (~10x a large file changed on both sides; pre-existing); operator controls: the server's memory limit, a byte budget whose ~10x fits it, keeping very large files out of merged branches. | — |

**Codex after review round 3** (`codex review --base init/INIT-024-scaffold`, plain; the last
round of the fresh budget; model `gpt-6.1-sol` confirmed in the review sub-session):

| Round | Review session | Findings | Disposition |
|---|---|---|---|
| B2 | `01a17dde-73be-7e30-a87d-23ac02375186` (on `69aa1988`) | P2 "Retry unfinished worktree renormalization": a renormalisation that failed after the line was written (HEAD over the budget, a cancelled request) was never retried — every later call found the file ending in the line and returned success, leaving the tree converted | Real, fixed `5636c02d`: the work tree is marked (`spine-renormalise-pending` in its own git directory, created `O_EXCL` before the line is written; never in a bare repository) and the mark is cleared only once the tree is at its stored bytes, found clean, or has an unborn HEAD; a marked tree is renormalised by the next call under the tree's hold, rechecking the mark after the wait. Follow-on `e57a6741`: the retry would have made the git HTTP endpoint refuse every push to a served tree whose renormalisation cannot finish yet (the push that would repair it included), though the hook's measure needs only the line; errors after the line is in place now match `git.ErrRenormalisePending` (the cause still matches) and the hook install warns and goes on for them; the gitpool's reuse and the workspace build keep reporting them. Caller log messages and CHANGELOG / KNOWN-LIMITATIONS / package comment updated. |

Tests: `TestNeutralAttributes_FailedRenormalisationIsRetried` (adapters/git: first call refused on
an over-budget HEAD as `ErrRenormalisePending`, refused again while still over, then a client whose
budget admits HEAD renormalises and the merge succeeds; afterwards a call with the tight budget is a
no-op and leaves a fresh edit alone) — **red at `69aa1988`** ("a retry while HEAD is still over the
budget: want the refusal again, got <nil>"; run before the sentinel existed, without its
`errors.Is` check); `TestBudgetHook_PendingRenormalisationDoesNotStopThePush`
(githttp: a served work tree with an untracked file where a tracked directory was — pushes land and
warn while pending; once the file is gone the next push restores the tree and stops warning) —
**red at `5636c02d`** ("push 0 with the renormalisation pending was refused"). Mutations (`go test
-overlay`, worktree untouched): marker never written — both tests and
`…EnsureNeutralWorkTreeRenormalises` fail; marker cleared before renormalising — both fail; the
`ErrRenormalisePending` wrap removed — both fail. No further codex round (B2 was the last of the
budget); the two fixes are covered by the red runs and mutations above.

**Gates after review round 3** (all at `e57a6741`; heavy legs under the shared heavy-suite lock):

| Gate | Result |
|---|---|
| lint-boundaries, `make build`, embed-smoketest, `go vet` plain / integration / scenario | rc=0 |
| `go test -p 4 ./...` | rc=0, no failures |
| `scenario-git-http` (`TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*`), own DB | rc=0 (87 / 26 / 130 passes, 0 skips) |
| integration (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `core/artifact`, `core/workflow`) | rc=0 |
| `-race` (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `workspace`, `core/worktreefs`) | rc=0 (also run before each commit) |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`): both CI steps, the budget/hook tests (incl. `TestBudgetHook_PendingRenormalisationDoesNotStopThePush`), a verbose run of `TestMerge_*`, `TestCheckBudgetGitVersion`, `TestNeutralAttributes_*` (9 tests, incl. `…FailedRenormalisationIsRetried`) | STEP1 / STEP2 / EXTRA / EXTRA2 rc=0 |
| golangci-lint v2.11.4 on the touched packages | the same findings as at round 2 (0 new vs the scaffold); `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files | clean |
| `TestNoUnconfinedWorktreeIO` | passes (allowlist: the marker's three helpers — a fixed name in git's own directory) |
| SMP compatibility gate (`../spine` at `e57a6741`; its one uncommitted file was this artifact) | build and vet x4 rc=0; `-race` §5 packages: only the two by-design `gitread` contract tests (as at round 2); testgate runtime PASS (2,143) and githttp PASS (588); no `ws_*` / `s24003_*` databases left. No SMP change. |

**Not run (review round 3):** a codex round on `5636c02d` / `e57a6741` (the budget ended at B2);
a crash between the marker and the line on a real host (the order — marker first — is what a
crash relies on; the retry itself is tested); a pre-existing linked worktree of a converted clone
(Spine's worktrees are its own, made after the line with `--no-checkout` + reset); GitHub CI
(nothing is pushed); the full `make test-scenario` with a registry DB.

### Review round 4 (2026-10-27; delta review of `37103f52..02334613`: 1 P2, 2 P3, 2 P4, all in the renormalisation)

Fixed in `6c71f215` (`adapters/git/neutral_attributes.go`, a lock-order comment in `treelock.go`,
tests, docs), and the codex finding below in `6033df2d`. `treebudget.go` (and its listing code,
which T004 moves) is untouched.

| Ruling | What was done | Tests (red at `02334613` / mutation) |
|---|---|---|
| 1 P2 stat-cache trust | Under the tree's hold, before deciding anything, `forceContentCompare` sets the index file's mtime to 1 s past the epoch (`git rev-parse --git-path index`, Lstat, regular only). git then treats every entry as racily clean (`is_racy_timestamp`: the entry's mtime is not older than the index's) and `git status` compares every tracked file's content under the neutral line; stat-matching files are hashed, not trusted. This is the "equivalent that re-hashes" rather than `read-tree HEAD`: it keeps the index's entries, staged changes and assume-unchanged / skip-worktree flags (a `read-tree HEAD` would drop staged changes; the record is anyway taken before the line, ruling 3), and status's refresh writes the index back with fresh stat data. The marker is cleared only after that comparison (and the rewrite). `--ignore-submodules=all`, `-c core.fsmonitor=false` (an fsmonitor's valid bits would skip the files). | `TestNeutralAttributes_StatCleanConvertedTreeIsRenormalised` (files dated an hour back + `update-index --refresh`, so non-racy; `status` clean under the old attributes): **red at `02334613`** — f.txt still `a\r\nb\r\n`, g.md still `$Id: 093c… $\r\nx\r\n`; green: both stored, marker cleared, and plain.bin (never converted) keeps its inode and mtime (content compared, not a blanket rewrite). Mutation (no mtime change): fails. |
| 2 P3 unborn HEAD | `headUnborn`: HEAD names no commit, `symbolic-ref -q HEAD` names `refs/heads/…`, `show-ref --verify --quiet` of it exits 1 with empty stderr, and `ctx` is not done. Anything else returns the error and keeps the mark (a cancelled request as a transient `GitError` wrapping `ctx.Err()`). Probed on 2.54.0 and bookworm 2.39.5: a branch file that holds garbage or is empty makes `symbolic-ref -q HEAD` fail (128 "No such ref"); one that names a missing object makes `show-ref` exit 128; a real unborn branch gives 0 / 1 + empty. A `for-each-ref` broken-ref check was tried as well and dropped: its mutation survived on both versions (no shape reaches it). | `TestNeutralAttributes_TornHEADKeepsTheMark` (names-no-object and garbage) — **red at `02334613`**: "torn HEAD: want ErrRenormalisePending, got <nil>" (both); `…CancelledHEADProbeKeepsTheMark` (a test hook right before the HEAD probe cancels the request) — **red at `02334613`** with the same hook inserted into the base's `renormalise` (instrumentation only, `zz_r4shim.go`): "got <nil>"; `…UnbornHEADClearsTheMark` (green at both: a real unborn HEAD still clears). Mutations: any failure = unborn — torn and cancelled fail; show-ref check off — "names no object" fails; symbolic-ref check off — "garbage" fails. |
| 3 P3 real changes | Before the line is written (under the old attributes, under the hold), `realChanges` records `git status --porcelain=v1 -z --untracked-files=no` (renames' sources included) into the marker: `spine-renormalise-record v1`, the count, NUL-terminated paths; written as a temp file renamed into place, so a marker is whole or absent. After the line, only paths with `X == ' '` (index matches HEAD), `Y == 'M'` (content differs) and not recorded are rewritten, from the index, with `git checkout-index -f -u -z --stdin` (HEAD's tree measured first); recorded paths, staged changes and untracked files are not touched; one Warn counts the recorded paths. A marker without a valid record (the round-3 build's text marker, or damage) falls back to `git stash create` (SHA at Warn), then rewrites every unstaged difference; staged paths are still never rewritten. Docs: CHANGELOG upgrade bullet rewritten; KNOWN-LIMITATIONS says what each tree kind does (shared tree: converted files rewritten when the line is added, real changes left to its restore as before; clone / served tree: real changes stay, a recorded path keeps its checked-out bytes; linked worktree; no-record fallback; assume-unchanged / skip-worktree trusted). | `TestNeutralAttributes_RealChangesAreLeftAsTheyWere` (racy and long-lived: an unstaged edit, a staged new file, an edit over a staged change, an untracked CRLF file, an assume-unchanged edit) — **red at `02334613`**: real.txt and g.md reset, staged.txt deleted; `TestClient_ReusedCloneKeepsItsRealChanges` (gitpool reuse) — **red**: edit reset, staged file deleted; `TestBudgetHook_RenormalisationKeepsRealChanges` (served tree, per push) — **red**: edit reset, staged file deleted, nothing staged; `…MarkerWithoutARecordQuarantinesFirst` (the round-3 marker shape; the quarantine commit holds the edit) — **red at `02334613`** on the staged path (the base reset it). Mutations: record ignored — the three real-changes tests fail; `X == ' '` guard off — the fallback test fails; quarantine off — the fallback test fails. |
| 4 P4 linked worktrees | When the line is added, `markWorkTrees` records and marks this tree and every non-bare, non-prunable entry of `git worktree list --porcelain -z` (each worktree's own `--absolute-git-dir`; this tree skipped by its canonical git dir). Another worktree that cannot be marked is logged at Warn and left; a linked worktree is renormalised by a call on its own path. | `TestNeutralAttributes_LinkedWorktreesAreMarked` (a linked worktree checked out converted, with an edit) — **red at `02334613`**: "the linked worktree was not marked"; green: a call on it brings it to stored bytes and keeps its edit. Mutation (others not marked): fails. |
| 5 P4 line outside the hold | `EnsureNeutralWorkTree` asks `ensureNeutral` (through `beforeAdd`, under the file's mutex) whether the line is being added; if it is and the tree is not held, it takes the tree's hold and goes again, so the record and the line are written under the hold, and the renormalisation re-enters it. Lock order: tree hold, then the per-file attributes mutex (now one per info/attributes file), documented beside `neutralWriteMus` and beside `treeLocks`; nothing waits for a tree while holding the mutex (other worktrees are recorded without their holds — KNOWN-LIMITATIONS). | `TestNeutralAttributes_LineWaitsForTheTreesHolder` (a sequence holds the tree; the call is seen waiting for it — `TreeRefsForTest` = 2 — while the line is still absent; the sequence's merge over the converted files succeeds; then the tree is renormalised) — **red at `02334613`**: "the line was added while a sequence held the tree"; `-race` on it, the linked and the cancelled tests and the touched packages: clean. Mutation (no hold before the line): fails. |

The round-3 `TestBudgetHook_PendingRenormalisationDoesNotStopThePush` used an untracked file in a
tracked directory's place to keep the renormalisation pending; with ruling 3 that deletion is a
recorded real change and is left as it is, so the test now keeps it pending with a HEAD over the
process budget (then lifted). Test seams: `adapters/git/neutral_export_test.go` (the HEAD-probe
hook, `TreeRefsForTest`, the marker's name).

**Codex on the round-4 delta** (`codex review --base init/INIT-024-TASK-003-r3`, plain; the one
round allowed; model `gpt-6.1-sol` confirmed in the review sub-session):

| Round | Review session | Findings | Disposition |
|---|---|---|---|
| R4 | `01a17eea-1396-7921-a530-8ea97dacce10` (on `6c71f215`) | P1 "Protect edits made after the renormalisation record is written": the record is taken when the line is added; a renormalisation that failed then and was retried later (or a linked worktree's, renormalised by a later call) took every unrecorded difference for a conversion, so an edit made in between was overwritten by `checkout-index -f` without quarantine (reproduced: first call refused on `MaxEntries: 1`, a clean tracked file edited, retry). | Real, fixed `6033df2d`. The call that records the tree and holds it since adds a PROOF to the marker before it rewrites anything: a fingerprint of each conversion-only file (`fingerprints`: the entry identified with `worktreefs.LstatEntry`, following nothing — a regular file `hash-object --stdin-paths`, which under the neutral line hashes its raw bytes; a link its target; other types, absent, unreachable — as the shared tree's phantom bookkeeping does; a path holding a newline gets none and is never proven). A later call rewrites only files whose fingerprint still matches and leaves a file edited since as it is (counted at Warn). A record no call has fingerprinted (a linked worktree's, or a crash right after the line) falls back to the quarantine (`git stash create`, SHA at Warn) before rewriting, as a marker without a record does. Docs: CHANGELOG, KNOWN-LIMITATIONS (retry; linked worktree; no-proof fallback). |

Tests: `TestNeutralAttributes_EditAfterTheRecordIsKept` (first call refused on HEAD over the
budget; then a clean file and a converted file edited; the unrestricted retry keeps both edits byte
for byte, renormalises the untouched g.md, counts `changed_since_recorded=2`, quarantines nothing)
— **red at `6c71f215`**: plain.bin back to "abc", f.txt to "a\nb\n"; `…LinkedWorktreesAreMarked`
now also edits the linked worktree after the line: its call quarantines first and the quarantine
holds the edit — **red at `6c71f215`** (no quarantine). Mutations: proof ignored on a retry —
`…EditAfterTheRecordIsKept` fails; proof not written before the rewrite — fails; an unproven
record rewritten without quarantine — `…LinkedWorktreesAreMarked` fails. No further codex round
(one was allowed); this fix rests on the red runs and mutations.

**Gates after review round 4** (all at `6033df2d`; heavy legs under the shared heavy-suite lock):

| Gate | Result |
|---|---|
| lint-boundaries, `make build`, embed-smoketest, `go vet` plain / integration / scenario | rc=0 |
| `go test -p 4 ./...` | rc=0, no failures |
| `scenario-git-http` (`TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*`), own DB | rc=0 (87 / 26 / 130 passes, 0 skips) |
| integration (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `core/artifact`, `core/workflow`) | rc=0 |
| `-race` (`adapters/git`, `gitpool`, `githttp`, `cmd/spine`, `workspace`, `core/worktreefs`) | rc=0 (also run before each of the two commits) |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`): both CI steps, the budget/hook tests, a verbose `TestMerge_*` / `TestCheckBudgetGitVersion` / `TestNeutralAttributes_*` run (18 tests, all nine round-4 ones included), plus an extra run of the gitpool `TestClient_Reused*` and githttp renormalisation tests (the CI filter does not reach them) | STEP1 / STEP2 / EXTRA / EXTRA2 rc=0; extra: all PASS |
| golangci-lint v2.11.4 on the touched packages | the same findings as at round 3 (0 new vs the scaffold; a `deferUnlambda`, a G118 and a G703 were fixed before the first commit); `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files | clean |
| `TestNoUnconfinedWorktreeIO` | passes (allowlist: the record's writer and reader, the marker's Lstat / remove, the index's time — git-administrative files only; the fingerprints go through `worktreefs`) |
| SMP compatibility gate (`../spine` at `6033df2d`; its one uncommitted file was this artifact) | build and vet x4 rc=0; `-race` §5 packages: only the two by-design `gitread` contract tests (as at rounds 2-3); testgate runtime PASS (2,143) and githttp PASS (588); no `ws_*` / `s24003_*` databases left. No SMP change. |

**Not run (review round 4):** a second codex round (one was allowed; `6033df2d` rests on its red runs
and mutations); a real crash between the record, the line and the proof (the order — record, line,
proof, rewrite, clear — is what a crash relies on; each retry path is tested); a concurrent
non-Spine writer in the tree during the renormalisation (Spine's hold does not stop one); a tree
whose differing paths exceed the 16 MiB command-output bound (`status` / `hash-object` output; the
renormalisation then fails and stays marked — reachable only near the path-bytes budget); GitHub CI
(nothing is pushed); the full `make test-scenario` with a registry DB.

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

**This task.** Merged last (`4f502b7e`, tip `02334613`). Conflicts: `CHANGELOG.md`, and
`service/cmd/spine/serve_builders.go` (`buildGitHTTPHandler`'s config: TASK-006's
`TrustedCIDRsByWorkspace` and this task's `TreeBudget` both kept). `githttp/handler.go` (TASK-006's
per-workspace trust), `pool.go` (TASK-008's bounded build), `serve_cmd.go` (TASK-006, TASK-007),
`KNOWN-LIMITATIONS.md`, the integration guide, README, `.env.example` and the git-push guide
auto-merged. Coordination `55789b28`:
- `buildServiceSet`'s `EnsureNeutralWorkTree` now runs under TASK-008's init deadline (it ran on the
  pool's lifetime context, so a renormalisation waiting for a held tree kept the build past
  InitTimeout); past it the renormalisation stays pending for the next build.
  `TestServicePool_PendingRenormalisationStopsAtTheInitDeadline` (red on the lifetime context).
  KNOWN-LIMITATIONS "Checkout Budget" and this task's CHANGELOG entry name the init deadline.
- `TestPreauth_PendingRenormalisationIsInvisibleBeforeAuthentication`: a pending renormalisation
  (`ErrRenormalisePending` at the build) is invisible to an unauthenticated caller — the missing
  workspace's `401` on `/api/v1` and `/git`, pushes included — while a valid token is served, and no
  unauthenticated push reaches `installBudgetHook`. Red at this task's tip (without TASK-006 and
  TASK-007): six distinct `401` messages.
The phase-2 codex round (`--base main`, below) is the first codex review of `5636c02d` and
`e57a6741`.

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

**Phase 3 (2026-10-28).** Round-4 tip `693fb648` merged as `23330ab7`. The conflicts were
`CHANGELOG.md` and KNOWN-LIMITATIONS "Checkout Budget" (round 4's rewritten text taken, with the
init-deadline case of `55789b28` added back) and this artifact (round 4's notes, then this
section). Rechecks of the `55789b28` tests:
- `TestPreauth_PendingRenormalisationIsInvisibleBeforeAuthentication` still holds on the round-4
  code, unchanged: HEAD over the budget leaves the renormalisation pending, with the marker.
- `TestServicePool_PendingRenormalisationStopsAtTheInitDeadline`: its property still holds (the
  workspace is served while another operation holds the tree; red on the lifetime context). Its
  final assertion expected the line and the marker after that build, but since round 4 the line is
  added only under the hold. Coordination `31556f1f` now asserts that nothing changes under the
  holder (no line, a.txt still converted) and that the next build, once the hold is released,
  adds the line, restores the stored bytes and clears the marker.

Codex round 1 (`codex review --base init/INIT-024-integration-a-p2`, session
`01a181fe-f004-7ae0-bacb-8d4c6ed131cf`, `gpt-6.1-sol`, on `31556f1f`) covered this task's
`6c71f215`/`6033df2d` and found two P2s in `neutral_attributes.go`, both fixed here on the integration branch:
- `ed2f3ff1`: the fingerprints passed paths to `hash-object --stdin-paths`, one per line. git
  C-unquotes a line that starts with `"`, drops a CR before the newline, and has no NUL-separated
  form (git 2.39 and 2.54). So a leading quote failed the renormalisation or hashed another file,
  a trailing CR could not be opened, and a newline was never fingerprinted. Paths are now
  `hash-object -- <path>...` arguments. `TestNeutralAttributes_EveryPathShapeIsFingerprinted`
  covers a leading quote, a quoted alias of another file, a backslash, a quoted octal escape, a
  newline, a trailing CR, a leading dash and a name that is not UTF-8 (APFS refuses that one; it
  passed on Linux in git-floor). Red on the previous code: five shapes.
- `d200cec3`: the marker is untrusted input. Counts are plain digits, at most 2,097,152 and no more
  than the rest of the file can hold; fields are non-empty; storage grows with the fields read;
  any violation is "no record", which means the quarantine fallback. The writer refuses past the
  same bound. This is the only place the marker is parsed.
  `TestNeutralAttributes_RecordParserRefusesDamage` (19 shapes, the bound, the writer) and
  `TestNeutralAttributes_DamagedMarkerFallsBackToTheQuarantine`. Red on the previous code: two
  panics ("makeslice: cap out of range"), four damaged shapes accepted, the end-to-end call
  panicked.

Codex round 2 (`codex review --base 31556f1f`, session `01a1820e-b16a-7a71-b867-fe877c024998`,
`gpt-6.1-sol`, on `d200cec3`), the last round, found one P2: the batches of `ed2f3ff1` were
bounded by 128 KiB of raw bytes, past Windows' 32,767-unit command line.
- `a33e399c`: `fingerprintBatches` bounds a batch by an upper bound of its Windows-quoted length,
  at half the limit. `TestNeutralAttributes_FingerprintBatchesFitWindows` quotes each batch as
  os/exec does on Windows (red on the old rule: 125,482 units), and
  `TestNeutralAttributes_ManyConvertedFilesAreFingerprintedInBatches` covers 400 files across
  several batches.
- No codex round followed `a33e399c`; the budget had ended.

Gates at `a33e399c`:
- Cheap gates: lint-boundaries, `make build`, embed-smoketest, `go vet` ×3 rc 0;
  golangci-lint `--new-from-rev main` plain / integration / scenario and gosec 0 issues; gofmt
  clean (except the pre-existing `planning_run_test.go`); pincheck OK; `go mod tidy -diff` clean,
  `verify` OK.
- `-race`: `adapters/git`, `gitpool`, `githttp`, `workspace`, `gateway`, `cmd/spine` and
  `core/worktreefs` rc 0.
- `go test -p 4 ./...` rc 0.
- `-tags integration`: this task's set plus `workspace` rc 0.
- scenario-git-http: every step rc 0, 0 skips (90 / 26 / 154 / 12 / 24 / 37 + 1 / 19 + 1 / 1).
- git-floor (git 2.39.5): STEP1 / STEP2 / EXTRA / EXTRA2 rc 0; all 8 path shapes pass on Linux.
- SMP (`82899ace`): build and vet ×4 rc 0; `-race` on the SMP importers of `adapters/git`
  (`cmd/customer-app`, `gitread`, `projectionwriter`, `spinecore`, `spineembedded`, `workspace`)
  and `githttp` all ok; testgate Spine-runtime leg PASS (2447 passed, 3 allowed skips) and githttp leg PASS (590 passed, 1 allowed skip); no `ws_*` or `s24ia_*` database left, SMP untouched.
