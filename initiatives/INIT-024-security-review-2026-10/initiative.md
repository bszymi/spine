---
id: INIT-024
type: Initiative
title: Security Review Remediation (2026-10)
status: In Progress
owner: bszymi
created: 2026-10-23
last_updated: 2026-11-05
links:
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/initiative.md
  - type: related_to
    target: /architecture/security-model.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/adr/ADR-016-governance-path-guard.md
---

# INIT-024 — Security Review Remediation (2026-10)

---

## 1. Intent

A security review on 2026-10-22 looked at Spine local `main` `a560c210`. That commit is `61994fe`
plus the INIT-023 remediation, 460 commits ahead of `origin`, and it is not pushed. The review
also looked at SMP `main` `3addd44b`, which embeds Spine `ae513dca`.

**Method.** The reviewers traced the code and compiled PoC tests into the real packages with
`go test -overlay`. They wrote no file into either repository and used no Docker and no network.
The coordinator re-ran both P2 PoCs and both reproduce.

**Verdict on INIT-023.** Every Oct-6 P1/P2 (F1–F10 and the latent gitpool item) is FIXED on
`a560c210` once its opt-in is on. The F1 fix held against 15 bypass variants. Of the 31 Spine-side
P3s, 28 are FIXED and 3 are PARTIAL:

- P3-10: still open in `migrate` envelope mode, by design (INIT-023 owner question 10);
- P3-19: by design (INIT-023 §7d);
- P3-27: `spine serve` never builds `MultiScheduler`.

The remediation code introduced no new P1 or P2.

**New findings: 0 P1, 2 P2, 15 P3.**

- **N-1 (P2, both).** A checkout "git bomb". A push of a few KB checks out or merges into
  terabytes on the disk every tenant shares. INIT-023 TASK-003 bounded reads only.
- **N-2 (P2, standalone shared mode).** A host inside `SPINE_GIT_HTTP_TRUSTED_CIDRS` can clone
  every workspace's repository without a token.
- **N-3 … N-17 (P3).** These are integrity, robustness and hygiene edges. The one that matters
  most for SMP is N-3: separation of duties does not count a planning run's starter as an author.

The review also found supply-chain items for Spine:

- no govulncheck in CI and no Dependabot (SC-06);
- actions on mutable tags, CI images unpinned and `main` unprotected (SC-08);
- an unpinned Dockerfile, no `toolchain` line, and old pgx and x/text (SC-09);
- a Go toolchain two security releases behind (SC-01, Spine half).

This initiative fixes every Spine-side item as discrete tasks that can each be merged on their
own, following INIT-023's conventions and coordination rules (§7b).

**The record.** The reports and PoCs contain working exploit steps, so they are not committed.
The owner holds them in the SMP working tree under the git-ignored
`tmp/security-review-2026-10-22/`: `spine/REPORT.md` with its `spine/scratch/` PoCs,
`supply-chain/REPORT.md` and `SUMMARY.md`. Each task describes its defect and the attacker's
precondition, names the PoC it turns into a regression test, and refers to the report for detail.

**Disclosure.** Spine is a public repository, and N-1 and N-2 are unfixed. These artifacts stay
local until the fixes land (owner question 1).

---

## 2. Scope

### In scope

- Every new Spine finding (N-1 … N-17). Each has exactly one owning task (§7c).
- P3-27's open half: the shared-mode scheduler.
- The Spine half of SC-01, SC-06, SC-08 and SC-09.
- Committed regression tests built from the review PoCs.
- Corrections to `KNOWN-LIMITATIONS.md`, `security-model.md`, `docs/integration-guide.md` and
  `CHANGELOG.md` wherever a fix changes what they say.

### Out of scope

- **SMP-owned code.** That covers SMP's gateway, `gitread` (including the byte budget N-1 needs
  in `CheckTreeDiff`), its history reader, its own merges and checkouts, and its stores. These
  belong to the SMP companion epic (INIT-027 EPIC-005, being scaffolded on
  `INIT-027/EPIC-005/scaffold`) and to SMP EPIC-004's unfinished wave 5. Every task names the SMP
  follow-up it implies under "SMP impact".
