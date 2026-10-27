---
id: TASK-002
type: Task
title: "Workflow definition strictness: shipped validate steps fail closed, one YAML document, nested workflow directories guarded"
status: In Progress
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

## Implementation Notes

Branch `init/INIT-024-TASK-002-workflow-definition-strictness`, cut from `init/INIT-024-scaffold`
`12376b55` (no Go file differs from `a560c210`). Diff base: `git diff init/INIT-024-scaffold...HEAD`.
Scratch (git-ignored, SMP working tree): `tmp/init027-orchestration/i24/t002/`. Databases used the
prefix `s24002_` and were dropped.

### Owner questions (defaults recorded, pending the owner)

- **OQ1 — N-7 default:** default taken, **on** for every reader (Design 2). No option was added.
- **OQ2 — backstop scope:** default taken, **any depth** (`a/b/c/workflows/x.yml` is held).
- **OQ3 — when the backstop applies** (added at review round 1; orchestrator ruling, pending the
  owner). *Default:* **always on** while the guard is on, narrowed to literal file entries (review
  round 1 fix 1): holding CI-workflow YAML in the primary repository for an operator approval is a
  reasonable fail-safe, and code repositories are not gated. *Alternatives:* (a) apply the backstop
  only when `WorkflowDirs` is empty and the orchestrator has no artifacts directory (N-5's
  precondition) — SMP passes `WorkflowDirs` from the projection constant, so the backstop adds
  cost there but no coverage; (b) an embedder opt-out field on `GovernanceGuard` whose zero value
  keeps it on. Not implemented.

### What was built

- **N-7 — `core/yamlsafe/decoder.go`.** After the strict decode, `DecodeIntoStrict` reads **every**
  remaining document, not only the next one (so `---\n---\nid: b` cannot pass on an empty first
  trailer). `io.EOF` accepts; a document that is exactly what a bare `---` produces is skipped
  (`isEmptyDocument`: a `DocumentNode` whose only child is an untagged, unanchored `!!null` scalar
  with value `""` and no children; comments do not count as content); anything else is
  `invalid YAML: unexpected second document (only one document per file)`, and a parse error in the
  trailer is `invalid YAML: trailing content: …`. The trailers decode into `yaml.Node` only (no
  alias expansion), bounded by the `MaxBytes` cap `Decode` already enforced. `workflow.Parse`,
  `ProjectionDefinition`, the projection (quarantine, last verified row kept, sync advances; the
  per-process upgrade sweep re-checks installed rows against head) and the workflow writers inherit
  it.
- **N-4 — `workflows/{adr,artifact,document}-creation.yaml`.** `timeout_outcome: invalid` on the
  `validate` step; versions `1.0` → `1.1`. Corpus guards in `workflows/reference_workflows_test.go`.
- **N-5 — `core/engine/governance_guard.go`.** `buildGovernanceCheck` takes its `Paths` from the new
  `governedPathsFor(changed)`: the configured set (`g.paths`, built by `WithGovernanceGuard` as
  before), plus `<o.ArtifactsDir()>/workflows` read at decision time, plus
  `branchprotect.WorkflowDefinitionPaths(changed)` (the matched files themselves, since review
  round 1). `checkGovernedMerge` (the post-merge assertion)
  adds the backstop over the merge's actual diff (`Diff(BaseSHA, M)`), so rename following into a
  `workflows` directory no configured entry names is refused like rename following into
  `workflows/`. `WithGovernanceGuard` itself is unchanged apart from the `WorkflowDirs` doc comment.
  TASK-001's `recordLifecycleStarter` is untouched.
- **`core/branchprotect/governance.go`.** New `GovernancePaths.WithLiteralEntries(entries ...string)`
  and `WorkflowDefinitionPaths(paths []string) []string` (named `WithDirs` / `WorkflowDefinitionDirs`
  before review round 1); `Match` now decides by lookup (below).

### Deviations (with reasons)

1. **Derived entries are literal, not validated by `NewGovernancePaths`** (Design 3 said
   "validated by `NewGovernancePaths`"). `NewGovernancePaths` refuses `* ? [ \ "` and empty or dot
   segments — a hygiene rule against operators writing patterns — but those are legal in git paths
   (`app/[id]/workflows/x.yaml`), and a refused backstop entry would leave nothing to add: the merge
   would go through unheld, or need a permanent refusal. `WithLiteralEntries` therefore folds the entry and trims
   leading/trailing `/` only (an empty result is skipped). Matching is still `Match`'s literal folded
   prefix compare, so a derived entry can never act as a pattern. Pinned by
   `TestGovernanceGuard_BackstopIgnoresNonWorkflowYAML/app_[id]_workflows_x.yaml` and
   `TestGovernancePaths_WithLiteralEntries`.
