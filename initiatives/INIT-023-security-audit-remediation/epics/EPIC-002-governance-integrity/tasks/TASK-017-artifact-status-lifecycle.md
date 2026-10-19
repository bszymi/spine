---
id: TASK-017
type: Task
title: "Declared artifact-status lifecycle and run-owned merge cascade (opt-in)"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-16
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
  - type: related_to
    target: /governance/task-lifecycle.md
  - type: related_to
    target: /governance/artifact-schema.md
  - type: related_to
    target: /architecture/workflow-validation.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/tasks/TASK-016-cascade-commit-status-to-branch-artifacts.md
---

# TASK-017 — Declared artifact-status lifecycle and run-owned merge cascade (opt-in)

---

## Purpose

This task closes audit finding **P3-21** (Spine half). The finding is "artifact status transitions
aren't a state machine, the status-conflict rules only warn, and the merge-time cascade rewrites
`status:` to whatever the outcome says" (security audit 2026-10-06, Spine `61994fe`, SMP
`80995559`). The SMP half (P3-21.smp) is adopted by SMP INIT-027 EPIC-004 TASK-011.

Line numbers refer to Spine `61994fe` unless stated. "SMP" means `spine-management-platform` at
`80995559`. "Bound SHA" means the run-branch commit that TASK-005 binds the governed merge to.

### The defect

1. **Status is a plain front-matter field with no transition rules.**
   - Runs, steps, divergences and branches have explicit machines (`core/workflow/*_machine.go`).
     Artifact status has only an allowed-values list, `domain.ValidStatusesForType`
     (`core/domain/artifact.go:81-99`). Nothing says which status may follow which.
   - `governance/task-lifecycle.md` §4 (`:76-117`) states the rules in prose ("Completed → any",
     "Cancelled → any", "Abandoned → any" and "Draft → terminal" are prohibited). No code enforces
     them.
2. **A governed merge publishes any status change on the run branch.**
   - `MergeRunBranch` → `preflightCheck` (`core/engine/merge.go:65-110`) → `mergePrimary` merges
     the whole branch (`:166-172`).
   - No step compares an artifact's status on `main` with its status on the branch. A run author
     can reopen a Completed task, complete a sibling task or an initiative, or retire an ADR. The
     review approved the run's work, not these side edits.
3. **The cascade stamps artifacts the run does not govern.**
   - `applyCommitStatus` (`merge.go:599-684`) rewrites `status:` on `run.TaskPath` plus every `.md`
     artifact *added* on the branch (`cascadePaths`, `:688-719`, `d.Status == "added"` at `:704`).
     It does this in standard (execution) runs as well as planning runs.
   - The rewrite is a regex (`rewriteStatusField`, `:723-735`) with no check that the new status
     exists for that artifact's type, or that the old status may move to it.
   - The cascade was designed for planning runs: one creation review promotes the planned package
     Draft → Pending (INIT-008 TASK-016). A task-default run that adds a follow-up Task or an ADR
     therefore mints it `Completed`, and an ADR cannot hold that status.
   - *Correction to the plan summary:* today's cascade does **not** rewrite every artifact the
     author touched. Modified artifacts are not cascaded. Their direct status edits merge
     unchecked (item 2).
4. **Status-conflict rules never block.**
   - SC-001..SC-005 all emit `Severity: "warning"` (`core/validation/rules_status.go:34`, `:111`,
     `:144`, `:181`).
   - `Engine.Validate` (`core/validation/engine.go:233-251`) and `EvaluatePreconditions`
     (`core/validation/precondition.go:43-63`) only fail on errors. The engine's
     `cross_artifact_valid` check (`core/engine/step.go:758-783`) blocks only on
     `Status == "failed"`.
   - So a Completed epic with open tasks (SC-002), or a Completed initiative with open epics
     (SC-003), is never refused anywhere.
5. **Workflows can declare impossible targets.**
   - `workflow.Service.Create/Update` (`core/workflow/service.go:76-105`, `writeAndCommit`
     `:119-197`) run `workflow.Validate` (`:133-136`). `Validate` (`core/workflow/parser.go:490-508`)
     never looks at `commit.status`.
   - `architecture/workflow-validation.md` §5.5 (`:245-251`) and §7.1 (`:297-301`) already list
     "invalid `commit.status` for the governed artifact type" as an **error** (`:409`). It was
     never implemented.

**Reproduced (design-phase PoC, real `git.CLIClient` + real `MergeRunBranch`, via
`go test -overlay` on `61994fe`):**

- `TestPOC_P321_StandardRunPublishesAnyStatus`: standard run on `task-001` (Pending, outcome
  commit `Completed`). The branch also edits `task-002` Completed → Draft and `INIT-001`
  Pending → Completed, and adds `task-003` (Draft) and `ADR-0099` (Proposed).
  - Result: the merge completes.
  - `main` ends with `task-002` = **Draft**, `INIT-001` = **Completed**, `task-003` =
    **Completed** and `ADR-0099` = **Completed**.
- `TestPOC_P321_OwnedIllegalTransition`: a Completed task with outcome commit `Pending` lands on
  `main` as `Pending`.
- `TestPOC_P321_WorkflowCreateAcceptsForeignCommitStatus`: `workflow.Service.Create` commits an
  `applies_to: [Task]` workflow whose outcomes set `commit.status: Accepted` and `Shipped`, and
  `ValidateBody` returns `passed`.
- `TestPOC_P321_PlanningCascadeSequence`: the legitimate planning cascade (epic + 2 tasks, Draft →
  Pending) touches exactly the three added artifacts. This is the shape that must keep working.
- The audit's `poc/gap2-git-state-concurrenc/smp_cascade_sequence` was re-run against SMP
  `80995559`. It fails at `Merge pre-fetch: git fetch: exit status 1`, which is P3-2
  (`internal/customer/spinecore/gitplumbing.go:193`, owned by SMP TASK-002), not P3-21. It is used
  here only as the shape of the cascade-scope check (AC 8).

### Attackers and reach

- **A1 (run-branch content author: agent, runner job, contributor)** writes the status edits or
  the new artifacts.
- **A2 (workspace member)** in SMP can drive a run end to end (P3-3 self-assign; separate tasks).
- **Both surfaces.** SMP embeds the same `MergeRunBranch`, the same cascade and the same rules
  (`internal/customer/spinecore/factory.go:109-252`, `:275-295`). Standalone is the same code.

### Impact

- Integrity of the governed lifecycle only. There is no confidentiality and no availability
  impact.
- The status on `main` is what boards, blocking (`blocked_by` resolution), SC rules, workflow
  binding preconditions (`artifact_status`) and humans rely on.
- A run can therefore:
  - un-block or re-block other tasks;
  - make a task eligible for another run (Completed → Pending);
  - mark an initiative done;
  - mint "Completed" work that never had its own review.
- Severity P3 (corrected): it needs run-branch write access and a merged run. Combined with F2/F3
  it removes any independent guarantee that lifecycle status reflects a governance decision.

---

## Design

### 0. Invariants

1. **Opt-in.** Every behaviour below is gated by one of three options whose zero value is today's
   behaviour, byte for byte: a nil lifecycle on the orchestrator, a nil lifecycle on
   `workflow.Service`, and an empty blocking set on `validation.Engine`. The existing tests in
   `core/engine/merge_test.go:513-750` and `core/validation/engine_test.go:386` stay unchanged and
   green.
2. **Standalone `spine serve` turns all three on** (§6). SMP turns them on in SMP TASK-011.
3. **One choke point.** Everything a run changes reaches `main` through `MergeRunBranch`. The
   lifecycle is enforced there, against the **bound SHA** (TASK-005), before any ref advances.
   Write-time checks in `artifact.Service` cannot see runner pushes through the git gateway.

   > **Design-check note (2026-10-06) — DC-2: the lifecycle requires merge binding.**
   > TASK-005 is itself opt-in (`EnableMergeBinding`). With binding on, it moves the cascade out of
   > `preflightCheck` into `resolveMergeSource` step 3 (`applyCommitStatusAt`, which degrades to a
   > Warn on error and runs only when `status` is set). With binding off, the plan below would
   > read the run-branch tip while `mergePrimary` merges whatever the tip is later, so a push
   > between the two bypasses every rule (the F2 window). Therefore:
   > - The lifecycle plugs into TASK-005 §6 step 3, not into `preflightCheck` (§5 below is amended).
   > - `MergeRunBranch` with `o.lifecycle != nil` and binding off refuses with
   >   `domain.ErrUnavailable` ("status lifecycle requires merge binding"), like a nil policy. The
   >   run stays `committing`; nothing is merged. This is a configuration error, not a violation.
   > - Standalone `serve` already enables binding (TASK-005 §6/AC). SMP TASK-011 already depends on
   >   SMP TASK-009 (binding). So the requirement costs no adoption step.
4. **The "from" status is authoritative.** It is the status at the merge-base with `main`, never
   the run branch's own value, which the run author controls.
5. **Fail closed when on.** A git read, merge-base or diff failure, or a cascade write failure,
   keeps the run in `committing` for a retry. It never degrades to "merge without checking". TASK-012
   bounds the retries (P3-27).

   > **Design-check note (2026-10-06) — DC-6: only retryable errors are transient.**
   > "Any read failure retries" turns content the author controls into an endless `committing`
   > loop: TASK-003's `BlobTooLargeError` (a `.md` over 4 MiB, `ErrKindPermanent`), a gitlink or
   > other non-blob at a `.md` path, or a not-found for a path the diff itself lists. Rule:
   > `errors.As(*git.GitError)` with `IsRetryable()` (and non-git store/transport errors) keep the
   > run in `committing`. Every other read failure on a diff-listed path is a violation
   > `unreadable_artifact` (permanent, routed like any refusal). A `CommitFilesOnto` (binding-on
   > write) error stays transient.
6. **Create/update only for workflows.** The `commit.status` check runs in
   `workflow.Service.Create/Update/ValidateBody` only. It never runs in `workflow.Validate`, the
   projection or the loaders (the TASK-011 rule): a stricter projection-time check could drop a
   workflow that is already on `main`.
7. **No runtime schema change.**
   - The new step classification is a JSONB string (`step_executions.error_detail`, no CHECK).
   - The merge-outcome row reuses the existing `precondition` class. A new class would need
     `migrations/022_repository_merge_outcomes.sql:45-50` changed.
8. **No interface additions SMP must implement.** `GitOperator`, `RunStore` and
   `CrossArtifactValidator` are unchanged. The new validator hook is an optional interface found by
   type assertion.
9. **The status that is validated is the status that lands.** *(Added by design check, DC-1/DC-8.)*

   > **Design-check note (2026-10-06).** The plan validates parsed YAML (`LifecycleFields`), but
   > the bytes that land are written by the regex `rewriteStatusField` (`merge.go:723-735`) and
   > then combined by `git merge`. Both can disagree with the parse (design-check PoCs, Design
   > check section). The design therefore re-parses every rewritten file before the cascade commit
   > (R1, §5) and checks the merge result before any push (V1, §5).
10. **Same path scope as the projection.** *(Added by design check, DC-5.)* The lifecycle judges
    exactly the paths the projection treats as artifacts, keyed the same way (§5 "Path scope").

### 1. `core/domain/artifact_lifecycle.go` (new): the declared table

```go
// StatusLifecycle declares, per Markdown artifact type, the statuses an
// artifact may be created with and the status transitions a governed change
// may make. Immutable; safe for concurrent use. A nil *StatusLifecycle is
// "no enforcement" wherever it is accepted as an option.
type StatusLifecycle struct{ types map[ArtifactType]typeLifecycle }

type StatusTransition struct{ From, To ArtifactStatus }

func DefaultStatusLifecycle() *StatusLifecycle                       // the seeded table below
func (l *StatusLifecycle) Types() []ArtifactType                     // sorted
func (l *StatusLifecycle) States(t ArtifactType) []ArtifactStatus    // declared states, sorted
func (l *StatusLifecycle) IsState(t ArtifactType, s ArtifactStatus) bool
func (l *StatusLifecycle) IsInitial(t ArtifactType, s ArtifactStatus) bool
// Allows reports whether from -> to is a declared edge. Identity (from == to)
// is allowed when from is a declared state. Unknown types and undeclared
// statuses are never allowed.
func (l *StatusLifecycle) Allows(t ArtifactType, from, to ArtifactStatus) bool
func (l *StatusLifecycle) Transitions(t ArtifactType) []StatusTransition // sorted, for docs and golden tests
```

**Seeded table.** It is seeded so that every `commit.status` effect in both shipped corpora (§7)
is accepted. Prohibitions follow `governance/task-lifecycle.md` §4.3. Every terminal status is
final.

| Type | Initial (may be created with) | Transitions |
|---|---|---|
| Initiative, Epic | Draft, Pending | Draft → Pending, Superseded · Pending → In Progress, Completed, Superseded · In Progress → Completed, Superseded |
| Task | Draft, Pending | Draft → Pending, Cancelled · Pending → In Progress, Completed, Cancelled, Rejected, Superseded, Abandoned · In Progress → Completed, Cancelled, Rejected, Superseded, Abandoned |
| ADR | Proposed | Proposed → Accepted, Deprecated, Superseded · Accepted → Deprecated, Superseded |
| Governance | Living Document, Foundational | Living Document ↔ Foundational · Living Document → Superseded · Foundational → Superseded |
| Architecture, Product | Living Document, Stable | Living Document ↔ Stable · Living Document → Superseded · Stable → Superseded |

- **Final statuses:** Completed, Cancelled, Rejected, Abandoned, Superseded and Deprecated have no
  outbound edge.
- **`Workflow` is not in the table.** Workflow definitions carry no status front matter.
- **Task `In Progress`.**
  - It is declared because SMP's `task-default` commits it
    (`workflows/task-default.yaml:41-45` in SMP) and SMP's board maps it
    (`internal/customer/board/seeder.go:59`).
  - It is **not** in `ValidStatusesForType(Task)` (`artifact.go:87`), and
    `governance/task-lifecycle.md` §3.3 calls it runtime-only. This task does not change either
    (Owner question 3).
- **States ⊇ `ValidStatusesForType(t)`** for every type, so no status that validates today
  becomes undeclared.

> **Design-check note (2026-10-06) — DC-9: undeclared statuses already on `main`.** "Validates
> today" is not "present today": `artifact.Parse` never checks status values, so real repositories
> hold undeclared ones. Counted on the two repos at the audited commits (`.md` with front matter,
> `templates/` and `tmp/` excluded): Spine has `Product: Draft` (1); SMP's own repo has
> `Architecture: Active` (7), `Product: Draft for Review` (2) and `ADR: Superseded by ADR-027` (1).
> Customer workspaces can hold the same. Consequences, by rule:
> - N1 compares raw values, so a run that edits such an artifact's body without touching its
>   status merges (pinned by AC 25 so an implementation does not refuse undeclared bases).
> - A run that normalises the status (e.g. `Active` → `Living Document`) is refused
>   `non_owned_status_change`; a run that *owns* such an artifact is refused `undeclared_status`.
>   Normalisation goes through a direct authoritative write (out of scope, follow-up 2).
> - `KNOWN-LIMITATIONS.md` gets this residual (§10).

### 2. `core/artifact/parser.go`: one reader for type and status

Add `LifecycleFields(content []byte) (domain.ArtifactType, domain.ArtifactStatus, bool)` next to
`IsArtifact` (`:249-272`).

- It uses the same `splitFrontMatter` + `yamlsafe.Decode` + struct decode as `Parse`, so the
  lifecycle reads exactly what the projection reads. No second parser and no regex read.
- `ok` is false when there is no front matter or `type` is not in `ValidMarkdownArtifactTypes()`.
- The status may be `""` (status line removed). The caller treats that as a status change.

