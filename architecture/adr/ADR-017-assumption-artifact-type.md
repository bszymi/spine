---
id: ADR-017
type: ADR
title: Assumption as a Governed Artifact Type
status: Accepted
date: 2026-11-10
decision_makers: bszymi (accepted product-intelligence types in Core and governed-artifact storage on 2026-11-10)
links:
  - type: related_to
    target: /governance/artifact-schema.md
  - type: related_to
    target: /architecture/engine-state-machine.md
  - type: related_to
    target: /architecture/validation-service.md
---

# ADR-017 — Assumption as a Governed Artifact Type

---

## Context

A decision is recorded in Spine as an ADR. What the decision relies on is recorded nowhere: the
claims a decision takes for granted live in prose, if at all, and nothing says whether anyone
tested them. A decision can be `Accepted` while every claim under it is unverified, and the
artifact model cannot tell the two states apart.

A repository review on 2026-11-10 found that the storage layer already carries typed nodes and
typed edges: `projection.artifacts.artifact_type` and `projection.artifact_links.link_type` are
free text with no database constraint. The closed sets are in Go (`domain.ValidMarkdownArtifactTypes`,
`validLinkTypes`). The same review found two defects that matter here:

- A Markdown file whose `type` Core does not know is skipped by discovery and never projected.
- Link targets are stored with the leading slash of the canonical form, while projection keys
  have none. `QueryArtifactLinksByTarget` matches exactly, so a lookup by projection path returns
  nothing. Checked on a live workspace database: 4 of 4 stored links had a leading slash and none
  matched an artifact path until it was trimmed.

The owner decided on 2026-11-10 that assumptions are governed Markdown artifacts in Core, not
records in an embedder's own store.

---

## Decision

### 1. A new Markdown artifact type: `Assumption`

An Assumption is one claim that some decision or plan relies on, written so that it can be shown
true or false.

- `type: Assumption`
- `id`: `ASM-NNN` (three digits, allocated like other IDs).
- Location: `/assumptions/` (rule SI-005).
- Required front matter: `id`, `title`, `status`, `created` (YYYY-MM-DD).
- Optional front matter, carried as metadata and not interpreted by Core: `impact`
  (`high`, `medium`, `low`), `proposed_by` (`human`, `ai`), `owner`.

### 2. Statuses and lifecycle

| Status | Meaning |
|---|---|
| `Proposed` | Someone or something suggested this is an assumption. Nobody with authority has confirmed it. |
| `Untested` | Confirmed as a real assumption. No evidence either way is recorded. |
| `Supported` | Confirmed, and recorded evidence supports it. |
| `Contradicted` | Confirmed, and recorded evidence contradicts it. |
| `Rejected` | Not an assumption anyone relies on. Final. |
| `Superseded` | Replaced or no longer relevant. Final. |

A status says what is recorded. It is never a claim of certainty.

Initial statuses: `Proposed`, `Untested`.

Transitions:

- `Proposed` → `Untested`, `Supported`, `Contradicted`, `Rejected`
- `Untested` → `Supported`, `Contradicted`, `Superseded`
- `Supported` → `Contradicted`, `Untested`, `Superseded`
- `Contradicted` → `Supported`, `Untested`, `Superseded`

`Supported` → `Untested` and `Contradicted` → `Untested` exist for the case where the evidence
itself stops holding. `Proposed` is how content that no human has confirmed stays visibly
separate from content one has: leaving `Proposed` is a governed status change.

### 3. Three new link types

| Link type | From | To | Meaning |
|---|---|---|---|
| `depends_on` | any artifact | any artifact | The source relies on the target. A decision that relies on an assumption links to it this way. |
| `supports` | any artifact | Assumption | The source is evidence for the assumption. |
| `refutes` | any artifact | Assumption | The source is evidence against the assumption. |

None of the three has a required inverse. The reverse direction is answered by querying links by
target (§5), so the artifact that is depended on does not have to be edited when a new dependent
appears.

Evidence is not a new artifact type. Any governed artifact — a spike task, an ADR, an
architecture document — is evidence when it links to an assumption with `supports` or `refutes`.

### 4. Evidence consistency rule

Two cross-artifact rules, both warnings:

- **AL-001:** an Assumption with status `Supported` has no incoming `supports` link.
- **AL-002:** an Assumption with status `Contradicted` has no incoming `refutes` link.

They are warnings because Core's cross-artifact link rules are warnings today, and because a
status may legitimately be set in the same change that adds the evidence artifact.

### 5. Lookups by target accept both path forms

`QueryArtifactLinksByTarget` matches the target with and without its leading slash. Stored rows
are not rewritten, so no projection rebuild or migration is needed, and the existing consumer
(`CheckAndEmitBlockingTransition`) starts receiving the dependents it was written to receive.

### 6. Not decided here

- An `Evidence` artifact type, per-link attributes (strength, rationale), and time-based
  staleness. Each needs evidence from use before it earns a place in Core.
- Any embedder-side API, UI or retrieval behaviour. Those belong to the embedder.

---

## Consequences

### Positive

- "Which accepted decisions rest on untested or contradicted assumptions?" becomes a query over
  existing tables.
- No migration. Old binaries skip `Assumption` files as they skip any unknown type.
- 338 `depends_on` links already in the SMP corpus become valid instead of merely tolerated.
- Blocking-transition events that depend on by-target lookups begin to fire.

### Negative

- Core gains a concept from the product domain. It stays small: one type, three links, two
  warnings.
- `depends_on` and `blocked_by` now sit side by side. `blocked_by` still alone drives execution
  blocking; `depends_on` has no execution effect.
- Emitting blocking-transition events that were silently missing is a behaviour change for
  embedders that listen for them.

### Neutral

- The lifecycle is enforced where every other artifact lifecycle is: at the governed merge.

---

## Alternatives Considered

### Keep assumptions in an embedder's database

Rejected by the owner. Assumptions would lose git history, review through workflow, and the
ability to be the target of an artifact link.

### A section inside each ADR

No type change at all. Rejected because an assumption shared by two decisions would be written
twice, could not carry its own status, and could not be linked to by evidence.

### Normalise link targets when they are written

Fixes the same lookup defect but changes stored rows, which needs a rebuild of every projection
and changes what every existing reader sees. Matching both forms at query time is the smaller
change.

---

## Links

- [Artifact schema](/governance/artifact-schema.md) §4.1, §5.10, §6
- [Engine state machine](/architecture/engine-state-machine.md) §5A
