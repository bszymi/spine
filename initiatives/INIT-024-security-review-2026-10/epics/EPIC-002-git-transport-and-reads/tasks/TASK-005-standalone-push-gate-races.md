---
id: TASK-005
type: Task
title: "Standalone push-gate races: case-folding storage, the review-freeze window, git slot starvation"
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

---

## Implementation Notes

*(Implementation, 2026-10-27. Branch `init/INIT-024-TASK-005-standalone-push-gate-races`, cut from
`init/INIT-024-wave3-base` `55789b28` (integration A with TASK-003 merged: TASK-001/002/003/006/007/
008/009 in). Run on macOS (APFS, case- and normalization-insensitive), git 2.54.0, go 1.26.10, and on
git 2.39.5 in the local `golang:1.26-bookworm` image (Linux, case-sensitive) for the `git-floor` job.
Scratch logs: SMP repo `tmp/init027-orchestration/i24/t005/`. Line references below are to the
branch.)*

### Rulings and owner-question defaults (recorded, pending the owner)

- **OQ1 — default taken, refined for single mode (review round 1, P3-2 ruling):** in shared mode 2
  per (workspace, principal), 4 per workspace, 8 process-wide (`githttp.DefaultMaxConcurrent*`); on
  a single-workspace server (no per-workspace pool) the per-workspace and per-actor caps default to
  the process-wide cap (8), so its capacity never drops below the base's 5 and follows
  `SPINE_GIT_HTTP_MAX_CONCURRENT` (`gitHTTPLimits.effective(shared)`; a ceiling set above an
  explicit per-workspace cap in single mode logs a startup Warn naming the binding cap). `spine serve`: `SPINE_GIT_HTTP_MAX_CONCURRENT`,
  `SPINE_GIT_HTTP_MAX_CONCURRENT_PER_WORKSPACE`, `SPINE_GIT_HTTP_MAX_CONCURRENT_PER_ACTOR` (a whole
  number 1–1024) and `SPINE_GIT_COMMAND_READ_TIMEOUT` (default `10s`, a positive Go duration); unset
  or blank is the default, anything else refuses to start (INIT-024 OQ3 standalone rule). A cap set
  above the cap that contains it logs a startup Warn (it has no effect).
- **OQ2 — default taken:** a repository whose storage folds names has receive-pack refused (403 on the
  advertisement and the push; `http.receivepack=false` in that repository); reads are served.
- **OQ3 — recorded:** the apply-time re-check runs the **admission rule** (run active; the latest
  execution of the target (branch, step) held by the actor, `assigned`/`in_progress`, same repository)
  and the **review freeze exactly as INIT-023 recorded it** (TASK-016 OQ4 ruling, TASK-014's I6 as
  amended: `review` and `internal` steps, non-operators), because the re-check calls the same
  `refadmit.Evaluator.Decide` the pre-pack gate calls — no new freeze semantics. If the owner answers
  INIT-023 OQ3 with "no freeze", dropping the freeze from `Decide` drops it from both decisions at
  once; the re-check still enforces the binding (`TestPrereceive_FreezeRaceRefusedAtApply` rows
  "execution was submitted", "step was reassigned", "run was paused").

### What was built

