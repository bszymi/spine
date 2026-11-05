---
id: INIT-023
type: Initiative
title: Security Audit Remediation
status: In Progress
owner: bszymi
created: 2026-10-06
last_updated: 2026-11-05
links:
  - type: related_to
    target: /initiatives/INIT-018-branch-protection/initiative.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-008-workflow-lifecycle-governance.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/security-model.md
  - type: related_to
    target: smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/epic.md
---

# INIT-023 — Security Audit Remediation

---

## 1. Intent

A security audit on 2026-10-06 reviewed Spine at `61994fe` and the Spine Management Platform
(SMP) at `80995559`, which embeds Spine `61994fe` in-process (`.spine-revision`).

- **Method.** Code review, plus proof-of-concept tests compiled against the real Spine and SMP
  packages with `go test -overlay`. No file was written into either repository.
- **Verification.** Two independent skeptics re-checked every elevated finding by re-running or
  rebuilding its PoC. Severities are the lower of the two votes.
- **Limits.** No Docker and no live network were used. Claims that need a running stack are
  code-traced, and the tasks say so where it matters.

**Verdict, in summary.** The invariant Spine exists to protect, governed change to `main`
(ADR-009, Constitution §4), has integrity defects that an insider can exploit:

- a workspace member;
- a run-branch author (an AI agent, a runner job, a contributor);
- the holder of a compromised runner credential.

There is one P1 and there are nine P2s:

- **F1 (P1).** A crafted push bypasses branch protection on `main`. Through the same push, the
  member can rewrite `.spine/branch-protection.yaml` and turn protection off for good.
- **F2 (P2).** The governed merge is not bound to the commit that was reviewed.
- **F4 (P2).** A small push can exhaust the memory of the shared embedded process, which is a
  cross-tenant availability defect.

The audit found no unauthenticated or cross-tenant *integrity* break in the embedded deployment.
Four P2s (F6–F9) reach only standalone Spine-as-a-service. Most of the crypto and secrets surface
is sound. 37 P3 hardening items and one latent item (refuted as unreachable today) complete the
record.

This initiative fixes every Spine-side finding as discrete, individually mergeable tasks. Each
task ships with its PR, its codex pass and its regression tests. The PoCs become those tests.

**The audit record.** The full report and its PoCs contain working exploit steps, so neither is
committed to either repository. The owner holds them, in the SMP working tree under the
git-ignored `local-scratch/spine-audit/` (`REPORT.md`, `poc/`). This initiative
summarises each finding in one line (§7c). Each task restates only what its fix needs.

**The SMP half.** SMP owns its own git gateway, its git plumbing and its event receiver, and it
adopts each Spine change deliberately. Its remediation is
`smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/epic.md`
(12 tasks). The two plans share one wave schedule (§7a), one set of coordination rules (§7b) and
one numbered list of owner questions (§7e).

---

## 2. Scope

### In scope

- Every confirmed finding (F1–F10) and every P3 item (P3-1 … P3-37) that has a Spine half, plus
  the latent gitpool token-host item. Each has exactly one owning Spine task (§7c).
- A new ADR that changes ADR-009 §1: a governed merge that touches governance paths is no
  longer allowed unconditionally (TASK-006).
- Corrections to documentation the audit contradicted:
  - `KNOWN-LIMITATIONS.md` claims that `safePath` resolves symlinks (F5);
  - `security-model.md` §4.1 and `actor-model.md` say skills are managed by an admin (F6);
  - ADR-008 §1/§5 and `api-operations.md` describe the reviewer workflow path (F7).
- Committed regression tests built from the audit PoCs, in the task that owns each finding.

### Out of scope

- **SMP-owned code**: SMP's `/git/{workspace_id}` gateway, its git plumbing, its step-event
  receiver and its own stores. That is INIT-027 EPIC-004. SMP-only items (P3-2, P3-7, P3-8,
  P3-18, P3-20, P3-34) are listed in §7c for the coverage record only.
- **The descoped and narrowed sub-items in §7d**, each with its reason.
- **New runtime schema.** No task adds a migration. New state uses existing columns and
  reserved `runs.commit_meta` keys (`spine.*`).
- **Wiring a code-repository catalog or a `RepositoryManager` into `spine serve`** (INIT-014
  deferrals). The dormant code-repository paths are hardened in place: TASK-002 (P3-36),
  TASK-005 (P3-37) and TASK-015 (the latent gitpool item).
- **Areas the audit did not cover.** These are git pack and fsck internals, `.gitattributes`
  merge drivers at merge time, `core/checkrunner` and `core/evidence` ref handling, and
  `HasCommitWithTrailer` matching. They go to the follow-up review in §8, not to tasks here.

---

## 3. Success Criteria

This initiative is successful when:

1. **F1 is closed in both gateways in wave 1.** A ref carrying a NUL or other control byte, on
   any command line, is refused before branch protection is evaluated. In Spine that is TASK-001;
   in SMP it is EPIC-004 TASK-001.
2. **"Approved" means "what was approved got merged".** With merge binding on, the commit merged
   to `main` is the commit that passed validation and review (TASK-005). A change to a
   governance path merges only after an operator approves that exact commit (TASK-006).
3. **The PoCs no longer reproduce.** Every audit PoC fails to reproduce against the INIT-023
   closing commit, and is a committed regression test in its task. Where a PoC cannot be
   committed as a test, the task records why.
4. **SMP is never broken by a bump.** At every SMP `.spine-revision` bump, SMP's behaviour is
   unchanged until SMP opts in. The only exceptions are the shared-behaviour changes listed in
   §7b, and each has a named SMP owner.
5. **A follow-up review is clean.** A security review of the same surfaces after INIT-023 closes
   reports no P1 or P2.

---

## 4. Primary Artifacts Produced

- 17 task files under `/initiatives/INIT-023-security-audit-remediation/epics/*/tasks/`.
- New packages:
  - `adapters/git/refname`, one strict refname grammar (TASK-001, reused by TASK-002);
  - `core/worktreefs`, confined worktree I/O (TASK-004);
  - `core/refadmit`, ref-class push admission (TASK-016).
- `architecture/adr/ADR-016-governance-path-guard.md` (TASK-006; the number is assigned at
  merge).
- New opt-in options. The zero value of each is today's behaviour:
  - `Orchestrator.EnableMergeBinding` (TASK-005);
  - `WithGovernanceGuard` and `WithProtectionRemovalGuard` (TASK-006);
  - `WithStepEntryPolicy` (TASK-007);
  - `SubmitRequest.ActorID` and an optional step compare-and-swap capability (TASK-008);
  - `WithStatusLifecycle` and `WithBlockingStatusConflicts` (TASK-017);
  - `WithSignatureVersion("v2")` (TASK-010);
  - `WithAuthoritativeBranch` and `WithStrictWorkflowValidation` (TASK-011);
  - the `enc:v2` key ring and envelope modes (TASK-009).
- Updated `security-model.md`, `KNOWN-LIMITATIONS.md`, `docs/integration-guide.md`,
  `CHANGELOG.md` and the affected ADRs (ADR-008, ADR-009).

