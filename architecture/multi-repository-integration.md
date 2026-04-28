---
type: Architecture
title: Multi-Repository Integration
status: Living Document
version: "0.1"
---

# Multi-Repository Integration

---

## 1. Purpose

This document extends the [Git Integration Contract](/architecture/git-integration.md) to define how Spine operates across multiple Git repositories within a single workspace.

The current contract assumes one repository per workspace. This document specifies how the Repository domain entity, multi-repo git client pool, branch lifecycle, merge coordination, and git HTTP serving work when a workspace manages N code repositories alongside its primary Spine repository.

---

## 2. Repository Domain Model

### 2.1 Repository Entity

```
Repository {
    ID              string          // Unique within workspace (e.g., "payments-service")
    WorkspaceID     string          // Owning workspace
    Kind            RepositoryKind  // "spine" | "code"
    URL             string          // Clone URL (HTTPS or SSH)
    DefaultBranch   string          // "main", "master", "develop", etc.
    LocalPath       string          // Resolved filesystem path (runtime, not persisted)
    Status          string          // "active" | "inactive"
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

**Invariants:**
- Every workspace has exactly one repository with `Kind = spine`. It is created when the workspace is provisioned and cannot be deleted.
- Repository IDs are unique within a workspace. IDs must be lowercase alphanumeric with hyphens (e.g., `payments-service`, `api-gateway`).
- The `spine` repository's ID is always `"spine"`. It cannot be renamed.
- The URL field is used for initial clone. After cloning, Spine operates on the local copy.

### 2.2 Storage

Repository metadata is stored in the workspace's runtime database (not in Git — it is operational config, not governed artifact state).

```sql
CREATE TABLE repositories (
    repository_id   TEXT        NOT NULL,
    workspace_id    TEXT        NOT NULL,
    kind            TEXT        NOT NULL DEFAULT 'code',
    url             TEXT        NOT NULL,
    default_branch  TEXT        NOT NULL DEFAULT 'main',
    status          TEXT        NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (workspace_id, repository_id),
    CHECK (kind IN ('spine', 'code')),
    CHECK (status IN ('active', 'inactive'))
);
```

### 2.3 Filesystem Layout

Each workspace's repositories are stored under the workspace's base directory:

```
/var/spine/workspaces/{workspace_id}/
    repos/
        spine/          # Primary repo (always present)
        payments-service/
        api-gateway/
        shared-libs/
```

The `spine` directory is the existing `RepoPath`. Code repos are cloned into sibling directories when registered.

---

## 3. Git Client Pool

### 3.1 Current Model

Today, each workspace gets one `git.CLIClient`:

```go
gitClient := git.NewCLIClient(cfg.RepoPath, gitOpts...)
```

This client is passed to all services (Artifact, Divergence, Engine, Projection).

### 3.2 Multi-Repo Model

Replace the single client with a `GitClientPool` that resolves clients by repository ID:

```go
type GitClientPool struct {
    clients map[string]git.GitClient  // repo_id -> client
    mu      sync.RWMutex
}

func (p *GitClientPool) Client(repoID string) (git.GitClient, error)
func (p *GitClientPool) PrimaryClient() git.GitClient
```

**Construction:**
1. On workspace initialization, create a client for the `spine` repo (always)
2. For each active code repository, clone (if not already local) and create a client
3. Lazy initialization: code repo clients can be created on first access

**Backward compatibility:** Services that currently accept `git.GitClient` continue to work. `PrimaryClient()` returns the spine repo client, which is the default when no repo context is specified.

### 3.3 Service Wiring

| Service | Current | Multi-Repo |
|---------|---------|------------|
| Artifact Service | Single git client | Primary client for governance artifacts; pool for code repo operations |
| Projection Service | Single git client | Primary client only (projections are derived from the Spine repo) |
| Divergence Service | Single git client | Routed via pool based on run's repo context |
| Engine Orchestrator | Single git client (GitOperator) | Pool-aware GitOperator that routes by repo ID |

---

## 4. Branch Lifecycle Across Repos

### 4.1 Task Repository Binding

Tasks declare which repositories they affect via a `repositories` field in their frontmatter:

```yaml
---
id: TASK-042
title: "Add rate limiting"
repositories:
  - payments-service
  - api-gateway
