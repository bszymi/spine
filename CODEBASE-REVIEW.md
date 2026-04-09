# Spine Codebase Review

## Executive Summary

Reviewed the full codebase across 5 dimensions: core domain logic, infrastructure/data layer, CLI/API/observability, test quality, and build/deployment. Found ~90 issues total.

---

## CRITICAL (5 issues)

### 1. Branch checkout races — global Git state corruption
**`internal/artifact/service.go:344-361`**

`enterBranch` uses `git checkout` which changes the working tree for the entire OS process. The `branchMu` mutex serializes within the artifact service, but the Orchestrator holds a separate `git.GitClient` and calls `CreateBranch`, `Merge`, `Push` independently. A concurrent `MergeRunBranch` call races against an artifact write on a planning branch. Artifacts can be committed to the wrong branch under concurrent load, silently corrupting the repo.

### 2. Branch step retries lose BranchID — run state corruption
**`internal/engine/retry.go:78-86`**

When `RetryStep` creates the retry execution, it does not copy `exec.BranchID`. The new execution has `BranchID: ""`. When the retried step completes, `SubmitStepResult` treats it as a top-level step, bypassing all branch state management. A transient failure on a branch step corrupts the run state machine.

### 3. Execution handlers bypass authorization entirely
**`internal/gateway/handlers_execution_query.go`, `handlers_claim.go`, `handlers_release.go`, `handlers_candidates.go`**

Seven execution handlers (claim, release, candidates, 4 query handlers) are in the authenticated route group but none call `s.authorize()` for operation-level RBAC. Any authenticated user with any role (including `reader`) can claim a step, release an assignment, or see all execution state.

### 4. Execution handlers bypass workspace isolation
**`internal/gateway/handlers_execution_query.go`**

All four execution query handlers call `s.store.QueryExecutionProjections()` using the server-level store directly instead of `s.storeFrom(r.Context())`. In multi-workspace mode, this leaks task execution data across workspace boundaries.

### 5. `task-default.yaml` commits `In Progress` status — directly violates governance
**`workflows/task-default.yaml:34`**

The workflow commits `In Progress` to the Git artifact on the `draft -> execute` transition. `governance/task-lifecycle.md` explicitly states `In Progress` is a runtime-only state that must never be committed to Git. This directly contradicts the constitution's principle that runtime states must not pollute versioned artifacts.

---

## HIGH (18 issues)

### Core Domain
6. **`workflow/parser.go:272-284`** — `canTerminate` false-positives reject valid divergence workflows (checks each step independently instead of traversing from entry point)
7. **`engine/consumer.go:32-78`** — `Consumer.ctx` struct field written/read without synchronization (data race detectable by race detector)
8. **`artifact/parser.go:192-211`** — `splitFrontMatter` accepts `---` prefix without newline requirement, causing malformed documents to pass parsing
9. **`artifact/acceptance.go:115-147`, `successor.go:136-178`** — Line-based YAML manipulation matches `---` in markdown body, `statusRegexp` replaces `status:` in body content
10. **`engine/candidates.go:40`** — Unguarded type assertion panics if blocking store doesn't implement `candidateQuerier`
11. **`artifact/renumber.go:42-83`** — No path containment check; path traversal possible via `artifactPath` argument in collision handling
12. **`engine/run.go:275-283`, `merge.go:128-130`** — Duplicate `run_completed` event possible on scheduler retry after merge

### Infrastructure
13. **`workspace/pool.go:107-120`** — Global mutex held during database network I/O in `Get()`, blocking all workspace operations during a slow DB connect
14. **`workspace/provision.go:178`** — Git URL (potentially containing credentials) logged in plaintext
15. **`queue/memory.go:45-66`** — TOCTOU race on idempotency check: two goroutines with the same key can both pass before either records
16. **`store/postgres.go:805-819`** — `UpsertArtifactLinks` delete+insert outside transaction; crash mid-insert leaves no links
17. **`divergence/service.go:162-183`** — DB branch record created before Git branch with no rollback on Git failure