- **The F3 gap in SMP.** SMP ships F3 today: one member can self-review a run that unprotects
  `main`. The fix is SMP EPIC-004 TASK-010/011/012, which adopts INIT-023 TASK-006/007. This
  initiative contributes only TASK-001, which must land first (§7a).
- **P3-10 and P3-19.** Both stay PARTIAL by design under INIT-023's rulings (owner question 4).
- **Repository settings.** The `main` ruleset, fork-PR approval and allowed actions are owner
  actions (§7d), not code.
- **New runtime schema.** No task adds a migration. New state goes in existing columns and
  reserved `runs.commit_meta` keys (`spine.*`).

---

## 3. Success Criteria

1. **The P2s are closed.**
   - A run-branch tree over the checkout budget is never checked out or merged by Spine's git
     client, and the standalone push gate refuses it at pre-receive (TASK-003).
   - In shared mode, a trusted-CIDR peer gets token-less reads only for the workspaces its
     address is bound to (TASK-006).
2. **The PoCs no longer reproduce.** Every review PoC fails against the INIT-024 closing commit,
   and each is a committed regression test in its task. Where one cannot be committed, the task
   records why.
3. **SMP is never broken by a bump.** At every SMP `.spine-revision` bump, SMP compiles
   unchanged. Its behaviour is unchanged until it opts in, except for the shared-behaviour
   changes listed in §7b, each of which has a named SMP owner.
4. **Supply chain.** Spine CI runs govulncheck and pins every action and image. Dependabot
   covers `gomod`, `github-actions` and `docker`. The Dockerfile's Go version equals the
   `toolchain` line, and a CI check enforces it.
5. **A follow-up review of the same surfaces is clean,** with no P1 or P2.

---

## 4. Primary Artifacts Produced

- Ten task files under `/initiatives/INIT-024-security-review-2026-10/epics/*/tasks/`.
- New API, each named in its task:
  - `git.TreeBudget`, `git.MeasureTree` and `ErrTreeTooLarge` (TASK-003; SMP-facing);
  - the hidden `spine git-hook pre-receive` subcommand (TASK-003);
  - optional git capabilities found by type assertion (TASK-004);
  - workspace-bound trusted CIDRs (TASK-006);
  - `run.status` evidence provenance fields (TASK-007).
- Updated `KNOWN-LIMITATIONS.md`, `security-model.md`, `docs/integration-guide.md`,
  `CHANGELOG.md`, `.github/workflows/ci.yml`, `.github/dependabot.yml`, `Dockerfile` and `go.mod`.

---

## 5. Constraints and Non-Negotiables

INIT-023 §5 applies unchanged: Constitution §2/§4/§7/§8/§12, one PR per task, no trust in commit
trailers, and no silent behaviour changes. These rules carry over explicitly:

- **No runtime schema.** No task adds a migration, a column or a table.
- **Opt-in for embedders unless the task argues otherwise.** New enforcement in a package SMP
  embeds is an option whose zero value is today's behaviour. A task that ships default-on
  enforcement says why, names the change in §7b, and gives it an SMP owner. TASK-002 (N-7) and
  TASK-003 (N-1) argue for default-on. `spine serve` may turn anything on for standalone
  (owner question 3).
- **No interface breaks for SMP.** No task adds a method to an interface SMP implements.
  - SMP implements `git.GitClient` (`divergencegitclient.go`) and the `StartPlanningRun` shape
    (`artifacts_createentry.go:22`).
  - New capabilities are optional interfaces found by type assertion. SMP must compile
    unchanged at every bump.
- **Every task has an SMP compatibility gate.** One acceptance criterion builds SMP `main` against
  the task branch (`replace ../spine`) and runs SMP's Spine-runtime and gateway suites, as
  INIT-023 TASK-007 AC 19 did.
  - The packages are `cmd/customer-app` and `internal/customer/{githttp,gitread,projectionwriter,spinecore/...,spineembedded/...,stepevents,workspace/...}`,
    plus `./internal/customer/run/...` and `./internal/engine/...`.
  - The `dev/testgate` Spine-runtime leg runs too.
  - Traps: the git-ignored `vendor/` shadows `../spine` (use `-mod=mod` or remove `vendor/`).
    Name packages explicitly, because `./...` with `-mod=mod` matches SMP's `tmp/`.