---
```

**Resolution rules:**
- If `repositories` is omitted or empty, the task affects only the primary Spine repo (backward compatible)
- If `repositories` contains entries, the task affects those code repos **and** the primary Spine repo (the Spine repo always participates for governance tracking)
- Invalid repository IDs (not registered in the workspace) cause validation failure at run start

### 4.2 Run Branch Creation

When a run starts for a task with multiple repos:

```
For each repo in [spine, ...task.repositories]:
    git.CreateBranch(ctx, run.BranchName, repo.DefaultBranch)
```

The same branch name (`spine/run/{artifact-id}-{slug}-{hex}`) is used in all repos for traceability.

**Failure handling:**
- If branch creation fails in any repo, branches already created in other repos are cleaned up
- The run fails to start with error details indicating which repo failed

### 4.3 Step Execution Routing

Each step in a run targets a specific repository. The step's repository context is determined by:

1. Explicit `repository` field on the step definition (if supported by the workflow)
2. The task's `repositories` list (if single repo, implicit)
3. Default: the primary Spine repo

The runner receives the repository context and clones accordingly:

```bash
git clone http://spine:8080/git/{workspace_id}/{repo_id} \
    --depth 1 --branch spine/run/task-042-rate-limiting-abc123 /workspace
```

### 4.4 Merge Coordination

On run completion, Spine merges the run branch in each affected repo:

```
outcomes = {}
for each repo in [spine, ...task.repositories]:
    result = git.Merge(ctx, MergeOpts{
        Source:  run.BranchName,
        Target:  repo.DefaultBranch,
        ...
    })
    outcomes[repo.ID] = result
