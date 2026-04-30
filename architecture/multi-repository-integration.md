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

Per [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md), every registered repository has two records: a **governed catalog entry** in `/.spine/repositories.yaml` (the source of truth for identity) and a **runtime binding row** in the `repositories` table (the source of truth for connection details). They are linked by the workspace-scoped repository ID.

### 2.1 Governed Catalog (`/.spine/repositories.yaml`)

The catalog lives in the primary Spine repo and is committed alongside other governed artifacts. It is the authority on which repository IDs exist within the workspace.

```yaml
# /.spine/repositories.yaml
- id: spine
  kind: spine
  name: Payments Platform Spine
  default_branch: main
  description: Governance, product, and architecture artifacts.

- id: payments-service
  kind: code
  name: Payments Service
  default_branch: main
  role: service
  description: Core payment processing API.

- id: api-gateway
  kind: code
  name: API Gateway
  default_branch: main
  role: edge

- id: shared-libs
  kind: code
  name: Shared Libraries
  default_branch: develop
  role: library
```

**Catalog fields:**

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | yes | string | Workspace-scoped ID. `spine` is reserved for the primary repo. |
| `kind` | yes | enum | `spine` or `code`. Exactly one entry has `kind: spine`. |
| `name` | yes | string | Human display name. |
| `default_branch` | yes | string | Branch that `spine/run/*` branches are cut from in this repo. |
| `role` | optional | string | Free-form role label (e.g., `service`, `library`, `infra`). |
| `description` | optional | string | One- or two-sentence explanation. |

**Operational fields are forbidden in the catalog.** The catalog parser rejects entries that contain `url`, `clone_url`, `credentials`, `token`, `secret_ref`, `local_path`, `path`, or `status`. Adding a new field requires an ADR change.

**ID rules:**
- IDs match `^[a-z0-9]+(-[a-z0-9]+)*$` (lowercase alphanumeric with single internal hyphens, max 64 chars; consecutive or leading/trailing hyphens are rejected).
- IDs are unique within a workspace.
- `spine` is reserved as the primary repository ID. The primary entry MUST use it; no other entry may.
- IDs are immutable. Renaming is a deregister + register cycle.

**Catalog presence:**
- Single-repo workspaces MAY omit the file entirely. The workspace behaves as if a single `kind: spine` entry existed with the configured authoritative branch — backward compatible with v0.x.
- Multi-repo workspaces MUST commit the file. The first code repo registration creates it.
- When present, the file MUST contain exactly one `kind: spine` entry. Zero or two-or-more primary entries fail validation at workspace load.

### 2.2 Runtime Binding

Operational connection details are stored in the workspace's runtime database, never in Git. The binding row holds clone URL, credentials reference, local path, and status; it holds no identity fields beyond the ID itself.

```sql
CREATE TABLE repositories (
    repository_id     TEXT        NOT NULL,    -- matches catalog id
    workspace_id      TEXT        NOT NULL,
    clone_url         TEXT        NOT NULL,
    credentials_ref   TEXT        NULL,        -- secret-client ref (ADR-010/011)
    local_path        TEXT        NOT NULL,    -- resolved at runtime
    status            TEXT        NOT NULL DEFAULT 'active',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (workspace_id, repository_id),
    CHECK (status IN ('active', 'inactive'))
);
```

`kind`, `name`, `default_branch`, `role`, and `description` are read from the catalog at workspace load — they are not duplicated in the binding row. The runtime store is a binding, not a copy.

### 2.3 Reconstruction From Git

The runtime store is disposable (Constitution §8). On rebuild:

1. The workspace re-reads `/.spine/repositories.yaml` from the primary Spine repo.
2. For each catalog entry, a binding row is constructed using the operator-supplied clone URL and credentials reference from runtime configuration.
3. A binding row whose `repository_id` is not present in the catalog is an orphan — it is dropped with a warning event and not used during the session.
4. A catalog entry that has no operator-supplied connection details is left without a binding row; subsequent operations against that repository fail with a `repository_unbound` error until an operator registers the runtime details.

This ensures task artifacts that reference repository IDs survive a database rebuild — the IDs come from Git, not from the database.

### 2.4 In-Memory Repository View

For services and the git client pool, the catalog and binding are joined into a single in-memory view:

```
Repository {
    ID              string          // From catalog
    WorkspaceID     string
    Kind            RepositoryKind  // "spine" | "code", from catalog
    Name            string          // From catalog
    DefaultBranch   string          // From catalog
    Role            string          // From catalog (optional)
    Description     string          // From catalog (optional)
    CloneURL        string          // From binding
    CredentialsRef  string          // From binding
    LocalPath       string          // From binding
    Status          string          // From binding ("active" | "inactive")
    CreatedAt       time.Time       // From binding
    UpdatedAt       time.Time       // From binding
}
```