> **Design-check note (2026-10-06).** `LifecycleFields` is also the reader for R1 (re-parse of
> every rewritten file) and V1 (merge-result check) in §5, so all three judgements use one
> decoder. Verified on `61994fe`: yaml.v3 rejects a duplicate `status:` key ("mapping key already
> defined"), so such a file is `ok == false`; with DC-3 that is a refusal for a non-owned artifact
> that existed at base, not "stopped being an artifact, ignored".

### 3. `core/workflow`: commit-status check at create/update

- **New `commit_status.go`:**
  ```go
  // ValidateCommitStatuses implements architecture/workflow-validation.md §5.5
  // rule 1: every outcome's commit.status must be a declared lifecycle status of
  // every applies_to type. nil lifecycle → nil. Never called from Validate.
  func ValidateCommitStatuses(wf *domain.WorkflowDefinition, l *domain.StatusLifecycle) []domain.ValidationError
  ```
  - Every step's outcomes are walked, and `commit["status"]` is checked when it is non-empty.
  - `applies_to: Workflow` with a `commit.status` is an error. The `merge` key is ignored.
  - Error shape: `RuleID: "commit_status"`, `Severity: "error"`,
    `Field: "steps[i].outcomes[j].commit.status"`. Message:
    `commit.status "Accepted" is not a status of Task (declared: …)`.
- **`Service`** (`service.go:44-52`) gets a `lifecycle *domain.StatusLifecycle` field and a
  setter, `func (s *Service) WithStatusLifecycle(l *domain.StatusLifecycle)`.
  - This is the setter style of `artifact.Service.WithPolicy` and `Orchestrator.With*`.
    `NewService`'s signature is unchanged (TASK-014 also edits this type, see §9).
- **`writeAndCommit`** (`:133-136`): after `Validate(wf)` passes, refuse with
  `ErrValidationFailed` + detail when `ValidateCommitStatuses` returns errors. This happens
  **before** `enterBranch` (`:138`), so nothing is written or committed.
- **`ValidateBody`** (`:273-287`): append the same errors when `s.lifecycle != nil`, so the
  validate preview matches create.
- **Untouched:** `Validate` / `ValidateSemantic` (`parser.go:470-508`), the projection, and the
  binding/loader paths.

### 4. `core/validation`: blocking status conflicts

- **`engine.go`:**
  ```go
  var DefaultBlockingStatusConflicts = []string{"SC-002", "SC-003"}
  // WithBlockingStatusConflicts promotes the named SC-* findings from warning
  // to error. Unknown IDs are ignored (a unit test pins the default list).
  func WithBlockingStatusConflicts(ids ...string) Option
  func (e *Engine) BlockingStatusConflicts() []string // for wiring tests
  ```
- **Promotion is engine-level.** In the `Validate` loop (`:233-243`), a result whose rule ID is
  in the set and whose severity is `"warning"` becomes `"error"`.
  - `rules_status.go` is unchanged.
  - `ValidateAll`, `EvaluatePreconditions` and `cross_artifact_valid` inherit the promotion.
- **New merge-time hook:**
  ```go
  // ValidateStatusChange evaluates the SC-* rules against the projected artifact
  // as if its status were newStatus (promotion applied). Not found in the
  // projection → passed, nil. Store failure → error. Nil engine → passed.
  func (e *Engine) ValidateStatusChange(ctx context.Context, artifactPath string, newStatus domain.ArtifactStatus) (domain.ValidationResult, error)
  ```
  It copies the `ArtifactProjection`, sets `Status`, and runs only the status-conflict rules.
  Children's statuses come from the projection. That is exact because the merge refuses
  non-owned status edits (§5, rule N1).

### 5. `core/engine`: run-owned cascade and the merge-time lifecycle

**Option.**

- `orchestrator.go:12-38` gets a field `lifecycle *domain.StatusLifecycle`.
- Setter `func (o *Orchestrator) WithStatusLifecycle(l *domain.StatusLifecycle)`, and accessor
  `StatusLifecycle()`.

**Optional validator hook** (`interfaces.go`, next to `CrossArtifactValidator` at `:85-87`):

```go
// StatusChangeValidator is implemented by *validation.Engine. When the
// orchestrator's validator implements it and the lifecycle option is on, owned
// status changes are also checked against the blocking SC rules.
type StatusChangeValidator interface {
	ValidateStatusChange(ctx context.Context, artifactPath string, newStatus domain.ArtifactStatus) (domain.ValidationResult, error)
}
```

**New file `status_lifecycle.go`.**

```go
var ErrStatusLifecycleViolation = errors.New("status lifecycle violation")

type StatusLifecycleViolation struct {
	Path     string
	Type     domain.ArtifactType
	From, To domain.ArtifactStatus
	Reason   string   // stable codes below
	Rules    []string // SC rule IDs for reason "status_conflict"
}

type StatusLifecycleError struct{ Violations []StatusLifecycleViolation }

func (e *StatusLifecycleError) Error() string // "status_lifecycle_violation: <path>: <reason> (<type> <from> -> <to>); …" (first 20, then "and N more")
func (e *StatusLifecycleError) Is(target error) bool // == ErrStatusLifecycleViolation

// planStatusLifecycle evaluates the bound diff. It returns the owned paths to
// rewrite (with the content it read at the bound SHA) or the violations. A git
// error is returned as-is (transient; the caller keeps the run in committing).
func (o *Orchestrator) planStatusLifecycle(ctx context.Context, run *domain.Run, newStatus string) (*lifecyclePlan, error)
```

**Inputs.**

- `tip` is the ref TASK-005's `applyCommitStatus` reads, the bound SHA.
- `base := MergeBase(main, tip)`.
- `diffs := Diff(base, tip)`.
- Both use the existing `GitOperator` methods (`interfaces.go:161-175`).

> **Design-check note (2026-10-06) — DC-2: `tip` is `bound`, always.** With DC-2 the lifecycle
> runs only under binding, so `tip` is `run.CommitMeta["spine.bound_sha"]` as validated by
> TASK-005 `resolveMergeSource` steps 1–2 (`VerifyCommit`). It is never `run.BranchName`.

> **Design-check note (2026-10-06) — DC-5: path scope and `artifacts_dir`.** `run.TaskPath` is
> *artifact-relative*: `StartRun` reads it through `artifact.Service.Read`
> (`core/engine/run.go:132`), which prefixes `artifacts_dir` (`core/artifact/service.go:84-92`).
> `Diff` paths are *repo-relative*. Today's cascade reads `run.TaskPath` raw
> (`merge.go:606`), so with `artifacts_dir: spine` (the `spine init-repo` default,
> `service/cmd/spine/cmd_init_repo.go:27`) the TaskPath read fails, the error is swallowed, and
> `commit.status` is silently not applied (design-check PoC `artifacts_dir_test.go`). With the
> lifecycle on, the same miss would become `not_an_artifact` on every such run, and the run's own
> repo-relative file would be judged non-owned. Therefore:
> - New setter `Orchestrator.WithArtifactsDir(dir string)` (zero value = repo root, today's
>   behaviour; SMP sets no `ArtifactsDir`, so SMP needs no call). Standalone wiring passes
>   `spineCfg.ArtifactsDir` in `buildOrchestrator` and the per-workspace orchestrator (§6).
> - `ownedKey := repoRelative(run.TaskPath)`; reads and rewrites use repo-relative paths.
> - Only diff paths the projection treats as artifacts are judged: inside `<dir>/` when a dir is
>   set, ending `.md`, and not under `templates/` (`artifact.IsTemplatePath` on the stripped
>   path). This is TASK-011 §5's `pathScope.artifactKey`; to share it without an
>   `adapters/projection` import from `core/engine`, TASK-011 or this task moves the predicate to
>   `core/artifact` (exported, e.g. `artifact.ScopeKey(artifactsDir, repoPath)`). Out-of-scope
>   paths are ignored because the projection never reads them.
> - **Depends on TASK-011 (P3-23).** Until incremental sync is scoped, an out-of-tree
>   `initiatives/…/task.md` collides with the governed `spine/initiatives/…/task.md` row, so an
>   *ignored* out-of-scope addition could rewrite a governed status in the projection.

**Ownership** (Owner question 2):

- `run.TaskPath` is always owned.
- In a **planning** run (`run.Mode == domain.RunModePlanning`), every `.md` artifact with diff
  status `added` is also owned (the TASK-016 package cascade).
- In a **standard** run, added artifacts are **not** owned and are not cascaded.
- Renamed, modified and deleted paths are never owned.

> **Design-check note (2026-10-06) — DC-7: renames onto the TaskPath.** "Renamed paths are never
> owned" and "`run.TaskPath` is always owned" collide when the diff reports the TaskPath as a
> rename target. Example (planning run): the branch deletes its drafted TaskPath and
> `git mv`s a non-owned `Completed` task onto it; `git diff --name-status` reports
> `R100 task-002.md → <TaskPath>`. Rule: the TaskPath stays owned, but whether it "exists at
> `base`" is judged through `OldPath` when the entry is a rename. So this case is O1 with
> `S_base = Completed` and `final = Pending` → `illegal_transition`, not O2 (which would stamp the
> completed task `Pending` under a new path). The `OldPath` side is also checked by DC-3 (N3).

**Rules.** For each `.md` path that is a lifecycle artifact (`LifecycleFields` ok) at `tip`
(type `T`, tip status `S_tip`). `S_base` is its status at `base`; for a rename, that is
`OldPath`'s status. `final` is `newStatus` if the path is owned and `newStatus != ""`, else
`S_tip`.

| # | Case | Rule | Reason code on failure |
|---|---|---|---|
| O1 | Owned, exists at `base` | `T` unchanged and `Allows(T, S_base, final)` | `type_changed`, `undeclared_status`, `illegal_transition` |
| O2 | Owned, new (planning additions; TaskPath absent at `base`) | `IsState(T, final)` | `status_not_in_type` |
| O3 | `run.TaskPath` with `newStatus != ""` but not a lifecycle artifact at `tip` | — | `not_an_artifact` |
| O4 | O1 transition with `S_base != final`, validator implements `StatusChangeValidator` | no blocking (`Severity: error`) finding | `status_conflict` (+ rule IDs) |
| N1 | Not owned, exists at `base` (modified/renamed) | `T` unchanged and `S_tip == S_base` | `type_changed`, `non_owned_status_change` |
| N2 | Not owned, new (standard-run additions; a modified file that becomes an artifact) | `IsInitial(T, S_tip)` | `illegal_initial_status` |
| Q | Diff path that is C-quoted and ends `.md"` | — (transitional guard until TASK-003 / SMP TASK-003 land `-z`) | `unparseable_path` |

> **Design-check note (2026-10-06) — rows added or changed by the design check.**
>
> | # | Case | Rule | Reason code on failure |
> |---|---|---|---|
> | O1′ (DC-4) | Owned, exists at `base`, `newStatus == ""` | `T` unchanged and `S_tip == S_base`: with no declared effect, the run may not change its own artifact's status either | `status_change_without_effect` |
> | O2′ (DC-4) | Owned, new, `newStatus == ""` | `IsInitial(T, S_tip)` (the N2 rule) | `illegal_initial_status` |
> | O3′ (DC-3) | `run.TaskPath` (`.md`) that is a lifecycle artifact at `base` but not at `tip` (deleted, unparseable, retyped), whatever `newStatus` is | — | `not_an_artifact` |
> | N3 (DC-3) | Not owned, a lifecycle artifact at `base`, and at `tip` deleted, renamed to an out-of-scope or non-`.md` path, or no longer `LifecycleFields`-ok (unparseable — e.g. a duplicate `status:` key —, retyped to an unknown type, replaced by a symlink or gitlink) | — | `non_owned_removal` |
> | R1 (DC-1) | Owned path rewritten to `final` | `LifecycleFields(rewritten)` is ok, same `T`, status `== final`; and a rewrite that leaves the bytes unchanged while `final != S_tip` is a failure | `status_rewrite_failed` |
> | U (DC-6) | Any diff-listed path whose read at `base` or `tip` fails non-retryably | — | `unreadable_artifact` |
> | Q′ (DC-7) | Rule Q also applies to a C-quoted `OldPath` | — | `unparseable_path` |
>
> - **Why O2 needs O2′.** O2 only asks `IsState(T, final)`. With no `commit.status`, `final` is
>   the author's own `S_tip`, so a planning run whose workflow declares no status effect
>   (`workflow-lifecycle` is planning mode, `core/engine/run.go:349`, `merge` only) could add a
>   Task authored `Completed` and land it `Completed` — exactly the "mint Completed work" impact.
>   D6's "normalised to the declared target" argument only holds when a target exists.
> - **Why O1′.** Likewise, an owned TaskPath under a workflow whose merging path sets no
>   `commit.status` could move itself along any legal edge (e.g. `Pending → Completed`) that the
>   workflow never declared. No shipped workflow merges a `.md` TaskPath without a status effect
>   (§7 corpus), so this costs nothing today. Owner question 7.
> - **Why N3.** "Deleted, or stopped being an artifact → ignored" (below) leaves three P3-21
>   impacts open: (a) *reopen by recreate* — delete a `Completed` task and re-add the same `id` at
>   another path with < 50 % similarity, so `git diff` reports `D` + `A` instead of `R`; the add
>   passes N2 as `Draft`, or in a planning run is owned and stamped `Pending`; (b) *re-block* — a
>   missing blocker counts as blocking (`core/engine/blocking.go:62-66`,
>   `adapters/projection/service.go:478-482`), so deleting one blocker re-blocks every dependent;
>   (c) *freeze* — an unparseable file is skipped by `DiscoverChanges`, leaving a stale projection
>   row. Deletion of an artifact *added on the same branch* has no base and is unaffected; the
>   owned TaskPath is covered by O3′. No shipped Spine or SMP flow deletes governed artifacts on a
>   run branch (checked: no delete call in `core/engine`, none in SMP `internal/`). Owner
>   question 6.

**Further rules.**

- Deleted paths, and paths that stop being artifacts, are ignored (follow-up 1).
  - **Design-check note (2026-10-06):** superseded by N3 for non-owned artifacts that existed at
    `base`. Still ignored: paths with no lifecycle artifact at `base`.
- Non-`.md` paths are ignored, except that a non-`.md` TaskPath is skipped silently when
  `newStatus == ""` (workflow-lifecycle).
- A `ValidateStatusChange` store error is transient, not a violation.

**`applyCommitStatus` (`merge.go:599-684`), as TASK-005 leaves it.**

- When `o.lifecycle != nil`:
  - The path set and contents come from `planStatusLifecycle` instead of `cascadePaths` and the
    per-path read loop.
  - Violations return `*StatusLifecycleError` **before** any checkout or write.
  - The rewrite (`rewriteStatusField`) and the single cascade commit (message and trailers
    unchanged) run only for owned paths whose `final != S_tip`.
- When `o.lifecycle == nil`, today's code path runs unchanged (`cascadePaths`, best-effort
  degrade).

> **Design-check note (2026-10-06) — DC-2: the hook is `applyCommitStatusAt`, not
> `applyCommitStatus`.** Under binding, TASK-005 §7 replaces the cascade with
> `applyCommitStatusAt(ctx, run, status, bound)` (paths from `cascadePathsAt(bound)`, reads at
> `bound`, writes through `CommitFilesOnto(bound, …)`, branch ref never moves), called from
> `resolveMergeSource` step 3, and leaves `applyCommitStatus` for binding-off only. With the
> lifecycle on:
> - `resolveMergeSource` step 3 calls the lifecycle variant **even when `status == ""`**, and does
>   not apply TASK-005's "on error, log at Warn and use `bound`": a `*StatusLifecycleError` goes to
>   `refuseStatusLifecycle`; any other error returns (run stays `committing`).
> - The paths and contents come from `planStatusLifecycle(…, bound)`; the cascade commit is
>   `CommitFilesOnto(bound, updates)` with today's message and trailers, so `src.sha`'s parent is
>   `bound` and the merge source is exactly the evaluated tree plus the verified rewrites.
> - **R1** runs on each rewrite before `CommitFilesOnto`: `rewriteStatusField` output must
>   re-parse through `LifecycleFields` to the same type and `final`. Verified on `61994fe`
>   (design-check PoCs `rewrite_differential_test.go`, `owned_quoted_key_test.go`): the regex
>   `(?m)^status:\s*.*$` (`merge.go:585`) does not touch `"status": X`, `'status': X`,
>   `status : X` or `? status\n: X`, which yaml.v3 reads as the status; a continuation line
>   (`status: Pending\n  Completed`) parses as `"In Progress Completed"` after the rewrite. Through
>   the real `MergeRunBranch`, an owned task authored `"status": Completed` with outcome
>   `commit.status: In Progress` (SMP `task-default` ≤ 8.0) lands **`Completed`**; O1 alone checks
>   only `Pending → In Progress` and passes. SMP's task-action rewrite
>   (`internal/customer/spineembedded/embedded_tasks.go:314-340`) has the same regex; it is a
>   direct write (follow-up 2).
> - `applyCommitStatus` (binding off) is untouched, so `merge_test.go:513-750` stays green.

> **Design-check note (2026-10-06) — DC-8: V1, the merge-result check.** D3 says a main-side
> status change since the merge-base "makes git conflict on the `status:` line anyway". That is
> false when `main` carries `*.md merge=union` in `.gitattributes`: git keeps both lines, the
> merge succeeds, and the file lands with two `status:` keys (unparseable, so it freezes in the
> projection). Design-check PoC `union_merge_test.go` reproduces it through the real
> `MergeRunBranch` on `61994fe` (owned task `Pending` at base, `Cancelled` on `main` after the
> fork, outcome `Completed`). A `.gitattributes` on `main` needs a reviewed merge (TASK-006 does
> not list it as a governed path), so this is defence in depth, but it is cheap:
> - In TASK-005's `verifyBoundMerge`, after `IsAncestor(src.sha, M)` and **before any push**, for
>   every path the plan judged: `E(p)` is `final` for an owned path that is new at `base` or whose
>   status the plan changes (`final != S_base`), `S_tip` for a non-owned path new at `base`, and
>   otherwise the status of `p` at `mergeResult.BaseSHA` (main before the merge). `LifecycleFields(M:p)` must be ok, with the
>   plan's type and status `E(p)`.
> - A mismatch is a `*StatusLifecycleError` with reason `merge_result_mismatch`, handled exactly
>   like TASK-005's post-merge violations (outcome recorded, `handlePermanentMergeFailure`, nothing
>   pushed). Its standalone caveat (the local `main` already holds `M` when auto-push is off) is
>   TASK-005's and TASK-012's to resolve, not a new one.
> - Cost: one read at `M` and at most one at `BaseSHA` per judged path. It also catches any
>   residual rewrite/merge differential and a criss-cross history where `git merge-base` returns a
>   different base than the one `git merge` uses. Owner question 8.