```

**Coordination model: Spine-repo-as-ledger**

- Each repo is merged independently. There is no distributed transaction.
- If repo A merges successfully but repo B has a conflict, repo A's merge stands.
- The primary Spine repo records per-repo outcomes.
- A run is considered fully complete only when all repos have merged successfully.
- A run with partial merges enters a `partial_merge` state — the merged repos are done, the failed repos need manual intervention.

**Merge order:**
1. Code repositories are merged first (the actual implementation)
2. The primary Spine repo is merged last (recording the governance outcome)

This ordering ensures the Spine repo accurately records whether code merges succeeded.

### 4.5 Branch Cleanup

After successful merge in a repo, the run branch in that repo is deleted. If a repo's merge failed, its branch is preserved for manual resolution.

---

## 5. Git HTTP Endpoint Extension

The git HTTP serve endpoint (INIT-009, TASK-006) is extended to support repo routing:

### 5.1 URL Structure

```
/git/{workspace_id}/{repo_id}/info/refs?service=git-upload-pack
/git/{workspace_id}/{repo_id}/git-upload-pack
```

**Fallback for primary repo:**
```
/git/{workspace_id}/info/refs    -->  resolves to workspace's spine repo
```

### 5.2 Resolution

1. Extract `workspace_id` from URL
2. Resolve workspace via workspace registry
3. Extract `repo_id` from URL (default: `"spine"`)
4. Look up repository in workspace's repository registry
5. Resolve local filesystem path
6. Set `GIT_PROJECT_ROOT` to that path
7. Proxy to `git http-backend`

### 5.3 Security

All security requirements from TASK-006 apply per-repo:
- `GIT_PROJECT_ROOT` is resolved from the registry, never from the URL path
- Each repo's path is validated against the workspace's known repos — no path traversal
- Inactive repos return 404
- Concurrent clone limits apply globally across all repos in the workspace

---

## 6. Repository Provisioning

### 6.1 Registration Flow

```
POST /api/v1/repositories
{
    "repository_id": "payments-service",
    "url": "https://github.com/acme/payments-service.git",
    "default_branch": "main"
}
```

On registration:
1. Validate repository ID (format, uniqueness within workspace)
2. Insert metadata into `repositories` table
3. Clone repository to `{workspace_base}/repos/{repo_id}/`
4. Verify clone succeeded and default branch exists
5. Create git client and add to pool
6. Return repository metadata

### 6.2 Credential Management

Code repos may require authentication for clone and push operations. Credentials are managed per repository:

- Stored in runtime configuration (not in Git)
- Support the same credential methods as the primary repo (SSH key, PAT, OAuth)
- Each repo can have independent credentials (different GitHub orgs, different platforms)

### 6.3 Deregistration

Deregistering a code repo:
1. Verify no active runs reference this repo
2. Set status to `inactive` (soft delete — preserves history)
3. Remove git client from pool
4. Local clone is retained for audit (operator can clean up manually)

Hard deletion is an operator action, not available via API.

---

## 7. Impact on Existing Architecture

### 7.1 Artifact Service

- Governance artifacts (initiatives, epics, tasks, ADRs, workflows) continue to be read from and written to the primary Spine repo only
- The Artifact Service gains a `WithRepository(repoID string)` context for code repo operations during execution
- Artifact path resolution remains relative to the repo root — but now "which repo root" is explicit

### 7.2 Projection Service

- Continues to project from the primary Spine repo only
- Code repos are not projected — Spine does not index source code
- The `repositories` table is a runtime table, not a projection

### 7.3 Workflow Engine

- `RunParams` gains an optional `Repositories []string` field, derived from the task
- `GitOperator` interface methods gain a `repoID` parameter
- Merge result tracking is extended from a single outcome to a per-repo outcome map

### 7.4 Workspace Provisioning

- `workspace.Provision()` creates the primary repo (unchanged)
- Code repos are registered separately via the API after workspace creation

---

## 8. Constitutional Alignment

| Principle | How Multi-Repo Supports It |
|-----------|---------------------------|
| Source of Truth (§2) | The primary Spine repo remains the single source of governed truth. Code repos are execution targets, not governance authorities. |
| Explicit Intent (§3) | Tasks explicitly declare which repositories they affect. No implicit repo assumptions. |
| Reproducibility (§7) | The same branch name is used across all repos. Per-repo merge outcomes are recorded in the Spine repo. |
| Workspace Isolation (§5.2) | Repository registrations are per-workspace. No cross-workspace repo sharing. |
| Controlled Divergence (§6) | Divergence branches work within individual repos. Cross-repo divergence is not supported in v0.x. |

---

## 9. Limitations (v0.x)

1. **No cross-repo atomic merge** — merges are per-repo. Partial merge states are possible and must be resolved manually.
2. **No cross-repo divergence** — divergence branches are scoped to a single repo. A divergence cannot span multiple repos.
3. **No code repo projection** — Spine does not index or discover artifacts in code repos. Only the primary repo is projected.
4. **No per-repo RBAC** — authorization is at the workspace level. All actors with write access to the workspace can operate on all repos.
5. **Single credential per repo** — no per-branch or per-actor credential delegation within a repo.

---

## 10. Cross-References

- [Git Integration Contract](/architecture/git-integration.md) — base single-repo contract that this document extends
- [System Components](/architecture/components.md) — Artifact Service, Projection Service, Engine
- [Product Definition: Multi-Repository Workspaces](/product/multi-repository-workspaces.md) — product model and use cases
- [Security Model](/architecture/security-model.md) — credential management
- [INIT-014](/initiatives/INIT-014-multi-repository-workspaces/initiative.md) — parent initiative
- [INIT-009 TASK-006](/initiatives/INIT-009-workspace-runtime/epics/EPIC-001-core-runtime/tasks/TASK-006-git-http-serve-endpoint.md) — git HTTP serve endpoint (extended for repo routing)

---

## 11. Evolution Policy

This document is expected to evolve as multi-repo support is implemented and operational experience is gained.

Areas expected to require refinement:

- Cross-repo atomic merge strategies (two-phase commit, saga pattern)
- Cross-repo divergence support
- Code repo artifact discovery (e.g., detecting README or config changes)
- Per-repo access control and credential delegation
- Repository grouping (e.g., "frontend repos", "backend repos") for task templates

Changes that alter the repository model, merge coordination, or branch lifecycle should be captured as ADRs.