---

## 5. Constraints and Non-Negotiables

This initiative must comply with the Spine Constitution, including:

- **§2 Source of Truth and §4 Governed Execution.** The fixes restore the guarantee that `main`
  changes only through a defined workflow or an audited operator override.
- **§7 Reproducibility.** Every fix carries a regression test derived from its PoC, or records
  why one is impractical.
- **§8 Disposable Database.** There is no runtime schema change. Binding, evidence and approval
  state live in existing columns and reserved `commit_meta` keys.
- **§12 Structural Integrity.** Documentation the audit contradicted is corrected in the same
  PR as the fix.

Additional constraints:

- **Wave 1 is small and fast.** TASK-001 is one PR. It has no migration, no configuration
  change and no API change, and it lands before any other INIT-023 task.
- **Opt-in by default for embedders.** New enforcement is an option whose zero value is
  today's behaviour, so SMP adopts it deliberately. `spine serve` may turn it on for standalone
  (owner question 10).
- **No interface breaks for SMP.** No task adds a method to an interface SMP implements.
  - New capabilities are optional interfaces found by type assertion. Examples are
    `CommitBinder` (TASK-005) and the step compare-and-swap port (TASK-008).
  - SMP must compile unchanged at every bump.
- **Every task has an SMP compatibility gate.** One acceptance criterion builds SMP `main` and
  runs its named suites against the task branch, with `replace ../spine`. Two traps apply:
  - SMP's git-ignored `vendor/` silently shadows `../spine`. Use `-mod=mod` or remove
    `vendor/`.
  - Name packages explicitly. With `-mod=mod`, `./...` also matches the Go files under SMP's
    `tmp/` and rewrites `go.mod`.
- **No trust in commit trailers for governance.** Any run-branch author can forge a trailer, so
  binding, approval and provenance are server-side records only.
- **One PR per task.** A task that grows a second concern splits rather than bundles.
- **No silent behaviour changes.** Every externally visible change (HTTP status, error class,
  log or event shape, wire format) is named in the task and in `CHANGELOG.md`.

---

## 6. Risks

- **Public disclosure before the fix ships.** Spine is a public repository. These artifacts
  describe attack paths for findings that stay open in SMP until its adoption tasks land.
  - *Mitigation:* owner question 1 sets the landing order. Wave 1 closes the P1 in both
    gateways first. No hosted pilot is live yet (INIT-027 EPIC-003 is open).
- **Parser and grammar changes refuse legitimate pushes or paths.**
  - *Mitigation:* every grammar is checked against real git (an `AgreesWithGit` corpus). A
    before/after `git push` acceptance test covers multi-ref pushes, push options and deletes.
- **A shared-behaviour change breaks SMP at a bump** (`-z` diffs, webhook dead-lettering,
  strict projection).
  - *Mitigation:* §7b lists each change with its SMP owner and the ordering that keeps SMP
    `main` green.
- **Opt-in policies are never turned on.** That would leave the findings open in practice.
  - *Mitigation:* SMP TASK-012 records an opt-in ledger, saying which options SMP turns on and
    why the others stay off. Owner question 10 sets the standalone defaults.
- **Engine changes interleave.** TASK-005, -006, -007, -008, -012 and -017 all touch
  `core/engine` merge and submit paths.
  - *Mitigation:* the waves and hard dependencies in §7a fix the order. Each task names its
    insertion points relative to its predecessors, and whichever lands second rebases.
- **Shipped workflow corpora fail stricter checks.** 10 of the 15 shipped workflow files fail
  `workflow.Validate` today.
  - *Mitigation:* `Validate` stays advisory (TASK-011; owner question 7). Workflow fixes that
    TASK-007 and TASK-017 need are made in the task that needs them, with a compatibility gate
    over both corpora.

---

## 7. Work Breakdown

### Epics

```
/initiatives/INIT-023-security-audit-remediation/
  initiative.md
  /epics/
    /EPIC-001-git-transport-and-content-integrity/
    /EPIC-002-governance-integrity/
    /EPIC-003-secrets-delivery-and-projection/
    /EPIC-004-standalone-service-hardening/
```

Task IDs are permanent. TASK-016 and TASK-017 were split out after the first numbering, so IDs
do not follow epic or wave order.

### EPIC-001 — Git Transport and Content Integrity

Purpose: make the git surface trustworthy.

- The pre-receive gate evaluates the ref git actually applies (F1, the P1).
- One ref grammar for the git CLI client: branch-typed arguments are qualified, every ref
  argument is validated and passed after `--end-of-options`, and dormant `default_branch`
  values are validated (F10 client half, P3-32, P3-36).
- The standalone gateway admits pushes by ref class: run, plan, divergence, init and non-branch
  refs (F2 push half, F10 gateway half).
- Blob reads are bounded and listings are NUL-delimited (F4, P3-9).
- Worktree writes are confined against symlinked path components (F5).

| Task | Title | Findings | Wave |
|---|---|---|---|
| TASK-001 | Reject NUL/control-byte and invalid refs on every pre-receive command line | F1 | 1 |
| TASK-002 | One ref grammar for the git CLI client: qualified branch arguments, validated refs, `--end-of-options` | F10 (client), P3-32, P3-36 | 2 |
| TASK-004 | Confine worktree writes against symlinked path components and `.git/` | F5 | 2 |
| TASK-016 | Ref-class push admission in the standalone git gateway | F2 (push), F10 (gateway) | 2 |
| TASK-003 | Bounded blob reads and NUL-delimited `Diff`/`ListFiles` in the git CLI client | F4, P3-9 | 3 |

### EPIC-002 — Governance Integrity

Purpose: make "approved" mean "what was approved got merged".

- Bind the governed merge to the commit that passed the gates (F2 merge half, P3-37).
- Guard governance files from riding a governed merge into live policy (F3). This needs an ADR
  that changes ADR-009 §1.
- Add a step-entry policy for assign and claim (P3-3, P3-4, P3-15, P3-16).
- Add submit ownership with evidence, and compare-and-swap writes (P3-17, the evidence half of
  P3-19, P3-24).
- Add an artifact-status lifecycle with a run-owned cascade (P3-21).

All new enforcement is opt-in.

| Task | Title | Findings | Wave |
|---|---|---|---|
| TASK-007 | Opt-in step-entry policy for assign and claim: run state, eligibility, mode, preconditions, separation of duties | P3-3, P3-4, P3-15, P3-16 | 2 |
| TASK-005 | Bind the governed merge to the commit that passed validation and review | F2 (merge), P3-37 | 3 |
| TASK-006 | Governance-path guard on governed merges and protection-removal guard in projection | F3 | 4 |
| TASK-008 | Submit ownership, step-output evidence and compare-and-swap state writes | P3-17, P3-19 (evidence), P3-24 | 4 |
| TASK-017 | Declared artifact-status lifecycle and run-owned merge cascade (opt-in) | P3-21 | 4 |

### EPIC-003 — Secrets, Delivery and Projection

Purpose:

- Add the `enc:v2` secret envelope (key id, AAD, key ring) and a re-encryption sweep (P3-10,
  P3-1).
