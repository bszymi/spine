---
id: TASK-005
type: Task
title: "Standalone push-gate races: case-folding storage, the review-freeze window, git slot starvation"
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
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-001-pre-receive-refname-validation.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-016-run-branch-push-admission.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
---

# TASK-005 — Standalone push-gate races: case-folding storage, the review-freeze window, git slot starvation

---

## Purpose

This task closes three standalone P3 findings from the 2026-10-22 review (N-8, N-9, N-13; detail
in SMP's git-ignored `tmp/security-review-2026-10-22/spine/REPORT.md`). All three sit in the
standalone git HTTP endpoint and its pre-receive gate. SMP embeds none of this code, so the task
is standalone-only; it carries a parity note for SMP's own gateway.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| N-8 | P3 | Standalone, **case- or normalization-insensitive storage only** (APFS, HFS+, a Docker Desktop bind mount, SMB). Not exploitable on a case-sensitive Linux filesystem. | `SPINE_GIT_RECEIVE_PACK_ENABLED=true` and a repository served from such storage. A push to `refs/heads/MAIN` matches no rule, and the filesystem then aliases it onto `main`. The requirement is documented (`KNOWN-LIMITATIONS.md:930`) but not enforced. |
| N-9 | P3 | Standalone | A bound run-branch author. Admission is decided when the command section is parsed; a pack held back until the step has become a review step still lands. Merge binding is the real control, so this is a hardening of the gate, not a bypass of the SHA guarantee. |
| N-13 | P3 | Standalone shared | Any authenticated workspace member. One process-wide 5-slot semaphore is taken before the push body read, which only `ReadTimeout` bounds, so one member can starve git for every tenant. |

## Current code (verified at `a560c210`)

- **N-8.** `branchprotect` and `refadmit` match ref names after folding, so the policy sees the
  real target. But the gate never checks the storage: `ensureReceivePackConfig`
  (`service/internal/githttp/handler.go:779`) sets only `http.receivepack` and
  `receive.advertisePushOptions`. `KNOWN-LIMITATIONS.md:958-960` records the fix (a
  case-sensitivity probe that refuses receive-pack) as an INIT-023 follow-up.
- **N-9.** `ServeHTTP` runs `prereceive` once (`handler.go:304`), which parses the command section
  and runs `refadmit.NewEvaluator(...).Decide` per ref (`:470`). The run's step state is read at
  that moment; there is no re-check after the pack is received and before refs move. TASK-014
  already freezes the API path for review steps; the git path does not.
- **N-13.** `ServeHTTP` takes the process-wide semaphore (`h.sem <- struct{}{}`, `handler.go:278`;
  `make(chan struct{}, maxConcurrent)` with `maxConcurrent = 5` default, `:153, :173`) before
  `prereceive` reads the body. The gateway's `ReadTimeout` is 30 s (`server.go:370, :442`). The
  key is global: one actor, or one workspace, can hold all five slots across tenants.

## Design

1. **N-8: refuse receive-pack on case-folding storage.**
   - `ensureReceivePackConfig` probes the served repository once per repo path, the first time
     receive-pack is enabled for it: create two ref files that differ only in case under a private
     `refs/spine-probe/` prefix, read both back, and delete them. If the storage folds them onto
     one, receive-pack is refused for that repository with a message naming the storage
     requirement (`KNOWN-LIMITATIONS.md:930`).
   - The probe result is cached beside `ensuredRepos` (`handler.go:807`).
   - Reads (clone/fetch) are unaffected.
   - This is the fail-closed handling the limitation already proposed; the paired SMP follow-up is
     the SMP gateway's own probe.
2. **N-9: re-decide admission after the pack is received.**
   - Keep the pre-pack decision (it rejects early and cheaply). Add a post-pack re-check: a real
     `pre-receive` hook (the same hook TASK-003 installs) runs after objects are written and before
     refs move, and re-evaluates ref-class admission for each `(old, new, ref)` against the run's
     **current** step state.
   - Because the hook runs at the moment git is about to apply the refs, a step that became a
     review step while the pack was in flight is seen, and the push is refused.
   - The hook calls the same admission evaluator the handler uses, over the per-workspace run
     reader and step-definition lookup, so there is one admission table (initiative §7a lists the
     shared `handler.go`).
3. **N-13: fair, bounded slots.**
   - Replace the single global semaphore with a two-level limit: a per-(workspace, actor) cap and
     a per-workspace cap, with a global ceiling kept as a backstop. This mirrors the SSE limiter's
     keying (`sseLimiterKey`, `handlers_events_stream.go`), so one actor or workspace cannot
     exhaust the others.
   - Deadline the command-section read: `readPushCommands` runs under a short context derived from
     a new `SPINE_GIT_COMMAND_READ_TIMEOUT` (default 10 s), separate from the 30 s whole-request
     `ReadTimeout`, so a slow client cannot hold a slot for the full window.
   - The slot is taken only once the command section has been read and admission has provisionally
     passed, so a stalled client holds no slot.

## SMP impact

- **None at the bump.** SMP embeds no part of `service/internal/githttp` or
  `service/internal/gateway`. The task changes standalone code only.
- **SMP parity follow-up** (recorded, not owned here): SMP's own `/git/{workspace}` gateway
  (`internal/customer/githttp`) should get the same case-folding probe, the same post-pack
  re-check and the same fair slots. SMP's `boundRunBranchCheckout` and gateway already exist; this
  is a hardening item for the SMP companion epic.

## Decisions

1. **N-9 is a defence-in-depth hardening, not a correctness fix.** Merge binding (INIT-023
   TASK-005) already guarantees that only the reviewed commit merges. The re-check closes the
   window in which a push lands at all, which keeps the branch state honest for operators and for
   the freeze rule (INIT-023 owner question 3).
2. **The N-9 re-check reuses TASK-003's hook.** One hook, two jobs (tree budget and admission
   re-check), so there is one install path and one subprocess.
