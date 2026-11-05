---
id: TASK-011
type: Task
title: "Projection sync integrity: close the workflow parser differential, stop dropped Diff errors, follow the authoritative ref, scope artifacts_dir"
status: In Progress
work_type: security
created: 2026-10-06
last_updated: 2026-11-04
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-003-secrets-delivery-and-projection/epic.md
  - type: related_to
    target: /architecture/data-model.md
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: /architecture/workflow-definition-format.md
  - type: related_to
    target: /architecture/event-schemas.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-006-governance-path-guard.md
---

# TASK-011 — Projection sync integrity: close the workflow parser differential, stop dropped Diff errors, follow the authoritative ref, scope artifacts_dir

---

## Purpose

This task closes four P3 findings from the 2026-10-06 Spine security audit:

- **P3-13**, narrowed to the parser differential;
- **P3-14**, the dropped `Diff` error;
- **P3-22**, the projection following the working tree's `HEAD`;
- **P3-23**, `artifacts_dir` not being applied to incremental sync.

All four live in `adapters/projection/service.go` (`IncrementalSync`, `FullRebuild` and
`projectWorkflow`), so they share one task.

Spine line numbers refer to `61994fe`. SMP line numbers refer to SMP `main` at `80995559`, which
pins Spine `61994fe` in `.spine-revision`. Every behaviour below was re-run on 2026-10-06 with
`go test -overlay`: the four audit PoCs in `poc/store-projection/`, plus probes over every
workflow file both repositories ship.

### P3-13: the projection and the engine read a workflow the governed path rejects (both surfaces)

1. **The governed path is strict.**
   - `workflow.Parse` (`core/workflow/parser.go:58-72`) decodes through
     `yamlsafe.DecodeIntoStrict` (`core/yamlsafe/decoder.go:63-73`). That decode is
     size/depth/alias-bounded and uses `KnownFields(true)`.
   - So unknown keys fail, and yaml.v3 field matching is exact: a key matches only if it is
     byte-equal to the tag. Duplicate keys also fail, because yaml.v3 rejects them.
   - The create/update path uses it (`core/workflow/service.go:125`), and so does
     `StartWorkflowPlanningRun` (`core/engine/run.go:304`).
2. **The projection never calls `Parse`.**
   - `projectWorkflow` (`adapters/projection/service.go:500-553`) bounds the input with
     `yamlsafe.Decode` (`:510`) and fills the typed columns with a non-strict decode
     (`:515-524`).
   - It then stores `json.Marshal` of a decode into `any` as the `definition` column (`:532-539`).
3. **Every loader reads that JSON case-insensitively.** Every loader decodes it with
   `encoding/json`, which matches object keys to struct fields case-insensitively, including
   Unicode simple folding (`ſ` U+017F folds to `s`).
   - Spine decodes it through `workflow.UnmarshalProjectionDefinition`
     (`core/workflow/projection_provider.go:90-98`). Its callers are:
     - the binding provider (`:72`);
     - scheduler recovery (`adapters/scheduler/recovery.go:462`);
     - the timeout scan (`adapters/scheduler/timeout.go:129`);
     - the gateway assign fallback (`service/internal/gateway/step_assign_fallback.go:108`).
   - SMP decodes it through `decodeProjectionWorkflow`
     (`internal/customer/spinecore/workflow_provider.go:111-133`), used by both the provider
     and `ProjectionWorkflowLoader` (`workflow_loader.go:127`).

   > **Design-check note (2026-10-06): the reader inventory is incomplete, and not every
   > reader folds case.** The fix is unchanged, because every reader agrees on an accepted row
   > (exact keys only). But the list matters for the sweep (§6) and for SMP regression scope:
   > - **Spine, folding but not strict:** `core/workflow/skill_references.go:52` (`json.Unmarshal`,
   >   no `DisallowUnknownFields`; Active rows only).
   > - **SMP, folding:** `stepModeFromDefinition` (`internal/customer/projection/projection.go:533-551`),
   >   reached by `GetStepMode` / `GetRunStepMode` (`:478-525`). Both join
   >   `projection.workflows` on **`workflow_id`**, with **no status filter**.
   > - **SMP, exact match (SQL JSONB operators):** `QueryRunnerStepExecutions`
   >   (`internal/customer/spinecore/execution_actor_store.go:196-203`) reads
   >   `w.definition->'steps'` by path, with no status filter.
   >   - On a smuggled row, SMP's runner-step filter therefore sees the **benign** `steps` list
   >     while the engine executes the hidden `ſteps` list. That is a second differential inside
   >     SMP, closed by the same fix.
   > - **Standalone step progression does not read the projection.** `serve` and `pool` wire
   >   `engine.NewGitWorkflowLoader` (`core/engine/workflow_loader.go:22-28`;
   >   `service/cmd/spine/serve_builders.go:246`, `serve_resolver.go:62`), which re-reads the file
   >   at the pinned commit through strict `Parse`.
   >   - So in standalone, the projection-decoded definition drives only these:
   >     - binding (`applies_to` / `mode` match);
   >     - `StartRun`'s entry step and `validateStepRouting` (`core/engine/run.go:162-197`);
   >     - scheduler recovery and timeout;
   >     - the gateway assign fallback.
   >   - The next `LoadWorkflow` then fails `Parse`, and the run stalls.
   >   - The full "review step disappears" chain is **SMP**, whose `ProjectionWorkflowLoader` reads
   >     the projection for every step. Standalone gets a hidden `entry_ſtep` plus a stalled run.
   > - All five audit PoCs in `poc/store-projection/` were re-run at `61994fe` on 2026-10-06 and
   >   all five reproduce.
4. **The attack.**
   - A workflow file carries the reviewed `steps:` list and, after it, a look-alike `ſteps:`
     list without the review step.
   - `json.Marshal` sorts map keys, and `ſteps` (0xC5 0xBF …) sorts after `steps`. So the hidden
     key is decoded last and wins.
   - Three readers see three different things:
     - `Parse` rejects the file;
     - a YAML viewer shows the benign list;
     - the engine executes the hidden one.
   - A one-key variant (`next_ſtep: publish` inside a `failed` outcome) routes a failed
     validation straight to publish. The definition the engine executes passes `workflow.Validate`.
   - PoCs: `TestPOC_ProjectionWorkflowFoldedKeyDifferential` and
     `TestPOC_ProjectionWorkflowFoldedOutcomeOverride` (`poc/store-projection/poc_workflow_fold_test.go`).
     Both reproduce at `61994fe`.
   - **Attacker:**
     - in SMP, any member who can land a file on `main`. A member can do that through a
       self-reviewed governed merge (F3, spine TASK-006 / SMP TASK-010).
     - in standalone, a reviewer or contributor, by the same governed-merge route.
   - **Impact:** the review step disappears from every future run of that workflow. Reviewers
     and validators look at a different workflow than the engine runs.
5. **The same class, re-verified.**
   - `domain.WorkflowDefinition.Path` and `.CommitSHA` carry `json:"path"` /
     `json:"commit_sha"` but `yaml:"-"` (`core/domain/workflow.go:80-81`).
   - A top-level `path:` or `commit_sha:` key is therefore rejected by `Parse`. Today it is still
     projected, and `UnmarshalProjectionDefinition` decodes it into those fields. The scheduler and
     gateway callers above never overwrite them.
6. **The same gap costs availability.** Some files are accepted by `Parse` but their projected
   JSON decodes differently or not at all. Examples:
   - unquoted `version: 1.0`;
   - `retry: {limit: 1.5}`, which `Parse` truncates to 1;
   - a number in a string field;
   - `version: 2026-10-06`, which `Parse` reads as `"2026-10-06"` but the projected JSON carries
     as `"2026-10-06T00:00:00Z"`.

   Today these are projected. In SMP, one such Active row makes
   `ProjectionWorkflowProvider.ListActiveWorkflows` return an error for the whole list
   (`workflow_provider.go:91-93`). Binding then fails for every workflow in the workspace
   (`core/workflow/binding.go:41-44`). Spine's provider silently skips the row instead
   (`projection_provider.go:62-65`).
7. **The narrowing.**
   - Full `workflow.Validate` is *not* made blocking at projection.
   - 10 of the 15 workflow files the two repositories ship fail `Validate` today, although all 15
     `Parse` and round-trip exactly:
     - Spine fails 6 of 8: `adr-creation`, `adr`, `document-creation`, `epic-lifecycle`,
       `task-default` and `task-spike`.
     - SMP fails 4 of 7: `adr-creation`, `document-creation`, `epic-lifecycle` and `task-spike`.
   - The reasons:
     - a missing `timeout_outcome`;
     - an automated step without `retry`;
     - `status: Draft`;
     - SMP actor steps without `required_skills`.
   - SMP seeds these files into every workspace repository.

### P3-14: a failed second `Diff` silently skips governance changes (both surfaces)

- `IncrementalSync` computes the artifact changeset with `artifact.DiscoverChanges` (`:190`),
  which calls `Diff` once.
- It then calls `Diff` a second time and throws away the error:
  `diffs, _ := s.git.Diff(ctx, state.LastSyncedCommit, head)` (`:254`).
- If that second call fails, `diffs` is nil, and two steps are skipped:
  - the branch-protection re-projection (`:265-282`);
  - the workflow loop (`:284-310`).
- `sync_state` still becomes `idle@head` (`:322-327`).
- The next tick returns early, because `head == LastSyncedCommit` and the status is idle
  (`:180-182`). So the change is never retried. That is fail-open on governance config.
- PoC: `TestPOC_IncrementalSyncDropsSecondDiffError` (`poc/store-projection/poc_diffdrop_test.go`).
- Two neighbours of the same kind:
  - A workflow rename drops the error of deleting the old row
    (`_ = s.store.DeleteWorkflowProjection(ctx, diff.OldPath)`, `:301`). The old Active row then
    stays bindable.
  - When the *first* `Diff` fails (`:190-193`), the state is left at `syncing` (set at
    `:185-188`) with no error detail.

### P3-22: the projection follows whatever branch the shared working tree has checked out (standalone)

- Both entry points resolve the target commit with `s.git.Head()`: `FullRebuild` at `:61` and
  `IncrementalSync` at `:174`. That is `git rev-parse HEAD` (`adapters/git/cli.go:542-548`).
- In standalone, one non-bare working tree is shared by the engine and the projection.
  - `applyCommitStatus` checks the run branch out (`core/engine/merge.go:643`) and checks `main`
    back out afterwards (`:666`).
  - A sync tick in that window projects the **unmerged run branch** as live state:
    - a `.spine/branch-protection.yaml` with `rules: []` installs zero rows;
    - workflows change;
    - `last_synced_commit` is stamped with the run-branch SHA.
  - PoC: `TestPOC_ProjectionFollowsCheckedOutRunBranch` (`poc/store-projection/poc_headfollow_test.go`).
- `spine init-repo` commits onto a `spine/init` branch and leaves it checked out
  (`service/internal/cli/initrepo.go:144-149`). So a freshly initialised repository projects
  unmerged content until the user merges it.
- Both cases contradict `architecture/git-integration.md` §2.2: "Projection Service syncs from
  the authoritative branch".
- **SMP is not affected.**
  - Its workspace repositories are bare (`internal/customer/workspace/filesystem_git_allocator.go:229-237`).
  - `HEAD` is a symbolic ref to `refs/heads/<default>`, and a push cannot move it.

### P3-23: `artifacts_dir` is applied by FullRebuild but not by IncrementalSync (standalone)

- `FullRebuild` scopes discovery through `artifact.DiscoverAll(ctx, git, head, dir)`
  (`core/artifact/discovery.go:37-118`):
  - it keeps only paths under `<dir>/` and strips the prefix (`:61-69`);
  - it treats `<dir>/workflows/**` as workflows, keyed by the full repository path (`:72-75`);
  - it skips `templates/` (`:86-89`).
- `IncrementalSync` runs `DiscoverChanges` over the **whole-repository** diff (`:190`) and only
  `TrimPrefix`es the keys (`:206`, `:220`, `:234`). Three things follow:
  1. A file **outside** `<dir>/` keeps its full path as the key. So
     `initiatives/…/TASK-001.md` at the repo root overwrites the row of the governed
     `spine/initiatives/…/TASK-001.md`: its status, type, links and title.
  2. Workflow diffs are matched with `artifact.IsWorkflowPath` on the unstripped path (`:285`).
     Governed edits to `spine/workflows/*.yaml` are therefore ignored, while root
     `workflows/*.yaml` files are projected.
  3. `DiscoverChanges` never skips templates, so a changed `templates/*.md` is projected as an
     artifact by incremental sync only. SMP already works around this in its document index
     (`internal/customer/docindex/indexer.go`, comment above the template exclusion).
- This breaks the data-model guarantee that "Full rebuild produces a Projection Store identical to
  one that was incrementally maintained" (`architecture/data-model.md` §4.1).
- PoC: `TestPOC_IncrementalSyncArtifactsDirKeyCollision`
  (`poc/store-projection/poc_artifactsdir_test.go`).
- **Who is affected:** standalone deployments with `artifacts_dir` set. That includes the
  `init-repo` default `spine` (`service/cmd/spine/cmd_init_repo.go:27`).
- **SMP:** its production projection writer sets no `ArtifactsDir`
  (`cmd/customer-app/workers.go:484-505`), and provisioning never calls `WithArtifactsDir`.

### What this task must not break (SMP runs `adapters/projection` in-process)

1. **The two SMP call sites.**
   - The sync loop: `internal/customer/projectionwriter/worker.go:312-330`. It calls
     `NewService(…, nil, …)` with no event router, plus `WithArtifactsDir` only when configured,
     then `IncrementalSync` and `StartSyncLoop`.
   - The provisioning / INIT-020 backfill rebuild:
     `internal/customer/workspace/provision_steps_runtime.go:320-327` (`FullRebuild`).
2. **SMP's step profiles sit under `workflows/`.**
   - SMP embeds `workflows/steps/*.yaml` (`governed_sources.go:24`) and seeds it with the
     `workflows/` tree. These are runner step profiles, not workflow definitions.
   - `IsWorkflowPath` matches them, because it accepts any depth (`discovery.go:256-259`). Today
     they are projected as rows with an empty `status`.
   - All three **fail strict `Parse`**, on the unknown keys `environment` and `run` (verified).
3. **SMP's governed document index only runs while the projection is idle.** It reconciles only
   while `sync_state.status = 'idle'` (`internal/customer/docindex/indexer.go:229-238`). A
   projection stuck in `error` therefore stops it for the whole workspace.
4. **SMP pins a run to the row's commit.** `ProjectionWorkflowLoader` refuses a row whose
   `source_commit` differs from the run's pinned commit
   (`internal/customer/spinecore/workflow_loader.go:116-118`). Re-stamping a valid row strands
   in-flight runs.
5. **SMP reads the `definition` JSONB raw**, for example
   `internal/customer/spinecore/execution_actor_store.go:196-203` and
   `internal/customer/projection/projection.go:508-522`. The stored shape of valid workflows must
   not change.
6. **Design-check note (2026-10-06): SMP's INIT-020 workflow backfill rebuilds when a canonical
   row is missing.**
   - `WorkflowBackfill.runWorkspace` (`internal/customer/workspace/workflow_backfill.go:183-204`)
     calls `RebuildProjection`, a `FullRebuild`, whenever a canonical top-level
     `workflows/<name>.yaml` path has **no row**. The check is
     `ProjectionHasWorkflowPaths`, `provision_steps_runtime.go:228-253`, a `count(DISTINCT
     workflow_path)` over the paths.
   - The backfill runs once per leader acquisition: every boot and every leader change
     (`cmd/customer-app/workers.go:680-693`).
   - Today a strict-`Parse`-rejected file still produces a row, so the probe is satisfied.
   - Under this task, a canonical file can end up with no row:
     - when it is rejected with no verified previous row (`not_projected`);
     - when its unverifiable row is `removed` by the sweep.
   - That workspace then gets a `FullRebuild` on **every** boot. Every `FullRebuild` re-stamps the
     `source_commit` of each accepted row, and item 4 strands every in-flight run in the
     workspace each time.
   - §10 below therefore makes a **probe change in SMP TASK-012 mandatory**. Owner question 5
     covers the optional Spine-side alternative.

## Design

### 0. Invariants

- **I1 — one reading.** A workflow row is written only from a file that strict `workflow.Parse`
  accepts. The stored JSON must also decode, through `UnmarshalProjectionDefinition`, to exactly
  what `Parse` returned.
- **I2 — a content rejection is not a sync error.**
  - A rejected file is not projected. Its existing row is kept only if that row verifies (§1).
  - The rejection is logged and evented, and the sync advances.
- **I3 — an infrastructure error is a sync error.** A git read or store failure sets
  `sync_state = error`, never advances `last_synced_commit`, and is retried on the next tick.
- **I4 — one path classifier.** `FullRebuild` and `IncrementalSync` decide what is an artifact,
  what is a workflow and what the key is with one function. So `FullRebuild(c)` over an
  incrementally maintained store changes nothing but `source_commit`.
- **I5 — authoritative ref.** With an authoritative branch configured, every sync reads
  `refs/heads/<branch>`. With none, behaviour is exactly today's `Head()`.
- **I6 — what `main` shows is what new runs bind (Design-check note, 2026-10-06).**
  - A row whose file at `head` is rejected is never **bindable** for a new run, even when it is
    kept for in-flight runs (§2, owner question 4).
  - Without this, a member could do the following through two governed merges:
    1. land a visibly review-less but valid workflow;
    2. then land a body that *shows* the review step but fails strict `Parse` (for example an
       extra unknown key).
  - The projection would keep binding the review-less version while `main` displays the
    reviewed one. That is P3-13's impact ("reviewers and validators look at a different
    workflow than the engine runs"), reached without any parser differential.
  - Today that sequence fails closed by accident: the unknown key reaches the JSON, and every
    `DisallowUnknownFields` loader refuses the row.

### 1. `core/workflow`: one encoder shared by the projection and the write path

**New file `core/workflow/projection_definition.go`:**

```go
// ErrProjectionRoundTrip: Parse accepted the file, but the JSON the projection stores does
// not decode (through UnmarshalProjectionDefinition) to exactly the parsed definition.
var ErrProjectionRoundTrip = errors.New("workflow: projected definition does not round-trip to the parsed definition")

// ProjectionDefinition is the only way a workflow body becomes a projection row.
//  1. wf := Parse(path, content). It is strict: yamlsafe bounds, unknown, duplicate and
//     case-variant keys fail, and the yaml:"-" fields path/commit_sha are refused.
//  2. def := json.Marshal of yamlsafe.Decode(content) decoded into `any`. This is
//     byte-for-byte the encoder projectWorkflow uses today, so accepted files store
//     unchanged JSONB.
//  3. got := UnmarshalProjectionDefinition(def), normalised the way every loader normalises
//     it: Path = wf.Path, CommitSHA = "", and an empty Mode becomes "execution".
//     reflect.DeepEqual(got, wf) must hold. Otherwise wrap ErrProjectionRoundTrip, naming
//     the first differing top-level field when it can.
func ProjectionDefinition(path string, content []byte) (*domain.WorkflowDefinition, []byte, error)

// VerifyProjectionDefinition checks a definition that is already stored, without git.
//  1. It must decode through UnmarshalProjectionDefinition.
//  2. The same JSON is decoded into `any`, re-emitted with yaml.Marshal, and decoded with a
//     yaml.v3 Decoder with KnownFields(true). That decode is exact and case-sensitive.
//  3. Both results must be DeepEqual after the Mode normalisation.
// It rejects every stored row the folding reader and a strict reader would disagree on:
// `ſteps`, `Steps`, `next_ſtep`, `path`, `commit_sha`, numeric strings.
// The input is our own JSONB, so the yaml.v3 decoder is used directly; there is no yamlsafe
// byte cap, and JSON has no aliases.
func VerifyProjectionDefinition(def []byte) error
```

