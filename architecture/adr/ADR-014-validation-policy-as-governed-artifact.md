---
id: ADR-014
type: ADR
title: Validation policy as a governed artifact type
status: Accepted
date: 2026-04-30
decision_makers: bszymi
links:
  - type: related_to
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md
  - type: related_to
    target: /architecture/validation-policy.md
  - type: related_to
    target: /architecture/execution-evidence.md
  - type: related_to
    target: /governance/artifact-schema.md
  - type: related_to
    target: /architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md
---

# ADR-014 — Validation policy as a governed artifact type

---

## Context

[EPIC-006](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md)
introduces validation policies — deterministic enforcement
recipes that ADRs link to so an architectural decision becomes
verifiable rather than interpreted at runtime. The policy
format itself is defined in
[Validation Policy Format](/architecture/validation-policy.md)
and lives in the
[`internal/domain/validation_policy.go`](/internal/domain/validation_policy.go)
schema, both shipped with TASK-002.

That work left a gap: the validation policy file is not yet a
recognized artifact type in
[`/governance/artifact-schema.md`](/governance/artifact-schema.md).
Spine's "everything committed to the primary repo is a typed
governed artifact" invariant means an unregistered file shape
has no canonical schema for the validation pipeline to enforce,
no documented lifecycle, and no clear guidance for ADR authors
about how to reference one. The same gap was closed for the
repository catalog by ADR-013 + artifact-schema §5.8, and this
ADR follows that pattern.

Three concrete operational realities frame the decision:

- **Validation policies are committed governance artifacts.**
  They are not runtime configuration. The file at
  `/governance/validation-policies/<name>.yaml` is the
  authoritative copy; nothing else can override it. Every
  change is a governance commit.
- **Validation policies are not Markdown front-matter
  artifacts.** They are pure YAML — ADR-013 / artifact-schema
  §5.8 already established this convention for the repository
  catalog. Forcing a `.md` shell with empty body would add
  ceremony for no benefit and conflict with the existing
  parser in `internal/domain/validation_policy.go`.
- **ADRs reference policies by canonical path.** The
  validation policy format requires every policy to declare
  one or more `adr_paths` entries; reverse linkage from the
  ADR side uses the standard `links` list with an existing
  link type. The cross-link is what makes the deterministic
  bridge auditable.

This ADR commits to registering validation policies as a
governed artifact type, defining their lifecycle and ownership,
and pinning the linkage shape between ADRs and policies.

---

## Decision

**Validation policies are a governed artifact type.**

Every validation policy document satisfies all four governed
artifact obligations:

1. Lives at a canonical path under `/governance/`.
2. Is parsed and validated against a documented schema.
3. Has a documented lifecycle (status enum, transition rules).
4. Is registered in `/governance/artifact-schema.md` so the
   workspace knows the file shape exists and how to enforce it.

**Canonical path.** Validation policies live at
`/governance/validation-policies/<name>.yaml`. The directory
sits under `/governance/` because the file is a governed
artifact — same root as `/governance/charter.md`,
`/governance/constitution.md`, and `/governance/artifact-schema.md`
itself. The `<name>` is a short, kebab-case label
(e.g., `api-contract.yaml`, `migration-safety.yaml`) chosen
by the author. The filename is opaque to validation; identity
within Spine is `(canonical document path, policy_id)` per
[Validation Policy Format §3](/architecture/validation-policy.md).

**File shape.** The file is pure YAML — no front matter, no
Markdown body. This matches the precedent set by
[ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md)
for `/.spine/repositories.yaml` and is consistent with how
governed YAML artifacts are handled today. The §2 / §4 schema
rules in `/governance/artifact-schema.md` therefore do not
apply; the schema rules in
[`/architecture/validation-policy.md`](/architecture/validation-policy.md)
do.

**Lifecycle.** A validation policy carries a `status` field
with the following values, defined in
`internal/domain/validation_policy.go::ValidationPolicyStatus`:

- `draft` — authored but not yet enforced. Validation rules
  MAY skip draft policies entirely; reporting surfaces SHOULD
  show them so authors can iterate.
- `active` — fully enforced. The default state for a
  published policy. Blocking checks block; warning checks
  surface advisory evidence.
- `deprecated` — still enforced, but slated for removal.
  Authors plan a successor and operators should migrate away.
- `superseded` — replaced by a newer policy (typically
  referenced via ADR linkage). MAY be skipped during
  enforcement; old evidence still references it for audit.

Transitions are governance commits. `draft` → `active` is the
go-live moment. `deprecated` and `superseded` are terminal
relative to new runs but never block evidence replay.

**Versioning.** Two axes, defined in Validation Policy Format §3:

- Document-level versioning is Git itself. Every commit to
  the file is the authoritative history.
- Each policy has an opaque `version` string for human-
  readable major-version markers. Spine treats it as opaque
  — there is no semver comparison.

When a policy's checks change in a way that would invalidate
prior evidence, the author SHOULD bump `version` AND rename
`policy_id` so old evidence keeps pointing to the old contract.
This mirrors the "two-axis versioning" pattern from EPIC-006's
execution evidence schema.

**Ownership.** Authoring a validation policy follows the
standard governance process. Policy files live in the primary
Spine repo on the authoritative branch; changes require the
same review and commit pattern as any other governance
artifact. A policy SHOULD list the `adr_paths` it enforces in
its `adr_paths` field (already mandatory per the schema), so
the operational owners of those ADRs are also the
de facto owners of the policy.

**ADR ↔ policy linkage.** ADRs MAY declare links to
validation policies using the existing `related_to` link type
from artifact-schema §4.1. The reverse direction (policy →
ADR) is declared in the policy's mandatory `adr_paths` list.

This ADR explicitly **does not** introduce a new typed link
(e.g., `enforces` / `enforced_by`). The current link types
already support the cross-reference unambiguously:

- A new link type would have to round-trip through
  artifact-schema §4.1, every parser and consistency rule,
  and every editor/template — a significant surface change
  for marginal semantic gain.
- `related_to` is symmetric and informational; it already
  carries the auditable cross-reference Spine needs.
- Determinism — the policy itself enforces the ADR — is
  already encoded by the policy's `adr_paths` field, which
  validation can cross-check against the ADR's existence.

If a future task identifies a concrete enforcement need that
`related_to` cannot express (e.g., "the validation pipeline
MUST refuse to start until every Accepted ADR has at least
one `enforces` link to an active policy"), it is a focused
follow-up that introduces the link type with a single ADR.

**Validation rules.** Three rules apply to validation policy
files:

1. **Schema-level** — the parser added in
   `internal/domain/validation_policy.go::ParseValidationPolicyDocument`
   rejects any malformed policy document with the same
   error shape (`domain.ErrInvalidParams`) used by the
   repository catalog parser. Unknown keys at every nesting
   level (top-level, per-policy, selector, per-check) are
   rejected via `yaml.NewDecoder(...).KnownFields(true)`,
   and a stray `---` separator that would split the file
   into multiple YAML documents is also refused. A typo
   like `timeout_second:` never silently degrades into
   "no timeout".
2. **Document-level** — `ValidationPolicyDocument.Validate`
   enforces `policy_id` uniqueness within a single document
   and `check_id` uniqueness across every check in that
   document.
3. **Set-level** — `ValidateAcrossDocuments` walks every
   policy file in the workspace and rejects `check_id`
   collisions across files (already implemented in TASK-002).
   `policy_id` is intentionally NOT enforced as set-unique:
   identity across the workspace is `(canonical document path,
   policy_id)`, so two files MAY reuse a `policy_id` without
   ambiguity. Adding cross-file `policy_id` uniqueness is a
   focused follow-up if a future rule needs flat policy IDs.
4. **Cross-artifact** — the existing LC-004 dangling-link
   rule is extended via a `GovernedFileResolver` Option
   so that ADR `links` whose targets point to a validation
   policy file resolve through the policy registry (when
   wired) rather than the artifact projection. The default
   resolver returns false, preserving today's behavior for
   workspaces without policies.

The fourth rule's production wiring lands with TASK-004 (which
loads policies into the validation service for evidence
rules). Until then, the resolver hook is plumbing — its
contract is documented and unit-tested but not yet consulted
by a non-test caller.

**Evolution policy.** Adding a new field to the validation
policy schema is governed by `/architecture/validation-policy.md`
§5 (canonicalization) and §7 (validation invariants). The
`schema_version` field on the document itself gates major
on-disk changes; additive minor changes that do not break
existing readers do not require a bump. This matches the
additive-extension policy applied in TASK-002 (where
`AdvisoryChecks` was added to execution evidence without a
version bump).

**Existing artifacts unaffected.** This ADR introduces a new
artifact type. It does not modify the schema, lifecycle, or
validation rules of any existing type. Workspaces with no
validation policy files behave exactly as they did before
EPIC-006; the validation service treats the absence of a
`/governance/validation-policies/` directory as a no-op.

---

## Consequences

### Positive

- Validation policies are auditable on the same terms as
  every other governed artifact: `git log` shows when they
  were drafted, activated, deprecated, or superseded, and
  the validation pipeline rejects malformed files with a
  typed error.
