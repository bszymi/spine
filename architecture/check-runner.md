---
type: Architecture
title: Check Runner Integration Boundary
status: Living Document
version: "0.1"
---

# Check Runner Integration Boundary

---

## 1. Purpose

This document defines the **check runner integration boundary** — the narrow interface that takes a single policy check declaration and produces a structured Result classified as pass / fail / timeout / unavailable.

[EPIC-006](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md) commits Spine to evidence-from-code-repos that maps cleanly into the [execution evidence schema](/architecture/execution-evidence.md). The boundary documented here is the lowest-level moving part: it is what an actual check executor (a local shell, a CI dispatcher, a human-review form) plugs into.

The boundary lives in [`internal/checkrunner`](/internal/checkrunner). The split between this boundary and the [validation policy format](/architecture/validation-policy.md) is deliberate: the policy format defines **declaration** shape, and the check runner defines **execution** shape. The same `domain.PolicyCheck` value flows from one to the other unchanged.

---

## 2. Scope

### 2.1 In Scope

- The `Runner` interface signature
- Runner-internal classification: `OutcomePass`, `OutcomeFail`, `OutcomeTimeout`, `OutcomeUnavailable`
- The `Request` and `Result` types passed across the boundary
- The `LocalCommandRunner` shape — how the package's first concrete runner translates a `kind=command` check into a child process under a working tree
- `Normalize` — translation from a runner Result into a `domain.CheckResult` row that fits the execution evidence schema
- Log handling: how raw command output stays out of evidence (TASK-003 deliverable: "Preserve raw logs as references, not inline evidence")

### 2.2 Out of Scope

- Cloning the repository working tree — owned by [TASK-005](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-005-runner-clone-context.md). The runner takes a `WorkingDir` path; whoever populates it is upstream.
- Persisting evidence files — owned by TASK-001 / TASK-005. The runner emits a single CheckResult row; the caller assembles it into an `ExecutionEvidence` document and writes it.
- Aggregating per-run status across multiple checks — owned by [TASK-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-004-validation-service-evidence-rules.md). Each Run produces N CheckResults; aggregation is the validation service's job.
- Selector evaluation (does this policy apply to this run?) — owned by `domain.PolicySelector.MatchesRepository` / `MatchesAnyPath`. The runner is invoked only after applicability has been decided.

---

## 3. The Runner Interface

```go
type Runner interface {
    Run(ctx context.Context, req Request) (Result, error)
}
```

The interface is the integration point. Concrete implementations include:

| Implementation | Status | Handles |
|----------------|--------|---------|
| `LocalCommandRunner` | shipped (TASK-003) | `domain.PolicyCheckKindCommand` checks executed against a local working tree. |
| External-CI runner | future | `domain.PolicyCheckKindExternal` checks dispatched to an external system. The contract is identical; the implementation routes by `Request.Check.Kind`. |

The interface does NOT switch on `Check.Kind` itself — that's the caller's choice. A composition layer (e.g. `KindRouter`) MAY be added later when more than one runner ships; no such layer exists today because TASK-003 explicitly does not commit to "a specific CI provider". Each kind that wants a concrete implementation owns its own type.

### 3.1 Error semantics

`Run` returns `(Result, error)`. The error channel is reserved for **runner-internal failures the operator must debug**:

- log sink open / close I/O failures
- contract violations from dependencies (e.g. a `LogSink` returning `(nil, nil)`)

Routine outcomes — including a deterministic non-zero exit (`OutcomeFail`) and a policy timeout (`OutcomeTimeout`) — are NOT errors; they are valid Results returned with `err == nil`. This split keeps caller code uniform: errors are exceptional, outcomes are routine.

---

## 4. Request and Result

### 4.1 Request

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `RepositoryID` | yes | string | Workspace-scoped repository ID (per ADR-013). |
| `BranchName` | yes | string | The run branch the check is being evaluated against. |
| `CommitSHA` | optional | string | Head commit of `BranchName` at invocation time. Used by the caller to tie the resulting evidence row back to a commit. |
| `WorkingDir` | conditional | string | Local filesystem path to the cloned repo working tree. Required for `kind=command`. The runner does NOT clone — the path must already be on `BranchName`. |
| `Check` | yes | `domain.PolicyCheck` | The policy check declaration. Carries `Kind`, `Command`, `TimeoutSeconds`, `Severity`. |

