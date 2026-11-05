---
id: TASK-012
type: Task
title: "Push re-check: no answer once the decision's deadline has passed"
status: Completed
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-11-05
last_updated: 2026-11-05
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
  - type: related_to
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-002-git-transport-and-reads/tasks/TASK-005-standalone-push-gate-races.md
---

# TASK-012 — Push re-check: no answer once the decision's deadline has passed

---

## Purpose

GitHub CI run 42047981897 at Spine `main` `39841330` failed the `go test ./...` job on one
subtest, `TestRecheck_StalledDecisionIsBoundedAndCancelled/a_decision_ends_at_the_connection's_deadline`
(`service/internal/githttp/push_gate_recheck_test.go:455`, "an answer after the deadline"). The
commit changed only `initiatives/`; the same test passed on the earlier pushes. The failure shows a
timing gap in TASK-005's apply-time re-check server, which this task closes.

## Finding

| ID | Sev | Reach | Defect |
|---|---|---|---|
| R-1 (CI run 42047981897) | P3 | Standalone `spine serve` with receive-pack on | `recheckSession.handle` (`service/internal/githttp/admission_recheck.go`) gives the decision a context that ends at the connection's deadline and then writes the decision's answer. Its comment says an answer after the deadline "could not be written anyway". That does not hold: the context's timer and the connection's deadline are separate timers, so a decision that returns as soon as its context ends can still have its answer written just after the deadline. The server's own rule — no answer after the deadline, so the hook refuses — was left to the decider. |

**Reach today.** The shipped decider (`Handler.recheckAdmission`) runs `refadmit.Evaluator.Decide`,
whose store reads use the decision's context. A read under an ended context returns an error,
which `recheckAdmission` turns into a refusal (`refadmit.EvaluationErrorMessage`), and an
unresolvable step definition refuses too (`ReasonStepUnresolved`). An answer written after the
deadline is therefore a refusal, or an admission computed from completed reads. This is a
fail-closed gap in the server, not an admission bypass. The test's stalled decider, which returns
no refusals when its context ends, is what exposes it.

## Design

1. After `s.decide` returns, `handle` checks the decision's context. If it has ended — the
   deadline passed or the session was closed — the server sends no answer and closes the
   connection, so the hook gets no well-formed answer and refuses the push (`errRecheckFailed`).
2. An answer is written only for a decision that finished while its context was live. Writing it
   can still fail at the connection's deadline; the hook refuses in that case as before.
3. The comment states the rule the code now enforces.

## Acceptance Criteria

1. `handle` writes no answer when the decision's context has ended by the time the decision
   returns, whatever the decider returned.
2. `TestRecheck_StalledDecisionIsBoundedAndCancelled` passes deterministically:
   `go test -race -count=300 -run TestRecheck_StalledDecisionIsBoundedAndCancelled
   ./service/internal/githttp/` passes. A fail-first run against the code before the fix is
   recorded in the Implementation Notes, or the notes say that it could not be reproduced locally.
3. A named test pins the rule against a decider that returns an admission after its context
   ended, and fails when the context check is removed.
4. No other behaviour changes: every other `TestRecheck_*` and push-gate test passes unchanged.
5. Gates:
   - `go build ./...`, `go vet ./...`, `go test ./...`, gofmt on touched files;
   - `golangci-lint run --new-from-rev main` on `service/internal/githttp` (0 new issues);
   - `./dev/lint-boundaries.sh`;
   - a codex review with no open P1/P2.
6. SMP: `service/internal/githttp` is internal to Spine's `service` tree, so SMP cannot import it.
   Record that SMP's pin is unaffected.

## Out of scope