### API/Gateway
18. **`gateway/handlers_claim.go:23-36`, `handlers_release.go:23-40`** — Claim/release accept arbitrary `actor_id` without verifying it matches the authenticated caller
19. **`gateway/handlers_claim.go:26`, `handlers_release.go:27`** — No body size limit (bypass `decodeJSON` helper's 1MB `LimitReader`)
20. **`projection/service.go:261-273`** — `LastSyncedCommit[:8]` panics on empty string, crashing the sync goroutine permanently
21. **`gateway/middleware.go:156-168`** — `X-Trace-Id` header accepted from clients verbatim without validation (log injection risk)

### Test Infrastructure
22. **`store/testutil.go`** — Auth tables missing from `CleanupTestData`; actor/skill data leaks across tests causing INSERT conflicts

### Build/Config
23. **`.gitignore`** — Compiled `spine` binary at repo root is not gitignored (17MB binary could be accidentally committed)

---

## MEDIUM (35 issues)

### Core Domain
24. `artifact/successor.go:63-85` — `nextFollowupID` collision: TASK-001 and TASK-101 both map to TASK-901
25. `engine/step.go:361-363` — Branch steps sharing step ID causes `findStepExecution` to return wrong execution
26. `engine/blocking.go:131-133` — `updateExecutionProjection` silently swallows all store errors, not just "not found"
27. `workflow/parser.go:337-354` — Schema validation doesn't validate divergence branch `StartStep` references
28. `engine/blocking.go:60-61` — `BlockedBy`/`Resolved` path format inconsistency (with/without leading `/`)
29. `artifact/acceptance.go:62-88` — `setAcceptance` reads from Git HEAD and filesystem simultaneously (can be out of sync)
30. `artifact/successor.go:95-96` — `buildSuccessorContent` doesn't escape title in YAML (quotes break YAML)
31. `workflow/parser.go:286-306` — `canReachEnd` creates O(n^2) map copies on deeply branched workflows

### Infrastructure
32. `workspace/db_provider.go:83,196` — String comparison instead of `errors.Is(err, pgx.ErrNoRows)`
33. `workspace/pool.go:38` — `refCount int32` misleading about atomic use; `Release` accepts non-canonical IDs
34. `scheduler/recovery.go:111-128` — `recoverPendingRuns` non-atomic: run status + step creation can split across failures
35. `queue/memory.go` — `idempotencySet` and `acknowledged` maps grow unboundedly (memory leak)
36. `queue/memory.go:129-144` — No-subscriber requeue goroutines pile up indefinitely
37. `store/postgres.go:419-437` — Missing index on `divergence_id` for lookup queries (full table scans)
38. `store/postgres.go:745-751` — `LIMIT %d` interpolated directly (safe today but fragile pattern)
39. `divergence/convergence.go:170-224` — `CommitConvergence` partial Git merge failure leaves inconsistent state
40. `event/router_impl.go:58-75` — O(n) full JSON deserialize per event across all subscriber types
41. `event/router_impl.go:59` — `handlers` map concurrent write without mutex during `Subscribe`
42. `auth/auth.go:62` — Token ID derived from first 16 chars of plaintext (should be independent random)
43. Duplicate migration number `002_` (two files share the prefix)

### API/Gateway
44. `auth/permissions.go` — `assignments.list` operation missing from permissions map (always rejected)
45. `gateway/routes.go:22` — `/system/metrics` endpoint unauthenticated, exposes internal telemetry
46. `auth/permissions.go:61` — `skill.deprecate` mapped to `RoleContributor` (should be `RoleOperator`)
47. `gateway/handlers_skills.go:179-186` — Skill deprecation 409 response uses non-standard body structure
48. `gateway/handlers_artifacts.go:246` — `handleArtifactValidate` silently ignores decode errors
49. `gateway/handlers_tasks.go:105-110` — Supersede action discards `successor_path`
50. `validation/engine.go:79` — `ValidateAll` hard-capped at 1000 artifacts with no truncation warning
51. `cli/initrepo.go:141` — `git add .` stages all files including potential secrets
52. `gateway/handlers_workspaces.go:147-157` — `repo_path` (server filesystem path) exposed in API response

### Tests
53. Hardcoded actor/skill IDs shared across test files that share a database
54. `time.Sleep`-based async assertions in `service_test.go`, `router_test.go`, `memory_test.go`
55. `workspace/pool_test.go` — 5ms sleep with 1ms idle threshold (flaky on slow CI)
56. `resilience_projection_test.go` — Double cleanup invalidates DB state mid-scenario
57. `governance_constitution_test.go` — Only tests success path despite name claiming "CrossArtifact Validation"
58. Workspace scenario tests bypass scenario framework, missing fail-fast semantics

### Build/Config
59. `epic-lifecycle.yaml` — `blocked` outcome routes back to `plan` instead of `execute`
60. `adr-creation.yaml` — `rejected` outcome maps to `Deprecated` status (semantically wrong)
61. `adr-creation.yaml` — Missing retry config on validate step (inconsistent with peer workflows)
62. Governance hierarchy contradiction: Charter says Charter > Constitution, Constitution says Constitution > Charter
63. `guidelines.md` — Outdated/incomplete Task status list includes runtime-only `In Progress`
64. `repository-structure.md` — Out of sync with actual directory names and file listing
65. ADR files use 3-digit IDs; governance mandates 4-digit
66. Makefile `docker-lint` installs golangci-lint `@latest` and suppresses errors

---

## LOW (27 issues)

### Core Domain
67. `engine/run.go:112` — Entry step activation failure logged as warning and swallowed
68. `engine/merge.go:105-110` — `abortMerge` passes `--abort` as source branch name
69. `artifact/service.go:225-228` — `Update` existence check before branch switch (wrong working tree)
70. `artifact/service.go:296-338` — `List` uses different git listing semantics for root vs subdir
71. `engine/claim.go:129` — Re-claim produces duplicate assignment ID
72. `workflow/binding.go:48-66` — `specificCandidates` dead code invites future logic error
73. `engine/branchname.go:34-37` — `prefix` variable shadowing (correct but misleading)

### Infrastructure
74. `scheduler/scheduler.go` — `commitMaxRetries` declared but never enforced; `commitRetryInterval` not configurable
75. `scheduler/timeout.go:50` — Off-by-one in timeout boundary check (`<=` vs `<`)
76. `config/config.go` — No validation of `artifacts_dir` for path traversal
77. `git/cli.go:222` — `HasCommitWithTrailer` loads entire git history into memory
78. `store/postgres.go:915` — `ListActiveWorkflowProjections` uses `'Active'` (capital A) vs lowercase status elsewhere
79. `store/postgres.go:1241-1276` — `ApplyMigrations` has no distributed lock
80. Migrations 004-006 missing self-recording INSERT INTO schema_migrations
81. `runtime.queue_entries` table unused (code uses in-memory queue)

### API/Gateway
82. No HTTP security headers (`X-Content-Type-Options`, etc.)
83. No rate limiting on any endpoint
84. `cli/inspect.go` — URL paths built by concatenation without encoding
85. `observe/audit.go` — `[AUDIT]` as log message, not structured field
86. `observe/export.go` — Prometheus output missing `HELP` lines
87. `spine health` CLI returns hardcoded healthy (never checks actual health)
88. Metrics counters only incremented on success, not failures

### Tests
89. Typo: `IgnesOtherClassifications` in `failure_test.go`
90. `fakeStore` embeds nil `store.Store` — panics on unimplemented methods
91. `context.Background()` used instead of `sc.Ctx` in `skill_system_test.go:251`

### Build
92. No `-race` flag on test targets in Makefile
93. Missing `.env` pattern in `.gitignore`
94. Dependencies outdated: pgx v5.8.0 (latest v5.9.1), cobra v1.9.1 (latest v1.10.2)

---

## Top Recommendations (Priority Order)

1. **Fix the branch checkout race** (#1) — This is the most dangerous bug. Consider using git worktrees instead of checking out branches in the main working tree, or serializing all git operations through a single goroutine.

2. **Add authorization to execution handlers** (#3) — Straightforward fix: add `s.authorize()` calls matching the pattern used in all other handlers.

3. **Fix workspace isolation bypass** (#4) — Change `s.store` to `s.storeFrom(r.Context())` in the 4 execution query handlers.

4. **Fix branch step retry losing BranchID** (#2) — Copy `exec.BranchID` into `nextExec` in `RetryStep`.

5. **Fix `task-default.yaml` governance violation** (#5) — Change `In Progress` to a governed status like `Pending` or remove the commit status entirely and let the runtime track it.

6. **Add auth table cleanup to `CleanupTestData`** (#22) — Add the 3 auth tables to the cleanup list.

7. **Add body size limits to claim/release handlers** (#19) — Use `decodeJSON` helper instead of direct `json.NewDecoder`.

8. **Fix idempotency TOCTOU race in queue** (#15) — Hold the lock across both check and publish.

9. **Add path containment check to `RenumberArtifact`** (#11) — Add the same `safePath` check used in `Service`.

10. **Resolve governance hierarchy contradiction** (#62) — Pick one document as supreme and align the other.
