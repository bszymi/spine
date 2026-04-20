---
type: Architecture
title: Git Integration Contract
status: Living Document
version: "0.1"
---

# Git Integration Contract

---

## 1. Purpose

This document defines how Spine interacts with Git repositories at the operational level — authentication, change detection, branch strategy during execution, commit format, and conflict handling.

Git is Spine's authoritative source of truth (Constitution §2). The [Artifact Service](/architecture/components.md) §4.2 and [Projection Service](/architecture/components.md) §4.4 are the primary components that interact with Git. This document specifies the operational contract for those interactions.

---

## 2. Repository Model

### 2.1 Repository Scope

Each workspace operates against a single Git repository. A `.spine.yaml` file at the repository root configures the **artifacts directory** — the subdirectory where all Spine artifacts live. When absent, artifacts are at the repo root (backward compatible). See [Repository Structure §1.1](/governance/repository-structure.md).

At startup (or on first request for a workspace in shared mode), Spine reads `.spine.yaml` and applies the `artifacts_dir` setting to all path resolution, file discovery, and git operations (commits, pathspecs).

The repository contains:

- All governed artifacts (initiatives, epics, tasks, ADRs, governance, architecture, product documents)
- Workflow definitions (`workflows/*.yaml`)
- Runtime configuration is **not** stored in the repository (per [Security Model](/architecture/security-model.md) §5)

#### Workspace-Scoped Repositories

In single mode (v0.x default), the repository path is set via the `SPINE_REPO_PATH` environment variable — one repository per Spine instance. This is unchanged from the original v0.x model.

In shared mode, each workspace's repository path is resolved from the workspace registry at the request boundary. Each workspace has its own independent Git repository, working directory, and credentials. All Git integration rules defined in this document — branch strategy, commit model, authoritative branch, merge behavior — apply per workspace.

### 2.2 Authoritative Branch

The authoritative branch (typically `main`) represents the current governed state of all artifacts.

- All governance queries resolve against the authoritative branch
- Projection Service syncs from the authoritative branch
- Workflow binding resolution reads workflow definitions from the authoritative branch

### 2.3 Hosting Requirements

Spine requires a Git hosting platform that supports:

- Branch protection rules (required for authoritative branch)
- Webhooks or API-based event notifications (for change detection)
- API access for programmatic operations (for Artifact Service)
- Merge operations (for incorporating task branch work)


Spine is the authority for merge decisions. Git hosting platforms provide storage, APIs, and optional collaboration features, but they are not the governance authority for accepting or merging governed work.

---

## 3. Authentication

### 3.1 Artifact Service Authentication

The Artifact Service authenticates to Git using a service-level credential:

| Method | Use Case | Configuration |
|--------|----------|---------------|
| SSH key | Preferred for server deployments | Deploy key or service account SSH key |
| Personal access token (PAT) | Alternative for hosted platforms | Token with repo read/write scope |
| OAuth app token | For platform integrations | OAuth app with repository access |

The credential is stored in runtime configuration (encrypted at rest, per [Security Model](/architecture/security-model.md) §5.2). It is never stored in Git.

### 3.2 Projection Service Authentication

The Projection Service requires read-only access to Git:

- May share the Artifact Service's credential with read-only scope
- Or use a separate read-only deploy key
- Does not require write access

### 3.3 Actor Access

Actors do not interact with Git directly. All Git operations are mediated through the Artifact Service. Actors authenticate to Spine (via the Access Gateway), and Spine authenticates to Git on their behalf.

The commit author is set to the actor's identity (see §5.2), but the Git authentication credential belongs to the Artifact Service, not the actor.

### 3.4 Authorization Model

Authentication to Git is performed by the Artifact Service, but authorization is enforced at the Spine layer.

- Actors are authorized based on workflow roles and permissions
- The Artifact Service validates whether an actor is allowed to perform a given operation
- Git itself is not used as the source of authorization

This ensures consistent enforcement of governance rules across human and AI actors.

---

## 4. Change Detection

### 4.1 Detection Mechanisms

The Projection Service must detect when the repository changes so it can update projections. Two mechanisms are supported:

| Mechanism | Latency | Reliability | Recommended For |
|-----------|---------|-------------|-----------------|
| Webhooks | Low (near real-time) | Depends on hosting platform delivery guarantees | Production deployments |
| Polling | Configurable (seconds to minutes) | High (no external dependency) | Development, fallback |

### 4.2 Webhook Integration

When configured, the Git hosting platform sends webhook events to Spine on:

- Push to the authoritative branch
- Pull request merged to the authoritative branch
- Branch created or deleted (for task branch tracking)

The webhook endpoint is exposed by the Access Gateway and forwarded to the Projection Service.

**Webhook payload processing:**

1. Validate webhook signature (platform-specific)
2. Extract the commit SHA and changed file paths
3. Trigger incremental projection sync for affected artifacts
4. Emit domain events for detected artifact changes

Note: Pull request events may be received for visibility, but they are not the source of truth for merge decisions. Spine determines merge eligibility via workflow completion and validation.

### 4.3 Polling Fallback

When webhooks are unavailable or as a reliability fallback:

1. Projection Service periodically fetches the authoritative branch HEAD
2. Compares against `projection.sync_state.last_synced_commit`
3. If different, runs incremental sync from the last synced commit to HEAD
4. Updates `projection.sync_state`

Polling interval is operator-configured (default: 30 seconds for v0.x).

### 4.4 Combined Strategy

For production deployments, both mechanisms should be active:

- Webhooks provide low-latency detection
- Polling provides a safety net for missed webhooks
- The Projection Service deduplicates syncs using `source_commit` tracking

---

## 5. Commit Format

### 5.1 Commit Message Structure

All commits produced by the Artifact Service follow a structured format:

```
<summary line>

<optional body>

Trace-ID: <uuid>
Actor-ID: <actor_id>
Run-ID: <run_id or "none">
Operation: <operation_name>
```

**Fields:**

| Field | Required | Description |
|-------|----------|-------------|
| Summary line | Yes | Concise description of the change (max 72 characters) |
| Body | No | Additional context when needed |
| `Trace-ID` | Yes | Observability correlation ID (per [Observability](/architecture/observability.md) §3.3) |
| `Actor-ID` | Yes | Actor who initiated the operation |
| `Run-ID` | Conditional | Run ID if the commit is part of workflow execution; `none` for direct operations |
| `Operation` | Yes | The operation that produced the commit (e.g., `artifact.create`, `step.submit`, `task.accept`) |

### 5.2 Commit Author

The commit author is set to the actor's identity:

```
Author: <actor_name> <actor_id@spine.local>
```

For system-generated commits (e.g., automated transitions):

```
Author: Spine System <system@spine.local>
```

The email format `<id>@spine.local` is a convention — it does not represent a real email address. It provides a parseable actor identifier in the Git commit metadata.

### 5.3 Atomic Commits

Each governed operation produces exactly one Git commit:

- An `artifact.create` operation produces one commit containing the new artifact file
- A `step.submit` with `commit.status` effect produces one commit with the status change
- A convergence result produces one commit with the selected/merged artifacts and convergence record

Multiple file changes within a single operation are grouped into a single commit. Operations must not produce partial commits — if any file in the operation fails validation, the entire commit is aborted.

### 5.5 Operational vs Governed Commits

Spine distinguishes between two categories of commits:

| Commit Type | Location | Meaning |
|-------------|----------|---------|
| Operational commit | Task or divergence branch | Represents intermediate execution state within a Run |
| Governed commit | Authoritative branch | Represents a durable governance decision (artifact state change) |

Operational commits:
- Occur during workflow execution on task or divergence branches
- May include intermediate changes, drafts, or partial outputs
- Are not considered governed system state

Governed commits:
- Occur only when merging into the authoritative branch
- Represent accepted outcomes, terminal task states, or approved artifact changes
- Are the only commits that define the durable system state

This distinction ensures alignment with the Task Lifecycle model, where only terminal governance outcomes modify the authoritative branch.

### 5.4 Commit Examples

**Task status change:**
```
Update TASK-001 status to In Progress

Trace-ID: 550e8400-e29b-41d4-a716-446655440000
Actor-ID: dev-alice
Run-ID: run-abc123
Operation: step.submit
```

**Artifact creation:**
```
Create ADR-005 technology selection

Trace-ID: 6ba7b810-9dad-11d1-80b4-00c04fd430c8
Actor-ID: architect-bob
Run-ID: none
Operation: artifact.create
```