2. **`Match` is reimplemented as a lookup** (Design 3: "`Match` is unchanged"). Codex round 1 (P2):
   one entry per changed workflow directory made `Touched()` quadratic in run-controlled input
   (80,000 paths: 39.9 s per evaluation, and the gate evaluates more than once). `Match` now looks the
   folded path and each prefix ending before a `/` up in a set built by the constructors; this decides
   exactly `q == E || HasPrefix(q, E+"/")` (entries never end in `/`). The fail-closed prelude (empty,
   unsafe, C-quoted) is unchanged. Equivalence is pinned against the old scan on fixed and 20,000
   generated paths (`TestGovernancePaths_IndexedMatchEqualsScan`).
3. **The post-merge assertion also applies the backstop.** The artifact names only
   `buildGovernanceCheck`/`WithGovernanceGuard`; without this, a rename-following merge could land a
   definition in a `workflows` directory the pre-merge diff never named
   (`TestGovernanceGuard_BackstopCoversPostMergeRenameFollowing`; a mutation run with the post-merge
   line reverted fails it).
4. **AC 8: one existing case changed.** `TestGovernanceGuard_ArtifactsDirWorkflows/"spine/workflows
   unguarded without WorkflowDirs"` asserted that `spine/workflows/task-default.yaml` merges with no
   `WorkflowDirs` — the N-5 defect itself. It now expects the hold, and a new case pins the documented
   residual (a nested `spine/workflows/steps/x.yaml` with neither `WorkflowDirs` nor an artifacts
   directory still merges). Every other guard and projection test passes unchanged.
5. **Additions not in the design.** The three workflow versions are bumped to `1.1`; the scenario
   golden fixture `goldenArtifactCreationYAML` (which "mirrors production") now times out as
   `invalid` too; explicit nulls, tags and anchors in a trailing document (`--- ~`, `--- !!null`,
   `--- &x`) are refused as content, comment-only trailers are accepted; extra tests
   `TestGovernanceGuard_BackstopWithCustomPaths`, `TestIncrementalSync_EmptyTrailingDocumentWorkflowInstalled`,
   `TestWorkflowDefinitionPaths`, `TestGovernancePaths_WithLiteralEntries`,
   `TestGovernanceCheck_TouchedLinearInDerivedEntries`.

### Acceptance criteria

Base runs: the final test files copied into a `git archive` of `12376b55`
(`red-base-final.log`); branch runs at `a80e7aee` (`green-branch-final.log`).

| AC | Test(s) | At the scaffold base | On the branch |
|---|---|---|---|
| 1 | `TestGovernanceGuard_ArtifactsDirWorkflowHeldWithoutWorkflowDirs` | FAIL: `run = completed, want committing (held)` (it merges) | PASS |
| 2 | `TestGovernanceGuard_UsesOrchestratorArtifactsDir` | FAIL: after-guard, before-guard and `./spine/` cases merge; the no-dir and other-dir controls pass | PASS |
| 3 | `TestGovernanceGuard_BackstopIgnoresNonWorkflowYAML` | FAIL on the held cases (`Docs/WORKFLOWS/x.YML`, `docs/workflows/x.yaml`, KELVIN SIGN, `.github/workflows/ci.yml`, `app/[id]/…`, …); `docs/notes.yaml`, `docs/workflows/README.md` and the other controls pass | PASS |
| 4 | `TestDecodeIntoStrict_RefusesSecondDocument` | FAIL: all 9 refusal cases accepted; the 8 accepted cases pass | PASS |
| 5 | `TestParse_RefusesTrailingDocument`, `adapters/projection` `TestIncrementalSync_TrailingDocumentWorkflowQuarantined` | FAIL: both refusal cases parse; the row is installed at c2 (`source_commit` c2, Active) | PASS (kept quarantined at c1, one `parse`/`kept` rejection, sync advanced) |
| 6 | `TestReferenceWorkflows_TimeoutNeverRoutesIntoReview` | FAIL naming `adr-creation.yaml`, `artifact-creation.yaml`, `document-creation.yaml` | PASS |
| 7 | existing corpus parse tests + `TestReferenceWorkflows_SingleDocument` | PASS | PASS |
| 8 | every existing `core/engine` guard and projection test | — | PASS, except the one case of Deviation 4 |
| 9 | SMP compatibility gate, SMP corpus and projection-writer suites | — | PASS (below) |
| 10 | docs | — | done (below) |

