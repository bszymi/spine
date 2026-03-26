---
id: TASK-003
type: Task
title: "Proof-of-Concept Spike"
status: Completed
work_type: spike
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-001-architecture-design/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-001-architecture-design/epic.md
---

# TASK-003 — Proof-of-Concept Spike

---

## Purpose

Validate key architectural decisions through a minimal working proof-of-concept. Build the thinnest possible vertical slice: create a temp Git repo, bootstrap Spine, create one artifact, and assert its existence — proving the harness + engine + assertion stack works end-to-end.

## Deliverable

Working proof-of-concept code (can be throwaway or foundational) that demonstrates:

- Temporary Git repository creation and cleanup
- Spine runtime bootstrap within test context
- One artifact created and committed via helpers
- One assertion validating the artifact exists and has correct structure
- Clean teardown on test completion

## Acceptance Criteria

- Proof-of-concept runs as a Go test and passes
- Demonstrates the full vertical slice: environment setup -> action -> assertion -> teardown
- Validates that the architecture spec's design is feasible
- Findings are documented: what worked, what needs adjustment, any surprises
- Architecture spec is updated if the spike reveals necessary design changes

## Spike Findings

### What Worked

1. **4-layer architecture is sound.** The harness/engine/assert/scenarios layering maps cleanly to Go packages with no circular import issues. Dependency direction (scenarios -> engine -> harness, scenarios -> assert -> harness) is natural.

2. **Event router is safely nil-able.** Both `artifact.Service.emitEvent` and `projection.NewService` handle nil event routers gracefully. This simplifies the harness significantly — the queue/router wiring is entirely optional for scenarios that don't need event-driven behaviour.

3. **`FullRebuild` is the natural test sync path.** Calling `FullRebuild` after artifact creation fulfills the architecture spec's "Projection sync runs synchronously in tests" requirement without needing the queue or polling loop.

4. **Existing test utilities compose well.** `testutil.NewTempRepo`, `testutil.WriteFile`, `testutil.GitAdd`, `store.NewTestStore` all work as building blocks for the harness wrappers without modification.

### What Needs Adjustment

1. **`CleanupTestData` was missing `projection.sync_state`.** The existing cleanup function did not clear the sync_state table, which would cause cross-test pollution when using `FullRebuild`. Fixed in this spike by adding `projection.sync_state` to the cleanup table list.

2. **Timestamp non-determinism in `artifact.Service`.** The architecture spec promises deterministic timestamps, but `artifact.Service.Create` uses `os.Environ()` internally for git commits without injecting `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE`. Commits created by the service will have the current system time, not the `2026-01-01` pinned by `testutil.NewTempRepo`. Future work needs a `TimeProvider` injection or environment variable propagation in the service's `execCommand`.

3. **Orchestrator complexity is real.** `engine.New()` requires 7 non-nil interfaces (`WorkflowResolver`, `RunStore`, `ActorAssigner`, `ArtifactReader`, `EventEmitter`, `GitOperator`, `WorkflowLoader`). A `TestRuntime` that includes the orchestrator will require a dedicated harness helper. This is deferred to EPIC-002.

### No Architecture Spec Changes Required

The spike validated that the architecture spec's design is feasible as-is. No structural changes to the 4-layer model, package structure, or API shapes are needed.