### 4.2 Result

| Field | Type | Description |
|-------|------|-------------|
| `Outcome` | `Outcome` | `pass` / `fail` / `timeout` / `unavailable` (§5). |
| `StartedAt` | `time.Time` | UTC. Brackets the actual command execution. |
| `CompletedAt` | `time.Time` | UTC. |
| `ExitCode` | int | Process exit status. Meaningful only when `Outcome == Pass / Fail`. |
| `Reason` | string | Single-line classification tag (e.g. `"exit 1"`, `"context deadline exceeded"`, `"working_dir does not exist"`). MUST NOT contain newlines — it lands in `CheckResult.Summary`, which evidence `Validate` rejects with `\n`. |
| `LogReference` | string | Opaque pointer to where the runner's log sink stored captured output. Empty when no sink was configured. `Normalize` (§7) copies this into `CheckResult.EvidenceURI`. |

---

## 5. Outcome Classification

| Outcome | Meaning | Evidence mapping (`Normalize`) |
|---------|---------|--------------------------------|
| `pass` | Check ran to completion with success verdict (exit 0). | `domain.CheckStatusPassed` |
| `fail` | Check ran to completion with deterministic failure (non-zero exit). | `domain.CheckStatusFailed` |
| `timeout` | Did not complete within `Check.TimeoutSeconds`. The verdict is unknown. | `domain.CheckStatusError` |
| `unavailable` | Could not produce a verdict (missing tool / missing working tree / unsupported kind / configuration error / caller cancellation). | `domain.CheckStatusError` |

Why four runner outcomes for three evidence statuses: the evidence schema deliberately collapses `Timeout` and `Unavailable` into `Error` because audit consumers care about "the verdict is unknown", not whether the unknown was a deadline or a missing tool. The runner-internal distinction is preserved in `Result.Reason`, which `Normalize` copies into `CheckResult.Summary` so the operator-level surface still has the diagnostic.

`OutcomeSkipped` does NOT exist at this layer. A "skipped" verdict means "the policy declared this check but it didn't apply to this run" — the caller decides applicability via the policy selector and constructs `CheckResult{Status: CheckStatusSkipped}` directly, without invoking the runner.

### 5.1 Classification precedence (LocalCommandRunner)

Order matters in `classifyExit`:

1. **Leader exited cleanly** (`cmd.ProcessState.ExitCode() ≥ 0`) → honour the leader's verdict regardless of subsequent context state. Once the leader produced an exit code, post-run pipe drain that crosses a deadline or a `WaitDelay` only annotates the Reason; it does not erase the verdict. Within this branch:
   - `0` → `Pass`.
   - `127` (POSIX shell: command not found in PATH) → `Unavailable`. Environment failure: the runner image lacks the tool the policy declared.
   - `126` (POSIX shell: command found but not executable) → `Unavailable`. Same classification.
   - Anything else → `Fail`.