- **Commit and branch convention.**
  - Commit prefix: `INIT-024 EPIC-00N TASK-NNN: `.
  - Branch: `init/INIT-024-TASK-NNN-<slug>`.
  - Integration branches: `init/INIT-024-waveN-integration`.

---

## 6. Risks

- **Public disclosure before the fix ships.** This repository is public and the artifacts
  describe open attack paths.
  - *Mitigation:* owner question 1 holds the push until TASK-003 and TASK-006 are merged and
    SMP's N-1 half is on SMP `main`. No hosted pilot is live (INIT-027 EPIC-003 is open).
- **A default-on budget refuses legitimate trees.**
  - *Mitigation:* the defaults are about 30× the largest real repository: Spine has 1,624 entries
    and 21 MB, SMP 3,708 entries and 33 MB, measured with `git ls-tree -r -l HEAD`. Every limit
    is configurable.
  - The refusal is a typed, permanent error that names the limit. A refusal never leaves a
    partial checkout behind.
- **The trailing-document refusal quarantines a live workflow** (TASK-002).
  - *Mitigation:* neither repository's corpus contains a `---` line: `workflows/*.yaml` in both,
    plus SMP `workflows/steps/*.yaml`, checked 2026-10-23.
  - An empty trailing document is still accepted. The change is a §7b row with an SMP owner.
- **Three tasks change `adapters/git` and `service/internal/githttp`** (TASK-003, -004, -005).
  - *Mitigation:* §7a orders them and lists the shared files. Whichever lands second rebases.
- **Requiring `SPINE_ENV` breaks unconfigured deployments** (TASK-007).
  - *Mitigation:* the shipped compose files set it in the same PR. The refusal names the three
    valid values. Owner question 2 of TASK-007 offers "unset means production" instead.

---

## 7. Work Breakdown

### Epics

```
/initiatives/INIT-024-security-review-2026-10/
  initiative.md
  /epics/
    /EPIC-001-engine-and-governance/
    /EPIC-002-git-transport-and-reads/
    /EPIC-003-standalone-server-hardening/
    /EPIC-004-supply-chain/
    /EPIC-005-close-out/
```

Task IDs are unique across the initiative, as in INIT-023.