- ADR ↔ policy cross-references are visible to dangling-link
  detection (LC-004) once the policy registry is wired in
  TASK-004. Until then, the resolver hook means the wiring
  is a one-line change rather than a structural rewrite of
  the validation engine.
- The pure-YAML / no-front-matter shape keeps validation
  policy files small, diff-friendly, and aligned with the
  pattern already established by `/.spine/repositories.yaml`.
- Lifecycle states (`draft`/`active`/`deprecated`/
  `superseded`) make it safe to roll out a new check
  without it blocking on day one.

### Negative

- Adding a new validation policy file requires a governance
  commit, which is heavier than a runtime configuration
  toggle. This is intentional — policies define which
  ADRs are enforceable — but it means experiments with
  candidate policies should use `status: draft` until the
  check stabilizes.
- The `enforces` / `enforced_by` link types are not in this
  ADR, so the ADR-side reverse link uses `related_to`. A
  validation rule that checks "every Accepted ADR has at
  least one policy linked back to it" is not yet expressible
  as a typed-link rule and would have to read `adr_paths`
  out of each policy.

### Neutral

- The `validation-policies/` subdirectory under `/governance/`
  follows the same root-by-purpose convention as
  `/governance/charter.md` and the policy authoring tools.
  Keeping policies under `/governance/` (rather than under
  `/architecture/`) reflects that they enforce governance
  invariants, not architectural design choices.
- The default `GovernedFileResolver` returning `false`
  preserves today's LC-004 behavior. Workspaces with no
  policies see no behavior change; workspaces that adopt
  policies get full dangling-link enforcement once TASK-004
  wires the registry.

---

## Alternatives Considered

### Frontmatter-wrapped validation policy

Use the standard `.md` + front matter shape (per
artifact-schema §2). **Rejected** because the validation
policy is exclusively structured data — there is no
narrative body to host. Forcing a Markdown shell would
require duplicating policy fields between front matter and
the body, or treating one as canonical and the other as
documentation, and would conflict with the existing parser
in `internal/domain/validation_policy.go`. The pure-YAML
precedent set by `/.spine/repositories.yaml` (ADR-013) is
the correct fit.

### Multiple policies per file via `policy_id`-named subfiles

Split each policy into `<name>/<policy_id>.yaml` so each
policy has its own file. **Rejected** because document-wide
uniqueness checks (`check_id` across policies in one
document) and `ValidateAcrossDocuments` set-wide checks
already work over the existing list-of-policies shape, and
splitting would either require a new directory-walking
parser or lose the document-wide invariants. The current
shape — one file MAY hold one or more policies — is the
right granularity, matching the pattern in
[Validation Policy Format §3](/architecture/validation-policy.md).

### Introduce `enforces` / `enforced_by` link types now

Register a new typed link in artifact-schema §4.1 so the
ADR → policy direction is explicit. **Rejected for now**
because it expands the link type taxonomy ahead of any
concrete rule that needs it. Today, `related_to` carries
the cross-reference Spine actually uses, and the policy's
`adr_paths` field is the deterministic source of truth.
A focused follow-up ADR can introduce the typed link if
a validation rule emerges that cannot be expressed against
`adr_paths` alone.

### Skip schema registration; let the parser exist standalone

Keep the validation policy parser in
`internal/domain/validation_policy.go` but do not register
the file shape in artifact-schema. **Rejected** because
artifact-schema is the single source of truth for which
file shapes are governed. Skipping registration would force
every future tool that walks committed governance to
hard-code knowledge of validation policies, and would leave
ADR authors with no canonical place to learn how to
reference one. The cost of registering is one §5.x entry
plus this ADR.

### Canonical path under `/architecture/` instead of `/governance/`

Place validation policies at
`/architecture/validation-policies/<name>.yaml`. **Rejected**
because architectural documents describe how Spine works;
validation policies enforce governance commitments. A
policy's contents are the deterministic shape of an ADR's
"this MUST hold" statement — not a description of system
behavior. The `/governance/` root makes that ownership
visible.

---

## Links

- Initiative: [INIT-014](/initiatives/INIT-014-multi-repository-workspaces/initiative.md)
- Epic: [EPIC-006](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/epic.md)
- Predecessor task: [TASK-002 — ADR-linked validation policy format](/initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-006-cross-repo-execution-evidence/tasks/TASK-002-adr-linked-validation-policy-format.md)
- Architecture: [Validation Policy Format](/architecture/validation-policy.md)
- Architecture: [Execution Evidence Schema](/architecture/execution-evidence.md)
- Governance: [Artifact Front Matter Schema](/governance/artifact-schema.md)
- Companion ADRs: [ADR-013](/architecture/adr/ADR-013-repository-identity-and-catalog-binding-split.md) (the prior pure-YAML governed artifact pattern)
