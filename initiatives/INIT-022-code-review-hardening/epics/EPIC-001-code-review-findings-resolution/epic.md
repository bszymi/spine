---
id: EPIC-001
type: Epic
title: Code Review Findings Resolution
status: Completed
acceptance: Approved
acceptance_rationale: |
  All 37 tasks (TASK-001 through TASK-037) are Completed/Approved.
  Coverage spans P1/P2 findings from the 2026-05-07 — 2026-05-12
  codex review passes against the INIT-014/-021 batch and the
  follow-up INIT-022 batch itself: governance/ID hygiene, store
  interface partitioning, run lifecycle clock seams, scenario-test
  load-bearing assertions, CGI env whitelist, secrets-cipher
  validation, scheduler timeouts, branchprotect rule_source error
  handling, pooled workspace bootstrap, and run-timeout side-effect
  timestamp coherence. No follow-up tasks queued; further findings
  would land in a new initiative.
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
owner: bszymi
created: 2026-05-07
last_updated: 2026-05-14
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/initiative.md
---

# EPIC-001 — Code Review Findings Resolution

---

## 1. Purpose

Resolve every concrete finding from the 2026-05-07 full-codebase code
review across four lenses (code quality, unit-test coverage,
scenario-test coverage, security). One task per finding so each fix
is a discrete, individually merge-able PR with its own codex pass.

Finding severity follows the convention used elsewhere in this repo:

- **P1** — production-correctness, security risk, or data-loss
  potential. Ship first.
- **P2** — high-impact hardening; production hazard with limited blast
  radius, or a high-value test gap.
- **P3** — quality, polish, or hardening of a non-exploitable surface.

## 2. Scope

### In Scope

- 37 tasks total: 29 resolving findings from the 2026-05-07 review
  and 8 follow-ups (TASK-030 — TASK-037) resolving findings from the
  2026-05-12 codex re-review of the INIT-022 batch itself.
- Each task is independently scoped: cite a single file:line surface,
  carry its own deliverable, acceptance criteria, and rollback note.
- Tasks SHOULD ship as one PR each; P3 polish tasks MAY bundle when
  they share files, recorded in the task frontmatter.

### Out of Scope

- Net-new features or architecture changes beyond what the findings
  call for.
- The 466 historical Completed-without-acceptance tasks flagged by
  `internal/validation/rules_status.go::SC-001` — separate sweep,
  needs tooling.
- INIT-014 v0.x deferrals (`RepositoryManager` auto-wire, Git-backed
  `CatalogStore`, production `RunReferenceChecker`) — those are about
  filling missing wiring, not validating the wiring that exists.

## 3. Task Inventory

### P1 — Production correctness (8 tasks)

| ID | Title | Surface |
|---|---|---|
| TASK-001 | Wire gitpool `WithRepoBase` and validate `LocalPath` containment | `internal/repository/manager.go`, `cmd/spine/cmd_serve.go`, `internal/workspace/pool.go` |
| TASK-002 | Restrict `repository.ValidateCloneURL` to safe schemes | `internal/repository/clone_url.go` |
| TASK-003 | Add `internal/yamlsafe/decoder_test.go` | `internal/yamlsafe/decoder.go` |
| TASK-004 | Add `internal/git/branchwrite_test.go` | `internal/git/branchwrite.go` |
| TASK-005 | Build `harness.WithCodeRepos` helper for multi-repo scenarios | `internal/scenariotest/harness/` |
| TASK-006 | Scenario: partial-merge retry happy path | `internal/scenariotest/scenarios/` |
| TASK-007 | Scenario: partial-merge external resolution | `internal/scenariotest/scenarios/` |
| TASK-008 | Scenario: cancel from `partially-merged` | `internal/scenariotest/scenarios/` |

### P2 — High impact (13 tasks)