**`preflightCheck` (`merge.go:100-107`).**

> **Design-check note (2026-10-06):** with DC-2 this hunk applies only if the owner rejects the
> binding requirement (Owner question 9). Under binding, TASK-005 already restricts
> `preflightCheck`'s cascade block to binding-off, and the lifecycle lives in
> `resolveMergeSource` (note above). The ordering claim of §9 ("lifecycle after the
> branch-protection/path guard") still holds: TASK-006's guard runs in `preflightCheck`, before
> `resolveMergeSource`.

```go
if o.lifecycle != nil {
	if err := o.applyCommitStatus(ctx, run, run.CommitMeta["status"]); err != nil {
		if le := (*StatusLifecycleError)(nil); errors.As(err, &le) {
			return nil, true, o.refuseStatusLifecycle(ctx, run, le)
		}
		return nil, false, fmt.Errorf("apply commit status: %w", err) // fail closed: stays committing
	}
} else if newStatus := run.CommitMeta["status"]; newStatus != "" {
	// unchanged: log.Warn and proceed (contract at :62-64)
}
```

- With the option on, the evaluation runs even when there is no `commit.status`, because non-owned
  edits still have to be checked.
- The doc comment at `:52-64` is updated.
- TASK-004 OQ3 decides the option-off swallow. This task only changes the option-on path.

**Refusal (`refuseStatusLifecycle`).**

1. `log.Warn` and `observe.AuditLog(ctx, "status_lifecycle_refused", "run_id", …, "violations", …)`.
2. `recordPrimaryMergeOutcome(ctx, run, "", le, "")`. If that errors, return the error (the run
   stays committing).
3. `handlePermanentMergeFailure(ctx, run, le)` (`:309-322`).
   - With a publish step: `merge_failed` → the run is routed back (`handlers.go:206-236`).
   - Without one: the run fails (`failRunOnMergeError`).
   - No ref has advanced. Code-repo merges have not started, because preflight runs before
     `mergeCodeRepos`.

> **Design-check note (2026-10-06) — refusal routing.**
> - Under DC-2 the refusal happens in `resolveMergeSource`, which also runs before
>   `mergeCodeRepos` (TASK-005 §6 order), so the "nothing advanced" claim holds. A V1
>   (`merge_result_mismatch`) refusal happens after the local merge and before any push, with
>   TASK-005's post-merge handling.
> - Only Spine's `task-default` has a publish step (`workflows/task-default.yaml:106-111`). Every
>   other Spine workflow (`epic-lifecycle`, `artifact-creation`, `adr*`, `document-creation`,
>   `task-spike`) therefore **fails the run** on any refusal; the author starts a new run.
> - Liveness (Owner question 10): a violation the author cannot fix on the branch — the workflow's
>   own target is illegal from `main`'s state (e.g. `task-default` started on a task that was
>   `Completed` on `main`), or `S_base` is undeclared (DC-9) — routes back to `execute` on every
>   attempt in a publish-step workflow. Default: keep the route-back (the classification and
>   detail are visible); the alternative is to fail the run for violations whose `To` is the
>   workflow's `newStatus` and whose `From` is `S_base`.
> - SC gate lag: O4 reads the projection, so within one sync interval after the last child merges
>   (standalone polls every 30 s, `serve_wiring.go:287`) an epic completion can be refused
>   `status_conflict` although `main` is consistent. For `epic-lifecycle` (no publish step) that
>   fails the run. Recorded in `KNOWN-LIMITATIONS.md` (§10); follow-up 7 removes it.

**Distinct classification.**

- `core/domain/step.go:42-48`: add `FailureStatusLifecycle FailureClassification =
  "status_lifecycle_violation"` (not retryable).
- `recordGitConflictOnStep` (`merge.go:471-509`): `errors.As(*StatusLifecycleError)` →
  `FailureStatusLifecycle`.
- `classifyMergeFailure` (`multi_repo_merge.go:293-341`): first branch, `*StatusLifecycleError` →
  `(domain.MergeFailurePrecondition, err.Error())`. The detail therefore starts
  `status_lifecycle_violation:`. There is no new `MergeFailureClass` and no migration.

### 6. Standalone wiring (`service/`)

- `serve_wiring.go:281` and `internal/workspace/pool.go:948`:
  `workflowSvc.WithStatusLifecycle(domain.DefaultStatusLifecycle())`.
- `serve_wiring.go:334-340` and `pool.go:969-974`: add
  `validation.WithBlockingStatusConflicts(validation.DefaultBlockingStatusConflicts...)`.
- `serve_builders.go:249-283` (`buildOrchestrator`) and `serve_resolver.go:64-90` (per-workspace
  orchestrator): `orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())`.
- The gateway's workflow create/update/validate and reviewer planning-run workflow writes go
  through these services, so they inherit the check. No new environment variable (Decision D9).
- **Scenario harness.** Add a `harness.WithStatusLifecycle()` `EnvOption`
  (`scenariotest/harness/environment.go`) that sets all three on the runtime it builds
  (`runtime.go:108-170`). The default stays off, so existing scenarios are unchanged.
- `cmd/embed-smoketest` is unchanged (zero value).

> **Design-check note (2026-10-06) — wiring additions.**
> - DC-5: `buildOrchestrator` and the per-workspace orchestrator also call
>   `orch.WithArtifactsDir(<that workspace's spineCfg.ArtifactsDir>)` (`serve_wiring.go:288` and
>   `pool.go:956` already pass the same value to the projection). That makes eight wiring lines;
>   AC 13 covers all of them.
> - DC-2: both orchestrators must also have binding on (TASK-005 wires it). AC 13 asserts
>   `MergeBindingEnabled()` alongside `StatusLifecycle() != nil`.
> - `harness.WithStatusLifecycle()` must also enable binding (TASK-005's harness option, or the
>   same call), or every scenario using it refuses with the DC-2 configuration error.
> - Behaviour change to record in `CHANGELOG.md`: with the blocking set on, standalone
>   `GET /system/validate` (`service/internal/gateway/handlers_system.go:151`) and `spine validate`
>   report SC-002/SC-003 as errors (status `failed`) where they reported warnings. No shipped
>   workflow declares a `cross_artifact_valid` precondition (checked in both corpora), so step
>   activation is unaffected by the promotion.

### 7. Compatibility gate (both shipped corpora)

Every `commit.status` effect and the transition it produces at merge must pass with the options
on.

- `workflows/*.yaml` are parsed at test time for Spine.
- SMP's corpus is pinned as table rows, with its `file:line`, because Spine CI does not check out
  SMP. SMP TASK-011 re-runs the same gate against SMP's live files.

| Corpus | Workflow (applies_to) | `commit.status` (file:line) | Merge-time transition checked |
|---|---|---|---|
| Spine | adr-creation (ADR, planning) | Accepted `:68-72`, Deprecated `:76-80` | new Proposed → Accepted / Deprecated (O2) |
| Spine | adr-review `adr.yaml` (ADR) | Accepted `:47-51`, Deprecated `:52-56` | Proposed → Accepted / Deprecated (O1) |
| Spine | artifact-creation (Initiative, Epic, Task; planning) | Pending `:79-83` | new Draft → Pending, TaskPath + children (O2) |
| Spine | document-creation (Governance, Architecture, Product; planning) | Living Document `:72-76` | new Living Document → identity, no cascade commit (O2) |
| Spine | epic-lifecycle (Epic) | In Progress `:31-35`, Completed `:69-73` | Pending → In Progress / Completed; In Progress → Completed (O1) |
| Spine | task-default (Task) | Completed `:83-87` | Pending → Completed (O1) |
| Spine | task-spike (Task) | Completed `:31-35`, `:74-78` | Pending → Completed (O1) |
| Spine | workflow-lifecycle (Workflow) | `merge` only `:57-65` | no status; TaskPath `.yaml` skipped |
| SMP | adr-creation | Accepted `:70-74`, Deprecated `:78-82` | as Spine |
| SMP | artifact-creation | Pending `:108-112` | new Draft → Pending; CreateEntry authors `status: Draft` (`internal/customer/spineembedded/artifact_entry_helpers.go:160`) |
| SMP | document-creation | Living Document `:74-78` | identity; authored `Living Document` (`artifact_entry_helpers.go:288`) |
| SMP | epic-lifecycle | In Progress `:30-34`, Completed `:66-70` | as Spine |
| SMP | task-default (Task) | In Progress `:41-45`, Completed `:155-159` | **Pending → In Progress** (see note) |
| SMP | task-spike | Completed `:30-34`, `:78-82` | Pending → Completed |

**Note on SMP `task-default`.** This is pre-existing and outside this finding, but it matters to
SMP TASK-011.

- `commit.status: Completed` sits on the engine-owned `publish.published` outcome. That outcome
  is advanced *after* the merge, by `advancePublishStepIfAny` (`core/engine/handlers.go:238-243`).
  Only `merge_failed` is routed through `routeStepOutcome`, the one place that calls
  `SetCommitMeta` (`core/engine/step.go:396-401`).
- So the merge applies the `In Progress` that `execute.completed` set. Once SMP TASK-002 fixes
  P3-2, SMP tasks will land on `main` as **In Progress**, not Completed.
- Spine's own `task-default` puts `Completed` on `review.accepted` and does not have this problem.
- The table accepts SMP's actual transition. The consequence for blocking SC-002 is handed to SMP
  TASK-011 (§8).

### 8. SMP: effect and what SMP TASK-011 must change

**Without SMP TASK-011, nothing changes for SMP.**

- All three options are off, `workflow.NewService`'s signature is unchanged, and no interface SMP
  implements gains a method.
- SMP `80995559` builds against this commit unchanged (AC 15).
- The new `status_lifecycle_violation` string has no SMP enum to update (SMP has no switch over
  classifications).

**SMP TASK-011 (adopts P3-21.smp) must do the following.**

1. **Orchestrator.** Call `orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())` in
   `newOrchestratorWithRunStore` (`internal/customer/spinecore/factory.go:69-252`, next to
   `WithBranchProtectPolicy` at `:209`).
   - This one site covers `NewOrchestrator`, `Cache.Get` and `NewPlanningOrchestrator`
     (`planningwriter.go:141-175`).
2. **Workflow writes.** Call `svc.WithStatusLifecycle(domain.DefaultStatusLifecycle())` after
   `workflow.NewService` in `NewWorkflowWriter` (`workflowwriter.go:343`).
3. **Workflow validate preview.** `embeddedWorkflowValidate`
   (`internal/customer/spineembedded/workflows.go:317-331`) mirrors `ValidateBody`. It must append
   `workflow.ValidateCommitStatuses(wf, domain.DefaultStatusLifecycle())` errors so the validate
   preview matches create.
4. **Blocking set.** Choose it in `NewValidator` (`factory.go:275-295`).
   - Promoting SC-002 while `task-default` leaves tasks `In Progress` (§7 note) would refuse every
     `epic-lifecycle` completion.
   - SMP TASK-011 either fixes `task-default` first, or ships with SC-003 only / no promotion and
     records why. The fix is: move `commit: {status: Completed}` to `review.accepted` as Spine
     does, bump the version and backfill through INIT-020 seeding.
5. **SMP-side gate.**
   - Every SMP `workflows/*.yaml` passes `ValidateCommitStatuses` with no errors (10
     `commit.status` outcomes).
   - Drive the real `MergeRunBranch` over `RealGitOperator` with the option on:
     - an artifact-creation planning run (Epic + 2 Tasks, Draft → Pending) merges and cascades
       exactly those three files;
     - the `task-default` shape (Pending → In Progress) merges;
     - a run that edits a non-owned task's status is refused, and `main` is unchanged.
   - Build this on the audit's `smp_cascade_sequence` PoC, which SMP TASK-002 commits as the P3-2
     regression.
6. **Revision bump.** Move `.spine-revision` to a Spine commit that contains this task. SMP
   TASK-012 does the closing bump.

> **Design-check note (2026-10-06) — SMP references and additions.**
> - SMP TASK-003 (wave 1, `batch7/E4-TASK-003-validation-path-correctness.md`) deletes
>   `factory.go:275-295`; the blocking set is then chosen in `newValidationEngine`
>   (`internal/customer/spinecore/validator.go`), and the orchestrator's validator becomes a
>   `*spinecore.Validator` that embeds `*validation.Engine`. `StatusChangeValidator` and
>   `BlockingStatusConflicts()` are promoted through the embedding, so item 4 still works; SMP
>   TASK-011's draft already records this.
> - SMP has two validator instances (the orchestrator's, `factory.go:179`, and the
>   `EmbeddedEngine`'s, `spineembedded/attach.go:111`, used by assign/automation-recovery
>   `EvaluatePreconditions`). Both come from the same constructor, so one option site covers both.
> - DC-2: SMP must enable binding (SMP TASK-009) before item 1; SMP TASK-011 already depends on it.
> - DC-5: SMP sets no `ArtifactsDir`, so no `WithArtifactsDir` call is needed in SMP.
> - The SMP-side gate (item 5) should add the DC-1 quoted-key case and the DC-3 delete case on the
>   real `RealGitOperator` (SMP TASK-011 AC 12 is the natural home).
> - Gate command: build SMP with `go build ./cmd/... ./internal/...` (a root `./...` also
>   compiles ignored `tmp/` prototypes) and without a stale untracked `vendor/`.

### 9. Coordination and ordering

- **Depends on TASK-005.**
  - That task moves `applyCommitStatus` and the cascade diff onto the bound SHA. This task reads,
    diffs and writes at the ref TASK-005 uses, and adds no binding of its own.
  - If TASK-005 names the ref differently, substitute it.
- **TASK-006 (F3)** also refuses in `preflightCheck` (path guard), next to
  `checkBranchProtectMerge` (`merge.go:84-98`).
  - The hunks are adjacent but distinct.
  - Whichever lands second reuses the same "refuse → record outcome → handlePermanentMergeFailure"
    shape. Lifecycle evaluation runs **after** the branch-protection/path guard.
- **TASK-004 (F5)** changes `CLIClient.WriteAndStageFile`, which the cascade uses.
  - Its OQ3 governs the option-off swallow of cascade write errors.
  - With the lifecycle on, this task already makes those errors fail closed.
- **TASK-014 (F7)** adds a branch-protection call in `workflow.Service.writeAndCommit`.
  - This task inserts only at `:133-136` and adds a setter, not a constructor parameter.
- **TASK-003 (P3-9)** gives `Diff` `-z`. After it lands, rule Q is dead code (follow-up 6).
- **TASK-007 / TASK-008** do not touch these functions. TASK-007 owns P3-4 (`EvaluatePreconditions`
  fail-open), and TASK-008 owns compare-and-swap writes (P3-24).

> **Design-check note (2026-10-06) — coordination corrections.**
> - **TASK-005 is a hard dependency, and the insertion point is `resolveMergeSource` step 3 /
>   `applyCommitStatusAt` (TASK-005 §6–§7), not `preflightCheck` and not `applyCommitStatus`**
>   (DC-2). TASK-005's `applyCommitStatusAt` "on error, log at Warn and use `bound`" must not
>   apply with the lifecycle on. V1 (DC-8) extends TASK-005's `verifyBoundMerge`.
> - **TASK-004**: under binding the cascade writes through `CommitFilesOnto` (no working tree), so
>   TASK-004's `WriteAndStageFile` change and its OQ3 concern only the binding-off path, which the
>   lifecycle no longer uses.
> - **TASK-006**: the lifecycle narrows the cascade's write set to owned paths, a subset of the
>   diff plus `run.TaskPath`, so TASK-006 §3.2's changed-path set stays correct with no change.
>   TASK-006's guard runs in `preflightCheck`, i.e. before the lifecycle.
> - **TASK-003**: besides `-z` (rule Q), its `BlobTooLargeError` / `ErrKindPermanent` read errors
>   are classified `unreadable_artifact` by DC-6, never retried.
> - **TASK-011 (P3-23)** is a dependency for standalone deployments with `artifacts_dir` (DC-5),
>   and the source of the shared path-scope predicate.
> - **TASK-012** bounds retries; lifecycle refusals are permanent and must not count as retries.

### 10. Documentation

- `architecture/workflow-validation.md` §5.5: rule 1 is implemented by
  `workflow.ValidateCommitStatuses` (create/update/validate only, opt-in; standalone on). Rules 2–3
  are not enforced (follow-up 3).
- `architecture/engine-state-machine.md`:
  - §3.3.1: add the `status_lifecycle_violation` row.
  - New §5.4 "Artifact status lifecycle (declared)": the table, the owned/non-owned rules and the
    reason codes.
- `architecture/git-integration.md` §5.3 (`:204-212`): the status cascade touches only owned
  artifacts. With the lifecycle on, a refused cascade aborts the merge before any commit.
- `KNOWN-LIMITATIONS.md`: residuals.
  - Deletions are not lifecycle-checked.
  - Direct authoritative writes (artifact API, task actions) are not checked.
  - The SC merge gate reads the projection and can lag `main` by one sync.
- `CHANGELOG.md` staging section: a "Security" entry naming the three options and that `serve`
  enables them.