| Commit | Change |
|---|---|
| `5273c581` | Status In Progress. |
| `ff20b06f` | **N-8** `service/internal/githttp/storage_probe.go` (new): `probeRefStorage` — in `os.MkdirTemp(<git-common-dir>/refs, ".spine-storage-probe-")` it creates (O_EXCL) three name pairs a folding filesystem identifies (`spine-probe-case`/`SPINE-PROBE-CASE`, NFC/NFD e-acute, `k`/KELVIN SIGN), reads each back and lists the directory; any collision is folding; the directory is removed. `gitCommonDir` (extracted from `installBudgetHook`, same behaviour). `handler.go` `ensureReceivePackConfig`: with receive-pack on, the first alignment of a repository probes it and records the verdict (`storageFolds`, beside `ensuredRepos`); folding → both keys `false`; a probe error is not cached, writes `false` and refuses pushes; the first alignment of each repository is serialised (`ensureMu`) because it now runs before a slot. `ServeHTTP`: a push request (advertisement or POST) on folding storage → 403 `caseFoldingStorageMessage`; unprobed → 500 "push gate unavailable"; before the body is read. **N-9** `admission_recheck.go` (new): `startRecheck` (per-push Unix socket in a fresh 0700 dir under `os.TempDir()` — `/tmp` when that path is too long for `sun_path` — 32-byte hex token, single use, constant-time compare, line protocol `spine-push-recheck 1`), `recheckAdmission` (every hook line must be an announced update — SHAs compared case-insensitively — then a fresh `refadmit.NewEvaluator(p.runs).WithStepDefs(p.defs).Decide` per line with the pre-pack actor and repository), the hook client (`recheckFromEnv` reads and unsets `SPINE_INTERNAL_PUSH_RECHECK_SOCKET`/`_TOKEN`; `check` fails closed). `budget_hook.go` `PreReceiveHookMain`: reads the channel first, collects every line (deletes too), runs the unchanged budget measure (moved verbatim into `measureTips`), then the re-check. **N-13** `push_slots.go` (new): `gitSlots` (per principal, per workspace, global; non-blocking; idempotent release), `slotPrincipalOf` (workspace from `observe.WorkspaceID`, actor ID or — no actor / empty ID — the peer host of `r.RemoteAddr`). `handler.go`: `Config.MaxConcurrentPerWorkspace`, `MaxConcurrentPerActor`, `CommandReadTimeout`, `MaxConcurrent` default 5 → 8; `ServeHTTP` order now: query check → push-disabled → unsupported → ensure + probe → storage refusal → `prereceive` (no slot) → slot → hook install → re-check armed → override events → CGI with `hooksPathEnv` + the re-check env. `prereceive` reads through `readCommandSections` (goroutine + timer; on expiry `http.ResponseController.SetReadDeadline(now)` cuts the blocked read, falling back to closing the body; the goroutine is always waited for; the connection deadline is never moved forward on success) and returns `*admittedPush` (body, updates, actor, repository, the runs/defs pair, `emitOverrides`). `service/cmd/spine/serve_git_http_limits.go` (new): `loadGitHTTPLimits`, `logGitHTTPLimits`; `serve_cmd.go` parses them before any store; `serveDeps.GitHTTPLimits`; `buildGitHTTPHandler(..., limits, log)`. Tests (below); `TestMain` in githttp, gateway and the scenario package (`FakeCaseSensitiveStorageOnFoldingHostForTests`, Deviation 3). |
| `c6ad616d` | Docs: `KNOWN-LIMITATIONS.md` (storage requirement "enforced"; run-branch admission re-checked at apply time; git capacity shared fairly, with residuals; the SoD note), `docs/integration-guide.md` §7 (re-check, storage probe, slot keying under Authentication, Limits, four env rows), `CHANGELOG.md`, `docs/git-push-guide.md`, `architecture/security-model.md`, `architecture/docker-runtime.md` §9.2, `docker-compose.override.yaml.example`. |
| `c9a6969b` | The probe names as Go escapes (as literal characters a normalising editor would merge the NFC/NFD pair); AC-test seams for the red runs. |
| `a1b6965b` | Codex round 1 (P2): the re-check decision is bounded by its connection's deadline and cancelled by `close`. |
| `f4baae12` | Implementation Notes. |
| `604a47c7` | Review round 1 fixes (below). |
| `e91b1a8f` | Review round 1 docs. |
| (this commit) | Implementation Notes: review round 1. |

### N-13 keying (carried handoff 4)

Every request the handler serves is keyed by (workspace, principal). The workspace is the one
`handleGit` resolved and attached (`observe.WithWorkspaceID(ctx, cfg.ID)`, `handlers_git.go:268`)
before every delegation; the actor is the one `validateGitAuth` returned (`domain.WithActor`,
`:275`). TASK-006/TASK-007 make every request that reaches the handler either authenticated for that
workspace or a token-less read the workspace itself trusts.

| Request shape | Workspace key | Principal key |
|---|---|---|
| Token push (`POST git-receive-pack`, the advertisement) | resolved `cfg.ID` | actor ID |
| Token clone / fetch | resolved `cfg.ID` | actor ID |
| Server-level operator token | resolved `cfg.ID` | the operator actor's ID |
| Trusted-CIDR token-less read (TASK-006: direct peer, bound to `cfg.ID`, no forwarding header) | resolved `cfg.ID` | the TCP peer's address (`r.RemoteAddr` host; the gateway never rewrites it) |
| `SPINE_GIT_HTTP_DEV_NO_AUTH` read (development only) | resolved `cfg.ID` | the peer address |
| An attached actor with an empty ID | resolved `cfg.ID` | the peer address (never one shared empty key) |
| Single mode | the one workspace's `cfg.ID` (its per-workspace cap is the server's capacity) | as above |
| Handler used without the gateway (tests) | `""` | actor ID or peer |

`slotPrincipal.peer` separates actor IDs from addresses, so no actor ID collides with a peer.

### Acceptance criteria — evidence

"Red" = the AC tests at the head run against the base's package (`go test -overlay`: every githttp
file mapped to its `init/INIT-024-wave3-base` content, new files removed, and
`push_gate_races_seams_test.go` replaced by a base variant — one semaphore, no probe, no deadline;
`red/seams_base_test.go`), and the same against `a560c210` (`red/overlay-a560.json`; plus
`objectExists`, which TASK-003 added). Logs `red/red-base.log`, `red/red-a560.log`,
`red/red-*-ac1.log`, `gitfloor/out.log` (Linux red). "Green" = the branch (`red/green-head.log`).