- Add an opt-in v2 webhook signature that matches SMP's receiver contract, plus fixes to the
  delivery queue, the circuit breaker, the SSRF oracle and URL redaction (P3-11, P3-12, P3-5,
  P3-25, P3-26).
- Close the projection's parser differential and its silent sync gaps (P3-13 narrowed, P3-14,
  P3-22, P3-23).

| Task | Title | Findings | Wave |
|---|---|---|---|
| TASK-009 | `enc:v2` secret envelope (key id + AAD + key ring) and subscription-secret re-encryption sweep | P3-10, P3-1 | 2 |
| TASK-010 | Opt-in v2 webhook signatures matching SMP's receiver, plus delivery-queue, breaker, SSRF and redaction fixes | P3-11, P3-12, P3-5, P3-25, P3-26 | 2 |
| TASK-011 | Projection sync integrity: close the workflow parser differential, stop dropped `Diff` errors, follow the authoritative ref, scope `artifacts_dir` | P3-13, P3-14, P3-22, P3-23 | 2 |

### EPIC-004 — Standalone Service Hardening

Purpose: close the findings that reach only standalone Spine-as-a-service. The shared packages
SMP embeds stay compile- and behaviour-compatible.

- Convergence target and shared-working-tree safety, with bounded merge retries (F8, F9, P3-27).
- Skill and actor authority, with bootstrap-token hygiene (F6, P3-31, P3-33, P3-35).
- Reviewer workflow-update and foreign `write_context` bypasses (F7, P3-6).
- The remaining gateway and credential surfaces, including the latent gitpool token-host
  scoping (P3-28, P3-29, P3-30).

| Task | Title | Findings | Wave |
|---|---|---|---|
| TASK-013 | Admin-only skill management, actor role ceiling, and bootstrap-admin token hygiene | F6, P3-31, P3-33, P3-35 | 2 |
| TASK-014 | Reviewer workflow updates go through governance; `write_context` limited to the run's assignee | F7, P3-6 | 3 |
| TASK-015 | SSE field safety, shared-mode tenant scoping, git-endpoint auth and gitpool token-host scoping | P3-28, P3-29, P3-30, latent gitpool | 3 |
| TASK-012 | Standalone git-state safety: explicit convergence target, serialized working tree, bounded merge retries | F8, F9, P3-27 | 4 |

---

## 7a. Waves (both repositories)

One schedule covers INIT-023 and SMP INIT-027 EPIC-004. A task starts when its wave opens
**and** its hard dependencies have merged. Tasks in the same wave run in parallel.