3. **N-8 refuses rather than fold-matches at the gate.** Enforcing receive-pack only on
   case-sensitive storage is simpler and safer than teaching every ref comparison to fold the
   `refs/heads/` prefix, and it matches the documented deployment requirement.

## Owner questions

1. **N-13 default slots.** *Default:* 2 per (workspace, actor), 4 per workspace, 8 global.
   *Alternative:* keep 5 global and add only the per-actor cap.
2. **N-8 on a repository that fails the probe.** *Default:* refuse receive-pack for that
   repository, allow reads. *Alternative:* refuse to start `spine serve` at all. The default keeps
   a mixed-storage deployment serving reads.
3. **Review freeze (shared with INIT-023 owner question 3).** This task's N-9 re-check enforces the
   freeze on the git path only if the owner's answer is "freeze". If the answer is "no freeze, SHA
   binding is the control", the re-check still refuses a push whose bound execution is no longer
   `assigned`/`in_progress`, which is the admission rule, not a freeze.

## Acceptance Criteria

1. **Regression: N-8** (`service/internal/githttp`, `TestReceivePack_RefusedOnCaseFoldingStorage`).
   - A repository on a simulated case-folding store (a test `ensureReceivePackConfig` probe hook
     that reports folding) refuses receive-pack with the storage message; reads still work.
   - On a case-sensitive store the probe passes and push works.
   - The audit's `TestRV_F1_CaseFoldRef` shape (`scratch/git/f1`) is refused at the gate rather
     than reaching git. It is not run on a Linux CI filesystem directly; the probe is faked.
2. **Regression: N-9** (`TestPrereceive_FreezeRaceRefusedAtApply`). This ports
   `scratch/git/f1` `TestRV_PushAdmission_FreezeRace`: a pack whose run step became a review step
   between parse and apply is refused by the hook. At `a560c210` it lands.
3. **N-9 keeps the fast path** (`TestPrereceive_EarlyRejectStillFires`). A ref the pre-pack
   decision rejects is still rejected before the pack is read.
4. **Regression: N-13 fairness** (`TestGitHTTP_SlotsFairPerActorAndWorkspace`). With the default
   caps, actor A in workspace 1 cannot hold a slot that blocks actor B in workspace 2; one actor
   holds at most its per-actor cap. At `a560c210` one actor holds all slots.
5. **N-13 command-read deadline** (`TestGitHTTP_SlowCommandSectionDoesNotHoldSlot`). A client that
   stalls mid-command-section is cut at `SPINE_GIT_COMMAND_READ_TIMEOUT` and holds no slot.
6. **Existing suites.** `go test ./service/internal/githttp/... ./service/internal/gateway/...`
   and `scenario-git-http` pass.
7. **SMP compatibility gate** (initiative §5) passes with no SMP change. (Expected: inert; the
   task touches no embedded package.)
8. **Documentation.** `KNOWN-LIMITATIONS.md:930-960` becomes "enforced" for N-8;
   `docs/integration-guide.md` ("Authentication", the trusted-CIDR and push sections) gets the
   slot env vars; `CHANGELOG.md`.

## Compatibility

- Standalone only. New env vars: `SPINE_GIT_COMMAND_READ_TIMEOUT`, the slot caps.
- A deployment on case-folding storage that currently serves pushes (against the documented
  requirement) will have receive-pack refused per repository. The CHANGELOG and the limitation
  entry say so.

## Out of scope

- SMP's gateway parity (SMP companion epic).
- Teaching branch protection to fold the `refs/heads/` prefix (Decision 3 chose the probe).
- Multi-replica slot coordination (the caps are in-process, as the SSE limiter is).

## File overlaps

- `service/internal/githttp/handler.go`: this task adds the storage probe to
  `ensureReceivePackConfig` (`:779`), changes the semaphore (`:278`) and deadlines
  `readPushCommands`. TASK-003 adds the hook install to the same `ensureReceivePackConfig` and
  registers the hook. **This task rebases on TASK-003** and extends the one hook with the
  admission re-check. The semaphore and probe changes do not overlap TASK-003's lines.
- No other file overlap.