2. **Parent context deadline / canceled** → `Unavailable`. Caller signals a stop before the leader produced a verdict; reported as `caller deadline exceeded` or `context canceled`. Caller signals take precedence over policy timeouts because the caller didn't want the check to keep running.
3. **Run context deadline exceeded** (only when leader was killed mid-execution, no clean exit code) → `Timeout`. Checked BEFORE `*exec.ExitError` so a SIGKILLed leader (signal-style exit code) reports as `Timeout` rather than as a non-zero `Fail`.
4. **`runErr == nil`** → `Pass` (defensive — should be covered by #1).
5. **`*exec.ExitError`** (signalled, no clean exit code) → `Fail` with the signal-style exit code.
6. **Anything else** (binary not found at exec time, fork failure) → `Unavailable`.

---

## 6. Log Handling

### 6.1 Logs as references, not inline evidence

The TASK-003 deliverable commits explicitly: *"Preserve raw logs as references, not inline evidence."* The runner enforces this at the type boundary: `Result` has no `Stdout` / `Stderr` / `Output` field. Captured bytes flow through a separate `LogSink` interface, and `Result.LogReference` is the opaque pointer that ties them back together.

```go
type LogSink interface {
    OpenLog(ref string) (io.WriteCloser, error)
}
```

Why this matters:

- **Secret leakage.** Inlining stdout in committed evidence would re-introduce the secret-leak surface that the [`ChangedPathsSummary`](/architecture/execution-evidence.md) was specifically built to avoid. Code under check can contain credentials; build commands routinely echo environment variables; inlining is a one-step path from check execution to leaked secret in audit history.
- **Audit size.** A `make test` rebuild loop or a `find /` typo can produce gigabytes of stdout. Inline storage scales with the runaway, not with the verdict.

### 6.2 MaxLogBytes truncation

`LocalCommandRunner.MaxLogBytes` caps how many bytes the runner forwards to the sink. When the cap fires the runner appends a single-line marker (`[checkrunner: log truncated at N bytes]`) so audit readers can tell the log is incomplete versus terminated by the process. The cap is an **integrity property**: a runaway producer must NOT be able to silently consume unbounded sink storage, and downstream consumers must be able to tell that what they're reading is incomplete.

### 6.3 LogReference shape

The reference is opaque to callers. The runner's internal format embeds the repository ID, branch name, check ID, and a high-resolution UTC start timestamp, with non-`[A-Za-z0-9_-]` bytes replaced by `_`. This means:

- Two parallel Runs cannot collide on a flat-file sink.
- A reference is safe to use as a filesystem path segment or an S3-style key — sinks do not have to defend independently against path traversal in caller-supplied repository / branch names.

Callers MUST treat the string as opaque. The format is internal and may evolve.

### 6.4 WaitDelay

`LocalCommandRunner.WaitDelay` bounds how long the runner waits for stdout/stderr pipes to drain after the policy timeout fires. Without it, `sh -c "sleep 30"` (or any check that spawns descendants) hangs the runner for the full sleep: SIGKILL kills the parent shell, but the orphaned `sleep` keeps the pipe write end open, so `cmd.Wait` stays blocked until the kernel reaps the descendant. Defaults to 5 seconds when zero — long enough for legitimate I/O drain, short enough to bound runner latency.

---

## 7. Normalization Into Evidence

`Normalize(req Request, res Result, producer string) domain.CheckResult` is the single function callers use to translate a Runner Result into a CheckResult row. It is the **only** path from runner outcomes to evidence rows; centralizing it makes the mapping testable and prevents the schema invariants from drifting if a new caller is added.

```go
row := checkrunner.Normalize(req, res, "spine/runner@host-1")
evidence.CheckResults = append(evidence.CheckResults, row)
```

The `producer` parameter is required (no default). EPIC-006 audit consumers want a non-empty `produced_by` on every non-pending row; silent fallbacks defeat the audit guarantee.

`Normalize` copies `Result.LogReference` into `CheckResult.EvidenceURI` so audit readers can find captured stdout/stderr from the evidence row alone — without that copy the log linkage would be silently lost. Callers that need a transformed reference (wrap a filesystem path in a `file://` URL, prefix with an object-store host) overwrite `row.EvidenceURI` after the call returns.

`Normalize` produces a row that always passes `ExecutionEvidence.Validate` when slotted into a complete record (round-trip test in `normalize_test.go::TestNormalize_RoundTripIntoEvidence`). If the runner produces a Reason with a newline, it is rejected by the runner itself (`sanitizeReason`) before ever reaching Normalize.

---

## 8. Concurrency

`Runner` is a value type. `LocalCommandRunner.Run` is safe to call concurrently on the same instance: every Run captures its own start/end timestamps, opens its own log sink invocation, and cleans up its own context. A test (`TestLocalCommandRunner_ConcurrentRuns`) pins this contract.

---

## 9. What This Document Does Not Define

- The validation service's evidence rules — see [TASK-004](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-004-validation-service-evidence-rules.md). The runner produces evidence rows; rule evaluation reads them.
- Reporting / query surfaces over evidence — see [TASK-005](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-005-evidence-query-and-reporting.md).
- The validation policy artifact format — see [validation-policy.md](/architecture/validation-policy.md). The runner consumes a `domain.PolicyCheck`; how that check landed on disk is policy-format territory.
- The execution evidence schema itself — see [execution-evidence.md](/architecture/execution-evidence.md). Normalize produces a `domain.CheckResult`; the schema owns what fields exist on it.