Code that mutates the view writes to the correct side: identity fields go to the catalog (governance commit on the primary Spine repo); connection fields go to the binding (runtime store update, no commit).

**Invariants:**
- Every workspace has exactly one repository with `Kind = spine`. It is created when the workspace is provisioned and cannot be deleted.
- The `spine` repository's ID is always `"spine"`. It cannot be renamed.
- The clone URL is used for initial clone. After cloning, Spine operates on the local copy.

### 2.5 Filesystem Layout

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
    "name": "Payments Service",
    "url": "https://github.com/acme/payments-service.git",
    "default_branch": "main",
    "role": "service",
    "description": "Core payment processing API.",
    "credentials_ref": "secret://payments-deploy-key"
}
```

On registration:
1. Validate repository ID (format and uniqueness against the existing catalog).
2. Append a catalog entry to `/.spine/repositories.yaml` with the identity fields (`id`, `kind: code`, `name`, `default_branch`, optional `role` and `description`). If the file does not exist, create it with the implicit primary entry materialized first.
3. Commit the catalog change to the primary Spine repo as a governance commit (standard commit trailers per [Git Integration Contract](/architecture/git-integration.md) §5).
4. Insert a binding row into the runtime `repositories` table with `clone_url`, `credentials_ref`, the resolved `local_path`, and `status: active`.
5. Clone the repository to `{workspace_base}/repos/{repo_id}/`.
6. Verify the clone succeeded and the catalog's `default_branch` exists.
7. Create the git client and add it to the pool.
8. Return the in-memory repository view.

Failure handling: if any step after the catalog commit fails (binding insert, clone, default-branch verification, or client creation), the registration is rolled back end-to-end:

- The binding row is deleted (or never inserted, if the failure occurred earlier).
- Any partial local clone at `{workspace_base}/repos/{repo_id}/` is removed.
- The catalog commit is reverted on the primary Spine repo with a follow-up commit.

The catalog never advances ahead of a working binding, and the binding never lingers without a matching catalog entry. After a failed registration, the same `repository_id` must be reusable on retry without manual cleanup.

### 6.2 Credential Management

Code repos may require authentication for clone and push operations. Credentials are managed per repository:

- Stored in runtime configuration (not in Git)
- Support the same credential methods as the primary repo (SSH key, PAT, OAuth)
- Each repo can have independent credentials (different GitHub orgs, different platforms)

### 6.3 Deregistration

Deregistering a code repo:
1. Verify no active runs reference this repo.
2. Remove the catalog entry from `/.spine/repositories.yaml` and commit the change to the primary Spine repo. The committed catalog is what task validation reads against on subsequent run starts, and the Git history of this file is the durable audit record for the deregistration.
3. Delete the binding row from the runtime `repositories` table. Retaining the row would create an orphan under the reconstruction rule in §2.3 (it would be dropped on the next workspace load anyway) and would keep the `(workspace_id, repository_id)` primary key occupied, blocking re-registration of the same ID.
4. Remove the git client from the pool.
5. The local clone at `{workspace_base}/repos/{repo_id}/` is retained on disk (operator can clean up manually). It is no longer registered, so Spine will not operate on it.

Soft deletion — flipping the binding row's `status` to `inactive` while leaving the catalog entry in place — is reserved for operator maintenance windows where a code repo must be temporarily taken out of rotation without losing its identity. The public deregistration API always removes the catalog entry and the binding row together. Hard deletion of the local clone directory is an operator action, not available via API.

---

## 7. Impact on Existing Architecture

### 7.1 Artifact Service

- Governance artifacts (initiatives, epics, tasks, ADRs, workflows) continue to be read from and written to the primary Spine repo only
- The Artifact Service gains a `WithRepository(repoID string)` context for code repo operations during execution
- Artifact path resolution remains relative to the repo root — but now "which repo root" is explicit

### 7.2 Projection Service

- Continues to project from the primary Spine repo only
- Code repos are not projected — Spine does not index source code
- The `repositories` table is a runtime binding, not a projection. The Projection Service does, however, read `/.spine/repositories.yaml` from the primary Spine repo and surface the catalog (identity fields only) so cross-artifact validation can resolve `repositories: [...]` references in tasks.

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
- [Artifact Front Matter Schema](/governance/artifact-schema.md) — catalog file (§5.8) and validation policy file (§5.9) as governed artifacts
- [Validation Policy Format](/architecture/validation-policy.md) — schema for `/governance/validation-policies/<name>.yaml`, the deterministic enforcement recipe an ADR links to
- [Execution Evidence Schema](/architecture/execution-evidence.md) — `RequiredChecks` / `AdvisoryChecks` lists that policy `severity` selects between
- [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md) — identity model and catalog/binding split
- [ADR-014](/architecture/adr/ADR-014-validation-policy-as-governed-artifact.md) — validation policy as a governed artifact type
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