| AC | Status | Evidence |
|---|---|---|
| 1 | done | `TestReceivePack_RefusedOnCaseFoldingStorage`: probe faked to "folds" → advertisement 403 naming the requirement; POST `refs/heads/MAIN` and `refs/heads/Main` with main's next commit → 403 before git (no ref, objects not in the repository, main unchanged); `http.receivepack=false`; fetch advertisement 200 and a real `git clone` work; probed once. Subtests: the review's `TestRV_F1_CaseFoldRef` push (packed and loose main) against the **real** probe — refused on the macOS host, an unrelated branch on Linux, main never moves; a failing probe → 500 for pushes, reads 200, probed again (3 calls); a case-sensitive store → a real `git push` lands, `http.receivepack=true`. **Red (base and a560c210, macOS):** advertisement 200; the MAIN pushes reach git; with main loose, `refs/heads/MAIN` **moved main** (`main moved to 27abf0ad…`); the probe-failure subtest serves pushes. Linux red: the faked-folding subtests fail the same way. Also `TestStorageProbe_MatchesTheHostFilesystem` (the probe equals an independent os-level check: macOS APFS folds case and normalization; a **case-sensitive APFS volume** (`hdiutil`, 20 MB) folds normalization only — the probe refuses it; Linux: neither; no entry left in `refs/`, no ref visible; work tree; a non-repository is an error). |
| 2 | done | `TestPrereceive_FreezeRaceRefusedAtApply` (ports `TestRV_PushAdmission_FreezeRace`; the pack is held until the pre-pack decision has consulted the step lookup): the step becomes `review` or `internal`, the execution is submitted, the step is reassigned, the run is paused → `remote: spine: push refused: ref-admission: …`, `pre-receive hook declined`, branch unchanged, quarantine discarded; with no change the held push lands; a push announced during review is refused up front. **Red (base and a560c210, macOS and Linux):** all five changed-state rows land (`the run branch moved to …`). |
| 3 | done | `TestPrereceive_EarlyRejectStillFires`: a non-bound member's run-branch push is answered with the admission refusal and `ng … pre-receive hook declined` while its pack is never sent. The fast path is unchanged (those assertions pass at the base). Two N-13 assertions are new and **red at the base:** the push held 1 slot while its command section was admitted (want 0), and it installed the pre-receive hook (the base installs it before the gate). |
| 4 | done | `TestGitHTTP_SlotsFairPerActorAndWorkspace` (slots held by upload-pack POSTs whose body never ends): actor A of ws-1 holds exactly 2; actor B of ws-2 is served; a second member fills ws-1 to 4, a third is refused 503 while ws-2 is served; other workspaces fill the ceiling of 8, then 503; everything released → 0 held; token-less reads keyed per peer (another peer served, the same peer from another port refused, an empty-ID actor keyed on its peer). **Red (base and a560c210):** "actor A of ws-1 held 5 git slots, want its per-actor cap 2". |
| 5 | done | `TestGitHTTP_SlowCommandSectionDoesNotHoldSlot` (one slot in all, 300 ms deadline, a real server, a raw TCP client that stops inside a pkt-line): no slot held during the stall; the same actor's read is served; the push is answered within the deadline (`the command section was not received within 300ms (SPINE_GIT_COMMAND_READ_TIMEOUT)`); a push stalled in its push-options section through a ResponseWriter that cannot set a read deadline is cut too. **Red (base and a560c210):** the stalled push holds the slot, the read gets 503, no answer within 3 s, the options stall is not cut. |
| 6 | done | Gates below: `go test ./service/internal/githttp/... ./service/internal/gateway/...` ok (macOS and Linux), `scenario-git-http` rc=0. |
| 7 | done | SMP gate below: no SMP change, inert (SMP imports no changed package: it cannot import `service/internal/...`). |
| 8 | done | `KNOWN-LIMITATIONS.md` ("enforced"), `docs/integration-guide.md` (Authentication, push section, Limits, env table), `CHANGELOG.md`; plus the git-push guide, security model, docker-runtime and the compose example, which described the window as open. |

