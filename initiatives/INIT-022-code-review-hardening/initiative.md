---
id: INIT-022
type: Initiative
title: Code Review Hardening
status: Pending
owner: bszymi
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: related_to
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
  - type: related_to
    target: /architecture/components.md
---

# INIT-022 — Code Review Hardening

---

## 1. Intent

A full-codebase code review on 2026-05-07 — covering code quality,
unit-test coverage, scenario-test coverage, and security — surfaced a
small number of production-correctness issues plus a long tail of
quality and coverage gaps. This initiative tracks the resolution of
those findings as discrete, individually merge-able tasks so each fix
ships with its own PR, codex pass, and frontmatter closure.

The review's executive verdict was that the codebase is mature and
well-disciplined overall (structured logging never leaks bearers,
secrets redact themselves at boundary, pgx parameterization is
universal, gitpool concurrency tests exercise real goroutine ordering),
but four findings sit in the production-correctness band:

- **Operator-supplied repository `local_path` is never validated** and
  the gitpool sandbox (`gitpool.WithRepoBase`) is not wired in
  production — an Operator with `repository.create` can hand the
  workspace any host path.
- **`repository.ValidateCloneURL` accepts `file://` and `git://`** —
  diverges from the stricter `git.ValidateCloneURL` and exposes
  arbitrary local paths through `git show`.
- **`internal/yamlsafe/decoder.go` has zero tests** — the billion-laughs
  / depth-bomb guard is the only defense between six YAML ingestion
  seams and a DoS class.
- **`internal/git/branchwrite.go` has no dedicated test** — a documented
  TOCTOU mitigation and cancellation-survivable cleanup are unverified.

A fifth P1 sits in coverage rather than code: three INIT-014
multi-repo recovery flows (`RetryRepositoryMerge`,
`ResolveRepositoryMergeExternally`, cancel-from-`partially-merged`)
shipped with unit tests only, no scenario guard.

The remaining tasks address P2 hardening (argv `--` sentinels in git
shellouts, log+propagate cleanup errors, bound queue dispatch
goroutines, split the 111-method `Store` interface, drop the pool
mutex around the resolver call) and P3 polish (`cmd_serve.go` split,
`cgi.Handler.Env` whitelist, harness `AdvanceClock` primitive, dead-code
removal).

---

## 2. Scope

### In scope

- Production-correctness fixes (P1 security and P1 coverage gaps).
- High-impact hardening (P2 code quality, P2 security, P2 coverage).
- Quality polish (P3 — long functions, dead code, harness affordances,
  hardening of non-exploitable surfaces).
- One epic — `EPIC-001 — Code Review Findings Resolution` — with one
  task per finding so each fix is a discrete PR.

### Out of scope

- Sweeping the **466 historical Completed-without-acceptance tasks**
  flagged by the validation engine — that backlog is a separate
  multi-iteration project that needs tooling.
- Auto-wiring of `RepositoryManager`, Git-backed `CatalogStore`, and a
  production `RunReferenceChecker` — these are tracked as INIT-014
  v0.x deferrals; this initiative narrows to **input validation** of
  the existing surface, not new wiring.
- Supply-chain / SBOM / container hardening — out of band.

---

## 3. Success Criteria

This initiative is successful when:

1. The five P1 findings (two security, two unit-test, one scenario
   bundle) all land on `main` with green codex passes.
2. The P2 hardening tasks land or are explicitly deferred with a
   rationale recorded in the task frontmatter.
3. P3 polish tasks land or are explicitly deferred — none of them are
   blockers, but each should be evaluated.
4. After this initiative, a fresh full-repo review re-running the same
   four lenses surfaces no P1 findings in the same surfaces.

---

## 4. Primary Artifacts Produced

- 29 task files under
  `/initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/`
- Code changes across `internal/repository`, `internal/git`,
  `internal/gitpool`, `internal/yamlsafe`, `internal/queue`,
  `internal/engine`, `internal/divergence`, `internal/workspace`,
  `internal/secrets`, `internal/auth`, `internal/githttp`,
  `internal/store`, `internal/scenariotest/harness`, and
  `cmd/spine/`.
- New test files for `yamlsafe`, `git/branchwrite`, multi-repo recovery
  scenarios, run-timeout scenario, deactivate-while-runs scenario,
  bootstrap-admin idempotency scenario, delivery bootstrap+retention,
  actor selection, auth Authorize.

---

## 5. Constraints and Non-Negotiables

This initiative must comply with the Spine Constitution, including:

- **Source of Truth** — all changes are tracked through the standard
  governance commit format.
- **Disposable Database** — none of these tasks introduce schema
  migrations beyond the existing migration discipline; if any do,
  they ship behind the standard migration framework.
- **Reproducibility** — every fix must include a regression test, OR
  must explicitly document why a regression test is impractical.

Additional constraints:

- **No combined PRs.** Each P1 and P2 task is its own PR so the codex
  cascade per change is small. P3 polish tasks may bundle when they
  share files, recorded in the task frontmatter.
- **No silent behavior changes.** Any externally observable change
  (HTTP error class, validation contract, log shape) must be called out
  in the task frontmatter and the architecture docs updated in the
  same PR.

---

## 6. Risks

- **P1 security fix surface area.** `LocalPath` validation + `WithRepoBase`
  wiring touches `internal/repository/manager.go`,
  `cmd/spine/cmd_serve.go`, and `internal/workspace/pool.go`. A wrong
  containment check could lock operators out of legitimate paths.
  *Mitigation:* validation table-test before shipping; preserve the
  exact error class for invalid paths so existing alerts still fire.
- **`Store` interface split could mask a missed method.** The interface
  has 111 methods. Splitting into role interfaces risks a forgotten
  method only caught at runtime via the workspace `ServiceSet`'s `any`
  fields.
  *Mitigation:* compile-time `var _ Store = (*PostgresStore)(nil)`
  preserved alongside per-role compile-time assertions.
- **Scenario-harness `WithCodeRepos` change touches every multi-repo
  test.** Refactoring shared helpers risks scenario regressions.
  *Mitigation:* land helper as additive first, migrate one scenario at
  a time.

---

## 7. Work Breakdown

### Epics

```
/initiatives/INIT-022-code-review-hardening/
  /epics/
    /EPIC-001-code-review-findings-resolution/
```

| Epic | Title | Dependencies |
|------|-------|--------------|
| EPIC-001 | Code Review Findings Resolution | — |

EPIC-001 holds 29 tasks (8 P1, 13 P2, 8 P3) — see the epic file for
the full table.

---

## 8. Exit Criteria

INIT-022 may be marked complete when:

- All P1 tasks are Completed/Approved on `main`.
- All P2 tasks are Completed/Approved OR explicitly Cancelled with a
  recorded rationale.
- All P3 tasks are Completed/Approved, Cancelled, or moved to a
  separate hardening backlog with a forwarding link.
- A re-run of the original four-lens review reports zero P1 findings
  in the same surfaces.

---

## 9. Links

- Charter: `/governance/charter.md`
- Constitution: `/governance/constitution.md`
- Components: `/architecture/components.md`
- Predecessor multi-repo work: `/initiatives/INIT-014-multi-repository-workspaces/initiative.md`
- Predecessor secret/pool work: `/initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md`