Also: `TestGovernanceGuard_BackstopCoversPostMergeRenameFollowing` FAILS at the base (the merge is
pushed) and passes; `TestGovernanceCheck_TouchedLinearInDerivedEntries` FAILS on the pre-fix
matcher of `27016959` (39.9 s > 10 s) and passes (0.09 s; 0.9 s under `-race`).

### Documentation

`architecture/workflow-definition-format.md` (§2.3 one document per file; §3.2 timeouts on automated
checks), ADR-016 §2 implementation note (the per-decision set, the artifacts-directory source, the
backstop, literal entries, its cost and residual), `KNOWN-LIMITATIONS.md` (guard intro, new item 18;
projection-sync bullet: workspace repositories are not repaired), `architecture/security-model.md`,
`docs/integration-guide.md` (`SPINE_GOVERNANCE_GUARD` row and the wiring step), `CHANGELOG.md`
(Security, Changed (standalone), Changed (embedders); tells operators who copied the three files to
change `timeout_outcome`).

### Gates
Logs are in the scratch directory. Code gates ran at `bf27be29`/`a80e7aee` (the commits after it
change only Markdown) unless noted.

| Gate | Command | Result |
|---|---|---|
| Boundary lint | `./dev/lint-boundaries.sh` | rc 0 (`411d97c3`; no import changed since) |
| Build | `make build` | rc 0 (`411d97c3`) |
| Embed smoke test | `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | rc 0 (`411d97c3`) |
| Vet | `go vet ./...`, `-tags integration`, `-tags scenario` | rc 0 each (`411d97c3`) |
| Unit | `go test -p 4 -count=1 ./...` | rc 0 at `411d97c3`; after the codex fix `go test -p 4 ./core/... ./adapters/projection/ ./workflows/ ./service/cmd/...` rc 0 |
| Race, touched packages | `go test -race -p 4 ./core/engine/ ./core/branchprotect/... ./core/yamlsafe/ ./core/workflow/ ./adapters/projection/ ./workflows/` | rc 0 (`bf27be29`) |
| Integration, touched packages | `SPINE_TEST_DATABASE_URL=… go test -tags integration -p 1 ./adapters/projection/ ./core/branchprotect/... ./core/yamlsafe/ ./core/workflow/ ./workflows/` | rc 0, 689 passes, 0 skips |
| Scenario, full | `go test -tags scenario -p 1 ./service/scenariotest/...` with a migrated registry, branch (`411d97c3`) and a `git archive` of `12376b55` | both rc 1 with the **identical** 31 top-level / 59 total failures (pre-existing) and 1711 passes each |
| Scenario race | `go test -race -tags scenario -run 'TestGovernancePathGuard_\|TestWorkflowLifecycle_\|TestPlanningRun_\|TestArtifactCreation'` | 18 passes, 0 data races; the 2 failures (`TestArtifactCreationEntry_FirstArtifactInScope`, `…_TaskGoldenPath`) are in the base list |
| CI scenario-git-http job | `TestGitHTTP_*`, `TestBootstrapInternalAdmin_*`, `TestWorkflowLifecycle_*`, plus the workflow-policy, step-CAS, step-evidence, status-lifecycle, `TestGovernancePathGuard_*` and governance-store legs | all rc 0, 0 skips (87/26/130/1/12/24/37/1/19/1 passes) |
| git-floor | `golang:1.26-bookworm` (`--pull=never`, read-only mounts), git 2.39.5: the two CI steps plus `TestGovernanceGuard_*` and this task's yamlsafe/branchprotect/projection/workflows tests | rc 0 |
| golangci-lint v2.11.4 | `--new-from-rev 12376b55` on `core/engine`, `core/branchprotect`, `core/yamlsafe`, `core/workflow`, `adapters/projection`, `workflows` (plain, integration, scenario) | 0 new issues (a first run flagged G404 on the deterministic RNG of the equivalence test; annotated in `bf27be29`, as `core/workflow/termination_test.go` does) |
| gosec only | `--enable-only=gosec`, branch vs base tree | 0 / 0 |
| gofmt | `gofmt -l` on every changed `.go` file | clean except `service/scenariotest/scenarios/planning_run_test.go`, unformatted at the base already (only a YAML string line changed) |
| SMP compatibility gate | in `smp` (`3addd44b`, `../spine` = this worktree, `dirty=0`, HEAD `bf27be29`): `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario ./scenariotest/...`, `-tags faultinject` | all rc 0 |
| SMP race | `go test -race -p 4` on `cmd/customer-app`, `githttp`, `gitread`, `projectionwriter`, `spinecore/...`, `spineembedded/...`, `stepevents`, `workspace/...`, `run/...`, `internal/engine/...`, `executionreadiness`, `projectionsvc` (every SMP package importing a changed Spine package: `executionreadiness`, `githttp`, `projectionsvc` (no tests), `projectionwriter`, `spinecore`, `spineembedded`, `workspace`) | 14 ok; `gitread` fails `TestClient_ErrorContractEqualsCLIClient` and `TestClient_SpineReadFileRendersTheGitlinksPatch` — the two by-design gitread contract tests; both fail identically with SMP unchanged against the base tree (`-modfile` with `replace => base-tree`), so they are not caused by this task |
| SMP testgate | Spine-runtime leg (`spinecore`, `spineembedded`, `runner`, `executionreadiness`; platform migrations via `dev/apply-migrations`) and `-tags integration ./internal/customer/githttp/` | PASS: 2143 passed, 3 allowed skips; PASS: 588 passed, 1 allowed skip; `ws_*` databases and roles 0 before / 0 after |
| SMP corpus and projection suites | `internal/automation` (`workflow_timeout_outcomes_test.go`), `projectionwriter`, `workspace` (workflow backfill), `spinecore` (workflow writer/reader) | pass (in the race run and the full plain run below) |

### SMP impact and hand-offs

- **N-7 (shared, §7b Rule 4).** Verified with SMP unchanged at `3addd44b`: no `---`/`...` separator
  line in Spine `workflows/*.yaml`, SMP `workflows/*.yaml`, SMP `workflows/steps/*.yaml` or SMP's
  repo seed. A probe overlay of this branch's `decoder.go` (via `go test -overlay`, nothing written to either
  repository; confirmed loaded in SMP test binaries) logged every trailing-document refusal and
  every accepted empty trailer while SMP ran `go test ./...` (148 packages ok; only the two gitread
  tests above fail) and the `-tags integration` runtime, projection-writer and githttp suites
  against `s24002_` databases (all ok): **zero refusals and zero empty trailers**. No SMP test, seed
  or fixture feeds a definition the stricter parser refuses. SMP companion task for TASK-002: re-run
  SMP's workflow corpus and projection suites at the bump (§7b).
- **N-5.** Inert for SMP today (guard off). When SMP TASK-010 turns the guard on, the backstop also
  holds a merge that adds `x/workflows/y.yaml` anywhere in a workspace repository (including
  `.github/workflows/*.yml`), and SMP's own `workflows/steps/*.yaml` stay covered by the default
  `workflows/` entry. SMP TASK-010's suites should be re-run against this task at its bump.
- **N-4.** Inert: SMP seeds its own corpus, already `invalid`.
- **New API (additive):** `branchprotect.GovernancePaths.WithLiteralEntries`, `branchprotect.WorkflowDefinitionPaths`.
  No interface SMP implements changed.

### Codex
`codex review --base init/INIT-024-scaffold`, plain, from the worktree; findings read from the review
sub-session (`source: {"subagent":"review"}`, `cwd` = the worktree), model confirmed `gpt-6.1-sol`.

- **Round 1** (session `01a16911-b638-7630-b1ad-dba42c23cd99`, over `411d97c3`): one **P2** —
  quadratic matching of derived workflow directories (`Touched()` scanned every entry for every
  path, and the backstop adds an entry per changed directory). **Accepted and fixed** in `c6361d97`
  (Deviation 2), with `TestGovernanceCheck_TouchedLinearInDerivedEntries` (fails on the pre-fix
  matcher: 39.9 s for 80,000 paths; 0.09 s after) and the equivalence test.
- **Round 2** (session `01a16922-1c75-7972-b24b-3f5e3b47e5ab`, over `c6361d97`): no findings.
- **Round 3** (session `01a16998-7cd8-7633-84ab-b6d195bb0471`, over `568a2892`, the final code): no
  findings.

### Review round 1 (no P1/P2; three P3s fixed)

Report: SMP `tmp/init027-orchestration/i24/t002-reviews/REVIEW.md`, over `f6c81316`.

1. **The backstop governed the whole directory** (`422cb162`). It added each matched file's parent
   as a prefix entry, so a script, README or nested file the same merge changed beside a
   definition (`.github/workflows/scripts/build.sh`) was held too, against what the docs promised.
   It now adds the matched files themselves as literal entries; the post-merge pass over the actual
   diff still catches rename following (`TestGovernanceGuard_BackstopCoversPostMergeRenameFollowing`
   green). Renamed the unreleased API: `WithDirs` → `WithLiteralEntries`, `WorkflowDefinitionDirs`
   → `WorkflowDefinitionPaths`. New tests: `TestGovernanceGuard_BackstopHoldsOnlyTheDefinitionFiles`
   (ci.yml held alone beside a script, README, JSON and nested YAML; the siblings alone merge) —
   it fails with the `f6c81316` guard (all five paths held) and passes now — and
   `TestWorkflowDefinitionPaths_GovernsOnlyTheFiles`.
2. **Upgrade note** (`6dc50d90`, CHANGELOG "Changed (standalone)" and KNOWN-LIMITATIONS "Projection
   Sync"). `spine serve` loads a run's workflow with `GitWorkflowLoader`, which re-parses the file at
   the pinned commit on every action that needs it, so a run bound before the upgrade to a
   two-document file fails those actions. Remedy: remove the second document on `main`, cancel the
   run (`CancelRun` does not load the workflow; not from `committing`) and start it again. Checked
   that the projection-backed paths are unaffected, in Spine and in SMP: the scheduler's timeout and
   recovery scans (`adapters/scheduler/timeout.go`, `recovery.go`) and SMP's
   `ProjectionWorkflowLoader` read the stored row by path with no status filter, and the projection
   keeps a refused file's verified row (quarantined) at its old `source_commit`; binding lists Active
   rows only, so no new run binds.
3. **CLI** (`d94e65fc`). `spine workflow list/show/resolve` now read each file with
   `workflow.ProjectionDefinition`, the projection's own acceptance (strict keys, one document, the
   round trip). `list` and `resolve` skip a refused file with a stderr warning naming it and the
   reason; `show` returns the error. New tests `TestWorkflowCLI_SecondDocumentRefusedLikeTheServer`
   (fails with the old CLI: the two-document workflow is listed) and `TestWorkflowCLI_ShowDetail`
   (the detail output is unchanged).
- Nit (`--- !` accepted as an empty trailer): left as is; harmless, as the review says.
- OQ3 recorded above (orchestrator ruling; default always on, file entries only).

Gates after the fixes (head `6dc50d90`; the notes commit after it is Markdown only):

| Gate | Result |
|---|---|
| `go build ./...`; `go vet` on the touched packages (plain, integration, scenario) | ok |
| `go test -p 4 ./core/engine/ ./core/branchprotect/... ./core/yamlsafe/ ./core/workflow/ ./adapters/projection/ ./workflows/ ./service/internal/cli/` | rc 0 |
| the same with `-race` | rc 0 |
| `go test ./service/cmd/...`; `./dev/lint-boundaries.sh` | ok; rc 0 |
| golangci-lint `--new-from-rev 12376b55` (plain, integration, scenario) on those packages | 0 new (a `rangeValCopy` in the new CLI code was fixed before commit) |
| gosec only, branch vs base | 0 / 0 |
| gofmt on the changed `.go` files | clean |
| SMP gate in `smp` (`../spine` = `6dc50d90`, the notes edit uncommitted): build, vet plain / integration / scenario / faultinject; testgate Spine-runtime leg; testgate githttp | all rc 0; 2143 passed (3 allowed skips); 588 passed (1 allowed skip); `ws_*` 0 / 0 |
| codex (session `01a169d9-f1f1-72e3-b69f-abfd0235e96a`, `gpt-6.1-sol`, over `6dc50d90`) | no findings |

### Not run

- GitHub CI (nothing is pushed; INIT-024 owner question 1).

### Integration A (2026-10-27)

Branch `init/INIT-024-integration-a`, cut from Spine local main `83d82409` (= `a560c210` + the
INIT-024 scaffold + TASK-001), in the TASK-008 group's worktree. Merge order (each `--no-ff`,
"Merge <branch> into the INIT-024 integration A branch"): TASK-009 `b8dc7bb3` → `899cd774`;
TASK-002 `07d6ca8a` → `c6bad6c0`; TASK-006 `96dcea55` → `000c5461`; TASK-007 `55a67a45` →
`e6ab7e81`; TASK-008 `c6541bd5` → `8a0a1b29`; TASK-003 `02334613` → `4f502b7e`. Coordination
commits (prefix "INIT-024 integration A — "): `7d0e8522` (TASK-006 × TASK-007), `69c15fb4`
(TASK-007 × TASK-008), `55789b28` (TASK-003 × TASK-007/TASK-008), `377c3343` (TASK-007 ×
TASK-008 logging). Nothing is pushed; the status stays In Progress until GitHub CI has run after a
push.

`CHANGELOG.md` conflicted at every merge (each task adds its own section at the top): every
section is kept once, newest merge on top, each followed by a `---` line.

**This task.** Merged second (`c6bad6c0`). Conflicts: `CHANGELOG.md`, and the
`docs/integration-guide.md` env table, where TASK-001 edited the `SPINE_STEP_ENTRY_POLICY` row and
this task the `SPINE_GOVERNANCE_GUARD` row (the two rows taken from their editors; TASK-008's
later edit of the same table kept both). `core/engine/governance_guard.go` auto-merged with
TASK-001's `recordLifecycleStarter` (different functions); `go vet` of `core/engine`,
`core/branchprotect` and `core/yamlsafe` passed on the merge, and the -race run of those packages
plus `core/workflow`, `workflows` and `adapters/projection` passed at the phase-1 head. No
coordination commit was needed.

Gates at `377c3343` (logs in the SMP repo, `tmp/init027-orchestration/i24/int-a/`; heavy legs under
the shared heavy-suite lock):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`, `make build`, embed-smoketest | OK / rc 0 / "library surface is sound" |
| `go vet` plain / `-tags integration` / `-tags scenario` | rc 0 ×3 |
| `go test -p 4 -count=1 ./...` | rc 0 |
| `go test -race -p 4 -count=1 ./...` | rc 0 |
| ci.yml `scenario-git-http`, every run step, own database | every step rc 0, 0 skips: `TestGitHTTP_*` 90 passes, `TestBootstrapInternalAdmin_*` 26, `TestWorkflowLifecycle_*` 154, step CAS 12, step evidence 24, status lifecycle 37 + 1, governance 19 + 1, the shared-mode scheduler test; every required `--- PASS` present |
| `go test -tags integration -p 1 ./...` (migrated registry database) | 39 packages ok; only the two known pre-existing failures (`TestAWSClient_Contract_LocalStack`, `TestRunModeDatabaseDefault`) |
| ci.yml `git-floor` (local `golang:1.26-bookworm`, git 2.39.5, go1.26.8 with `GOTOOLCHAIN=local`, `--pull=never`; the pinned 1.26.10 image is not present locally) | both CI steps rc 0, plus TASK-003's budget/hook tests and the integration coordination tests rc 0 |
| golangci-lint v2.11.4 `--new-from-rev main ./...` plain / integration / scenario; `--enable-only=gosec` | 0 issues ×4 |
| `gofmt -l` on the files changed since main | clean, except `service/scenariotest/scenarios/planning_run_test.go`, already unformatted on main (left as is) |
| `go run ./dev/pincheck`; `go mod tidy -diff`; `go mod verify` | OK; no diff; all modules verified |
| SMP compatibility (SMP `82899ace` detached, `../spine` = the integration head, `GOFLAGS=-mod=readonly`, 0 dirty files before and after) | `go build ./...` and `go vet` plain / integration / scenario / faultinject rc 0; `go test -race -p 4 ./...` 151 packages ok, 0 failures; testgate Spine-runtime leg PASS (2447 passed, 3 allowed skips); testgate githttp leg (`-tags integration`, -race) PASS (590 passed, 1 allowed skip); no `ws_*` database or role left |

Codex (`codex review --base main`, plain, from the integration worktree): round 1 at `377c3343`,
review sub-session `01a17e99-5b95-71f3-995c-e5f81a1d37b2`, model `gpt-6.1-sol`: no findings ("No
actionable regressions were identified"). It covered every merged task and coordination commit,
including TASK-003's `5636c02d` and `e57a6741`, which had no task-level codex round.
