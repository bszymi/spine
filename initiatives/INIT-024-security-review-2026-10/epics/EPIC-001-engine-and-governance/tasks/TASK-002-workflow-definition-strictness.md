---
id: TASK-002
type: Task
title: "Workflow definition strictness: shipped validate steps fail closed, one YAML document, nested workflow directories guarded"
status: Pending
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-001-engine-and-governance/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-001-engine-and-governance/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-006-governance-path-guard.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/tasks/TASK-011-projection-sync-fail-closed.md
  - type: related_to
    target: /architecture/workflow-definition-format.md
---

# TASK-002 — Workflow definition strictness: shipped validate steps fail closed, one YAML document, nested workflow directories guarded

---

## Purpose

This task closes three P3 findings from the 2026-10-22 review (detail in SMP's git-ignored
`tmp/security-review-2026-10-22/spine/REPORT.md`, N-4, N-5, N-7). Each is a place where a
workflow definition, or the guard over it, is less strict than the rest of the system.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| N-4 | P3 | Standalone: Spine's shipped `workflows/` corpus, and every repository that copied it. SMP's own copies already use `invalid`. | A validator that is missing, hangs or is starved for 5 minutes. Human review still follows, so the gate fails open into review rather than into `main`. |
| N-5 | P3 | Embedders that set `artifacts_dir` on the projection but pass no `WorkflowDirs` to the guard and do not call `WithArtifactsDir`. `spine serve` passes them; SMP sets no `artifacts_dir`. | A run-branch author whose governed merge adds `<dir>/workflows/<id>.yaml`. The projection then installs it as live policy without an operator approval. |
| N-7 | P3 (hygiene) | Both | A writer of a workflow file. Every Spine reader agrees on the first document, so there is no live differential today. A second document is silently ignored, unlike the branch-protection parser. |

## Current code (verified at `a560c210`)

- **N-4.** The `validate` steps set `timeout: "5m"` with `timeout_outcome: valid`:
  - `workflows/adr-creation.yaml:52`;
  - `workflows/artifact-creation.yaml:66`;
  - `workflows/document-creation.yaml:56`.

  In each, `valid` routes to `review` and `invalid` to `draft`. SMP's copies use `invalid` (SMP
  `workflows/adr-creation.yaml:54`, `artifact-creation.yaml:70`, `document-creation.yaml:58`).
  `spine init-repo` seeds only `workflow-lifecycle.yaml` (`service/internal/cli/initrepo.go:99`),
  whose timeout outcome is `needs_rework`. `task-default.yaml:37` (`ready` on the AI `draft`
  step, which leads to `execute`) is not a validation gate and is left alone.
- **N-7.**
  - `core/yamlsafe/decoder.go:63-73` `DecodeIntoStrict` decodes one document and stops. Its only
    Spine caller is `core/workflow/parser.go:62` (`Parse`).
  - SMP calls `workflow.Parse` directly at SMP `internal/customer/spineembedded/workflows.go:157,
    :319` and `internal/customer/spinecore/workflowwriter.go:141`.
  - The branch-protection parser refuses a second document
    (`core/branchprotect/config/parser.go:47-53`).
  - Measured with yaml.v3 v3.0.1 on 2026-10-23: `"a: 1\n---\n"` yields a second, empty document
    node, and `"a: 1\n...\n"` does not.
- **N-5.**
  - `core/engine/governance_guard.go:88-92` documents `WorkflowDirs` as the caller's job.
  - `WithGovernanceGuard` builds the guarded set once (`:313-322`) from `Paths`, or
    `branchprotect.DefaultGovernancePaths` (`core/branchprotect/governance.go:37`: `.spine/`,
    `.spine.yaml`, `workflows/`), plus `WorkflowDirs`.
  - `buildGovernanceCheck` (`:499-523`) uses that fixed set.
  - The projection installs direct children of `<dir>/workflows/`
    (`adapters/projection/scope.go:71`, `WorkflowDirs` `:130`).
  - The orchestrator already knows the directory when the embedder calls `WithArtifactsDir`
    (`core/engine/orchestrator.go:252`, `ArtifactsDir` `:258`), but the guard never reads it.
  - `spine serve` passes the directories (`service/cmd/spine/serve_governance_guard.go:75-80`,
    `serve_resolver.go:113`).

## Design

1. **N-4: the shipped corpus fails closed on timeout.**
   - Set `timeout_outcome: invalid` on the three validate steps. That is SMP's value, and a hung
     validator now sends the run back to `draft`.
   - Add the corpus guard `TestReferenceWorkflows_TimeoutNeverRoutesIntoReview` to
     `workflows/reference_workflows_test.go`. For every shipped step of type `automated`, the
     outcome named by `timeout_outcome` must not have a `next_step` whose type is `review`.
2. **N-7: one document per workflow file, default on (argued below).**
   - After the strict decode, `DecodeIntoStrict` decodes a second `yaml.Node`.
     - `io.EOF`: accept.
     - An empty document: accept. That is a `DocumentNode` whose only child is a `!!null` scalar
       with value `""`, no anchor and no further content; a trailing bare `---` produces one.
     - Anything else: `invalid YAML: unexpected second document (only one document per file)`.
     - A parse error in the trailing content is returned wrapped.
   - Why default on, against initiative §5's opt-in rule:
     - no Spine or SMP reader ever consumed a second document, so a refusal only rejects files
       whose meaning is ambiguous;
     - neither corpus contains a `---` line (checked 2026-10-23);
     - a refused file in a workspace repository is quarantined by the INIT-023 TASK-011 projection
       rules, with its last verified row kept, rather than half-installed.
