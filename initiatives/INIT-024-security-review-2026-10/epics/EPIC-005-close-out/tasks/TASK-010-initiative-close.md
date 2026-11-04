---
id: TASK-010
type: Task
title: "Initiative close: integration, gates, documentation and the SMP hand-off"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-11-04
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/epic.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-001-engine-and-governance/tasks/TASK-001-planning-starter-separation-of-duties.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/tasks/TASK-003-checkout-merge-amplification-budget.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-003-standalone-server-hardening/tasks/TASK-006-workspace-scoped-trusted-cidr.md
  - type: blocked_by
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-004-supply-chain/tasks/TASK-009-toolchain-and-ci-pinning.md
---

# TASK-010 — Initiative close: integration, gates, documentation and the SMP hand-off

---

## Purpose

This task closes INIT-024. It integrates the final waves, runs the cross-task review and the gates
at the closing commit, reconciles the documentation, names the closing commit SMP pins, and
decides when the artifacts and fixes may be pushed to the public repository.

It is blocked by TASK-001 … TASK-009. It follows INIT-023's close pattern: tasks stay In Progress
until GitHub CI runs green on the pushed closing commit.

## Scope

### In scope

- The integration branch(es), the cross-task review, the gates, and the status lifecycle of every
  INIT-024 task and epic.
- A reconciliation pass over `KNOWN-LIMITATIONS.md`, `security-model.md`,
  `docs/integration-guide.md` and `CHANGELOG.md`, so the shipped behaviour and the docs agree
  after every wave.
- The push decision (initiative owner question 1).

### Out of scope

- New fixes. A defect found at close becomes a task in its owning epic, or a recorded follow-up.

## The close procedure

1. **Integrate.** Merge the task branches through `init/INIT-024-waveN-integration` branches in
   wave order (initiative §7a). Resolve the file overlaps each task declared (initiative §7a
   table). Whichever task landed second rebased, so the integration is a merge, not a rewrite.
2. **Cross-task review.** Two skeptics per P2 (N-1, N-2), as INIT-023 did. Re-run both P2 PoCs
   (`scratch/git/f4/zz_rv_bomb_test.go`, `scratch/standalone/sa2`) against the closing tree and
   confirm neither reproduces.
3. **Gates at the closing commit.**
   - Spine CI (all five jobs), `go vet` with every tag, race on the changed packages,
     `scenario-git-http` on PostgreSQL 18, `git-floor` on git 2.39.5, golangci (no new issues).
   - `govulncheck` clean (TASK-009).
   - codex review on the configured model, two rounds clean.
4. **SMP compatibility at the pair.** Build SMP `main` against the closing commit with
   `replace ../spine`, and run SMP's Spine-runtime and gateway suites (initiative §5). Record the
   SMP commit and the result.
5. **Documentation reconciliation.** Confirm `KNOWN-LIMITATIONS.md`, `security-model.md`,
   `docs/integration-guide.md` and `CHANGELOG.md` match the shipped behaviour across all tasks,
   with no contradiction between tasks' edits.
6. **Name the closing commit** in these Implementation Notes, for SMP's `.spine-revision` pin.
7. **Status.** Set each task and epic to Completed, and the initiative to Completed, once the exit
   criteria (initiative §8) hold. Tasks stay In Progress until GitHub CI has run green on the
   pushed closing commit.

## SMP hand-off