*(Corrected by the codex 6.1 catch-up, 2026-10-14. In step 2, "decoded into `any`" by
`json.Unmarshal` turns every number into a float64. An integer above 2^53, which
`ProjectionDefinition` stores exactly, then made a valid stored row fail verification. Step 2 now
decodes with `UseNumber`: an integer that fits an int64 stays exact, and any other number reads as
before. The sweep's `sameRow` (§6) compares definitions the same way. See "Codex 6.1 catch-up".)*

Verified with overlay probes on 2026-10-06:

- **`ProjectionDefinition`:**
  - all 15 shipped top-level workflow files pass and produce today's exact bytes;
  - the two PoC payloads, `Steps:`, duplicate `id`, and `path:` / `commit_sha:` fail `Parse`;
  - `version: 1.0`, `retry: {limit: 1.5}` and an integer step `name` fail decoding;
  - `version: 2026-10-06` decodes fine but is **not equal**. This is the case that needs the
    `DeepEqual`.
- **`VerifyProjectionDefinition`:**
  - accepts the stored JSON of all 15 shipped files;
  - rejects the stored JSON of both PoC payloads and of `path:` / `commit_sha:`.

> **Design-check note (2026-10-06): two content classes pass `Parse` but must still be rejected
> here, or they wedge the sync as "infrastructure".** Re-verified with an independent overlay
> probe (`tmp/init027-orchestration/spine-init023/design-check/task011/zz_probe_task011_test.go`).
>
> 1. **U+0000 in any string.** `name: "a\0b"`, or a `\0` inside `commit:` / `config:` map
>    values, passes `Parse` *and* the round trip.
>    - `json.Marshal` then emits `\u0000`, which PostgreSQL refuses in `jsonb` ("unsupported
>      Unicode escape sequence"). It also refuses 0x00 in the `text` columns `workflow_id`, `name`
>      and `version`.
>    - `UpsertWorkflowProjection` fails, I3 counts that as infrastructure, and
>      `IncrementalSync` stays in `error` forever. In SMP that also stops the document index for
>      the workspace (Purpose, must-not-break 3).
>    - **Required:** `ProjectionDefinition` rejects a definition with U+0000 in any key or string
>      value, wrapping `ErrProjectionRoundTrip`.
>    - Walk the decoded value. A byte search for `\u0000` in the JSON false-positives on a
>      literal backslash-`u0000` text.
>    - The write path (§7) inherits it.
> 2. **Encoder failures.** `commit: {1: x}` passes `Parse`, because yaml.v3 stringifies the key
>    for a `map[string]string`. The `any` decode, however, yields `map[interface{}]interface{}`,
>    and `json.Marshal` fails.
>    - **Required:** every failure after `Parse` (the `any` decode, `json.Marshal`, the
>      `UnmarshalProjectionDefinition` decode) wraps `ErrProjectionRoundTrip`. It is then reported
>      as `round_trip`, not `parse`, and is never surfaced as an infrastructure error.
>
> The same probe also confirms the design's corpus claims:
> - all 15 shipped top-level files pass and keep byte-identical JSON;
> - `VerifyProjectionDefinition` accepts all 15;
> - 10 of the 15 fail `Validate`.
>
> Across **every workflow-shaped string literal in both repositories' Go code** (55: tests,
> harness seeds and the `init-repo` seed, which is byte-identical to `workflows/workflow-lifecycle.yaml`),
> 52 are accepted, **0** are newly rejected by the round trip, and 3 are deliberate negative tests
> that `Parse` already rejects. `!!binary`-encoded duplicate keys are refused by `Parse` ("field
> steps already set"). Multi-document files read the first document in both readers, so they
> agree.

### 2. `adapters/projection`: building a workflow row and the rejection contract

**`Service` gains fields:**

- `scope pathScope` (§5);
- `authoritativeBranch string` (§4);
- `strictWorkflowValidation bool`;
- `workflowsVerified atomic.Bool` (§6).

`IncrementalSync` can run concurrently: `RegisterEventHandlers` subscribes it, and the sync loop
calls it too. That is why the flag is atomic.

**New exported error type:**

```go
// WorkflowRejection reports a workflow file the projection refused because of its content.
// It is never counted as a sync error.
type WorkflowRejection struct {
	Path   string // repo-relative workflow path
	Commit string // commit the file was read at
	Check  string // "parse" | "round_trip" | "validate" | "read_bounds" | "scope" | "absent_at_head"
	Err    error
}
func (r *WorkflowRejection) Error() string
func (r *WorkflowRejection) Unwrap() error
```

**`buildWorkflowRow(ctx, path, commit) (*store.WorkflowProjection, *WorkflowRejection, error)`**
replaces the body of `projectWorkflow` (`:500-553`):

1. **Read.** `content, err := s.git.ReadFile(ctx, commit, path)`. An error is returned as
   infrastructure.
   - One exception: if `isContentReadError(err)` reports true, it becomes a `read_bounds`
     rejection. That helper is a single classifier that returns false in this task.
   - Spine TASK-003 adds its oversize-blob sentinel there. Then an oversize workflow file is
     rejected rather than wedging the sync in `error`.
2. **Encode.** `wf, def, err := workflow.ProjectionDefinition(path, content)`. An error becomes a
   rejection, with check `round_trip` when `errors.Is(err, workflow.ErrProjectionRoundTrip)` and
   `parse` otherwise. yamlsafe bound errors surface here as `parse`.
3. **Validate.** `workflow.Validate(wf)` stays advisory.
   - On `failed`:
     - with `strictWorkflowValidation` set, it becomes a `validate` rejection;
     - otherwise it is logged at Info: `"workflow fails workflow.Validate; projected (projection
       validation is advisory)"`, with path, commit, the error count and the first message.
   - Info, not Warn, because 10 shipped files trip it on every rebuild. Warn is kept for
     actionable rejections.
4. **Row.** The typed columns `WorkflowID`, `Name`, `Version`, `Status` and `AppliesTo` come from
   `wf`, the parsed definition, instead of today's separate non-strict decode (`:515-529`). For
   every accepted file the values are identical. `Definition` is `def`, and `SourceCommit` is
   `commit`.

**`projectWorkflow(ctx, path, commit) error`** keeps its signature, as a thin wrapper. It returns
the infrastructure error, or the `*WorkflowRejection` as an error, or the result of the upsert.
The existing bounds tests (`workflow_bounds_test.go`) therefore stay valid unchanged: they assert
an error whose text contains "byte cap", "alias" or "depth", and no upsert.

**`syncWorkflowFile(ctx, path, head) error`** is used by `IncrementalSync` for an added, modified
or renamed-to workflow path:

1. Build the row.
   - An infrastructure error is returned, and the caller counts it into `syncErrors`.
   - An accepted row is upserted.
2. For a rejection, run `prev, err := s.store.GetWorkflowProjection(ctx, path)`.
   - A `domain.ErrNotFound` `SpineError` means `prev = nil`. Any other error is infrastructure.
   - Then report one of three actions:
     - **`not_projected`**: `prev == nil`.
     - **`kept`**: `workflow.VerifyProjectionDefinition(prev.Definition) == nil`. The row is
       **not touched**, so `source_commit` keeps pinned runs valid. The event names
       `kept_source_commit`.
     - **`removed`**: otherwise. `DeleteWorkflowProjection` runs, and its error is
       infrastructure.
3. Report the rejection (§8) and count it for the `projection_synced` payload. Return nil.

> **Design-check note (2026-10-06): a `kept` row is quarantined, not left bindable (I6, owner
> question 4).**
> - **What is kept:** the row's `definition`, typed columns and `source_commit` stay exactly as
>   they were.
> - **What changes:** its **`status` column only** is upserted to the marker **`Quarantined`**.
> - **Every bindability read filters `status = 'Active'`.** So the row drops out of binding for
>   new runs in both repositories:
>   - Spine `ListActiveWorkflowProjections` (`adapters/store/postgres_projections.go:280-284`);
>   - SMP `ProjectionWorkflowProvider` (`workflow_provider.go:71-75`);
>   - the SMP UI list (`projection.go:577-579`);
>   - the skill-reference guards.
> - **Every in-flight-run read is path- or id-keyed with no status filter.** So runs pinned to
>   the kept `source_commit` continue:
>   - Spine scheduler recovery/timeout (`recovery.go:457`, `timeout.go:124`);
>   - the gateway assign fallback (`step_assign_fallback.go:104`);
>   - SMP `ProjectionWorkflowLoader` (`workflow_loader.go:103-118`);
>   - SMP `GetStepMode` / `GetRunStepMode`;
>   - SMP `QueryRunnerStepExecutions`.
> - The `definition` JSON keeps its authored `"status"`. Nothing reads bindability from it.
> - A later accepted edit upserts a normal row, which restores `Active` (or the authored status).
> - The same rule applies wherever this design says "keep": `FullRebuild` §2 step 4, and the
>   sweep §6.
> - This also keeps the path present for SMP's backfill probe (Purpose, must-not-break 6), so
>   only `not_projected` / `removed` can trigger it.
> - The `projection_workflow_rejected` payload gains `"quarantined": true` on `kept`.
> - **Upgrade caveat:** a workspace whose valid pre-upgrade row backs a file that `head` now
>   rejects, for example a working `version: 2026-10-06` file, stops binding new runs of that
>   workflow until the file is fixed. Today it keeps working with a mangled version label. The
>   release note names the Warn line to search for.

**`FullRebuild`** applies the same rule across its delete-all window (`:78`):

1. Resolve `head` (§4) and mark `rebuilding`, as today.
2. **Move discovery before the wipe.**
   - Run `artifact.DiscoverAll` (`:87`) and the new workflow path list (§5) *before*
     `DeleteAllProjections`.
   - Build every workflow row first. For each rejection, capture `prev` (as above) and keep it
     only if `VerifyProjectionDefinition` passes.
   - Today a git failure in discovery happens *after* the wipe and leaves an empty projection.
     After this change it happens before.
3. Run `DeleteAllProjections`, then project artifacts as today.
4. Upsert the accepted workflow rows. **Re-upsert each verified `prev` unchanged**, which keeps
   its own `source_commit`. Report every rejection (`kept`, or `not_projected` when no verified
   prev exists).
5. **Counting.** Workflow infrastructure errors count into `projErrors` as today. Rejections do
   not. Branch protection is unchanged (`:121-127`).
6. **The verification flag.** `workflowsVerified` is stored as true only when no workflow
   infrastructure error occurred.

### 3. `IncrementalSync`: one `Diff`, every error counted (P3-14)

1. **Resolve the head.** `head, err := s.resolveHead(ctx)` (§4). On error, call
   `s.markSyncError(ctx, state, "resolve sync target: …")` and return the error.
   - `markSyncError` writes `{LastSyncedCommit: state.LastSyncedCommit, Status: "error",
     ErrorDetail}` and joins any `UpdateSyncState` error.
   - Today the state is left untouched, which can keep reporting `idle`.
2. **Sweep first.** If `!s.workflowsVerified.Load()`, run the sweep (§6) **before** the
   early-return at `:180`. On error, call `markSyncError` and return; the next tick retries. On
   success, store true.
3. **Early return, unchanged:** `head == LastSyncedCommit && Status == "idle"` returns nil.
4. Mark the state `syncing`, as today.
5. **One `Diff`.**
   - `diffs, err := s.git.Diff(ctx, state.LastSyncedCommit, head)`.
   - On error: `markSyncError(ctx, state, fmt.Sprintf("diff %s..%s: %v", short(from), short(head), err))`
     and return a wrapped error. `last_synced_commit` is unchanged.
   - The status is now `error`, so the next tick retries even if `head` has not moved, and
     `bpDirty` (`:265`) forces a branch-protection re-projection on that retry.
6. **Artifacts from the same diff.**
   - `changeset, err := artifact.DiscoverChangesFromDiffs(ctx, s.git, from, head, s.scope.artifactDiffs(diffs))`.
   - **New in `core/artifact/discovery.go`:** `DiscoverChangesFromDiffs` is the current body of
     `DiscoverChanges` after its `Diff` call. `DiscoverChanges` becomes `Diff` plus that function,
     so its behaviour is unchanged for `core/engine/branch_validation.go:46`.
   - An error goes to `markSyncError` and returns.

   > **Design-check note (2026-10-06): the swallowed artifact `ReadFile` errors were orphaned, and
   > this task now owns them.**
   > - Spine TASK-003 says they "stay TASK-011's" (its §11 item 3 and its Decisions). This task's
   >   Out-of-scope list said they were "candidates for Spine TASK-003". Nobody owned them.
   > - They are the P3-14 class (a dropped error → a silently skipped change → the sync advances)
   >   on the artifact side.
   > - A blanket "propagate" is unsafe. A gitlink named `x.md` reads as `fatal: bad object
   >   HEAD:x.md` (verified), which `classifyGitError` (`adapters/git/errors.go:43-61`) reports as
   >   `ErrKindPermanent`. Any member could then wedge the sync with one plantable entry.
   >
   > **Narrow, wedge-safe rule:**
   > - **`DiscoverChangesFromDiffs` records** every `ReadFile` failure it currently `continue`s
   >   past in a new **additive** field, `ChangeSet.ReadErrors []DiscoveryError` (path, ref,
   >   error).
   >   - The record carries the error itself, not only its text, so the class can be tested.
   >   - `DiscoverChanges` callers that ignore the field (branch validation) are unchanged.
   >   - TASK-003's own additive `ChangeSet` field sits beside it.
   > - **`IncrementalSync` treats a recorded read error as infrastructure** (`markSyncError`, not
   >   advanced) only when one of these holds:
   >   - it is **not** a `*git.GitError` (exec or spawn failure, for example `EAGAIN` under load);
   >   - it is `ErrKindTransient`;
   >   - `ctx.Err() != nil`.
   > - **Every other read error keeps today's skip**, but is logged at Warn (path, ref, kind) and
   >   counted as `"read_skipped": n` in `projection_synced`.
   > - TASK-003 then adds its `ErrBlobTooLarge` refusal on top. Its §11 item 3 already rebases
   >   on this task.
7. **Artifact keys.** The created, modified and deleted paths are keyed with
   `s.scope.artifactKey(path)`. This replaces the `TrimPrefix` at `:206`, `:220` and `:234`.
   Every path in the changeset is already in scope, so the key always resolves.
8. **Branch protection is unchanged.** It is still detected over the raw `diffs` at the fixed
   repo-root path (`:265-282`).
9. **Workflows over the raw `diffs`, classified with `s.scope.isWorkflow` (§5):**
   - **added / modified:** `syncWorkflowFile`.
   - **deleted:** `DeleteWorkflowProjection`, with errors counted as today.
   - **renamed:**
     - if the old path is a workflow, delete its row, **counting the error** (fixes `:301`);
     - if the new path is a workflow, run `syncWorkflowFile`.
10. **Finish.** The state update is unchanged (`:312-328`). The `projection_synced` payload
    (`:345-352`) gains `"workflows_rejected": n`.

### 4. Authoritative ref (P3-22)

```go
// WithAuthoritativeBranch makes every sync read refs/heads/<branch> instead of the working
// tree's HEAD.
//   - "" restores the default, Head(). That is right for bare repositories, whose HEAD
//     cannot be moved by a push.
//   - The branch is a short name. It goes through refname.Validate
//     (adapters/git/refname/refname.go), and a "refs/" prefix is refused.
func (s *Service) WithAuthoritativeBranch(branch string) error

// AuthoritativeBranch returns the configured short branch name, or "".
// Spine TASK-006's projection guard defaults to it.
func (s *Service) AuthoritativeBranch() string

func (s *Service) resolveHead(ctx context.Context) (string, error) {
	if s.authoritativeBranch == "" {
		return s.git.Head(ctx) // unchanged: SMP and every caller that sets nothing
	}
	ref := "refs/heads/" + s.authoritativeBranch
	sha, err := s.git.RefSHA(ctx, ref) // adapters/git/cli.go:554-560
	if err != nil {
		return "", fmt.Errorf("resolve authoritative ref %s: %w", ref, err)
	}
	if !isHexSHA(sha) { // 40 or 64 lowercase hex characters
		return "", fmt.Errorf("resolve authoritative ref %s: unexpected output %q", ref, sha)
	}
	return sha, nil
}
```

**Why the fully qualified ref is safe.** `git rev-parse refs/heads/main` resolves
`$GIT_DIR/refs/heads/main` first, so a pushed `refs/tags/refs/heads/main` cannot shadow it (F10's
DWIM problem needs a *bare* name). Spine TASK-002's ref grammar, which adds `--verify
--end-of-options` and qualified refs, stays compatible.

**What it touches.** Both `FullRebuild` and `IncrementalSync` call `resolveHead`, and nothing
else in the package reads `Head()` for syncing. `QueryService.CheckFreshness` (`query.go:187`)
still uses `Head()`. It has no production caller and is listed as a follow-up.

**Exported constant.** `core/engine` exports `const AuthoritativeBranch = authoritativeBranch`,
an alias of `merge.go:23`, so the wiring cannot drift from the merge target.

**Standalone wiring.** Each site calls `WithAuthoritativeBranch(engine.AuthoritativeBranch)` and
fails startup on error:

- `service/cmd/spine/serve_wiring.go:287-288` (single-workspace `serve`);
- `service/internal/workspace/pool.go:955-956` (every per-workspace `ServiceSet`; this file gains
  the `core/engine` import);
- `service/scenariotest/harness/runtime.go:116`, because harness repositories are created on
  `main`.

**Self-healing on upgrade.** A standalone store whose `last_synced_commit` is a run-branch SHA,
left by the bug, heals on its own. The next sync diffs from that SHA to `main` and applies the
difference.

**A new failure mode.**

- A repository whose `main` is unborn reports `sync_state = error` with "resolve authoritative
  ref refs/heads/main" until `main` exists. This is a fresh `init-repo` before the `spine/init`
  PR merges.
- Today that repository projects `spine/init`. The engine cannot merge into an unborn `main`
  either, so the remedy is the documented PR merge.

> **Design-check note (2026-10-06), P3-22.**
> 1. **The shadowing claim was verified (git 2.54).**
>    - Setup: `refs/tags/refs/heads/main`, `refs/refs/heads/main`, `refs/heads/refs/heads/main`
>      and `refs/remotes/refs/heads/main` all point at another commit.
>    - `git rev-parse refs/heads/main` still prints the real `main`. The "refname is ambiguous"
>      warning goes to stderr, and `CLIClient.run` returns stdout only (`adapters/git/cli.go:675-686`),
>      so `isHexSHA` is unaffected.
>    - A tracked working-tree file named `refs/heads/main`, or `HEAD`, does not make `rev-parse`
>      ambiguous either.
>    - An unborn `main` exits 128 (and echoes the ref name on stdout), which `run` surfaces as an
>      error.
>    - AC 17 gains the shadow-ref case.
> 2. **`WithAuthoritativeBranch("")` must short-circuit before `refname.Validate`.** That function
>    rejects `""` (`adapters/git/refname/refname.go:31-34`).
> 3. **The "self-healing on upgrade" claim holds only while the stamped run-branch commit still
>    exists.**
>    - The commit can disappear: a cancelled run's branch is deleted, and `git gc` later prunes
>      the commit.
>    - `Diff` then fails with `bad object`. §3 marks the sync `error` every tick (today it
>      stalls in `syncing`), until `POST /api/v1/system/rebuild`.
>    - The release note gives the check `git cat-file -e <last_synced_commit>^{commit}` and the
>      rebuild remedy.
> 4. **Standalone repositories whose governed branch is not `main` go to `error` after the
>    upgrade.** An example is a `master` repo initialised with `init-repo --no-branch`. Today they
>    project `HEAD`.
>    - The engine already cannot merge there (`core/engine/merge.go:23`), so nothing that works
>      end to end regresses.
>    - **Required:** the wiring sites log one Warn at startup when `refs/heads/main` does not
>      resolve, and the release note says so.

### 5. One path scope for both entry points (P3-23), and top-level workflow files only

**New unexported type in `adapters/projection/scope.go`:**

```go
type pathScope struct{ dir, prefix string } // dir "" for the repo root; prefix = dir + "/"

// newPathScope normalises like config.normalizePath: "", ".", "./" and "/" mean the root,
// "./" is trimmed and a trailing "/" is trimmed.
func newPathScope(artifactsDir string) pathScope

// artifactKey mirrors DiscoverAll (discovery.go:59-89) exactly.
//   - Outside <dir>/ gives "", false. The prefix is stripped.
//   - Not ".md", or under templates/ (artifact.IsTemplatePath), gives "", false.
func (p pathScope) artifactKey(repoPath string) (string, bool)

// isWorkflow is true for a DIRECT child of workflows/ ending ".yaml" or ".yml" (no further
// "/"). When dir is set, a direct child of <dir>/workflows/ also counts (owner question 3).
func (p pathScope) isWorkflow(repoPath string) bool

// artifactDiffs keeps the diffs whose path is an artifact candidate in scope.
// A rename that crosses the scope boundary becomes:
//   - "added" (new path)   when only the new path is in scope;
//   - "deleted" (old path) when only the old path is in scope.
func (p pathScope) artifactDiffs(diffs []git.FileDiff) []git.FileDiff

// WorkflowDirs returns the repo-relative directories whose direct *.yaml|*.yml children the
// projection treats as workflows: ["workflows/"], plus "<dir>/workflows/" when artifacts_dir
// is a subdirectory. Spine TASK-006 uses it to build its governed path set.
func WorkflowDirs(artifactsDir string) []string
```

**Wiring.**

- `WithArtifactsDir` (`:51-53`) also sets `s.scope = newPathScope(dir)`.
- `FullRebuild` passes `s.scope.dir` to `DiscoverAll`.
- `FullRebuild`'s workflow list is `result.Workflows` filtered by `isWorkflow`. When `dir` is set,
  it adds `artifact.DiscoverWorkflows(ctx, s.git, head)` (the repo root, `discovery.go:272-307`),
  also filtered by `isWorkflow`. The result is de-duplicated and sorted.
- `IncrementalSync` uses the same predicates (§3).

**Effects:**

- **Artifacts:** both paths see exactly `<dir>/**.md` minus `templates/`, keyed by the stripped
  path. The collision and the incremental template projection are gone.
- **Workflows:**
  - Both paths see the same set: root `workflows/*.y(a)ml`, which is where
    `workflow.Service` (`core/workflow/service.go:18`, `:313`) and `StartWorkflowPlanningRun`
    (`core/engine/run.go:302`) write regardless of `artifacts_dir`.
  - When `dir` is set, `<dir>/workflows/*.y(a)ml` is included too. That is where `init-repo`
    seeds (`initrepo.go:63-92`) and where `FullRebuild` projects today. Rows stay keyed by the
    full repository path, as today.
- **Nested files under `workflows/` are no longer workflows**, for example SMP's
  `workflows/steps/*.yaml`.
  - The documented location is `/workflows/<workflow-id>.yaml`
    (`architecture/workflow-definition-format.md:39`), and `workflow.Service` can only write a
    top-level file: its ID pattern excludes `/` (`service.go:23`).
  - Today these files become empty-status rows. Under strict `Parse` they would become a
    rejection on every rebuild in every SMP workspace.
  - `artifact.IsWorkflowPath` itself is **not** changed. It stays the classifier used by
    `DiscoverAll` / `DiscoverWorkflows` and by Spine TASK-006.
- **`.spine/branch-protection.yaml`** stays at its fixed repo-root path in both paths (`:25`).

> **Design-check note (2026-10-06), P3-23.**
> 1. **`artifactDiffs` must apply `artifactKey`'s whole predicate to both `OldPath` and `Path`.**
>    The predicate is: in scope, ends in `.md`, not under `templates/`. A rename across the
>    `templates/` boundary inside `<dir>/` must therefore convert to `added` or `deleted` exactly
>    like a rename across the `<dir>/` boundary. Otherwise `DiscoverChangesFromDiffs` reads a
>    template as an artifact, or keeps a stale row.
> 2. **The PoC's third observation stays true by design.** With `artifacts_dir: spine`, a root
>    `workflows/rogue.yaml` is projected Active.
>    - Under owner question 3's default, root `workflows/` is a governed workflow location
>      (it is where `workflow.Service` writes), and both entry points now project it.
>    - AC 19 asserts this explicitly, so the decision is visible rather than masked by the PoC
>      failing on its first assertion.
>    - Consequence: the same `id` under both directions, for example `workflows/task-default.yaml`
>      and `spine/workflows/task-default.yaml`, both `Active` with the same `applies_to`, makes
>      binding return `ErrConflict` for every run of that type (`core/workflow/binding.go:65-72`).
>    - That is today's behaviour after any incremental sync, and the reason Spine TASK-006 must
>      guard **both** directories (§11).

### 6. Sweep of Active workflow rows, once per `Service` instance

Without a sweep, the fix would leave in place every row projected before it, including a smuggled
review-less workflow. Only a later edit of that file, or a `FullRebuild`, would replace it. A
`FullRebuild` re-stamps every `source_commit`, which strands SMP's pinned runs (§Purpose, "must
not break" 4).

```go
func (s *Service) reconcileActiveWorkflows(ctx context.Context, head string) error
```

1. `rows := s.store.ListActiveWorkflowProjections(ctx)` and
   `atHead := set(s.git.ListFiles(ctx, head, ""))`. These are one query and one `ls-tree`. Errors
   are returned.
2. Then, for each row `r`:
   - **Out of scope** (`!s.scope.isWorkflow(r.WorkflowPath)`, for example an Active nested or
     out-of-tree row): delete it and report `scope` / `removed`.
   - **Absent at `head`** (a missed deletion; P3-14 could cause one): delete it and report
     `absent_at_head` / `removed`.
   - **Present at `head`:** build `head`'s row with `buildWorkflowRow`.
     - **Accepted:** if `sameRow(cand, r)` holds, the row is **untouched** (no re-stamp).
       `sameRow` compares ID, Name, Version and Status, plus `AppliesTo` and `Definition` as
       decoded JSON (`any` plus `DeepEqual`; integers kept exact since the codex 6.1 catch-up).
       JSONB normalises key order and spacing. If it does
       not hold, upsert `cand`; the row was stale.
     - **Rejected:** if `VerifyProjectionDefinition(r.Definition) == nil`, keep the row untouched
       and report `kept`. Otherwise delete it and report `removed`. A row smuggled before the
       upgrade lands here and is removed.
3. Any delete or upsert error is returned, so the flag stays false and the next tick retries.

**Cost.** This is one query, one `ls-tree` and one `ReadFile` per Active workflow (about 8 per
workspace), once per instance.

- **Standalone:** once per process per workspace.
- **SMP:** once per warm-workspace sync loop (`runSyncer`) and once per provisioning instance.
- A `FullRebuild` satisfies it (§2).

Non-Active rows are not swept. They are not bindable, and a `FullRebuild` re-derives them.

> **Design-check note (2026-10-06): sweep every row, not only Active rows, and run the sweep
> single-flight.**
> 1. **"Not bindable" is not "not read".** In-flight runs load their pinned workflow **by path or
>    `workflow_id` with no status filter**:
>    - Spine `recovery.go:457`, `timeout.go:124` and `step_assign_fallback.go:104`;
>    - SMP `workflow_loader.go:103-108`, `projection.go:478-525` and
>      `execution_actor_store.go:196`.
>
>    So a pre-upgrade smuggled row that was later set `Deprecated`, with its hidden key kept,
>    survives an Active-only sweep. It keeps steering runs pinned to it.
>
>    **Required:**
>    - The sweep lists **all** rows through an optional capability, asserted on the store:
>      `interface{ ListWorkflowProjections(ctx) ([]store.WorkflowProjection, error) }`.
>      `*store.PostgresStore` implements it (`SELECT … FROM projection.workflows`, no status
>      filter).
>    - Do **not** add the method to `store.Store`. Spine has full-interface test stubs that would
>      all need it: `adapters/delivery/subscriber_test.go:406`,
>      `service/cmd/spine/stubstore_test.go:212`, `service/internal/gateway/stubstore_test.go:192`.
>    - SMP embeds `store.Store` (`execution_actor_store.go:41`) and constructs Spine's
>      `PostgresStore` (`projectionwriter/worker.go:313`), so it gets the capability with no SMP
>      change.
>    - Stores without the capability fall back to `ListActiveWorkflowProjections`.
>    - **Out-of-scope rows that are not Active** are deleted and logged at **Info**, with no event
>      or Warn: the three empty-status `workflows/steps/*.yaml` rows in every SMP workspace.
>      Nothing reads them: step profiles come from git (`gitreader.go:281`,
>      `cmd/runner/job_execute.go:480`), and no `projection.workflows` query selects them.
>    - An out-of-scope **Active** row keeps the Warn and event.
>    - The cost is unchanged in order: one query, one `ls-tree`, and one `ReadFile` per in-scope
>      row.
> 2. **Single-flight.**
>    - The sweep runs under a per-`Service` `sync.Mutex`, with the flag double-checked inside it.
>    - Every `IncrementalSync` on the instance that finds the flag false waits for that mutex.
>    - The event-handler and sync-loop `IncrementalSync`s (§2) therefore cannot interleave
>      sweep writes with their own.
>    - Without this, a slower sweep at an older `head` can re-upsert a stale row *after* a
>      concurrent sync projected the newer one, while `sync_state` says `idle@newer`. The row is
>      then stale until that file next changes.
>    - Cross-instance concurrency stays out of scope, as before: SMP's provisioning `FullRebuild`
>      next to the warm-workspace loop.
>      *(Corrected by the wave 2 cross-task review: not "as before". Before this task every path
>      that deleted or rewrote a row from a stale head also wrote `sync_state`, so a cross-instance
>      race healed on the next tick; the sweep followed by the up-to-date early return did not, and
>      could lose a row another instance projected at a newer head for good. A sweep that writes
>      now re-records `sync_state` on that path — see "Cross-task review (wave 2)". Since the
>      codex 6.1 catch-up it moves only `last_synced_commit` and keeps the status another
>      instance wrote — see "Codex 6.1 catch-up".)*

### 7. Write-path parity

Each of these call sites switches from `workflow.Parse` to `workflow.ProjectionDefinition`. That
way an author gets the rejection at create time instead of a silent kept row.

- **`core/workflow/service.go:125` (`writeAndCommit`).** It serves `Create`/`Update` in both
  repositories, because SMP's `WorkflowWriter` wraps `workflow.Service`
  (`internal/customer/spinecore/workflowwriter.go:343`).
  - A parse or round-trip failure returns `domain.ErrValidationFailed` with the error text, the
    same code a parse failure returns today.
  - Nothing is written or committed.
- **`core/workflow/service.go:275` (`ValidateBody`).**
  - A round-trip failure maps to one error with `RuleID: "projection_round_trip"`.
  - A parse failure keeps `RuleID: "parse"`.
- **`core/engine/run.go:304` (`StartWorkflowPlanningRun`).** It returns `ErrInvalidParams
  "invalid workflow body: …"`, as for a parse failure today.

`Validate` stays where it is (`service.go:133`, `run.go:312`). This keeps the INIT-023 rule:
workflow-content checks are enforced at create and update, never at projection or load.

### 8. Reporting: log, event, documentation

**`reportWorkflowRejection(ctx, rej, action, keptCommit)`:**

1. **Log at Warn**: `"projection: workflow file rejected"`, with `workflow_path`, `commit`,
   `check`, `action`, `kept_source_commit` and `error`.
   - The `error` field has control bytes replaced, is limited to its first line, and is capped
     at 512 bytes. YAML error text echoes attacker-chosen keys.
2. **Emit** through `event.EmitLogged`, which is a no-op for a nil router: `domain.Event{Type:
   domain.EventProjectionWorkflowRejected, ArtifactPath: path, EventID: "wfrej-" +
   hex(sha256(path, commit, action))[:16], Payload}`.
   - The payload is `{workflow_path, commit, check, action, kept_source_commit, error}`.
   - The ID is deterministic, so the repeated report from a sweep at the next instance start
     de-duplicates.
3. **New operational event type.**
   - `EventProjectionWorkflowRejected EventType = "projection_workflow_rejected"` goes in
     `core/domain/event.go`, in the operational block (`:32-78`).
   - It is added to `allEventTypes()` (`adapters/delivery/subscriber.go:137-172`).
4. **Documentation.**
   - `architecture/event-schemas.md`:
     - a new §4 subsection for the payload;
     - an appendix row ("`projection_workflow_rejected` | `projection.Service` |
       projection/service.go");
     - `workflows_rejected` added to `projection_synced`.
   - `architecture/data-model.md` §4.1/§4.2: one scope, the rejection rule, and that the
     equivalence guarantee holds for all columns except `source_commit`.
   - `architecture/git-integration.md` §2.2: standalone pins `refs/heads/<authoritative>`.
     Bare-repository deployments use `HEAD`.
   - `architecture/workflow-definition-format.md`:
     - only top-level files are definitions;
     - files are strict-parsed at projection;
     - quote date-like and numeric strings.

### 9. Data, format, migration, backward compatibility

**Data and format.**

- No runtime or projection schema change, and no migration.
- The `definition` JSONB of every accepted file is **byte-identical** to today's. Same encoder,
  and AC 3 asserts it on both corpora.
- The `sync_state.status` values are unchanged (`idle`, `syncing`, `rebuilding`, `error`).

**Behaviour changes:**

1. **Content rejections** (`Parse`, round-trip, or `Validate` when strict) are no longer
   projected:
   - the last verified row is kept;
   - an Active row that cannot be verified is removed by the sweep or on the next change.
   - Today a yamlsafe failure counts as a sync error and wedges `IncrementalSync` in `error`.
     After this task it is a rejection, which is the I2 decision (D2).
2. **A `Diff` failure** sets `error` instead of leaving `syncing`, or instead of advancing to
   `idle` (`:254`).
3. **Nested `workflows/**` files** are no longer projected. Existing empty-status rows remain
   until the next `FullRebuild`. They are not Active and are not read by anything.
4. **Standalone with `artifacts_dir` set:**
   - out-of-tree `.md` and `templates/` stop being projected incrementally;
   - `<dir>/workflows/*` edits are projected incrementally;
   - root `workflows/*` is projected by `FullRebuild` too.
   - Rows already corrupted by the collision stay until a rebuild. The release note tells
     operators of such deployments to run `POST /api/v1/system/rebuild` once
     (`service/internal/gateway/routes.go:58`).
5. **Standalone:** see §4 for the projection reading `refs/heads/main`, including the unborn-main
   error.
6. **Create and update** of a workflow body that round-trips differently now fail with
   `validation_failed`, for example `version: 1.0` or `version: 2026-10-06`. Today such a body
   breaks loading (`version: 1.0`) or silently diverges (the date).
7. **Design-check additions (2026-10-06):**
   - **Kept rows are quarantined.** Their `status` column becomes `Quarantined` (§2 note, I6), so
     new runs of a workflow whose file at `head` is rejected fail with "no active workflow" until
     the file is fixed. In-flight runs continue.
   - **The sweep also visits non-Active rows.** It deletes SMP's nested `workflows/steps/*`
     rows on the first sync after deploy (§6 note).
   - **Bodies with U+0000, and non-string map keys, are rejections.** Today they are sync errors
     that wedge (§1 note).
   - **Transient artifact read failures now set `error` instead of being skipped.** Deterministic
     read failures stay skipped but are logged and counted (§3 note).

### 10. SMP impact and what the dependent SMP task must change

**No SMP code change is needed for the bump to compile or behave correctly.**

- **P3-22:** zero value, so SMP keeps `Head()`.
- **P3-23:** SMP sets no `ArtifactsDir`.
  - The only SMP-visible scope changes are that incremental sync stops projecting `templates/`,
    and that nested `workflows/steps/*.yaml` stop being projected.
  - SMP's docindex already excludes templates.
  - Nothing reads the step-profile rows: step profiles are read from git
    (`internal/customer/spinecore/gitreader.go:281`).
- **P3-14:** a transient `Diff` failure now shows as one `error` tick. Docindex skips that one
  tick (it gates on `idle`, which is correct).
- **P3-13:**
  - SMP's 7 top-level seeded workflows are accepted, with unchanged bytes.
  - A customer-modified workflow that fails strict `Parse` or the round trip is not projected.
    A previously verified row is kept.
  - Owner question 1 decides the strict default.

**Rejections appear in SMP only as Warn logs.** Both SMP call sites pass a nil `EventRouter`.

**SMP TASK-012 (the `.spine-revision` bump) must do the following for this task:**

1. **Keep the validate mirror exact.** `embeddedWorkflowValidate`
   (`internal/customer/spineembedded/workflows.go:317-331`) documents that it "mirrors exactly"
   `ValidateBody`.
   - Switch its `workflow.Parse` (`:319`) to `workflow.ProjectionDefinition`, with the same
     `projection_round_trip` rule ID.
   - Extend `workflows_validate_embedded_test.go` with a `version: 2026-10-06` body.
2. **Add an SMP corpus test** (no DB). Every top-level `.yaml` in `governedsources.Workflows`
   passes `workflow.ProjectionDefinition`, and every `workflows/steps/*.yaml` is
   `!isWorkflow`-equivalent: not a direct child of `workflows/`.
3. **Extend `TestWorker_Integration_ProjectsSeededRepo`**
   (`internal/customer/projectionwriter/worker_test.go`) to assert:
   - the first sync is `idle`, as today;
   - there are exactly 7 rows `LIKE 'workflows/%'`;
   - there are 0 rows `LIKE 'workflows/steps/%'`.
4. **Release note:** the first sync after deploy runs the sweep. Search the logs for "projection:
   workflow file rejected" per workspace.
5. **Optional, not required for the bump:** wire an event router into the projection writer so
   rejections reach SMP's event pipeline and admin UI. This is a candidate SMP follow-up.

> **Design-check note (2026-10-06): the claim "no SMP code change is needed for the bump to …
> behave correctly" is wrong in one place.** It is still true that the bump compiles and that
> AC 24 needs no SMP edit.
>
> **6. Mandatory: the backfill probe** (Purpose, must-not-break 6).
> - **The change:** `ProjectionHasWorkflowPaths` (or `WorkflowBackfill.runWorkspace` before
>   calling it) must not count as missing a canonical path whose file at the repository's `HEAD`
>   is present but rejected by `workflow.ProjectionDefinition`. Simplest: read each missing path
>   from the bare repo and exclude the rejected ones from the probe. Log them at Warn.
> - **Why:** otherwise such a workspace gets a `FullRebuild` on every leader acquisition. Each
>   one re-stamps `source_commit` and strands every in-flight run (`workflow_loader.go:116-118`).
> - **Test:** a workspace whose `workflows/task-default.yaml` at `HEAD` carries `ſteps:` and has
>   no row → two backfill passes → **zero** `RebuildProjection` calls. A genuinely missing valid
>   canonical file still triggers exactly one.
>
> **7. Item 3's test must also assert the quarantine behaviour.** After committing a rejected
> `task-default.yaml` over the seeded one:
> - the row's `status` is `Quarantined` and `source_commit` is unchanged;
> - `ProjectionWorkflowProvider.ListActiveWorkflows` does not return it;
> - `ProjectionWorkflowLoader.LoadWorkflow(path, <old commit>)` still returns the old definition.
>
> **8. Release note additions:**
> - the three nested `workflows/steps/*` rows per workspace are deleted on the first sync;
> - quarantined workflows show as non-Active in the admin UI's workflow list until fixed.

### 11. Coordination with sibling tasks

- **Spine TASK-006 (downstream, hard dependency on this task).**
  - It gets three things from here:
    - the single counted `Diff` and error state (§3);
    - `head` as the authoritative ref (§4), with `AuthoritativeBranch()` for its guard default;
    - `WorkflowDirs(artifactsDir)` (§5).
  - **Its `DefaultGovernancePaths` (`.spine/`, `.spine.yaml`, `workflows/`) must also include
    `<artifacts_dir>/workflows/` when `artifacts_dir` is a subdirectory.** Otherwise a governed
    merge can change a projected workflow without the guard.
  - Its projection guard returns errors from `projectBranchProtection`, which this task leaves as
    sync errors.
- **Spine TASK-003.** It adds its oversize-blob sentinel to `isContentReadError` (§2 step 1), so
  an oversize workflow is a `read_bounds` rejection, not an error loop. It also decides whether
  `DiscoverChangesFromDiffs` should stop swallowing artifact `ReadFile` errors (out of scope
  here). Its NUL-delimited `Diff` changes only `FileDiff` production, which this task consumes
  unchanged.
- **Spine TASK-002.** `RefSHA` may gain `--verify --end-of-options`. §4 passes a fully qualified
  ref, so it is compatible in either order.
- **Spine TASK-012.** It serialises the standalone working tree. That is complementary: §4 keeps
  the projection off the checked-out branch even while a run branch is checked out legitimately.
- **Spine TASK-014.** An update-capable workflow planning run must validate its body with
  `workflow.ProjectionDefinition`, as §7 does for `StartWorkflowPlanningRun`.
- **Spine TASK-015 and TASK-017.** Their content checks belong at create and update, next to §7.
  None of them is added to projection.

> **Design-check note (2026-10-06): Spine TASK-003 contradicts this task and must be amended
> before it rebases.**
> - **The conflict.** TASK-003's invariant I4 says an oversized workflow "stays a sync error with
>   the previous projection retained, exactly as today". This task's D2 makes every workflow
>   content failure, including the 64 KiB `yamlsafe` cap, a **rejection that advances the sync**.
> - **The missing hook.** TASK-003 never mentions `isContentReadError`. Its `ErrBlobTooLarge`
>   (over 4 MiB by default) would therefore reach §2 step 1 as **infrastructure**, and wedge the
>   sync permanently. That is exactly the error loop D2 exists to prevent.
> - **The amendments TASK-003 needs:**
>   - I4 reworded: a workflow over either limit is TASK-011's `parse` / `read_bounds` rejection;
>     `.spine/branch-protection.yaml` stays a sync error, because `projectBranchProtection` is
>     unchanged here;
>   - one line registering `errors.Is(err, git.ErrBlobTooLarge)` in `isContentReadError`;
>   - an AC: a 5 MiB `workflows/x.yaml` ends `idle` with a `read_bounds` rejection and the
>     previous row quarantined (owner question 4) or `not_projected`.
> - **Ownership.** The swallowed artifact read errors are now owned **here** (§3 note). TASK-003's
>   §11 item 3 and its Decisions entry already point here, so they need no change.
> - **Shared files.** Both tasks add an operational event to `allEventTypes()` and to
>   `architecture/event-schemas.md`. Whichever lands second rebases.

## Decisions

- **D1. Detect the differential at projection and keep today's JSON shape.**
  - *Rejected: store `json.Marshal(parsed)`.* That would change the stored shape of every
    workflow (`mode`, `path`, `commit_sha`, null fields). At least four SMP readers parse the
    `definition` JSONB raw (§Purpose, "must not break" 5).
  - *Rejected: make the loaders case-sensitive.* Six loaders across two repositories would each
    need the change. A load-time refusal also drops a workflow at run time instead of at sync, and
    the INIT-023 rule keeps content checks out of load.
- **D2. A content rejection is not a sync error.**
  - *Rejected: count it, which is today's behaviour for yamlsafe failures.* A file that never
    changes would keep `IncrementalSync` in `error` forever. The diff window would never advance
    and would grow every tick, and SMP's document index would stop for the whole workspace
    (`indexer.go:229-238`).
  - *Rejected: count it in `FullRebuild` only.* The next incremental tick heals it to `idle` at
    the same commit anyway (`:180-182`), so it adds a blip, not a signal.
  - The durable signals are the Warn log and the event.
- **D3. Keep the last verified row on rejection, including across `FullRebuild`.**
  - *Rejected: delete the row.* One bad edit would disable a working workflow and strand
    in-flight runs pinned to its `source_commit`.
  - Keeping only a row that passes `VerifyProjectionDefinition` means a kept row can never be a
    smuggled one.
  - **Design-check note (2026-10-06).** "Never a smuggled row" is necessary but not sufficient.
    A kept row that stays bindable lets `main` show one workflow while new runs bind an older one
    (I6).
    - D3 is therefore narrowed: keep the row **for in-flight runs only**, quarantined (§2 note).
    - This still avoids D3's stated cost: runs pinned to the old `source_commit` keep loading.
    - It costs new runs of that workflow, which fail loudly until `main` holds a valid body. That
      is a DoS a `main`-landing attacker already has today, by deleting the file or setting
      `status: Deprecated`.
    - The designer's "kept and bindable" variant is owner question 4 alternative (a).
- **D4 amendment (Design-check note, 2026-10-06).** The sweep visits every row, not only Active
  ones, and runs single-flight per instance (§6 note). The "not bindable" rationale for skipping
  non-Active rows does not hold, because in-flight runs read rows by path or id regardless of
  status.
- **D4. Sweep Active rows once per instance.**
  - *Rejected: tell operators to run `FullRebuild` after the upgrade.* That re-stamps every
    `source_commit` (pinned runs) and leaves the hole open until someone does it.
  - *Rejected: verify rows on every tick.* The fixed sync paths cannot create unverifiable rows,
    so the only sources are pre-upgrade rows and missed deletions.
- **D5. Only direct children of `workflows/` are definitions.**
  - *Rejected: keep `IsWorkflowPath`'s any-depth rule and reject nested files one by one.* That
    would produce three rejections on every rebuild in every SMP workspace, for files that are not
    workflows.
  - It also matches the documented location and the only shape `workflow.Service` can write.
  - Changing `IsWorkflowPath` itself is avoided, because other callers and TASK-006 use it.
- **D6. One `Diff` per sync.**
  - *Rejected: keep two calls and check the second error.* It is correct but duplicates work, and
    the artifact changeset and the governance loop could in principle see different diffs.
- **D7. The authoritative branch is opt-in, and the zero value is `Head()`.**
  - *Rejected: always resolve `refs/heads/main`.* SMP's bare repositories already follow
    `HEAD → refs/heads/<default>`, and an imported SMP repository keeps the remote's default
    branch (`internal/customer/workspace/git_repository_allocator.go:41-42`). Hard-coding `main`
    could break those repositories and adds nothing for them.
- **D8. Validate stays advisory at projection, with an opt-in strict flag. Its failures log at
  Info.** 10 of 15 shipped files fail it today (§Purpose, P3-13.7).
- **D9. Write-path parity (§7).** It costs one call per site and means the projection never
  rejects a body that the API accepted.
- **D10. A dedicated `projection_workflow_rejected` event.**
  - *Rejected: reuse `projection_synced`.* It is emitted only by `IncrementalSync` and carries
    counts, not paths.

## Owner questions

1. **How strict is projection for customer-modified workflows?** This is INIT-023 owner question 7.
   - **Default: strict.** A file that fails strict `Parse` or the round trip is never projected.
     The last verified row is kept. An Active row that cannot be verified, for example one with a
     `Steps:` key, is removed by the sweep, so runs pinned to it stop at their next load.
   - **Alternative (a):** project `Parse`-accepted, round-trip-failing files with the canonical
     encoding `json.Marshal(parsed)` instead of rejecting them. This is safe, because `Parse`
     already saw every key exactly, but those rows get a different JSON shape.
   - **Alternative (b):** log-only for one release. This is not recommended, because it leaves
     the review-removal path open for that release.
2. **When should the strict `Validate` flag be turned on?**
   - **Default: off in both repositories.** Turn it on only after the 10 shipped workflow files
     pass `Validate` (a follow-up). Until then, enabling it would reject `task-default`,
     `epic-lifecycle` and others.
3. **Which workflow directories are canonical when `artifacts_dir` is a subdirectory?**
   - **Default: both** root `workflows/` (where `workflow.Service` and planning runs write) and
     `<dir>/workflows/` (where `init-repo` seeds), applied identically by both entry points. This
     regresses nothing that works today.
   - **Alternative:** only `<dir>/workflows/`. That first needs `workflow.Service` and
     `StartWorkflowPlanningRun` to honour `artifacts_dir`. Otherwise API-created workflows in such
     deployments would stop being projected.
   - *Design-check note (2026-10-06).* Under the default, a root `workflows/rogue.yaml` stays
     projected Active. This is the audit PoC's third observation, kept on purpose and asserted by
     AC 19. Its integrity depends on Spine TASK-006 guarding both directories.
4. **Design-check addition (2026-10-06): is a row kept on rejection still bindable?**
   - **Design-check default: quarantined (I6, §2 note).** The definition and `source_commit` are
     kept for in-flight runs. The `status` column is set to `Quarantined`, so no new run binds it
     until `main` holds a valid body.
     - This closes the two-commit stealth downgrade: a valid review-less version, then a
       reviewed-looking body that is rejected.
     - Upgrade caveat: a working but round-trip-failing file stops binding (§2 note).
   - **Alternative (a), the designer's original:** kept and bindable. This favours availability:
     one bad raw-git edit never blocks new runs. But it opens the stealth downgrade, and the only
     signals are a Warn log (SMP passes a nil event router) and the event (standalone).
   - **Alternative (b):** remove the row on any rejection. This fails closed for in-flight runs
     too (D3's original objection).
5. **Design-check addition (2026-10-06, optional): should `FullRebuild` preserve `source_commit`
   for unchanged rows?**
   - The mechanism: capture each row before the wipe, and re-use its `source_commit` when
     `sameRow(candidate, previous)` (§6).
   - What it fixes: SMP's stranding of in-flight runs on every `FullRebuild`, including today's
     INIT-020 backfill-add path, would disappear at the source. SMP TASK-012's probe change
     (§10 note, item 6) would then be defence in depth only.
   - It is safe in standalone: `GitWorkflowLoader` reads the file at the older commit, and
     `sameRow` guarantees the same parsed definition.
   - **Default: not in this task.** The SMP probe change is the required fix, and this is a
     candidate follow-up.

## Acceptance Criteria

All test names are proposals. Paths are in the Spine repository.

**Each new regression test must fail when run against `61994fe`'s `adapters/projection` and
`core/workflow`, and pass after the fix.** Record the before and after output in Implementation
Notes. The audit PoCs become the tests in ACs 4, 12, 15 and 19.

**Core encoder (`core/workflow/projection_definition_test.go`)**

1. **`ProjectionDefinition` refuses the smuggling class with a parse error.** The refused inputs:
   - `pocFoldWorkflow` (`ſteps`) and `pocFoldOutcome` (`next_ſtep`), copied verbatim from the
     audit PoC;
   - `Steps:` alone;
   - duplicate `id`;
   - top-level `path:` and `commit_sha:`.

   No error wraps `ErrProjectionRoundTrip`.
2. **It refuses round-trip failures with `ErrProjectionRoundTrip`.** The inputs are
   `version: 1.0`, `retry: {limit: 1.5}`, an integer step `name`, and **`version: 2026-10-06`**.
   The last one decodes without error, so this AC fails if the `DeepEqual` is replaced by "decode
   succeeded".
   - *Design-check amendment (2026-10-06).* Add three inputs that `Parse` accepts. Each must also
     fail with `ErrProjectionRoundTrip`:
     - `name: "a\0b"`;
     - a step outcome `commit: {status: "x\0"}`;
     - a step outcome `commit: {1: x}` (`json.Marshal` fails today).

     A body containing the literal text `\u0000`, as six characters inside a description, is
     **accepted**. That guards against a byte-search implementation.
3. **Shipped corpus, Spine.** For every `workflows/*.yaml` in the Spine repository (8 files),
   `ProjectionDefinition` succeeds. The returned JSON is **byte-identical** to
   `json.Marshal(<yamlsafe.Decode(content) decoded into any>)`, which is today's encoder.
   `VerifyProjectionDefinition` accepts it.
4. **`VerifyProjectionDefinition` rejects stored rows a strict reader disagrees with.** It
   rejects the JSON produced by `61994fe`'s encoder from each AC 1 input that encodes, and the
   JSON of the `version: 1.0` file.

**Projection, P3-13 (`adapters/projection/workflow_integrity_test.go`, real git and a map store)**

5. **Incremental rejection keeps the verified row.** This is the inverted
   `TestPOC_ProjectionWorkflowFoldedKeyDifferential`.
   - **Setup:** commit `c1` holds a valid `workflows/task-default.yaml` with a `review` step, and
     a `FullRebuild` runs. Commit `c2` replaces the file with `pocFoldWorkflow`, and
     `IncrementalSync` runs.
   - **Expected:**
     - the row is unchanged: its decoded steps still include `review` and `source_commit == c1`;
     - `sync_state` is `idle@c2`;
     - exactly one `projection_workflow_rejected` event names `workflows/task-default.yaml`, with
       `check: parse`, `action: kept` and `kept_source_commit: c1`.
   - The same holds for `pocFoldOutcome`, where the decoded `failed` outcome still routes to
     `validate`.
   - *Design-check amendment (2026-10-06, owner question 4 default).* Also assert:
     - the kept row's `status` column is `Quarantined`;
     - `ListActiveWorkflowProjections` does not return it;
     - `GetWorkflowProjection("workflows/task-default.yaml")` still returns the `c1` definition;
     - the event payload carries `"quarantined": true`.

     Under alternative (a), the status stays `Active` instead.
6. **`FullRebuild` honours the same rule.**
   - **On the AC 5 store at `c2`:** the row is restored with `source_commit == c1`, and the state
     is `idle`.
   - **On an empty store at `c2`:** there is no row, the event has `action: not_projected`, and
     the state is `idle`.
   - *Design-check amendment (2026-10-06).* The restored row is `Quarantined`, as in AC 5.
7. **Removal of an unverifiable row.** A store holds a row whose definition is `61994fe`'s
   encoding of `pocFoldWorkflow`, at `c1`. When `c2` changes the file to another rejected body:
   - the row is deleted;
   - the event has `action: removed`.
8. **Validate is advisory.** A file that `Parse`s and round-trips but fails `Validate`, such as a
   `status: Draft` workflow, is projected. With `WithStrictWorkflowValidation(true)` it is
   rejected with `check: validate` and not upserted.
9. **Rejection vs infrastructure error.** A rejection never sets `error`. A `ReadFile` failure on a
   changed workflow sets `error` and does not advance `last_synced_commit`, which is today's
   behaviour, kept.
   - This AC fails if rejections are counted into `syncErrors`.
10. **The upgrade sweep.** Each case runs at a `head` already equal to `last_synced_commit` with
    status `idle`. That is the early-return path, so this AC fails if the sweep sits after it.
    - (a) An Active smuggled row, with the smuggled file still at `head`: removed on the first
      `IncrementalSync`.
    - (b) The same row, with a valid file at `head`: replaced by `head`'s row.
    - (c) An Active row whose file is absent at `head`: removed (`absent_at_head`).
    - (d) An Active nested `workflows/steps/x.yaml` row: removed (`scope`).
    - (e) A valid row equal to `head`'s content at an older `source_commit`: **no upsert call**,
      and `source_commit` unchanged.
    - (f) A second `IncrementalSync` on the same instance makes no `ListActiveWorkflowProjections`
      call.
    - *Design-check amendments (2026-10-06):*
      - **(d)** applies to the **Active** nested row. A non-Active, empty-status nested row
        (SMP's shape) is also deleted, with an Info log and **no** event or Warn.
      - **(g)** A `Deprecated` row holding `61994fe`'s encoding of `pocFoldWorkflow` is removed by
        the sweep, using the store's all-rows capability. With a store lacking the capability,
        the sweep falls back to Active rows and the test documents that the row survives.
      - **(h)** Two `IncrementalSync` calls started concurrently on a fresh instance make exactly
        **one** all-rows (or Active) listing call. `go test -race` is clean.
      - **(i)** A valid row whose file at `head` is rejected is quarantined, not untouched
        (owner question 4).
11. **Nested files are not workflows.** A repository with `workflows/task-default.yaml` and
    `workflows/steps/validate.yaml` (SMP's file, as test data) is checked through `FullRebuild`
    and then an `IncrementalSync` that modifies both files.
    - Only the top-level row exists.
    - No rejection is reported for the nested file.

**Projection, P3-14 (`adapters/projection/sync_errors_test.go`)**

12. **A `Diff` failure is an error and is retried.** This is the inverted
    `TestPOC_IncrementalSyncDropsSecondDiffError`. A fake git fails the **first** `Diff` call of
    the first sync. It changes `.spine/branch-protection.yaml` and `workflows/task-default.yaml`.
    - **The first `IncrementalSync`:**
      - returns an error;
      - leaves `sync_state = {error, last_synced_commit unchanged}`, with `error_detail`
        containing `diff`;
      - makes no branch-protection or workflow upsert.
    - **The second call, with `Diff` healthy:** re-projects branch protection, projects the
      workflow and ends `idle@head`.
    - *Design-check amendment (2026-10-06).* Isolate this AC from the sweep: the store holds no
      rows, or the instance has already swept. Otherwise the sweep's own upserts would make "no
      workflow upsert" untestable. The fake git must implement `ListFiles` and `ReadFile`, which
      the sweep calls.
13. **One `Diff` per sync.** The fake git counts exactly one `Diff` call per `IncrementalSync` that
    does work. This catches a reintroduced second call.
14. **Rename errors are counted.** A workflow rename whose old-row `DeleteWorkflowProjection`
    fails ends in `error` and does not advance.

**Projection, P3-22 (`adapters/projection/authoritative_ref_test.go`)**

15. **The run-branch checkout is ignored.** This is the inverted
    `TestPOC_ProjectionFollowsCheckedOutRunBranch`. With `WithAuthoritativeBranch("main")`:
    - a sync while `spine/run/r1` (carrying `rules: []`) is checked out keeps 1 rule;
    - `last_synced_commit == rev-parse refs/heads/main`.
    - *Design-check amendment (2026-10-06).* Run this for **both** entry points: `IncrementalSync`,
      and `FullRebuild` (the `POST /system/rebuild` path).
16. **Control: the zero value follows `HEAD`.** This documents SMP's bare-repository behaviour.
    The same scenario without the option still installs 0 rules. That proves the option, not the
    test setup, provides the protection.
17. **Argument validation and unborn `main`.**
    - `WithAuthoritativeBranch` returns an error for ` main` (whitespace), `-x`,
      `refs/heads/main`, `a..b` and a control byte. `""` is accepted and restores the `Head()`
      default.
    - In a repository with unborn `main`, `IncrementalSync` returns an error naming
      `refs/heads/main` and sets `sync_state = error`.
    - *Design-check amendment (2026-10-06).* Add the shadow-ref case. With `refs/tags/refs/heads/main`,
      `refs/refs/heads/main` and `refs/heads/refs/heads/main` all pointing at a commit that carries
      `rules: []`, a sync still projects `refs/heads/main`'s rule. A tracked working-tree file
      named `refs/heads/main` does not break resolution either.
18. **Standalone wiring is covered.**
    - A test over `workspace.Pool`'s `ServiceSet` asserts `ProjSync.AuthoritativeBranch() ==
      engine.AuthoritativeBranch`.
    - The same assertion holds for `serve_wiring`.
    - `make test-scenario` passes with the harness option set.
    - *Design-check amendment (2026-10-06).* Wiring over a repository with no `refs/heads/main`
      logs exactly one startup Warn naming `refs/heads/main` (§4 note, item 4) and does not fail
      startup.

**Projection, P3-23 (`adapters/projection/scope_test.go`)**

19. **Artifacts and workflows are scoped.** This is the inverted
    `TestPOC_IncrementalSyncArtifactsDirKeyCollision`. With `WithArtifactsDir("spine")`:
    - the governed `initiatives/…/TASK-001.md` row keeps `status: Pending` and its title after the
      out-of-tree file is committed;
    - `spine/workflows/task.yaml` becomes `Deprecated`, version 2;
    - a `spine/templates/x.md` with `type: Task` front matter is not projected.
    - *Design-check amendment (2026-10-06):*
      - the PoC's root `workflows/rogue.yaml` **is** projected Active by both `IncrementalSync`
        and `FullRebuild` (owner question 3 default; flips with the alternative);
      - a rename `spine/templates/t.md → spine/initiatives/…/TASK-002.md` projects TASK-002 as
        created;
      - the reverse rename deletes the TASK-002 row.
20. **Equivalence.** A scripted history of at least 8 commits is run for `artifacts_dir` `""`
    and `"spine"`. It covers:
    - adds, modifies and deletes;
    - renames across the scope boundary in both directions;
    - template files;
    - nested and root workflow files;
    - a rejected workflow;
    - branch-protection edits.

    After each commit, a clone of the incrementally maintained store is run through `FullRebuild`.
    It equals the incremental store on every artifact column (except `source_commit`), every
    workflow column (except `source_commit` of accepted rows) and the branch-protection rows.
    This AC fails if any scope filter is removed from either entry point.
    - *Design-check amendment (2026-10-06):*
      - Also exclude `source_commit` on branch-protection rows: incremental sync stamps the
        commit that changed the file, while rebuild stamps `head`.
      - Exclude `synced_at`.
      - Exclude the execution projection's `blocked` / `blocked_by`. They depend on processing
        order in both entry points (`resolveBlockingStatus`, `service.go:467-488`), which is
        pre-existing; alternatively, keep `blocked_by` links out of the scripted history.
      - Compare `artifact_links` rows as sets.

**Corpus, events, write path**

21. **Shipped corpus through the projection.** `FullRebuild` over a repository holding Spine's
    `workflows/*.yaml` projects 8 rows, reports zero rejections, and ends `idle`.
22. **The event type is registered.** `domain.EventProjectionWorkflowRejected` is in
    `allEventTypes()`. No test covers that list today, so a new delivery test asserts it.
    `architecture/event-schemas.md` documents the payload.
23. **The write path matches the projection.**
    - `workflow.Service.Create` and `Update` reject `version: 1.0` and `version: 2026-10-06`
      bodies with `ErrValidationFailed`, and no commit is created.
    - `ValidateBody` returns `failed` with `RuleID: "projection_round_trip"`.
    - `StartWorkflowPlanningRun` returns `ErrInvalidParams`.

**Gates**

24. **SMP compatibility gate, run locally.** With `../spine` checked out at this task's head, the
    SMP repository passes:
    - `go build ./...` and `go vet ./...`;
    - `go test ./internal/customer/projectionwriter/... ./internal/customer/workspace/...
      ./internal/customer/spinecore/... ./internal/customer/spineembedded/...
      ./internal/customer/docindex/... ./internal/docmemory/...`. This includes the DB-backed
      `TestWorker_Integration_ProjectsSeededRepo` (still `idle`, `task-default` present) and
      `TestWorker_Integration_SurfacesMalformedArtifact` (still `error`).
    - No SMP edits are allowed. The commands and results go in Implementation Notes.
25. **SMP seeded corpus, run locally.** A `go test -overlay` test (no files written into SMP) runs
    `FullRebuild` over a temporary repository seeded from SMP's `governedsources` tree. The tree
    includes `workflows/steps/*.yaml`. The result:
    - `idle`;
    - exactly 7 workflow rows;
    - 0 rows under `workflows/steps/`;
    - 0 rejections;
    - every `definition` byte-identical to `61994fe`'s projection of the same file.
26. **Spine gates and review.**
    - `go build ./...`, `go vet ./...` and `go test ./...` are green.
    - `go test -race ./adapters/projection/... ./core/workflow/... ./core/artifact/...` is green.
    - `go test -tags integration ./adapters/projection/...` is green against the test DB.
    - `make test-scenario` is green.
    - Lint is at the existing baseline, including the ADR-017 boundary lint.
    - `codex review` leaves no open P1 or P2.
    - The four original audit PoCs, compiled with `-overlay` against the merged tree, now fail
      their vulnerability assertions. The PR body records this.
    - *Design-check amendment (2026-10-06): the per-PoC expected outcome against the merged tree.*
      All five PoC functions reproduce at `61994fe` (re-run 2026-10-06).
      - **`…FoldedKeyDifferential` and `…FoldedOutcomeOverride`:** fail at `projectWorkflow`,
        which now returns the rejection.
      - **`…ArtifactsDirKeyCollision`:** fails "collision not reproduced". Its third observation
        stays true by design (AC 19).
      - **`…DropsSecondDiffError`:** its fakes embed nil `store.Store` and nil `git.GitClient`, so
        the new pre-early-return sweep panics.
        - The single `Diff` now succeeds, so the workflow diff is processed and reaches
          `ReadFile`, which the fake does not implement either.
        - Run it with a three-method shim:
          - `ListActiveWorkflowProjections` returns nil;
          - `ListFiles` returns nil;
          - `ReadFile` returns an error.
        - It must then fail "not reproduced": branch protection is re-projected once and the state
          is `error`, not advanced. The PR records the shim.
      - **`…FollowsCheckedOutRunBranch`:** **still reproduces**, because it constructs
        `NewService` without the option and the zero value is `Head()` (D7). The closure is
        ACs 15, 16 and 18, not this PoC.

**Design-check additions (2026-10-06)**

27. **No stealth downgrade through a kept row** (I6, owner question 4).
    - **File:** `adapters/projection/workflow_integrity_test.go`.
    - **Setup:**
      - `c1`: a valid `workflows/task-default.yaml` **without** a review step;
      - `FullRebuild`;
      - `c2`: the same file **with** a review step plus one unknown top-level key `x: 1`;
      - `IncrementalSync`.
    - **Expected:**
      - the row keeps `c1`'s definition and `source_commit`, with `status = Quarantined`;
      - `workflow.ResolveBinding` over a `ProjectionWorkflowProvider` built on the store returns
        `ErrWorkflowNotFound` for `Task`;
      - `GetWorkflowProjection` still returns `c1`'s definition for a run pinned to `c1`;
      - one `projection_workflow_rejected` event, with `action: kept` and `quarantined: true`;
      - `sync_state` is `idle@c2`.
    - **Then** `c3` fixes the file. The row becomes `c3`'s definition and `Active`.
    - This AC fails under alternative (a). That is intended: if the owner picks (a), this AC is
      rewritten to assert binding to `c1`, and the stealth path is recorded as accepted.
28. **Artifact read errors are classified, never silently dropped** (§3 note).
    - **File:** `adapters/projection/sync_errors_test.go`.
    - **Case 1.** A fake git whose `ReadFile` returns a non-`*git.GitError` for a modified
      `initiatives/…/TASK-001.md` at `head`:
      - `IncrementalSync` returns an error;
      - `sync_state = {error, last_synced_commit unchanged}`;
      - the next call with a healthy `ReadFile` projects the change.
    - **Case 2.** The same with an `ErrKindTransient` error behaves the same way.
    - **Case 3.** A real repository with a gitlink entry named `x.md` (`git update-index
      --cacheinfo 160000,<sha>,x.md`):
      - the sync ends `idle@head`;
      - one Warn names `x.md`;
      - `projection_synced` carries `read_skipped: 1`.
    - **Branch validation is unchanged:** `ValidateBranch` over the same gitlink repository
      returns the same result as at `61994fe` (the field is ignored).
29. **Content that PostgreSQL cannot store is a rejection, not a wedge** (§1 note).
    - **Integration test**, `-tags integration`, real PostgreSQL:
      - `c1` holds a valid `workflows/task-default.yaml`; `FullRebuild` runs.
      - `c2` sets its `name` to `"a\0b"`; `IncrementalSync` runs.
    - **Expected:**
      - it ends `idle@c2`;
      - the row keeps `c1` (quarantined);
      - one rejection with `check: round_trip` is reported.
    - **Before the fix,** the same test ends in `error` with the commit not advanced. Record both
      runs in Implementation Notes.

## Out of scope / follow-ups

- **Artifact read failures in discovery.** `DiscoverChanges` / `DiscoverChangesFromDiffs` still
  swallow artifact `ReadFile` errors (`discovery.go:142-208`). Artifact content errors also count
  as `FullRebuild` errors, while incremental sync drops them or turns them into deletes. Both are
  pre-existing artifact-side inconsistencies. They are not governance config, and they are
  candidates for Spine TASK-003, which changes the `ReadFile` error contract.
  - **Design-check note (2026-10-06): superseded for the read errors.** They are now **in
    scope** (§3 note, AC 28), because TASK-003 hands them back here.
  - Still out of scope: the artifact *parse* asymmetry (`FullRebuild` counts a parse error,
    while incremental turns a modified one into a delete).
- **Design-check follow-ups (2026-10-06), observed during the check and not in the audit:**
  - **`workflow_id` is not unique across paths.** SMP's `GetStepMode`, `GetRunStepMode` and
    `GetWorkflow` select by `workflow_id` with no status filter (`projection.go:478-525`, `:563`).
    So a second file declaring an existing `id`, for example a `Deprecated`
    `workflows/x.yaml` with `id: task-default`, makes them pick an arbitrary row and step mode.
    Candidate SMP follow-up: key by the run's `workflow_path`.
  - **The U+0000 wedge (§1 note) very likely applies to artifacts too.** Artifact content and
    front matter go into `text` / `jsonb` columns, and a NUL byte would keep `IncrementalSync` in
    `error`. Candidate for Spine TASK-003's "refused" classes, or a follow-up.
- **Shipped workflows that fail `Validate`.** Making the 10 files pass `Validate` in both
  repositories is a prerequisite for owner question 2. That is a separate task in each repository.
- **`workflow.Service` and `StartWorkflowPlanningRun` ignore `artifacts_dir`.** Both write root
  `workflows/`. Fixing them is a prerequisite for the narrow answer to owner question 3.
- **`QueryService.CheckFreshness`** (`adapters/projection/query.go:187`) still compares against
  `Head()`. It has no production caller. Align it with `resolveHead` if a caller is added.
- **Non-Active workflow rows are not swept.** They are not bindable, and a `FullRebuild`
  re-derives them.
- **Concurrent `IncrementalSync` calls** (sync loop plus event handlers, and SMP replicas) are not
  serialised here. Every write is a path-keyed upsert or delete, and they converge on the same git
  state, as today.
- **Multi-document workflow files.** `Parse` and the projection both read only the first YAML
  document, so the two agree. Refusing trailing documents is a possible extra guard and is not
  added.
- **Surfacing rejections in SMP** beyond logs (an event router in the projection writer, plus
  admin UI) is a candidate SMP follow-up.
- **Rows corrupted by P3-23 before the upgrade** in standalone `artifacts_dir` deployments are
  fixed by one `POST /api/v1/system/rebuild` (release note), not automatically.

## Design check

Adversarial design check, 2026-10-06, against Spine `61994fe` and SMP `80995559`, read-only.

**Evidence:**
- All five audit PoC functions were re-run with `-overlay`; all reproduce.
- An independent re-implementation of `ProjectionDefinition` and `VerifyProjectionDefinition`
  was probed against attack bodies, the 15 shipped files, and all 55 workflow-shaped Go string
  literals in both repositories.
- A git 2.54 scratch repository tested ref shadowing and gitlink reads.
- The probe sources and overlays are in `tmp/init027-orchestration/spine-init023/design-check/task011/`.

**What held:**
- Every `file:line` reference that a design claim rests on was opened, about 60 across both
  repositories, including the SMP bare-repo and imported-`HEAD` allocator lines. All are accurate
  to within a line.
- The encoder closes the folded-key, case-variant, duplicate-key (including `!!binary` keys) and
  `path` / `commit_sha` classes for every reader, because accepted rows carry exact keys only.
- The corpus and byte-identity claims reproduce.
- The fully qualified ref is not shadowable.
- The production wiring list (`serve_wiring`, `pool`) is complete.
- `RefSHA` is already on `git.GitClient`, so there is no interface change.
- `DeleteWorkflowProjection` is a plain `Exec`, so deleting a missing row is not an error.
- No existing test fixture in either repository is newly rejected.

**What changed, and why:**

| # | Change | Why |
|---|--------|-----|
| 1 | I6 and §2 note: a row kept on rejection is **quarantined** (status `Quarantined`), not left bindable. D3 narrowed. Owner question 4. New AC 27; ACs 5, 6 and 10(i) amended. | The designer's "kept and bindable" lets a member make `main` show a reviewed workflow while new runs bind an older review-less one. That is P3-13's impact without a parser differential, and an integrity regression against today's accidental fail-closed. |
| 2 | §6 note: the sweep visits **all** rows through an optional store capability, and runs **single-flight** per instance. D4 amended. ACs 10(d), (g) and (h). | In-flight-run loaders read rows by path or id regardless of status, so a `Deprecated` smuggled row survived an Active-only sweep. A concurrent sweep could also regress a row after a newer sync. |
| 3 | §1 note: U+0000 and post-`Parse` encoder failures become `round_trip` rejections. AC 2 amended; new AC 29. | Both pass `Parse`. PostgreSQL refuses the first; the second fails `json.Marshal`. Either way the sync stayed in `error` forever, which in SMP also stops the document index. |
| 4 | Purpose must-not-break 6 and §10 note: SMP TASK-012 **must** change the INIT-020 backfill probe. Optional Spine alternative is owner question 5. | A canonical workflow with no row triggers a `FullRebuild` on every leader acquisition. Each re-stamps `source_commit`, stranding every in-flight SMP run. "No SMP change needed" was wrong. |
| 5 | §3 note: this task now **owns** the swallowed artifact `ReadFile` errors, with a narrow, wedge-safe classification. New AC 28. | It was orphaned: TASK-003 handed it here, and this task handed it to TASK-003. A blanket propagate would let a planted gitlink wedge the sync, which was verified. |
| 6 | §11 note: **Spine TASK-003 must be amended** (its I4, and the `isContentReadError` hook). | TASK-003 keeps an oversized workflow as a sync error, which contradicts D2. Without the hook, its `ErrBlobTooLarge` would wedge the sync as infrastructure. |
| 7 | §4 note: shadow-ref and unborn verification recorded; `""` short-circuits `refname.Validate`; self-heal caveat; startup Warn for a missing `main`. ACs 15, 17 and 18 amended. | The design claimed self-healing unconditionally, and repositories whose governed branch is not `main` regress silently. |
| 8 | §5 note: `artifactDiffs` applies the full key predicate to both paths; the root `workflows/` part of the PoC stays true by design. AC 19 amended. | A rename across the `templates/` boundary was unspecified. AC 26 would otherwise imply the PoC's third observation was closed. |
| 9 | Purpose P3-13 note: the reader inventory is completed, and the standalone impact is narrowed. | Three readers were missing; one of them (SQL) is exact-match. Standalone step progression re-parses from git. |
| 10 | ACs 12, 20 and 26 tightened. | AC 12 interacted with the sweep. AC 20 would fail on pre-existing order-dependent fields. Two original PoCs would panic or still reproduce, not "fail their assertions". |

**Remaining owner questions:** 1–3 (designer's), 4 (quarantine versus bindable), 5 (optional:
`FullRebuild` preserves `source_commit`).

## Implementation Notes

*(Implementation, 2026-10-07. Branch `init/INIT-023-TASK-011-projection-sync-fail-closed`, cut from
Spine local `main` `ee3c249` (`61994fe` + the INIT-023 scaffold + the merged TASK-001). The
orchestrator named the branch. Run on macOS, git 2.54.0, go 1.26.8, against a shared native
PostgreSQL 17.10 + pgvector; CI uses 18. Every database used was named `s011_*` and dropped at the
end.)*

### Orchestrator rulings and owner-question defaults (recorded, pending the owner)

- **Scope:** audit P3-13 (narrowed to the parser differential), P3-14, P3-22, P3-23. P3-22's
  enforcement is opt-in (`WithAuthoritativeBranch`; zero value `Head()`); `spine serve` turns it on.
- **OQ1 (= INIT-023 OQ7) — default taken: strict.** A file failing strict `Parse` or the round
  trip is never projected; the last verified row is kept.
- **OQ2 — default taken:** `WithStrictWorkflowValidation` exists and is **off** in both repositories
  until a corpus-repair follow-up per repository. Verified fact used: all 15 shipped workflow files
  `Parse` and round-trip equal; 10 fail full `Validate`.
- **OQ3 — default taken:** root `workflows/` and `<artifacts_dir>/workflows/` are both canonical,
  applied identically by both entry points.
- **OQ4 — default taken: QUARANTINED.** The ruling made it conditional on the status column
  accepting the value without a schema change. Checked: `projection.workflows.status` is
  `text NOT NULL` with no CHECK constraint, enum or domain in Spine `migrations/001_initial_schema.sql`
  (and no later migration touches it) and in SMP's vendored copy
  `internal/customer/workspace/spinemigrations/001_initial_schema.sql`. Proven on PostgreSQL by
  `TestIntegration_NULWorkflowIsRejectionNotWedge` and `TestListWorkflowProjections_AllStatuses`
  (a `Quarantined` row round-trips). So the quarantine was implemented; nothing was stopped.
- **OQ5 — default taken:** `FullRebuild` `source_commit` preservation is a recorded follow-up, not
  done here.

### What was built

| Commit | Change |
|---|---|
| `0d676d3` | `core/workflow/projection_definition.go` (new): `ProjectionDefinition`, `VerifyProjectionDefinition`, `ErrProjectionRoundTrip` (§1, both design-check classes: U+0000 walked on the decoded value; every post-`Parse` failure wraps the sentinel). Write-path parity (§7): `workflow.Service` Create/Update, `ValidateBody` (new `ProjectionRoundTripRuleID = "projection_round_trip"`), `StartWorkflowPlanningRun` |
| `b780511` | `core/artifact`: `DiscoverChangesFromDiffs`; `DiscoverChanges` = `Diff` + it. Every skipped `ReadFile` failure recorded in additive `ChangeSet.ReadErrors`; `DiscoveryError` gains additive `Ref` and `Err` |
| `fa4e2dc` | `domain.EventProjectionWorkflowRejected`, `domain.WorkflowStatusQuarantined`, `allEventTypes()` registration (+ the first test of that list), `(*store.PostgresStore).ListWorkflowProjections` (optional capability, not on `store.Store`) |
| `857df75` | `adapters/projection`: `scope.go` (one classifier, `WorkflowDirs`), `authoritative.go` (`WithAuthoritativeBranch`, `AuthoritativeBranch`, `ProbeAuthoritativeRef`, `resolveHead`), `workflow_rows.go` (`WorkflowRejection`, `buildWorkflowRow`, `syncWorkflowFile`, quarantine, `reportWorkflowRejection`, `isContentReadError` hook, `WithStrictWorkflowValidation`), `sweep.go` (single-flight all-rows sweep, `sameWorkflowRow`), `service.go` (`FullRebuild` plans before the wipe, one-`Diff` `IncrementalSync`, `markSyncError`, read-error classification). The regression tests |
| `d5fd6c1` | `engine.AuthoritativeBranch`; wiring in `serve_wiring.go`, `workspace/pool.go`, `scenariotest/harness/runtime.go`; wiring tests |
| `82564de` | Docs: `event-schemas.md` (§4.13 `projection_synced`, §4.14 `projection_workflow_rejected`, emission map), `data-model.md` §4.1/§4.2, `git-integration.md` §2.2, `workflow-definition-format.md` §2.2; `CHANGELOG.md`; `KNOWN-LIMITATIONS.md` |
| `8b68611`, `0a65121` | Lint hygiene in new tests |
| `43592a5` | Out-of-scope changeset path after discovery is a counted sync error (found by mutation testing, see Deviations 5) |
| `cdd506b`, `882220a` | `FullRebuild` read failure restores the verified row **quarantined** (Deviation 3); CHANGELOG / KNOWN-LIMITATIONS |
| `9ace88f` | PostgreSQL test for the all-rows capability |
| `ad1836c` | Codex round-2 P1: `canReachEnd` made linear (Deviation 6) |

No migration, column, table, configuration key or environment variable was added
(`git diff --stat main...HEAD -- migrations go.mod go.sum` is empty).

### Per-AC evidence

Test files are in `adapters/projection` unless named otherwise.

1. `core/workflow/projection_definition_test.go` `TestProjectionDefinition_RefusesSmugglingClassAsParseError`:
   the two audit payloads (verbatim), `Steps:`, duplicate `id`, top-level `path:` / `commit_sha:` —
   all parse errors, none wraps `ErrProjectionRoundTrip`.
2. `TestProjectionDefinition_RefusesRoundTripFailures`: `version: 1.0`, `retry: {limit: 1.5}`,
   integer step `name`, `version: 2026-10-06`, `name: "a\0b"`, `commit: {status: "x\0"}`,
   `commit: {1: x}` — each `Parse`-accepted (asserted) and `ErrProjectionRoundTrip`.
   `TestProjectionDefinition_AcceptsLiteralBackslashU0000Text` accepts the six-character text.
3. `TestProjectionDefinition_ShippedCorpusUnchanged`: 8 Spine files pass, JSON byte-identical to an
   independent re-implementation of the `61994fe` encoder, and verify.
4. `TestVerifyProjectionDefinition_RejectsDisagreeingRows`: the `61994fe` encoding of every AC 1
   input that encodes (6; duplicate `id` does not encode) and of `version: 1.0` is refused. Plus
   `…RejectsStoredOddities` / `…AcceptsMinimalAndEmptyShapes`.
5. `workflow_integrity_test.go` `TestIncrementalSync_FoldedKeyWorkflowIsRejectedAndVerifiedRowKept`
   and `…FoldedOutcomeOverride…`: row kept (review step / `failed → validate`), `source_commit == c1`,
   `Quarantined`, not in `ListActiveWorkflowProjections`, still returned by `GetWorkflowProjection`,
   `idle@c2`, exactly one event `parse` / `kept` / `kept_source_commit: c1` / `quarantined: true`.
6. `TestFullRebuild_KeepsVerifiedRowOfRejectedFileQuarantined` (restored at `c1`, `Quarantined`,
   `idle`), `TestFullRebuild_RejectedFileWithNoPreviousRowIsNotProjected` (`not_projected`, `idle`).
   Review round 1 added the `VerifyProjectionDefinition` gate on both branches:
   `TestFullRebuild_UnverifiableRowOfRejectedFileIsRemoved` and
   `TestFullRebuild_ReadFailureDoesNotRestoreAnUnverifiableRow` (mutation-checked).
7. `TestIncrementalSync_UnverifiableRowIsRemovedOnRejection`, run with the instance already swept
   (diff path) and fresh (sweep path): row deleted, first event `removed`.
8. `TestProjection_ValidateIsAdvisoryUnlessStrict`: a `Draft` file failing `Validate` is projected
   with one Info line; with `WithStrictWorkflowValidation(true)` it is a `validate` rejection, no upsert.
9. `TestIncrementalSync_WorkflowReadFailureIsSyncErrorRejectionIsNot`: a `ReadFile` failure sets
   `error`, holds `c1`, leaves the row; a healthy retry projects `c2`. Rejections end `idle` (ACs 5–7).
10. `TestSweep_UpgradeCases` (a)–(i), all at `head == last_synced_commit`, `idle`: (a) removed;
    (b) replaced; (c) `absent_at_head`; (d) Active nested row `scope` + event, empty-status nested
    row deleted with one Info line and no Warn/event; (e) no upsert, `source_commit` unchanged;
    (f) no second listing; (g) Deprecated smuggled row removed with the capability, documented
    survivor without it; (h) two concurrent first syncs make exactly one listing (`-race` clean);
    (i) quarantined. Also `TestSweep_FailureHoldsTheSyncButDoesNotFreezeIt` (review round 1's
    replacement for `TestSweep_FailureMarksErrorAndRetries`, removed in `e7b1a3c`: a failed sweep
    holds the sync in `error` and retries, while the window's changes are applied) and, on
    PostgreSQL, `TestIntegration_SweepRemovesDeprecatedSmuggledRow`.
11. `TestProjection_NestedWorkflowFilesAreNotWorkflows`: only the top-level row; no rejection, no Warn.
12. `sync_errors_test.go` `TestIncrementalSync_DiffFailureIsErrorAndRetried` (store holds no rows, so
    the sweep is a no-op; the fake implements `ListFiles`/`ReadFile`): error returned, `{error, c1}`,
    `error_detail` contains `diff`, no branch-protection or workflow upsert; healthy retry re-projects
    both and ends `idle@head`.
13. `TestIncrementalSync_OneDiffPerSync`: exactly one `Diff`; none on the up-to-date tick.
14. `TestIncrementalSync_WorkflowRenameDeleteErrorIsCounted`.
15. `authoritative_ref_test.go` `TestAuthoritativeBranch_RunBranchCheckoutIsIgnored`, sub-tests
    `IncrementalSync` and `FullRebuild`: 1 rule kept, `last_synced_commit == rev-parse refs/heads/main`.
16. `TestAuthoritativeBranch_ZeroValueFollowsHead`: 0 rules without the option.
17. `TestWithAuthoritativeBranch_ArgumentValidation` (` main`, `main `, `-x`, `refs/heads/main`,
    `a..b`, NUL, `\x01`, `\n`, `m:a` refused; `""` restores the default),
    `TestAuthoritativeBranch_UnbornMainIsSyncError` (both entry points name `refs/heads/main`; state
    `error`, commit held, `error_detail` names the ref), review round 1's
    `TestAuthoritativeBranch_UnbornMainOnFreshStoreReportsError` (the empty-store case: `error` with
    the ref in `error_detail`, then `idle@main` once the branch exists),
    `TestAuthoritativeBranch_ShadowRefsDoNotWin` (`refs/tags/refs/heads/main`,
    `refs/refs/heads/main`, `refs/heads/refs/heads/main`, `refs/remotes/refs/heads/main`, tracked files
    `refs/heads/main` and `HEAD`, the relaxing branch checked out), `TestIsHexSHA`.
18. `service/cmd/spine/serve_projection_wiring_test.go` (unit) and
    `service/internal/workspace/pool_projection_wiring_test.go` (`-tags integration`, real DB):
    `ProjSync.AuthoritativeBranch() == engine.AuthoritativeBranch`; a `master`-only repository logs
    exactly one Warn naming `refs/heads/main` and startup does not fail.
    `TestProbeAuthoritativeRef_WarnsOnceWhenMissing`. The scenario suite runs with the harness option
    (Gates).
19. `scope_test.go` `TestIncrementalSync_ArtifactsDirScopesArtifactsAndWorkflows`: governed row keeps
    `Pending` + title; `spine/workflows/task.yaml` `Deprecated` v2; `spine/templates/x.md` not
    projected; root `workflows/rogue.yaml` Active through **both** entry points (OQ3 default); rename
    out of `templates/` creates TASK-002, the reverse deletes it. Classifier tables:
    `TestPathScope_Classifier`, `TestPathScope_ArtifactDiffsConvertsBoundaryRenames`.
20. `TestProjection_FullRebuildEqualsIncrementalStore`: 9 commits × `artifacts_dir` `""` and
    `"spine"` (adds, modifies, deletes, boundary renames both ways and across `templates/`,
    templates, nested and root and subdir workflows, two rejected workflows — one kept, one never
    projected — a workflow rename, branch-protection edit and deletion, a link change). After every
    commit a clone of the incremental store is rebuilt and compared: artifacts on every column but
    `source_commit`, links as sets, workflows on every column (`source_commit` too for quarantined
    rows), branch-protection rows without `source_commit`, execution projections of live tasks
    without `blocked`/`blocked_by`. **Mutation runs** (overlay, worktree untouched): dropping
    `DiscoverAll`'s dir, the incremental workflow classifier, the rebuild workflow filter, the rebuild
    root-workflow list, or the incremental `artifactDiffs` each fail this test and/or AC 19/11. (The
    last one passed until `43592a5`; Deviation 5.)
21. `corpus_test.go` `TestFullRebuild_ShippedWorkflowCorpus`: 8 rows, 0 rejections, `idle`, definitions
    and typed columns identical to the `61994fe` row, and a second instance's sweep upserts nothing.
22. `adapters/delivery/event_types_test.go` `TestAllEventTypes_IncludesProjectionWorkflowRejected`;
    `event-schemas.md` §4.14 + emission map.
23. `core/workflow/write_path_parity_test.go` (Create/Update reject unquoted `1.0` and `2026-10-06`
    with `ErrValidationFailed`, HEAD unchanged, file not written/unchanged; `ValidateBody` reports
    `projection_round_trip`, a strict-parse failure keeps `parse`) and
    `core/engine/run_test.go` `TestStartWorkflowPlanningRun_RejectsProjectionRoundTripBody`
    (`ErrInvalidParams`, writer not called).
24. SMP compatibility gate — see below.
25. Overlay test (`go test -overlay`, nothing written into SMP) over SMP's `governedsources.Workflows`
    (10 files, 3 under `workflows/steps/`): `idle`, exactly 7 rows, 0 under `workflows/steps/`,
    0 rejections, every definition byte-identical to the `61994fe` encoder.
26. Gates and PoCs — see below.
27. `TestIncrementalSync_KeptRowIsNotBindable`: `c1` review-less row kept, `Quarantined`,
    `workflow.ResolveBinding` over a `ProjectionWorkflowProvider` returns `workflow_not_found`,
    `GetWorkflowProjection` still returns `c1`, one `kept` event with `quarantined: true`, `idle@c2`;
    `c3` fixes the file → `c3`'s definition, `Active`, binding pins `c3`.
28. `sync_errors_test.go` `TestIncrementalSync_ArtifactReadErrorsAreClassified`: a non-`GitError`
    and an `ErrKindTransient` read hold the sync (error returned, `{error, c1}`) and a healthy retry
    projects the change; a real gitlink `x.md` (commit absent → `fatal: bad object`) ends `idle@head`,
    one Warn names `x.md`, `projection_synced` carries `read_skipped: 1`. `core/artifact`
    `discovery_read_errors_test.go` pins the recording; `core/engine`
    `TestValidateBranch_UnreadableGitlinkIsSkippedAsBefore` passes **unchanged at `main`** and at HEAD.
    **Superseded in review round 1:** the classification no longer reads the `GitError` Kind; see
    "Review round 1" — real-client tests in `read_errors_test.go`. Cases 1 and 2 of
    `TestIncrementalSync_ArtifactReadErrorsAreClassified` still pass, but only because that fake
    does not implement `git.TreeEntryTyper`, so every failure is infrastructure; the proof of
    AC 28 is now `TestIncrementalSync_GitlinkWithTransientLookingNameIsSkipped`,
    `TestIncrementalSync_KilledGitReadHoldsTheSync` (real `CLIClient`) and the table
    `TestClassifyReadErrors_OnlyAProvedNonBlobIsSkipped` (a typed blob and a client without the
    capability both hold).
29. `workflow_integrity_integration_test.go` `TestIntegration_NULWorkflowIsRejectionNotWedge` (written
    against the pre-existing API only). **HEAD:** `idle@c2`, row kept at `c1`, `Quarantined`, not
    Active, one `round_trip` / `kept` rejection. **`main` (same file run in a `git archive main`
    export):** `state=&{LastSyncedCommit:<c1> Status:error ErrorDetail:1 sync errors}`, commit not
    advanced. Review round 1 added the over-long indexed field case
    (`TestIntegration_OverLongIndexedFieldIsRejectionNotWedge`).

**Before/after for the new tests.** Run in a `git archive main` export with the new test files
copied in: every new test package fails to build at `main` (`svc.WithAuthoritativeBranch undefined`,
`undefined: artifact.DiscoverChangesFromDiffs`, `undefined: domain.EventProjectionWorkflowRejected`,
`rt.ProjSync.AuthoritativeBranch undefined`, `undefined: workflow.ErrProjectionRoundTrip`). The
behavioural before-state is the audit PoCs (AC 26) and AC 29 above.

### Audit PoCs (AC 26)

Run with `go test -overlay`-equivalent copies in scratch exports (git-ignored PoC material is not
committed):

| PoC | `main` | This branch |
|---|---|---|
| `TestPOC_ProjectionWorkflowFoldedKeyDifferential` | reproduces (engine steps `[execute publish]`) | fails at `projectWorkflow`: `rejected at c0ffee (parse)` |
| `TestPOC_ProjectionWorkflowFoldedOutcomeOverride` | reproduces (`failed -> "publish"`) | fails at `projectWorkflow` (parse rejection) |
| `TestPOC_IncrementalSyncArtifactsDirKeyCollision` | reproduces | fails `collision not reproduced`; governed row `Pending`, `spine/workflows/task.yaml` `Deprecated`, root `rogue.yaml` Active by design (AC 19) |
| `TestPOC_IncrementalSyncDropsSecondDiffError` | reproduces (`diff calls=2, bp=0, idle@2222…`) | with the three-method shim (`ListActiveWorkflowProjections`→nil, `ListFiles`→nil, `ReadFile`→error): fails `not reproduced` — `diff calls=1, branch-protection re-projections=1, sync_state=error@1111…` |
| `TestPOC_ProjectionFollowsCheckedOutRunBranch` | reproduces | **still reproduces**, as designed: it constructs `NewService` without the option (D7). Closed by ACs 15, 16, 18 |

### Deviations (closest safe alternative, with reasons)

1. **Rejection text is joined, not cut to its first line.** yaml.v3 puts the actionable detail
   (`line 8: field ſteps not found…`) on the second line, so `sanitizeForLog` replaces line breaks
   with `"; "`, every other control/format character (bidi overrides, U+2028/9) and invalid UTF-8 with
   U+FFFD, and caps at 512 bytes. Same log-injection guarantee, more useful text. `error_detail` of
   `markSyncError` goes through the same function (it also keeps a stray NUL out of a `text` column).
2. **`FullRebuild` reports `removed`, not `not_projected`, when a previous row existed but did not
   verify** (§2 step 4 said `not_projected` whenever no *verified* prev exists). The row was there
   and is gone, and the incremental path reports the same situation as `removed`, so both entry
   points now agree.
3. **`FullRebuild` cannot read a workflow (infrastructure error):** the verified previous row is
   restored **quarantined** and the sweep stays pending (the design only said such errors count as
   today; at `61994fe` the wipe simply lost the row and nothing retried it). Kept for pinned runs,
   not bindable while head cannot be checked (I6), restored by the next sync's sweep once the file
   reads. `TestFullRebuild_WorkflowReadFailureKeepsRowQuarantinedAndSweepRetries`. Review round 1:
   this now applies only to failures git causes; a gitlink (or other non-file entry) at the path is
   a `not_a_file` rejection on every entry point, and a failing sweep no longer stops the sync.
4. **`FullRebuild` holds the sweep mutex**, so a sweep and a rebuild on one instance never interleave
   workflow writes (the design made only the sweep single-flight).
5. **An out-of-scope path after scoped discovery is a counted sync error** instead of the
   design's silent "always resolves". Mutation testing showed the defensive skip masked removal of
   `artifactDiffs` from AC 20; now that removal fails AC 19/20.
6. **`workflow.Validate`'s termination check is linear** (codex round 2, P1). Projection now runs
   `Validate` on every committed file; `canReachEnd` enumerated simple paths (exponential: 2^n on a
   chain of two-outcome steps, codex measured 75 ms at 16 steps and 1.45 s at 20). It is now one
   graph search with a single visited set; reachability equals simple-path existence, so the result
   is identical for every input — checked against the old code on 20 000 random graphs
   (`core/workflow/termination_test.go`), plus a 400-step bounded-time test. This is in
   `core/workflow` and also bounds the create/update path; SMP behaviour is provably unchanged.
7. **`ReadErrors` also records the failed *old*-blob reads** in `modified` and `renamed` (not a
   `continue` in the old code, but a transient failure there turned a delete into a skip).
   `read_skipped` counts distinct paths.
8. **`DiscoveryError` gained `Ref` and `Err` fields** rather than a new record type. Additive; SMP
   only builds it with keyed literals (`projectionwriter/repo_artifacts_test.go`).
9. **New exported `ProbeAuthoritativeRef(ctx) error`** implements the one startup Warn (§4 note 4)
   so both wiring sites and the tests share it.
10. **AC 20's execution-projection comparison is restricted to live tasks.** Incremental sync never
    calls `DeleteExecutionProjection`, so a deleted task's execution row survives until a rebuild.
    That predates this task (artifact side), is recorded in `KNOWN-LIMITATIONS.md`, and is not
    changed here.
11. **SMP `TEST_PROVISIONING_DSN` suites were not run.** They allocate `ws_*` databases and roles on
    the server, and the shared server allows only `s011_*` databases. AC 24's two DB-backed worker
    tests were therefore replayed as overlay twins over an `s011` Spine-runtime database with the
    same `fixtureRepo` and the real `Worker` (see SMP gate), plus the §10 item 3/7 assertions.
12. **gofmt of touched files** (precedent: TASK-001) reformats pre-existing import-order drift in 9
    files and the domain-event alignment block in `core/domain/event.go`; the new event constant is
    placed in its own section at the end of the operational block so siblings that add operational
    events do not collide on alignment.
13. `event-schemas.md` had no `projection_synced` subsection; added §4.13 for it and §4.14 for the
    new event (§4 numbering already repeats 4.11 upstream).

### Gates

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | ok |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` | exit 0, 38 packages ok, 0 FAIL |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all clean |
| `go test -race -p 4 -count=1` on `adapters/projection`, `adapters/store`, `adapters/delivery`, `core/workflow`, `core/artifact`, `core/domain`, `core/engine`, `service/cmd/spine`, `service/internal/workspace`, `service/scenariotest/harness`, `workflows` | all ok |
| `SPINE_TEST_DATABASE_URL=…/s011_spine go test -race -p 1 -count=1 -tags integration ./adapters/projection/ ./adapters/store/ ./adapters/delivery/ ./service/internal/workspace/` | projection, delivery, workspace ok; store: 1 FAIL `TestRunModeDatabaseDefault` (`cannot scan NULL into *string` on `workflow_version_label`) — **identical at `main`** (run in a `main` export on its own `s011_store_main`), unrelated. New `TestIntegration_*`, `TestListWorkflowProjections_AllStatuses`, `TestBuildServiceSet_ProjectionFollowsAuthoritativeBranch` pass |
| `SPINE_TEST_DATABASE_URL=…/s011_scenario go test -p 1 -count=1 -tags scenario -v ./service/scenariotest/...` (`make test-scenario` with `-p 1`) | 190 pass / 30 fail / 6 skip; **`main` export on `s011_scenario_main`: the same 190 / 30 / 6, identical pass and fail sets** (the 30 are pre-existing: `artifact validation failed` on fixture creation, `TestSchemaMatchesProduction` NOT NULL `metadata`, gateway connection refused). The CI job's `TestGitHTTP_*`: 19 pass, 0 fail, 0 skip on both |
| `golangci-lint run` (v2 config) on every touched package, plain / `--build-tags integration` / `--build-tags scenario`, branch vs a `main` export, compared as (linter, file, message) sets | 0 new issues in each; 9 fewer (gofmt) |
| `golangci-lint run --enable-only=gosec` on the same packages | 1 issue, identical to `main` (`core/workflow/service.go` G703, pre-existing) |
| `gofmt -l` on every Go file in `git diff main...HEAD` | empty |
| `codex review --base main` | round 1: no findings. Round 2: **one P1** (unbounded `canReachEnd` now reachable from projection) — fixed in `ad1836c` with tests (Deviation 6). Round 3 (at `ad1836c`): no findings. Round 4 (final code `0a65121` plus these notes): no findings. No rejected findings |

### SMP compatibility gate

`/Users/bartoszszymichowski/42sid/i23/s-T011/smp` detached at SMP `7d4e7cc9`, `../spine` = this
worktree, no `vendor/`, no SMP file modified (`git status` empty after every run). Packages: the 31
that depend on a changed Spine package (`go list -deps`) plus `internal/docmemory`.

Final run with `../spine` at `0a65121` (the last code commit; later commits touch only this
artifact):

| Command (in the SMP checkout) | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...` | all ok |
| `go test -race -p 4 -count=1 <32 packages>` (no DB env) | 31 ok, 1 `[no test files]` (`workspace/faultharness`), 0 FAIL |
| `go run ./dev/apply-migrations -set platform -dir migrations` → `s011_smp_it` (pgvector); `-set spine-runtime` → `s011_smp_spine_rt` | 115 and 26 versions |
| `TEST_SPINE_DB=…s011_smp_spine_rt TEST_PLATFORM_DB=…s011_smp_it go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -allow-skip 'TEST_PROVISIONING_DSN' -- -race -p 4 -count=1 -timeout 15m ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` (CI's Spine-runtime step) | `testgate: PASS` — 1536 passed, 0 failed, 4 allowed skips (CI's parked placeholder + 3 `TEST_PROVISIONING_DSN`) |
| `TEST_DATABASE_URL=TEST_PLATFORM_DB=…s011_smp_it go run ./dev/testgate -allow-skip 'TEST_PROVISIONING_DSN' -allow-skip 'TEST_MINIO' -allow-skip 'TEST_AWS_OBJECT_STORE_' -- -race -p 4 -count=1 -timeout 20m ./internal/customer/projectionwriter/ ./internal/customer/workspace/ ./internal/customer/workspace/workspacetest/ ./internal/customer/eventdelivery/ ./internal/customer/stepevents/ ./internal/customer/profileinventory/ ./internal/docmemory/ ./internal/customer/docindex/ ./internal/automation/ ./internal/customer/discovery/` (the gate-list members of CI's shared step, plus `docindex`) | `testgate: PASS` — 1111 passed, 0 failed, 36 allowed skips (`TEST_PROVISIONING_DSN` per Deviation 11, MinIO, live AWS) |
| `go run ./dev/testgate -- -race -count=1 -tags integration ./internal/customer/githttp/` | `testgate: PASS` — 140 passed |
| Overlay twin of `TestWorker_Integration_ProjectsSeededRepo` (same `fixtureRepo` + real `Worker`, `s011_smp_spine_rt`) | PASS: `idle`, `task-default` and the constitution projected, exactly 7 rows `LIKE 'workflows/%'`, 0 `LIKE 'workflows/steps/%'` |
| Overlay twin of `TestWorker_Integration_SurfacesMalformedArtifact` | PASS: still `error` |
| Overlay §10 item 7: commit a rejected `task-default.yaml` (extra unknown key), next tick as `runSyncer` builds it | PASS: row `Quarantined` at the old `source_commit`, `sync_state` `idle`, `ProjectionWorkflowProvider.ListActiveWorkflows` omits it, `ProjectionWorkflowLoader.LoadWorkflow(path, <old commit>)` still returns it |
| AC 25 overlay (corpus) | PASS (see AC 25) |

No SMP edit was needed for the bump to compile or for any of the above to pass. The SMP-side
obligations of §10 (above all the backfill probe) remain SMP TASK-012's.

### Follow-ups (recorded, not done here)

- **OQ5:** `FullRebuild` preserving `source_commit` for unchanged rows (SMP strands pinned runs on
  every rebuild).
- **OQ2:** corpus repair so the 10 shipped workflows pass `Validate` in both repositories, then
  decide on `WithStrictWorkflowValidation`.
- **SMP TASK-012 obligations from §10** (all still required): validate-mirror switch to
  `ProjectionDefinition` with rule `projection_round_trip`; SMP corpus test; worker test assertions
  (7 rows, 0 `workflows/steps/%`, quarantine); **the mandatory backfill-probe change** — review
  round 1: it must also exempt a canonical path whose entry at `HEAD` is not a regular file (a
  gitlink, now a `not_a_file` rejection; `git.TreeEntryTyper` answers it), not only bodies
  `ProjectionDefinition` refuses; release notes (nested rows — every nested `workflows/**` row,
  Active ones included — deleted on first sync; quarantined workflows show non-Active; a git-caused
  read failure is an `error` tick).
- **Spine TASK-003:** register `ErrBlobTooLarge` in `isContentReadError`
  (`adapters/projection/workflow_rows.go`) and amend its I4 as §11 says. Since review round 1 the
  same hook is also consulted for changed-artifact reads, where it makes an oversize artifact a
  counted `read_skipped` skip (wedge-safe); TASK-003 confirms that is the artifact semantics it
  wants. A gitlink at a workflow path no longer needs TASK-003 (`not_a_file`).
- **Spine TASK-012 (working-tree serialisation):** if it wraps the `GitClient` handed to
  `projection.NewService`, the wrapper must forward `git.TreeEntryTyper`; otherwise every read
  failure is infrastructure and a gitlink holds the sync in `error` (fail closed, recorded in
  `KNOWN-LIMITATIONS.md`).
- **Spine TASK-006:** guard every directory in `projection.WorkflowDirs(artifactsDir)` and default its
  guard to `AuthoritativeBranch()`.
- Artifact-side U+0000 wedge (verified on PostgreSQL: an artifact title with `\0` keeps incremental
  sync in `error`), stale execution projections after incremental deletes, `workflow_id` not unique
  across paths in SMP's step-mode reads, `QueryService.CheckFreshness` still on `Head()`, surfacing
  rejections in SMP beyond logs.
- **Gitlink entries in a governed repository (review round 2).** Refuse a gitlink (mode 160000) at
  the push and merge boundary — Spine's pre-receive and SMP's `/git/{workspace_id}` gateway — so a
  gitlink recording the id of a blob in the repository cannot reach any reader. The projection now
  types workflow and branch-protection entries before reading them, but every artifact reader
  (projection discovery, `artifact.Service`, branch validation) still reads such an entry as the
  blob it names (`KNOWN-LIMITATIONS.md`).

### Review round 1 (2026-10-07)

Three reviewers (correctness and SMP compatibility, adversarial security, completeness and tests)
reviewed `0aeff8c`. Every finding was checked against the code first, and reproduced where it could
be; all were real. Commits: `bb61148` (git tree-entry typing), `7d1d34b` (indexed-field cap),
`4cc06b8` (sanitiser and event-id tests), `e7b1a3c` (read-failure classification, sync-state
recording, sweep), `5753b16` (docs), `a0adf30` (test-wrapper robustness), and these notes.

**Findings** (duplicates merged; the reviewers' severities):

| # | Finding | Disposition | What changed / why | Regression tests |
|---|---|---|---|---|
| F1 | P1 (two reviewers): `classifyReadErrors` took "transient, hold" from `GitError.Kind`, which `classifyGitError` derives from stderr substrings; `git show <sha>:<path>` echoes the attacker-chosen path, so a gitlink named `initiatives/blocked.md` or `docs/timeout.md` wedged the sync | **FIXED** | Reproduced before the fix: `read docs/Could not resolve.md …: git read_file: network error`, state `error@c1` every tick. A read failure is now deterministic only when the commit's **tree** proves there is no regular file at the path: new optional capability `git.TreeEntryTyper` (`*CLIClient.TreeEntryType`: `git --literal-pathspecs ls-tree -z --full-tree <ref> -- <path>`, exact-path match, tree objects only). stderr is never consulted (`adapters/projection/read_errors.go`) | `TestIncrementalSync_GitlinkWithTransientLookingNameIsSkipped` (gitlinks `initiatives/blocked.md`, `docs/timeout.md`, `docs/Could not resolve.md`, `docs/x.lock.md`; 3 ticks `idle@c2`, the real change applied, `read_skipped: 4`); `TestTreeEntryType` (its precondition asserts the stderr misclassification) |
| F2 | P2 (two reviewers): with the real client, a killed, unspawnable or silently failing git is a `*GitError` with empty stderr, Kind `permanent`, so it was skipped and the sync advanced past the change; the "not a `*GitError`" branch never fired | **FIXED** | Reproduced before the fix with a `PATH` wrapper (SIGKILL / `exit 1` on the blob read): `idle@c2`, change lost. Now a blob at the path means git failed → infrastructure; a client without the capability, an untypable tree or a cancelled context also hold | `TestIncrementalSync_KilledGitReadHoldsTheSync` (real `CLIClient`; signal and silent exit × modified and deleted), `TestClassifyReadErrors_OnlyAProvedNonBlobIsSkipped`, `TestIncrementalSync_KilledWorkflowReadIsInfrastructure`, `TestTreeEntryType_Errors` (git unavailable is an error, never "absent") |
| F3 | P2: a gitlink at a stored workflow path froze the projection — the pre-diff sweep returned the read error every tick, a `FullRebuild` restored the row quarantined and left the sweep pending, so a rebuild no longer healed it | **FIXED** | (a) A non-file entry at a workflow path is a new `not_a_file` rejection on every entry point (kept quarantined / `not_projected` / `removed`, sync advances). (b) A failed sweep is now **counted**, not returned: the state holds in `error` and the sweep retries, but the window's artifact, branch-protection and workflow changes are applied, as before the sweep existed | `TestProjection_GitlinkAtWorkflowPathIsRejection` (incremental, a fresh instance's sweep, `FullRebuild` then incremental; all advance and apply later commits), `TestSweep_FailureHoldsTheSyncButDoesNotFreezeIt` |
| F4 | P2: a Parse-accepted body whose indexed column overflows PostgreSQL's btree entry limit (an incompressible 6000-character `id`) failed the upsert → `error@c1` forever, previous row Active (I6 downgrade shape) | **FIXED** | Reproduced on PostgreSQL before the fix (`error@c1`, "1 sync errors"). `ProjectionDefinition` refuses a workflow path, `id` or `status` over 1024 bytes (`ErrProjectionRoundTrip`, value not echoed); the write path inherits it. `name` / `version` are not capped: their columns are unindexed, so PostgreSQL stores any length, and a cap would refuse legitimate text for no storage reason | `TestProjectionDefinition_RefusesOverLongIndexedFields` (1024 accepted, 1025 and 6000 refused, the `id: *desc` alias form), `TestService_ValidateBody_ReportsProjectionRoundTripRule` (long `status`), `TestIntegration_OverLongIndexedFieldIsRejectionNotWedge` (PostgreSQL: **before** `error@c1`; **after** `idle@c2`, `c1` row `Quarantined`, no Active row, one `round_trip`/`kept`) |
| F5 | P2: `FullRebuild`'s `VerifyProjectionDefinition` gate (rejection and infra-error branches) and Deviation 2 untested | **FIXED** | Mutation `… == nil \|\| true` now fails both new tests | `TestFullRebuild_UnverifiableRowOfRejectedFileIsRemoved` (row gone, one `parse`/`removed`, `idle`, sweep done), `TestFullRebuild_ReadFailureDoesNotRestoreAnUnverifiableRow` |
| F6 | P3: KNOWN-LIMITATIONS said an unreadable workflow's previous row is "kept quarantined"; only `FullRebuild` did that | **FIXED** | The plantable case (a gitlink) is now a `not_a_file` rejection that quarantines on every path. For a git-caused failure the limitation now says what happens: incremental holds with the row as it was (every change in the window is unapplied too); only a rebuild restores it quarantined. Not quarantining on a git failure is deliberate: no author can cause one (a push cannot plant a missing blob — receive-pack's connectivity check), and quarantining on every lock blip would deny new runs | (F3 tests) |
| F7 | P3 (two reviewers): docs promised `sync_state = error` for an unborn `main`, but an empty store got no row (the hand-off to `FullRebuild` returned before writing); `markSyncError` stored only the label | **FIXED** | `FullRebuild` records every failure (`error`, sanitised cause, previous commit kept; no commit after a failed — possibly partial — wipe). `IncrementalSync` rebuilds a store with no synced commit, except while it is `rebuilding` (a first rebuild in flight elsewhere, e.g. SMP provisioning — a second wipe could expose a half-written projection under the other's `idle`). `error_detail` = label + cause, through `sanitizeForLog` | `TestAuthoritativeBranch_UnbornMainOnFreshStoreReportsError`, `TestAuthoritativeBranch_UnbornMainIsSyncError` (detail), `TestFullRebuild_FailuresAreRecorded`, `TestIncrementalSync_NoBaselineWhileRebuildingIsLeftAlone`, `TestMarkSyncError_DetailCarriesTheSanitisedCause` |
| F8 | P3: CHANGELOG embedder list incomplete (write path SMP wraps; `Head()` failure now an `error` tick; the sweep deletes every nested `workflows/**` row, Active ones included); INIT-023 Rule 4 row narrowed the same way | **FIXED** | All three verified in the code; CHANGELOG embedder section and the Rule 4 row rewritten | — |
| F9 | P3: Deviation 3 made the sweep re-read an unreadable blob forever; "as before INIT-023" was wrong | **FIXED** | By F3 (a) and (b); docs corrected | (F3 tests) |
| F10 | P3: the sweep reported a legitimate deletion inside the pending window (`head` ahead of `last_synced_commit`, after a restart) as an `absent_at_head` rejection | **FIXED** | The sweep lists `last_synced_commit` lazily; a row whose file existed there is removed exactly as the diff would, with an Info line and no event. If that listing fails, the row is reported (the louder outcome); the delete happens either way | `TestSweep_DeletionInsidePendingWindowIsNotARejection` |
| F11 | P3: `sanitizeForLog` and the deterministic event id untested | **FIXED** | Mutations (identity sanitiser, raw error text in the event, a randomised id) each fail | `TestSanitizeForLog`, `TestReportWorkflowRejection_SanitisedAndDeterministicID` |

**Acceptance-criteria gaps:**

- **AC 28** (F1, F2) — FIXED. The fakes-only evidence is replaced by real-client tests.
- **AC 17 / §4 note 4** (F7) — FIXED.
- **AC 29** (F4) — FIXED, on PostgreSQL.
- **I6** — FIXED for every case an author can produce (an over-long indexed field → `round_trip`, a
  non-file entry → `not_a_file`; both quarantine). A git-caused failure on a real blob leaves the row
  as it was while the sync is held — not an I6 breach: nobody can make it happen through a commit,
  and under I3 the whole projection stays at `last_synced_commit` until the retry succeeds.
- **I2 / D2** (F3) — FIXED. `isContentReadError` still returns false for blob reads (TASK-003 adds
  its sentinel); non-blob entries no longer need it.
- **AC 6** (F5) — FIXED.
- **AC 18 "`make test-scenario` passes"** — NOT REAL as a TASK-011 defect: re-run at this round's
  code on `s011_r1_scen`, the suite gives 190 pass / 30 fail / 6 skip with pass and fail sets
  **identical** to `main`'s run (subtests too: 1475 / 57 / 6), and CI's `TestGitHTTP_*` 19 / 0 / 0.
  The 30 failures exist on `main` (fixture creation "artifact validation failed",
  `TestSchemaMatchesProduction` NOT NULL `metadata`, gateway connection refused) and are outside this
  task.
- **AC 24 (SMP `TEST_PROVISIONING_DSN` suites)** — NOT REAL as a defect, an environment limit: those
  suites create `ws_*` databases and roles, and the shared server allows only `s011_*` databases. The
  overlay twins (same fixture, real `Worker`) were re-run against this round's code and pass; the
  literal suites run in SMP TASK-012's gate at the bump (Deviation 11 stands).
- **AC 26** — re-run this round: `codex review --base main` reports no findings (its sandbox could not
  bind ports for one unrelated service test). The five audit PoCs (identifiers renamed to avoid
  clashes, the three-method shim for the diff-drop one) behave exactly as the table above records.
  The reviewers' own PoCs (`TestPOCSEC_GitlinkNamedLockWedgesSync`,
  `…WorkflowGitlinkSweepWedgeSurvivesRebuild`, `…KilledGitReadIsSkippedNotRetried`,
  `…Integration_LongIDWedges`, `…ProjectionDefinitionExactKeys`) all pass now; each failed (reported
  the vulnerability) before.

**New deviations (closest safe alternative):**

14. **Read failures are classified positively from the tree**, not with the §3 note's rule (not a
    `*GitError`, or Kind transient, or cancelled → infrastructure; everything else skipped). That rule
    was F1 and F2. `git.TreeEntryTyper` is an optional capability, not a `GitClient` method (no
    interface break for SMP or test doubles); a client without it fails closed.
15. **New rejection check `not_a_file`** (the design listed six checks); documented in
    `event-schemas.md` §4.14.
16. **A failed sweep is counted, not returned** (§3 step 2 said `markSyncError` and return), so it
    cannot freeze the rest of the sync.
17. **`FullRebuild` records its failures**, an empty `last_synced_commit` is rebuilt, and an empty
    one while `rebuilding` is left alone (the design wrote nothing on a rebuild failure).
18. **In-window deletions found by the sweep are not rejections** (§6 reported every absent row).
19. **Workflow path, `id` and `status` are capped at 1024 bytes** in `ProjectionDefinition` (the §1
    note covered U+0000 and encoder failures only); the write path inherits it.
20. **`isContentReadError` is consulted for changed-artifact reads too**, so TASK-003's sentinel will
    make an oversize artifact a counted skip rather than a wedge (no-op today).

**Mutation runs** (overlays, worktree untouched; each made the named tests fail): the `|| true`
verify gate; no in-window check; identity sanitiser; raw error text in the event; randomised event
id; stderr-Kind classification (`GitlinkWithTransientLookingName`, `KilledGitReadHoldsTheSync`); a
blob treated as skippable (`KilledGitReadHoldsTheSync`, `OnlyAProvedNonBlobIsSkipped`,
`KilledWorkflowReadIsInfrastructure`); workflow reads not classified (`GitlinkAtWorkflowPath`); the
sweep returning early (`FailureHoldsTheSyncButDoesNotFreezeIt`). The new classification and
workflow-gitlink tests were also run against `0aeff8c`'s code first: all failed.

**Gates (review round 1)** — Spine at `a0adf30` unless stated (`a0adf30` changes one test file
over `5753b16`):

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | ok; `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` (at `5753b16`) | exit 0, 38 packages ok, 0 FAIL |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all clean |
| `go test -race -p 4 -count=1 ./adapters/git/ ./adapters/projection/ ./core/workflow/ ./core/artifact/ ./core/engine/` | all ok (projection and git re-run at `a0adf30`) |
| `SPINE_TEST_DATABASE_URL=…/s011_r1 go test -race -p 1 -count=1 -tags integration` on projection, git, workflow, artifact, engine, store, delivery, workspace | all ok except store `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`), the pre-existing failure identical at `main` (round 0) |
| `SPINE_TEST_DATABASE_URL=…/s011_r1_scen go test -p 1 -count=1 -tags scenario -v ./service/scenariotest/...` | 190 / 30 / 6, sets identical to `main`; `TestGitHTTP_*` 19 / 0 / 0 |
| `golangci-lint run` on `adapters/git`, `adapters/projection`, `core/workflow`, `core/artifact`, plain / `--build-tags integration` / `--build-tags scenario`, vs the `main` export as (linter, file, message) sets | 0 new issues each (28 vs 31) |
| `golangci-lint run --enable-only=gosec` (plain and integration) on the same packages | 1 issue, the pre-existing G703 in `core/workflow/service.go` |
| `gofmt -l` on every Go file in `git diff main...HEAD` | empty |
| `codex review --base main` (at `5753b16`) | no findings |
| SMP gate (SMP `7d4e7cc9`, `../spine` at `5753b16`, the last commit SMP compiles): `go build ./...`, `go vet ./...`, `go vet -tags integration ./...` | all ok |
| SMP `go test -race -p 4 -count=1` on the 31 packages depending on a changed Spine package plus `internal/docmemory` | 31 ok, 0 FAIL |
| SMP testgate, Spine-runtime step (`s011_smp_spine_rt`) | PASS — 1536 passed, 0 failed, 4 allowed skips |
| SMP testgate, shared step + `docindex` (`s011_smp_it`) | PASS — 1111 passed, 0 failed, 36 allowed skips |
| SMP testgate `-tags integration ./internal/customer/githttp/` | PASS — 140 passed |
| AC 25 overlay corpus; overlay worker twins (`ProjectsSeededRepo`, `SurfacesMalformedArtifact`, `RejectedWorkflowIsQuarantined`) | all PASS; SMP `git status` clean |

Databases used this round (`s011_r1`, `s011_r1_main`, `s011_r1_scen`, `s011_smp_it`,
`s011_smp_spine_rt`) were dropped at the end.

### Review round 2 (2026-10-07)

One reviewer re-examined `01c0e90` after round 1. They confirmed F1–F11 fixed, re-ran the
PostgreSQL tests for F4 and AC 29 and found them passing, and re-ran every gate with no failures.
They reported one finding and six gaps. The finding was checked against the code first and
reproduced. Commits: `9166d5c` (code and tests), `f60a4f1` (docs), and these notes.

**Finding:**

| # | Finding | Disposition | What changed / why | Regression tests |
|---|---|---|---|---|
| R2-1 | P3: the `not_a_file` rule ran only when `ReadFile` failed. `git show <c>:<path>` prints whatever object a gitlink's recorded id names, so a gitlink whose id is an existing blob read successfully. The projection then accepted a hidden review-less workflow, or a `rules: []` branch-protection file, while diffs and repository viewers show only `Subproject commit <id>`. This contradicted CHANGELOG, event-schemas and KNOWN-LIMITATIONS. The same behaviour exists at `main`, so this is a bypass of the documented guarantee, not a regression | **FIXED** for workflow files and `.spine/branch-protection.yaml`. Artifacts are **documented, not typed** (Deviation 21) | Reproduced at `01c0e90` with the new tests. Incremental: the row was `Active` at `c2`, version `0.9`, with no review step. Branch protection: `idle@c2` with the relaxed rules. The new `readFileEntry` (`adapters/projection/read_errors.go`) types the entry with `git.TreeEntryTyper` **before** every workflow and branch-protection read: <br>• a gitlink, a directory or no entry at that exact path is an `unreadableEntry`; <br>• a typing failure, or a type outside the documented set, is infrastructure, so the sync holds; <br>• only a blob is read. A read failure on a blob is `read_bounds` when `isContentReadError` matches, otherwise infrastructure. <br>`buildWorkflowRow` turns `unreadableEntry` into a `not_a_file` rejection (or `read_bounds`), which covers incremental sync, the sweep and `FullRebuild`. `projectBranchProtection` turns it into a sync error with the rules unchanged (Deviation 22). A client without the capability keeps the historical read (Deviation 23). The ref is a commit SHA on every projection path, so the tree read and the blob read see the same entry. Cost: one `git ls-tree` per workflow read and per branch-protection projection | `gitlink_blob_test.go`, all on the real `CLIClient`: <br>`TestProjection_GitlinkToExistingBlobAtWorkflowPathIsRejection`. Its preconditions assert that `ls-tree` records `160000 commit <blob id>`, that `cat-file -t` is `blob`, that `git show` prints the hidden body, and that the diff is `T`. It covers incremental sync (`kept` quarantined at `c1`, plus `not_projected` for a new hidden workflow, `idle@c2`), a fresh instance's sweep, `FullRebuild` over the same store, `FullRebuild` into an empty store (no row for either path), and then a regular file at `c3`, which projects. <br>`TestProjection_GitlinkToExistingBlobAtBranchProtectionIsHeld`: 2 ticks `error@c1` with the strict rules kept and a Warn naming `tree entry: gitlink`; a rebuild keeps them; an empty store writes no rules; the same relaxation committed as a regular file projects. <br>Fake tables: `TestBuildWorkflowRow_TypesTheEntryBeforeEveryRead` (non-blob entries are not read at all) and `TestProjectBranchProtection_TypesTheEntryBeforeReading` |

**Gaps:**

| # | Gap | Disposition |
|---|---|---|
| G1 | Round-1 dispositions F1–F11 confirmed, including tree-shape probes (root `..`, `/` in a name, unsorted tree) | Confirmed. No action |
| G2 | Gates green at `01c0e90`, lint 0 new | Confirmed; re-run at this round's code (below) |
| G3 | AC 18 (third bullet) and AC 26: "`make test-scenario` passes" is not met as written. The reviewer did not re-verify that the sets match `main` | **Re-verified this round** with `go test -json` on both trees: HEAD and a `main` export each give **190 pass / 30 fail / 6 skip** (subtests 1285 / 27), **0 differences** in any test's outcome; `TestGitHTTP_*` 19 / 0 / 0 on both. Not a TASK-011 defect. Closing the AC as written **needs an owner waiver or an amended AC**; it stays an open question |
| G4 | AC 24: SMP `TEST_PROVISIONING_DSN` suites not run | Still unmet as written; it is an environment limit (Deviation 11). The literal suites run at SMP TASK-012's gate. **Owner waiver or deferral** needed; open question |
| G5 | AC 10 evidence cited the removed `TestSweep_FailureMarksErrorAndRetries` | **FIXED**: the evidence now cites `TestSweep_FailureHoldsTheSyncButDoesNotFreezeIt` |
| G6 | AC 28 cases 1–2 pass only because the fake has no `TreeEntryTyper` | **FIXED** in the evidence: it now says so and names the real-client tests as the proof |

**New deviations (closest safe alternative):**

21. **Artifact reads are not typed before a successful read** (the reviewer marked this optional).
    A gitlink at an artifact path whose id names a blob is still read as that blob. That is true of
    projection discovery and of every other artifact reader: `artifact.Service` Read/List and
    branch validation. A projection-only guard therefore would not close the class. It would also
    break the `FullRebuild` ≡ `IncrementalSync` equivalence (AC 20) unless both entry points typed
    every artifact, which adds a git process per artifact to every full rebuild. Artifact semantics
    for gitlinks already differ between the entry points at `main`. The CHANGELOG / event-schemas
    `read_skipped` wording is narrowed to reads git could not perform. KNOWN-LIMITATIONS records the
    gap, and the follow-up refuses gitlinks at the push and merge boundary, which covers every
    reader. Workflow files are fully covered: run binding goes only through the projection
    (`ProjectionWorkflowProvider`, the only `WorkflowProvider`), and the loader reads at the bound
    row's `source_commit`. Branch-protection enforcement reads only the projection
    (`core/branchprotect/projection`).
22. **A non-file entry at `.spine/branch-protection.yaml` is a held sync error, not a new
    rejection kind.** This matches ADR-009 §1's rule for a malformed config: the rules stay as they
    are, the sync does not advance, and the forced re-projection on `error` retries every tick. It
    is never treated as "absent", which would seed bootstrap defaults. A dangling gitlink there was
    already held (read error) at `main` and in round 1.
23. **A git client without `TreeEntryTyper` keeps the historical read.** A successful read is
    trusted, and a failure goes through `classifyReadFailure` (infrastructure). Refusing every read
    would stop the projection for an embedder whose decorator does not forward the capability, and
    for every test double. `*git.CLIClient` implements it, and Spine standalone (`gitpool` primary
    client), the scenario harness and both SMP call sites (`projectionwriter/worker.go`,
    `workspace/provision_steps_runtime.go`) pass a `CLIClient`. Documented in CHANGELOG (embedders)
    and KNOWN-LIMITATIONS.
24. **Symlinks stay blobs.** `TreeEntryBlob` covers mode 120000. A symlink's stored content is its
    link text, which is what the diff shows and what the projection reads. A link-text body is
    therefore not hidden, and a real symlinked workflow never parses. The docs say "file (blob)"
    rather than "regular file".

**Mutation runs** (`go test -overlay`, worktree untouched):
- reading the entry anyway after a non-file answer fails all four new tests and
  `TestProjection_GitlinkAtWorkflowPathIsRejection`;
- treating an unknown type as a blob fails both fake tables.

**Gates (review round 2)** — Spine at `f60a4f1` (code at `9166d5c`):

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | ok; `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` | exit 0, 38 packages ok, 0 FAIL |
| `go vet ./...`; `-tags integration`; `-tags scenario` | all clean |
| `go test -race -p 4 -count=1` on `adapters/projection`, `adapters/git`, `adapters/store`, `adapters/delivery`, `core/workflow`, `core/artifact`, `core/domain`, `core/engine`, `service/cmd/spine`, `service/internal/workspace`, `service/scenariotest/harness` | all ok |
| `SPINE_TEST_DATABASE_URL=…/s011_r2 go test -race -p 1 -count=1 -tags integration` on projection, git, workflow, artifact, engine, store, delivery, workspace | all ok except store `TestRunModeDatabaseDefault` (`cannot scan NULL into *string` on `workflow_version_label`); the same test fails the same way in a `main` export on `s011_r2_main` |
| `SPINE_TEST_DATABASE_URL=…/s011_r2_scen go test -p 1 -count=1 -tags scenario -json ./service/scenariotest/...`, and the same in a `main` export on `s011_r2_scen_main` | 190 / 30 / 6 on both, 0 outcome differences (G3); `TestGitHTTP_*` 19 / 0 / 0 |
| `golangci-lint run` (v2, `--allow-parallel-runners`) on the 11 touched packages, plain / `--build-tags integration` / `--build-tags scenario`, vs the `main` export as (linter, file, message) multisets | 0 new each (144 vs 154, 146 vs 156, 145 vs 155) |
| `golangci-lint run --enable-only=gosec`, plain and integration, same packages | 1 and 2 issues, identical to `main`; 0 new |
| `gofmt -l` on every Go file in `git diff main...HEAD` | empty |
| `codex review --base main` (at `f60a4f1`, with these notes' AC 10/28 edits uncommitted) | "No actionable regressions were identified"; no findings to reject |
| SMP gate (SMP `7d4e7cc9`, `../spine` at `f60a4f1`, no SMP file modified): `go build ./...`, `go vet ./...`, `go vet -tags integration ./...` | all ok |
| SMP `go test -race -p 4 -count=1` on the 31 packages depending on a changed Spine package (`go list -deps`) plus `internal/docmemory` | 31 ok, 1 `[no test files]`, 0 FAIL |
| SMP testgate, Spine-runtime step (`s011_smp_spine_rt`, `-set spine-runtime`, 26 versions) | PASS — 1536 passed, 0 failed, 4 allowed skips |
| SMP testgate, shared step + `docindex` (`s011_smp_it`, platform set, 115 versions) | PASS — 1111 passed, 0 failed, 36 allowed skips (`TEST_PROVISIONING_DSN`, MinIO, live AWS) |
| SMP testgate `-tags integration ./internal/customer/githttp/` | PASS — 140 passed |

SMP needs no edit: both of its `NewService` call sites pass a `*git.CLIClient`, so they get the
typed read. The only behaviour change for SMP-produced input is the extra `ls-tree` per read.
The SMP TASK-012 backfill-probe obligation from §10 covers the new case unchanged: a canonical
path whose entry at `HEAD` is a gitlink, now including one that names a blob, is a `not_a_file`
rejection.

Databases used this round (`s011_r2`, `s011_r2_main`, `s011_r2_scen`, `s011_r2_scen_main`,
`s011_smp_it`, `s011_smp_spine_rt`) were dropped at the end. Both repositories' `git status` was
clean apart from this artifact.

### Integration (wave 2 merge) (2026-10-08)

Integration branch `init/INIT-023-wave2-integration` (worktree `i23/s-int2/spine`), cut from Spine
local `main` `ee3c249`. Merge order, each `git merge --no-ff` and built and tested before the next:
TASK-002 `b674b90`, TASK-016 `350fe87`, TASK-004 `50b9c8e`, TASK-009 `8925de9`, TASK-007 `2aca054`,
TASK-011 `c90c049`, TASK-010 `1e63692`, TASK-013 `85bf4b3`. Coordination commits: `3b88fb1`
(TreeEntryType joins the ref grammar), `0c26c91` (step-entry policy through the shared pool
constructor), `9e43640` (a refused signing secret holds its deliveries), `d61359d` (security model:
skills are a step-entry boundary), `1291e74` (codex round 1). Nothing was pushed; `main` and the task
branches are unchanged.

**This task in the integration.** Merge `c90c049`.

- *Conflicts:* `core/workflow/service.go` (with TASK-004; resolution under TASK-004's note),
  `CHANGELOG.md`, `KNOWN-LIMITATIONS.md`; and, at TASK-010's merge, the initiative's SMP-effect table,
  where each branch carried the other's older row (this task's row is kept from this task).
- *Semantic:* `pool_projection_wiring_test.go` adopted TASK-009's `buildServiceSet` signature. `3b88fb1`
  puts `TreeEntryType` on TASK-002's Rev grammar (exact branch lookup, `--end-of-options`, no trailing
  `--`); the projection passes object ids, so its behaviour is unchanged, which the projection tests
  and SMP's projectionwriter testgate confirm.
- `isContentReadError` still awaits TASK-003's sentinel (not in wave 2).

**Gates on the integration branch** (code at `d61359d`; `1291e74` changes one `adapters/git` test
file, after which `go vet` and `go test -race ./adapters/git/...` were re-run, ok):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "library surface is sound" |
| `go test -p 4 -count=1 ./...` | exit 0 (40 packages ok) |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 25 packages the eight tasks touched | 24 ok; `service/scenariotest/scenarios` has only `scenario`-tagged files |
| `-tags integration`, one fresh `s2i_int_*` database per package | store 165 PASS + the pre-existing `TestRunModeDatabaseDefault`; delivery 314, projection 160, cmd/spine 168, workspace 223 (with `SPINE_REGISTRY_TEST_DATABASE_URL`, registry migrated from `migrations/registry`), testutil 4; 0 SKIP |
| CI `scenario-git-http` job | `TestGitHTTP_*` 20 PASS, 0 SKIP; `TestBootstrapInternalAdmin_*` 2 PASS |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` | 30 FAIL, the **same set** as a `git archive main` export on its own database (1479 vs 1475 PASS, 6 SKIP for the registry env in both) |
| `git-floor` job's two steps in `golang:1.26-bookworm` (git 2.39.5, read-only mounts, `GOPROXY=off`, `--pull=never`, `--rm`) | step 1: 73 PASS, 0 SKIP, 0 FAIL (includes the new `RefGrammar_TreeEntryType*`); step 2 all ok |
| `golangci-lint run --build-tags scenario` on the touched packages | `--new-from-rev main` reports one QF1006 at `adapters/delivery/webhook_dispatcher_test.go:349`, present on `main` (TASK-010 changed the literal on that line). Full sets compared by (file, linter, text): 298 issues vs `main`'s 348, nothing new except three `rangeValCopy` whose byte count moved 224 → 248 (`store.EventSubscription` grew in TASK-009) |
| `--enable-only=gosec` | 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| `codex review --base main` | round 1: one P2 (macOS xcrun cache in the TASK-002 leftover checks), fixed in `1291e74`; round 2: "No actionable regressions were identified relative to the merge base." |
| SMP compat (`i23/s-int2/smp` = SMP `6ca0eeb7`, `../spine` = this worktree, `GOFLAGS=-mod=readonly`, never modified) | `go build ./...`; `go vet` plain, `integration`, `scenario`, `faultinject`: OK. `go test -race -p 4` on the 38 SMP packages depending on a changed Spine package (`go list -test -deps`) plus `./internal/customer/...` (67 packages): 62 ok, 5 without tests. `dev/testgate -allow-skip TEST_PROVISIONING_DSN -- -race` on spinecore, spineembedded, runner, executionreadiness (`s2i_smp_spine_runtime` and `s2i_smp_platform`, migrated with `dev/apply-migrations`): 1785 passed, 0 failed, 3 allowed skips. githttp `-tags integration` testgate: 208 passed. Extra: eventdelivery, projectionwriter, stepevents testgate: 35 passed, 6 allowed skips. `dev/lint-spine-internals.sh` OK. `TEST_PROVISIONING_DSN` suites not run (out of scope). |

All `s2i_*` databases were dropped afterwards; the only container started was removed.

### Cross-task review (wave 2) (2026-10-08)

**This task.** Finding 5: the first-sync workflow sweep, on the up-to-date early-return path,
now re-records `sync_state` when it wrote a row, restoring the pre-TASK-011 property that every
stale-head row write also moves `sync_state`; the §6 "as before" note is corrected. Finding 7's
TASK-011 half: the `TreeEntryTyper` CHANGELOG entry now states its ref grammar.

Wave 2 cross-task review of the integration branch at `2fab682`, five lenses (governance-engine,
secrets-delivery, git-ref-surface, smp-compat, merge-ci). Each P1/P2 survived two adversarial
skeptics. Every finding was re-verified against the code before it was fixed. Fixes are on
`init/INIT-023-wave2-integration` (worktree `i23/s-int2/spine`). Nothing was pushed, and `main`
and the task branches are unchanged.

| # | Finding (lens) | Severity | Disposition | Commit | Regression test |
|---|---|---|---|---|---|
| 1 | Separation of duties counted only `manual`/`automated` executions as authorship, while TASK-016 lets the bound actor of every type but `review`/`internal` push the run branch: the evaluator of a `convergence` step (or of an untyped step) could push commits and then review the run (governance-engine) | P2 (both skeptics: P2) | **FIXED.** `isAuthorStepType` is `!refadmit.FrozenStepType`, used by `runAuthor`, the early return and the symmetric guard. KNOWN-LIMITATIONS, `architecture/actor-model.md`, CHANGELOG and the TASK-007 notes no longer claim full coverage, and they state the operator gap (operators push unbound and are invisible to SoD) | `b191be9` | `TestStepEntry_SoDAuthorshipCoversEveryPushableStepType` drives TASK-016's evaluator (orchestrator as step-definition source), then assign and claim, for every step type plus a live-reviewer case. Mutation: the old predicate fails 6 sub-tests. Also `TestIsAuthorStepType` |
| 2 | `spine secrets reencrypt` without `--all-workspaces` bound the FileProvider's `default` fallback when `SPINE_WORKSPACE_ID` was unset. A migrate sweep then sealed legacy rows under `default` and reported the workspace clean, and serve, bound to its real id, refused every row (secrets-delivery) | P2 (both skeptics: P3) | **FIXED.** The command refuses ("nothing was changed") unless `SPINE_WORKSPACE_ID` is set or `--workspace` is given (`--workspace default` stays valid), as it already did for the mode. The header line prints `workspace_id=`. Integration guide, `.env.example` and CHANGELOG updated | `808bd6b` | `TestSecretsReencrypt_SingleWorkspaceRequiresExplicitID` (refusal before any database is opened); integration `TestSecretsReencrypt_SingleWorkspaceNeverGuessesTheID` (the reviewer's reproduction: both rows untouched; with the id stated, serve reads them). Mutation fails both |
| 3 | An exploratory `branch_id` of `refs` (and a workflow-defined `refs`/`HEAD`) created `spine/<run>/<div>/refs`, which TASK-016 rule 0b refuses for every principal (git-ref-surface) | P3 | **FIXED for the API path:** `ValidateExploratoryBranchID` refuses `refs` (400). `HEAD` cannot match the lower-case grammar, and the run and divergence components the engine builds never equal either name. **Follow-up:** structured branch ids and divergence-point ids from a workflow definition are not validated. Checking them in `workflow.ValidateSemantic` changes validation output on SMP's `WorkflowWriter` path, and a `createBranchRecord` refusal changes embedded behaviour; under the INIT-023 rule both need an SMP-compat argument or an opt-in. The divergence-point id needs care too: one containing `/refs` produces the same component | `abfb014` | `TestValidateExploratoryBranchID_AgreesWithPushAdmission` classifies the ref of every admitted id with `refadmit.Classify` |
| 4 | `resolveRev` is branch-first, so a contributor-pushed `refs/heads/<n>` overrides a same-named operator tag in bare-name reads (`?ref=` on the artifact, list and workflow APIs). git and Spine at `61994fe` read the tag (git-ref-surface) | P3 | **DOCUMENTED, follow-up.** A new KNOWN-LIMITATIONS residual under "Git Ref Arguments" gives the workarounds: name tags in full, or protect the branch namespace with a rule. Not fixed in `resolveRev` because each option changes engine-internal resolution for every embedder. Refusing an ambiguous bare name lets anyone who can push a same-named tag deny reads of a run branch where tags are not operator-only. Preferring the tag reopens F10's tag substitution. Follow-up (owner: TASK-002): qualify user-supplied `?ref=` at the standalone gateway and refuse a bare name that is both a branch and a tag, which costs one extra `show-ref` on that path only | `4849960` | — |
| 5 | The first-sync workflow sweep judges rows at a head resolved before it lists them. It deleted a row another instance had projected at a newer head, then took the up-to-date early return without writing `sync_state`, so nothing re-diffed the window and the row stayed lost (smp-compat) | P3 | **FIXED.** The sweep reports whether it wrote a row. On the early-return path a sweep that wrote re-records `sync_state` (idle at the head it judged by), so if another instance advanced it, the next tick re-diffs and restores the row. A sweep that wrote nothing keeps the early return read-only. **Residual** (CHANGELOG): if the other instance's whole sync spans the sweep, the row can still be lost. That is the same window the diff path had before TASK-011. A compare-and-set on `sync_state` (an optional store capability) would close it; follow-up | `0fe5011` | `TestSweep_StaleHeadInstanceDoesNotLoseANewerRow` (two instances over one store; a stale-HEAD decorator lets the other instance sync between head resolution and the sweep's listing; mutation fails), `TestSweep_NoChangeKeepsTheEarlyReturnReadOnly` |
| 6 | The CHANGELOG's TASK-004 entry said SMP's `validateRepoRelativePath` already refuses `a//b.md`, `a/./b.md` and a trailing `/`. It refuses only empty, absolute, NUL and `..` paths, and only in front of SMP's `RealGitOperator` (smp-compat) | P3 | **FIXED.** The CHANGELOG now says SMP is unaffected because its embedded entry points pass canonical paths (`normalizeTaskPath`, `path.Clean` equality, `BuildArtifactPath`; checked at SMP `6ca0eeb7`). The TASK-004 §1a note is corrected | `4849960` | — |
| 7 | CHANGELOG statements that the merge made wrong: TASK-001 still said a control byte in `branch_id` is a 500, TASK-002 still "recommended" refusing object-id-named refs to TASK-016, and `TreeEntryType` (joined to the Rev grammar in `3b88fb1`) was missing from TASK-002's Rev lists and from TASK-011's `TreeEntryTyper` entry (merge-ci) | P3 | **FIXED** | `4849960` | — |

Also checked and unaffected: under the zero-value step-entry policy (SMP today) finding 1's change
is invisible, and no shipped Spine or SMP workflow has a `convergence` or untyped step. The
`spine serve` strict scenario suite gives the same results before and after (below).

**Gates** (code at `4849960`; the artifact notes were committed afterwards):

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "library surface is sound" |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the 25 packages touched since `main` | 24 ok; `service/scenariotest/scenarios` has only `scenario`-tagged files |
| `-tags integration`, one fresh `s2f_int_*` database per package | store 165 PASS plus the pre-existing `TestRunModeDatabaseDefault` failure; delivery 314; projection 162 (+2); cmd/spine 170 (+2); workspace 223 (with `SPINE_REGISTRY_TEST_DATABASE_URL`, registry migrated from `migrations/registry`); testutil 4; 0 SKIP |
| CI `scenario-git-http` job | `TestGitHTTP_*` 20 PASS, 0 SKIP; `TestBootstrapInternalAdmin_*` 2 PASS |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` | 30 FAIL, the **same set** as a `git archive 2fab682` export (the pre-review integration head) run on its own database: 1479 PASS and 6 SKIP in both |
| `golangci-lint run --build-tags scenario --new-from-rev main` on the touched packages | 1 issue: the QF1006 at `adapters/delivery/webhook_dispatcher_test.go:349`, present on `main`. The full set is 298 issues, as at the integration, and none is in a file this review changed. `--build-tags integration --new-from-rev 2fab682` on `service/cmd/spine` and `adapters/projection`: 0 |
| `--enable-only=gosec` | 0 issues |
| `gofmt -l` on every `.go` file changed since `2fab682` | empty |
| `codex review --base main` | "No actionable regressions were identified." |
| SMP compat (`i23/s-int2/smp` = SMP `6ca0eeb7`, `../spine` = this worktree, `GOFLAGS=-mod=readonly`, never modified) | `go build ./...`; `go vet` plain, `integration`, `scenario`, `faultinject`: OK. `go test -race -p 4` on the 67 packages (dependents of a changed Spine package plus `./internal/customer/...`): 62 ok, 5 without tests. `dev/testgate -allow-skip TEST_PROVISIONING_DSN -- -race` on spinecore, spineembedded, runner, executionreadiness (`s2f_smp_spine_runtime` and `s2f_smp_platform`, migrated with `dev/apply-migrations`): 1785 passed, 0 failed, 3 allowed skips. githttp `-tags integration` testgate: 208 passed. `dev/lint-spine-internals.sh` OK. `TEST_PROVISIONING_DSN` suites not run (out of scope) |

All `s2f_*` databases were dropped afterwards, and no container was started.

### Codex 6.1 catch-up (2026-10-14)

A catch-up review of the merged waves 1 and 2 ran on Codex `gpt-6.1-sol`; the earlier reviews ran
on an older model. Each P1/P2 survived two skeptics, both of whom checked it against Spine local
`main` `d23b897`. Fixes are on `init/INIT-023-codex61-catchup` (worktree `i23/s-cx/spine`, cut
from `d23b897`). Nothing was pushed, and `main` and the other branches are unchanged. The third
finding (range `sp-w2int`) is on the wave 2 cross-task review's `0fe5011`, which amended this
task's §6 sweep, so it is recorded here.

**This task.** Two findings, plus one sibling found while verifying them. TASK-004's finding (a
leaf swapped for a FIFO) is recorded in that task.

| # | Finding | Severity | Disposition | Commit | Regression test |
|---|---|---|---|---|---|
| 1 | `VerifyProjectionDefinition` (`projection_definition.go:140-141`) built its strict reading by running `json.Unmarshal` into `any`, which rounds an integer above 2^53 through float64. `ProjectionDefinition` stores such an integer exactly (`retry.limit`, `min_branches`, `max_branches`, for example `9007199254740993`), and the folding reader decodes it exactly, so a definition it had accepted failed verification. On a later rejected edit of the file, all three rejection paths (`applyRejection`, the sweep, `planWorkflowRows`) deleted the row instead of keeping it quarantined for the runs pinned to it | Codex P2. Skeptics: P3, P3 (an absurd value, and it fails closed) | **FIXED.** `decodeStoredJSON` decodes with `UseNumber`. An integer literal that fits an int64 becomes an int64, which `yaml.Marshal` writes digit for digit, and every other number becomes the float64 it was. Trailing data and out-of-range numbers are still refused | `334ee23` | `TestVerifyProjectionDefinition_AcceptsEveryStoredInteger` covers every int field (`retry.limit`, both `min_branches`, `max_branches`) at 2^53+1, 2^62+1 and -(2^53+1). Max int64 passed on `main` only because arm64 saturates a float-to-int conversion. `TestIncrementalSync_RejectedEditKeepsARowWithALargeInteger`. On real PostgreSQL, `TestIntegration_LargeIntegerRowSurvivesSweepAndRejection` (`0811ff3`) shows jsonb returns the exact digits; neither skeptic could check that live. All fail on `main`. Guards: `TestVerifyProjectionDefinition_RefusesAnIntegerNoFieldHolds`; `trailing_value` and `trailing_garbage` in `RejectsStoredOddities`, which also pass on `main` |
| 1a | *Sibling, not reported:* the sweep's `sameJSON` (`sweep.go`) has the same root cause. Two definitions that differ only in such an integer compared equal, so a drifted row was counted unchanged and never repaired | P3 | **FIXED** with the same exact decode (`decodeExactJSON`). Integers in accepted definitions are always int64 digit strings (Go's `json.Marshal` writes them so, and jsonb returns them unchanged), so no row that compared equal before is re-stamped unless its content really differs | `334ee23` | `TestSweep_ReplacesARowThatDiffersOnlyInALargeInteger` fails on `main`. `TestSweep_KeepsAnUnchangedRowWithALargeInteger` (no re-stamp), and the real-PostgreSQL test above checks the same on jsonb |
| 2 | The swept early return (`service.go:439-442`, added by `0fe5011`) wrote an unconditional `UpdateSyncState{head, idle}`. Another instance can write `sync_state` after this sync read `idle@head`, and the write then replaced: `rebuilding` (a reader saw `idle` over a half-written projection); `error` (a same-head rebuild failure was erased, so the next tick took the up-to-date return and skipped the forced branch-protection retry); and the no-baseline `error@""` a partial wipe records (the next sync diffed instead of rebuilding, so the wiped rows stayed missing; this case was not in the finding) | Codex P2. Skeptics: P3, P3 (the newest copy of the protocol's existing last-writer-wins race) | **FIXED.** The write needs only the baseline, so the next tick re-diffs from the head the sweep judged by. `resetSyncBaseline` asserts a new optional store capability, `SetSyncBaseline(ctx, commit)`. On `*store.PostgresStore` it is one `UPDATE` of `last_synced_commit`, applied only when the stored commit is not empty, and it leaves status and `error_detail` alone. As with `ListWorkflowProjections` (design-check note to §6), it is asserted rather than added to `store.Store`, so no stub or embedder store breaks. A store without it keeps the plain `idle@head` write. Compile-time assertions pin both capabilities on `PostgresStore`. No schema change. The wider residual (every `sync_state` write is last-writer-wins, including the existing "a sync spanning the sweep") is now a KNOWN-LIMITATIONS entry, with compare-and-set or a lock as the follow-up | `26111a9` | `TestSweep_BaselineResetKeepsAConcurrentStatus` (`rebuilding`, `error`, `no_baseline`): a stale-head instance sweeps while another instance projects c2 and records that status. Every sub-test fails on `main`. The `error` case then checks that the next tick restores the row and re-projects branch protection. `TestSweep_StaleHeadInstanceDoesNotLoseANewerRow` now runs both through the capability and through the plain-store fallback. Integration: `TestSetSyncBaseline_MovesOnlyTheBaseline` |

**SMP impact.** SMP's projection writer and provisioning build `projectionsvc.New(repoPath,
spinestore.NewPostgresStoreWithQuerier(pool), ...)`, so they get `SetSyncBaseline` with no SMP
change. The only behaviour that differs is when another instance writes `sync_state` inside the
sweep's window; any sync without such a concurrent write ends `idle@head` as before. Finding 1
changes the outcome only for a definition holding an integer above 2^53, which no Spine or SMP
workflow has. SMP's test store `bpStore` embeds `store.Store`, so it does not have the capability
and keeps the old write.

**Gates** (code at `2aa99d6`; these notes were committed afterwards). Run from the Spine worktree
unless noted.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go test -p 4 -count=1 ./...` | exit 0, 40 packages ok |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean |
| `go test -race -p 4 -count=1` on the four touched packages (`core/worktreefs`, `core/workflow`, `adapters/projection`, `adapters/store`) | 4 ok |
| Linux: `golang:1.26-bookworm` (already present; `--pull=never --rm`, read-only mounts, `GOPROXY=off`), `go test -race ./core/worktreefs/ ./core/workflow/` | ok. The FIFO tests fail there on `main` too. Windows: `GOOS=windows go build` and `go vet ./core/worktreefs` OK |
| `-tags integration`, one fresh `cxp_*` database per package | store: 171 PASS (counting subtests) plus the pre-existing `TestRunModeDatabaseDefault` failure; projection: 171 PASS, 0 FAIL, 0 SKIP, plus the new real-PostgreSQL test. None of the touched packages has any other integration suite |
| CI `scenario-git-http` job | `TestGitHTTP_*`: 20 PASS, 0 SKIP. `TestBootstrapInternalAdmin_*`: 2 PASS (`IdempotencyAndRotation`, `PooledBuilderIdempotencyAndRotation`) |
| `go test -tags scenario -p 1 -count=1 ./service/scenariotest/...` | 30 top-level FAIL, the **same set at every level** as a `git archive d23b897` export run on its own database. 1479 PASS and 6 SKIP in both |
| `golangci-lint run --new-from-rev d23b897` on the touched packages, with default, `integration` and `scenario` tags | 0 issues (after `2aa99d6`, which fixed one G115 and one stringXbytes from `de9c013`) |
| `--enable-only=gosec` | 0 issues. With `integration`: one G602 at `adapters/store/postgres_integration_test.go:1563`, a file this branch does not touch; the same issue is on `main` |
| `gofmt -l` on every `.go` file changed since `d23b897` | empty |
| `codex review --base main` (CLI 0.162.1, no model override) | round 1: "No actionable regressions were found in the changes." Model confirmed: the newest session with `source: {"subagent": "review"}` (`rollout-2026-10-14T12-56-38-01a13a45-8a5b-…`, cwd = this worktree) shows `"model":"gpt-6.1-sol"` |
| SMP compat (`i23/s-cx/smp` = SMP `f5c6fd6d` detached, `../spine` = this worktree, `GOFLAGS=-mod=readonly`, never modified) | `go build ./...`; `go vet` plain, `integration`, `scenario`, `faultinject`: OK. `dev/lint-spine-internals.sh`: OK. `go test -p 4` on the 43 SMP packages depending on a changed Spine package (`go list -test -deps`): 39 ok, 2 without tests, 2 FAIL. The failures are `gitread` `TestBounded_ErrorContractEqualsCLIClient` and `projectionwriter` `TestBoundedProjection_IncrementalAddAndModifyOversized` / `…OversizedWorkflowStalls`, the last a nil-pointer panic in `bpStore.GetWorkflowProjection` reached through `getWorkflowRow`. A `git archive f5c6fd6d` copy run against a Spine `d23b897` export gives the identical failure set and panic frame. They are pre-existing: SMP `main` pins `.spine-revision` `61994fe`, and its INIT-027 TASK-004 tests assert that revision's behaviour. With `-race` on the same 43 packages: the same result (39 ok, 2 without tests, the same 2 packages and failure set) and no data race reported |

All `cxp_*` databases were dropped afterwards. The only containers started were the `--rm` test
runs above.

### Adversarial review of the wave 4 follow-up (2026-10-20)

Recorded in full on TASK-003 ("Adversarial review of the follow-up"); fixed in `6fa432e4` on
`init/INIT-023-wave4-integration`, not pushed. Two findings touched this task's design:

- **The classifier's "a blob there means git failed" rule (P2).** `TreeEntryType` types an entry
  from its mode, and a regular-file entry can record the id of a tree, a commit or a tag. After
  the follow-up's `cat-file blob <oid>` read, such an entry failed with a plain `bad file`, and
  `classifyReadFailure` / `readFileEntry` took the mode's "blob" as proof that git had failed: the
  sync was held on every tick, a plantable wedge. The rule still holds, because the git client
  now proves the object's own type before it reads (`cat-file -t`) and refuses anything but a blob
  as `git.ErrNotBlob` with `NotBlobError.FileEntry`. The classifier consults that refusal before
  the tree (`notBlobEntry` → the new description `non_blob_object`), so the case is deterministic:
  a `not_a_file` rejection for a workflow, a held sync with the rules kept for branch protection,
  and a `not_a_file` skip for an artifact.
- **Review round 2's typing before the read (P1).** It typed the entry, not the object, so a
  file entry recording an annotated tag whose target is a review-less body read as that body (the
  tag was peeled), while diffs show the tag object's text. The same client check closes it.
  `TestProjection_FileEntryRecordingATagAt{WorkflowPathIsRejection,BranchProtectionIsHeld}` and
  `TestIncrementalSync_FileEntryRecordingATreeOrCommitAdvances` pin both; each fails without the
  fix. AC 28 (branch validation ignores `ReadErrors`) is unchanged: such a file is a refusal now,
  not a read error, so `TestValidateBranch_FileEntryRecordingANonBlobObjectFails` fails it.

### Status after GitHub CI (2026-11-04)

- **2026-11-04: stays In Progress.** Spine `main` was pushed on 2026-11-04 with the owner's
  approval. GitHub CI run 41875102030 at `main` `ce0b9c9e` (the INIT-024 closing commit `fd35f802`
  plus INIT-024 TASK-010's git-identity fix) passed all 8 jobs. Remaining (owner; gaps G3 and G4 of
  review round 2):
  - **AC 18 (third bullet) and AC 26:** a waiver or an amended AC for "`make test-scenario` passes"
    (the failures equal `main`'s).
  - **AC 24:** a waiver or a deferral for the `TEST_PROVISIONING_DSN` SMP suites.