3. **N-5: the guard covers every directory the projection could install from.**
   - At decision time, `buildGovernanceCheck` builds the effective set from three sources:
     - (a) the configured set, as today;
     - (b) `<o.ArtifactsDir()>/workflows/` when the orchestrator has an artifacts directory, read
       at decision time so the call order of `WithArtifactsDir` and `WithGovernanceGuard` no
       longer matters;
     - (c) a structural backstop: for every changed path whose parent directory's last component
       folds to `workflows` (the same simple case folding as `foldPath`) and whose leaf ends in
       `.yaml` or `.yml` (folded), that parent directory is added as an entry.
   - (c) over-approximates the projection's set for every possible `artifacts_dir`. So an
     embedder that configured the projection's directory but told neither the guard nor the
     orchestrator still holds the merge.
   - `branchprotect.GovernancePaths.Match` is unchanged. The new entries are ordinary prefix
     entries, validated by `NewGovernancePaths`.
   - The zero value is unchanged: no guard, nothing held.

## SMP impact

- **N-7 is a shared-behaviour change** (initiative §7b Rule 4). The SMP companion task re-runs
  SMP's workflow corpus test, the projection-writer suites and the workflow writer and reader
  tests at the bump.
- **N-5 matters only with the guard on.** SMP TASK-010, built and not yet merged, turns it on.
  The backstop then also holds a merge that adds `x/workflows/y.yaml` anywhere in a workspace
  repository. SMP TASK-010's suites are re-run against this task.
- **N-4 is inert.** SMP seeds its own corpus.

## Decisions

1. **The backstop (3c) is on whenever the guard is on.** There is no field to turn it off. Its
   cost is an operator approval for a non-definition YAML file in a directory named `workflows`.
2. **An empty trailing document is accepted.** Editors and generators append a bare `---`; it
   carries no content.
3. **No `workflow.Validate` warning for forward-routing timeouts.** `Validate` stays advisory
   (INIT-023 owner question 7), and the corpus test covers what ships.

## Owner questions

1. **N-7 default.** *Default:* on (Design 2). *Alternative:* an orchestrator or parser option,
   off for embedders.
2. **Backstop scope.** *Default:* any depth. *Alternative:* only `<one segment>/workflows/`.

## Acceptance Criteria

1. **Regression: N-5 PoC** (`core/engine`, `TestGovernanceGuard_ArtifactsDirWorkflowHeldWithoutWorkflowDirs`).
   - This ports the `artifacts_dir_wf` case of `scratch/governance/f3_guard_test.go`:
     `GovernanceGuard{Enabled: true}` with no `WorkflowDirs` and no `WithArtifactsDir`, and a run
     that adds `spine/workflows/task-default.yaml`.
   - The merge is held (run `committing`, `governance.merge_held` emitted). It merges at
     `a560c210`.
2. **N-5 via `WithArtifactsDir`** (`TestGovernanceGuard_UsesOrchestratorArtifactsDir`).
   - With `WithArtifactsDir("spine")` called **after** `WithGovernanceGuard`, a change to
     `spine/workflows/steps/x.yaml`, which is nested and so not caught by the backstop, is held.
   - It fails at `a560c210`.
3. **N-5 controls** (`TestGovernanceGuard_BackstopIgnoresNonWorkflowYAML`):
   - `docs/notes.yaml` is not held;
   - `docs/workflows/README.md` is not held;
   - `Docs/WORKFLOWS/x.YML` is held.
4. **Regression: N-7** (`core/yamlsafe`, `TestDecodeIntoStrict_RefusesSecondDocument`).
   - Refused: `"id: a\n---\nid: b\n"`, `"id: a\n--- \nsteps: []\n"`, and a second document
     holding only an anchor.
   - Accepted: `"id: a\n"`, `"---\nid: a\n"`, `"id: a\n---\n"` and `"id: a\n...\n"`.
   - The first three are accepted at `a560c210`.
5. **N-7 through `workflow.Parse` and the projection.**
   - `TestParse_RefusesTrailingDocument`.
   - `adapters/projection` `TestIncrementalSync_TrailingDocumentWorkflowQuarantined`: a workflow
     row with a trailing document is quarantined, not installed. It is installed at `a560c210`.
6. **N-4 corpus** (`TestReferenceWorkflows_TimeoutNeverRoutesIntoReview`). It fails at
   `a560c210`, naming the three files, and passes after the edit.
7. **Corpora parse.** Every `workflows/*.yaml` in Spine parses (existing tests).
   `TestReferenceWorkflows_SingleDocument` asserts that no shipped file has a second document.
8. **Zero value.** Every existing `core/engine` guard and projection test passes unchanged.
9. **SMP compatibility gate** (initiative §5), plus SMP's workflow corpus and projection-writer
   suites, passes with no SMP change.
10. **Documentation.**
    - `workflow-definition-format.md`: one document per file.
    - ADR-016 implementation note: the backstop and the `ArtifactsDir` source.
    - `KNOWN-LIMITATIONS.md` and `CHANGELOG.md`. The CHANGELOG tells operators who copied the
      three shipped files to change `timeout_outcome` in their own repositories.

## Compatibility

- **N-7.** A workflow file with a second non-empty document is now refused everywhere. This is
  shared behaviour (initiative §7b).
- **N-5.** With the guard on, more merges are held: `<dir>/workflows/**`, and any directory
  named `workflows`.
- **N-4.** A timed-out validate step now routes to `draft`.

## Out of scope

- Repairing existing workspace repositories.
- Blocking `Validate` (INIT-023 owner question 7).
- The `task-default.yaml` draft-step timeout.

## File overlaps

- `core/engine/governance_guard.go`: this task touches `buildGovernanceCheck` and
  `WithGovernanceGuard`; TASK-001 touches `recordLifecycleStarter`.
- `core/yamlsafe/decoder.go` and `workflows/*.yaml`: this task only.