- The closing commit is what SMP EPIC-005 (and SMP EPIC-004's closing bump) pin. TASK-001 is
  already on `main` before the INIT-023 closing commit (initiative owner question 2), so SMP's
  INIT-023 bump carries it.
- The hand-off note records, per Spine task, what SMP must adopt or verify (the "SMP impact"
  section of each task), and which SMP companion task owns it:
  - N-1 byte budget in `CheckTreeDiff` / `boundRunBranchCheckout` / `MergeBound`;
  - N-7 corpus and projection re-run;
  - N-4 history-reader separator;
  - N-8/N-9/N-13 gateway parity;
  - the MVS-raised modules.

## Push decision (initiative owner question 1)

- Nothing containing these artifacts or the N-1/N-2 fixes is pushed to the public repository until
  TASK-003 and TASK-006 are merged on Spine `main` and SMP's N-1 half is on SMP `main`.
- When that holds, the owner is asked to approve the push. Until approval, the branch stays local,
  as INIT-023's 460 unpushed commits do.

## Acceptance Criteria

1. Every INIT-024 task is Completed (or Cancelled with a recorded reason), and every epic is
   Completed.
2. The cross-task review found no new P1 or P2, and both P2 PoCs fail to reproduce at the closing
   commit.
3. Every task's regression tests are committed and pass at the closing commit; each PoC that could
   not be committed has a recorded reason.
4. The gates in step 3 and the SMP compatibility build in step 4 passed, with the SMP commit and
   results recorded.
5. `KNOWN-LIMITATIONS.md`, `security-model.md` and `docs/integration-guide.md` match the shipped
   behaviour.
6. The closing commit is named in these Implementation Notes.
7. The owner questions (initiative §7d and per task) are answered, or their defaults are recorded
   in each affected task.
8. The push decision is recorded with the owner's approval or the reason to hold.

## Out of scope

- New fixes (a defect at close becomes a task or a follow-up).
- The owner-only repository settings (initiative §7d), which are tracked but not performed here.

## File overlaps

- This task edits the shared docs (`KNOWN-LIMITATIONS.md`, `CHANGELOG.md`, `security-model.md`) to
  reconcile what each task wrote. It makes no code change.

---

## Implementation Notes

Close run on branch `init/INIT-024-TASK-010-initiative-close`, cut from Spine local `main`
`854a1e08`. Close makes no code change of its own: its commits are this artifact, a doc-only
reconciliation and one merge of `main` (TASK-011's code arrives with it). Commits (all prefixed
`INIT-024 EPIC-005 TASK-010:`): `55ef529c` start (status In Progress); `fa68a2f1`, `1cdcb2f6`, `918af609` and `ab41a1be` docs (step 5; the last two from the
cross-task review); `9c013ce9` notes; `00f7da78` the one allowed merge, `git merge --no-ff main`
("bring in TASK-011 from main", `main` = `ea77cff6`); then this notes update. Nothing is pushed.

### The closing commit

- **Candidate:** this branch's final head. The orchestrator merges it into Spine `main`; **that
  merge commit is the INIT-024 closing commit**, the one SMP INIT-027 EPIC-005 TASK-018 pins in
  `.spine-revision`.
- **The closing tree includes TASK-011.** Its Go code is `main` `ea77cff6`'s: `854a1e08` +
  `Merge init/INIT-024-TASK-011-aggregate-checkout-bound`. This branch differs from `ea77cff6` in
  Markdown only.
  - `854a1e08` = `83d82409` (TASK-001, merged on `main` first) + `Merge
    init/INIT-024-integration-a` (TASK-002 … TASK-009 and the integration's coordination commits;
    tree `d332c5ab`, identical to the integration head `a28b3248`).
  - The full gate set below (step 3), both codex rounds and the SMP pair (step 4) ran on
    `854a1e08`'s code. TASK-011 ran its own full gates, codex rounds and SMP compatibility gate on
    its branch (its Implementation Notes). On the merged head this close re-ran the quick gates
    ("Quick gates on the merged head" below); no codex round there (orchestrator ruling: the code
    is `main`'s).

### Step 1 — integration (done before this task; record)

| Task | Merged tip | Merge |
|---|---|---|
| TASK-001 | `49fad9c0` | `83d82409` on `main` (before integration A; SMP EPIC-004 TASK-012 pinned `49fad9c0`) |
| TASK-009 | `b8dc7bb3` | `899cd774` |
| TASK-002 | `07d6ca8a` | `c6bad6c0` |
| TASK-006 | `96dcea55` | `000c5461` |
| TASK-007 | `55a67a45` | `e6ab7e81` |
| TASK-008 | `c6541bd5` | `8a0a1b29` |
| TASK-003 | `02334613`, review round 4 `693fb648` | `4f502b7e`, `23330ab7` |
| TASK-004 | `d18a9471` | `557e78cb` |
| TASK-005 | `80240088` | `e1f24c05` |
| integration A | `a28b3248` | `854a1e08` on `main` |
| TASK-011 (added by the cross-task review) | `fd7f72ef` | `ea77cff6` on `main`; brought into this branch by `00f7da78` |

Each task's "Integration A" section records its overlaps, coordination commits and the phase
gates; nothing was rewritten.

### Step 2 — cross-task review

Two independent skeptics reviewed the two P2s across tasks (N-1: TASK-003 with TASK-004's read
paths; N-2: TASK-006 with TASK-005/007/008), read-only on the same tree (`i23/s24-T003`, detached at
`a28b3248`); their reports go to the orchestrator (SMP repo `tmp/init027-orchestration/i24/t010/`).

- **Skeptic A: no P1 or P2.** Both PoCs reproduce at the pre-fix base `49fad9c0` and are refused
  at the closing tree for the right reason, on the host git and on git 2.39.5: N-1 with
  `ErrTreeTooLarge` (total bytes) and no worktree directory written, the two-level bomb writing
  nothing; N-2 with the uniform `401` for the workspace the runner is not bound to, on every `/git`
  spelling tried. Six mutations of the central checks were killed.
  - **P3, recorded as a residual and a follow-up (no code change here).** The budget bounds each
    checkout, merge and push, but not the total bytes of checkouts in flight at once: concurrent
    branch-scoped writes, to one branch or to several, each write a full within-budget tree to
    the server's temporary directory, including writes git then refuses. `KNOWN-LIMITATIONS.md`
    stated a narrower premise (different branches only); `918af609` corrects it at defect level
    and names the operator levers. **Fixed by INIT-024 TASK-011** (one writer per branch, and an
    in-flight ceiling shared fairly across workspaces, `SPINE_GIT_CHECKOUT_INFLIGHT_MAX_BYTES`),
    now in the closing tree; the doc text was updated at the merge.
  - **P4:** under concurrent entries git's own in-use guard can let several linked worktrees
    attach to one branch; the ref compare-and-swap turns that into failed writes, never lost
    updates (the P3 fix closes it). **P4:** `SPINE_GIT_HTTP_DEV_NO_AUTH` is not workspace-scoped
    and is accepted in shared mode under `SPINE_ENV=development` (already a documented residual;
    option: refuse it in shared mode).
- **Skeptic B: N-1 and N-2 closed, no new findings.** Its only P3 is skeptic A's (now TASK-011),
  seen from more angles: failing requests still wrote the full tree, and the request rate was the
  only limiter. No new P4. Both PoCs are red at `49fad9c0`. At the closing tree, with `spine
  serve`'s process default, N-1's shapes are refused with `ErrTreeTooLarge` naming the right limit
  on `EnterBranch`, `Merge`, `MergeBound` and `Checkout`, with nothing written and `main` unmoved;
  N-2's other workspace gets the uniform `401`. Both hold on git 2.54 and 2.39.5. The refusals are
  for the right reason: mutating the `EnterBranch` budget brings N-1's write back, and trusting
  every workspace brings N-2's clone back. Six mutations were killed.
- **The two agree:** N-1 and N-2 are closed at the closing tree. Skeptic A's P3 became TASK-011,
  which is fixed and merged (`ea77cff6`).

### Step 3 — gates at the closing tree

Logs: SMP repo `tmp/init027-orchestration/i24/t010/`. Heavy legs ran under the shared heavy-suite
lock. Go `go1.26.10` (go.mod's toolchain) for every Spine leg on the host.

| Gate | Command | Result |
|---|---|---|
| CI `boundary-lint` | `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| CI `build` | `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | rc 0; "engine constructed and exercised; library surface is sound." (binaries deleted) |
| CI `test` | `go test -p 4 -count=1 ./...` | rc 0, 41 packages ok, 0 failures |
| CI `scenario-git-http` | every run step of the job, against an own PostgreSQL **18.6** database on `i23-pg` (the job's `postgres:18-bookworm` is 18.6 too) | every step rc 0, 0 skips; every required `--- PASS` present: `TestGitHTTP_*` 90, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*` 154, workflow policy 1, step CAS 12, step evidence 24, status lifecycle 37 + 1, governance 19 + 1, shared-mode scheduler 1 |
| CI `git-floor` | `docker run --pull=never` of the local `golang:1.26-bookworm` (git 2.39.5; its own Go is 1.26.8), with the module cache's `go1.26.10` linux-arm64 toolchain copied into a tmpfs as `GOROOT` (`GOTOOLCHAIN=local`, `GOPROXY=off`, worktree and module cache read-only); the pinned `golang:1.26.10-bookworm` image is not present and was not pulled | `go1.26.10 linux/arm64`, `git version 2.39.5`. Both CI steps rc 0, plus: `gitpool`, `projection`, `worktreefs`, `githttp`, `gateway`, `dev/pincheck` in full; TASK-003/005/006 hook, budget, slot, probe and trusted-CIDR tests in `cmd/spine` and `workspace`; every `TestNeutralAttributes_*` path shape — all rc 0. Container removed. |
| CI `vuln` | `GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` (needs the network for the vulnerability database; it was reachable) | exit 0, "No vulnerabilities found"; module-level only: chi GO-2026-5774/5775/5777 (`middleware.RealIP`, not imported). Go go1.26.10, DB updated 2026-10-27 21:16:18 UTC. Informational: `-tags integration,scenario -test ./...` also 0 reachable. |
| CI `toolchain-parity` | `go test -count=1 -v ./dev/pincheck/`; `go run ./dev/pincheck`; `go run ./dev/pincheck -resolve` (registry and `git ls-remote`, no pull) | ok; "OK — toolchain go1.26.10; 3 golang reference(s) on one digest; 10 image(s) and 14 action(s) pinned" (both) |
| CI `image-build` | `docker build` | **Not run**: building images is not allowed on this host (Docker VM disk). GitHub CI runs it. |
| `go vet` | plain; `-tags integration`; `-tags scenario`; `-tags integration,scenario`; `GOOS=linux` plain and `-tags integration` | rc 0 ×6 |
| `-race` | `go test -race -p 4 -count=1` on the 18 packages with Go changes in `49fad9c0..854a1e08` (`adapters/{git,gitpool,projection,scheduler}`, `core/{artifact,branchprotect,engine,evidence,workflow,worktreefs,yamlsafe}`, `dev/pincheck`, `service/cmd/spine`, `service/internal/{cli,gateway,githttp,workspace}`, `workflows`; `service/scenariotest/scenarios` is scenario-tagged only and ran in the suites below) | rc 0, 18 ok |
| Integration | `go test -tags integration -p 1 -count=1 ./...` (own database plus a migrated registry database) | rc 1 only for the two known pre-existing failures (`TestAWSClient_Contract_LocalStack`, which needs LocalStack, and `TestRunModeDatabaseDefault`, both outside INIT-024's packages); 39 packages ok, 571 s |
| Scenario | `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (own database plus a migrated registry) | 31 top-level and 59 total `--- FAIL` lines: exactly the known pre-existing set (TASK-001's branch run, TASK-009's base run; nothing new, nothing gone); 1,791 `--- PASS` lines. Every INIT-024 scenario test passes: TASK-001's three `TestScenario_*Starter*`/`PlanningStartWithoutActor*`, the four `TestWorkflowLifecycle_GatewayStarted*`/`*_GuardOn`, `TestRunnerCloneContext_AssignmentPayloadDrivesClone`. |
| AC regression tests | every AC-named test of TASK-001 … TASK-009 that exists under that name, `go test -count=1 -v -run` by name in its 11 packages | 57 / 57 pass. The six AC names not in the tree are review-scratch PoC names whose committed ports carry other names (`TestZZSA1_*`, `TestGV_SoD_*`, `TestRV_*` ×3; each task's per-AC evidence names the port) and TASK-009's `TestGoModHasToolchain`, which its AC allows as "or a CI assertion" (`dev/pincheck`, above). |
| golangci-lint v2.11.4 | `--new-from-rev 49fad9c0 ./...` plain / `--build-tags integration` / `--build-tags scenario` | 0 issues ×3 |
| gosec | `--enable-only=gosec ./...` | 0 issues. With `--build-tags integration` one issue, G602 at `adapters/store/postgres_integration_test.go:1563`, in a file INIT-024 did not touch (0 with `--new-from-rev 49fad9c0`). |
| gofmt | `gofmt -l` on the 156 Go files changed since `49fad9c0` | clean except `core/artifact/id_allocator.go` and `service/scenariotest/scenarios/planning_run_test.go`, both unformatted at `a560c210` already (left as they are) |
| Modules | `go mod tidy -diff`; `go mod verify` | no diff; all modules verified |

No gate left a file in the worktree (`git status` clean after each); every database created
(`s24010_*`) was dropped, and no `ws_*` database or role was left.

### Codex

`codex review --base 49fad9c0` (the INIT-024 diff after TASK-001: 191 files, +32,012 / −941),
plain, from the worktree; the model confirmed in each review sub-session's JSONL.

| Round | Head | Review sub-session | Model | Result |
|---|---|---|---|---|
| 1 | `55ef529c` (code = `854a1e08`) | `01a18f34-3ba2-7612-82b2-1d2d49676772` | `gpt-6.1-sol` | `findings: []`, "patch is correct" (confidence 0.7), 148 s |
| 2 | `fa68a2f1` (plus the step 5 docs) | `01a18f3d-5019-7cc1-a266-18de397932c5` | `gpt-6.1-sol` | `findings: []`, "patch is correct" (confidence 0.67), 216 s |

Two clean rounds, no P1–P4 to disposition. Both reviews say their own broad test run did not
finish (codex runs tests inside its time budget); the full suites are the gates above. A clean
codex round on a 32k-line diff in under four minutes is a shallow review: the depth for this close
comes from the gates, the skeptics and each task's own review rounds. The later commits
(`1cdcb2f6`, `918af609`, `ab41a1be` and this notes commit) are Markdown only.

### Step 4 — SMP compatibility at the pair

SMP `21cc2e8a` (SMP local `main`, detached, 0 dirty files before and after; `.spine-revision`
`49fad9c0`), `../spine` = this worktree (Go code = `854a1e08` throughout; the close commits are
Markdown only), `GOFLAGS=-mod=readonly`, go1.26.10.

| Leg | Result |
|---|---|
| `go build ./...`; `go vet` plain / `-tags integration` / `-tags scenario ./scenariotest/...` / `-tags faultinject` | rc 0 ×5 |
| `go test -race -p 4 -count=1` on every SMP package importing a Spine package INIT-024 changed (from `go list` imports and test imports: the module root, `cmd/customer-app`, `internal/customer/{executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,spinecore,spineembedded,workspace}`) | 11 ok, 0 failures |
| ci.yml integration job, "Spine runtime integration suites" (`dev/testgate`, `spinecore`, `spineembedded`, `runner`, `executionreadiness`; platform and spine-runtime databases migrated with `dev/apply-migrations`) | PASS: 2,463 tests, 0 failed, 3 allowed skips (`ru_maxrss` is Linux-only) |
| githttp suite (`dev/testgate … -tags integration ./internal/customer/githttp/`) | PASS: 590 tests, 0 failed, 1 allowed skip (Linux RSS) |

Known and not a break: SMP's listing limit (`SMP_GIT_MAX_OUTPUT_BYTES`, 16 MiB) is below Spine's
`git.DefaultMaxListingBytes` and the tree budget — an SMP TASK-018 carry (hand-off below).

### Step 5 — documentation reconciliation (`fa68a2f1`, `1cdcb2f6`, `918af609`, `ab41a1be`, `00f7da78`; doc-only)

- **`KNOWN-LIMITATIONS.md`.** The git floor bullet still put `spine serve` at git 2.30; it now
  names 2.38 (TASK-003: the budgeted merge uses `git merge-tree --write-tree`). "Not covered"
  separates the checkout budget from the mutate deadline and says both are on in `spine serve` and
  opt-in for embedders. The `IncrementalSync` auto-fallback was described as TASK-010 work; it is a
  recorded follow-up (below). New residual: a tree on folding storage can stay marked for
  renormalisation (integration review round 7, P4). From the cross-task review (`918af609`): the
  "totals bound one tree at a time" residual claimed only concurrent worktrees of different
  branches; it now states that the total bytes of checkouts in flight are not bounded, per branch or
  across branches (refused writes included), with the operator levers; `ab41a1be` names INIT-024
  TASK-011 as its fix.
- **`architecture/security-model.md` §7.4.** The git HTTP endpoint's controls after INIT-024:
  workspace-bound token-less reads (N-2), the checkout budget at pre-receive and on every server
  checkout (N-1), the storage requirement (N-8), the apply-time admission (N-9, §7.3) and fair
  slots (N-13), with the development read flag (`1cdcb2f6`) and the in-flight residual
  (`918af609`). The section said nothing about the endpoint before.
- **`docs/integration-guide.md` §7.** Only the git protocol's own paths reach `git-http-backend`
  (TASK-004); the route list did not say so.
- **The merge of TASK-011 (`00f7da78`).** `KNOWN-LIMITATIONS.md` had one conflict: TASK-011's new
  bullet ("Concurrent branch-scoped writes are bounded in aggregate") and this close's
  folding-storage residual were added at the same spot, and both are kept. The close's in-flight
  residual now says TASK-011 bounds the checkouts in flight and points to that bullet, and the
  stale "different branches each get the full budget" sentence is gone. TASK-011's "What remains"
  gains the stale-worktree-registration residual (TASK-011 round-2 review, P4). `security-model.md`
  §7.4 says what TASK-011 bounds (one writer per branch and the in-flight ceiling, per process,
  opt-in for embedders except the claim), as its `CHANGELOG.md` section does. `docs/integration-guide.md`
  and `architecture/git-integration.md` merged without conflict.
- **Beyond the four named documents** (they contradicted shipped behaviour):
  `architecture/git-integration.md` (reserved and home names no longer fall back by DWIM; `spine
  serve` needs git 2.38) and `docs/git-push-guide.md` §5 (rows for the folding-storage `403`, the
  slot `503`, the re-check and command-read refusals, and the further causes of `500 push gate
  unavailable`).
- **`CHANGELOG.md`: checked, unchanged.** The nine INIT-024 sections are in landing order (newest
  first: TASK-005, 004, 003, 008, 007, 006, 002, 009, 001), no section or bullet repeats another,
  and every cross-reference ("above", "below", `KNOWN-LIMITATIONS.md` section names and items)
  resolves.
- Checked and consistent across tasks (no edit needed): the uniform `401` (TASK-006 on `/git`,
  TASK-007 in shared mode, TASK-008's pool timeouts), the git slot defaults (2 / 4 / 8 shared,
  8 / 8 / 8 single), the listing and diff limits against the tree budget (TASK-003 × TASK-004),
  the pre-receive hook's two jobs (TASK-003 × TASK-005), `SPINE_ENV` (TASK-007) in every doc
  that mentions it, and no remaining "follow-up"/"not yet enforced" text for N-1, N-2, N-8, N-9,
  N-13 or P3-27.

### Quick gates on the merged head

At `00f7da78` (Go code = `main` `ea77cff6`: `git diff --name-only main HEAD -- '*.go'` is empty), go1.26.10:

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | rc 0 (binary deleted) |
| `go vet ./...` | rc 0 |
| `go test -p 4 -count=1 ./...` (under the heavy lock) | rc 0, 41 packages ok, 0 failures, 297 s |
| `gofmt -l` on the 175 Go files changed since `49fad9c0` (TASK-011's included) | clean except the two files already unformatted at `a560c210` |
| Links and cross-references in the edited docs (`KNOWN-LIMITATIONS.md`, `security-model.md`, `git-integration.md`, `integration-guide.md`, `git-push-guide.md`, this artifact, `initiative.md`): every relative link target and anchor exists; every quoted `KNOWN-LIMITATIONS.md` section name exists as a heading or bold item; "below" and "above" point the right way | OK. The only flag is a pre-existing abbreviation (`"Engine State Writes …"` in `integration-guide.md`, from INIT-023), which names an existing heading. |

### SMP hand-off (owner of the bump: SMP INIT-027 EPIC-005 TASK-018)

SMP's `.spine-revision` is `49fad9c0` (SMP EPIC-004 TASK-012: INIT-023 complete + TASK-001).
SMP TASK-018 bumps it to the closing commit; the range `49fad9c0..closing` holds TASK-002 … TASK-009
and TASK-011.

| Spine task | What SMP adopts or verifies at the bump | SMP owner |
|---|---|---|
| TASK-001 (N-3) | Already in SMP's pin. Inert while SMP's separation of duties is off; if SMP turns it on, `CreateEntry` puts the member's `human-<uuid>` handle on the context (`domain.WithActor`) before `StartPlanningRun`, or every planning start is refused. Re-run the separation-of-duties tests with it on. | SMP TASK-018 (SMP EPIC-004 TASK-011 owner question 2) |
| TASK-002 (N-4, N-5, N-7) | **Shared:** `workflow.Parse` refuses a second non-empty YAML document (the projection quarantines the file). Re-run SMP's workflow corpus and projection suites. N-5's backstop matters once SMP turns the guard on (SMP EPIC-004 TASK-010); N-4 is inert (SMP's corpus already uses `invalid`). | SMP TASK-018 |
| TASK-003 (N-1) | Spine's budget is opt-in for embedders; nothing changes at the bump. SMP's own N-1 half — the byte budget in `gitread.CheckTreeDiff`, the merge result measured in `RealGitOperator.MergeBound`, and the pool's whole-tree measure in `cliWorktree.Add`, which replaced `boundRunBranchCheckout` — landed as SMP TASK-011 (SMP local `main` `21cc2e8a`, not pushed). At the bump: one too-large error identity with `git.ErrTreeTooLarge`, keeping the stricter limit; **one shared Spine constant for the neutral-attributes line** (`git.NeutralCheckoutAttributes`; SMP's `executionmeta.NeutralCheckoutAttributes` carries the same text today) so `info/attributes` never has two writers with different text; optionally a differential test against `git.TreeBudget.Check`, or `git.WithTreeBudget` on SMP's Spine clients. | SMP TASK-011 (landed), SMP TASK-018 |
| TASK-004 (N-6, N-10, N-11, N-12) | **Shared:** listings refuse less (kept bytes, record ceiling), reserved/home names with no branch are NotFound, `Diff` reports gitlinks hidden by submodule settings, `Log` parses strictly. Re-run `gitread`, `projectionwriter` and `spinecore`. **N-11 in SMP:** SMP's own history reader still delimits with the fixed `---END---` (`internal/customer/spinecore/artifacthistoryreader.go:178`, `:228`) — the defect TASK-004 fixed in Spine; SMP fixes it. **Listing limit:** `gitread.NewClient` passes `SMP_GIT_MAX_OUTPUT_BYTES` (16 MiB), below `git.DefaultMaxListingBytes` (~17.2 MiB) and the tree budget's 16 MiB of path bytes: raise it to at least `git.DefaultMaxListingBytes`, or pass `git.WithTreeBudget` (a carry, not a break). `gitread.resolveRevLikeSpine` documents only rules 1–4 (behaviour correct; doc drift). `CountChangedPaths` is optional (owner question 2 default: SMP keeps counting through `Diff`). | SMP TASK-018 |
| TASK-005 (N-8, N-9, N-13) | No shared code. Parity for SMP's own `/git/{workspace}` gateway: a storage probe refusing receive-pack on folding storage, the apply-time admission re-check, and fair slots taken after a deadlined command-section read (the pattern is in TASK-005's "SMP parity"). | SMP TASK-018 (parity check; a new SMP task if the gateway lacks them) |
| TASK-006, TASK-007, TASK-008 | Standalone only (TASK-007's `core/evidence` fields are additive; SMP does not import the package). Gates only. | SMP TASK-018 (range audit) |
| TASK-011 (aggregate checkout bound) | **Shared at the bump:** a second concurrent `EnterBranch` scope for a branch the process already holds is refused before any git process runs (untyped error wrapping `git.ErrBranchInUse`; callers classify it as before). **Opt-in:** the in-flight ceiling (`git.CheckoutLimits.InflightMaxBytes`; zero, the default, means no aggregate bound). SMP calls no branch-scoped `EnterBranch` and sets no ceiling (TASK-011's SMP gate passed). **Parity:** an aggregate gate for SMP's own write scopes (`SMP_GIT_CHECKOUT_INFLIGHT_MAX_BYTES`, follow-ups). | SMP TASK-018 |
| TASK-009 | **MVS:** Spine's floors (pgx v5.9.2, x/text v0.42.0, x/sync v0.23.0, `go 1.26.0`) equal SMP's selections, so SMP's build list does not move; run `go mod tidy` at the bump to confirm. SMP's own toolchain is SMP TASK-012. | SMP TASK-018, SMP TASK-012 |

### Owner questions

**Initiative §7d** (defaults recorded; answers pending the owner):

1. **Landing and push order — HOLD** (below).
2. **TASK-001 before INIT-023's close — default taken.** TASK-001 merged on Spine `main`
   (`83d82409`) before integration A, and SMP EPIC-004 TASK-012 pinned `49fad9c0` (INIT-023
   complete + TASK-001), so one SMP bump carried both.
3. **Standalone defaults — applied as stated:** the tree budget has no off switch (only larger
   limits); the pre-receive hook is on whenever receive-pack is; an unbound trusted-CIDR list
   refuses to start in shared mode; `SPINE_ENV` is required; SSE streams revalidate on every
   heartbeat; the scheduler runs in every mode.
4. **P3-10 and P3-19 stay PARTIAL — default accepted;** no task.
5. **TASK-008 stays here — default,** labelled functional.

**Per task** (each task's Implementation Notes record its defaults, all pending the owner):
TASK-001 OQ1 fail closed for unrecorded starters, OQ2 `ErrUnauthorized`; TASK-002 OQ1 N-7 on for
every reader, OQ2 backstop at any depth, OQ3 backstop always on while the guard is on; TASK-003
OQ1 200,000 entries / 2 GiB / 16 MiB, OQ2 a 2-minute mutate deadline in `spine serve` (none in the
library), and the orchestrator ruling that phase 4's fail-closed over-budget HEAD (no line, no
marker, nothing written) is accepted; TASK-004 OQ1 narrow DWIM, OQ2 `Diff` fallback for operators
without `ChangedPathCounter`, open question 1 no automatic rebuild fallback (follow-up below);
TASK-005 OQ1 per the orchestrator ruling — shared mode 2 / 4 / 8 (per principal, per workspace,
process), single mode 8 / 8 / 8 — OQ2 receive-pack refused on folding storage, reads served, OQ3
the re-check runs the same admission and freeze; TASK-006 OQ1 refuse to start, OQ2 no job-scoped
tokens; TASK-007 OQ1 the 30 s heartbeat, OQ2 refuse to start when `SPINE_ENV` is unset, OQ3
`verified` + `source_ref` + `unverified_repositories`; TASK-008 OQ1 kept, OQ2 4 workspaces, OQ3
no per-workspace startup recovery, OQ4 an invalid value refuses to start; TASK-009 OQ1 floors equal
SMP's selections, OQ2 fail on any reachable advisory.

**Owner-only actions** (tracked, not performed): a Spine `main` ruleset (PR and CI required, no
force push); approval for all outside contributors; "allowed actions: selected" with SHA pinning
(possible now that TASK-009 pinned every action); revoke the PAT behind SMP's unused
`SPINE_REPO_TOKEN` secret and delete the secret.

### Follow-ups (recorded, not done here)

- **N-1: bounded per checkout, merge and push, and — since TASK-011 — in aggregate for
  branch-scoped writes.** The cross-task review's aggregate residual (skeptic A, P3) is fixed by
  INIT-024 TASK-011
  (`/initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/tasks/TASK-011-aggregate-checkout-bound.md`).
  `KNOWN-LIMITATIONS.md` ("Concurrent branch-scoped writes are bounded in aggregate") records
  what remains: the bound is per process; there is no reservation, so a large tree can wait out
  its deadline behind a stream of other workspaces' smaller writes; and the worktree listing does
  not show a worktree in the middle of a rebase or bisect (Spine runs neither).
- **Stale worktree registrations:** a registration left by a crash between a scope's worktree
  creation and its removal, or by a temporary directory cleaned under a live registration, keeps
  its branch refused until `git worktree prune`. This predates TASK-011: git's own guard refused
  the same. Prune at `spine serve` start, or prune and retry once when the only holder is
  prunable (TASK-011 round-2 review, P4; now in `KNOWN-LIMITATIONS.md`).
- **`SPINE_GIT_HTTP_DEV_NO_AUTH` in shared development mode:** it is not workspace-scoped, and
  `spine serve` accepts it in shared mode under `SPINE_ENV=development`. Option: refuse it in
  shared mode (a TASK-006 follow-up; skeptic A, P4; already a documented residual).
- **SMP TASK-018 parity for the aggregate gate:** SMP's write scopes are one per clone and are
  measured, but scopes of different clones have no aggregate bound. The parity fix is a
  process-wide weighted gate on the measured bytes, with a ceiling knob mirroring Spine's,
  suggested name `SMP_GIT_CHECKOUT_INFLIGHT_MAX_BYTES` (TASK-011's "Parity note for SMP
  TASK-018").
- `IncrementalSync` falls back to `FullRebuild` on its own when its window's `Diff` is refused as
  too large (TASK-004 open question 1; today an operator rebuild recovers it).
- A tree on case- or normalization-insensitive storage can keep a renormalisation marker that has
  no readable record: treat the paths that survive the rewrite from the index as recorded, Warn
  once and clear the marker (integration review round 7, P4; residual in `KNOWN-LIMITATIONS.md`).
- The rest stay where their tasks recorded them: job-scoped clone tokens and the single-mode
  no-auth `503` (TASK-006), a per-workspace build rate limit or negative cache (TASK-007),
  per-workspace startup recovery and paged scans (TASK-008), the owner actions above (TASK-009).

### Push decision: HOLD

Nothing from INIT-024 is pushed. Spine is public and these artifacts and fixes describe the N-1 and
N-2 defects. Owner question 1's preconditions on Spine hold locally (TASK-003 and TASK-006 are on
Spine local `main` at `854a1e08`), and SMP's own N-1 half is on SMP local `main` (`21cc2e8a`,
SMP TASK-011), but nothing is pushed on either side, the bump that reconciles SMP with Spine's API is
SMP TASK-018, and the owner has not approved. The push waits for the owner's approval; Spine's
commits reach GitHub first, then SMP's (SMP EPIC-004 push rule).

### Statuses

TASK-001 … TASK-009 stay **In Progress**, and so does this task: they move to Completed only after
GitHub CI runs green on the pushed closing commit (owner-gated), as in INIT-023. The five epics and
the initiative are left as they are (Pending), as INIT-023's are; they move with the tasks.
TASK-011, which the cross-task review added, is in the closing tree and stays In Progress like
the others until GitHub CI.

### Acceptance criteria at this commit

| AC | State |
|---|---|
| 1 | Not yet: statuses are owner-gated (above). |
| 2 | Done: two skeptics (A, B) agree that N-1 and N-2 are closed; neither found a new P1 or P2, and both P2 PoCs fail at the closing tree for the right reason. A's P3 became TASK-011, which is fixed. |
| 3 | Done for this close's part: every AC-named regression test in the tree passes by name (57 / 57) and every committed test passes in the gates above (unit, `-race`, integration, scenario, git-floor). The ports of the review PoCs carry their own names (each task's per-AC evidence); the PoCs themselves are the skeptics' (step 2). |
| 4 | Done: every gate in step 3 passed at `854a1e08`'s code (known pre-existing failures only), and the SMP pair passed at SMP `21cc2e8a`. TASK-011's own gates and SMP gate passed on its branch. The quick gates passed on the merged head. `image-build` is GitHub CI's. |
| 5 | Done (`fa68a2f1`, `1cdcb2f6`, `918af609`, `ab41a1be`, and the merge resolution in `00f7da78`). |
| 6 | The candidate is named above; the closing commit is the orchestrator's merge of this branch. |
| 7 | Defaults recorded (above and in each task). |
| 8 | HOLD, with the reason above. |

### Not run

- CI `image-build` (`docker build`): no image builds on this host; GitHub CI runs it.
- `git-floor` in the exact pinned `golang:1.26.10-bookworm` image: not present locally and not
  pulled; the job ran in the local `golang:1.26-bookworm` with go.mod's go1.26.10 toolchain as
  `GOROOT`, so the git (2.39.5) and the Go (1.26.10) are the pinned job's.
- The SMP gates beyond the compatibility gate (golangci, the `dbstructure` check, the scenario suite,
  CI images, codex at the pair): SMP TASK-018's, at the bump.
- The two P2 PoCs and the adversarial cross-task search: the skeptics' (step 2), not repeated here.
- The full gate set, codex and the SMP pair at the merged head: they ran at `854a1e08`'s code, and
  TASK-011 ran its own on its branch (codex rounds, its last fixes delta-reviewed instead; `-race`,
  scenario-git-http, git-floor 2.39.5,
  golangci/gosec, and the SMP gate at SMP `21cc2e8a` with `../spine` = TASK-011's head). Only the
  quick gates were re-run here (orchestrator ruling: the merged code is `main`'s).
- GitHub CI on a pushed closing commit: owner-gated (push held).

### GitHub CI after the push (2026-11-04)

Spine `main` `fd35f802` was pushed on 2026-11-04. Its first GitHub CI run, **41856447149**, failed
only the `go test ./...` job, on three tests; every other job was green (boundary lint, build,
toolchain parity and pins, git-floor on 2.39, govulncheck, the git push scenarios, image build).

- `adapters/gitpool` `TestClient_ReusedConvertedCloneIsRenormalised` (both subtests): "first merge
  after the upgrade: git merge: Committer identity unknown".
- `service/internal/gateway` `TestF7_OperatorUpdate_KeepsBypassTrailer` (operator PUT 500 "git
  commit workflows/task-default.yaml: Committer identity unknown") and
  `TestP36_Assignee_CanWriteOwnRunBranch` (assignee write 500, the same).

**Cause.** GitHub's runner gives git no identity: no `user.name` in any config, and the runner
user's GECOS name is empty ("empty ident name (for <runner@…>) not allowed"). The machine every
earlier run used had a global identity. The tests were right: the git client's commits pass
`--author` but never a committer, and its merges pass neither, so they depend on the host's
identity whenever the repository has none of its own.

**Reproduction (hermetic, per command, no global config touched).** `HOME` and `XDG_CONFIG_HOME`
empty scratch directories, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, no
`GIT_AUTHOR_*`/`GIT_COMMITTER_*`/`EMAIL`/`GIT_EDITOR`, Go's caches pinned to their usual paths.
On macOS that alone is not enough: git builds a name from the passwd GECOS field and an address
from the host name, and commits. So `user.useConfigOnly=true` was added through
`GIT_CONFIG_COUNT`, which makes git refuse any identity it was not given, as the runner does. With
it, `go test -count=1 -p 4 ./...` at `fd35f802` failed exactly CI's three tests and nothing else. A
second run put a `git` wrapper on `PATH` that, before each commit-creating command (commit,
commit-tree, merge other than `--ff-only`/`--abort`, stash, cherry-pick, …), asks `git var
GIT_COMMITTER_IDENT`/`GIT_AUTHOR_IDENT` in the same directory and environment and logs the command
when git cannot resolve one. It found one more test, which passed while its merge was refused. Two
cross-checks agree: the `scenario-git-http` job's eight steps under the same environment and the
wrapper (green, and no identity-less commit-creating command), and the `git-floor` job in
`golang:1.26-bookworm` (git 2.39.5, go1.26.10 from the module cache) as uid 501, which has no
passwd entry, so git's guess is "Unknown <unknown@<host>.(none)>" and is refused, with no identity
configured anywhere. Both of that job's steps are ok there. `adapters/gitpool` and
`service/internal/gateway` fail CI's three tests there.

| Test (hermetic run at `fd35f802`) | Path | Production? | Fix |
|---|---|---|---|
| `TestClient_ReusedConvertedCloneIsRenormalised` (2 subtests), FAIL | `CLIClient.Merge` (merge-commit) in a gitpool code-repository clone | **Yes.** `gitpool.NewCLICloner` clones with plain `git clone`, so no clone has an identity of its own. On a host with none, every governed merge into a code repository is refused. | Fallback in `runMerge`; new `TestClient_CodeRepoCloneMergesWithNoHostIdentity` (the production cloner and factory) |
| `TestF7_OperatorUpdate_KeepsBypassTrailer`, FAIL | `workflow.Service` write → `git.StageAndCommit` → `commitFile`, in the authoritative repository | **Yes.** `Provision` sets a repo-local identity only on the repositories it initialises (fresh, or a clone of a non-Spine repository), not on a clone of an existing Spine repository, and an operator-mounted single-repository deployment has none. Spine's standalone image has no identity either (`useradd spine`: empty GECOS name, no `user.name`). | Fallback in `commitFile`; new gateway test |
| `TestP36_Assignee_CanWriteOwnRunBranch`, FAIL | `artifact.Service` run-branch write → `EnterBranch` worktree → `commitFile` | **Yes**, as above (a linked worktree reads the repository's config) | Same |
| `TestP36_R12_ReviewFreeze_InjectionNeverReachesMain`, **passed vacuously** (on CI too) | `engine.MergeRunBranch` → `MergeBound` into main | **Yes.** The governed merge was refused ("permanent merge failure … Committer identity unknown", "run failed on merge"), `MergeRunBranch` returned nil, and the only assertion, that the injection is not on main, held trivially. | Fallback; **test gap fixed**: the test also requires that the run did not fail and that the reviewed work is on main |

No failure was a fixture gap. Every fixture commit in these tests already carried an explicit
identity. In each failure the production client made the commit, in a repository with no identity
of its own.

**Fix** (`fd690802`). `adapters/git/identity.go`: before `Commit`, `Merge` (every strategy but
`fast-forward`), `MergeBound`, `CommitFilesOnto` and `StageAndCommit`, the client asks git whether
it resolves the committer, and the author too where the caller names none (merge commits, system
commits). It runs `git var GIT_<ROLE>_IDENT` in the repository with the command's environment, and
skips the probe when that environment names the identity. Only git's own identity refusal adds
`GIT_<ROLE>_NAME=Spine` and `GIT_<ROLE>_EMAIL=spine@spine.local`. This is the quarantine commit's
identity from `treelock.go` (INIT-023 TASK-012), which now uses the same constants. The server is
the committer of what it writes on an actor's behalf, and the actor stays the author. It is a
fallback, not an override (the way `treelock.go` does its own commit), because an identity a
deployment configures must be kept: SMP's embedded scopes set a repo-local committer before they
write (`spinecore.configureCommitterIdentity`), and SMP's `gitsync` compares committer fields. When
git resolves an identity, the command, its argv and its environment are unchanged. That covers
every commit SMP makes, so SMP's commits are byte-identical, at one or two `git var` processes per
commit or merge. A probe that fails for another reason (a cancelled context, a broken config) adds
nothing. Tests (the red runs are in the gate table): `adapters/git`
`TestNoHostIdentity_CommitPathsFallBackToSpine` (9 paths), `TestNoHostIdentity_ConfiguredIdentityIsKept`
(passes before and after: the compatibility guard), `TestNoHostIdentity_ResolvedIdentityRunsTheCommandUnchanged`,
`TestIdentityRefusal`, `TestIdentityFallbackEnv_OnlyGitsRefusalFallsBack`; `adapters/gitpool`
`TestClient_CodeRepoCloneMergesWithNoHostIdentity`; `service/internal/gateway`
`TestNoHostIdentity_GatewayWritesCommitAsSpine`; the strengthened R12 test. All run under the new
`testutil.NoGitIdentity`, so they fail without the fix on every host, not only on CI.
`TestCommands_RunUnderTheirClassEnvironment` pins the probe's environment (the C locale, nothing
else).

**Hermetic by default** (`874090fe`). The `ci.yml` `go test ./...` step, step-scoped so
actions/checkout is untouched, and `make test` (`GIT_HERMETIC`, which also unsets the identity
variables) run with `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1` and
`user.useConfigOnly=true`. A future identity dependency now fails locally too. It broke no other
test: the hermetic full run is green. Docs: `81790e80` (CHANGELOG; KNOWN-LIMITATIONS "Git Commit
Identity: Spine's Fallback").

**Gates** (code at `fd690802`/`874090fe`, go1.26.10; heavy runs under the machine lock):

| Gate | Result |
|---|---|
| Hermetic `go test -count=1 -p 4 ./...` (the environment above, with the identity wrapper) | rc 0, 41 packages ok. The wrapper logged only commits whose author is on the command line (`--author`, committer resolved) and `TestNoGitIdentity`'s deliberate refusal |
| `go test -count=1 -p 4 ./...` (this host's own git identity) | rc 0, 41 ok |
| `make test` (`GOFLAGS=-count=1`) | rc 0, 41 ok |
| `scenario-git-http`: its eight steps, verbatim, under the hermetic environment and the wrapper | all rc 0, 48 required PASS, no FAIL or SKIP, nothing logged |
| `git-floor`: both steps in `golang:1.26-bookworm` (git 2.39.5, go1.26.10), uid 501, no identity anywhere; plus `adapters/gitpool`, `service/internal/gateway`, `testutil` | all ok |
| Red runs (`go test -overlay` with `fd35f802`'s production files) | every new regression test fails (`TestNoHostIdentity_CommitPathsFallBackToSpine` 9/9 "Committer/Author identity unknown", `TestClient_CodeRepoCloneMergesWithNoHostIdentity`, both gateway subtests `500`, R12 "the governed merge failed the run", `TestNoHostIdentity_ResolvedIdentityRunsTheCommandUnchanged` "no identity probe ran"); `TestNoHostIdentity_ConfiguredIdentityIsKept` passes (the guard). The unit tests of the new functions and of `testutil.NoGitIdentity` have no base |
| `go vet ./...` plain, `-tags integration`, `-tags scenario` | rc 0 |
| `./dev/lint-boundaries.sh`; `go run ./dev/pincheck` | OK; OK |
| golangci-lint v2.11.4 `--new-from-rev=main` on the touched packages, and `--enable-only=gosec` | 0 issues each (a gocritic `appendAssign` in `identity.go` fixed before the commit) |
| `gofmt -l` on the changed Go files | clean |
| Build job: `go build ./service/cmd/spine`, `embed-smoketest` built and run | ok |
| Codex: `codex review --base main`, plain; review sub-session `01a1a75c-7e7f-7120-bd48-5d025b4f3858`, model `gpt-6.1-sol` | no findings ("patch is correct", confidence 0.88) |
| SMP compatibility: SMP `efe1dec2` with `../spine` = `81790e80` | `go build ./...`; `go vet` plain, `-tags integration`, `-tags scenario ./scenariotest/...`, `-tags faultinject`: all rc 0. `dev/testgate` `-race` spinecore, spineembedded, runner, executionreadiness: 2468 passed, 0 failed, 3 allowed skips. `dev/testgate` githttp `-tags integration`: 594 passed, 1 allowed skip. `go test -race` gitread, projectionwriter, workspace, cmd/customer-app, githttp: all ok. Databases `s24010ci_*` dropped; no `ws_*` database or role left |

**Also seen (not changed here).** 618 commits on the pushed `main` (from 2026-10-06 through
`fd35f802`) carry the author and committer `t <t@t>`, a placeholder identity from earlier agent sessions
(its source is not traced here), now in the public history; rewriting it is the owner's call. `spine init-repo` (operator CLI) still commits
with the operator's identity, and with none it exits 0 without committing (KNOWN-LIMITATIONS).
Statuses are unchanged.