- The window between the server's answer and git's ref update (TASK-005's recorded residual).
- Any change to the hook's side of the channel or to `refadmit`.

---

## Implementation Notes

Branch `init/INIT-024-TASK-012-recheck-answer-after-deadline` from `main` `39841330`. Commits: the
task artifact (`c3f19cf5`), then the fix and its test (`f3174bd7`), then these notes.

### Change

- `service/internal/githttp/admission_recheck.go`, `recheckSession.handle`: after `s.decide`
  returns, `if dctx.Err() != nil { return }` before `reply`. A decision whose context ended (its
  deadline passed, or `close` cancelled it) gets no answer, whatever it returned, and the deferred
  `conn.Close` ends the connection; the hook then gets no well-formed answer and refuses
  (`errRecheckFailed`). The comment now states this rule and why the connection's deadline alone
  does not enforce it.
- `push_gate_recheck_test.go`: `TestRecheck_NoAnswerAfterTheDecisionContextEnded`, two subtests
  (a decider that returns an admission, and one that returns a refusal, after its context ended).
  The context ends through the request's context while the connection's deadline is a minute away
  and the session stays open, so the result does not depend on timer order.
- No other file changed outside `initiatives/`. The hook's side and `refadmit` are unchanged.

### Root cause, confirmed

`handle` set `conn.SetDeadline(deadline)` and gave the decision `context.WithDeadline(s.ctx,
deadline)` with the same instant. These are separate runtime timers. When the context's timer fires
first, a decider that returns on `ctx.Done()` reaches `reply` while the connection's deadline has
not yet been marked expired, and the write succeeds. The CI subtest's stalled decider returns no
refusals when its context ends, so the answer written was `ok`.

### Production before the fix: fail closed

The shipped decider is `Handler.recheckAdmission`. It calls `refadmit.Evaluator.Decide`, whose run,
step-execution and branch reads take the decision's context (`core/refadmit/evaluator.go`,
`getRun`, `listExecs`, `findBranch`). A read under an ended context returns an error, `Decide`
returns it, and `recheckAdmission` turns it into a refusal (`refadmit.EvaluationErrorMessage`). An
unresolvable step definition refuses as well (`ReasonStepUnresolved`). Refs that need no read
(ordinary branches, operators, invalid refs) are decided by the ref and actor alone, so their
answer does not depend on timing. Before the fix, then, an answer written after the deadline was a
refusal or a decision computed from completed reads, never an admission from an interrupted read.
The fix makes the server enforce the rule itself.

### Gates

| Gate | Result |
|---|---|
| Fail-first: `go test -race -count=200 -cpu 1,2,8 -run 'TestRecheck_StalledDecisionIsBoundedAndCancelled/a_decision' ./service/internal/githttp/` at `c3f19cf5` (no code change yet) | 7 FAIL of 600 runs, each `an answer after the deadline: "ok\n"`, the CI failure |
| The same command with the fix | 600 PASS, 0 FAIL |
| `go test -race -count=300 -run TestRecheck_StalledDecisionIsBoundedAndCancelled ./service/internal/githttp/` (AC 2) | PASS |
| `TestRecheck_NoAnswerAfterTheDecisionContextEnded`, `-race -count=20` | PASS |
| Mutation: the `dctx.Err()` check removed | the new test fails 5 of 5 runs, both subtests (`"ok\n"` and `"refuse\npush refused\n"` written); restored |
| `go build ./...`; `go vet ./...` | OK; clean |
| `go test -count=1 ./...` | 41 packages ok, rc 0 |
| `gofmt -l` on the two touched Go files | empty |
| `golangci-lint run --new-from-rev main ./service/internal/githttp/...` | 0 issues |
| `./dev/lint-boundaries.sh` | OK |
| `codex review --base main` (gpt-6.1-sol) | No findings: "The change correctly suppresses replies when the decision context has ended, and the added regression test covers both admission and refusal responses." |

### SMP

`service/internal/githttp` sits under Spine's `service/internal/`, which Go's internal-package rule
keeps out of reach of any package outside `service/`, so SMP cannot import it, and SMP's Go code
has no import of `spine/service/internal`. SMP's pin is unaffected. Since `ce0b9c9e` (SMP's
`.spine-revision`), the only change outside `initiatives/` is this task's two files.

### Acceptance Criteria

1. Met: the context check in `handle` (Change, above).
2. Met: 300 of 300 at `-race`; the fail-first run reproduced 7 of 600 before the fix.
3. Met: `TestRecheck_NoAnswerAfterTheDecisionContextEnded`; the mutation fails it deterministically.
4. Met: every other `TestRecheck_*` and push-gate test passes in the full `go test ./...`.
5. Met: every gate in the table, and codex has no findings.
6. Met: SMP is unaffected (SMP, above).
