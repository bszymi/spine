---
id: TASK-012
type: Task
title: "Push re-check: no answer once the decision's deadline has passed"
status: In Progress
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