### 5.6 Idempotency

All operations that produce commits must be idempotent.

- The `Trace-ID` uniquely identifies an operation
- If an operation is retried, the system must detect existing commits with the same Trace-ID and avoid duplication
- Commit creation must be safe under retry conditions

This is required for reliability in distributed and asynchronous execution environments.

---

## 6. Branch Strategy During Execution

### 6.1 Task Branches

When a Run produces work that requires multiple commits or intermediate states before a durable outcome, the Artifact Service creates a task branch:

```
spine/<run-id>/<task-slug>
```

Example:
```
spine/run-abc123/implement-auth-service
```

**Task branch lifecycle:**

1. Created by the Artifact Service when a Run's first work step begins
2. Work commits are made to the task branch
3. When a durable outcome is committed (e.g., task accepted), the branch is merged to the authoritative branch
4. The task branch is deleted after successful merge

**Important:**
Task branches represent *proposed state*, not governed state.

- Commits on task branches are operational and may be revised or discarded
- Only the merge into the authoritative branch establishes governed truth
- Systems reading governed state must ignore task branches unless explicitly operating in execution context

### 6.1.1 Planning Run Branches

Planning runs (per [ADR-006](/architecture/adr/ADR-006-planning-runs.md)) use the same branch naming convention as standard runs (see §6.4) but have distinct branch semantics:

**Planning run branch lifecycle:**

1. Branch is created when `StartPlanningRun()` initializes the run
2. The initial artifact is written to the branch as the first commit — this is the artifact being created
3. During the draft step, child artifacts may be created on the same branch (e.g., epics and tasks under a new initiative)
4. The validate step runs cross-artifact validation against the branch content
5. On review approval, the branch is merged to the authoritative branch via the standard `MergeRunBranch()` path
6. The branch is deleted after successful merge

**Constraints:**

- Planning run branches may only contain new artifact files — no modifications to files that exist on the authoritative branch
- Only artifact types declared at run creation may be written to the branch
- Writes are restricted to allowed repository root paths for the declared artifact types

**Distinction from task branches:**

Standard task branches contain modifications to an existing governed artifact. Planning run branches contain the creation of one or more new artifacts that do not yet exist on `main`. Once merged, the created artifacts become fully governed and all subsequent work occurs through standard runs on task branches.

### 6.2 Divergence Branches

During divergence, each branch gets its own Git branch (per [Divergence and Convergence](/architecture/divergence-and-convergence.md) §3.4):

```
spine/<run-id>/<divergence-id>/<branch-id>
```

Example:
```
spine/run-abc123/explore-designs/branch-a
spine/run-abc123/explore-designs/branch-b
```

**Divergence branch lifecycle:**

1. Created when the divergence point is triggered
2. Branch-specific work commits are isolated to each divergence branch
3. After convergence, the selected branch is merged to the task branch (or authoritative branch)
4. Non-selected branches are preserved (never deleted, per Constitution §6)

### 6.3 Merge Strategy

Merges into the authoritative branch are governed operations and must only occur after workflow completion.

- The Artifact Service performs merges — not human actors directly
- A merge represents a governance decision, not a technical operation

**Authority model:**

- Spine owns all merges into the authoritative branch
- Only the Artifact Service is allowed to perform merges for Spine-managed branches (`spine/*`)
- Manual merges by humans directly in the Git hosting platform for Spine-managed branches are prohibited

**Pull requests (optional):**

- Pull requests may be created for visibility or collaboration
- Pull requests do not determine merge eligibility
- Approval in Git hosting platforms does not override Spine governance

**Merge conditions:**

- Workflow execution for the Run has reached a terminal or accepted outcome
- All required validation steps have passed
- No policy violations are present
 - All required Spine validations and tests have passed within the workflow execution

Spine uses **fast-forward merges** when possible to preserve linear history.

When fast-forward is not possible:

- The Artifact Service creates a merge commit
- The merge commit includes standard commit trailers (Trace-ID, Actor-ID, Run-ID, Operation)

**Conflict handling:**

- If a merge results in conflicts, the merge is aborted
- The step execution is marked as failed with classification `git_conflict`
- An operator must resolve the conflict before the workflow can proceed