| Epic | Task | Title | Findings | Size | SMP impact |
|---|---|---|---|---|---|
| EPIC-001 | TASK-001 | Separation of duties counts a planning run's starter as an author | N-3 | S | yes (opt-in) |
| EPIC-001 | TASK-002 | Workflow definition strictness: shipped validate steps fail closed, one YAML document, nested workflow directories guarded | N-4, N-5, N-7 | S | yes (N-7 shared) |
| EPIC-002 | TASK-003 | Checkout and merge amplification budget, pre-receive tree limits, SMP-facing `MeasureTree` | N-1 | L | yes (API; own half) |
| EPIC-002 | TASK-004 | Read-path robustness: streamed listings, no DWIM fallback for reserved names, unforgeable log records, bounded evidence diff | N-6, N-10, N-11, N-12 | M | yes (shared listings) |
| EPIC-002 | TASK-005 | Standalone push-gate races: case-folding storage, the review-freeze window, git slot starvation | N-8, N-9, N-13 | M | no (parity note) |
| EPIC-002 | TASK-011 | Aggregate checkout bound: one writer per branch, bounded in-flight checkout bytes (added by the TASK-010 cross-task review) | X-1 | M | no at the bump |
| EPIC-002 | TASK-012 | Push re-check: no answer once the decision's deadline has passed (added after GitHub CI run 42047981897) | R-1 | S | no |
| EPIC-003 | TASK-006 | Workspace-scoped trusted-CIDR clone in shared mode | N-2 | S | no |
| EPIC-003 | TASK-007 | Session and auth hygiene: SSE revalidation, no pre-auth ServiceSet, explicit `SPINE_ENV`, evidence provenance | N-14, N-15, N-16, N-17 | M | no |
| EPIC-003 | TASK-008 | Wire the multi-workspace scheduler into shared-mode `spine serve` | P3-27 (open half) | M | no |
| EPIC-004 | TASK-009 | Toolchain and dependency bumps, Dockerfile and CI pinning, govulncheck, Dependabot | SC-01 (Spine), SC-06, SC-08, SC-09 | S | yes (MVS) |
| EPIC-004 | TASK-013 | Dependency refresh: postgres digest, the gomod group (pgx v5.11.0), LocalStack 4.14.0 (replaces Dependabot #602 and #604) | — | S | yes (MVS; `TypeMap` on SMP's fakes) |
| EPIC-005 | TASK-010 | Initiative close: integration, gates, documentation and the SMP hand-off | — | S | yes (closing pin) |

**Grouping notes.**

- The closing task has its own epic (EPIC-005), because its acceptance spans every epic.
- `golang.org/x/net` is not bumped. It is not in Spine's module graph: `go list -m all` at
  `a560c210` lists `x/sys` and `x/text`, and `go.sum` has no `x/net` line. The x/net finding
  belongs to SMP.
- N-17 stays in TASK-007, the standalone `run.status` surface. Its "deleted run branch" half is
  closed by TASK-004's N-10 fix.
- TASK-008 is a functional gap more than a security finding. It is kept because it is the only
  Spine PARTIAL with a code fix, and because INIT-023 promised it in `KNOWN-LIMITATIONS.md:1382`
  (owner question 5).

---

## 7a. Waves and file overlaps

| Wave | Tasks | Notes |
|---|---|---|
| 1 | TASK-001 | Small and self-contained. It merges to Spine `main` **before the INIT-023 closing commit**, so SMP EPIC-004 TASK-012's closing bump carries it and SMP TASK-011's separation of duties gets it (owner question 2). |
| 2 | TASK-002, TASK-003, TASK-006, TASK-007, TASK-008, TASK-009 | In parallel. TASK-009 goes first in practice, so later tasks test on the final toolchain. |
| 3 | TASK-004, TASK-005 | Both rebase on TASK-003 (shared files below). |
| 4 | TASK-010 | Closes the initiative and names the closing commit. |

**Hard dependencies.** Only TASK-010, which is blocked by every other task. Everything else is
soft ordering. Whichever task lands second rebases.

**Shared files.**

| File | Tasks | How they coexist |
|---|---|---|
| `core/engine/governance_guard.go` | TASK-001 (`recordLifecycleStarter` → planning-starter record), TASK-002 (`buildGovernanceCheck` path set) | Different functions |
| `core/engine/run.go` | TASK-001 only | — |
| `adapters/git/cli.go` | TASK-003 (`Checkout`, `Merge`, `runMerge`), TASK-004 (`ListFiles`, `Diff`, `Log`, `parseLogOutput`, `HasCommitWithTrailer`, new `CountChangedPaths`) | Different functions. TASK-004 also takes TASK-003's `TreeBudget.MaxPathBytesTotal` invariant. |
| `adapters/git/limits.go` | TASK-003 (budget defaults beside the read caps), TASK-004 (streamed-listing sink) | Additive |
| `adapters/git/refgrammar.go` | TASK-004 only | — |
| `service/internal/githttp/handler.go` | TASK-003 (`ensureReceivePackConfig` installs the hook; CGI env), TASK-005 (`ensureReceivePackConfig` storage probe; semaphore order; tail hold-back) | TASK-005 rebases on TASK-003: `ensureReceivePackConfig` grows two steps |
| `service/cmd/spine/serve_config.go` | TASK-006 (trusted-CIDR parsing), TASK-007 (`resolveRuntimeEnv`) | Different functions |
| `service/cmd/spine/serve_wiring.go` | TASK-006 (git handler config), TASK-008 (scheduler) | Different blocks |
| `service/cmd/spine/main.go` | TASK-003 (registers `git-hook`) | — |
| `docker-compose*.yaml` | TASK-007 (`SPINE_ENV=development`), TASK-009 (image digests) | Different keys |
| `KNOWN-LIMITATIONS.md`, `CHANGELOG.md`, `security-model.md` | all | Each task edits its own section. TASK-010 reconciles. |

---

## 7b. Cross-repository coordination

INIT-023 §7b Rules 1, 3 and 5 apply unchanged:

- a Spine task lands first, then SMP's adoption, then the bump;
- every bump moves forward on purpose and records each INIT-024 task in its range;
- every bump runs the gates at the exact pair.

**Rule 2: what SMP does per task.** The adopter is the matching INIT-027 EPIC-005 task; numbers
are cross-filled when both scaffolds land.

| Spine task | SMP action |
|---|---|
| TASK-001 | If SMP turns separation of duties on (SMP TASK-011 owner question 2), `CreateEntry` must put the member on the context with `domain.WithActor`, under the same handle the step-entry resolver binds, before `StartPlanningRun`. Otherwise planning starts are refused (fail closed). |
| TASK-002 | Re-run the workflow corpus and the projection suites (N-7 is shared). SMP's own validate steps already use `invalid` (N-4 is inert). |
| TASK-003 | SMP's own N-1 half: a byte budget in `gitread.CheckTreeDiff`, in `boundRunBranchCheckout` and in `RealGitOperator.MergeBound`. SMP may call `git.MeasureTree` or keep its own measure, plus a differential test against `git.TreeBudget.Check`. |
| TASK-004 | Re-run the projection and gitread suites (streamed `ListFiles`/`Diff` are shared). SMP's own `artifacthistoryreader.go:178` uses the same `---END---` separator: SMP-side fix. |
| TASK-005 | Parity check of SMP's own gateway for case-folding storage, the freeze window and slot starvation. SMP-side. |
| TASK-006, TASK-007, TASK-008 | Standalone only. Gates only. |
| TASK-009 | The bump runs `go mod tidy`. MVS raises SMP's pgx (v5.9.2), x/text (v0.42.0) and chi (v5.2.5) to Spine's new floors. |

**Rule 4: shared-behaviour changes.** These reach SMP at the first bump past the task, whatever
the opt-in.

| Spine task | What changes for SMP | SMP owner |
|---|---|---|
| TASK-002 | `workflow.Parse` refuses a second non-empty YAML document. The projection quarantines such a file (INIT-023 TASK-011 behaviour), and the workflow writers refuse it. | SMP companion task for TASK-002 (corpus and projection gate) |
| TASK-003 | Spine's `CLIClient.Checkout`/`Merge`/`MergeBound` and `EnterBranch` refuse an over-budget tree. SMP reaches `EnterBranch` only in place (branch `""`), and runs its own merges, so in practice only `git.MeasureTree` becomes available. | SMP companion task for TASK-003 |
| TASK-004 | `ListFiles` and `Diff` no longer fail on total output size, only on the size of what they keep. A bare reserved-namespace name with no `refs/heads/<n>` is NotFound instead of DWIM. | SMP companion task for TASK-004 |
| TASK-009 | Dependency floors rise (MVS). | The SMP bump task |

---

## 7c. Coverage

"Both" means the surface is reachable in embedded SMP and in standalone Spine.

| ID | Sev | Surface | Defect (one line) | Owner |
|---|---|---|---|---|
| N-1 | P2 | both | Checkouts and merges of a run-branch tree have no byte or entry budget, so a tiny push fills the shared disk | TASK-003; SMP companion (CheckTreeDiff byte sum) |
| N-2 | P2 | standalone shared | The trusted-CIDR bypass is decided from the peer IP alone, for every workspace | TASK-006 |
| N-3 | P3 | engine (in pin) | `runAuthor` ignores whoever started a planning run and wrote its content | TASK-001 |
| N-4 | P3 | standalone shipped corpus | Validate steps in `adr-`, `artifact-` and `document-creation.yaml` set `timeout_outcome: valid` | TASK-002 |
| N-5 | P3 | embedders with `artifacts_dir` | The guard skips `<artifacts_dir>/workflows/` unless the embedder passes `WorkflowDirs` | TASK-002 |
| N-6 | P3 | both (SMP ships it) | The 16 MiB per-command output cap fails every sync of a tree with a huge listing | TASK-004 |
| N-7 | P3 | both | A trailing YAML document in a workflow file is accepted and ignored | TASK-002 |
| N-8 | P3 | standalone, case-insensitive storage | `refs/heads/MAIN` aliases `main` and bypasses its protection | TASK-005 |
| N-9 | P3 | standalone | Push admission is decided before the pack arrives, so a pack held back past the review freeze lands | TASK-005 |
| N-10 | P3 | both | Rev-class reads fall back to a tag or `refs/<n>` once `refs/heads/<n>` is gone | TASK-004 |
| N-11 | P3 | standalone | `---END---` inside a commit message forges `/query/history` entries | TASK-004 |
| N-12 | P3 | standalone | The output-evidence diff of an author-chosen commit pair runs without `GIT_ALLOC_LIMIT` | TASK-004 |
| N-13 | P3 | standalone shared | One process-wide 5-slot git semaphore is taken before the push body read is deadlined | TASK-005 |
| N-14 | P3 | standalone | An open SSE stream keeps delivering after token revocation or actor suspension | TASK-007 |
| N-15 | P3 | standalone shared | Unauthenticated requests build a workspace ServiceSet, and its 500 is an existence oracle | TASK-007 |
| N-16 | P3 | standalone | An unset or mistyped `SPINE_ENV` silently runs non-production | TASK-007 |
| N-17 | P3 | standalone | `run.status` evidence for active runs is read from the author-writable run branch and shown as fact | TASK-007 |
| P3-27 | P3 | standalone shared | `MultiScheduler` is never built by `spine serve` (no timeouts, orphan scan or merge retry in db/platform-binding modes) | TASK-008 |
| P3-10 | P3 | embedded | `enc:v1` is accepted in `migrate` mode | No task: closed only in `strict` (INIT-023 OQ10; owner question 4) |
| P3-19 | P3 | both | `required_outputs` are self-attested | No task: INIT-023 §7d (owner question 4) |
| SC-01 | P2 (SMP) | Spine half | Toolchain two security releases behind (GO-2026-6809 et al.) | TASK-009 |
| SC-06 | P3 | Spine | No govulncheck, no Dependabot | TASK-009 |
| SC-08 | P3 | Spine | Actions on mutable tags, unpinned CI images, no top-level `permissions` | TASK-009; repository settings are owner actions (§7d) |
| SC-09 | P3 | Spine | Unpinned Dockerfile bases, no `toolchain` line, pgx v5.8.0, x/text v0.29.0 | TASK-009 |

**Checked and not turned into tasks.** The review's "checked and dismissed" list stands (§(b) of
the report). `x/net` is not a Spine dependency (§7, grouping notes).