Further tests: `TestRecheck_ChannelIsPrivateSingleUseAndTokenBound` (0700 dir; another push's or a
wrong token, another protocol version → no answer, no decision; the token works once; malformed
lines refused; a message is one line; close removes the directory), `TestRecheck_HookFailsClosed`
(malformed variables, unreachable server, garbage or truncated answers → refusal; the variables are
removed from the hook's environment; no variables → budget only; the token never reaches stderr),
`TestRecheck_UnannouncedUpdateIsRefused` (another ref, other ids, a duplicate → refused with no store
read; upper-case announced ids match), `TestRecheck_RunsWithRenormalisationPending` (handoff 2),
`TestPrereceive_OverrideAuditOnlyOnceThePushReachesGit`, `TestRecheck_StalledDecisionIsBoundedAndCancelled`
(codex round 1), `TestLoadGitHTTPLimits`, `TestLogGitHTTPLimits`; `TestNewHandler_*` and
`TestServeHTTP_ConcurrencyLimit` adapted to the slots.

### Carried handoffs

1. **One hook, two jobs; per-push data in the CGI env.** The TASK-003 script is unchanged (no
   per-push data in it). Per push the CGI environment carries the socket path and token;
   `PreReceiveHookMain` unsets both before running any git command. The credential is per push
   (fresh socket and token), single-purpose (only the re-check protocol), short-lived (removed when
   the push's request returns), never logged, refused for any other push (another push's token gets
   no answer: `TestRecheck_ChannelIsPrivateSingleUseAndTokenBound`); unreachable → refusal. TASK-003's
   budget behaviour and tests are unchanged (the measure moved verbatim into `measureTips`; every
   TASK-003 test passes, including through the real hook, now with the re-check armed).
2. **`git.ErrRenormalisePending`.** `installBudgetHook` still warns and goes on; the re-check is armed
   after it either way: `TestRecheck_RunsWithRenormalisationPending` (a frozen held push is refused, a
   normal one lands, the warning is logged).
3. **handler.go overlaps.** Changed: the Handler struct, `Config`, `NewHandler`, `ServeHTTP`'s
   ordering, slot, hook install and CGI env, `prereceive`'s reads and its return value,
   `readCommandSections`/`parseCommandSections` (new, around `readPushCommands`, which is
   unchanged), `ensureReceivePackConfig`. Not touched: `isReadOnly`, `IsReceivePack`,
   `IsNonCanonicalInfoRefs`, `isInfoRefs`, `infoRefsServiceOf` (TASK-004's). Moving the slot after
   the command read moved the hook install after it too (TASK-003's note).
4. **Keying** — table above.
5. **Owner questions** — recorded above.
6. **SMP parity** — recorded below, not touched.

### Adversarial self-review

| # | Attempt | Result |
|---|---|---|
| 1 | Hold the pack until the step is a review step, then send it (N-9) | Refused at apply time (AC 2). |
| 2 | Announce one update, have git apply another (a parser differential) | The hook's lines must be announced updates; otherwise refused without a store read. |
| 3 | Reach the re-check socket from another push or process | 0700 directory (same uid only); a wrong or another push's token gets no answer; single use. |
| 4 | Make the hook skip the re-check | The hook re-checks whenever the variables are present; ServeHTTP always sets them for a push it serves; the client cannot set or remove CGI variables (headers arrive as `HTTP_*`). A hook without them is not serving an endpoint push (residual: an operator's local push into the repository, which no HTTP gate covers). |
| 5 | Stall a store read during the re-check to keep the slot | Codex round 1: bounded by the connection deadline and cancelled on close. |
| 6 | Stall before or inside the command section (N-13) | No slot held; cut at the command-read deadline (AC 5). |
| 7 | One actor, or one workspace, fills the slots | Per-principal 2, per-workspace 4 (AC 4). |
| 8 | Token-less reads share one key | Keyed per peer (AC 4). |
| 9 | Push `refs/heads/MAIN` on APFS (N-8) | Refused before git (AC 1, real probe on macOS). |
| 10 | Case-sensitive but normalization-insensitive storage (case-sensitive APFS) | The NFC/NFD pair refuses it (verified on an `hdiutil` volume; mutation M3). |
| 11 | Make the probe answer "sensitive" by failing | An error is never "sensitive": pushes refused, retried. |
| 12 | Probe files seen as refs | Dot-directory (git's loose-ref iteration skips names starting with '.', and no valid ref name has such a component), removed at once; `for-each-ref` shows nothing. |
| 13 | Override audit for a push that never reached git | Events written only once slot, hook and re-check are held (as before, when the slot came first). |
| 14 | Not covered | The window between the server's answer and git's ref update; branch-protection rules evaluated once (on the command section); the actor's role is the one authenticated at request start; a stalled fetch body or pack holds its own principal's slot until the 30 s `ReadTimeout`; members of two workspaces can fill the ceiling; caps are per replica (KNOWN-LIMITATIONS). |

### Deviations

1. **Probe at the filesystem level, not with `git update-ref`** (the design said "create two ref
   files under a private `refs/spine-probe/` prefix"). A git-level probe needs an object to point
   the refs at (a fresh repository has none), shows the probe refs to concurrent readers, runs
   reference-transaction hooks, and a `refs/spine-probe/` namespace would be a real, pushable-looking
   ref namespace. The probe instead creates the files itself, with O_EXCL, in a private
   dot-directory under `refs/` (invisible to git, unaddressable as a ref) — the directory where the
   aliasing happens. Three pairs, not one: ASCII case alone admits a case-sensitive APFS volume, which
   still folds normalization. Consequence: a reftable repository on folding storage is refused too,
   although reftable does not alias names (fail closed; noted).
2. **Every push the endpoint serves arms the re-check**, not only pushes with store-backed refs: the
   hook cannot tell "not needed" from "not served by Spine", and the announced-update check applies
   to every push. Cost: one socket and one exchange per push; ordinary-branch and operator decisions
   read no store.
3. **`FakeCaseSensitiveStorageOnFoldingHostForTests`** (exported from the internal githttp package,
   called only from the `TestMain` of githttp, gateway and the scenario package). Without it every
   push test fails on a macOS development host (the real probe refuses APFS, correctly). It changes
   nothing on a case-sensitive host, so CI and the bookworm `git-floor` run exercise the real probe in
   every push test; no embedder can call it (internal package).
4. **Override events moved** from `prereceive`'s allow path to just before CGI (after slot, hook and
   re-check), keeping the old observable behaviour (no event for a push refused before CGI).
5. **`MaxConcurrent` default 5 → 8** (OQ1's global), and a single-workspace server's capacity is now
   its per-workspace cap (4). CHANGELOG "Changed (operators)".
6. **`ensureReceivePackConfig` runs before the slot**, serialised on first use per repository
   (`ensureMu`), so the storage refusal precedes any body read; afterwards it is a map lookup.

### Mutations (one per new check; `go test -overlay`, logs `mut/`) — all killed

| # | Mutation | Killed by |
|---|---|---|
| M1 | drop the folding refusal in ServeHTTP | AC 1 (git-http-backend still answers 403 with no message, since the probe set `http.receivepack=false`) |
| M2 | a probe error counts as case-sensitive | AC 1 probe-failure subtest |
| M3 | drop the NFC/NFD pair | `TestStorageProbe_MatchesTheHostFilesystem` on the case-sensitive APFS volume |
| M4 | the re-check env not passed to CGI | AC 2 |
| M5 | no announced-update check | `TestRecheck_UnannouncedUpdateIsRefused` |
| M6 | any token accepted | channel test |
| M7 | token reusable | channel test |
| M8 | an unreachable server admits | `TestRecheck_HookFailsClosed` |
| M9 | the re-check admits everything | AC 2 |
| M10 | slot taken before admission (the base order) | AC 3, AC 5 |
| M11 | no per-principal cap | AC 4 |
| M12 | no per-workspace cap | AC 4 |
| M13 | token-less reads share one key | AC 4 |
| M14 | no command-read deadline | AC 5 |
| M15 | the read is not cut by the read deadline (body close only) | AC 5 |
| M16 | override events emitted before the slot | `TestPrereceive_OverrideAuditOnlyOnceThePushReachesGit` |

### Gates

At `a1b6965b` (the code is unchanged since; the notes commit is docs only).

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | rc=0 |
| `make build`; `go build ./cmd/embed-smoketest && ./embed-smoketest` | rc=0; rc=0 ("library surface is sound") |
| `go vet ./...` plain / `-tags integration` / `-tags scenario` | rc=0 / 0 / 0 |
| `go test -p 4 ./...` (heavy lock) | rc=0 (41 ok, 3 without tests) |
| `-race -p 4` on the touched packages (`githttp`, `gateway`, `cmd/spine`) | rc=0 |
| integration `-tags integration` (same packages), own DB `s24005_ci_final` (dropped) | rc=0 |
| `scenario-git-http` job: `TestGitHTTP_*` / `TestBootstrapInternalAdmin_*` / `TestWorkflowLifecycle_*` | rc=0: 90 / 26 / 154 passes, 0 skips, 8 `TestGitHTTP_Push*` passes (macOS: the folding-host fake, Deviation 3) |
| `git-floor` (golang:1.26-bookworm, git 2.39.5, `--pull=never`, read-only mounts): both CI steps; githttp, gateway and cmd/spine in full (Linux: the real probe in every push test, the Unix-socket re-check); the AC and re-check tests verbose; the AC tests at the base | STEP1 / STEP2 / EXTRA / EXTRA2 rc=0; the probe test reports "folds case: false, Unicode normalization: false"; RED rc=1 (the same AC failures as on macOS) |
| `scenario-git-http` on Linux (the same bookworm image, git 2.39.5, own DB `s24005_ci_linux`, dropped): the CI job's filesystem class, so the real probe | rc=0: 90 passes, 0 skips, 8 `TestGitHTTP_Push*` passes, no fake installed |
| golangci-lint v2.11.4 `--new-from-rev=init/INIT-024-wave3-base` on githttp, gateway, cmd/spine (and the scenario package with its tag) | 0 issues (2 gocritic and 3 gofmt found during the work and fixed); `--enable-only=gosec`: 0 issues |
| `gofmt -l` on changed files | clean |
| codex | below |

### SMP compatibility gate

`smp` (SMP local main `82899ace`, detached; `../spine` = this worktree at `a1b6965b`, the only
uncommitted file this artifact): `go build ./...` rc=0; `go vet` plain / integration / scenario /
faultinject rc=0; `go test -race -p 4` over the initiative §5 packages (`cmd/customer-app`,
`internal/customer/{githttp,gitread,projectionwriter,spinecore/...,spineembedded/...,stepevents,
workspace/...,run/...}`, `internal/engine/...`) rc=0 with no failure (the two by-design gitread
contract tests TASK-003 recorded against SMP `3addd44b` pass on `82899ace`); `dev/testgate`
Spine-runtime leg PASS (2,447 passed, 0 failed, 3 allowed skips) and githttp leg PASS (590 passed, 1
allowed skip); platform and spine-runtime migrations applied to `s24005_*` databases (dropped); no
`ws_*` database or role created or left. Inert by construction: SMP imports no package this task
changed (`service/internal/...` and `cmd/` are not importable from SMP). No SMP change.

### Codex (`codex review --base init/INIT-024-wave3-base`, plain; model `gpt-6.1-sol` confirmed in each review sub-session)

| Round | Review session | Findings | Disposition |
|---|---|---|---|
| 1 | `01a17ee4-2568-7091-9445-2d5ecd3ae5e4` (on `c9a6969b`) | P2: a stalled store read during the apply-time decision is not interrupted (request context, no deadline), and `rc.close()` waits for it, so the push's git slot is kept after the hook has given up | Real, fixed `a1b6965b`: the session owns a cancellable context, each decision ends at its connection's deadline, `close` cancels first. `TestRecheck_StalledDecisionIsBoundedAndCancelled` — red at `c9a6969b` (`close waited for a stalled decision`, `the decision has no deadline`; `red/red-codex-r1.log`), green |
| 2 | `01a17eea-92b0-7021-bfc3-6567a2fe37b1` (on `a1b6965b`) | none ("No actionable regressions were identified") | — clean |

### Not run

- The full `make test-scenario` with a registry DB (only the CI job's three scenario groups; known
  ~31 pre-existing failures). Command: `SPINE_TEST_DATABASE_URL=… SPINE_REGISTRY_TEST_DATABASE_URL=…
  make test-scenario`.
- GitHub CI (nothing is pushed).
- A Windows host; an SMB or ext4-casefold store (the probe was checked on APFS, a case-sensitive APFS
  volume and Linux ext4/overlayfs).

### SMP parity (follow-up for the SMP companion epic, INIT-027 EPIC-005; not owned here)

SMP's `/git/{workspace}` gateway (`internal/customer/githttp`) embeds none of this code (the package
is internal to Spine), so it needs its own:

- **Storage probe**: the same three name pairs, O_EXCL, in a dot-directory under the clone's
  `refs/`, once per repository; refuse receive-pack (and set `http.receivepack=false`) on folding
  storage, keep reads. Write the names as escapes.
- **Apply-time re-check**: SMP's own pre-receive admission (`refadmit.go`, SMP TASK-007) decides at
  parse time too. The pattern that carries over: a real pre-receive hook calling back over a per-push
  channel whose path and one-time token travel in the per-request CGI environment (never in the
  shared hook script), every hook line required to be an announced update, the same admission
  re-run with a fresh evaluator under a deadline cancelled when the request ends, fail closed.
- **Fair slots and a command-read deadline**: key by (workspace, actor) or (workspace, peer) for
  token-less reads, take the slot only after the command section has been read and admitted, cut a
  stalled command section without moving the connection's read deadline forward.

### Integrator notes

- **TASK-004** edits the read path-classification functions of `githttp/handler.go` (`isReadOnly`,
  `IsReceivePack`, `isInfoRefs`, `infoRefsServiceOf`); this task did not touch them. Both change
  `ServeHTTP`'s file; this task rewrote everything after the "unsupported git operation" block
  (ensure/probe, storage refusal, `prereceive`, slot, hook, re-check, CGI env) and `prereceive`'s
  head and tail. A conflict there is resolved by keeping this task's order.
- **SMP bump**: inert — no embedded package changed.

### Review round 1 (2026-10-28; security + correctness review of `f4baae12`: 0 P1/P2, 2 P3, P4s)

Report: SMP repo `tmp/init027-orchestration/i24/t005-reviews/REVIEW.md`; the reviewer's probes and
mutants `i24/t005-rev/`. Rulings by the coordinator. Red runs at `f4baae12`: `go test -overlay`
mapping the five githttp source files and the seams file to their `f4baae12` content (plus a
no-op `pgrSetProbeRetry`), the round-1 internals-only test file removed (`rr1/overlay-f4ba.json`,
log `rr1/red-f4ba.log`). Mutations: `mut2/` (summary `mut2/summary.txt`).

| Finding | Ruling | What was done | Tests (red / mutation) |
|---|---|---|---|
| **P3-1** pre-admission work uncapped: pushes sit in their command read and admission before any slot, so one member could run unbounded concurrent admissions (the reviewer's R3: 40/40 inside, no 503) | Non-blocking admission counter per principal and per workspace before `readCommandSections`, released when `prereceive` returns; 503 + `Retry-After`; 2x the per-actor and per-workspace caps; no new knob; no ref-count cap, the 1 MiB command section is the residual | `Handler.admissions` (a second `gitSlots`: `admissionFactor`=2 x per-actor, 2 x per-workspace, no global cap), taken in `ServeHTTP` for `POST git-receive-pack` before `prereceive`, released right after it; "too many concurrent git pushes being admitted" 503. It holds no git slot. Single-mode caps follow P3-2 (16/16 by default). KNOWN-LIMITATIONS N-13 names the residual (each push in admission may buffer the 1 MiB command section and evaluates admission and branch protection per ref). No new variable. | `TestGitHTTP_PreAdmissionWorkIsBounded` (R3's shape: 10 stalled pushes of one principal → 4 inside, 6 x 503; a second member fills ws-1 to 8; a third gets 503 x 3; another workspace is unaffected; no slot held; released when the pushes end) — **red at `f4baae12`**: "alice of ws-1: 0 of 10 pushes answered, want 6"; MA1 (no counter), MA2 (no per-workspace bound) killed |
| **P3-2** single mode had 4 slots (base 5), shared-token runners 2, and raising `SPINE_GIT_HTTP_MAX_CONCURRENT` did nothing | Single mode: per-workspace and per-actor default to the global cap (8); shared keeps 2/4/8; warn when the ceiling is set above an explicit per-workspace cap in single mode | `gitHTTPLimits.effective(shared)`; `buildGitHTTPHandler` hands githttp the mode's values; `logGitHTTPLimits(log, l, shared)` logs `mode=` and the single-mode warning. CHANGELOG, integration guide (Limits, env table), KNOWN-LIMITATIONS, the OQ1 note above. | `TestGitHTTPLimits_EffectiveCapsByMode` (7 rows, both modes), `TestLogGitHTTPLimits` (both modes, the warning in single mode only), `TestBuildGitHTTPHandler_SlotDefaultsFollowTheMode`; MS1 (single mode with the shared defaults) killed |
| **P4-3** the body-close fallback cannot interrupt a real net/http body | Call it best-effort in the comment and docs; a gateway-level AC5 test through the full middleware chain | `readCommandSections` doc; KNOWN-LIMITATIONS and integration guide. | `TestGitHTTP_GatewayCutsStalledCommandSection` (`gateway.NewServer(...).Handler()`: recovery, security headers, rate limit, CORS, trace, logging; an authenticated push stalled in a pkt-line is answered at the 300 ms deadline with no server ReadTimeout; one slot in all, the actor's clone is served meanwhile); MG1 (`statusWriter` without `Unwrap`) killed: "not cut through the gateway within 3s" |
| **P4-4** mutants MR2 (prefix token compare) and MR5 (reader goroutine not joined) survived; the channel test leaked its socket dir on failure | Empty and truncated token rows; a body recording reads after ServeHTTP returns; `t.Cleanup` | Channel test: empty, 32-hex, 63-hex and extended tokens get no answer; every session closed by `t.Cleanup`. | MR2 killed ("a empty token got an answer"); `TestPrereceive_CommandReaderEndsBeforeServeHTTPReturns` (a body whose reads return 50 ms late, through the fallback path) — MR5 killed ("1 read(s) of the request body returned after ServeHTTP had returned"). Both pass at `f4baae12` (they pin the existing behaviour). |
| **P4-5** the probe override was callable from any service code | Refuse unless `testing.Testing()`; test the refusal if possible in one binary | `FakeCaseSensitiveStorageOnFoldingHostForTests` returns `errNotATestBinary` and changes nothing unless `isTestBinary()` (= `testing.Testing`). Importing `testing` adds no flags to `spine` (`spine --help` and `spine serve --help` show none). | `TestFakeCaseSensitiveStorage_RefusedOutsideATestBinary` (swaps `isTestBinary` in-process — no second binary needed); MF1 killed |
| **P4-6** a failing probe re-ran on every request (reads too), with two config writes under the handler-wide mutex | Re-probe only on receive-pack requests; per-repository singleflight; ~30 s negative cache | `repoAlignment` per repository path (its own mutex; `ensureMu`, `ensuredRepos`, `storageFolds` removed): with receive-pack off every request pins `false` once; with it on only push requests run `ensurePushStorage` (probe, then config to match); reads never touch the probe or the config. A failed probe's refusal is reused for `probeRetry` (30 s) and writes the config `false` best-effort. | `TestStorageProbe_ReadsNeverWaitAndPushesProbeOnce` (reads are served while a push's probe is held; 6 concurrent first pushes run 1 probe) — **red at `f4baae12`**: "a read waited on a push's probe"; the probe-failure subtest (two pushes and a read in the window run 1 probe; after the window a push probes again) — **red at `f4baae12`**: "probe ran 3 times within the retry window"; MP1 (reads probe), MP2 (no negative cache), MP3 (no per-repository lock) killed |
| **P4-7** the re-check variables reach the repository's other hooks (undocumented); close did not close accepted connections | Doc sentence; stop listening once the token is used; close closes accepted connections; test | `admission_recheck.go` header and integration guide: the variables also reach `update`/`post-receive`-style hooks, by when the token is spent and the socket closed. The listener closes as soon as the token is accepted; `close` closes every connection it has accepted before waiting. | `TestRecheck_CloseDoesNotWaitForStalledConnections` (a same-user connection that sends nothing; and no dial succeeds after the token was used) — **red at `f4baae12`**: "close waited for a stalled connection (until its 1-minute deadline)"; MC1, MC2 killed; the channel test's second-use check now accepts the closed socket |
| **P4-8** a stalled upload-pack body holds its slot until ReadTimeout | Keep as the documented residual | — | — |
| **P4-9** docs: reftable refusal, the heading at `:93`, the TMPDIR cases | Fix the docs | KNOWN-LIMITATIONS: the reftable false positive, the heading "Run-Branch Admission Is Checked at Push Start and Again at Apply Time" (and its reference); integration guide: reftable, TMPDIR longer than 64 bytes → `/tmp`, a relative TMPDIR fails every push closed. | — |
| **P4-10** the probe ran in `refs/`, the protected loose refs live in `refs/heads` | Move the dot-directory into `refs/heads`; show for-each-ref, pack-refs and a clone never see it, on both git versions | `probeRefStorage` uses `refs/heads` (`refs/` when that is not a directory — a reftable repository's placeholder file), `probeDirHookForTest`. | `TestStorageProbe_InvisibleToGit` (while the probe files exist: `for-each-ref`, `ls-remote`, `pack-refs --all` and the packed-refs it writes, a `--mirror` clone show nothing; nothing left after): git 2.54 (macOS) files and reftable; git 2.39.5 (git-floor) files (reftable needs git >= 2.45: skipped there); MR10 (probe in `refs/`) killed |

**Round-0 mutations re-run on the new head** (M1, M2, M4, M10, M14, M15; the code they target
moved): all killed. M1 now answers `500` (the folds refusal falls through to the fail-closed
branch), still caught by the message check. **All 19 mutations killed** (`mut2/summary.txt`).

**Gates after round 1** (code at `604a47c7`, docs `e91b1a8f`; heavy suites under the lock):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; embed-smoketest | rc=0; rc=0; rc=0 |
| `go vet` plain / integration / scenario | rc=0 / 0 / 0 |
| `go test -p 4 ./...` | rc=0 (41 ok, 3 without tests) |
| `-race` and `-tags integration` on githttp, gateway, cmd/spine (DB `s24005_ci_rr1`, dropped) | rc=0 / rc=0 |
| `scenario-git-http` / bootstrap-admin / workflow-lifecycle (macOS) | rc=0: 90 / 26 / 154 passes, 0 skips, 8 `TestGitHTTP_Push*` |
| `scenario-git-http` on Linux (bookworm, git 2.39.5, real probe) | rc=0: 90 passes, 0 skips, 8 push passes, no fake |
| `git-floor` (bookworm, git 2.39.5): CI steps, githttp/gateway/cmd/spine in full, the AC and round-1 tests verbose, the base red | STEP1 / STEP2 / EXTRA / EXTRA2 rc=0; RED rc=1 as expected |
| golangci-lint `--new-from-rev=init/INIT-024-wave3-base` (githttp, gateway, cmd/spine); gosec-only; gofmt | 0 issues; 0 issues; clean |
| SMP gate (82899ace) | build, vet x4 rc=0; race (§5 packages) rc=0, no failure; testgate runtime PASS (2,447, 3 allowed skips); githttp PASS (590, 1 allowed skip); no `ws_*` left; inert |
| codex round 3 | below |

**Codex round 3** (`codex review --base init/INIT-024-wave3-base`, plain, on `e91b1a8f`): session
`01a182a2-1e92-7242-a1ae-20badc63c9e7`, model `gpt-6.1-sol` confirmed — "No actionable regressions
were identified in the diff." Clean; the third and last round.

**Deviations in round 1:** none from the rulings. Recorded choices: the admission counter reuses
`gitSlots` with no process-wide cap (the ruling named per principal and per workspace); in single
mode it is therefore 16/16 by default; the probe falls back to `refs/` when `refs/heads` is not a
directory (reftable), rather than refusing outright; P4-9's TMPDIR cases are documented, not
changed (the ruling asked for docs).

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
  git-identity fix) passed all 8 jobs, among them `git-floor` on the pinned image and the
  `scenario-git-http` job on Linux storage, where the real probe runs. Every AC is met, and the
  owner questions stay at their recorded defaults.
