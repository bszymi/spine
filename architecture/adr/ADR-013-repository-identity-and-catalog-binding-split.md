---
id: ADR-013
type: ADR
title: Repository identity model — governed catalog vs runtime binding
status: Accepted
date: 2026-04-28
decision_makers: bszymi
links:
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: related_to
    target: /architecture/multi-repository-integration.md
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: /governance/artifact-schema.md
---

# ADR-013 — Repository identity model — governed catalog vs runtime binding

---

## Context

INIT-014 introduces multi-repository workspaces: a single Spine
workspace governs one primary Spine repo and N registered code
repos. The `Repository` entity sketched in
[Multi-Repository Integration](/architecture/multi-repository-integration.md)
§2 originally folded everything into one shape — ID, kind, clone
URL, default branch, local path, status, timestamps — and stored
it in a runtime table.

That layout conflates two unrelated concerns:

- **Identity** — the stable, workspace-scoped ID that task
  artifacts reference (`repositories: [payments-service]`). This
  must survive a database rebuild from Git and must not change
  when an operator rotates a token, moves the clone behind a new
  proxy, or marks a repo inactive.
- **Connection** — the clone URL, credentials, local filesystem
  path, and active/inactive flag. These are operator-managed
  runtime concerns that legitimately change over the life of a
  workspace and must not enter Git.

The Constitution (§2 Source of Truth, §8 Disposable Database) is
clear about which side wins: governed identity belongs in Git,
operational state belongs in the runtime store. The original
all-in-one shape would have leaked clone URLs and credentials
into commits if the entity were ever serialized as a governed
artifact, and would have made artifact-to-repository references
unstable across rebuilds.

Two operational realities frame the decision:

- Task artifacts pin the `repositories` field by ID. If the ID
  is reconstructible only from the runtime table, a database
  rebuild from Git can produce dangling references. The ID has
  to live in Git.
- Code repos move. Clone URLs are rewritten when teams migrate
  hosts; tokens rotate; local paths differ between a developer
  laptop and a production runner. None of these changes are
  governance events and none of them should produce a commit.

This ADR commits to a clean split between governed identity and
runtime binding so the two can evolve independently.

---

## Decision

**Two artifacts, one repository.**

Every registered repository has exactly two records:

1. A **catalog entry** in `/.spine/repositories.yaml` — governed,
   committed to the primary Spine repo, the source of truth for
   repository identity within the workspace.
2. A **runtime binding** row in the `repositories` table — not
   governed, stores the clone URL, credentials reference, local
   path, status, and timestamps.

The two are linked by the workspace-scoped repository ID. The
catalog entry is the authority on which IDs exist; the binding
row is the authority on how to reach them.

**Catalog format (`/.spine/repositories.yaml`).** The file is a
list of entries with these fields:

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `id` | yes | string | Workspace-scoped repository ID. `spine` is reserved for the primary repo. |
| `kind` | yes | enum | `spine` or `code`. Exactly one entry has `kind: spine`. |
| `name` | yes | string | Human display name shown in UIs and CLI output. |
| `default_branch` | yes | string | Branch that `spine/run/*` branches are cut from in this repo. |
| `role` | optional | string | Free-form role label (e.g., `service`, `library`, `infra`). |
| `description` | optional | string | One- or two-sentence explanation of what the repo contains. |

**Runtime-only fields are forbidden in the catalog.** The
catalog file MUST NOT contain `url`, `clone_url`, `credentials`,
`token`, `secret_ref`, `local_path`, `path`, `status`, or any
other operational connection field. The catalog parser rejects
unknown fields rather than silently dropping them — adding a
field requires updating this ADR and the catalog schema.

**ID rules.**

- IDs are lowercase alphanumeric with single internal hyphens,
  matching `^[a-z0-9]+(-[a-z0-9]+)*$`. Maximum length 64
  characters. Consecutive hyphens (e.g., `api--gateway`) and
  leading/trailing hyphens are rejected.
- IDs are unique within a workspace.
- `spine` is reserved as the primary repository ID. No other
  entry may use it; the primary entry MUST use it.
- IDs are immutable. Renaming a repository is a deregister +
  register cycle, which orphans any task artifact that still
  references the old ID.

**Catalog presence rules.**

- Single-repo workspaces MAY omit the catalog file entirely. The
  workspace behaves as if a single entry existed with
  `id: spine`, `kind: spine`, `default_branch:` matching the
  Spine repo's configured authoritative branch, and a synthetic
  display name. This preserves backward compatibility for every
  existing v0.x workspace.
- Multi-repo workspaces MUST commit the catalog file. The first
  code repo registration creates it.
- When the file exists, it MUST contain exactly one
  `kind: spine` entry. A catalog with zero or two-or-more
  primary entries fails validation at workspace load.

**Runtime binding shape.** The runtime `repositories` table
holds one row per catalog entry plus the operational fields:

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

The binding row stores no identity fields beyond the ID itself.
`kind`, `name`, `default_branch`, `role`, and `description` come
from the catalog. The runtime store is a binding, not a copy.