### 6.4 Branch Naming Convention

All Spine-managed branches use the `spine/` prefix to distinguish them from human-managed branches. Branch names are human-readable, incorporating the artifact identity and a slug derived from the artifact path.

| Branch Type | Pattern | Example |
|-------------|---------|---------|
| Standard run | `spine/run/<artifact-id>-<slug>-<run-hex>` | `spine/run/task-003-git-push-0a5d0f6d` |
| Planning run | `spine/plan/<artifact-id>-<slug>-<run-hex>` | `spine/plan/init-001-initiative-abcd1234` |
| Divergence branch | `spine/<run-id>/<divergence-id>/<branch-id>` | `spine/run-abc123/explore/branch-a` |

**Slug rules:**
- Derived from the artifact filename (without extension)
- Lowercased, non-alphanumeric characters replaced with hyphens
- Truncated to 60 characters maximum (excluding prefix)
- Run ID hex suffix (8 characters) always appended for uniqueness

Human-managed branches (per [Naming Conventions](/governance/naming-conventions.md) §6) use the `INIT-XXX/EPIC-XXX/TASK-XXX-<slug>` pattern. These are not managed by the Artifact Service.

Spine-managed branches (`spine/*`) are governed exclusively by the Artifact Service. Direct manipulation of user-facing authoritative branches (e.g. `main`, `staging`, `release/*`) is **enforced** against direct writes and deletions by the branch-protection policy described in [ADR-009](/architecture/adr/ADR-009-branch-protection.md). Operators authoring `/.spine/branch-protection.yaml` are expected to scope rules to user-facing branches; the config parser does not currently reject patterns that would match `spine/*` (e.g. `spine/run/*`, `*/*/*`), so a pattern like that would accidentally gate Spine's own Run branches and block routine Orchestrator operations. Treat `spine/*` as a reserved namespace and do not target it from rules.

### 6.5 Git Push (`git-receive-pack`)

Push is a first-class, enforced surface in v1. It is off by default — `SPINE_GIT_RECEIVE_PACK_ENABLED=true` (accepted: `1`/`true`/`yes`/`on`) turns it on. An upgrade that does not set the flag keeps the prior read-only behaviour.

**Enforcement sequence** on each `POST /git-receive-pack`:

1. The gateway requires a bearer token on every push. The trusted-CIDR bypass used for clone/fetch does not apply to receive-pack — every push carries an actor identity so branch-protection decisions and audit events can pin the caller.
2. The pre-receive gate in `internal/githttp` reads the pkt-line command section out of the request body, extracts each `(old_sha, new_sha, ref)` triple, and classifies it (`delete` if `new_sha == 00…0`, else `direct_write` — every push is a direct write from the policy's perspective; governed merges happen inside Spine, not over the wire).
3. `branchprotect.Policy.Evaluate` is called per ref update against the target **workspace's** rules (each workspace has its own branch-protection table in shared mode). A `Deny` on any ref rejects the **entire** push — pre-receive semantics are all-or-nothing, so no ref advances if any ref would have been blocked. `spine/*` refs bypass the policy by design (§6.4) but still flow through CGI so audit remains consistent with the API path.
4. Rejections render as a `x-git-receive-pack-result` body: a side-band 2 `remote: branch-protection: <rule> denies <branch>` line plus a side-band 1 `unpack ok` + `ng <ref> pre-receive hook declined` per ref. Frames are chunked to stay under the pkt-line length limit so even multi-thousand-ref pushes parse cleanly.
5. On allow, the buffered command bytes are replayed verbatim in front of the still-pending PACK stream; `git-http-backend` sees the original request unchanged. Spine does not rewrite client-produced commits.

**Operator override.** The Git-path equivalent of `write_context.override` is `git push -o spine.override=true`. Operators (role `operator+`) bypass a matching rule for that single push; contributors setting the same option are rejected with a distinct "override not authorised" reason, not silently accepted. Every honored override emits exactly one `branch_protection.override` governance event per overridden ref update — not per push — with the ADR-009 §4 payload plus a `pre_receive_ref: {old_sha, new_sha, ref}` block. No commit trailer is added on this path; the event is the sole audit record for a Git push override. Unused overrides (flag set on a push that did not need it) emit nothing.

**`receive.advertisePushOptions=true`** is set on every workspace repo alongside `http.receivepack=true` so `git push -o` actually reaches the gate — without it the client silently drops the option.

---

## 7. Conflict Handling

### 7.1 Prevention

Conflicts are minimized by:

- Each Run operates on its own task branch, isolating concurrent work
- Divergence branches are isolated from each other
- The Artifact Service uses atomic commits — partial writes don't occur
- Fast-forward merges are preferred

- Centralized merge authority (Artifact Service) ensures consistent conflict detection and prevents uncontrolled merges

### 7.2 Detection

Conflicts are detected at merge time:

1. The Artifact Service attempts to merge the task branch to the authoritative branch
2. If Git reports a conflict, the merge is aborted
3. The step execution is marked as failed with classification `git_conflict`
4. An error event is emitted with conflict details

### 7.3 Resolution

Git conflicts are treated as permanent errors in v0.x (per [Error Handling](/architecture/error-handling-and-recovery.md) §5.2):

- Automatic resolution is not attempted
- An operator must manually resolve the conflict
- After resolution, the Run may be restarted or the step retried

Future versions may introduce assisted resolution strategies, such as:

- Rebase-and-retry mechanisms
- Workflow-defined merge resolution steps
- Automated conflict classification and guidance

---

## 8. Repository Discovery

### 8.1 Artifact Discovery

The Artifact Service discovers artifacts by scanning the repository tree:

- Files matching `*.md` with valid YAML front matter are candidate artifacts
- The `type` field in front matter determines the artifact type
- Files in `/workflows/` matching `*.yaml` are workflow definitions
- Files without recognized front matter are ignored

### 8.2 Change Discovery

When processing a new commit (incremental sync):

1. Diff the commit against its parent
2. For each changed file, check if it is an artifact (has valid front matter)
3. For changed artifacts, update the projection
4. For deleted files, remove the projection
5. For new files, create the projection

### 8.3 Discovery During Workflow Execution

During workflow execution, the system may need to discover artifacts created on a task branch that haven't been merged yet (per [Access Surface](/architecture/access-surface.md) §3.2.1):

- The Workflow Engine reads from the task branch, not the authoritative branch
- Artifacts on the task branch are treated as proposed until the branch is merged
- Validation runs against the task branch content

---

## 9. Constitutional Alignment

| Principle | How Git Integration Supports It |
|-----------|--------------------------------|
| Source of Truth (§2) | All operations produce Git commits; the authoritative branch is the single source of governed state |
| Explicit Intent (§3) | Structured commit messages with trailers make every change traceable |
| Reproducibility (§7) | Commit format includes Trace-ID for correlation; atomic commits ensure no partial state |
| Disposable Database (§8) | Projection Service rebuilds from Git; change detection enables incremental sync |
| Controlled Divergence (§6) | Divergence branches are preserved; non-selected branches are never deleted |

---

## 10. Cross-References

- [System Components](/architecture/components.md) §4.2 — Artifact Service, §4.4 — Projection Service
- [Security Model](/architecture/security-model.md) §5 — Credential management, §7 — Git security
- [Observability](/architecture/observability.md) §3.3 — Trace-ID commit trailer
- [Error Handling](/architecture/error-handling-and-recovery.md) §5 — Git operation failures
- [Divergence and Convergence](/architecture/divergence-and-convergence.md) §3.4 — Branch isolation
- [Naming Conventions](/governance/naming-conventions.md) §6 — Human branch naming
- [Data Model](/architecture/data-model.md) §2.1 — Git layer, §4 — Projection rebuild
- [Runtime Store Schema](/architecture/runtime-schema.md) §3.4 — Projection sync state

---

## 11. Evolution Policy

This Git integration contract is expected to evolve as the system is implemented and operational experience is gained.

Areas expected to require refinement:

- Multi-repository support (artifacts spanning multiple repos within a single workspace)
- Git LFS integration for large binary artifacts
- Automated conflict resolution strategies
- Monorepo vs polyrepo guidance
- CI/CD pipeline integration patterns
- Git hosting platform-specific adapters (GitHub API, GitLab API)

Changes that alter the commit format, branch strategy, or merge behavior should be captured as ADRs.