> **Design-check note (2026-10-06) — documentation corrections.**
> - `engine-state-machine.md` §5 is the *Branch* (divergence) state machine (§5.1–§5.3). Put the
>   lifecycle in a new top-level section after §5 instead of "§5.4", without renumbering existing
>   sections (cross-references point at them).
> - That section also lists the design-check reason codes: `status_change_without_effect`,
>   `non_owned_removal`, `status_rewrite_failed`, `unreadable_artifact`, `merge_result_mismatch`,
>   and states that the lifecycle requires merge binding and uses the projection's path scope.
> - `KNOWN-LIMITATIONS.md`: replace "Deletions are not lifecycle-checked" with "deleting an
>   artifact owned by the run, or one added on the same branch, is not lifecycle-checked"; add
>   undeclared statuses already on `main` (DC-9); keep the projection-lag item and add that an
>   `epic-lifecycle` run refused within that window fails.
> - `CHANGELOG.md`: also note the `/system/validate` and `spine validate` severity change (§6
>   note), and that with the lifecycle on `commit.status` is applied under `artifacts_dir` (DC-5
>   fixes today's silent miss there; the binding-off path is unchanged).

---

## Decisions

- **D1 — The table is in code (`core/domain`), not in the repository.**
  - *Rejected:* a `.spine/artifact-lifecycle.yaml` read through the projection.
  - *Reason:* a run branch could rewrite the file and ride it into `main`. That is the same
    slippage as F3, and it would need provenance checks before it could be trusted. A table in
    code changes only with a Spine release.
- **D2 — Enforce at the governed merge, on the bound diff.**
  - *Rejected:* checks in `artifact.Service` writes.
  - *Reason:* runners push run branches through the git gateway. Only the merge sees every change.
- **D3 — The "from" status is the merge-base status.**
  - *Rejected:* the run-branch tip.
  - *Reason:* the tip is author-controlled. A merge-base/`main` difference that would matter makes
    git conflict on the `status:` line anyway.
  - **Design-check note (2026-10-06):** not unconditionally — a `*.md merge=union` attribute on
    `main` merges both `status:` lines without a conflict (DC-8 PoC). The merge-base choice stays;
    V1 checks the merge result so the reason no longer has to hold.
- **D4 — Ownership is TaskPath, plus additions in planning runs.**
  - *Rejected:* today's "TaskPath + every added `.md`" (it stamps foreign types and mints Completed
    artifacts in execution runs).
  - *Rejected:* "added and type ∈ `applies_to`" (it still mints terminal statuses in task runs).
  - *Reason:* the cascade's design intent (INIT-008 TASK-016) is the creation-review package.
- **D5 — Non-owned status edits are refused outright.**
  - *Rejected:* "allowed if the transition is legal". The audit's exploit (sibling Pending →
    Completed) is a legal transition.
  - Owner question 1.
- **D6 — Planning-owned additions need only a valid status for their type (O2).**
  - *Rejected:* "authored status must be initial".
  - *Reason:* the reviewed package is normalised to the workflow's declared target. Today an
    authored Completed child already lands as Pending, and refusing that would regress SMP's
    verify-children-less flows for no integrity gain.
  - **Design-check note (2026-10-06):** holds only when the workflow declares a target. With
    `newStatus == ""` there is nothing to normalise to, so O2′ applies the N2 rule (DC-4).
- **D7 — The distinct classification lives in the step's JSONB `error_detail` and the existing
  `precondition` merge class.**
  - *Rejected:* a new `MergeFailureClass`. It needs a CHECK change in migration 022, which breaks
    the no-runtime-schema rule.
- **D8 — "Block" means two things:** severity promotion in the validation engine, and a
  merge-time evaluation through an optional validator interface.
  - *Rejected:* promotion only. Validation reads the projection of `main`, so a parent is never
    "Completed" there before its own merge; promotion alone would only change reporting.
  - *Rejected:* walking children at the bound SHA. It needs `ListFiles` on `GitOperator`, an
    interface change SMP would have to implement.
- **D9 — No standalone environment kill switch.**
  - The library zero value is the rollback for embedders. Standalone operators pin the previous
    release.
  - Owner question 5 offers a switch.
- **D10 — The `commit.status` check stays out of `workflow.Validate`.**
  - *Reason:* TASK-011 adds `Parse`+`Validate` to projection sync. Putting the check there would
    drop already-merged workflows at load (the TASK-011 rule).
- **D11 — Fail closed on git errors when on.**
  - *Rejected:* today's degrade to primary-only (`merge.go:689-701`). Non-owned edits would
    then merge unchecked whenever `Diff` hiccups.
  - **Design-check note (2026-10-06):** "fail closed" means *retry* only for retryable errors;
    non-retryable read failures are permanent `unreadable_artifact` refusals (DC-6).
- **D12 — The lifecycle requires merge binding.** *(Added by design check, DC-2.)*
  - *Rejected:* evaluating the run-branch tip without binding. The tip can move between the
    evaluation and `mergePrimary`, so every rule is bypassable by a well-timed push.
  - *Rejected:* a second, lifecycle-only SHA pin. It duplicates TASK-005.
- **D13 — Validate what lands, not only what was planned.** *(Added by design check, DC-1/DC-8.)*
  - The regex writer and `git merge` are separate from the YAML reader. R1 re-parses each rewrite;
    V1 re-reads the merge result. *Rejected:* a YAML-node writer instead of R1 — it would reformat
    front matter on every cascade; R1 keeps today's byte-preserving rewrite and refuses the
    exotic encodings it cannot handle.

## Owner questions

The design works without answers, using the stated defaults.

1. **Non-owned status edits in a run.** Refuse every status change on an artifact the run does not
   own, or allow those that are legal transitions? *Default:* refuse (D5).
   - The lenient variant leaves the audit's "complete a sibling task" exploit open.
2. **Execution-run additions.** Should a standard run's cascade stamp the artifacts it adds?
   *Default:* no.
   - Only planning runs cascade their additions.
   - A standard run's new artifacts land with their authored status, which must be an initial
     status: Draft/Pending for Initiative/Epic/Task, Proposed for ADR, Living Document /
     Foundational / Stable for documents.
3. **Task `In Progress`.** *Default:* declared in the table (so SMP's `task-default` passes).
   `ValidStatusesForType(Task)`, artifact-schema §6 and task-lifecycle §3.3 are unchanged here.
   - *Alternative:* leave it undeclared, and require SMP TASK-011 to drop
     `commit.status: In Progress` from its `task-default` (seed + backfill).
4. **Default blocking SC rules.** *Default:* SC-002 and SC-003 (a completed parent with open
   children).
   - SC-001 (acceptance missing) stays a warning: acceptance is recorded by a separate API after
     the merge.
   - SC-004 (superseded link) and SC-005 (in-progress task with an open blocker) stay warnings.
5. **Standalone kill switch.** Add `SPINE_STATUS_LIFECYCLE=off` to `serve`? *Default:* no (D9).

*Added by the design check (2026-10-06):*

6. **Deleting governed artifacts the run does not own (N3, DC-3).** *Default:* refuse
   (`non_owned_removal`), including "stops parsing" and retype-to-unknown.
   - *Alternative:* allow deletions and accept the residuals: reopen-by-recreate (same `id`, new
     path), re-blocking every dependent of a deleted blocker, and frozen projection rows.
   - Runs that must retire an artifact set `Superseded` through a run that owns it.
7. **Owned status change with no declared effect (O1′, DC-4).** *Default:* refuse
   (`status_change_without_effect`); no shipped workflow merges a `.md` TaskPath without
   `commit.status`.
   - *Alternative:* allow any legal transition the author writes.
8. **Merge-result check V1 (DC-8).** *Default:* include. *Alternative:* follow-up, relying on R1
   plus git conflicts (accepting the `merge=union` gap).
9. **Lifecycle without binding (DC-2).** *Default:* refuse to merge (configuration error).
   - *Alternative:* evaluate the tip anyway and document the push-race bypass. Not recommended.
10. **Violations the author cannot fix on the branch.** *Default:* route back like every refusal.
    - *Alternative:* fail the run when the workflow's own `newStatus` is illegal from `S_base`, or
      `S_base` is undeclared, so a publish-step workflow does not loop.

## Acceptance Criteria

1. **Declared table** (`core/domain/artifact_lifecycle_test.go`).
   - `TestDefaultStatusLifecycle_Golden`: `Initial` and `Transitions(t)` for every type equal the
     §1 table exactly. Adding or removing any edge fails it.
   - `TestDefaultStatusLifecycle_Invariants`:
     - every `ValidMarkdownArtifactTypes()` type is present and `Workflow` is not;
     - `States(t) ⊇ ValidStatusesForType(t)`;
     - every state is reachable from an initial state;
     - Completed/Cancelled/Rejected/Abandoned/Superseded/Deprecated have no outbound edge;
     - `Allows(t,s,s)` is true only for declared `s`;
     - an unknown type is never allowed.
   - A prohibited table is false, for example: Task Completed → Pending, Cancelled → Pending,
     Draft → Completed, Draft → Superseded; ADR Accepted → Proposed; Epic Completed → In Progress.
2. **Compatibility gate** (`core/workflow/commit_status_compat_test.go`).
   - Every `workflows/*.yaml` in the repo parses and returns zero `ValidateCommitStatuses` errors.
     The test asserts it saw **11** `commit.status` outcomes, so an empty glob cannot pass.
   - Each §7 row (Spine and pinned SMP, **10** SMP outcomes) is accepted by the merge rule named in
     its last column.
   - Deleting Task `Pending → In Progress` from the table fails the SMP `task-default` row.
3. **Workflow service** (`core/workflow/commit_status_test.go`, real temp repo). The design-phase
   PoC body is `applies_to: [Task]` with outcomes `commit.status: Accepted` and `Shipped`. With
   `WithStatusLifecycle(Default)`:
   - `Create` → `ErrValidationFailed` with exactly 2 `commit_status` errors on fields
     `steps[0].outcomes[0|1].commit.status`. `HEAD` is unchanged and the file is absent.
   - `Update` is refused the same way, and the file is unchanged.
   - `ValidateBody` → `failed`, with the same 2 errors.
   - `applies_to: [Workflow]` + `commit.status` → 1 error. `commit.merge` only → passes.
   - Without the option, the same `Create` succeeds (zero-value control).
   - `workflow.Validate(wf)` still passes the same body (the TASK-011 rule).
   - Removing the `writeAndCommit` call fails the Create case. Moving the check into `Validate`
     fails the control.
4. **Validation** (`core/validation/engine_test.go`). With
   `WithBlockingStatusConflicts(DefaultBlockingStatusConflicts...)`:
   - a Completed epic with a Pending child → `Status == "failed"` and SC-002 in `Errors`;
   - a Completed task without acceptance → SC-001 still in `Warnings`;
   - the existing no-option assertion at `engine_test.go:386` is unchanged and green;
   - `DefaultBlockingStatusConflicts == [SC-002 SC-003]`, and each is a registered rule ID.
   - `ValidateStatusChange`:
     - Pending epic with a Pending child, new status Completed → an SC-002 error when promoted, a
       warning when not;
     - an unknown path → `passed, nil`;
     - a store error → non-nil error;
     - a nil engine → `passed`.
5. **P3-21 PoC as the regression** (`core/engine/status_lifecycle_test.go`, real `git.CLIClient`,
   the design-phase repo). The run is standard, outcome commit `Completed`, and the branch edits
   `task-002` Completed → Draft and `INIT-001` Pending → Completed. With
   `WithStatusLifecycle(Default)`:
   - `MergeRunBranch` returns nil.
   - The `main` SHA and the run-branch tip are unchanged (no cascade commit).
   - The run is `failed` (`stubWorkflowLoader` has no publish step).
   - The primary merge outcome is `failed` / `precondition`, and its detail starts
     `status_lifecycle_violation:`.
   - The violations are exactly `{task-002: non_owned_status_change Completed->Draft,
     initiative.md: non_owned_status_change Pending->Completed}`.
   - The same test against a build where `planStatusLifecycle` returns no violations fails. The
     design-phase PoC (pre-fix assertions) passes on `61994fe`.
   - **Design-check note (2026-10-06):** the orchestrator also has binding on (DC-2):
     `EnableMergeBinding` on the `CLIClient` and `CommitMeta["spine.bound_sha"]` = the run-branch
     tip, `spine.bound_by` set. A twin with binding off and the lifecycle on returns the DC-2
     configuration error, leaves the run `committing` and never calls `Merge`/`MergeBound`.
6. **Run-owned cascade** (same file, real git).
   - The same repo without the two non-owned edits merges with the option on.
   - `main` then has `task-001` Completed, `task-003` **Draft** and `ADR-0099` **Proposed**. The
     cascade commit touches exactly `task-001`.
   - A control with the option off reproduces today's result (`task-003` and `ADR-0099` stamped
     Completed).
   - Dropping the `run.Mode == planning` condition fails the option-on case.
   - **Design-check note (2026-10-06):** under binding the cascade commit is not on the run
     branch; find it as the merge commit's second parent (`main^2`) and assert its parent is the
     bound SHA.
7. **Rule coverage** (table-driven; `cascadeGitOperator`-style mocks are acceptable).
   - Each reason code fires on its case:
     - `illegal_transition`: TaskPath Completed, commit Pending.
     - `illegal_initial_status`: a standard run adds a Task authored Completed.
     - `status_not_in_type`: a planning run adds an ADR, commit Pending.
     - `type_changed`: Task → Epic.
     - `not_an_artifact`: TaskPath without front matter, commit Completed.
     - `undeclared_status`: TaskPath status `Done`, commit Completed.
   - These merge:
     - Task Pending → In Progress (the SMP `task-default` shape);
     - Epic Pending → Completed and In Progress → Completed;
     - an ADR planning run Proposed → Accepted / Deprecated;
     - document-creation Living Document identity, with no cascade commit.
   - **Design-check note (2026-10-06):** the design-check reason codes are covered by ACs 17–24
     (`status_rewrite_failed`, `non_owned_removal`, `status_change_without_effect`,
     `unreadable_artifact`, `merge_result_mismatch`); `type_changed` is asserted for both an owned
     and a non-owned path.
8. **Cascade-sequence check** (the audit's `smp_cascade_sequence` shape, ported to Spine with real
   git).
   - A planning run adds an Epic + 2 Draft Tasks and edits a non-artifact `README.md`; commit is
     Pending and the option is on.
   - It merges. `git show --name-only <cascade commit>` lists exactly the 3 artifacts, and all are
     `Pending` on `main`.
   - The five existing `TestApplyCommitStatus_*` tests (`merge_test.go:513-750`) pass unchanged.
9. **Fail closed when on** (mocks).
   - A `Diff`, `MergeBase` or bound-SHA `ReadFile` error, or a cascade `WriteAndStageFile` error:
     `MergeRunBranch` returns an error, the run stays `committing`, and `Merge` is never called.
   - With the option off, the same `Diff` error degrades as today (the merge proceeds).
   - **Design-check note (2026-10-06):** under binding the write is `CommitFilesOnto`, so the
     write-failure case is a `CommitFilesOnto` error (no `WriteAndStageFile` is called at all).
     "Error" here means a retryable `*git.GitError`; non-retryable read errors are AC 22.
10. **Quoted-path guard.** With the option on, a `Diff` entry `"tasks/\303\251.md"` (added) is
    refused `unparseable_path`. `"src/\303\251.go"` is ignored.
11. **Refusal routing and classification.** The workflow loader returns a `task-default`-shaped
    definition with a publish step. A refusal then:
    - completes the publish execution with `merge_failed`;
    - returns the run to `active`, with the next `execute` execution created;
    - sets `ErrorDetail.Classification == "status_lifecycle_violation"` on the last completed step;
    - records the merge-outcome class `precondition`.
    - `git diff --stat 61994fe -- migrations/` is empty, and `domain.ValidMergeFailureClasses()` is
      unchanged.
12. **SC merge gate** (fake validator).
    - A validator implementing `StatusChangeValidator` that returns an SC-002 error for an Epic
      Pending → Completed: refused `status_conflict` with `Rules == [SC-002]`.
    - Warnings only: the merge proceeds.
    - A returned error: the run stays committing.
    - A validator without the interface: the gate is skipped.
13. **Standalone wiring.**
    - `buildOrchestrator` and the per-workspace orchestrator return `StatusLifecycle() != nil`.
    - A workspace `ServiceSet`'s workflow service returns `failed` from `ValidateBody` on the AC 3
      body.
    - Its validator's `BlockingStatusConflicts()` equals the default.
    - Deleting any one of the six wiring lines (§6) fails a test.
    - **Design-check note (2026-10-06):** both orchestrators also return
      `MergeBindingEnabled() == true` and carry the workspace's `artifacts_dir` (DC-5): a
      workspace configured with `artifacts_dir: spine` yields an orchestrator whose plan resolves
      `initiatives/x.md` to `spine/initiatives/x.md`. There are eight wiring lines; deleting any
      one fails a test.