---

## 7d. Program owner questions

Each task works with its stated default. Tasks cite these as "INIT-024 owner question N".

1. **Landing and push order.**
   - *Default:* nothing containing these artifacts is pushed until TASK-003 and TASK-006 are
     merged on Spine `main` and SMP's N-1 half is on SMP `main`. INIT-023's own push gate still
     applies to the commits underneath (SMP wave 5 closes F3).
2. **TASK-001 versus INIT-023's close.**
   - *Default:* TASK-001 merges before the INIT-023 closing commit, so SMP EPIC-004 TASK-012's
     closing bump contains it. SMP TASK-011 records it as "adopted here".
   - *Alternative:* SMP bumps twice.
3. **Standalone defaults for new enforcement.**
   - *Default:*
     - the tree budget is on with no off switch, only larger limits;
     - pre-receive limits are on whenever receive-pack is on;
     - unbound trusted CIDRs refuse startup in shared mode;
     - `SPINE_ENV` is required;
     - SSE streams revalidate on every heartbeat;
     - the scheduler runs in every mode.
4. **P3-10 and P3-19 stay PARTIAL.**
   - *Default:* accept, per INIT-023 OQ10 and INIT-023 §7d.
   - *Alternative:* flip `spine serve` to `SPINE_SECRET_ENVELOPE_MODE=strict` by default, as a
     separate task.