| Wave | Spine INIT-023 | SMP INIT-027 EPIC-004 |
|---|---|---|
| 1 | TASK-001 | TASK-001 (F1 in SMP's gateway), TASK-002, TASK-003. No Spine dependency. |
| 2 | TASK-002, TASK-004, TASK-016 (EPIC-001); TASK-007 (EPIC-002); TASK-009, TASK-010, TASK-011 (EPIC-003); TASK-013 (EPIC-004) | TASK-004 (cross-tenant OOM and symlink fix in SMP's own plumbing; no Spine dependency) |
| 3 | TASK-003, TASK-005, TASK-014, TASK-015 | TASK-005, TASK-006 (first `.spine-revision` bump) |
| 4 | TASK-006, TASK-008, TASK-017, TASK-012 | TASK-007 (gated on owner question 3), TASK-008, TASK-009 |
| 5 | — | TASK-010, TASK-011 |
| 6 | INIT-023 closes | TASK-012 (bump to the INIT-023 closing commit) |

**Hard dependencies (Spine).**

| Task | Blocked by | Soft or ordering notes |
|---|---|---|
| TASK-001 | — | Lands first in practice. It owns `adapters/git/refname`. |
| TASK-002 | — | Reuses TASK-001's grammar. If it is built first, it lands the grammar exactly as TASK-001 specifies. There must never be two grammars. |
| TASK-004 | — | |
| TASK-016 | — | After TASK-001 by preference. Its owner question 1 must equal SMP TASK-007's (owner question 3). |
| TASK-007 | — | |
| TASK-009 | — | |
| TASK-010 | — | SMP TASK-006's R1 and R2 must be on SMP `main` before any SMP bump past this task (§7b). |
| TASK-011 | — | |
| TASK-013 | — | |
| TASK-003 | TASK-002 | Merges to Spine `main` only after **SMP TASK-003 is on SMP `main`**. It rebases on TASK-011. |
| TASK-005 | TASK-002 | Its ref grammar must accept full object ids. |
| TASK-014 | TASK-004 | Soft: TASK-016 (shared bound-actor predicate). TASK-007 for its AC 19. |
| TASK-015 | TASK-016 | |
| TASK-006 | TASK-005, TASK-011 | Soft: TASK-003. Its guard plugs in after TASK-005's `resolveMergeSource`. |
| TASK-008 | TASK-005, TASK-007 | It uses TASK-005's head capture and `commit_meta` contract. |
| TASK-017 | TASK-005 | Needs TASK-011 for `artifacts_dir` deployments. |
| TASK-012 | TASK-002, TASK-004, TASK-005 | |

---

## 7b. Cross-repository coordination

**Rule 1 — order.** A Spine task lands first. Then SMP's adaptation task lands. Then the SMP
`.spine-revision` bump, which is the adaptation task's final commit.

**Rule 2 — which SMP task adopts each Spine task.**

| Spine task | SMP adopter | What SMP does |
|---|---|---|
| TASK-001 | SMP TASK-001 (parity, independent); SMP TASK-012 | SMP fixes its own gateway copy in wave 1. At the closing bump, SMP swaps its temporary grammar for `refname.ValidateQualified` and adds a pin test for the `Evaluate` second layer. |
| TASK-002, TASK-003, TASK-004 | SMP TASK-008 | Boot-time git version check, too-large error mapping, and regression tests at SMP's call sites |
| TASK-005 | SMP TASK-009 | `RealGitOperator` implements `CommitBinder`; SMP enables binding |
| TASK-006 | SMP TASK-010 | Both guards on, plus the admin approval API and UI |
| TASK-007, TASK-008, TASK-017 | SMP TASK-011 | Step-entry policy, submitter identity, step compare-and-swap, status lifecycle |
| TASK-009, TASK-010 | SMP TASK-006 | Encrypt step-event secrets by default, `enc:v2`, then require v2 signatures |
| TASK-011 | SMP TASK-012, and the first bump that contains it | Validate-preview parity, corpus test, backfill-probe change (Rule 4) |
| TASK-016 | SMP TASK-007 (mirrors the rule); SMP TASK-012 | Same admission table; a differential test against `core/refadmit` |
| TASK-012 … TASK-015 | SMP TASK-012 (gates only) | Standalone-only. SMP re-runs the named compatibility gates at the closing pin. |

**Rule 3 — every SMP bump moves forward on purpose.**

- Spine `main` is linear, so an SMP bump inherits everything merged before its target.
- A bump never moves backwards.
- It targets the earliest Spine `main` commit that contains what it adopts and descends from
  the current pin.
- It records every INIT-023 task in its range as one of:
  - inert for SMP;
  - adopted here;
  - adopted by an earlier bump (named);
  - an obligation carried here.
- It does not bump past a non-additive Spine change whose SMP adoption has not merged. Each
  change has one owner.

**Rule 4 — shared-behaviour changes.** These reach SMP at the first bump past their merge,
whatever the opt-in. Each has an owner, and some have an ordering constraint.

| Spine task | What changes for SMP | SMP owner and ordering |
|---|---|---|
| TASK-001 | `branchprotect.Evaluate` denies branch names that hold a control byte. This is a second layer behind SMP's own parser fix. | SMP TASK-012 (pin test) |
| TASK-002 | Argv terminators and qualified refs. AddToRun now resolves `refs/heads/<run>` first, which is a fix. There is a git version floor. | SMP TASK-008 |
| TASK-003 | `Diff` output becomes NUL-delimited. Oversized and unsafe-named files are skipped and recorded. | **Spine TASK-003 merges only after SMP TASK-003 is on SMP `main`.** SMP TASK-008 adopts. |
| TASK-004 | Confined writes; new refusal status codes | SMP TASK-008 |
| TASK-010 | A 4xx from the receiver dead-letters on the first attempt. Orphaned and malformed rows go `dead`, and so do rows whose TLS configuration cannot be built — the first such row takes the subscription's whole due queue with it. Error text is redacted. Rows held by an open circuit, or whose subscription cannot be read (database, decrypt), are held without consuming attempts — the subscription's whole due queue at once — and go `dead` once held 24 h (`MaxCircuitHold`, counted from the circuit opening or the first failed read on that replica, or from `created_at` if later; in memory, so a restart starts it again). A transient subscription-read failure is retried with backoff, then every minute, instead of on every poll; it still counts attempts and is never dead-lettered for this before the 24 h cap. | **SMP TASK-006's R1 (503 for transient receiver failures) and R2 (the rotation-window requeue) must be on SMP `main` before any SMP bump past Spine TASK-010.** |
| TASK-011 | Strict, round-trip-checked workflow projection (also U+0000, a path / `id` / `status` over 1024 bytes, and a non-file entry such as a gitlink). A rejected row is quarantined. The write path SMP wraps (`workflow.Service` Create/Update, `StartWorkflowPlanningRun`) refuses the same bodies. The first sync deletes every stored row outside the workflow definition locations — every nested `workflows/**` row, Active ones included (reported; runs pinned to them lose their row). A `Head()`, `Diff` or git-caused read failure is an `error` tick. | **The first SMP bump whose pin contains Spine TASK-011 also carries SMP's backfill-probe change** (SMP TASK-012 §4.6), exempting rejected and non-file paths. Without it, every leader acquisition triggers a `FullRebuild` that strands in-flight runs. |
| TASK-012 | Git children are stopped with SIGTERM on cancel, and the merge blocks re-check run status. A successful result does not change. | SMP TASK-012 (gates) |
| TASK-013 | `BootstrapInternalAdmin` no longer re-activates a suspended `smp-admin`, revokes superseded bootstrap bearers and uses random token ids. Nothing in SMP observes this. | SMP TASK-012 (re-runs TASK-013 AC 18) |
| TASK-014 | `workflow.Service` gains a branch-protection policy. SMP's admin writes stay bypass writes. | SMP TASK-012 (§6 of that task) |

**Rule 5 — gates at the exact pair.** Every SMP bump runs SMP's full gates with `../spine`
checked out at exactly the pinned commit (`dev/checkout-spine.sh` refuses anything else). A
`.spine-revision` change also triggers SMP CI's images and RDS-emulation jobs, and both must be
green.

---

## 7c. Coverage

Every confirmed finding and every P3 item has exactly one owning task per surface half, or a
reason in §7d.

- "Spine" means this initiative.
- "SMP" means INIT-027 EPIC-004.
- "Both" means the surface is reachable in embedded SMP and in standalone Spine.

### Confirmed findings

| ID | Sev | Surface | Defect (one line) | Owner |
|---|---|---|---|---|
| F1 | P1 | both | The pre-receive gate evaluates a different ref than git applies when a later command line is crafted, so literal-name branch protection is bypassed, unaudited | Spine TASK-001; SMP TASK-001 |
| F2 | P2 | both | The governed merge publishes the run-branch tip at accept time, not the validated and reviewed commit; run branches are pushable by non-owners | Spine TASK-005 (merge), TASK-016 (push); SMP TASK-009 (merge), TASK-005 (runner bearer), TASK-007 (members) |
| F3 | P2 | both | Governance files ride a governed merge into `main`, and the projection installs them as live policy with no provenance check | Spine TASK-006; SMP TASK-010 |
| F4 | P2 | both | Unbounded git reads: a small compressible push exhausts memory in the shared process (cross-tenant availability) | Spine TASK-003; SMP TASK-004 (own reads), TASK-008 (projection) |
| F5 | P2 | both | A committed symlink as the last path component lets an artifact create write outside the worktree | Spine TASK-004; SMP TASK-004 (own `Commit`), TASK-008 (embedded) |
| F6 | P2 | standalone | Contributors can grant themselves, or strip from others, any skill | Spine TASK-013 |
| F7 | P2 | standalone | A reviewer's workflow update without `write_context` commits straight to `main` | Spine TASK-014 |
| F8 | P2 | standalone | Convergence merges divergence content into the checked-out branch (`main`) before review | Spine TASK-012 |
| F9 | P2 | standalone | Concurrent runs share one working tree with no lock; a poisoned tree fails every later merge | Spine TASK-012 |
| F10 | P2 | standalone; embedded latent | Bare branch names let a pushed tag or other ref shadow a run branch or `main` | Spine TASK-002 (client), TASK-016 (gateway); SMP TASK-005 (own plumbing), TASK-007 (gateway) |
| Latent | — | dormant | gitpool sends the process-wide push token to any host named in a code-repo clone URL. It is unreachable at `61994fe`, and becomes P1 once a catalog is wired. | Spine TASK-015 |

### P3 items

| ID | Surface | Item | Owner |
|---|---|---|---|
| P3-1 | embedded | Step-event HMAC key stored in plaintext by default; adding a key later never re-encrypts | Spine TASK-009; SMP TASK-006 |
| P3-2 | embedded | `fastForwardLocalRefs --quiet` defeats the non-fast-forward tolerance | SMP TASK-002 |
| P3-3 | both | `AssignStep` enforces no actor constraints | Spine TASK-007; SMP TASK-011 |
| P3-4 | both | Preconditions enforced only at activation; the helper fails open | Spine TASK-007; SMP TASK-011 |
| P3-5 | both | Non-terminal deliveries re-claimed ahead of newer rows starve the queue | Spine TASK-010 |
| P3-6 | standalone | `write_context` and `/artifacts/add` commit into another actor's run branch | Spine TASK-014 |
| P3-7 | embedded | SMP's reader and `CreateBranch` resolve bare names on the remote | SMP TASK-005 |
| P3-8 | embedded | The `verify-children` validation diff is always empty | SMP TASK-003 |
| P3-9 | both | `Diff` and `ListFiles` without `-z` silently drop C-quoted paths | Spine TASK-003; SMP TASK-003 |
| P3-10 | embedded | `enc:v1` has no AAD and no key id (transplant and rotation) | Spine TASK-009; SMP TASK-006 |
| P3-11 | both | The webhook HMAC covers only the body, so deliveries replay | Spine TASK-010; SMP TASK-006 |
| P3-12 | both | The circuit breaker sticks half-open on an early-return probe | Spine TASK-010 |
| P3-13 | both | Projection skips `Parse`/`Validate`; case-folding parser differential | Spine TASK-011 (narrowed, §7d) |
| P3-14 | both | `IncrementalSync` drops the second `Diff` error and reports `idle` | Spine TASK-011 |
| P3-15 | both | `ClaimStep` ignores `execution.mode` | Spine TASK-007 |
| P3-16 | standalone | Step entry ignores run status; cancel leaves executions live | Spine TASK-007 |
| P3-17 | both | Submit ownership is caller-enforced only | Spine TASK-008; SMP TASK-011 |
| P3-18 | embedded | Any member can cancel any run, which deletes its branch | SMP TASK-007 |
| P3-19 | both | `required_outputs` are self-attested | Spine TASK-008 (evidence half; enforcement half descoped, §7d) |
| P3-20 | embedded | EV-001…005 wired with nil resolvers, so the rules are silent no-ops | SMP TASK-003 (full wiring descoped, §7d) |
| P3-21 | both | Status transitions are not a state machine; the cascade writes whatever the outcome says | Spine TASK-017; SMP TASK-011 |
| P3-22 | standalone | Projection follows the shared tree's checked-out `HEAD` | Spine TASK-011 |
| P3-23 | standalone | `IncrementalSync` is not scoped to `artifacts_dir` | Spine TASK-011 |
| P3-24 | standalone | No compare-and-swap on step and run status writes | Spine TASK-008 |
| P3-25 | standalone | The target validator leaks resolved private IPs and misses IPv6 transition forms | Spine TASK-010 |
| P3-26 | standalone | Full webhook URLs, including secret tokens, land in error columns and logs | Spine TASK-010 |
| P3-27 | standalone | Serial multi-workspace scheduler; `commitMaxRetries` never enforced | Spine TASK-012 |
| P3-28 | standalone | Step ids interpolated raw into SSE fields (frame injection) | Spine TASK-015 |
| P3-29 | standalone | Shared-mode leakage through process-global gateway state | Spine TASK-015 |
| P3-30 | standalone | Git-endpoint auth bypasses (trusted CIDR on raw `RemoteAddr`; dev mode) | Spine TASK-015 |
| P3-31 | standalone | Bootstrap-admin rotation does not revoke; a suspended `smp-admin` is re-activated | Spine TASK-013 |
| P3-32 | standalone | Refs reach git argv without validation or an option terminator | Spine TASK-002 |
| P3-33 | standalone | No role ceiling on `actor.create` | Spine TASK-013 |
| P3-34 | embedded | The write gate is not released on a git error | SMP TASK-002 |
| P3-35 | standalone (dormant) | The bootstrap `token_id` exposes a hash prefix of the platform bearer; no entropy check | Spine TASK-013 |
| P3-36 | standalone (dormant) | Repository `default_branch` is never ref-validated | Spine TASK-002 |
| P3-37 | standalone (dormant) | Code-repo merges are ungated, merge the tip, and drop work on loop-back | Spine TASK-005 |

---

## 7d. Descoped and narrowed items

The owner accepts or rejects these as a set (owner question 11). The SMP-side items are
repeated in SMP EPIC-004.

| Item | Status | Reason |
|---|---|---|
| **F6 sub-fix:** match `required_skills` by immutable `skill_id` instead of name | Descoped (TASK-013 Decision 13) | TASK-013 makes skill create, update and deprecate, and actor-skill grants, admin-only, and refuses self-grant. That removes the rename and self-grant vector this sub-fix targets. Switching `core/actor` selection to ids would change matching semantics SMP relies on (name-based `EnsureSkillByName` and selection), and no exploit would remain to justify it. Revisit only if skill management ever becomes non-admin. |
| **F7 sub-fix:** update-capable workflow planning runs | **Not descoped** (correction) | The plan listed this as descoped. TASK-014's design delivers it: `StartWorkflowUpdatePlanningRun` (§2, Decision 3), because without it a reviewer has no governed way to edit an existing workflow. TASK-014 owns it. |
| **P3-19 enforcement half:** verify that declared `required_outputs` exist or changed | Descoped (TASK-008 Decision D4) | Every `required_outputs` value shipped in both corpora is a label, not a path. A path-existence check would protect nothing that ships. A "non-empty diff since the run base" rule would be satisfied cumulatively by earlier steps, or would break review steps that declare outputs without committing. The substantive control is the SHA binding (TASK-005) plus the review step. TASK-008 records per-step head SHA and changed-path evidence, so an empty step is visible. Typed path outputs could be a later workflow-schema feature. |
| **P3-13 strict half:** `workflow.Validate` as a blocking check at projection | Descoped as a default | 10 of the 15 shipped workflow files fail `Validate` today (overlay test at `61994fe`/`80995559`). Blocking would drop the ADR, document, epic and spike workflows on the next change or `FullRebuild` in every workspace, and the "no active workflow" regression class would return. TASK-011 closes the actual differential (strict `Parse` plus round-trip equality). It ships `WithStrictWorkflowValidation`, off in both repositories. Turning it on needs a corpus-repair task in both repositories first, propagated into existing workspace repositories (owner question 7). |
| **P3-37 sub-fix:** re-merge code-repo commits dropped by a `merge_failed` loop-back | Narrowed | Under binding, TASK-005 §8 re-merges a code repository whose bound commit is not on its default branch, which fixes the drop. With binding off, the existing skip remains, recorded in `KNOWN-LIMITATIONS.md`. Both paths are dormant: no code-repo catalog is wired in `spine serve` or in SMP. |
| **P3-20 full fix** (SMP): wire real EV-001…005 evidence resolvers | Descoped in SMP EPIC-004 | It needs the validation-policy registry and an evidence producer, which neither repository ships. SMP TASK-003 adds an explicit no-op plus a fail-closed guard. The SHA binding provides the "validated commit is merged commit" guarantee. |
| **AAD for SMP's own AEAD stores** (not an audit finding) | SMP decision (owner question 9) | SMP-owned, outside Spine's `core/crypto`. See SMP EPIC-004. |

---

## 7e. Program owner questions

Every task works with its stated defaults. These questions cut across tasks or repositories,
so they are answered once, here. SMP EPIC-004 carries the same list with the same numbers. Tasks
cite them as "plan owner question N" or "INIT-023 owner question N". Each task's own owner
questions stay in that task.

1. **Landing order and pilot gate.**
   - (a) When do these artifacts land in the public Spine repository?
   - (b) Does the hosted pilot wait for the SMP fixes?
   - *Default:*
     - (a) This initiative lands with, or after, Spine TASK-001's PR, and only once SMP TASK-001
       is on SMP `main`.
     - (b) The hosted pilot (INIT-027 EPIC-003) admits no member or runner host the operator
       would not trust as an admin until SMP waves 1–2 are deployed. Those waves close F1, F4
       and F5, and need no Spine change.
2. **Separation of duties in the SMP pilot.** Spine TASK-007 provides it as an option.
   - *SMP default:* off (`SMP_STEP_SEPARATION_OF_DUTIES`), so that one-person workspaces can
     complete their own runs. With it off, a member can still execute and approve their own
     change to ordinary content. Governance paths are covered by the admin approval in owner
     question 6 either way.
   - *Spine's recommendation:* on, with a logged per-workspace opt-out.
   - Set it on in any pilot with more than one human per workspace.
   - Source: Spine TASK-007 OQ7, SMP TASK-011 OQ2.
3. **Run ownership.** Who may push to or delete a run or planning branch, who may push
   non-branch refs, who may cancel a run, and is a run branch frozen while its current step is a
   review step? There is one answer for both gateways and the API path. SMP TASK-007 waits for
   this answer.
   - *Default, push:* an operator or admin, always. The run's job credential (SMP). Otherwise
     only the actor bound to the latest execution of the run's current step, while that
     execution is `assigned` or `in_progress` and the run is `active`.
   - *Default, delete:* operator or admin only.
   - *Default, non-branch refs:* operator or admin only.
   - *Default, cancel (SMP):* an admin, the run's launcher, or an actor who holds or held a step.
     Until SMP TASK-011, only admin-only cancel is robust against self-assignment.
   - *Review freeze:* Spine TASK-016 recommends a freeze for non-operators on both paths, and
     TASK-014 already freezes the API path. SMP TASK-007 defaults to no freeze, with the SHA
     binding as the control. The owner picks one answer. If it is "no freeze", Spine TASK-014
     drops its API freeze so that both paths agree.
   - Source: Spine TASK-016 OQ1, OQ2 and OQ4; TASK-014 OQ5; SMP TASK-007 OQ2 and OQ3.
4. **Merge-binding policy.**
   - *Default, moved head:* an approval of a branch that moved since the last binding is refused
     with 409, and the reviewer sends the run back.
   - *Default, in-flight runs:* a run that reaches merge with no bound SHA fails closed and
     routes back.
   - *Default, standalone:* no environment switch to disable binding.
   - *Default, SMP:* command-mode and runner-held gate steps assert the commit they ran on.
   - Source: Spine TASK-005 OQ2, OQ4 and OQ6; SMP TASK-009 OQ1, OQ2 and OQ4.
5. **Read and push size limits.**
   - *Default:* 4 MiB per blob read and 16 MiB per command output, in Spine and SMP; 8 MiB per
     pushed object at SMP's gateway.
   - All are configurable. Spine's worktree read cap equals the blob cap.
   - *Alternatives:* 1 MiB (tighter, but larger documents drop out of the projection with a
     record) or 16 MiB.
   - Source: Spine TASK-003 OQ1; SMP TASK-004 OQ1; SMP TASK-008 OQ3 and OQ6.
6. **Governance-path guard.**
   - *Default, model:* (a) an operator in Spine, or a workspace admin in SMP, approves the exact
     bound SHA, and the merge is held until approve or reject.
   - *Default, paths:* `.spine/`, `.spine.yaml`, `workflows/` and `<artifacts_dir>/workflows/`.
     AI-agent configuration is not guarded; SMP already flags it.
   - *Default, ADR-008 lifecycle exemption:* on in `spine serve`, off in SMP.
   - *Default, ADR form:* a new ADR-016 rather than an in-place ADR-009 amendment.
   - Source: Spine TASK-006 OQ1–3; SMP TASK-010 OQ1, OQ2 and OQ4.
7. **Projection strictness for workflows.**
   - *Default:* strict `Parse` plus round-trip equality now. A rejected file is never projected,
     and its last verified row is quarantined.
   - `workflow.Validate` stays advisory in both repositories
     (`WithStrictWorkflowValidation` off).
   - Making `Validate` blocking first needs a corpus-repair task in both repositories,
     propagated into existing workspace repositories. 10 of Spine's and SMP's 15 shipped files
     fail it, and 4 of SMP's 7 seeded workflows do.
   - Source: Spine TASK-011 OQ1 and OQ2; SMP TASK-012 OQ1.
8. **Workspaces behind `verify-children` version 7** (SMP).
   - *Default:* each affected workspace's admins get one in-platform notice, through a notice
     floor (ADR-033 amendment).
   - *Alternatives:* a release note only; or refuse the known-defective step versions, which
     stops planning runs in those workspaces until an admin acts.
   - Under any answer, P3-8 stays open in an existing workspace until its admin applies
     version 7.
   - Source: SMP TASK-003 OQ1.
9. **AAD for SMP's own AEAD stores** (`internal/secrets/store.go`,
   `internal/customer/internalcreds/store.go`). This is not an audit finding.
   - *Default:* no extra task; record a follow-up tied to the ARC-SEC-R14 column drop. Both
     stores are decrypt-only legacy paths (`aead.Open` only, no `Seal`, verified at
     `80995559`), every new write goes to the secret store, and AAD cannot be added to
     ciphertext that is only ever read.
   - *Alternative:* add an EPIC-004 task now.
10. **Standalone defaults.** The embedded library's zero value stays today's behaviour. What
    does `spine serve` turn on?
    - *Default:*
      - merge binding on, with no off switch;
      - both governance guards on, with `SPINE_GOVERNANCE_GUARD=off` as the v1 escape;
      - the strict step-entry policy, with `SPINE_STEP_ENTRY_POLICY=legacy` as a logged
        escape;
      - the status lifecycle on, with no kill switch;
      - `SPINE_SECRET_ENVELOPE_MODE=migrate`, with `strict` as the documented closing step.
        P3-10 is closed only in `strict` mode, and the CHANGELOG says so.
      - v1 webhook signatures remain the default.
    - Source: Spine TASK-005 OQ6, TASK-006 OQ4, TASK-007 OQ2, TASK-009 OQ1, TASK-010 OQ3,
      TASK-017 OQ5.
11. **Accept the descoped and narrowed items in §7d.**
    - *Default:* accept, including the correction that F7's update-capable planning runs are in
      scope (TASK-014).
    - *Answered 2026-11-05: accept (the default), by the owner's delegation to the recorded
      defaults.*
12. **Partial close.** If a standalone-only Spine task (TASK-012 … TASK-016) stalls, does SMP
    EPIC-004 wait for the INIT-023 closing commit?
    - *Default:* wait.
    - *Alternative:* SMP bumps to the latest commit that contains every task SMP consumes
      (TASK-001 … TASK-011 and TASK-017), and records the stalled tasks as not yet picked up.
    - Source: SMP TASK-012 OQ2.

---

## 7f. Progress

- 2026-10-07 — **Wave 1:** TASK-001 (the P1: every pre-receive command line
  is validated; `refname.ValidateQualified`; the branch-protection
  control-byte deny) merged to local `main` after its own review rounds, a
  cross-task review with SMP's fix of the same bug (INIT-027 EPIC-004
  TASK-001), Spine CI and the SMP compatibility build. Not pushed.