14. **Scenarios** (`-tags scenario`, Postgres).
    - `TestPlanningRun_StatusCascadesToChildren` (`planning_run_test.go:417-451`) passes with
      `harness.WithStatusLifecycle()` (a variant is added).
    - New `TestStandardRun_StatusLifecycleRefusesNonOwnedStatusEdit`: the run is not completed,
      `main` is unchanged, and the step classification is `status_lifecycle_violation`.
    - **Design-check note (2026-10-06):** `harness.WithStatusLifecycle()` enables binding too.
      Add `TestStandardRun_StatusLifecycleArtifactsDir` with `harness.WithArtifactsDir("spine")`:
      a `task-default` run on `spine/…/task.md` completes and `main` shows the task `Completed`
      (today the status is silently not applied, design-check PoC `artifacts_dir_test.go`).
15. **Zero-value compatibility and gates.**
    - SMP `80995559`, with `replace => ` this branch: `go build ./...` and `go vet ./...` pass
      with no SMP code change.
    - Spine passes `go test ./...`, `go vet ./...`, `dev/lint-boundaries.sh` (no new allowlist
      entry), golangci-lint and the `cmd/embed-smoketest` build.
    - Every pre-existing test is unchanged.
    - **Design-check note (2026-10-06):** run the SMP build as `go build ./cmd/... ./internal/...`
      and `go vet ./cmd/... ./internal/...` with no untracked `vendor/` (a root `./...` compiles the
      ignored `tmp/` prototypes). The new `Orchestrator.WithArtifactsDir` setter is not an
      interface method, so SMP still needs no code change.
16. **Documentation.** The §10 edits are present, and the §1 table in
    `architecture/engine-state-machine.md` is identical to the golden in AC 1.
    - **Design-check note (2026-10-06):** including the §10 design-check corrections (new
      top-level section, not §5.4; the added reason codes; the `KNOWN-LIMITATIONS.md` and
      `CHANGELOG.md` items).

*ACs 17–25 were added by the design check (2026-10-06). All run with the lifecycle and binding on,
real `git.CLIClient` unless stated, and assert `main` (and the bound SHA) unchanged on refusal.*

17. **R1 — rewrite verification (DC-1).** Owned TaskPath `Pending` at base, outcome
    `commit.status: In Progress`, TaskPath at the bound SHA authored with each of:
    `"status": Completed`, `'status': Completed`, `status : Completed`,
    `? status` / `: Completed`, and `status: Pending` + continuation line `  Completed`.
    - Each is refused `status_rewrite_failed`; nothing is merged.
    - A planning-run addition authored `"status": Completed` with outcome `Pending` is refused the
      same way.
    - Control: plain `status: Pending` merges and lands `In Progress`.
    - Mutation: removing the re-parse makes the `"status": Completed` case land `Completed` (the
      shape of design-check PoC `owned_quoted_key_test.go`, which passes on `61994fe`).
18. **Binding (DC-2).**
    - Lifecycle on, binding off: `MergeRunBranch` returns an `ErrUnavailable` error naming merge
      binding; the run stays `committing`; no `Merge`/`MergeBound`; no outcome row is written.
    - Lifecycle and binding on: after the bound SHA is captured, a commit pushed to the run branch
      changes `task-002` `Completed → Draft`. The run merges with **no** refusal (the plan judged
      the bound SHA), `task-002` is still `Completed` on `main`, and the pushed commit is not an
      ancestor of `main`.
    - The lifecycle runs with no `commit.status`: `CommitMeta` without `status` and a non-owned
      status edit is refused `non_owned_status_change`.
19. **N3 — removal guard (DC-3).** Non-owned `task-002`, `Completed` at base. Each of these is
    refused `non_owned_removal`:
    - deleted;
    - given a second `status:` key;
    - retyped `type: Taskx`;
    - replaced by a symlink;
    - deleted and re-added at another path with the same `id`, < 50 % similarity, `status: Draft`
      (git reports `D` + `A`), in a standard run and in a planning run (where it would be stamped
      `Pending`).
    - Controls that merge: deleting a non-artifact `.md`; deleting an artifact added earlier on the
      same branch; `git mv` of `task-002` with unchanged status (reported `R`, judged by N1).
    - Mutation: restoring "deleted → ignored" lets the recreate case land a `Pending`/`Draft`
      `TASK-002`.
    - O3′: a standard run with no `commit.status` that deletes its own `.md` TaskPath is refused
      `not_an_artifact`.
20. **O1′/O2′ — no declared effect (DC-4).**
    - Planning run with `CommitMeta = {merge: …}` (the `workflow-lifecycle` shape, TaskPath
      `workflows/x.yaml`) that adds a Task authored `Completed`: refused `illegal_initial_status`.
      Authored `Draft`: merges, lands `Draft`.
    - Standard run with no `commit.status` whose branch changes its own TaskPath `Pending →
      Completed`: refused `status_change_without_effect`.
21. **Path scope (DC-5).** `Orchestrator.WithArtifactsDir("spine")`, TaskPath
    `initiatives/i/epics/e/tasks/t.md`, file at `spine/initiatives/i/epics/e/tasks/t.md`, outcome
    `Completed`.
    - It merges and `main:spine/…/t.md` is `Completed` (design-check PoC `artifacts_dir_test.go`
      shows `Pending` on `61994fe`).
    - A non-owned status edit under `spine/` is refused; a status edit to a `.md` outside `spine/`
      and to `spine/templates/x.md` is ignored (merges).
    - Mutation: comparing the raw TaskPath with diff paths fails the first case.
22. **Error classes (DC-6).** With mocks:
    - a retryable `ReadFile` / `Diff` / `MergeBase` error keeps the run `committing` (AC 9);
    - a `*git.BlobTooLargeError` (TASK-003) on a modified non-owned `.md`, a non-retryable
      permanent read error, and a not-found for a diff-listed path are each refused
      `unreadable_artifact` and leave the run **not** `committing` (routed or failed per AC 11).
23. **Renames onto the TaskPath (DC-7).** Planning run: the branch removes its drafted TaskPath
    and renames non-owned `task-002` (`Completed`) onto it (git reports `R100`). Refused
    `illegal_transition` with `From == Completed`. A `Diff` entry whose `OldPath` is C-quoted and
    ends `.md"` is refused `unparseable_path`.
24. **V1 — merge-result check (DC-8).** `main` carries `.gitattributes` `*.md merge=union`; owned
    TaskPath `Pending` at base; `main` changes it to `Cancelled` after the fork; outcome
    `Completed`.
    - Refused `merge_result_mismatch`; nothing is pushed (auto-push on with a bare origin:
      `origin/main` unchanged).
    - Mutation: removing the V1 call merges with two `status:` lines (the shape of design-check PoC
      `union_merge_test.go`, which passes on `61994fe`).
    - Control: the same repo without the `main`-side change merges.
25. **Undeclared base data (DC-9).** A standard run edits the body (not the status) of a non-owned
    `Architecture` artifact whose status is `Active`: it merges. Changing that status to
    `Living Document`: refused `non_owned_status_change`.

## Out of scope / follow-ups

1. **Deleting governed artifacts.** This covers deleting a file, or making it stop being an
   artifact, inside a run. It is not a status transition, so this task ignores it. Deciding whether
   runs may delete governed artifacts (vs. `Superseded`) is a separate policy.
   - **Design-check note (2026-10-06):** narrowed by N3 (DC-3). Non-owned artifacts that existed
     at `base` can no longer be deleted or made unparseable by a run, and O3′ covers the run's
     own TaskPath. What remains out of scope is the policy question whether *any* run may
     retire an artifact by deletion.
2. **Direct authoritative writes.** These are artifact API create/update on `main` and the task
   governance actions (Spine `service/internal/gateway/handlers_tasks.go:112-149`, SMP
   `internal/customer/spineembedded/embedded_tasks.go:220-249`). They are not lifecycle-checked.
   - They are operator-authority paths behind branch protection.
   - A later task can call `StatusLifecycle.Allows` there (for example, cancel on a Completed task).
   - **Design-check note (2026-10-06):** both task-action writers use the same
     `(?m)^status:\s*.*$` regex (Spine `handlers_tasks.go:155`, SMP `embedded_tasks.go:314`), so
     the DC-1 differential applies there too; the later task should re-parse after rewriting.
3. **Rules 2–3 of `workflow-validation.md` §5.5** are not enforced: no skipping governed
   intermediates, and terminal statuses only on `end`-routing outcomes. Rule 3 conflicts with
   Spine's own `task-default` (`Completed` on `review.accepted` → `publish`).
4. **Reconcile Task `In Progress`.** Bring `ValidStatusesForType(Task)`, artifact-schema §6 and
   task-lifecycle §3.3 in line with the table (Owner question 3).
5. **SMP workflow defects** found while building the gate. They belong to SMP TASK-011 or an SMP
   follow-up:
   - `task-default`'s dead `publish.published` commit (§7 note);
   - `epic-lifecycle`'s `execute` precondition reads `main`, where the planned `In Progress` is
     never merged (`core/engine/step.go:808-813`).
6. **Remove rule Q** once TASK-003 and SMP TASK-003 ship NUL-delimited diffs.
7. **Evaluate SC rules from the bound tree** instead of the projection, to remove the one-sync
   lag. This needs a listing port on `GitOperator`.
8. **A repository-configurable lifecycle table** (D1), only with provenance checks.

*Added by the design check (2026-10-06):*

9. **Link edits that change blocking.** A run can still edit a non-owned task's `blocked_by` links
   (a content edit, not a status change), which un-blocks or re-blocks it without any status
   transition. It is visible to the reviewer; a later task could treat `blocked_by`/`blocks` edits
   on non-owned artifacts like N1.
10. **Duplicate artifact `id`s across paths.** A run can add a new path carrying an existing `id`
    (with an initial status, so N2 passes). Nothing in this task enforces `id` uniqueness; the
    projection is path-keyed. Track with the ID-allocation work.
11. **A YAML-node status writer** to replace `rewriteStatusField`, so R1 never refuses an exotic
    but valid encoding (D13).
12. **Children added in the same run are invisible to the SC gate.** O4 reads the projection, so
    an `epic-lifecycle` run that also adds a `Pending` child under the epic completes the epic
    (SC-002 after the fact). Follow-up 7 removes it.

## Design check (2026-10-06)

An adversarial check of this design against Spine `61994fe` and SMP `80995559`. The designer's
text is kept; every change is marked **Design-check note (2026-10-06)** in place.

**Reproduced first.** The four design-phase PoCs in `tmp/init027-orchestration/spine-init023/_work/task017/`
pass on `61994fe` via `go test -overlay` (standard run publishes any status; owned illegal
transition; workflow create accepts foreign `commit.status`; planning cascade touches exactly the 3
added artifacts). The corpus check counts 11 Spine and 10 SMP `commit.status` outcomes, all inside
the proposed table.

**New design-check PoCs** (`tmp/init027-orchestration/spine-init023/_work/task017-check/`, run
from `../spine` with `go test -overlay <overlay>.json -run TestDesignCheck_ -v ./core/engine/`; no
file is written to either repository, and the Spine tree stays clean):

| PoC | Overlay | What it shows on `61994fe` |
|---|---|---|
| `rewrite_differential_test.go` | `overlay-rewrite.json` | `rewriteStatusField` leaves `"status":`, `'status':`, `status :` and `? status` untouched while `artifact.Parse` reads them; a continuation line becomes `In Progress Completed`; `status:` with an empty value swallows the next line. |
| `dup_test.go` | `overlay-dup.json` | A duplicate `status:` key fails `Parse` and `IsArtifact` (so "stops being an artifact → ignored" is an escape hatch). |
| `owned_quoted_key_test.go` | `overlay-owned.json` | Through the real `MergeRunBranch`: owned task authored `"status": Completed`, outcome `In Progress` → `main` parses `Completed`. |
| `artifacts_dir_test.go` | `overlay-adir.json` | With `artifacts_dir: spine`, `commit.status` is silently not applied today (TaskPath read raw). |
| `union_merge_test.go` | `overlay-union.json` | `*.md merge=union` on `main` merges a concurrent status change with no conflict; the file lands with two `status:` keys. |

**What changed and why.**

| ID | Change | Why (attacker path or breakage) |
|---|---|---|
| DC-1 | R1: re-parse each rewrite; refuse `status_rewrite_failed` | **Bypass.** The plan validates `final` with YAML but the regex writer can no-op; the author's `S_tip` lands unvalidated on the owned path (PoC). |
| DC-2 | Lifecycle requires merge binding; hook moves to TASK-005 `resolveMergeSource` step 3 / `applyCommitStatusAt`; runs with no `status`; no warn-and-degrade; D12 | **Bypass / wrong seam.** TASK-005 is opt-in and moves the cascade out of `preflightCheck`; without binding the tip can move between plan and merge. The original `preflightCheck` hunk would never run under binding. |
| DC-3 | N3 `non_owned_removal`; O3′ for the owned TaskPath | **Bypass of the stated impacts.** Delete-and-recreate reopens a `Completed` task under the same `id`; deleting a blocker re-blocks dependents; unparse (duplicate key) freezes the projection row. All were "ignored". |
| DC-4 | O1′ `status_change_without_effect`; O2′ `IsInitial` when `newStatus == ""` | **Bypass.** A `workflow-lifecycle` planning run (no `commit.status`) could mint `Completed` tasks; an owned TaskPath could take any legal edge the workflow never declared. |
| DC-5 | `Orchestrator.WithArtifactsDir`; projection path scope (no `templates/`, in-dir only); TASK-011 dependency | **Breakage.** TaskPath is artifact-relative, diff paths repo-relative: with `artifacts_dir: spine` every standalone run would be refused or stall, and its own file judged non-owned. |
| DC-6 | Only retryable errors retry; others `unreadable_artifact` | **Self-DoS.** A > 4 MiB `.md` (TASK-003 `BlobTooLargeError`) or a gitlink would keep the run `committing` forever. |
| DC-7 | Renames onto the TaskPath judged through `OldPath`; rule Q on `OldPath` | **Ambiguity → bypass.** Read as O2, a `Completed` task renamed onto a planning TaskPath is stamped `Pending`. |
| DC-8 | V1 merge-result check in TASK-005 `verifyBoundMerge`; D3 note; D13 | **D3 is false under `merge=union`** (PoC); V1 also covers criss-cross bases and any residual writer/merge differential. |
| DC-9 | Undeclared statuses already on `main` documented; AC 25 | **Compatibility.** Real repos hold `Architecture: Active`, `Product: Draft for Review`, etc.; N1 must keep comparing raw values. |
| — | §6/§8/§9/§10 corrections; ACs 5, 6, 7, 9, 13–16 amended; ACs 17–25 added; owner questions 6–10; follow-ups 9–12 | Wiring (binding, artifacts dir, harness), SMP TASK-003's validator rename, routing liveness, projection lag, doc placement (`engine-state-machine.md` §5 is the Branch machine), SMP build command. |

**Checked and found correct.** Every `file:line` cited in Purpose and §1–§9 at `61994fe` / `80995559`
(merge.go function ranges, `ValidStatusesForType`, SC rule severities, `Engine.Validate`,
`EvaluatePreconditions`, `cross_artifact_valid`, `SetCommitMeta` at `step.go:396-401`, the publish
handler at `handlers.go:206-236`, `workflow.Service` ranges, `classifyMergeFailure`, migration 022's
CHECK, the standalone wiring lines, the scenario test, all 21 corpus `commit.status` lines, and the
SMP references). The SMP `task-default` note (merge applies `In Progress`; `publish.published` is
dead) is right. SC promotion has no step-activation effect today: no shipped workflow declares
`cross_artifact_valid`. SMP has no switch over failure classifications, so the new string needs no
SMP change. Compile compatibility holds: no interface SMP implements gains a method.

**Residual risks after the amendments.** Direct authoritative writes (follow-up 2, same regex);
`blocked_by` link edits (follow-up 9); duplicate `id`s (follow-up 10); SC gate projection lag and
same-run children (follow-ups 7, 12); V1's standalone caveat that the local `main` already holds
the merge when auto-push is off (TASK-005/TASK-012).

## Implementation Notes

Implemented on branch `init/INIT-023-TASK-017-artifact-status-lifecycle`, from Spine local `main`
`ae513dca` (origin `61994fe` + the INIT-023 scaffold + waves 1–3 + the codex 6.1 catch-up;
TASK-005's merge binding and TASK-011's `artifacts_dir` handling are on it). Diff base:
`git diff main...HEAD`. Status stays In Progress; the orchestrator closes it after merge.

### Orchestrator rulings recorded (owner questions take their defaults, pending the owner)

