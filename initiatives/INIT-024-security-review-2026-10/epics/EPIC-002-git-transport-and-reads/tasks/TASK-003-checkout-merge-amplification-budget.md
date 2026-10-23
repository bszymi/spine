---
id: TASK-003
type: Task
title: "Checkout and merge amplification budget, pre-receive tree limits, SMP-facing MeasureTree"
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