- 2026-10-08 — **Wave 2:** TASK-002, TASK-004, TASK-007, TASK-009, TASK-010,
  TASK-011, TASK-013 and TASK-016 merged to local `main` through
  `init/INIT-023-wave2-integration`. Each task was reviewed alone (three
  lenses, two rounds, codex); the integration fixed four semantic conflicts
  with tests (`TreeEntryType` follows the ref grammar; the step-entry policy
  reaches every pooled workspace; a refused signing secret holds its
  deliveries; docs). A cross-task review (git/ref surface,
  governance/engine, secrets/delivery, SMP compatibility, merge audit; two
  skeptics per P1/P2) confirmed two P2s, both fixed with tests: separation
  of duties now counts every step type push admission lets write, and
  `spine secrets reencrypt` never guesses the workspace id. Gates: Spine CI,
  vet with every tag, race on the touched packages, integration and the
  `TestGitHTTP_*` scenarios on PostgreSQL 17, golangci (no new issues),
  codex clean; SMP `main` 6ca0eeb7 builds and passes its Spine-runtime and
  gateway suites against the merged tree with no SMP change. Known
  pre-existing failures, identical on `main`: 30 scenario tests,
  `TestRunModeDatabaseDefault`. Tasks stay In Progress until GitHub CI runs
  after a push. Not pushed.