- **OQ1** refuse every status change on an artifact the run does not own (D5).
- **OQ2** a standard run's cascade does not stamp its additions; only planning runs cascade theirs.
- **OQ3** Task `In Progress` is declared in the table (SMP's `task-default` passes);
  `ValidStatusesForType(Task)`, artifact-schema §6 and task-lifecycle §3.3 are unchanged.
- **OQ4** blocking SC rules are SC-002 and SC-003 only.
- **OQ5** no `SPINE_STATUS_LIFECYCLE` kill switch in `serve` (plan owner question 10: the lifecycle
  is on in `spine serve`; the embedded zero value is today's behaviour).
- **OQ6** refuse deleting governed artifacts the run does not own (`non_owned_removal`).
- **OQ7** refuse an owned status change with no declared effect (`status_change_without_effect`).
- **OQ8** the merge-result check V1 is included.
- **OQ9** lifecycle without binding = refuse to merge (configuration error, `ErrUnavailable`); the
  lifecycle evaluates TASK-005's bound commit.
- **OQ10** violations route back like every refusal.
- Needs TASK-011's `artifacts_dir` handling (on `main`). SMP TASK-011 adopts this later.

### What was built

- **`core/domain`** (`06f13acd`, `fb14106c`): `StatusLifecycle` (the §1 table, immutable, nil-safe;
  `Types`, `States`, `Initial`, `IsState`, `IsInitial`, `Allows`, `Transitions`,
  `WithoutTransition`), `DefaultStatusLifecycle()`, `FailureStatusLifecycle`
  (`"status_lifecycle_violation"`, not retryable).
- **`core/artifact`** (`06f13acd`): `LifecycleFields` (Parse's decoder: `splitFrontMatter` +
  `yamlsafe.Decode` + the `frontMatter` struct; a duplicate key is not-ok), `EqualExceptStatus`
  (R1's "nothing else changed" half), `PathScope` (`NewPathScope`, `ArtifactKey`,
  `GovernedArtifactKey`, `RepoPath`) — TASK-011's projection predicate moved here;
  `adapters/projection/scope.go` delegates to it. `Service.ArtifactsDir()` (`8c5d4391`).
- **`core/workflow`** (`fb14106c`): `ValidateCommitStatuses` + `CommitStatusRuleID`;
  `Service.WithStatusLifecycle`; `writeAndCommit` refuses after `Validate` and before the
  branch-protection check and `enterBranch`; `ValidateBody` appends the same errors.
- **`core/validation`** (`72a850a0`): `DefaultBlockingStatusConflicts`,
  `WithBlockingStatusConflicts`, `BlockingStatusConflicts()`, engine-level promotion in `Validate`;
  `ValidateStatusChange` (status-conflict rules only, on a copy of the projection row; store
  errors of blocking rules are recorded through a wrapping store because the rules swallow them).
- **`core/engine`** (`b2f45bce`, `68cbefd2`): `status_lifecycle.go` — `WithStatusLifecycle` /
  `StatusLifecycle()` and `WithArtifactsDir` / `ArtifactsDir()` (orchestrator.go),
  `StatusChangeValidator` (interfaces.go), `StatusLifecycleError` / `StatusLifecycleViolation` /
  `ErrStatusLifecycleViolation` and the `Lifecycle*` reason constants, the planner
  (`planStatusLifecycle`: rows O1, O1′, O2, O2′, O3, O3′, O4, N1, N2, N3, R1, U, Q, Q′),
  `applyStatusLifecycleAt` / `lifecycleCascadeFrom` / `resolveLifecycleSource` /
  `refuseStatusLifecycle`, V1 (`verifyLifecycleMerge`). Seams: `preflightCheck` refuses
  lifecycle-without-binding before the binding-off cascade; `resolveMergeSource` step 3 branches
  to the lifecycle; `mergePrimaryBound` runs V1 after `verifyBoundMerge` and handles a
  `*StatusLifecycleError` refusal; `cascadeCommitFrom`'s commit is `commitCascadeOnto` (shared);
  `thisRunsCascadeMerge` recomputes the lifecycle cascade when on; `cascadeBaseAt` wraps the new
  `cascadeBase` (replay flag); the not-a-child-of-bound refusal is the helper
  `refuseNotChildOfBound`; `recordGitConflictOnStep` and `classifyMergeFailure` classify the
  refusal; `startWorkflowPlanningRun` checks `commit.status` before any branch.
- **Standalone** (`8c5d4391`): `buildOrchestrator` and `workspaceOrchestratorBuilder` call
  `WithStatusLifecycle` and `WithArtifactsDir(<artifact service>.ArtifactsDir())`;
  `buildWorkflowService` and the pooled workflow service call `WithStatusLifecycle`; the process and
  pooled validators get `WithBlockingStatusConflicts(Default…)`. Scenario harness:
  `harness.WithStatusLifecycle()` (EnvOption) / `WithDeclaredStatusLifecycle()` (RuntimeOption),
  implying validation and refusing `WithoutMergeBinding`; `WithArtifactsDir` also reaches the
  orchestrator.
- **Docs** (`af5ea46f`): `engine-state-machine.md` §5A (new top-level section after §5, nothing
  renumbered; pinned to the code by `TestStatusLifecycleDocTable`) and the §3.3.1 row;
  `workflow-validation.md` §5.5/§7.1; `git-integration.md` §5.3; `KNOWN-LIMITATIONS.md`
  "Artifact Status Lifecycle: What INIT-023 TASK-017 Leaves Open"; `CHANGELOG.md` (Security,
  Changed (standalone), Changed (embedders)); `docs/integration-guide.md` §10.

### Per-AC evidence

| AC | Status | Evidence |
|---|---|---|
| 1 | done | `core/domain/artifact_lifecycle_test.go`: `TestDefaultStatusLifecycle_Golden` (Initial + Transitions of every type equal the §1 table edge by edge), `TestDefaultStatusLifecycle_Invariants` (all Markdown types, no Workflow; States ⊇ ValidStatusesForType; reachability; final statuses have no outbound edge; identity only for declared; unknown types; nil lifecycle), `TestDefaultStatusLifecycle_Prohibited` (Task Completed→Pending, Cancelled→Pending, Draft→Completed, Draft→Superseded, ADR Accepted→Proposed, Epic Completed→In Progress, …). |
| 2 | done | `core/workflow/commit_status_compat_test.go`: `TestCommitStatusCompat_SpineCorpus` parses every `workflows/*.yaml`, zero errors, asserts **11** outcomes and that each file matches its §7 row; `TestCommitStatusCompat_RowsPassTheMergeRules` (every Spine and pinned SMP row, **10** SMP outcomes, through O1/O2); `TestCommitStatusCompat_SMPTaskDefaultNeedsPendingToInProgress` (`WithoutTransition(Task, Pending, In Progress)` fails the SMP `task-default` row). |
| 3 | done | `core/workflow/commit_status_test.go` (real temp repo): Create → `validation_failed`, exactly 2 `commit_status` errors on `steps[0].outcomes[0\|1].commit.status`, HEAD unchanged, file absent; Update refused, file and HEAD unchanged; ValidateBody `failed` with the same 2; `applies_to: [Workflow]` + status → 1 error, `merge` only passes; zero-value control Create succeeds; `workflow.Validate` passes the body. Mutations: removing the `writeAndCommit` call fails the Create case (W1), moving the check into `Validate` fails the control (W2), removing the ValidateBody append fails it (W3). |
| 4 | done | `core/validation/status_change_test.go`: promotion (Completed epic + Pending child → `failed`, SC-002 in Errors), SC-001 stays a warning, no-option control, unknown ids ignored, default list pinned and registered (`status_change_internal_test.go`), `ValidateStatusChange` (promoted error / warning without promotion / terminal children pass / only SC rules / unknown path passed / store error / a failed child listing in a blocking rule is an error / nil engine), `EvaluatePreconditions` inherits. The existing `engine_test.go` SC tests are unchanged and green. |
| 5 | done | `core/engine/status_lifecycle_test.go` `TestStatusLifecycle_P321StandardRunRefused` (real `CLIClient`, binding on: the PoC repo; `MergeRunBranch` nil; main and the run branch unchanged; run `failed`; outcome failed/precondition with the exact `status_lifecycle_violation:` detail; violations exactly task-002 Completed→Draft and INIT-001 Pending→Completed). Twin `TestStatusLifecycle_RequiresBinding` (binding off: `ErrUnavailable` naming merge binding, run committing, no Merge/MergeBound, no outcome, run branch not rewritten). Mutation M1 (no violations) fails it. Review round 1: the same refusal through a criss-cross history and a concurrent merge (`TestStatusLifecycle_MergeResultCrissCross`, `TestStatusLifecycle_MergeResultMainMoved`). |
| 6 | done | `TestStatusLifecycle_RunOwnedCascade`: main has task-001 Completed, task-003 Draft, ADR-0099 Proposed; the cascade commit is `main^2`, its parent is the bound SHA, it touches exactly task-001; lifecycle-off control stamps task-003 and ADR-0099 Completed (today). Mutation M2 (planning condition dropped) fails it. |
| 7 | done | `status_lifecycle_unit_test.go` `TestStatusLifecycle_RuleTable` (fake binder): `illegal_transition`, `illegal_initial_status`, `status_not_in_type`, `type_changed` (owned and non-owned), `not_an_artifact` (no front matter; non-.md TaskPath with an effect), `undeclared_status`; accepted: Task Pending→In Progress, Epic Pending→Completed and In Progress→Completed, ADR planning Proposed→Accepted/Deprecated, document-creation identity with no rewrite, workflow-lifecycle `.yaml` TaskPath skipped. The design-check codes are ACs 17–24. |
| 8 | done | `TestStatusLifecycle_PlanningCascadeSequence` (Epic + 2 Draft Tasks + README.md edit, Pending): merges; `git show --name-only main^2` lists exactly the 3 artifacts; all Pending; README.md landed. The five `TestApplyCommitStatus_*` tests are unchanged and green. |
| 9 | done | `TestStatusLifecycle_FailsClosed`: retryable Diff, MergeBase, bound-SHA ReadFile, merge-base ReadFile, a non-git error, and a `CommitFilesOnto` error → `MergeRunBranch` returns an error, run `committing`, no MergeBound, no outcome; lifecycle-off control: the same Diff error degrades and MergeBound runs. |
| 10 | done | `TestStatusLifecycle_QuotedPaths`: added `"tasks/\303\251.md"` → `unparseable_path`; `"src/\303\251.go"` ignored. |
| 11 | done | `TestStatusLifecycle_RefusalRouting` (real git, publish workflow): publish completed `merge_failed`, run `active`, a new waiting `execute`, last completed step classified `status_lifecycle_violation`, outcome class `precondition`, main unchanged. `TestStatusLifecycle_RefusalClassification` (fake): the same route plus `ValidMergeFailureClasses()` pinned unchanged. `git diff --stat main...HEAD -- migrations/` and `git diff --stat 61994fe HEAD -- migrations/` are empty. Mutation E7 fails it. |
| 12 | done | `TestStatusLifecycle_SCGate`: SC-002 error → `status_conflict` with `Rules == [SC-002]`; warnings only → merges; validator error → committing; validator without the interface → skipped; identity transition not checked. Mutation E1 fails it. |
| 13 | done | `service/cmd/spine/serve_status_lifecycle_test.go`: `buildServerConfig` with `artifacts_dir: spine` (process orchestrator: lifecycle, binding, `spine` resolving `initiatives/x.md` to `spine/initiatives/x.md`; validator blocking set = default; workflow ValidateBody `failed`), `buildOrchestrator`, `workspaceOrchestratorBuilder`, `buildWorkflowService`; `service/internal/workspace/pool_status_lifecycle_test.go` (pooled workflow service refuses; artifact service carries `.spine.yaml`'s `artifacts_dir`) and `pool_status_lifecycle_integration_test.go` (pooled validator blocking set, with a database). Deleting any one of the eight wiring lines fails a test (mutations S1–S8, all caught). Review round 1: CI now runs the database-backed one (S8), in the scenario-git-http job. |
| 14 | done | `service/scenariotest/scenarios/status_lifecycle_test.go` (`-tags scenario`, PostgreSQL 18.6): `TestPlanningRun_StatusCascadesToChildren_StatusLifecycle` (the existing scenario's variant with `harness.WithStatusLifecycle()`), `TestStandardRun_StatusLifecycleRefusesNonOwnedStatusEdit` (run failed, main unchanged, step classification `status_lifecycle_violation`), `TestStandardRun_StatusLifecycleArtifactsDir` (`WithArtifactsDir("spine")`: completes, `spine/…/task-001.md` is Completed). The original `TestPlanningRun_StatusCascadesToChildren` still passes. Review round 1: CI runs the three (scenario-git-http job, must pass, never skip). |
| 15 | done | SMP compatibility gate below (SMP `1ae9b0e3`, no SMP change); Spine gates below; no pre-existing test file was modified (`git diff --diff-filter=M main...HEAD -- '*_test.go'` is empty); `dev/lint-boundaries.sh` OK with no allowlist change. |
| 16 | done | §10 edits and the design-check corrections (new top-level §5A, the added reason codes, KNOWN-LIMITATIONS and CHANGELOG items); `TestStatusLifecycleDocTable` pins the doc table to `DefaultStatusLifecycle()`. |
| 17 | done | `TestStatusLifecycle_RewriteVerification`: `"status":`, `'status':`, `status :`, `? status`/`: X`, and the continuation line, each refused `status_rewrite_failed` with nothing merged; a planning addition authored `"status": Completed` with outcome Pending refused; an empty status value swallowing the next key refused (R1's second half); control lands `In Progress`. Mutations M3 (no re-parse — the quoted case lands Completed) and M3b (no everything-else check) fail it. |
| 18 | done | `TestStatusLifecycle_RequiresBinding` (binding off); `TestStatusLifecycle_Binding` (a push after the bound SHA changes task-002 Completed→Draft: merges with no refusal, task-002 Completed on main, the pushed commit is not an ancestor of main; `CommitMeta` without `status` + a non-owned edit → `non_owned_status_change`). Mutation E6 fails it. |
| 19 | done | `TestStatusLifecycle_RemovalGuard`: deleted, second `status:` key, `type: Taskx`, symlink, and delete + re-add with the same `id` at another path (git reports D + A, asserted) in a standard and a planning run → `non_owned_removal`, main unchanged and no recreated TASK-002 on main; controls merge (delete a non-artifact `.md`; delete an artifact added on the same branch; `git mv` with the status unchanged); a field edit that makes the projection drop the row is refused, a parseable retitle merges; O3′ (a run with no `commit.status` deleting its own TaskPath) → `not_an_artifact`. Mutations M4 (deleted → ignored) and M8 (no parse-drop removal) fail it. Review round 1: renames out of scope (to `.txt`, under `templates/`, out of `spine/` in `TestStatusLifecycle_ArtifactsDir`) refused (mutant MR8); a deletion through a criss-cross history refused by V1 (`TestStatusLifecycle_MergeResultCrissCross`). |
| 20 | done | `TestStatusLifecycle_NoDeclaredEffect`: workflow-lifecycle shape (planning, `workflows/x.yaml`, `{merge: true}`) adding a Task authored Completed → `illegal_initial_status`, authored Draft → merges and lands Draft; a standard run moving its own TaskPath Pending→Completed with no effect → `status_change_without_effect`. Mutations E2 (no O1′) and E3 (no O2′) fail it. |
| 21 | done | `TestStatusLifecycle_ArtifactsDir` (`WithArtifactsDir("spine")`): merges and `main:spine/…/t.md` is Completed; a non-owned edit under `spine/` refused; edits to a `.md` outside `spine/` and to `spine/templates/x.md` merge. Mutation M5 (raw TaskPath) fails the first case. |
| 22 | done | `TestStatusLifecycle_FailsClosed` (retryable = committing); `TestStatusLifecycle_PermanentReadErrors`: `BlobTooLargeError` on a modified non-owned `.md`, a permanent read error, a not-found for a diff-listed path → `unreadable_artifact`, not committing, routed (publish) or failed (legacy); no merge-base → `unreadable_diff`. `TestStatusLifecycle_TransientClassification` pins the split (deviation 3). |
| 23 | done | `TestStatusLifecycle_RenameOntoTaskPath` (git reports `R100 task-002 → TaskPath`, asserted): `illegal_transition` with `From == Completed` (plus `non_owned_removal` for task-002, deviation 5); `TestStatusLifecycle_QuotedPaths` covers a C-quoted `OldPath`. Mutation M7 fails it. |
| 24 | done | `TestStatusLifecycle_MergeResultCheck` (`*.md merge=union` on main, owned TaskPath Pending at base, Cancelled on main after the fork, outcome Completed, auto-push on with a bare origin): `merge_result_mismatch`, origin `main` unchanged, local `main` taken back; control merges and pushes. `TestStatusLifecycle_MergeResultMismatchUnit` (owned / non-owned-follows-main / transient read retries / replay skipped). Mutation M6 (no V1) fails it. Review round 1 (V1 judges the merge result against `main`): `TestStatusLifecycle_MergeResultCrissCross` (five criss-cross shapes — a Completed sibling reopened with natural and with author-chosen dates, a non-owned Completed task deleted, a Completed task overwritten as a Draft "addition", the owned TaskPath completed over a Cancelled `main` — all refused `merge_result_mismatch` with the local `main` taken back; control: a criss-cross carrying no status change merges), `TestStatusLifecycle_MergeResultMainMoved` (R1 lands between the judgement and the merge: retried, then refused `non_owned_status_change`, R1 kept), `TestStatusLifecycle_MergeResultMainSideRename` (main-side renames merge; a rename plus a status change main made is refused), `TestStatusLifecycle_MergeResultRules` (fake: 24 rule cases); mutants MR1–MR11. |
| 25 | done | `TestStatusLifecycle_UndeclaredBaseData`: a body edit of a non-owned `Architecture: Active` merges (Active kept); normalising it to `Living Document` → `non_owned_status_change`. |

### Deviations and design choices (with reasons)

1. **The path predicate moved to `core/artifact` as `PathScope`** (DC-5 left the choice to TASK-011
   or this task; TASK-011 kept it in `adapters/projection`). The projection's `newPathScope` /
   `artifactKey` now delegate to it; the projection's behaviour and tests are unchanged.
   `GovernedArtifactKey` adds `domain.UnsafeRepositoryPath`, which discovery refuses without
   reading (TASK-003), so an unsafe path is out of scope for the lifecycle, as for the projection.
2. **The standalone artifacts directory comes from the artifact service** (`artifact.Service.
   ArtifactsDir()`, new accessor), not a new `spineCfg` parameter on `buildOrchestrator`: it is the
   value StartRun resolved `run.TaskPath` with (the same `spineCfg.ArtifactsDir` in both builders),
   and it keeps both builder signatures unchanged for the wave's other tasks (TASK-006 also edits
   them).
3. **DC-6's transient/permanent split is infrastructure-aware.** `CLIClient` reports a cancelled
   context, a git that could not start and a git killed by a signal as an `ErrKindPermanent`
   `GitError` with empty stderr; a literal "every non-retryable error is permanent" would turn a
   shutdown into a permanent refusal (a failed legacy run). `lifecycleTransient` treats those
   (`ctx.Err()`, `*exec.Error`, `*fs.PathError`, a signalled `*exec.ExitError`) and non-git errors
   as transient; every other git error — an oversized blob/output, a not-found for a diff-listed
   path, a git that ran and refused the object — is permanent.
4. **A new reason code `unreadable_diff`**: no merge-base with `main` (an orphan bound commit) or a
   permanent merge-base/diff failure has no row in the design; retrying could never succeed, so it
   is a permanent refusal (the merge itself would have failed "unrelated histories").
5. **A rename onto the TaskPath also reports `non_owned_removal`** for the old path when it was a
   non-owned artifact (DC-7: "the OldPath side is also checked by DC-3"); the TaskPath itself is
   judged with the old path's status (`illegal_transition`, AC 23).
6. **N3 includes "stops parsing for the projection"**: a non-owned artifact `artifact.Parse`
   accepts at the merge-base and refuses at the bound commit (a required field removed) is
   `non_owned_removal`, because `DiscoverChanges` deletes its row (it is "no longer
   LifecycleFields-ok" in effect). A file that already failed `Parse` on `main` is unaffected.
7. **R1 also requires "nothing else changed"** (`EqualExceptStatus`): the regex writer's `\s*`
   runs over a line break, so an empty status value swallows the next key; the type/status
   re-parse alone would accept that rewrite.
8. **A replay of this run's own landed merge skips O4** (`cascadeBase` reports the replay; review
   round 1 adds: and so does any change `main` already holds, see Review round 1 R1-2): the
   projection may have moved after the judged merge landed on the local `main`, and every other
   rule is a pure function of git objects. A plain retry (nothing merged yet) re-runs O4.
9. *(Superseded by Review round 1 R1-1: V1 now judges the merge result against `main`.)*
   **V1 is skipped when the merge adds nothing** (`M == BaseSHA`, a replay): nothing lands in that
   attempt. V1 runs in `mergePrimaryBound` right after `verifyBoundMerge` (which is shared with code
   repositories), with TASK-005's take-back and retry rules; its expectation for a renamed
   non-owned artifact is the old path's status on `main`.
10. **The workflow planning-run start checks `commit.status`** with the orchestrator's lifecycle,
    before any branch or run record exists (`workflow.Service` would refuse it only inside the
    branch write, leaving a pending run — the TASK-014 L1 shape).
11. **O4 runs only when no other violation was found**, so a refusal never depends on a store read
    and a transient store error cannot mask a permanent verdict.
12. **`harness.WithStatusLifecycle()` implies validation** (so the blocking set has a validator to
    sit on) and fails the test with `WithoutMergeBinding`; the RuntimeOption is named
    `WithDeclaredStatusLifecycle` because the EnvOption took the design's name.
13. **Error text is sanitised**: paths and statuses that are not short, printable, single-line text
    are quoted (`strconv.QuoteToASCII`, cut) in `StatusLifecycleError.Error()`, which becomes the
    outcome detail, the step message and log fields (a status may hold a line break or U+0000).

### Mutations (overlays; the worktree untouched)

`go test -overlay` with one mutated file each (`scratchpad/mut/mutate.py`); every mutant is
caught by the named test: M1 plan returns no violations → AC 5; M2 planning condition dropped →
AC 6; M3 no R1 re-parse, M3b no everything-else check → AC 17; M4 deleted → ignored, M8 no
parse-drop removal → AC 19 (M8 first survived; `e503b63d` adds the missing case); M5 raw
TaskPath → AC 21; M6 no V1 → AC 24; M7 no N3 on a rename onto the TaskPath → AC 23; W1/W2/W3 →
AC 3; V1 no promotion, V2 child-query errors swallowed → AC 4; E1 no SC gate → AC 12; E2 no O1′,
E3 no O2′ → AC 20; E4 a signal-killed git permanent → the classification test; E5 SC gate on a
replay → the replay test; E6 no binding requirement → AC 18; E7 step classification → AC 11; E8
no planning-run pre-check → its test; S1–S8 (each of the eight wiring lines; S8 with the database)
→ AC 13.

### Gates

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` (no allowlist change) |
| `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | pass ("engine constructed and exercised; library surface is sound") |
| `go test -p 4 -count=1 ./...` | pass on the final code `e503b63d` (40 packages ok, none failed) |
| `go vet ./...`, `go vet -tags integration ./...`, `go vet -tags scenario ./...` | pass |
| `go test -race -p 4` on `./core/engine/... ./core/workflow/... ./core/validation/... ./core/artifact/... ./core/domain/... ./adapters/projection/...` | pass; again `-race -run StatusLifecycle` on the final tree |
| `SPINE_TEST_DATABASE_URL=…/s017_it go test -tags integration -race -count=1 -p 1` on `./core/{domain,artifact,workflow,validation,engine}/... ./adapters/projection/... ./service/cmd/spine/... ./service/internal/workspace/...` (PostgreSQL 18.6) | pass (`TestBuildServiceSet_ValidatorBlocksStatusConflicts` ran, not skipped) |
| `go test -tags scenario -count=1 -p 1 ./service/scenariotest/...` on the branch (`s017_scen`, `s017_reg` migrated with `migrations/registry/*.sql`) vs the same on `git archive main` (`s017_scen_main`, `s017_reg_main`) | 31 top-level failures on both, the known pre-existing set (30 + `TestWorkspace_ScopedServices`); `diff` of the two sorted `--- FAIL` lists is empty |
| scenario-git-http job: `-run '^TestGitHTTP_'`, `'^TestBootstrapInternalAdmin_'`, `'^TestWorkflowLifecycle_'` (`-v`) | 20, 2 and 4 pass, 0 fail, 0 skip; the four TASK-017 scenarios pass, 0 skip |
| git-floor job in `golang:1.26-bookworm` (git 2.39.5), worktree mounted read-only: `go test -count=1 ./adapters/git/... ./core/engine/ -run 'RefShadow\|RefGrammar\|Checkout\|EnterBranch\|CurrentBranch'`; `go test -count=1 ./adapters/git/... ./core/artifact/ ./core/workflow/`; extra `go test -count=1 -v -run TestStatusLifecycle_ ./core/engine/` | pass; all 23 `TestStatusLifecycle_*` pass on git 2.39.5 (rename detection and the `merge=union` driver behave as on 2.54) |
| `golangci-lint run --new-from-rev main` on the touched packages (plain, `--build-tags=integration`, `--build-tags=scenario`) | 0 issues (7 found and fixed in `68cbefd2`) |
| `golangci-lint run --enable-only=gosec --new-from-rev main` (plain and `--build-tags=scenario,integration`) | 0 issues |
| `gofmt -l` on every file this task changed | empty (`core/validation/engine.go`'s import block was unsorted on `main`; sorted here because its import block changed) |
| Non-printing characters in changed files | none (a zero-width space in a comment removed) |
| `codex review --base main` (plain, no model override) | round 1 on `68cbefd2`: review sub-session `01a13bd5-27e4-7951-95da-3ee3e622415d`, model `gpt-6.1-sol`: "No actionable defects were identified beyond explicitly documented, intentional limitations." Round 2 on `e503b63d` (the final code): review sub-session `01a13be9-895b-7b80-8636-a523a1f6130c`, model `gpt-6.1-sol`: "No actionable regressions were identified in the changes relative to the merge base." No finding to fix or reject. |

Self-review findings fixed before codex (`68cbefd2`): the transient/permanent split (deviation 3)
and the replay SC gate (deviation 8); found with tests, not by a reviewer.

### SMP compatibility gate

SMP checkout `/Users/bartoszszymichowski/42sid/i23/s-T017/smp`, detached at SMP `main`
`1ae9b0e3` (`.spine-revision` `d23b897`), `../spine` → this worktree, `GOFLAGS=-mod=readonly`, no
SMP edit:

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...`; `go vet -tags integration ./...`; `-tags scenario`; `-tags faultinject` | pass |
| `go test -race -count=1 -p 4` on every SMP package importing a changed Spine package (`internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}`) | all ok (`projectionsvc` has no tests) |
| `go run ./dev/apply-migrations -set platform -dir migrations` (`s017_smp_it`, PG 18.6 + pgvector) / `-set spine-runtime` (`s017_spine_runtime_it`) | 116 / 26 versions recorded |
| `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 20m ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` (TEST_SPINE_DB, TEST_PLATFORM_DB, TEST_PROVISIONING_DSN as in SMP CI) | `4 packages, 1914 tests passed, 0 failed, 2 skipped (2 allowed)`, PASS |
| `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `301 tests passed, 0 failed, 1 skipped (1 allowed)`, PASS |
| The three Linux-only skips, in `golang:1.26-bookworm` (git 2.39.5): `TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch` (`./internal/customer/spinecore/`), `TestIntegration_ReceiveTimeMemoryBounded` (`-tags integration ./internal/customer/githttp/`) | all pass |
| `ws_` databases and roles before / after | 0 / 0 |

### Not run

- Spine's GitHub CI itself (runs after a push). Everything it runs was run locally above.
- Databases: `s017_it`, `s017_scen`, `s017_reg`, `s017_scen_main`, `s017_reg_main`, `s017_smp_it`,
  `s017_spine_runtime_it` on the shared `i23-pg` (PostgreSQL 18.6), dropped after the gates.

### Handoffs

- **SMP TASK-011 (adopts P3-21.smp):** §8 items 1–6 stand. The orchestrator's validator is a
  `*spinecore.Validator` embedding `*validation.Engine`, so `StatusChangeValidator` and
  `BlockingStatusConflicts()` are promoted; SMP sets no `ArtifactsDir`, so no `WithArtifactsDir`.
  SMP's `embeddedWorkflowValidate` appends `workflow.ValidateCommitStatuses`. SMP's
  `RealGitOperator` should classify a cancelled context, a git that could not start and a killed
  git as retryable or wrap the `exec` error, so `lifecycleTransient` keeps such reads transient
  (deviation 3). Re-run `core/workflow/commit_status_compat_test.go`'s rows against SMP's live
  files; the `task-default` row needs Task `Pending → In Progress`.
- **SMP TASK-011 (review round 1 additions):** V1 now calls `MergeBase(<BaseSHA>, <merged
  commit>)`, `Diff(<BaseSHA>, <merge result>)` and, for a main-side rename of the TaskPath,
  `Diff(<plan merge-base>, <BaseSHA>)`, all with full object ids, one of them the local merge
  result before any push; O4 calls the binder's `ResolveBranchHead(main)` and reads/diffs at that
  tip. SMP's `RealGitOperator` must answer them for a commit that exists only in its clone, and its
  `MergeBase` (origin's `main`) and `MergeBound`'s `BaseSHA` should describe the same `main`:
  where they differ, V1's merge-base check retries the attempt instead of judging it.
- **TASK-006:** its guard runs in `preflightCheck`, before the lifecycle; the lifecycle's write set
  is a subset of the diff plus the TaskPath.
- **TASK-012:** lifecycle refusals are permanent and must not count as retries; transient lifecycle
  errors return from `MergeRunBranch` (run stays `committing`).
- **Follow-ups (KNOWN-LIMITATIONS):** direct-write lifecycle checks (re-parse the regex rewrite
  there too), a YAML-node status writer, SC rules from the bound tree, `blocked_by` edits, `id`
  uniqueness, Task `In Progress` reconciliation in `ValidStatusesForType`, removal of rule Q once
  every `GitOperator` diff is NUL-delimited.

### Review round 1 (2026-10-16)

Three reviewers (correctness and compatibility, adversarial security, completeness and tests)
examined `ee26995a`. Each finding was verified against the code first; every real one has a test
that fails on the code it fixes. Commits: `21b0840a` (V1 against `main`, O4 skip), `64a0ac87` (N3
rename tests), `664b35a7` (projection scope), `d401f740` (CI), `194bc6bd` (docs), `629f9b9d` (the
two codex findings on the round-1 code, R1-7 and R1-8).

| ID | Finding | Disposition |
|---|---|---|
| R1-1 | P1: V1 re-read only the paths the plan judged, with expectations from one `git merge-base`; `git merge` (ort) merges against the virtual base of every best common ancestor and against `main` at merge time, so a criss-cross bound commit or `main` moving between plan and merge lands unjudged status changes and deletions | **Fixed** |
| R1-2 | P3: O4 asked again on a resume after the merge landed when the replay is not recognised (binding moved to a recovery-ledger commit; walk limit) | **Fixed** |
| R1-3 | P2: V1 refused a rename `main` made after the fork (non-owned artifact the branch edits; the owned TaskPath) on every attempt | **Fixed** (with R1-1) |
| R1-4 | P3: the projection's `artifactKey` re-normalised `artifacts_dir`; not idempotent, so another prefix than `isWorkflow` | **Fixed** |
| R1-5 | P3: no test for N3 "renamed out of scope" (the guard narrowed to deletions passed every test) | **Fixed** (tests only; the code was right) |
| R1-6 | P3: CI never ran the AC 14 scenarios or the database-backed AC 13 wiring test | **Fixed** |
| R1-7 | codex P2 (on the round-1 code): O4 asked under the bound commit's name; after a main-side rename the projection has no row there and passes, while V1 now lets the rename land | **Fixed** |
| R1-8 | codex P2: `PathScope.RepoPath` kept `./` / `//` at the repository root, which `StartRun` accepts; the run did not own its TaskPath and the cascade's `CommitFilesOnto` refused the path | **Fixed** |

**R1-1 (P1).** Reproduced first, on real git through `MergeRunBranch` with binding and the
lifecycle on: the new `TestStatusLifecycle_MergeResultCrissCross` shapes (a `Completed` sibling
reopened, with natural and with author-chosen commit dates; a non-owned `Completed` task deleted;
a `Completed` task overwritten as a `Draft` "addition"; the owned TaskPath completed over a
`Cancelled` `main`) all merged on `ee26995a` with `main` moved, and
`TestStatusLifecycle_MergeResultMainMoved` (R1's completion lands between the judgement and the
merge; this run's branch builds on it and puts the task back to `Pending`) completed the run. Each
shape asserts `git merge-base --all` lists two bases and the plain one is `C1`. Fix — the
suggested steps 2 and 3:

- `verifyLifecycleMerge` first checks that `MergeBase(<main before the merge>, <merged commit>)`
  is still the plan's base. If not (`main` gained commits of the bound history), it returns a
  retryable error: TASK-005's take-back restores the local `main` and the run stays `committing`,
  so the next attempt judges against the base git now merges with (in the race test: refused
  `non_owned_status_change` on the retry, R1's merge kept).
- It then judges `Diff(<main before the merge>, M)` (renames detected), relative to `main`: an
  owned change the plan made (`lifecyclePlan.owned`: the TaskPath's planned transition, a
  planning run's stamped additions) must land at the planned type and status, from the judged
  status on `main` or onto `main`'s already holding it, and a planned creation must not overwrite
  a lifecycle artifact on `main`; every other artifact `main` holds keeps type, status, existence
  and its projection parse; an addition carries an initial status; another path renamed onto an
  owned one is a removal; an owned change the diff does not list must already be on `main`.
  Out-of-scope paths are ignored, as in the plan; a C-quoted `.md` path is `unparseable_path`; an
  unreadable side `unreadable_artifact`; an unreadable merge diff or merge-base `unreadable_diff`.
- Not adopted: step 1 (`git merge-base --all` and a permanent `ambiguous_merge_base`). With step
  3 what lands is judged against `main` whatever base git merged with, so a criss-cross can no
  longer land anything unjudged; `GitOperator` has no `merge-base --all`, and adding a method
  breaks SMP's `RealGitOperator` (invariant 8), while an optional interface would make the verdict
  depend on the operator; and the plan already fails closed on a criss-cross whose other base
  carries a status change (it attributes that change to the run — KNOWN-LIMITATIONS; the control
  case shows a criss-cross carrying none merges).
- **Deviation from DC-8's text** (it defined V1 "for every path the plan judged"): V1 judges every
  path the merge changes relative to `main` — strictly more, and the only form that holds whatever
  base git merges with. The plan keeps only the owned changes (`ownedChange`), not a judged-path
  list. `merge_result_mismatch`'s `From`/`To` are "expected → found" (`""` = no lifecycle artifact,
  or, as the expectation for an addition, an initial status), documented on the constant.

**R1-2 (P3).** Verified: with the binding on a ledger commit `L` (parent `B`), `cascadeBase`
returns `B` with `replay=false` (asserted in the test), the plan judges the TaskPath from `B`'s
content and O4 is asked again. Fix: O4 is not asked about a change `main`'s tip already holds
(`planner.mainHolds`: `ResolveBranchHead(main)`, then the path's type and status there; a
transient read keeps the run `committing`, anything else asks the gate as before). The merge then
lands no status change on that path, so it cannot be what trips a status-conflict rule; this covers
the ledger resume, the walk-limit fallback and a status another run set meanwhile, and keeps the
replay skip (deviation 8). `TestStatusLifecycle_ResumeAfterLanding` (ledger; 65 commits past the
walk limit; control: the gate is asked and refuses) and `TestStatusLifecycle_SCGateMainHolds`
(fake). Not adopted: `IsAncestor(<bound's first parent>, main)` — for an ordinary step binding the
bound commit's first parent is usually on `main` (a one-commit run branch forks from it), so O4
would be skipped on ordinary first merges; and "a `merge_recovery:` binding whose parent is on
`main` is a replay" misses the walk-limit case.

**R1-3 (P2).** Reproduced: `TestStatusLifecycle_MergeResultMainSideRename` failed on `ee26995a`
with `merge_result_mismatch (Task "" -> "")` (non-owned) and `(Task Completed -> "")` (owned
TaskPath). Fixed by R1-1's V1 — a rename `main` made is `main`'s state, not a change of the merge
— plus, for the owned TaskPath, a lookup of `main`'s new name through `Diff(<plan base>, <main
before the merge>)` when the old name is gone. A rename plus a status change `main` made
(`merge=union`) is refused under `main`'s name. KNOWN-LIMITATIONS: `run.TaskPath` keeps the old
name.

**R1-4 (P3).** Reproduced: `TestPathScope_OnePrefixForEverySpelling` (new file
`adapters/projection/scope_prefix_test.go`, so no pre-existing test file changes) failed for
`foo//`, `././foo`, `foo /` and ` foo/ /`. Fix: the projection's `pathScope` keeps the
`artifact.PathScope` `newPathScope` built and `artifactKey` uses it; its prefix equals `prefix`.
The CHANGELOG's "no behaviour change" now holds. SMP sets no `ArtifactsDir`.

**R1-5 (P3).** Verified a coverage gap, not a bypass: the finding's mutant (`if it.tip == ""`) is
MR8 below. Added refused cases: renamed to `.txt` and under `templates/`
(`TestStatusLifecycle_RemovalGuard`), and out of `spine/` (`TestStatusLifecycle_ArtifactsDir`,
git's `R100` asserted); main unchanged in each.

**R1-6 (P3).** Verified: `ci.yml` ran neither. The scenario-git-http job gains "Status-lifecycle
scenarios and pooled validator (must pass, never skip)": `go test -tags scenario -run
'StatusLifecycle'` (the three AC 14 scenarios, each required to `PASS`) and `go test -tags
integration -run '^TestBuildServiceSet_ValidatorBlocksStatusConflicts$'`, with the `--- SKIP`
grep of the TASK-013/014 steps. Both command lines were run locally against PostgreSQL 18.6.

**R1-7 (codex P2).** Verified and reproduced on the fake (`TestStatusLifecycle_SCGateMainHolds`
"main renamed the artifact": asked `epics/e.md`, not refused) — reachable only once R1-1/R1-3 let a
main-side rename of the owned artifact land. Fix: O4 resolves the name `main`'s tip gives the
artifact (`planner.onMain`: the bound name, or the merge-base..main rename) and asks the validator
under that projection key; the name is recorded on the owned change (`ownedChange.scAt`), and V1
retries a merge that would land the change under yet another name (main renamed it again between
the check and the merge). Real git: the moved-epic case of
`TestStatusLifecycle_MergeResultMainSideRename` (SC-002 only under the new key → refused
`status_conflict`, main unchanged). Mutants MR13 (ask under the bound name) and MR15 (no retry).

**R1-8 (codex P2).** Verified: `artifact.Service.Read` (`repoRelativePath`) trims only a leading
`/` at the root, so `StartRun("./tasks/t.md")` reads the artifact, while diffs name `tasks/t.md`;
reproduced on the fake (`not_an_artifact` / `non_owned_status_change`). Fix: `RepoPath` cleans the
path (`path.Clean` at the root; `path.Join` already cleans under a subdirectory), `""` stays `""`.
`TestStatusLifecycle_NonCanonicalTaskPath` (fake: `./`, `/`, `//`; and an authored completion of
the TaskPath spelled `./`), `TestStatusLifecycle_NonCanonicalTaskPathOnGit` (real git: completes,
`main` Completed), RepoPath spellings in `core/artifact/lifecycle_test.go`. Mutant MR14.

**Acceptance-criteria gaps.** Invariant 9 / D13 / DC-8 and AC 24: R1-1 (criss-cross, `main`
advancing, main-side renames, the fake rule table). AC 5 / OQ1 and AC 19 / OQ6 through a
criss-cross: R1-1 (`TestStatusLifecycle_MergeResultCrissCross`). AC 13 (S8) and AC 14 in CI: R1-6.

**Mutations (review round 1; `go test -overlay`, worktree untouched), all caught:** MR1 no
merge-diff loop → `MergeResultCrissCross`/`Rules`; MR2 no merge-base check → `MainMoved`,
`Rules`; MR3 `mainHolds` always false → `ResumeAfterLanding`, `SCGateMainHolds`; MR4 no
main-side rename lookup → `MainSideRename`, `Rules`; MR5 owned from-status unchecked →
`CrissCross` (owned shape), `Rules`; MR6 additions not held to an initial status → `Rules`; MR7 a
rename onto an owned path not a removal → `Rules`; MR8 N3 narrowed to deletions (the reviewer's
mutant) → `RemovalGuard`, `ArtifactsDir`; MR9 the owned post-check dropped →
`MergeResultMismatchUnit`; MR10 an owned artifact moved away by the merge accepted → `Rules`;
MR11 V1's parse-drop rule dropped → `Rules` (first survived; the "projection would drop" case was
added); MR12 `artifactKey` re-normalising → `TestPathScope_OnePrefixForEverySpelling`; MR13 O4
asks under the bound name → `SCGateMainHolds`, `MainSideRename`; MR14 `RepoPath` not cleaned →
`NonCanonicalTaskPath`, `…OnGit`; MR15 no retry on a second main-side rename → `SCGateMainHolds`.

**Gates (review round 1, on the final code).**

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `embed-smoketest` | OK; built; "engine constructed and exercised; library surface is sound" |
| `go vet ./...`, `-tags integration`, `-tags scenario` | pass |
| `go test -p 4 -count=1 ./...` | pass (no package failed) |
| `go test -race -p 4` on `./core/{engine,workflow,validation,artifact,domain}/... ./adapters/projection/... ./service/cmd/spine/... ./service/internal/workspace/...` | pass |
| `SPINE_TEST_DATABASE_URL=…/s017_it go test -tags integration -race -p 1` on the same packages (PostgreSQL 18.6) | pass |
| Full scenario suite (`-tags scenario -p 1 ./service/scenariotest/...`, `s017_scen` + `s017_reg` migrated with `migrations/registry/*.sql`) vs the same on `git archive main` (`s017_scen_main`, `s017_reg_main`) | 31 top-level failures on both (the known set, incl. `TestWorkspace_ScopedServices`); the sorted `--- FAIL` lists are identical |
| scenario-git-http job: `^TestGitHTTP_`, `^TestBootstrapInternalAdmin_`, `^TestWorkflowLifecycle_`, the new `StatusLifecycle` step, `^TestBuildServiceSet_WorkflowServiceHasProjectionPolicy$`, `^TestBuildServiceSet_ValidatorBlocksStatusConflicts$` (`-v`) | 20 / 2 / 4 / 3 pass, 0 fail, 0 skip; both integration tests pass |
| git-floor job in `golang:1.26-bookworm` (git 2.39.5): the ref-grammar filter; `./adapters/git/... ./core/artifact/ ./core/workflow/`; extra `-run TestStatusLifecycle_ ./core/engine/` and `./adapters/projection/` | pass; all 31 `TestStatusLifecycle_*` pass (criss-cross, rename detection, ort and `merge=union` as on 2.54) |
| `golangci-lint run --new-from-rev main` on the touched packages (plain, `--build-tags=integration`, `--build-tags=scenario`); `--enable-only=gosec` (plain, `scenario,integration`) | 0 issues each |
| `gofmt -l` on every Go file changed vs `main` | empty |
| Mutants MR1–MR15 re-run on the final code | all caught |
| `codex review --base main` (plain) | on `194bc6bd`: review sub-session `01a14475-b46b-7171-ba3b-361e4adf628c`, model `gpt-6.1-sol`: two P2 (R1-7, R1-8), both real and fixed in `629f9b9d`. On `629f9b9d`: sub-session `01a14481-bab9-7cf1-8c8d-34c9c0004485`, model `gpt-6.1-sol`: "No actionable defects were identified in the changes relative to the specified merge base." |

**SMP compatibility gate (final code).** `/Users/bartoszszymichowski/42sid/i23/s-T017/smp` at SMP
`main` `1ae9b0e3`, `../spine` → this worktree, `GOFLAGS=-mod=readonly`, no SMP edit: `go build
./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject` pass;
`go test -race -count=1 -p 4` on the 12 SMP packages importing a changed Spine package
(`internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}`)
all ok (`projectionsvc` has no tests); platform / spine-runtime migrations 116 / 26 versions
(`s017_smp_it`, `s017_spine_runtime_it`); `dev/testgate` runtime step "4 packages, 1914 tests
passed, 0 failed, 2 skipped (2 allowed)", githttp "301 tests passed, 0 failed, 1 skipped (1
allowed)", both PASS; the three Linux-only tests in `golang:1.26-bookworm` pass;
`ws_` databases and roles 0 before / 0 after. The SMP-visible surface changed in this round only
inside the lifecycle (off in SMP) and `artifact.PathScope.RepoPath` (no SMP caller).

**Databases.** `s017_it`, `s017_scen`, `s017_reg`, `s017_scen_main`, `s017_reg_main`,
`s017_smp_it`, `s017_spine_runtime_it` on the shared `i23-pg`, dropped after the gates.

### Review round 2 (2026-10-16)

A second-round reviewer examined the branch after round 1 (`3e2a1af7`). It confirmed R1-1 to R1-8
as fixed. It re-ran their evidence: mutants of `judgeFrozen` and the merge-base check, a git PoC of
equal-dated criss-cross bases, and the AC 13/14 Postgres steps (0 SKIP). It reported one finding.
Commit: `220d817a`.

| ID | Finding | Disposition |
|---|---|---|
| R2-1 | P3: plain golangci-lint reports one issue that is new compared with `main`: gocritic `typeDefFirst` at `core/validation/engine.go` (`type Engine struct`). The branch had declared `BlockingStatusConflicts()` and `promote()` above the type. The reported line is unchanged since March, so `--new-from-rev main` filtered it out, and the round-1 gate rows recorded 0 | **Fixed** |

**R2-1 (P3).** Reproduced on `3e2a1af7`: `golangci-lint run ./core/validation/...` reports
`engine.go:242:1: typeDefFirst` (gocritic), and `main` does not. Fix: both methods now sit below
`NewEngine`. `DefaultBlockingStatusConflicts` and `WithBlockingStatusConflicts` stay where they
were because they are not methods. No behaviour change. This is a pure move, so there is no test
that fails without it. The evidence is the lint comparison below. **Method change:** this round's
lint gate compares plain output with `main` and does not rely on `--new-from-rev`. Both trees are
linted with `--max-issues-per-linter=0 --max-same-issues=0`. Each issue is normalised to
`file: message` (line and column dropped), and the multisets are compared with `comm`. `main` is
a `git archive main`.

**Gaps.** The reviewer reported no gap besides R2-1. It found the R1-3 sub-case where a rename
falls under 50 % similarity once `main` edits the same file. That case fails closed and is already
in KNOWN-LIMITATIONS ("pairs renames with git's rename detection"), so nothing changed.

**Gates (review round 2, on `220d817a`).**

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `embed-smoketest` | OK; built; "engine constructed and exercised; library surface is sound" |
| `go vet ./...`, `-tags integration`, `-tags scenario` | pass |
| `go test -p 4 -count=1 ./...` | pass (40 packages ok, none failed) |
| `go test -race -p 4 -count=1` on `./core/{engine,workflow,validation,artifact,domain}/... ./adapters/projection/... ./service/cmd/spine/... ./service/internal/workspace/... ./service/scenariotest/harness/...` | pass (9 packages ok) |
| `SPINE_TEST_DATABASE_URL=…/s017_it go test -tags integration -race -count=1 -p 1` on `./core/{domain,artifact,workflow,validation,engine}/... ./adapters/projection/... ./service/cmd/spine/... ./service/internal/workspace/...` (PostgreSQL 18.6) | pass (8 packages ok; `TestBuildServiceSet_ValidatorBlocksStatusConflicts` PASS, not skipped) |
| scenario-git-http job on a fresh `s017_job`: `^TestGitHTTP_`, `^TestBootstrapInternalAdmin_`, `^TestWorkflowLifecycle_`, `StatusLifecycle`, `^TestBuildServiceSet_WorkflowServiceHasProjectionPolicy$`, `^TestBuildServiceSet_ValidatorBlocksStatusConflicts$` (`-v`) | 20 / 2 / 4 / 3 pass, 0 fail, 0 skip; the three AC 14 scenarios and both integration tests PASS |
| Full scenario suite (`-tags scenario -count=1 -p 1 -v ./service/scenariotest/...`; `s017_scen` + `s017_reg` migrated with `migrations/registry/*.sql`) vs the same on `git archive main` (`s017_scen_main`, `s017_reg_main`) | 31 top-level failures on both (the known set, incl. `TestWorkspace_ScopedServices`); the sorted `--- FAIL` lists are identical; the `--- PASS` lists differ only by the three TASK-017 scenarios |
| git-floor job in `golang:1.26-bookworm` (git 2.39.5): the ref-grammar filter; `./adapters/git/... ./core/artifact/ ./core/workflow/`; extra `-v -run TestStatusLifecycle_ ./core/engine/`, `./adapters/projection/ ./core/validation/` | pass; all 31 `TestStatusLifecycle_*` pass |
| Plain `golangci-lint run` on the touched packages (`./adapters/projection/... ./core/{artifact,domain,engine,validation,workflow}/... ./service/cmd/spine/... ./service/internal/workspace/...` plus `./service/scenariotest/harness/...`, or `./service/scenariotest/...` with build tags), branch vs `main`, normalised | plain: 103 vs 104, `--build-tags=integration`: 103 vs 104, `--build-tags=scenario`: 161 vs 162. 0 new on the branch in each run. The one fewer is `engine.go`'s gofmt import order, which the branch fixed |
| `golangci-lint run --enable-only=gosec`, same comparison (plain and `--build-tags=scenario`) | 0 vs 0 |
| `golangci-lint run --new-from-rev main ./...` | 0 issues |
| `gofmt -l` on every Go file changed vs `main` (37 files) | empty |
| No pre-existing test file modified (`git diff --diff-filter=MDR main...HEAD -- '*_test.go'`) | empty |
| `codex review --base main` (plain, no model override) | on `220d817a`: review sub-session `01a144c9-75e8-7eb1-9894-4ca87cff048a`, model `gpt-6.1-sol` (every `model` field in the session): "No actionable regressions were identified. The lifecycle enforcement, merge-result verification, and production wiring are consistent with the documented scope and limitations." No finding to fix or reject |

**SMP compatibility gate (review round 2, on `220d817a`).** `/Users/bartoszszymichowski/42sid/i23/s-T017/smp`
at SMP `main` `1ae9b0e3` (`.spine-revision` `d23b897`), `../spine` → this worktree,
`GOFLAGS=-mod=readonly`, no SMP edit:
- `go build ./...` and `go vet ./...` (plain, `-tags integration`, `-tags scenario`, `-tags faultinject`) pass.
- `go test -race -count=1 -p 4` passes on the 12 SMP packages importing a changed Spine package (11 ok; `projectionsvc` has no tests).
- Platform / spine-runtime migrations record 116 / 26 versions (`s017_smp_it`, `s017_spine_runtime_it`).
- The `dev/testgate` runtime step reports "4 packages, 1914 tests passed, 0 failed, 2 skipped (2 allowed)", PASS.
- The `dev/testgate` githttp suite reports "301 tests passed, 0 failed, 1 skipped (1 allowed)", PASS.
- The three Linux-only tests pass in `golang:1.26-bookworm`.
- `ws_` databases and roles: 0 before, 0 after.

**Databases.** `s017_it`, `s017_job`, `s017_scen`, `s017_reg`, `s017_scen_main`, `s017_reg_main`,
`s017_smp_it` and `s017_spine_runtime_it` were created on the shared `i23-pg` and dropped after
the gates; none remain. The containers ran with `--rm`; none remain.