**Reconstruction from Git.** The runtime store is disposable
(Constitution §8). On rebuild, the workspace re-reads the
catalog and reconstructs binding rows using the operator-supplied
clone URLs and credentials from runtime configuration. A binding
row whose `repository_id` is not present in the catalog is an
orphan and is dropped during reconstruction with a warning event.

**Validation.** The catalog is validated at workspace load and
on every commit that touches `/.spine/repositories.yaml`:

- Schema: required fields present, no unknown fields, ID format
  matches, exactly one `kind: spine` entry.
- Cross-artifact: every `repositories: [...]` reference in a
  task points at a catalog entry. Dangling references fail run
  start (per [Multi-Repository Integration](/architecture/multi-repository-integration.md) §4.1).
- Idempotence: catalog parsing is deterministic — the same file
  always yields the same in-memory representation.

**Catalog example.**

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

---

## Consequences

### Positive

- Task artifacts that reference repository IDs survive a full
  database rebuild from Git, because the ID lives in the
  catalog.
- Operators can rotate clone URLs, credentials, and local paths
  without producing a governance commit.
- Credentials and tokens never enter Git — the catalog parser
  rejects the fields outright, eliminating a common leak path.
- Multi-repo state is auditable in the same way as every other
  governed artifact: `git log /.spine/repositories.yaml` shows
  exactly when a repo was added, renamed (via deregister +
  register), or marked archived.
- Single-repo workspaces remain catalog-free, so the v0.x
  upgrade is a no-op for installations that never register a
  second repo.

### Negative

- Two records to keep in sync. A binding row with no catalog
  entry, or a catalog entry with no binding, is a real
  operational state and must be detected and surfaced. The
  reconstruction path handles the binding-without-catalog case
  by dropping; the catalog-without-binding case requires the
  operator to register the runtime details.
- Adding a new catalog field is an ADR change, not just a code
  change. This is intentional friction — the catalog is a
  governed schema — but it means proposals like "store the repo
  topic" go through review.
- Operators can no longer infer the clone URL by reading the
  repo. URL discovery moves to the management API and CLI,
  which is a behavior change from the all-in-one entity sketch.

### Neutral

- The catalog filename `/.spine/repositories.yaml` mirrors the
  existing `/.spine/branch-protection.yaml` convention from
  ADR-009, keeping per-workspace operational governance under
  one directory.
- `default_branch` lives in the catalog rather than the binding
  because changing a code repo's default branch is a governance
  decision affecting where `spine/run/*` branches are cut from.
  Operators who disagree with this categorization should propose
  a follow-up ADR rather than moving the field unilaterally.

---

## Alternatives Considered

### All-in-one runtime entity (original sketch)

Keep the original `Repository` shape with ID, URL, default
branch, local path, status, and timestamps in a single runtime
table. **Rejected** because task artifacts that reference
repository IDs would not be reconstructible from Git alone — a
database rebuild would orphan every task that pinned a repo. It
also makes it too easy to leak clone URLs and credentials into a
governed artifact if the entity is ever serialized.

### Catalog-only, no runtime binding

Put everything — including clone URL and credentials — in
`/.spine/repositories.yaml`. **Rejected** because credentials
and tokens cannot enter Git (Security Model §5), local paths
differ between machines, and operational changes (URL rewrite,
credential rotation, marking inactive) would each require a
governance commit. The catalog would churn for non-governance
reasons.

### Catalog stored per-repo as an artifact (one file per repo)

Use `/.spine/repositories/<id>.yaml`, one file per registered
repo. **Rejected** because the workspace-scoped uniqueness and
"exactly one primary" invariants are easier to enforce against a
single list than against N files. The single-file format also
matches the existing `/.spine/branch-protection.yaml`
convention. If the catalog ever needs to scale past hundreds of
repos, splitting can be reconsidered.

### Implicit primary entry (no `spine` row in the catalog)

Treat the primary repo as implicit and only list code repos in
the catalog. **Rejected** because it forks the validation logic
(some IDs come from the catalog, the `spine` ID comes from
elsewhere) and makes the "exactly one primary" invariant
invisible in the file. An explicit primary entry costs five
lines of YAML and removes a class of edge cases.

---

## Links

- Initiative: [INIT-014](/initiatives/INIT-014-multi-repository-workspaces/initiative.md)
- Epic: [EPIC-001](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md)
- Architecture: [Multi-Repository Integration](/architecture/multi-repository-integration.md)
- Architecture: [Git Integration Contract](/architecture/git-integration.md)
- Governance: [Artifact Front Matter Schema](/governance/artifact-schema.md)
- Companion ADRs: [ADR-009](/architecture/adr/ADR-009-branch-protection.md) (per-workspace operational governance pattern), [ADR-010](/architecture/adr/ADR-010-secret-client-abstraction.md) and [ADR-011](/architecture/adr/ADR-011-workspace-resolver-secret-ref-dereference.md) (credentials reference model)