- 2026-10-14 — **Codex 6.1 catch-up of waves 1 and 2.** The earlier codex
  rounds had run on older models (the CLI refused the configured gpt-6.1-sol).
  After a CLI update, every merged wave 1 and wave 2 range was re-reviewed on
  gpt-6.1-sol with two skeptics per P1/P2. Three real P3s were fixed with tests
  on `init/INIT-023-codex61-catchup`: a FIFO leaf is opened non-blocking in
  `worktreefs`, workflow integers above 2^53 are decoded exactly, and the
  projection sweep resets its baseline through `SetSyncBaseline`.
- 2026-10-14 — **Wave 3:** TASK-003 (bounded blob reads, NUL-delimited
  `Diff`/`ListFiles`), TASK-005 (the governed merge is bound to the reviewed
  commit), TASK-014 (reviewer workflow updates go through governance;
  `write_context` limited to the assignee) and TASK-015 (SSE field safety,
  shared-mode tenant scoping, git-endpoint auth, gitpool token-host scoping)
  merged to local `main` through `init/INIT-023-wave3-integration`, together
  with the catch-up. Each task was reviewed alone (three lenses, two rounds,
  codex); the integration fixed two semantic conflicts (the commit binder's
  git runner now has TASK-003's output bounds and the C locale; docs) and
  codex's P2s (the binding is written before the step completes; refusals are
  reported before root workflows are listed). The cross-task review (two
  skeptics per P1/P2) confirmed two more P2s, both fixed with tests: the
  binding-on status cascade could take another run's merge as its own replay,
  and concurrent recovery requests could lose a binding update (now under a
  per-run lock). Gates: Spine CI, vet with every tag, race on the touched
  packages, integration and the `scenario-git-http` job on PostgreSQL 18, the
  `git-floor` job on git 2.39.5, golangci (no new issues), codex on
  gpt-6.1-sol clean in two rounds on the final tree. SMP `main` f5c6fd6d
  builds against it and passes its Spine-runtime and gateway suites; SMP's
  wave 3 integration (`.spine-revision` d23b897) also builds and passes
  against it. Known pre-existing failures, identical on `main`: 31 scenario
  tests (30 + `TestWorkspace_ScopedServices` with a migrated registry),
  `TestRunModeDatabaseDefault`. Open for the owner: TASK-015 OQ2 ruled
  "never" (a deviation from its default); whether the commit binder should
  get `GIT_ALLOC_LIMIT` (KNOWN-LIMITATIONS "Git Read Bounds"). Tasks stay In
  Progress until GitHub CI runs after a push. Not pushed.
