# Known Limitations — Spine v0.x

This document tracks known gaps and deferred functionality in the INIT-002 implementation.

## Missing Functionality

### WriteContext / Branch Writes
- Artifact writes always go to the authoritative branch (main/HEAD)
- `write_context` field in API requests is accepted but ignored
- Task-branch scoped writes require Git branch integration not yet wired

### Idempotency-Key
- Header is accepted on write operations
- No deduplication store — duplicate requests are not detected
- Idempotency is achieved via Git commit detection (Trace-ID trailer) for artifact operations

### Workflow Binding
- `resolveWorkflow` returns defaults — full workflow binding via `ResolveBinding` not wired into run creation
- `WorkflowPath` and `WorkflowID` are set to empty on API-created runs
- Work type filtering in `applies_to` not implemented

### Queue Consumers
- Step assignment messages are published to queue but no consumer delivers them
- Human actors: no notification mechanism
- AI agents: provider interface defined but no real provider (Anthropic/OpenAI) integrated
- Automated systems: no webhook delivery

### Events Not Emitted
The following event types are defined but not emitted in any code path:
- `run_completed`, `run_failed`, `run_cancelled`, `run_paused`, `run_resumed`
- `step_started`, `step_failed`, `retry_attempted`
- `projection_synced`, `thread_created`, `comment_added`, `thread_resolved`
- `validation_passed`, `validation_failed`, `step_assignment_failed`

These events would be emitted by a full engine orchestrator (future work).

### Git Commit Retry
- Runs stuck in `committing` state have no automatic retry mechanism
- Scheduler recovery logs the situation but cannot re-attempt Git commits

### CLI
- `spine init-repo` command is a placeholder
- No `spine query` subcommands (artifacts, graph, history, runs)

## Test Coverage

| Package | Coverage | Notes |
|---------|----------|-------|
| gateway | 73.9% | Handler paths need integration tests with real services |
| scheduler | 79.7% | Start/Stop lifecycle + some error paths uncovered |
| projection | 0% | Requires live Git repo + database for testing |
| store | 0% | Requires live PostgreSQL (integration tests exist separately) |
| testutil | 40.7% | Utility package, coverage not critical |

## Security Notes

### Token Hashing
- SHA-256 used for API token storage (appropriate for 256-bit random tokens)
- Not bcrypt/argon2 — tokens are not user-chosen passwords, they are cryptographically random
- If tokens were user-chosen, bcrypt would be required

### Auth Bypass in Dev Mode
- When `SPINE_DATABASE_URL` is not set, auth service is nil
- Auth middleware returns 503 (fail closed) — no bypass
- `authorize()` helper allows operations when actor is nil (no auth configured)
- Production deployments MUST configure database and auth

### Path Traversal
- `safePath` in artifact service resolves symlinks and validates prefix
- `GIT_LITERAL_PATHSPECS=1` prevents Git pathspec injection
- Scoped commits use `-- path` to prevent accidental broader commits

## Architecture Deviations

### Convergence Evaluation Step
- Convergence strategies are evaluated programmatically (select first, select all)
- The spec describes an "evaluation step" where an actor reviews branch outcomes
- This is deferred — current implementation auto-selects based on strategy

### Branch Isolation
- Git branches are created for isolation
- No runtime enforcement prevents cross-branch artifact reads via API
- Isolation is convention-based, not enforced at the store level
