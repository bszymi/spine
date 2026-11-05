---
id: ADR-016
type: ADR
title: Governance-Path Guard on Governed Merges
status: Accepted
date: 2026-10-14
decision_makers: Spine Architecture; the owner (bszymi) signed off the authorization model on 2026-11-05 (INIT-023 TASK-006 owner question 1)
---

# ADR-016 — Governance-Path Guard on Governed Merges

**Accepted by the owner, 2026-11-05.** Proposed on 2026-10-14. The owner approved the decision as
built, including the authorization model in §4 (INIT-023 TASK-006 owner question 1, option (a)).

---

## Context

[ADR-009](/architecture/adr/ADR-009-branch-protection.md) made branch protection branch-level and flat (branch + kind) and allowed every governed merge (`OpGovernedMerge`) unconditionally. It named the resulting gap explicitly (§1 bullet 2, §5, Consequences): a run branch can carry changes to governance files — `/.spine/branch-protection.yaml`, workflow definitions, `.spine.yaml` — and the governing workflow's review step is the only gate on that path. It deferred a merge-time path guard to "a future ADR … once there is evidence it is warranted".

The 2026-10-06 security audit is that evidence (finding F3, "run-branch slippage", both Spine and the Spine Management Platform). A single contributor who can pass or skip review (in SMP one member could self-assign and self-accept the review step) commits `rules: []` and a review-less `workflows/task-default.yaml` to their run branch, review accepts, the publish step merges, and the projection installs zero branch-protection rules and the review-less workflow as live policy. Any contributor may then direct-write or delete `main`, every later run uses the review-less workflow, and no `branch_protection.override` event is ever emitted. The projection never asked how the change arrived.

Two further facts shape the design:

- [INIT-023 TASK-005](/initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-005-merge-bound-reviewed-sha.md) binds every governed merge to one commit (`spine.bound_sha`) and merges exactly that commit. A guard that evaluates a branch name would be bypassed by anything pushed between the guard and the merge; a guard that evaluates the bound commit is not.
- A single `merge-base` is not enough. When a run branch has two merge bases with `main` (a criss-cross history: the run branch merged another run's branch and resolved a governance file back to its old content, and that other run then landed on `main`), `Diff(merge-base(main, S), S)` does not list the file, yet git's recursive merge reverts it on `main`. This was reproduced against the real `git.CLIClient`.

---

## Decision

### 1. What this supersedes

This ADR supersedes ADR-009's decision not to add a merge-time path guard in v1 (§1 bullet 2, §1's closing paragraph, §5's two "v1 does not add a merge-time path guard" statements, and Consequences, negative bullet 3). It does **not** add a branch-protection rule kind: the ruleset in `/.spine/branch-protection.yaml` stays flat (ADR-009 §2/§6). The guard is a merge-authorization rule configured by the deployment, not a per-branch rule.

### 2. Guarded paths

A governed merge is **governance-changing** when it changes any path under a guarded entry. The default entries are `.spine/`, `.spine.yaml` and `workflows/`, plus every workflow directory the projection installs definitions from — `<artifacts_dir>/workflows/` when `.spine.yaml` names an `artifacts_dir` (the projection's `WorkflowDirs`). The set is configurable per deployment.

- Matching is a case-folded prefix match on git paths, so a case-insensitive checkout cannot dodge it. Every entry also covers what lies beneath it (`.spine.yaml/x`, a file named `workflows`), so replacing a directory with a file, or the reverse, is governed too.
- Both sides of a rename count, and so do deletions.
- A path the guard cannot read safely — git's C-quoted form, a control byte — counts as governed.
- **Own-run evidence carve-out** (TASK-006 owner question 9). Paths under `.spine/runs/<the run's own id>/evidence/` (compared byte-exactly) never need approval; this is the documented execution-evidence location ([Execution Evidence](/architecture/execution-evidence.md) §6.1). Another run's evidence directory stays governed: writing it would forge that run's evidence.
- AI-agent configuration (`.claude/`, `.codex/`, `AGENTS.md`, …) is **not** guarded by default (TASK-006 owner question 2). It does not change governance; SMP already flags it on the run page.

**Implementation note (INIT-024 TASK-002, review finding N-5).** The guarded set is built when each merge is decided, not once at setup, from three sources:

1. the configured entries (`Paths`, or the defaults, plus `WorkflowDirs`), as before;
2. `<artifacts_dir>/workflows/` when the orchestrator has an artifacts directory (`WithArtifactsDir`). It is read at decision time, so the order of `WithArtifactsDir` and `WithGovernanceGuard` does not matter. As a prefix entry it covers nested files too;
3. a structural backstop: every changed `*.yaml` / `*.yml` file that sits directly in a directory whose last segment is `workflows`, at any depth, compared case-folded like every entry (`branchprotect.WorkflowDefinitionPaths`). Each such file is an entry of its own; its directory is not, so a script, README or nested file the same merge changes beside it is judged by sources 1 and 2 alone.

The backstop over-approximates the projection's workflow directories for every possible `artifacts_dir`, so an embedder that configured the projection's directory without telling the guard or the orchestrator still holds the merge. Its cost is an operator approval for a YAML file directly in any directory named `workflows` that is not a Spine definition (`.github/workflows/ci.yml`); whether to apply it only when the guard knows no workflow directory, or to give embedders an opt-out, is INIT-024 TASK-002 owner question 3 (default: always on). Sources 2 and 3 are added as literal prefix entries (`GovernancePaths.WithLiteralEntries`): unlike configured entries they may contain glob or quoting characters, which are legal in git paths, and matching stays a literal folded prefix compare. The post-merge assertion (§3) applies the backstop to the paths the merge actually changed. What the backstop does not catch is a nested file (`<dir>/workflows/steps/x.yaml`) of a directory that neither `WorkflowDirs` nor the orchestrator names; the projection does not install nested files as definitions.

### 3. The changed-path set is evaluated in two parts

- **Before the merge**, the changed paths are the union of `Diff(B, S)` over **every** merge base `B` of `main` and `S` (`git merge-base --all`), where `S` is the commit the merge will actually merge: the bound commit, or the status-cascade commit the engine builds on it. If `S` agrees with every merge base on a path, git's virtual merge base agrees too (recursively merging identical sides is a no-op), so a standard merge keeps `main`'s version of that path.
- **After the merge, before the push**, the governed paths the merge actually changed (`Diff(BaseSHA, M)`) must be a subset of what the first part evaluated. This covers merge behaviour the first part cannot see: under git's **default** configuration, rename following — a run edits a path that `main` has since renamed into a governed tree, and git carries the edit onto the governed name — and, beyond it, non-default configuration (`merge.directoryRenames`, merge drivers from the host's git configuration). A violation is a **permanent refusal** (TASK-006 owner question 6): the local target is restored to `BaseSHA`, nothing is pushed, and the run takes the workflow's `merge_failed` route (or fails, on a workflow with no publish step). The refusal tells the author the remedy: update the run branch from `main` and resubmit; the merge base then moves, the governed path is evaluated by the first part, and the merge is held for an operator decision.

If the set cannot be computed the merge is refused, never allowed. A failure that may be transient (a git or store error) keeps the run in `committing` and is retried; a merge that can never be evaluated — no merge base (an unrelated history), a diff over the git output limit — is refused permanently on its first attempt and takes the same `merge_failed` route, so no run is left in `committing` with nothing an operator could decide.

### 4. Authorization: an operator approves the exact bound commit

This is TASK-006 owner question 1, default **(a)**, which the owner signed off on 2026-11-05:

- A governance-changing merge is **held**. The run stays in `committing`, nothing is merged (no code repository either), and the hold is recorded on the run's publish step (or, on a workflow with no publish step, its last completed step) as `error_detail.classification = governance_approval_required`, with a `governance.merge_held` event.
- An actor with role **operator or higher** approves or rejects through an engine API (`DecideGovernanceMerge`; standalone `POST /runs/{run_id}/governance-decision`). The approval names the bound commit and is recorded server-side under reserved `runs.commit_meta` keys (`spine.governance_approval.*`) and audited by a `governance.merge_decided` event. It is never read from commit text, branch content or workflow-authored data: workflow outcomes cannot write reserved keys (TASK-005 strips them).
- An approval binds to the bound commit, not to a branch: a run that loops back and binds a new commit needs a new approval, and an approval for another commit is reported as stale.
- **Approve** records the approval — only while the run is still `committing` — and runs the merge at once; approving again is a manual merge retry. **Reject** clears any approval (the keys are emptied; JSONB merge cannot delete a key), takes the workflow's `merge_failed` route — back to `execute` in `task-default` — or fails the run on a workflow with no publish step, with the branch preserved, and clears the approval once more after the route. Cancel is not allowed from `committing`, so reject is the way out.
- **Decisions are ordered with merges.** Within one process every merge attempt of a run (while the guard is on) and every decision on it hold the run's governance stripe: a reject that arrives while an approved merge is in flight waits for it, and is answered `409` (the merge won) when that merge completed or reached the local `main`; an approve can never recreate an approval a reject revoked. Across processes sharing one database the approval write and the double revocation keep the second guarantee; serialising a reject with an approved merge in flight in another process is left to the embedder (KNOWN-LIMITATIONS).
- There is no separation-of-duties rule on the approver: an operator can already land any change with an override push (ADR-009 §4/§5), so a self-approval grants nothing new, and requiring a second operator would deadlock single-operator workspaces.

The decision is one pure function (`branchprotect.EvaluateGovernance`), consulted through `Policy.Evaluate` (the facts travel on `branchprotect.Request.Governance`) **and** by the engine itself, so a permissive or custom policy cannot turn the guard off.

### 5. ADR-008 interaction (lifecycle exemption)

A workflow-lifecycle planning run may merge its **own** workflow file without operator approval when the deployment enables the exemption (TASK-006 owner question 3: on in `spine serve`, off in the embedded library's zero value). It applies only when all of these hold (owner question 10):

- the run is a planning run whose task path is exactly `workflows/<id>.yaml`, `<id>` being the parsed workflow's id (byte-exact);
- the task path is not the governing workflow's own path, and the governing workflow declares `applies_to: [Workflow]`;
- neither the edited file nor its version at any merge base or at the target's current tip declares `applies_to: [Workflow]`, and after the merge the result is checked again (an exempt edit can never create, edit or shadow a workflow-governing binding, even when the target changes between the check and the merge);
- the last completed review step was completed by someone who neither started the run (the starter wrote the body onto the branch; the engine records them at start, and a run with no recorded starter gets no exemption) nor is a run author — bound to any author step execution of the run, in any status, or holding an assignment row of one — a local separation-of-duties check that holds whatever the deployment's step-entry policy says.

Every other governance path in that run still needs approval. ADR-008 §2 carries an "Amended by ADR-016" note.

### 6. Projection protection-removal guard

The projection never installs a branch-protection config that removes protection from the authoritative branch (the kinds the installed rules apply to `main` are not a subset of the candidate's) unless a **database record of an operator act that changed the config** matches the content being installed (owner question 8):

- an honoured `branch_protection.override` git push whose old→new range changed `.spine/branch-protection.yaml` (`runtime.event_log`), or
- an approved governed merge whose recorded `BaseSHA`→`M` changed it **and accounts for every kind being removed** (`runtime.runs.commit_meta`, the consumption marker the engine writes before the push): its own diff removed the kind (the config at `BaseSHA` applied it), or the kind was already missing at `BaseSHA` and that config is, byte for byte, the result of another such record — newer than the installed rules, with `BaseSHA` descending from its commit — that accounts for it in turn; an override record at the end of that chain accounts for every kind its bytes lack (a chain of custody). The operator approved the run's diff; if `main` already carried an unauthorised relaxation (refused, the rules kept), a diff on top of it that only adds a rule must not vouch for the relaxation underneath. Yet two authorised relaxations that land before one projection tick (two approvals inside one poll, or a sync held meanwhile) must both count, or the second one's result is refused for good (TASK-006 review round 2). The chain links by bytes and by history, never by "some record in the window removed this kind": an unauthorised write between two authorised acts breaks it (§8).

A record matches when it is a full commit id, reachable from the sync head, newer than the installed rules (the installed rows' `source_commit`, or — while the bootstrap rows are installed — the sync's starting commit), and its config bytes equal the candidate's. Commit trailers are never used: any run-branch author can forge them. An override push is held to the changed-the-file rule only: the operator wrote the bytes being installed, which is what the documented re-affirmation relies on — so an editing override push vouches for the whole file it leaves, a relaxation already on `main` included (§8).

When no record matches, the previous rules stay installed, a `branch_protection.removal_refused` event is emitted and a Warn is logged. This is **not a sync error** (owner question 7; INIT-023 TASK-011 D2): the sync advances, so the diff window does not grow without bound and SMP's governed document index (which reconciles only at `idle`) does not stall. The refusal is re-evaluated on the next change to the file and on every full rebuild. The guard covers the authoritative branch only (owner question 5).

### 7. Opt-in for embedders, on in `spine serve`

Both guards are off at their zero value, and the embedded library then behaves exactly as before (SMP adopts them in its TASK-010). `spine serve` turns both on; `SPINE_GOVERNANCE_GUARD=off` (`false`, `0`, `no`) restores v1 behaviour (owner question 4). The merge guard requires SHA-bound merges (TASK-005) and a git operator that can list every merge base; enabling it without them is a configuration error.

### 8. Accepted gaps

- While the installed rules no longer carry `no-direct-write` on the authoritative branch, the projection guard is inactive. This keeps ADR-009 §1 bullet 3 consistent: self-protection is already off, and direct pushes produce no override event to vouch for anything.
- A database rebuilt from git refuses a relaxed config until an operator re-affirms it with an override push that **edits** the file (a comment line is enough). An override push that does not touch the file proves nothing.
- Override provenance is the honoured override's row in `runtime.event_log`. The git pre-receive gate emits the event before git moves the ref; SMP's emitter writes the row synchronously, and with the guards on `spine serve`'s push path writes it synchronously too (whatever `SPINE_EVENT_DELIVERY` says), so the projection never sees the push before its record. The event log is pruned by retention, which is why a database rebuilt later needs the re-affirmation.
- Convergence no longer moves content into `main` outside `MergeRunBranch` (audit F8): since INIT-023 TASK-012 the selected divergence branches merge into the run branch only, so their content reaches `main` through the governed merge and this guard (noted at the INIT-023 wave 4 integration).
- The consumption marker is written after the merge moves the local `main` and before the push, so a projection that follows the local `main` (single-workspace `spine serve`) can sync in between. A removal it cannot match while a run is still `committing` with an approval whose commit is on the head and no reachable marker holds the sync (retried) instead of being refused; it is decided on the next tick, once the marker exists.
- Decisions are serialised with merge attempts within one process only (§4).
- Several operator acts reaching `main` before one projection install are judged as a chain of byte-identical states (§6). Any unauthorised write to the file between two of them, even a comment-only edit, breaks the chain and the result is refused until an editing override push re-affirms it. Newness is judged against the installed rules, not against later acts in the same window, so an unauthorised write that restores the exact bytes of an earlier act in that window (after the file was tightened again) is installed on that act's record. Telling the two apart needs the file's commit log, which would need a new `git.GitClient` method (see the trailer and path-log alternative below); every install ends the window.

---

## Consequences

### Positive

- A run branch can no longer change governance files on `main` — branch protection, workflow definitions, `.spine.yaml`, agent definitions under `.spine/` — without an operator approving the exact commit. F3 is closed in standalone by default.
- The projection no longer trusts content that reached `main` by any route: a relaxation needs a matching operator record, so a bypass of the merge guard does not silently unprotect `main`.
- The approval is a server-side record bound to one commit, audited by events, and consumed exactly once by the merge it authorised.

### Negative

- Runs whose agents legitimately edit `workflows/**` or `.spine/**` (for example step profiles or agent definitions) now wait for an operator. Lifecycle runs editing their own workflow are exempt only under the conditions in §5.
- A held run stays in `committing` until an operator decides; there is no timeout. The standalone commit-retry sweep re-evaluates it each tick without logging an error.
- Recovery after a database rebuild needs an operator re-affirmation (§8).

### Neutral

- No schema change: the approval and the consumption marker live in reserved `runs.commit_meta` keys, the hold in `step_executions.error_detail`, and the audit in three new event types.
- No branch-protection rule kind is added; ADR-009's flat ruleset is unchanged.

---

## Alternatives Considered

### Refuse every governance-changing merge (owner question 1, option b)

Governance files would then change only through an override push or the ADR-008 operator bypass. Rejected as the default: it removes the governed path for legitimate governance work and pushes every such change onto the least-audited path.

### A second reviewer instead of an operator (owner question 1, option c)

Depends on separation of duties (TASK-007) and is weaker than ADR-009 §5's operator-only intent for protection changes.

### A single `merge-base` diff

Provably bypassable by a criss-cross history (Context). The union over every merge base makes the pre-merge evaluation exact for content git merges path by path; the post-merge assertion covers everything else — rename following under default configuration as well as non-default merge configuration — so the guard is independent of merge semantics.

### `git merge-tree --write-tree` to precompute the result

Needs git 2.38+ on every image and a new operator method every embedder must implement; the union plus the post-merge assertion gives the same guarantee.

### Provenance from commit trailers or a first-parent path log

Trailers are forgeable by any run-branch author. A path log needs a new `git.GitClient` method, which would break embedders' implementations of the interface.

### Make a refused relaxation a sync error

More visible, but it freezes `last_synced_commit` (the diff window grows every tick) and stops SMP's governed document index for the workspace until an operator acts.

---

## Links

- [ADR-008 — Workflow Lifecycle Governance](/architecture/adr/ADR-008-workflow-lifecycle-governance.md)
- [ADR-009 — Branch Protection](/architecture/adr/ADR-009-branch-protection.md)
- [INIT-023 TASK-006 — Governance-path guard](/initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-006-governance-path-guard.md)
- [INIT-023 TASK-005 — Merge bound to the reviewed commit](/initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-005-merge-bound-reviewed-sha.md)
- [Security Model](/architecture/security-model.md)
- [Event Schemas](/architecture/event-schemas.md)
- [Engine State Machine](/architecture/engine-state-machine.md)
- [Git Push Guide](/docs/git-push-guide.md)