5. **TASK-008 belongs here.**
   - *Default:* keep it, labelled functional.
   - *Alternative:* move it to a functional backlog initiative.

**Owner-only actions** (repository settings, no task):

- a Spine `main` ruleset: PR required, CI required, no force push;
- require approval for all outside contributors (currently first-time contributors only);
- GitHub "allowed actions: selected" with SHA pinning required, once TASK-009 has pinned everything;
- revoke the PAT behind SMP's unused `SPINE_REPO_TOKEN` secret and delete the secret (SMP repository).

---

## 7e. Progress

- 2026-10-23: scaffolded. All artifacts are Pending. Not pushed (owner question 1).
- 2026-10-31: TASK-001 … TASK-009 merged on Spine local `main` (`854a1e08`). TASK-010 ran the
  closing gates, two codex rounds and the SMP compatibility gate at the pair (all green),
  reconciled the shared docs and recorded the SMP hand-off and the owner-question defaults. The
  cross-task review (two skeptics: N-1 and N-2 closed) added TASK-011 (aggregate checkout bound),
  merged on Spine local `main` (`ea77cff6`) and into the close branch. Tasks stay In Progress until
  GitHub CI runs on the pushed closing commit. Not pushed (owner question 1: hold).
- 2026-11-04: pushed with the owner's approval (owner question 1): Spine `main` `fd35f802` (the
  closing commit), then `ce0b9c9e` (TASK-010's git-identity fix, found by the first run). GitHub CI
  run 41875102030 at `ce0b9c9e` passed all 8 jobs; the first run, 41856447149 at `fd35f802`, failed
  only `go test ./...`, on that identity defect. TASK-001 … TASK-008 and TASK-011 are Completed, and
  EPIC-001, EPIC-002 and EPIC-003 with them. TASK-009 stays In Progress for one live check (AC 8,
  the compose bring-up on a host that may pull), so EPIC-004, TASK-010 (its AC 1), EPIC-005 and the
  initiative are In Progress. With the owner: the repository settings in §7d, and the owner
  questions, which stay at their recorded defaults.
- 2026-11-05: GitHub CI run 42047981897 at `main` `39841330` (an INIT-023 status commit) failed one
  subtest of TASK-005's apply-time re-check, which showed that the re-check server could still
  answer just after a decision's deadline. TASK-012 (EPIC-002) was added and is Completed: the server
  now sends no answer once the decision's context has ended. EPIC-002 stays Completed; SMP's pin is
  unaffected (`service/internal` is not importable by SMP).
- 2026-11-05: the owner ruled to replace Dependabot PRs #602 (postgres digest) and #604 (the gomod
  group, with pgx v5.11.0) with our own fixed change, and to move LocalStack to 4.14.0. TASK-013
  (EPIC-004) is added for it.