| ID | Title | Surface |
|---|---|---|
| TASK-009 | Insert `--` sentinels and `ValidateRef` in git Diff/MergeBase/Clone argv | `internal/git/cli.go`, `internal/gitpool/pool.go` |
| TASK-010 | Split `internal/store.Store` into role-specific interfaces | `internal/store/store.go` |
| TASK-011 | Bound `internal/queue` dispatch goroutines | `internal/queue/memory.go` |
| TASK-012 | Log + propagate cleanup errors in engine.run and divergence.service | `internal/engine/run.go`, `internal/divergence/service.go` |
| TASK-013 | Wrap AWS Secrets Manager errors with `%w` | `internal/secrets/aws.go` |
| TASK-014 | Replace branchprotect rule_source `panic` with returned error | `internal/branchprotect/projection/rule_source.go` |
| TASK-015 | Drop pool mutex around resolver call | `internal/workspace/pool.go` |
| TASK-016 | Scenario: run-timeout cancellation | `internal/scenariotest/scenarios/` |
| TASK-017 | Scenario: repository deactivate while runs are open | `internal/scenariotest/scenarios/` |
| TASK-018 | Scenario: BootstrapInternalAdmin idempotency | `internal/scenariotest/scenarios/` |
| TASK-019 | Unit tests for delivery bootstrap and retention | `internal/delivery/` |
| TASK-020 | Unit tests for actor selection strategies | `internal/actor/` |
| TASK-021 | Unit tests for auth.Authorize suspended-actor path | `internal/auth/` |

### P3 — Polish and hardening (8 tasks)

| ID | Title | Surface |
|---|---|---|
| TASK-022 | Split `cmd/spine/cmd_serve.go` (1712 LOC) | `cmd/spine/` |
| TASK-023 | Extract phase helpers from MergeRunBranch and checkrunner.Run | `internal/engine/merge.go`, `internal/checkrunner/local_command.go` |
| TASK-024 | Delete dead code in workflow.binding and divergence.convergence | `internal/workflow/binding.go`, `internal/divergence/convergence.go` |
| TASK-025 | Lock `cgi.Handler.Env` whitelist with a regression test | `internal/githttp/handler.go` |
| TASK-026 | Harden `secrets/file.go` VersionID against second-resolution mtime | `internal/secrets/file.go` |
| TASK-027 | Strict-startup error for bootstrap-admin hash collision | `internal/auth/bootstrap.go` |
| TASK-028 | Build `harness.AdvanceClock` primitive | `internal/scenariotest/harness/` |
| TASK-029 | Per-handler typed minimal-store for gateway tests | `internal/gateway/` |

### Post-merge codex pass (8 tasks, 2026-05-12)

Findings surfaced by `codex review --commit <sha>` run across all 29
merged INIT-022 commits. All P2; no P0/P1 introduced. Each cites the
INIT-022 commit it follows up on.

| ID | Title | Surface |
|---|---|---|
| TASK-030 | Normalise worktree paths in branchwrite_test on macOS | `internal/git/branchwrite_test.go` |
| TASK-031 | Accept explicit primary repo in harness.WithCodeRepos resolver | `internal/scenariotest/harness/multirepo.go` |
| TASK-032 | Assert Actor-ID trailer in partial-merge external resolution scenario | `internal/scenariotest/scenarios/partial_merge_external_resolution_test.go` |
| TASK-033 | Avoid head-of-line blocking in internal/queue deferred list | `internal/queue/memory.go` |
| TASK-034 | Couple run resolver to registered binding in repository-deactivate scenario | `internal/scenariotest/scenarios/repository_deactivate_during_run_test.go` |
| TASK-035 | Drive bootstrap-admin scenario through workspace-scoped gateway | `internal/scenariotest/scenarios/bootstrap_admin_idempotency_test.go` |
| TASK-036 | Seed narrow inherited vars before asserting cgi.Handler.Env whitelist | `internal/githttp/cgi_env_test.go` |
| TASK-037 | Thread injected clock into run-timeout side-effect timestamps | `internal/scheduler/run_timeout.go` |

## 4. Success Criteria

- All 8 P1 tasks Completed/Approved.
- All 13 P2 tasks Completed/Approved OR explicitly Cancelled with a recorded rationale.
- All 8 P3 tasks resolved (Completed, Cancelled, or forwarded).
- All 8 post-merge codex tasks (TASK-030 — TASK-037) resolved.
- Re-running the four-lens review reports zero P1 findings on the same surfaces.
- Re-running `codex review` over the new TASK-030 — TASK-037 commits surfaces no P1 findings.

## 5. Cross-Repo Coordination

None. All findings live within the Spine repository. Some tasks
(TASK-001, TASK-002) interact with the platform binding shape but the
validation lives entirely on Spine's intake side and does not require
SMP changes.