- 2026-10-20 — **Wave 4:** TASK-012 (one process-wide lock per working tree,
  home-branch restore with quarantine, SIGTERM on cancel for every git child,
  convergence targets the run branch, bounded governed-merge retries), TASK-008
  (submitter identity, run-status and step compare-and-swap, cancel losing to a
  merge start answers 409), TASK-017 (the artifact status lifecycle at the
  governed merge, the V1 merge-result check) and TASK-006 (the governance-path
  guard: governed merges that change `.spine/`, `.spine.yaml` or `workflows/`
  wait for an operator's approval of the exact bound commit; the projection
  keeps protection rules a merge removed without authority) merged to local
  `main` through `init/INIT-023-wave4-integration`. Each task was reviewed alone
  (three lenses, two rounds, codex on gpt-6.1-sol). The integration put every
  take-back through TASK-012's armed `ResetBranch` under the tree lock and fixed
  the check order (binding, lifecycle, governance hold, merge, V1 and the subset
  check, push); codex found one P2 (`MergeBases` on a home-branch tree), fixed.
  The cross-task review (two skeptics per P1/P2) confirmed two P2s, both fixed
  with tests: V1 and the governance check could be skipped for good after a late
  `MergeBound` error or a crash inside the merge block (now a
  `spine.merge.unverified` record taken back at the next attempt), and a failed
  read could put a governance hold on the wrong step. A follow-up fixed two
  TASK-003 residuals SMP found: `ReadFile` reads blobs only (a gitlink took the
  git child to 272 MiB; a tag, tree or commit recorded at a file entry is
  refused; range syntax in a path never reaches the revision parser), and pinned
  two guards no test covered. An independent check mutated 45 fixes out of the
  merged trees. Gates: Spine CI, vet with every tag, race on the changed
  packages, integration and the `scenario-git-http` job on PostgreSQL 18, the
  `git-floor` job on git 2.39.5, golangci (no new issues), codex on gpt-6.1-sol
  clean on the final code. SMP `main` builds against it and passes its
  Spine-runtime and gateway suites; SMP's wave 4 (`.spine-revision` ae513dca)
  also passes against it. Expected at SMP's next bump: two SMP gitread contract
  tests that pin the old gitlink reading fail, and SMP's own `gitread` read
  still peels a tag at a file entry (SMP TASK-010 closes it). Known pre-existing
  failures, identical on `main`: 31 scenario tests with a migrated registry,
  `TestRunModeDatabaseDefault`. Open for the owner: ADR-016 (TASK-006 OQ1) is
  Proposed until signed off; TASK-006 AC 11/18, TASK-008 AC 10/11/21 and TASK-012
  AC 15 wording amendments; whether an approved governance merge restarts its
  retry count. Tasks stay In Progress until GitHub CI runs. Not pushed.