---

## 8. Exit Criteria

INIT-024 may be marked Completed when:

- every task is Completed, or Cancelled by the owner with a recorded reason;
- every task's SMP compatibility gate passed at merge;
- every review PoC named in a task is a committed regression test (or the task says why not), and
  none reproduces at the closing commit;
- the owner questions (§7d and per task) are answered, or their defaults are recorded in each
  task's Implementation Notes;
- `KNOWN-LIMITATIONS.md`, `security-model.md` and `docs/integration-guide.md` match the shipped
  behaviour;
- the closing commit is named in TASK-010's Implementation Notes, for SMP's pin;
- GitHub CI has run green on the pushed closing commit. Until then, tasks stay In Progress, as in
  INIT-023.

---

## 9. Links

- Previous remediation: `/initiatives/INIT-023-security-audit-remediation/initiative.md`
- Security model: `/architecture/security-model.md`
- Branch protection: `/architecture/adr/ADR-009-branch-protection.md`
- Governance-path guard: `/architecture/adr/ADR-016-governance-path-guard.md`
- Review record (git-ignored, SMP working tree): `tmp/security-review-2026-10-22/`
- SMP companion: INIT-027 EPIC-005 (scaffold branch `INIT-027/EPIC-005/scaffold`); SMP EPIC-004
  wave 5 (TASK-010, -011, -012)