- 2026-11-04 — **Pushed; statuses after GitHub CI.** The owner approved the push. Spine `main` on
  GitHub now carries INIT-023 (waves 1–4, `a560c210`) and INIT-024 (its closing commit `fd35f802`,
  then `ce0b9c9e`, a git-identity fix the first run found). GitHub CI run 41875102030 at `ce0b9c9e`
  passed all 8 jobs (41856447149 at `fd35f802` failed only `go test ./...`, on that defect).
  Completed: TASK-002, TASK-005, TASK-007, TASK-010, TASK-014 and TASK-017. The other eleven stay In
  Progress; each task's "Status after GitHub CI" note lists what is left:
  - owner: the ADR-016 sign-off (TASK-006 AC 1, OQ1); TASK-015 OQ2; AC amendments or rulings in
    TASK-003 (AC 5, 11.4), TASK-006 (AC 11, 18), TASK-008 (AC 10, 11, 14, 21), TASK-012 (AC 15) and
    TASK-016 (AC 16); waivers in TASK-009 (AC19), TASK-011 (AC 18, 24, 26) and TASK-013 (AC 16);
  - owner or orchestrator: TASK-001 AC 9's deviations, TASK-012 AC 23's waiver, and the records for
    TASK-001 AC 13 and TASK-003 AC 16 and AC 17;
  - checks: TASK-004 AC 14 (the audit PoCs at the merge commit) and TASK-001 AC 10 (SMP's CI green;
    its first run since the push is red for SMP-side causes).

  All four epics and the initiative are In Progress.
- 2026-11-05 — **TASK-004 Completed; TASK-001 AC 10 met.** TASK-004's AC 14 check ran: the three
  original audit PoCs (`poc/git`, `poc/parsing`, `poc/verify-r1-8`), run with `go test -overlay`
  against the merge commit `d23b897` and against `main` `f6132470`, do not reproduce at either
  commit. TASK-001 AC 10: SMP CI run 42001537129 at SMP `main` `6bd97ac9` (`.spine-revision`
  `ce0b9c9e`) passed every job; TASK-001 stays In Progress for AC 9 and AC 13. Seven of the 17
  tasks are Completed; the epics and the initiative stay In Progress.
- 2026-11-05 — **Owner rulings; TASK-009, TASK-013 and TASK-015 Completed.** The owner waived
  TASK-009 AC19 (`make test-integration`'s two failures, the same on `main`; the
  `TestRunModeDatabaseDefault` fixture defect is filed as a follow-up there), accepted TASK-013's
  Deviation 10 (AC 16), and confirmed TASK-015 OQ2 "never". Ten of the 17 tasks are Completed;
  the epics and the initiative stay In Progress.
- 2026-11-05 — **Owner rulings; TASK-001, TASK-003, TASK-008, TASK-011, TASK-012 and TASK-016
  Completed.** By delegation to the recorded recommendations, the owner ratified TASK-001 AC 9's
  two deviations and accepted its direct merge `ee3c249e` as AC 13's evidence; adopted the proposed
  amendments to TASK-003 AC 5 and AC 11.4 and recorded its AC 16 and AC 17 as met; adopted the
  wording amendments to TASK-008 AC 10, 11, 14 and 21; waived TASK-011's `make test-scenario`
  clauses (AC 18, AC 26) and accepted SMP's GitHub CI for its AC 24; waived TASK-012 AC 23 and
  accepted Deviation 16 for AC 15's push half; and ratified TASK-016's DC-8 amendment (AC 16).
  Sixteen of the 17 tasks are Completed. EPIC-001, EPIC-003 and EPIC-004 are Completed. EPIC-002
  and the initiative stay In Progress: TASK-006 waits for the owner's ADR-016 sign-off (AC 1) and
  its AC 11 and AC 18 amendments.
- 2026-11-05 — **ADR-016 Accepted; TASK-006 and EPIC-002 Completed; all 17 tasks Completed.** The
  owner approved ADR-016 as built, with owner question 1's decision (a), and adopted TASK-006's AC 11
  and AC 18 amendments (Deviations 22 and 12). All four epics are Completed. Program owner question
  11 takes its default, accept. The exit criteria (§8), checked one by one:
  - all four epics Completed, every task Completed: **met**;
  - every task's SMP compatibility gate passed at merge, and TASK-003 merged after SMP TASK-003
    was on SMP `main`: **met** (each task's gate record; TASK-003 AC 17);
  - every audit PoC is a committed regression test, or its task records why not: **met** (each
    task's PoC AC). None reproduces at the closing commit: **not yet checkable**, because the
    closing commit does not exist yet. The evidence so far is at code identical to it or earlier:
    Spine CI 41875102030 at `ce0b9c9e` ran the committed regression tests green; TASK-004's three
    PoCs do not reproduce at `f6132470`; SMP INIT-027 EPIC-004 TASK-012's PoC sweep at `49fad9c0`
    found every PoC assertion refused except the cases its record lists as zero-value only, by
    design, or pending an owner decision. The PoCs are re-run at the closing commit when it exists;
  - the program owner questions answered or their defaults recorded: **met** (question 11 above;
    the others' defaults are recorded in each affected task);
  - `KNOWN-LIMITATIONS.md`, `security-model.md`, ADR-008 and ADR-009 match the shipped behaviour,
    and ADR-016 is Accepted: **met** (each task's documentation AC; ADR-016 Accepted today);
  - the closing commit is named in the last task's Implementation Notes: **open**, it follows the
    initiative's close;
  - "A follow-up security review of the same surfaces, plus the areas the audit did not cover
    (§2), is scheduled.": **open, for the owner.** The 2026-10-22 review behind INIT-024 covered
    the same surfaces before this initiative closed, and INIT-024 lists git pack and fsck
    internals as out of scope, so it does not cover §2's areas. No review is scheduled yet.

  The initiative stays In Progress until the owner schedules that review.

---

## 8. Exit Criteria

INIT-023 may be marked complete when:

- All four epics are Completed, so every task is Completed/Approved, or Cancelled by the owner
  with a recorded reason.
- Every task's SMP compatibility gate passed at merge. Spine TASK-003 merged only after SMP
  TASK-003 was on SMP `main`.
- Every audit PoC is a committed regression test, or its task records why not. None of them
  reproduces at the closing commit.
- The program owner questions (§7e) are answered, or their defaults are recorded in the
  Implementation Notes of each affected task.
- `KNOWN-LIMITATIONS.md`, `security-model.md`, ADR-008 and ADR-009 match the shipped behaviour,
  and ADR-016 is Accepted.
- The closing commit is the merge commit that sets this initiative to Completed. It is named in
  the last task's Implementation Notes, because SMP TASK-012 pins `.spine-revision` to it.
- A follow-up security review of the same surfaces, plus the areas the audit did not cover
  (§2), is scheduled.

---

## 9. Links

- Charter: `/governance/charter.md`
- Constitution: `/governance/constitution.md`
- Branch protection: `/architecture/adr/ADR-009-branch-protection.md`,
  `/initiatives/INIT-018-branch-protection/initiative.md`
- Workflow lifecycle: `/architecture/adr/ADR-008-workflow-lifecycle-governance.md`
- Security model: `/architecture/security-model.md`
- Previous hardening: `/initiatives/INIT-022-code-review-hardening/initiative.md`,
  `/initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/epic.md`
- SMP remediation:
  `smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/epic.md`
