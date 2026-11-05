---
id: TASK-002
type: Task
title: "One ref grammar for the git CLI client: qualified branch arguments, validated refs, --end-of-options"
status: Completed
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: bugfix
created: 2026-10-06
last_updated: 2026-11-04
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-009-git-argv-sentinels.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-001-pre-receive-refname-validation.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/tasks/TASK-016-run-branch-push-admission.md
---

# TASK-002 — One ref grammar for the git CLI client: qualified branch arguments, validated refs, --end-of-options

---

## Purpose

**Findings covered:** F10 (P2), Spine adapter half (`F10.spine-refs`); P3-32; P3-36 (dormant).
All line numbers are at Spine `61994fe` unless they name an SMP path, which are at SMP `80995559`.

This task is the **only** place that decides how `adapters/git` turns a caller's ref string into
git argv. TASK-003 (bounded reads, `-z`) and TASK-005 (SHA-bound merge) build on the argv shapes
defined here and must not re-decide them.

### F10 — a pushed tag or ref shadows a run branch or `main` (P2)

**Root cause.** The engine passes bare branch names, and `CLIClient` hands them to git verbatim:

- Merge source: `core/engine/merge.go:168-174` (`Source: run.BranchName`) → `adapters/git/cli.go:389`.
- Push: `merge.go:229` (`Push(ctx, "origin", "main")`) → `cli.go:609`.

git resolves a bare `<n>` in DWIM order: `$GIT_DIR/<n>`, `refs/<n>`, `refs/tags/<n>`,
`refs/heads/<n>`, … Branch protection allows every push outside `refs/heads/`
(`core/branchprotect/policy.go:80-85`, ADR-009 §6). A pushed tag or ref therefore beats the branch.

**Attacker.** A1, A2 or A6: anyone who can push a non-branch ref into the repository the engine's
`CLIClient` works on. In standalone this is the served repository itself (git receive-pack enabled).
The PoC `poc/verify-r2-0/push_phase1_test.go` pushes `refs/tags/<run-branch>` and `refs/tags/main`
through the real `githttp.Handler`; both are admitted. Run-branch suffixes are 32 random bits, so
the tag is pushed after the run exists.

**Impact.** Re-run on 2026-10-06 at `61994fe`:

1. **Unreviewed commit on `main`.** With `refs/tags/<victim-run-branch>` pushed, the victim's next
   governed merge publishes the tag's commit under the trusted `Merge run …` message, and the run
   completes. The reviewed `refs/heads` tip never reaches `main`.
   - `poc/gap0-ref-namespace-shadow/engine_refshadow_poc_test.go:101`
   - `poc/verify-r2-0/merge_phase2_test.go` scenario A
2. **Workspace-wide merge denial of service.** With `refs/tags/main` present, `git push origin main`
   fails with `src refspec main matches more than one`. That is classified as permanent, so every
   governed merge in the workspace fails.
   - `engine_refshadow_poc_test.go:133`
   - `merge_phase2_test.go` scenario B
3. **Bare-name reads follow the shadow** (`engine_refshadow_poc_test.go:161`). `ReadFile` and
   `RefSHA` with the run-branch name return the tag's content and SHA. Two consequences, verified
   while designing this task:
   - **Content still reaches `main` with a qualified merge source.** `applyCommitStatus`
     (`merge.go:599-675`) works like this:
     - `cascadePaths` runs `MergeBase` (`:690`) and `Diff` (`:696`) with the bare `run.BranchName`.
     - Each path is read from the same bare name (`:606`).
     - The status is rewritten and the file is committed onto the real branch (`:643-665`).
     - That branch is then merged.
     A prototype that only qualified `Merge` and `Push` still put the tag's `tasks/t.md` body on
     `main` (scratch test `TestScratch_CascadeFollowsShadowTag`; see Implementation reference).
   - **Every other bare-name read site is exposed:**
     - `core/engine/branch_validation.go:46`: `DiscoverChanges(…, "main", run.BranchName)`;
     - `core/evidence/summary.go:180`: the evidence reads, with the run branch first;
     - `service/internal/gateway/handlers_artifact_entry.go:234`: `NextID(…, run.BranchName)`;
     - `core/engine/multi_repo_branch.go:129`: `RefSHA(base)`;
     - TASK-005's planned capture of the reviewed SHA, which goes through `RefSHA`.

**Surface.**
- **Standalone:** directly exploitable.
- **Embedded (SMP):** the governed merge goes through SMP's own `RealGitOperator`, so its half is
  SMP TASK-005 and TASK-009. SMP still calls `CLIClient` directly from several sites, listed in
  Design §6. Only one passes a bare branch name to a read: `spineembedded/artifacts_addtorun.go:120`,
  `NextID(…, writer.Lister(), …, run.BranchName)`. This task covers that site.

> **Design-check note (2026-10-06), DC-3 and DC-8: two corrections to "Surface".**
>
> - **A second SMP bare-name read site.** `artifacts_addtorun.go:111` calls
>   `resolveParentFromRef(…, run.BranchName)`. That runs `artifact.Service.List(ctx, run.BranchName)`
>   (`spineembedded/artifact_entry_helpers.go:37`), which makes one `ListFiles` call and one
>   `ReadFile` call per file through the same `CLIClient`. Branch-first resolution covers it too.
>   See DC-7 for its cost.
> - **Embedded is not only latent: a remote-side shadow breaks every push now.** If the *remote*
>   holds `refs/tags/refs/heads/<n>`, `refs/refs/heads/<n>` or `refs/heads/refs/heads/<n>`, every
>   git client that pushes to `refs/heads/<n>` fails with
>   `dst refspec refs/heads/<n> matches more than one`. This is true whatever the source form,
>   including bare `main`, `refs/heads/main:refs/heads/main` and `<sha>^{commit}:refs/heads/main`.
>   `push --delete` fails the same way. Verified on git 2.39.5 and 2.54.
>   - Branch protection admits all three names for a Contributor when only the seeded `main` rule
>     exists. Overlay test: `_work/task002-check/overlay/zz_dc_doublequal_test.go`.
>   - In SMP, the remote is the workspace bare repository behind `/git`. One member push of
>     `refs/heads/refs/heads/main` therefore breaks, at `80995559` and with no clone-timing
>     precondition, all of these pushes to `main`:
>     - the governed-merge push of `RealGitOperator` (`gitoperator.go:512`, `push <url> main`);
>     - the artifact writer's auto-push;
>     - the workflow writer's push (`workflowwriter.go:155`).
>   - No client argv can avoid this, so this task cannot fix it. Admission must refuse these names:
>     SMP E4-TASK-005 §7 for SMP, and Spine TASK-016 for standalone. Spine TASK-016 currently
>     classifies `refs/heads/refs/heads/<x>` as an ordinary `branch`. See DC-3 in the Design check
>     section.

### P3-32 — refs reach git argv without validation or an option terminator (P3)

Only `Diff` and `MergeBase` validate their refs (`cli.go:427-432`, `:572-577`), and `Clone` uses
`--` (`:296`). These methods put caller strings into argv where a leading `-` is parsed as an
option:

- `Checkout` (`:335`);
- `Merge`: `Target` at `:367`, `Source` at `:389`;
- `CreateBranch` (`:407`) and `DeleteBranch` (`:413`);
- `ReadFile` (`:500`), `ListFiles` (`:524`) and `RefSHA` (`:555`);
- `Log`'s `Since` (`:484`);
- `Push`, `PushBranch` and `DeleteRemoteBranch` (`:609`, `:615`, `:621`), for both remote and ref;
- `EnterBranch` (`adapters/git/branchwrite.go:75`). Its validator is optional.

There is a concrete trigger today. P3-36's `default_branch` reaches `git branch <run> <base>`,
`git checkout <target>` and `git push origin <branch>` as a bare argv element (re-run on
2026-10-06, `poc/gap3-multi-repo-code-repo/poc_gap3_multirepo_test.go`):

- **`--force` (`:148`):** the code-repo merge force-pushed and dropped a collaborator's upstream
  commit (`CONFIRMED`).
- **`--pathspec-from-file=<host file>` (`:193`):** the file's lines were echoed into
  `RepositoryMergeOutcome.FailureDetail` (`CONFIRMED`).

### P3-36 — repository `default_branch` is never ref-validated (P3, dormant)

The only checks on `default_branch` are for empty or blank values:
- `adapters/repository/manager.go:132` and `:210` (blank);
- `adapters/repository/catalog.go:329` (empty);
- `core/engine/multi_repo_merge.go:176-179` and `core/engine/multi_repo_branch.go:183-186` (empty).

The value then reaches `client.Merge(Target: repo.DefaultBranch)` (`multi_repo_merge.go:204-210`),
`client.Push(…, repo.DefaultBranch)` (`:237`), `RefSHA(base)` and `CreateBranch(run, base)`
(`multi_repo_branch.go:129`, `:139`).

It is dormant in shipped builds:
- `spine serve` wires no `RepositoryManager`.
- Both registries load a primary-only catalog.
- SMP wires neither `WithRepositoryResolver` nor `WithRepositoryGitClients`.

It becomes reachable the moment a code-repo catalog is wired. The fix is a cheap guard, so this
task fixes it instead of descoping it.

## Deliverable

- `adapters/git/refgrammar.go` (new): the grammar helpers and exported predicates.
- `adapters/git/cli.go`: every method's argv. The grammar is documented on the `CLIClient` type.
- `adapters/git/branchwrite.go`: `EnterBranch` and `CurrentBranch`.
- `adapters/git/errors.go`: one classifier pattern.
- `adapters/git/refname/refname.go`: `Validate` converges on TASK-001's `ValidateQualified`.
- `core/engine/multi_repo_merge.go` and `core/engine/multi_repo_branch.go`: the `default_branch`
  guard.
- `adapters/repository/manager.go`: `Register` and `Update` input guard.
- `service/cmd/spine/serve_cmd.go`: git version floor at startup.
- Regression tests (Acceptance Criteria), `KNOWN-LIMITATIONS.md`, `CHANGELOG.md`, and
  `architecture/git-integration.md` §6.4.
- *Design-check additions (2026-10-06):*
  - `core/artifact/service.go` `List`: resolve the ref once (DC-7).
  - `adapters/git/refgrammar.go`:
    - the exact branch lookup (`show-ref --verify --hash`, DC-2);
    - two-phase validation helpers (DC-4).
  - `adapters/git/branchwrite.go` `EnterBranch`: add the worktree detached, then switch (DC-5).

## Design

### 1. The grammar: four argument classes

Every ref-like parameter of `CLIClient`, plus `EnterBranch`, belongs to exactly one class. The
mapping happens in one helper per class, in `adapters/git/refgrammar.go`. No method builds ref argv
any other way.

| Class | Parameters | Accepted input | Passed to git as | Refused |
|---|---|---|---|---|
| **Branch** | `Checkout(branch)`; `MergeOpts.Target` (non-empty); `CreateBranch` name; `DeleteBranch` name; `Push` ref; `PushBranch` branch; `DeleteRemoteBranch` branch; `EnterBranch` branch | `<n>` or `refs/heads/<n>`, where `<n>` passes `refname.Validate` (§3) | the short `<n>` for `switch`, `branch` and `worktree add`; the fully qualified `refs/heads/<n>` for push refspecs | everything else, including `HEAD`, `@`, `refs/tags/*`, any other `refs/*`, object names, rev expressions, `""`, and a leading `-` |
| **Commit** | `MergeOpts.Source`; `CreateBranch` base | a full object name (`^[0-9a-fA-F]{40}$` or `{64}`); `CreateBranch` base also takes `HEAD`; otherwise the Branch class | the object name, `HEAD`, or `refs/heads/<n>` | abbreviated SHAs, tags, `refs/remotes/*`, rev expressions, `""` |
| **Rev** | `ReadFile` ref; `ListFiles` ref; `Diff` from/to; `MergeBase` a/b; `RefSHA` ref; `LogOpts.Since` | non-empty, and passes `ValidateRef` (`credentials.go:61-77`, unchanged) | see resolution below | `""`, and anything `ValidateRef` rejects (leading `-`, `~ ^ : \`, space, control bytes, `..`) |
| **Remote** | `remote` of `Push`, `PushBranch`, `DeleteRemoteBranch` | `^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9._-]+)*$` with no `..` | unchanged | URLs, `ext::…`, a leading `-`, anything else |

**Rev resolution is branch-first.** It is computed by `resolveRev`:
1. A full object name, `HEAD`, a pseudo-ref (`^[A-Z_]*HEAD$`), or anything starting with `refs/`
   is passed through unchanged.
2. A bare name that is not a valid branch name, such as `main@{1}`, is passed through unchanged.
3. Otherwise run `git rev-parse --verify --quiet refs/heads/<n>`, with no DWIM:
   - **exit 0:** use `refs/heads/<n>`;
   - **exit 1:** pass `<n>` through, so tags, abbreviated SHAs and `origin/main` resolve exactly as
     they do today;
   - **any other exit status or exec failure:** return the classified `*GitError`. It fails closed
     and never falls back to DWIM.

> **Design-check note (2026-10-06), DC-2: the probe must be exact, and the object name must reach
> git.**
>
> - **A qualified name is still subject to DWIM.** git resolves `refs/heads/<n>` through its DWIM
>   rules. If the branch does not exist, git tries these names in turn:
>   - `refs/refs/heads/<n>`;
>   - `refs/tags/refs/heads/<n>`;
>   - `refs/heads/refs/heads/<n>`;
>   - `refs/remotes/refs/heads/<n>`.
> - **Verified on git 2.39.5 and 2.54:**
>   - `rev-parse --verify --quiet refs/heads/<n>` exits **0** and prints the shadow's SHA.
>   - `git merge refs/heads/<n>` merges the shadow.
>   - The prototype's `Merge(Source: <deleted run branch>)` put `refs/tags/refs/heads/<run>` on
>     `main`, and `ReadFile` and `RefSHA` returned it. Test:
>     `_work/task002-check/proto/tree/adapters/git/zz_dc_bypass_test.go`,
>     `TestDC_MissingBranchQualifiedNameFollowsShadow`.
> - **Who can trigger it.** An attacker who can delete a run branch (F2's capability until TASK-016
>   lands) and push a tag can merge unreviewed content. A concurrent deletion between the probe and
>   the command does the same.
> - **Change, part 1: exact lookup.** Branch existence is tested only with an exact lookup that
>   never applies DWIM: `git show-ref --verify --hash refs/heads/<n>`.
>   - Exit 0 prints the SHA.
>   - A missing ref exits non-zero, with `not a valid ref` on stderr. Map that to "missing" by
>     exit status and that marker, not through `classifyGitError`.
>   - Exactness was verified on both versions.
> - **Change, part 2: pass the object name.** The resolved object name reaches git, not
>   `refs/heads/<n>`:
>   - for a bare name that is a branch (rule 3, exit 0);
>   - for a Rev input `refs/heads/<n>`. This one is now handled like a branch: exact lookup, and
>     `ErrKindNotFound` when missing, with **no** DWIM fallback. It is no longer passed through
>     under rule 1.
>   - for the Commit class (merge source, `CreateBranch` base);
>   - for `Push` and `PushBranch` (DC-1);
>   - for `EnterBranch` (DC-5).
> - **What stays unchanged.** `refs/tags/*`, `refs/remotes/*` and other `refs/*` inputs still pass
>   through. If such a ref is missing, git's DWIM can substitute a doubly-qualified name. Admission
>   closes that (DC-3), and §7 records it as a residual.
> - **Cost.** One process per resolved branch, the same as the designed probe. `RefSHA` saves one,
>   because the lookup already yields the SHA.
>
> **Design-check note (2026-10-06), DC-6: rule 1 passes `HEAD` only.**
>
> - The pattern `^[A-Z_]*HEAD$` lets a legitimate branch such as `HOTFIXHEAD` skip branch-first
>   resolution, so `refs/tags/HOTFIXHEAD` would shadow it.
> - No Spine or SMP caller passes any pseudo-ref other than `HEAD` (grep for `FETCH_HEAD`,
>   `ORIG_HEAD` and `MERGE_HEAD` finds no caller).
> - Rule 1 is therefore: a full object name, or exactly `HEAD`.
> - Other pseudo-refs still resolve through `$GIT_DIR/<name>` when no branch of that name exists.

Consequences of branch-first resolution:
- A name that is a branch always means the branch.
- Every other input resolves exactly as at `61994fe`.
- The engine keeps passing bare names (Design §5), and all of its reads become shadow-proof:
  - `merge.go:606`, `:690`, `:696`;
  - `branch_validation.go:46`;
  - `evidence/summary.go:180`;
  - `handlers_artifact_entry.go:234`;
  - `multi_repo_branch.go:129`;
  - SMP's `artifacts_addtorun.go:120`.
- The probe costs one extra `git rev-parse` per call, and only for bare non-SHA names. That
  measured about 9 ms on macOS, comparable to the `git show` itself. Hot paths pay nothing:
  - projection passes SHAs;
  - `RunBranchDiffReader` passes `refs/*`;
  - services default to `HEAD`.

**Merge abort stays the in-band sentinel.** When `MergeOpts.Strategy == "abort"` or
`Source == "--abort"`, `Merge` runs exactly `git merge --abort`, with no checkout, no grammar check
and no other argv. This keeps the contract the engine uses:
- `merge.go:327-330`;
- `multi_repo_merge.go:219`.

SMP's operator keys on the same contract (`internal/customer/spinecore/gitoperator.go:387`).

**Errors.** Every refusal happens before any git process starts. It returns:

```go
&GitError{Kind: ErrKindPermanent, Op: <op>,
    Message: fmt.Sprintf("invalid git ref: %s %q: %v", what, value, cause),
    Err: ErrInvalidRef} // new exported sentinel: errors.Is(err, git.ErrInvalidRef)
```

- **Why `*GitError` with `Kind: Permanent`:**
  - The engine's push path treats a non-`*GitError` as transient (`merge.go:236-244`). A plain
    `error` would retry forever.
  - `classifyMergeFailure` and `recordGitConflictOnStep` already read `*GitError`.
- The substring `invalid git ref` is kept, because `TestDiffRejectsPoisonedRef` and
  `TestMergeBaseRejectsPoisonedRef` (`cli_test.go:439-495`) assert it.
- `%q` keeps control bytes out of logs.

> **Design-check note (2026-10-06), DC-4: validate every argument first, then resolve.**
>
> - **The conflict.** As written, `resolveRev` validates and probes one argument at a time, and
>   the probe fails closed. A call with a valid first argument and an invalid second one therefore
>   starts a git process (the probe for the first) before refusing the second. That contradicts
>   "every refusal happens before any git process starts" and AC 5's zero-invocation clause.
> - **It also breaks four existing subtests that AC 7 requires to pass unmodified.** Verified with
>   a fail-closed variant of the prototype (overlay `_work/task002-check/proto/overlay-failclosed.json`):
>   - `TestDiffRejectsPoisonedRef/flag-injection-to`;
>   - `TestDiffRejectsPoisonedRef/shell-meta-to`;
>   - `TestMergeBaseRejectsPoisonedRef/flag-injection-b`;
>   - `TestMergeBaseRejectsPoisonedRef/shell-meta-b`.
>
>   Their client is rooted at `/nonexistent/repo`, so the probe for `main` returns
>   `chdir … no such file or directory` instead of `invalid git ref`. The prototype passed them
>   only because it falls back to DWIM on probe errors.
> - **Change: two phases.**
>   - **Phase 1:** each method syntax-checks **all** of its ref, branch and remote arguments,
>     including `Merge`'s `Target`.
>   - **Phase 2:** only then does it look anything up or run git.
>
>   The amended prototype does this, and the four subtests pass unmodified.

**New exported API** (additive only):
- `ErrInvalidRef`;
- `IsFullObjectName(string) bool`;
- `QualifyBranch(name) (short, full string, err error)`;
- `ValidateBranchName(name) error`;
- `ValidateRemoteName(name) error`;
- `CheckGitVersion(ctx) error` and `const MinGitVersion = "2.30"` (§4).

TASK-005's `ResolveBranchHead` and `PushCommitCAS` use `QualifyBranch` and `ValidateRemoteName`
instead of `ValidateRef`.

### 2. Argv per method (`adapters/git/cli.go`, `branchwrite.go`)

`<n>` is the short branch name, `<rev'>` is the output of `resolveRev`, and `[eoo]` is
`--end-of-options`. Push operations keep the `-c credential.helper=…` prefix from `run`
(`cli.go:639-644`).

| Method (HEAD line) | `61994fe` argv | New argv |
|---|---|---|
| `Clone` (`:296`) | `clone -- <url> <path>` | `clone --no-tags -- <url> <path>` (owner question 1) |
| `Checkout` (`:334-337`) | `checkout <branch>` | `switch --no-guess [eoo] <n>` |
| `Merge` (`:365-403`) | `[checkout <Target>]`, then `merge [--ff-only \| --no-ff -m <msg>] <Source>` | target through `Checkout`, then `merge […] [eoo] <object-name \| refs/heads/<n>>`; abort becomes `merge --abort` |
| `CreateBranch` (`:406-409`) | `branch <name> <base>` | `branch [eoo] <n> <HEAD \| object-name \| refs/heads/<m>>` |
| `DeleteBranch` (`:412-415`) | `branch -D <name>` | `branch -D [eoo] <n>` |
| `Diff` (`:426-474`) | `diff --name-status <from> <to>` | `diff --name-status [eoo] <from'> <to'> --` |
| `Log` (`:477-496`) | `log --format=… [-nN] [<since>..HEAD] [-- <path>]` | `log --format=… [-nN] [eoo] [<since'>..HEAD] -- [<path>]` |
| `ReadFile` (`:499-505`) | `show <ref>:<path>` | `show [eoo] <ref'>:<path> --` |
| `ListFiles` (`:523-539`) | `ls-tree -r --name-only <ref>` | `ls-tree -r --name-only [eoo] <ref'>` |
| `RefSHA` (`:554-560`) | `rev-parse <ref>` | `rev-parse --verify [eoo] <ref'>` |
| `MergeBase` (`:571-583`) | `merge-base <a> <b>` | `merge-base [eoo] <a'> <b'>` |
| `Push` (`:608-611`) | `push <remote> <ref>` | `push [eoo] <remote> refs/heads/<n>:refs/heads/<n>` |
| `PushBranch` (`:614-617`) | `push -u <remote> <branch>` | `push -u [eoo] <remote> refs/heads/<n>:refs/heads/<n>` |
| `DeleteRemoteBranch` (`:620-623`) | `push <remote> --delete <branch>` | `push --delete [eoo] <remote> refs/heads/<n>` |
| `EnterBranch` (`branchwrite.go:75`) | `worktree add <dir> <branch>` | `worktree add [eoo] <dir> <n>`, then the post-condition `git -C <dir> symbolic-ref -q HEAD == refs/heads/<n>`. If it fails, remove the worktree and its parent and return an error |
| `CurrentBranch` (`branchwrite.go:141-149`) | `rev-parse --abbrev-ref HEAD` | `symbolic-ref -q HEAD`, then trim `refs/heads/`. Exit 1 (detached) returns `"HEAD"`, the same value as before |
| `Head`, `Commit`, `HasCommitWithTrailer`, `ConfigureCredentialHelper` | n/a | unchanged (no ref arguments) |
| `WriteAndStageFile` | n/a | unchanged here (`git add -- <path>` is a pathspec, not a ref; TASK-004 owns the body) |

> **Design-check note (2026-10-06): amended rows (DC-1, DC-2, DC-5).** `<sha>` below is the output
> of the exact lookup `show-ref --verify --hash refs/heads/<n>` (DC-2). Where one row of the table
> above conflicts with this one, this one replaces it.
>
> | Method | Amended argv |
> |---|---|
> | Rev-class methods (`Diff`, `Log`, `ReadFile`, `ListFiles`, `RefSHA`, `MergeBase`) | same shapes, but `<rev'>` is the exact `<sha>` when the input names an existing branch, either bare or as `refs/heads/<n>`. A missing `refs/heads/<n>` input gives `ErrKindNotFound` with no git call after the lookup |
> | `Merge` | `[switch --no-guess [eoo] <target>]`, then `merge […] [eoo] <object-name>`. A branch source is resolved exactly to `<sha>`; a missing branch gives `ErrKindNotFound` (not retryable) |
> | `CreateBranch` | `branch [eoo] <n> <HEAD \| object-name>`. A branch base is resolved exactly to `<sha>` |
> | `Push` | `show-ref --verify --hash refs/heads/<n>`, then `push [eoo] <remote> <sha>^{commit}:refs/heads/<n>`. A missing local branch gives a `Permanent` `*GitError`, which matches the classification of today's `src refspec … does not match any` |
> | `PushBranch` | as `Push`, with **no `-u`** (owner question 4) |
> | `DeleteRemoteBranch` | unchanged: `push --delete [eoo] <remote> refs/heads/<n>`. It has no source side; the destination is exposed only to remote-side shadows (DC-3) |
> | `EnterBranch` | `worktree add --detach [eoo] <dir> <sha>`, then `git -C <dir> switch --no-guess [eoo] <n>`, then the same post-condition |

Why each shape. Every behaviour below was verified on **git 2.39.5** (the customer-app image) and
**git 2.54.0**.

- **`switch --no-guess`, not `checkout`.**
  - Before git 2.44, `checkout` treats `--end-of-options` as a pathspec. On 2.39.5,
    `git checkout --end-of-options feat` printed `error: pathspec '--end-of-options' did not match`
    and did not switch. Git 2.44's release notes record the fix.
  - `git checkout <n> --` with a pre-probe still detaches onto a same-named tag if the branch
    disappears between the probe and the checkout. Tested: `git checkout onlytag --` detaches.
  - `git switch` refuses anything but a local branch (`fatal: a branch is expected, got tag 'v1'`).
    It never detaches, it carries staged changes the same way, and `--no-guess` stops it from
    creating a branch out of a remote-tracking ref.
- **`rev-parse --verify`.** Without `--verify`, `rev-parse --end-of-options X` prints the literal
  line `--end-of-options` before the SHA, on both versions. `--verify` changes the missing-ref
  stderr to `fatal: Needed a single revision`. `classifyGitError` (`errors.go:49`) adds that string
  to its `ErrKindNotFound` patterns, so `RefSHA` on an unknown ref keeps the `NotFound` kind it has
  today (`unknown revision`).
- **Trailing `--` on `diff`, `show` and `log`.**
  - Without it, a "rev" that is really a path is silently reinterpreted. On both versions,
    `git diff --name-status <sha> f` returned rc 0 with an empty diff, because `f` was taken as a
    pathspec.
  - With `[eoo] … --` the same call fails with `fatal: bad revision 'f'`, which is classified
    `NotFound`.
  - `--end-of-options` stops option parsing without turning the revs into pathspecs. That is the
    concern INIT-022 TASK-009 raised against a bare `--`.
- **`push -u … refs/heads/<n>:refs/heads/<n>`.** It still writes `branch.<n>.remote=origin` and
  `branch.<n>.merge=refs/heads/<n>` (verified). With a local `refs/tags/main`, the qualified push
  succeeds where `git push origin main` fails with `matches more than one`.

  > **Design-check note (2026-10-06), DC-1: the qualified source is still ambiguous.**
  >
  > - **Why.** git matches a push source against local refs using the DWIM rules in reverse
  >   (`count_refspec_match`).
  > - **Effect.** A local `refs/tags/refs/heads/main`, `refs/refs/heads/main` or
  >   `refs/heads/refs/heads/main` makes `push origin refs/heads/main:refs/heads/main` fail with
  >   `src refspec refs/heads/main matches more than one`. That is classified `Permanent`
  >   (`push rejected`), so it is the same workspace-wide merge denial of service as F10 impact 2,
  >   with a different shadow name.
  >   - Verified on git 2.39.5 and 2.54, and through the prototype (`TestDC_LocalDoubleQualifiedShadowBreaksPush`).
  >   - Standalone serves the engine's own repository, so any pusher can plant one of these refs.
  >     Branch protection admits all three (DC-3).
  > - **A raw 40-hex source is not safe either.** It is also matched against refs first: with a
  >   ref named `refs/tags/<sha>`, `push origin <sha>:refs/heads/main` sent the ref's object
  >   (verified; refused only because it was not a fast-forward).
  > - **What is safe.** `<sha>^{commit}` can never match a ref name, because `^` is illegal in
  >   refnames. It pushed correctly on both versions with every local shadow present.
  > - **Change.**
  >   - `Push` and `PushBranch` push `<sha>^{commit}:refs/heads/<n>`, where `<sha>` comes from the
  >     exact lookup (DC-2).
  >   - git writes upstream tracking only for a ref source. `PushBranch` therefore stops setting
  >     `branch.<n>.remote` and `branch.<n>.merge`.
  >   - Nothing in Spine or SMP reads tracking config: grep finds no `@{u}`, `@{upstream}`,
  >     `--set-upstream`, `branch.*.merge` or `pull`. The full Spine suite and the SMP gate pass
  >     without it.
  >   - Owner question 4 covers this.
  > - **Remote side.** A remote-side shadow is a different failure (DC-3) and is not fixable here.
- **`worktree add` with a bare name.** Prefers the local branch when one exists (verified; HEAD
  becomes a symref to `refs/heads/<n>`). If there is no branch it detaches onto the tag, which the
  post-condition catches.

  > **Design-check note (2026-10-06), DC-5: a non-commit shadow breaks `worktree add`.**
  >
  > - **Why.** `worktree add <dir> <n>` runs `lookup_commit_reference_by_name(<n>)` with DWIM before
  >   it attaches the existing branch.
  > - **Effect.** A `refs/<n>` or `refs/tags/<n>` pointing at a **blob** makes it die with
  >   `fatal: invalid reference: <n>`, although `refs/heads/<n>` exists. receive-pack accepts any
  >   object type outside `refs/heads/`.
  >   - Verified on git 2.39.5 and 2.54. It is also broken at `61994fe`.
  >   - The post-condition does not help, so this is a per-branch denial of service on
  >     `write_context` writes.
  >   - `git switch` is immune, because it replaces the DWIM result with the branch's own object.
  > - **Change.** `EnterBranch` runs:
  >   1. `worktree add --detach [eoo] <dir> <sha>`, with the exact SHA (DC-2);
  >   2. `git -C <dir> switch --no-guess [eoo] <n>`;
  >   3. the post-condition.
  >
  >   Verified on both versions:
  >   - the worktree ends on `refs/heads/<n>` with a blob shadow present;
  >   - `switch` keeps git's "already checked out / already used by worktree" guard.
  >
  >   On any failure, remove the worktree and its parent, as before.
- **`CurrentBranch`.** When a same-named tag exists, `rev-parse --abbrev-ref HEAD` returns
  `heads/<n>` (verified on 2.39.5 and 2.54). Under the new grammar that would become
  `refs/heads/heads/<n>`, and the artifact auto-push (`core/artifact/service.go:591-597`) would
  fail. `symbolic-ref` returns the unabbreviated name.

> **Design-check note (2026-10-06), DC-8: minor behaviour deltas to record, with no design
> change.**
>
> - **`git switch` is stricter than `checkout`.** It refuses to run while a merge, cherry-pick,
>   revert, rebase or bisect is in progress, and `checkout` does not always refuse. This fails
>   closed. The engine aborts a failed merge before its next checkout (`merge.go:180`, `:327-330`).
> - **`CurrentBranch` on an unborn `HEAD`.** It now returns the branch name, where `rev-parse
>   --abbrev-ref` returned an error.
> - **Code-repo clones (dormant).** A gitpool code-repo clone may have no local branch for a
>   `default_branch` that is not the remote's `HEAD`. At `61994fe`, `git checkout <d>` and
>   `git branch <run> <d>` reached `origin/<d>` through checkout's `--guess` or through DWIM. Under
>   this task, `switch --no-guess` and the exact Commit-class base refuse. The path stays dormant
>   (P3-36 and P3-37), and `KNOWN-LIMITATIONS.md` records it.

### 3. Branch-name grammar converges on TASK-001 (`adapters/git/refname/refname.go`)

TASK-001 (wave 1) adds `refname.ValidateQualified(ref)`, git's `check_refname_format` for a fully
qualified ref. Its §1 hands the convergence to this task. `refname.Validate(name)` (`:31-63`)
becomes:

> `name` passes **all existing checks** (unchanged, including the Unicode control/space refusal
> that is stricter than git), **and** `ValidateQualified("refs/heads/" + name)` passes, **and**
> `name` is neither `HEAD` nor `@`, **and** `name` does not start with `refs/`.

- **Newly refused.** These are the names git itself refuses to create as a branch:
  - a component starting with `.` (`a/.b`);
  - a component ending in `.lock` (`a.lock/b`);
  - `//`, a leading `/`, a trailing `.`;
  - `HEAD`, `@`.
- **Unaffected callers.** `EnterBranch` callers (`core/artifact/service.go:523`,
  `core/workflow/service.go:64`) keep their typed `ErrInvalidParams`.
- **Generated names still pass.** `core/engine/branchname.go` only emits `[a-z0-9-/]`.
- **`QualifyBranch` is the only producer of `refs/heads/<n>`.** It strips an optional
  `refs/heads/` prefix and then applies `refname.Validate`.

**Ordering.** TASK-001 is wave 1 and lands first in practice. If this task is implemented first, it
lands `ValidateQualified` exactly as TASK-001 §1 specifies (same file and tests), and TASK-001
reuses it. There must never be two refname grammars.

`git.ValidateRef` (`credentials.go:61-77`) keeps its exact accept set. It is the **rev-syntax** gate
used by:
- `service/internal/gateway/handlers_artifacts.go:174` and `handlers_workflows.go:199` (user
  `?ref=`);
- SMP's `internal/customer/spinecore/workflowreader.go:178`.

Only its doc comment changes: it validates rev syntax and is not a refname check.

### 4. Git version floor

- **Floor: 2.30.** The rules come from the release notes:
  - `rev-parse --end-of-options` arrived in git 2.30.0 ("git rev-parse learned the
    --end-of-options…");
  - parse-options and `setup_revisions` support arrived in 2.24;
  - `git switch` arrived in 2.23.
- **Shipped images, verified 2026-10-06 with `git --version` inside each image:**

  | Image | Git |
  |---|---|
  | standalone (`Dockerfile:18-26`: `debian:bookworm-slim` + apt `git`, the same Debian package as the customer-app image) | 2.39.5 |
  | SMP `customer-app` (`Dockerfile.customer-app:37`) | 2.39.5 |
  | `runner` and `ai-runner` (`docker:29-cli` + apk `git`) | 2.54.0 |
  | `smp/runner-validate-base` | 2.39.5 |
  | CI (`ubuntu-latest`) | newer than 2.30 |

  The runners never construct a `CLIClient`. SMP builds one only in customer-app packages
  (Design §6).
- **New `git.CheckGitVersion(ctx)`.**
  - It parses `git version X.Y[.Z][ suffix]` and returns an error below 2.30. Accepted examples:
    `2.39.5`, `2.54.0 (Apple Git-157)`, `2.45.1.windows.1`.
  - `spine serve` calls it immediately before `git.LoadPushAuthFromEnv()`
    (`service/cmd/spine/serve_cmd.go:114`) and refuses to start on error.
  - An old git would otherwise fail closed on the first `RefSHA` with a confusing message.

### 5. Engine contract and P3-36 (`core/engine`, `adapters/repository`)

- **No engine call site changes its arguments.** The engine keeps passing bare names
  (`run.BranchName`, `"main"`, `"HEAD"`) and the abort sentinel. SMP's `RealGitOperator`, which is
  the engine's `GitOperator` in embedded mode, prefixes `refs/heads/` itself:
  - `internal/customer/spinecore/gitplumbing.go:198`, `:253`;
  - `gitoperator.go:259`.

  Passing `refs/heads/<n>` from the engine would produce `refs/heads/refs/heads/<n>` there.
- **P3-36 guard (engine).** Insert this check immediately after each existing empty check, before
  any client call:
  - `attemptCodeRepositoryMerge` (`multi_repo_merge.go:176-179`);
  - `resolveCodeRepoForBranch` (`multi_repo_branch.go:183-186`).

  ```go
  if err := git.ValidateBranchName(repo.DefaultBranch); err != nil {
      return …, domain.NewError(domain.ErrPrecondition,
          fmt.Sprintf("repository %q default_branch is not a valid branch name: %v", repoID, err))
  }
  ```

  The handling mirrors the empty-branch path:
  - no git argv runs;
  - no outcome row is written (as the `:133-138` comment requires);
  - the run stays in `committing` for retry once the operator fixes the binding.

  `multi_repo_branch.go` gains the `adapters/git` import, which is allowlisted in
  `dev/lint-boundaries.sh:71-77`. Even without this guard, the adapter would refuse `--force` as a
  Branch or Commit argument. The guard turns that refusal into a clear precondition with zero git
  calls.

  > **Design-check note (2026-10-06): the order of events in `StartRun`.** `createRunBranches`
  > (`multi_repo_branch.go:52-120`) runs the primary repository's `CreateBranch` and `PushBranch`
  > before the code-repo loop reaches `resolveCodeRepoForBranch`. A bad `default_branch` therefore
  > leaves a primary run branch that is created, pushed and then rolled back
  > (`rollbackRunBranches`), with no run row. The guard must sit before `o.repoClients.Client` so
  > that no code-repo clone is attempted. AC 11 is tightened to match. Validating every code
  > repository's `default_branch` before the primary `CreateBranch` would be cleaner, but it is
  > optional.
- **P3-36 guard (input).** `Manager.Register` (`manager.go:132`) and `Manager.Update` (`:210`) also
  call `git.ValidateBranchName`, so the HTTP API returns `400 invalid_params`.
- **Catalog parsing is not changed.** `ParseCatalog` (`catalog.go:329`) is not tightened: a parse
  failure takes down registry lookups for every repository, while the engine guard fails only the
  affected repository's merge (Decision D10).

### 6. How SMP is affected, and what the dependent SMP task must change

**Compile surface: no change.**
- The `git.GitClient` method set (`client.go:11-32`) is unchanged, so SMP's assertion
  `var _ git.GitClient = (*divergenceGitClient)(nil)` (`divergencegitclient_test.go:432`) still
  compiles.
- `MergeOpts`, `GitError` and `FileDiff` are unchanged.
- The only new exports are the additive names in §1.

**SMP's direct `CLIClient` call sites** (SMP `80995559`):

| SMP site | What it passes | Effect of this task |
|---|---|---|
| `projectionwriter/worker.go:312` and `workspace/provision_steps_runtime.go:320` → `projection.Service` | `Head`; `Diff(sha, sha)`; `ListFiles(sha)`; `ReadFile(sha, …)` | argv gains terminators; no probe; identical results |
| `spinecore/runbranchdiffreader.go:176-180`, `:255`, `:259`, `:360` | `refs/remotes/origin/<d>`, `refs/heads/<r>`, merge-base SHA | pass-through; identical |
| `spinecore/artifactservice.go:68` (`ReadRaw`) | `"HEAD"` | identical |
| `artifactservice.go:79-81` (`Lister`) | `artifacts_addtorun.go:120` passes the bare `run.BranchName`; `artifacts_createentry.go:314` passes `"HEAD"` | AddToRun now resolves `refs/heads/<run>` first. This is the one SMP-visible behaviour change, a fix; same answer when nothing is shadowed |
| `artifactservice.go:306`, `:447` → `core/artifact` `autoPush` | `Push(origin, CurrentBranch())` | unabbreviated name; qualified refspec; same remote effect |
| `spinecore/workflowwriter.go:155`, `:335` | `Push(origin, defaultBranch)` | `push [eoo] origin refs/heads/<d>:refs/heads/<d>`; same effect |
| `spinecore/workflowreader.go:178` | `git.ValidateRef(ref)` | accept set unchanged |
| `spinecore/artifactreader.go:124`, `:216` | SMP's own `RealGitReader` (`GitFileReader`), **not** `CLIClient` | unaffected; its DWIM is P3-7, SMP TASK-005 |

> **Design-check note (2026-10-06), DC-7 and DC-8: one missing row, and a cost.**
>
> - **Missing row.** `spineembedded/artifacts_addtorun.go:111` → `artifact_entry_helpers.go:37`
>   calls `writer.Service.List(ctx, run.BranchName)`. That is one `ListFiles` call plus one
>   `ReadFile` call per `.md` file, through the scope `CLIClient`. AddToRun now resolves the branch
>   first. That is a fix, and the answer is the same when nothing is shadowed.
> - **Cost.** Branch-first resolution in each `ReadFile` adds one lookup process per artifact on
>   every AddToRun, and on the standalone `/artifacts/add` path
>   (`handlers_artifact_entry.go:271`). That is O(N) new processes per request.
> - **Change (in scope).** `core/artifact.Service.List` resolves `ref` once, with `RefSHA` unless
>   it is `HEAD` or a full object name, and lists and reads by that SHA. This removes the N lookups
>   and also gives a consistent snapshot. See AC 18.
> - **Re-verified with the amended overlay** (`_work/task002-check/overlay-amended.json`, DC-1 to
>   DC-6):
>   - SMP `go build -mod=readonly ./internal/... ./cmd/...` passes, and so does `go vet`;
>   - `go test` passes, default and `-tags integration`, for `spinecore`, `projectionwriter`,
>     `workspace` (default only), `spineembedded` and `gitclone`;
>   - no SMP edits were needed.

**Verified against a prototype of this design** (an overlay, with no edits to either repository):
- SMP `go build -mod=readonly ./internal/... ./cmd/...` passes, and so does `go vet`.
- `go test`, both default and `-tags integration`, passes for:
  - `internal/customer/spinecore`;
  - `projectionwriter`;
  - `workspace`;
  - `spineembedded`;
  - `gitclone`.
- No SMP test needed changing.

**Ordering for SMP.** This task changes no output format, unlike TASK-003's `-z`. Any SMP
`.spine-revision` bump may therefore include it.

**What SMP TASK-008 ("Adopt Spine's ref grammar…") must do for this task:**
1. Bump `.spine-revision` to a Spine commit containing this task (together with TASK-003 and
   TASK-004, per the plan). This task needs no SMP code change to compile.
2. Call `spinegit.CheckGitVersion(ctx)` in customer-app boot (`cmd/customer-app/boot.go`,
   `bootCore`) and fail the boot on error. The image ships 2.39.5.
3. Add SMP-side regression tests at its consumer sites:
   - AddToRun's `NextID` with a `refs/tags/<run-branch>` in the pooled clone lists the branch. The
     persistent clone is made by a plain `git clone`, so tags can exist there.
   - The workflow writer push succeeds with a local `refs/tags/<default>` present.
   - `RunBranchDiffReader` is unaffected by a shadow tag. This is a guard test.
4. SMP's own plumbing (`gitclone`, `RealGitOperator`, `RealGitReader`) is **not** changed by
   TASK-008. That is SMP TASK-005 (F10.smp-refs, P3-7), which may reuse `git.QualifyBranch` and
   `git.ValidateBranchName` once the bump has landed.

### 7. Data, format, migration, compatibility

- **No change to:** runtime or projection schema, migrations, events, configuration or environment
  variables.
- **Behaviour deltas for valid inputs:**
  1. A bare rev name that is an existing branch resolves to the branch even when a same-named
     tag or `refs/<n>` exists. Before, git's DWIM picked the shadow.
  2. `Clone` no longer imports tags, and persists `remote.origin.tagOpt=--no-tags` (owner
     question 1).
  3. `Push`, `PushBranch` and `DeleteRemoteBranch` accept branches only. Every in-repo caller
     passes branches (owner question 2).
  4. Empty rev arguments are refused. At `61994fe`, `ReadFile("", p)` ran `git show :p` and read the
     **index**. No caller passes `""`: `core/artifact/service.go:283-285` and
     `core/workflow/service.go:205-207` default to `HEAD`, and `WorkflowVersion` is always a pinned
     SHA (`core/workflow/binding.go:81-92`).
  5. Branch names git refuses to create are now refused earlier, by `refname.Validate`.
  6. Git 2.30 or newer is required.

  > **Design-check note (2026-10-06): further deltas from DC-1 to DC-8.**
  >
  > 7. Resolved branch arguments reach git as object names (DC-2). A path-not-found message now
  >    reads `… does not exist in '<sha>'` instead of naming the branch. The `Kind` is unchanged
  >    (`NotFound`).
  > 8. A Rev input `refs/heads/<n>` for a missing branch returns `ErrKindNotFound`. It no longer
  >    falls through git's DWIM to a doubly-qualified ref.
  > 9. `PushBranch` no longer configures upstream tracking (DC-1, owner question 4). `Push` and
  >    `PushBranch` push the object resolved at call time.
  > 10. `EnterBranch` creates the worktree detached and then switches (DC-5). The worktree's final
  >     state is unchanged.
  > 11. `core/artifact.Service.List` reads one snapshot (DC-7).
  > 12. **Residual: remote-side doubly-qualified refs.** They still break pushes to the branch they
  >     shadow (DC-3). Only admission closes this. **Residual: missing non-branch refs.** A
  >     `refs/tags/*` or `refs/remotes/*` input naming a missing ref can still be DWIM-substituted
  >     by `refs/tags/<full>` and similar names, which admission also refuses.
- **Error text.** Validation failures now read
  `git <op>: invalid git ref: <what> "<v>": <cause>: invalid git ref`. Checkout of a missing branch
  now carries `switch`'s stderr (`invalid reference`). The `Kind` is still `Permanent`, as before.

## Decisions

- **D1 — Branch-first rev resolution.** The plan summary said "bare names are not re-qualified".
  This task refines that. Bare names are qualified only when `refs/heads/<n>` exists; everything
  else resolves as before. Alternatives:
  - **(a) Leave DWIM.** Rejected. The status cascade still lands shadow content on `main` with a
    qualified merge source (verified). TASK-005's SHA capture through `RefSHA` would capture the
    shadow, and the evidence, branch-validation and `NextID` reads would stay exposed.
  - **(b) Always prefix `refs/heads/`.** Rejected. It breaks `HEAD`, SHAs, tags, abbreviated SHAs
    and `refs/remotes/*`. Callers pass all of these, including SMP.
  - **(c) Make the engine pass `refs/heads/<n>`.** Rejected, because SMP's operator already
    prefixes (§5).
  - **(d) Resolve to a SHA.** Rejected. It adds no safety for a single call and makes errors less
    readable.

  > **Design-check note (2026-10-06): (d) is reversed (DC-2).** It does add safety. git resolves a
  > qualified `refs/heads/<n>` by DWIM when the branch is absent (verified), so passing the name
  > re-opens a window between probe and use, and the designed `rev-parse` probe itself accepts a
  > doubly-qualified shadow. The exact lookup yields the SHA in the same process, and readable
  > errors are kept by naming the input in `GitError.Message`. D1's branch-first *policy* is
  > unchanged: a bare name that is a branch means the branch, and anything else resolves as
  > before.
- **D2 — `git switch --no-guess` for `Checkout`.** See §2. `checkout --end-of-options` is a pathspec
  bug before 2.44; `checkout <n> --` can detach in a race; `checkout refs/heads/<n>` always
  detaches.
- **D3 — `--end-of-options` plus a trailing `--`, not validation alone.** Validation alone is the
  P3-32 state, and one missed call site re-opens it. A bare `--` turns revs into pathspecs
  (INIT-022 TASK-009). `--end-of-options` is the terminator designed for revisions.
- **D4 — Keep the in-band abort sentinel rather than add `GitClient.AbortMerge`.** A new interface
  method breaks SMP's `divergenceGitClient` assertion and its operator's `Source == "--abort"`
  branch.
- **D5 — Refusals are `*GitError{Permanent}` wrapping `ErrInvalidRef`, not plain errors.** A plain
  error on `Push` is retried forever (`merge.go:236-244`).
- **D6 — `ValidateRef` keeps its accept set.** Tightening it, for example to reject `@{`, would turn
  user `?ref=main@{1}` reads into 400s in the standalone gateway and in SMP's workflow reader. It
  stays the rev-syntax gate; the branch grammar is separate (§3).
- **D7 — Explicit `src:dst` push refspec rather than `refs/heads/<n>` alone.** With a fully
  qualified source, git derives the same destination. The explicit form makes the destination
  reviewable in argv and in tests.

  > **Design-check note (2026-10-06): D7 is amended (DC-1).** The source is `<sha>^{commit}`, not
  > `refs/heads/<n>`, and not a raw SHA. Both of those are matched against local refs and can be
  > made ambiguous or shadowed. The explicit `:refs/heads/<n>` destination stays.
- **D12 (design check) — Admission, not argv, owns remote-side shadows.** A doubly-qualified ref on
  the remote breaks every client's push to the branch it shadows, whatever the argv (DC-3).
  - This task makes **local** shadows inert.
  - Spine TASK-016 (standalone) and SMP E4-TASK-005 §7 (SMP) must refuse such names, for every
    principal: any created or updated ref with a `refs` path component other than the first, or
    any `HEAD` component.
- **D13 (design check) — `EnterBranch` attaches through `switch`, not `worktree add <n>`.**
  `worktree add <n>` looks up `<n>` by DWIM before attaching. `switch` uses the branch's own object
  and keeps the checked-out guard (DC-5).
- **D8 — Empty rev is refused rather than mapped to `HEAD`.** Mapping it would hide caller bugs.
  At `61994fe` an empty rev read the index, which no caller intends.
- **D9 — Remote names are matched by pattern rather than `ValidateRef`.** `ValidateRef` accepts
  URLs such as `https://host/x` and `ext::sh`. Every caller passes `origin`.
- **D10 — The `default_branch` guard sits in the engine and `Manager`, not in `ParseCatalog`.** A
  catalog parse error fails registry lookups for every repository, including the primary. The
  engine guard fails only the affected repository's merge, with a precondition the operator can
  read.
- **D11 — A startup version check rather than per-call feature detection.** Every git in a shipped
  image is above the floor. Detection would add a fallback path that runs without terminators.

## Owner questions

1. **Clone without tags.**
   - `Clone` gains `--no-tags`. The F10 fix does not need it: qualification already makes tags
     inert for branch operations. It removes upstream-controlled tag names from the local namespace
     of newly provisioned standalone workspaces (`service/internal/workspace/provision.go:221`) and
     code-repo clones (`adapters/gitpool/pool.go:629`).
   - Cost: `?ref=<upstream tag>` reads stop working on workspaces provisioned after the change.
     Existing clones are unaffected, and tags pushed later still work.
   - **Default: yes, clone with `--no-tags`, as planned.** The alternative is to keep importing tags
     and rely on qualification alone.
2. **Narrow `Push` to branches (library contract).**
   - `Push(remote, ref)` previously accepted any refspec. Now it accepts branches only, which every
     in-repo and SMP caller passes.
   - An external embedder pushing tags would break.
   - **Default: narrow it, and record it in `CHANGELOG.md` under "Changed (embedders)".** The
     alternative is to keep a separate `PushRefspec` for arbitrary refs, which reopens the argv
     surface.
3. **Version floor policy.**
   - **Default: refuse to start below git 2.30.** That is Debian 11 or Ubuntu 22.04 and newer.
     Ubuntu 20.04 (2.25) would be refused.
   - The alternative is a warning only. Every `RefSHA` would then fail at runtime.
4. **(Design check) `PushBranch` upstream tracking.**
   - With an object-name source (DC-1), git no longer writes `branch.<n>.remote` and
     `branch.<n>.merge`. Nothing in Spine or SMP reads them (grep-verified).
   - **Default: drop it, and record it under `CHANGELOG.md` "Changed (embedders)".**
   - The alternative is to write both keys explicitly with `git config` after a successful push.
     That costs two more processes per run-branch creation, for no consumer.
5. **(Design check) Remote-side doubly-qualified refs: rule placement and existing refs** (DC-3,
   D12).
   - **Default for the rule:** Spine TASK-016 adds a class, or a rule 0b, that refuses any created
     or updated ref with a `refs` path component other than the first, or with a `HEAD` component.
     It applies to every principal, operators included, which mirrors SMP E4-TASK-005 §7 and
     TASK-016's DC-2 `invalid` rule. Deletions stay allowed so that cleanup works.
   - **Default for existing refs:** no automatic deletion. The upgrade note in both repositories
     lists them and says they **must** be deleted. The listing command is
     `git -C <repo> for-each-ref refs/refs refs/heads/refs refs/tags/refs refs/remotes/refs`.
   - **Correction for SMP.** SMP E4-TASK-005's owner question 1 assumes these names "matter only for
     a branch that is missing". They actually break pushes to the **existing** branch they shadow,
     so that rationale needs correcting.
   - **Alternative:** a one-off migration that deletes them. That mutates customer repositories.

## Acceptance Criteria

Each criterion names the mutation that must make it fail. The PoC-derived tests fail at `61994fe`:
re-run 2026-10-06, they reproduce the exploit there and invert it under the prototype.

All new git-backed tests are **hermetic**. They set `GIT_AUTHOR_*` and `GIT_COMMITTER_*`,
`GIT_CONFIG_NOSYSTEM=1`, and a temporary `HOME` through `t.Setenv`. The audit PoC
`engine_refshadow_poc_test.go` relied on host git config, and its tag-shadow case failed for an
unrelated "committer identity" reason in a clean container.

1. **F10 regression: run-branch shadow** (from `poc/gap0-ref-namespace-shadow` `:101` and
   `poc/verify-r2-0` phase 2 scenario A; committed as `core/engine/merge_refshadow_test.go`).
   - **Setup:**
     - a real `*git.CLIClient` engine repository with a bare origin;
     - a reviewed `spine/run/…` branch;
     - an attacker commit pushed as `refs/tags/<run-branch>`, plus a second case using
       `refs/<run-branch>`.
   - **`MergeRunBranch` must:**
     - complete the run;
     - leave the reviewed content at `origin refs/heads/main:f.txt`;
     - make the reviewed tip an ancestor of origin `main`;
     - **not** make the attacker commit an ancestor;
     - give the merge commit a second parent equal to `refs/heads/<run>`.
   - **Proof:** this fails at `61994fe`.
   - **Mutation:** reverting the merge source to the bare `opts.Source` fails it.
   - *Design-check addition (DC-2):* a third case deletes `refs/heads/<run>` and plants
     `refs/tags/refs/heads/<run>`.
     - `CLIClient.Merge(Source: <run>)` returns a non-retryable `*GitError` (`ErrKindNotFound`) and
       starts no `git merge`.
     - Through `MergeRunBranch`, the attacker commit is not an ancestor of origin `main`.
     - **Mutation:** passing `refs/heads/<n>` instead of the looked-up object name, or using
       `rev-parse` as the lookup, fails it. The designer's prototype fails it
       (`TestDC_MissingBranchQualifiedNameFollowsShadow`).
2. **F10 regression: `refs/tags/main` denial of service** (from `:133` and r2 scenario B).
   - With `refs/tags/main` present in the engine repository:
     - two independent runs both complete, and origin `main` advances twice;
     - `CLIClient.Push(ctx, "origin", "main")` returns nil.
   - **Mutation:** a bare push ref fails it with `matches more than one`.
   - *Design-check addition (DC-1):* the same assertions are repeated with each of these present
     **locally** in turn:
     - `refs/tags/refs/heads/main`;
     - `refs/refs/heads/main`;
     - `refs/heads/refs/heads/main`;
     - a ref `refs/tags/<local main SHA>` pointing at another commit.

     Expected: `Push` returns nil and origin `main` equals the local `main`.
   - **Mutation:** the original §2 refspec `refs/heads/main:refs/heads/main` fails the first three
     with `src refspec … matches more than one`. A raw `<sha>:refs/heads/main` source fails the
     fourth. Both reproduced on git 2.39.5 and 2.54.
3. **F10 regression: status cascade.**
   - **Setup:**
     - a run with `CommitMeta["status"]="Completed"`;
     - a shadow tag whose `tasks/t.md` body differs.
   - **Expected:** `main:tasks/t.md` carries the reviewed body with `status: Completed`.
   - **Mutation:** removing the branch-first probe from `resolveRev` fails it.
4. **F10 regression: reads** (from `:161`, inverted; in `adapters/git`).
   - **Setup:** `refs/tags/<b>` and `refs/<b>` pointing at another commit.
   - **Branch wins:** `ReadFile`, `RefSHA`, `MergeBase`, `Diff` and `ListFiles` called with the
     bare `<b>` return the branch's data.
   - **Unchanged resolution:**
     - a tag-only name;
     - an 8-hex abbreviated SHA;
     - `refs/tags/<b>`;
     - `HEAD`;
     - a full SHA;
     - `refs/remotes/origin/<b>`.
   - **Mutations:**
     - qualifying full SHAs (`refs/heads/<sha>`) fails the full-SHA case;
     - dropping the probe fails the `<b>` cases.
   - *Design-check addition (DC-2, DC-6):*
     - **Missing branch.** With `refs/heads/<b>` **absent**, plant each of `refs/refs/heads/<b>`,
       `refs/tags/refs/heads/<b>` and `refs/heads/refs/heads/<b>` in turn.
       - `ReadFile`, `RefSHA`, `ListFiles`, `Diff` and `MergeBase` called with
         `refs/heads/<b>` return `ErrKindNotFound`.
       - Called with the bare `<b>`, they never return the shadow's data.
     - **Pseudo-ref-like branch.** A branch named `HOTFIXHEAD` with a same-named tag resolves to
       the branch.
     - **Mutations:** a `rev-parse --verify --quiet refs/heads/<n>` lookup fails the missing-branch
       cases. Restoring the `^[A-Z_]*HEAD$` pass-through fails `HOTFIXHEAD`.
5. **Grammar table** (`adapters/git/refgrammar_test.go`).
   - **Method coverage:** every method and parameter in §1 is exercised.
   - **Input classes:**
     - bare branch;
     - `refs/heads/x`;
     - 40-hex and 64-hex;
     - `HEAD`;
     - abbreviated SHA;
     - `refs/tags/x`;
     - `refs/remotes/origin/x`;
     - `""`;
     - `-x`, `--force`;
     - `a~1`, `a^`, `a:b`, `a..b`, `a@{1}`;
     - NUL, LF, space;
     - `refs/heads/../x`;
     - `HEAD` used as a branch.
   - **Expected:** acceptance matches the table exactly. Each refusal satisfies
     `errors.Is(err, git.ErrInvalidRef)`, has `Kind == ErrKindPermanent`, contains
     `invalid git ref`, and starts **no** git process (the shim in AC 6 records zero invocations).
   - **Mutation:** removing any single class check fails a row.
   - *Design-check addition (DC-4):* for every method with two or more ref-like arguments
     (`Diff`, `MergeBase`, `Merge`, `CreateBranch`, `Push`, `PushBranch`, `DeleteRemoteBranch`),
     a call with a **valid first argument and an invalid second one** is refused with
     `ErrInvalidRef`, and the shim records zero invocations. **Mutation:** resolving argument 1
     before validating argument 2 fails it.
6. **Argv shape (P3-32).**
   - **Setup:** a recording `git` shim on `PATH` (`t.Setenv("PATH", shimDir)`). It logs argv and
     answers `rev-parse --verify --quiet` with exit 0 or 1.
   - **Expected:**
     - each method's argv equals the §2 table, including `--end-of-options` before the first ref
       and the trailing `--` on `diff`, `show` and `log`;
     - `Merge{Strategy:"abort"}` and `Merge{Source:"--abort"}` each produce exactly
       `merge --abort`.
   - **Mutation:** deleting any `--end-of-options` or trailing `--`, or reordering an argument,
     fails the test.
   - *Design-check amendment (DC-1, DC-2, DC-5):* the expected argv is the amended table in §2.
     - Every branch lookup is `show-ref --verify --hash refs/heads/<n>`.
     - No `rev-parse` argument anywhere in `CLIClient` or `EnterBranch` starts with
       `refs/heads/`.
     - `Push` and `PushBranch` end in `<sha>^{commit}:refs/heads/<n>`, and `PushBranch` has no
       `-u`.
     - `Merge` and `CreateBranch` pass the looked-up object name.
     - `EnterBranch` runs `worktree add --detach … <sha>`, then `switch --no-guess
       --end-of-options <n>`.

     The shim answers `show-ref --verify` with exit 0 plus a SHA, or with exit 128 plus
     `fatal: '<ref>' - not a valid ref`.
7. **Argv injection.**
   - **Refused before exec:**
     - `--upload-pack=x`, `-f`, `--force`, `--output=/tmp/x` as a branch, commit or rev argument;
     - `--receive-pack=touch /tmp/x`, `-v`, `https://evil/x`, `ext::sh` as a remote.
   - `TestDiffRejectsPoisonedRef` and `TestMergeBaseRejectsPoisonedRef` pass **unmodified**.
   - *Design-check note (DC-4):* this holds only with two-phase validation. With the fail-closed
     probe run per argument, their `*-to` and `*-b` subtests fail (verified).
8. **Checkout never detaches.**
   - `Checkout` of a tag-only name returns an error, and `symbolic-ref HEAD` is unchanged.
   - `Checkout` of a branch that has a same-named tag puts HEAD on `refs/heads/<n>` at the branch
     SHA.
   - **Mutation:** replacing `switch --no-guess` with `checkout <n>` fails the tag-only case.
9. **`EnterBranch` and `CurrentBranch`.**
   - **`EnterBranch`:**
     - on a tag-only name it returns an error and leaves no extra worktree (`git worktree list`
       shows one entry) and no temporary parent;
     - with a branch plus a shadow tag, the worktree is on `refs/heads/<n>`.
   - **`CurrentBranch`:**
     - with a same-named tag it returns `<n>`, not `heads/<n>`;
     - detached, it returns `"HEAD"`.
   - **Artifact auto-push:** a `core/artifact` `Create` with `write_context` on a branch that has a
     shadow tag puts the commit on the remote's `refs/heads/<n>`.
   - **Mutations:** removing the post-condition fails the tag-only case; reverting to
     `--abbrev-ref` fails the shadow case.
   - *Design-check addition (DC-5):*
     - **Blob shadows.** With `refs/heads/<n>` present and, in turn, `refs/<n>` and
       `refs/tags/<n>` pointing at a **blob**, `EnterBranch(<n>)` succeeds and the worktree is on
       `refs/heads/<n>` at the branch SHA.
     - **Branch already in use.** With `<n>` already checked out in another worktree,
       `EnterBranch` returns an error and leaves no extra worktree.
     - **Mutation:** `worktree add <dir> <n>` fails the blob cases with `invalid reference`. That
       is also the result at `61994fe`.
10. **Clone and error kinds.**
    - Cloning a source with tags yields no `refs/tags/*`, and `remote.origin.tagOpt` is
      `--no-tags`. Subject to owner question 1. Existing `TestClone` passes.
    - `RefSHA` and `ReadFile` on an unknown ref return `*GitError{Kind: ErrKindNotFound}`, as at
      `61994fe`.
    - An empty rev returns `ErrInvalidRef` and runs no `git show :path`.
    - **Mutations:** dropping `--no-tags` or the `Needed a single revision` pattern fails the test.
    - *Design-check addition (DC-2):*
      - A missing branch found by the exact lookup is reported as `ErrKindNotFound`, by exit
        status plus the `not a valid ref` marker, not through `classifyGitError`.
      - A lookup that fails for another reason (for example, an unreadable repository) returns
        the classified error and never falls back to DWIM.
11. **P3-36 regression** (from `poc/gap3-multi-repo-code-repo` `:148` and `:193`, inverted).
    - **`default_branch: "--force"`:**
      - `StartRun` returns `ErrPrecondition` containing `default_branch is not a valid branch name`;
      - the code-repo client (a recording stub) records **zero** calls;
      - no run row is persisted.
    - **`--pathspec-from-file=<file>` set after start:**
      - `attemptCodeRepositoryMerge` returns `ErrPrecondition`;
      - no `Merge` or `Push` call is made and no outcome row is written;
      - the file's contents appear in no error or log line.
    - **`Manager`:** `Register` and `Update` return `ErrInvalidParams` for `--force`, `a..b`, `HEAD`
      and `x y`, and accept `main` and `release/2026.10`.
    - **Mutation:** removing either engine guard makes the stub record a call.
    - *Design-check tightening:*
      - **No clone.** The `repoClients.Client` factory itself is **not invoked**, so no clone is
        attempted.
      - **Primary branch rolled back.** The primary run branch, which `createRunBranches` creates
        and pushes before the code-repo loop, is gone afterwards, both locally and on origin.
12. **Branch-name grammar.**
    - `refname.Validate` rejects `.hidden`, `a/.b`, `a.lock/b`, `a//b`, `/a`, `a.`, `HEAD`, `@` and
      `refs/heads/x`.
    - The existing cases in `refname_test.go` still pass.
    - A corpus test asserts `refname.Validate` never accepts a name that
      `git check-ref-format --branch` rejects.
    - Every `generateBranchName` output in `core/engine/branchname_test.go` is accepted.
13. **Git floor.**
    - `CheckGitVersion` with a shim returns an error for `git version 2.29.2`. It returns nil for
      `2.30.0`, `2.39.5`, `2.54.0 (Apple Git-157)` and `2.45.1.windows.1`.
    - `spine serve` exits non-zero before `LoadPushAuthFromEnv` when the check fails.
    - A new CI job runs `go test ./adapters/git/... ./core/engine/ -run 'RefShadow|RefGrammar|Checkout|EnterBranch|CurrentBranch'`
      in `golang:1.26-bookworm` (git 2.39.5, the shipped version) and passes. The prototype passed
      exactly this on 2.39.5.
14. **No contract drift.**
    - The `git.GitClient` method set and the `MergeOpts`, `GitError` and `FileDiff` fields are
      byte-identical to `61994fe`.
    - No existing Spine test is modified to pass, apart from additions.
      *Design-check note:* this held under the amended overlay. The full
      `go test ./adapters/... ./core/... ./service/... ./cmd/...` failed only the intended
      inversions: the audit PoCs and the design-check bypass tests. It held on git 2.54, and for
      `adapters/git` and `core/{engine,artifact,workflow,divergence}` on 2.39.5.
    - `go test ./...`, `go vet ./...`, `./dev/lint-boundaries.sh` and `make test-scenario` (against
      `docker-compose.test.yaml`) are green.
15. **SMP compatibility gate.**
    - **Setup:** SMP's `replace => ../spine` points at this branch.
    - `go build -mod=readonly ./internal/... ./cmd/...` and `go vet` pass.
    - `go test`, default and `-tags integration`, passes for `internal/customer/spinecore`,
      `projectionwriter`, `workspace`, `spineembedded` and `gitclone` with no SMP edits.
    - The PR description records the run.
16. **Docs.**
    - The doc comment on `CLIClient` carries the §1 class table and the §2 argv rules.
    - `KNOWN-LIMITATIONS.md` → Security Notes gains "Git ref arguments":
      - the grammar;
      - the git 2.30 floor;
      - residual: a user `?ref=<name>` for a non-branch name still follows DWIM, so `refs/<n>` beats
        `refs/tags/<n>`. Use `refs/tags/<n>`; TASK-016 blocks such pushes for non-operators.
    - `CHANGELOG.md`: Changed entries for `Push`, `Clone` and the git floor.
    - `architecture/git-integration.md` §6.4 states that Spine-managed branch names always resolve
      as `refs/heads/<n>`.
    - *Design-check addition:*
      - **`KNOWN-LIMITATIONS.md`** also records:
        - the remote-side doubly-qualified residual (DC-3), with the admission owners (TASK-016,
          SMP E4-TASK-005 §7) and the listing command;
        - the missing-non-branch-ref residual (§7 item 12);
        - the dormant code-repo `default_branch` checkout change (DC-8).
      - **`CHANGELOG.md`** also records that `PushBranch` no longer sets upstream tracking.
      - **§6.4** says "resolve exactly, by object name", not "as `refs/heads/<n>`". A qualified
        name is DWIM-resolved by git when it is missing.
17. **(Design check) Remote-side residual pinned** (DC-3).
    - **Setup:** a bare origin holding each of `refs/tags/refs/heads/main`,
      `refs/refs/heads/main` and `refs/heads/refs/heads/main` in turn.
    - **Expected:** `CLIClient.Push(origin, "main")` returns a **non-retryable** `*GitError`. It is
      not retryable, so the engine does not loop. The local `main` is unchanged.
    - **The test comment names its owners:** TASK-016 and SMP E4-TASK-005 §7. The test asserts the
      documented residual, so a future change that silently starts retrying is caught.
    - **Mutation:** classifying the push failure as transient fails it.
18. **(Design check) `List` resolves once** (DC-7).
    - **Setup:** a recording shim and a branch with N = 5 `.md` artifacts.
    - **Expected:**
      - `artifact.Service.List(ctx, "<branch>")` runs exactly one ref lookup and N `show`
        invocations, each against that same SHA;
      - the returned artifacts equal those at `61994fe`.
    - **Mutation:** per-file resolution records N+1 lookups.
19. **(Design check) Embedded push through a shadowed local namespace** (SMP-facing, run in the
    AC 15 gate).
    - **Setup:** SMP's workflow writer push (`workflowwriter.go:155`) with a local
      `refs/tags/refs/heads/<default>` in the scope worktree's repository.
    - **Expected:** the push succeeds.
    - This is the SMP-side twin of AC 2's DC-1 cases. It extends the regression tests listed in §6,
      SMP TASK-008 item 3.

## Out of scope / follow-ups

- **Push admission of non-branch refs.** TASK-016 owns the standalone gateway, including porting
  `verify-r2-0` phase 1 inverted. SMP TASK-007 owns SMP's gateway. This task makes shadows inert
  without depending on admission. Its regression tests plant refs with a plain `git push` into the
  repository, so they do not break when TASK-016 starts refusing anonymous tag pushes.
  - *Design-check note (DC-3):* "without depending on admission" holds for **local** shadows only,
    and only with DC-1 and DC-2.
    - **Remote-side shadows.** A doubly-qualified ref on the remote breaks every client's push to
      the shadowed branch, and only admission closes that.
    - **TASK-016 must add the rule.** It classifies `refs/heads/refs/heads/<x>` as an ordinary
      `branch` (allowed by policy), so it must add the D12 rule. SMP E4-TASK-005 §7 already has
      it.
- **Binding the merge to the reviewed SHA** (F2, TASK-005). That task adds `SourceSHA`,
  `MergeBound`, `ResolveBranchHead` and `PushCommitCAS` on top of these argv shapes.
- **Bounded reads and `-z` listings** (F4, P3-9; TASK-003). TASK-003 composes its `cat-file` spec
  from the `resolveRev` output and keeps `[eoo]` where this task puts it.
- **`WriteAndStageFile` confinement** (F5, TASK-004). TASK-004's AST scan allow-lists
  `EnterBranch`'s `RemoveAll`, which the new post-condition cleanup also uses.
- **Empty `MergeOpts.Target` meaning "current HEAD"** (F8, TASK-012). Unchanged here.
- **SMP's own plumbing:** `gitclone` clone tags, `RealGitOperator` bare merge argument, and
  `RealGitReader` DWIM (F10.smp-refs, P3-7; SMP TASK-005).
- **Pre-existing shadow refs in deployed repositories.** They are made inert by this task and
  cleaned up per TASK-016's upgrade note.
  - *Design-check note (DC-3, owner question 5):* local shadows are inert. Doubly-qualified refs on
    a repository that clients push to are **not** inert and must be deleted. TASK-016's upgrade
    note lists only refs "whose short name equals a branch name", so it misses them.
- **Follow-ups (not required):**
  - `core/artifact` `List(ref=<branch>)` pays one probe per file. It could resolve once through
    `RefSHA`. *Design-check note:* moved into scope (DC-7, AC 18).
  - *Design check:* `classifyGitError` matches the substring `lock` before anything else
    (`errors.go:45`). So any git error whose stderr echoes a branch containing `lock` is
    classified **transient** and retried, for example `error: src refspec spine/run/…-unblock-…
    does not match any` (code reading). Anchor the pattern to `index.lock` / `.lock': File exists`
    (follow-up, related to P3-27's unenforced retry cap).
  - The gateway echoes the user's `ref` as `source_commit` (`handlers_artifacts.go:186-187`).
  - `HasCommitWithTrailer`'s `log --all` substring match (not audited).
  - SMP's `gitclone.ValidateRef` could converge on `git.ValidateBranchName` after the bump.

## Implementation reference

There is a design-validation prototype. It is not the implementation; the tests in the Acceptance
Criteria still have to be written properly. It lives at
`tmp/init027-orchestration/spine-init023/prototypes/TASK-002/task-002-prototype.diff` in SMP and
applies cleanly to `61994fe` (`git apply --check`).

Test runs on 2026-10-06, overlay only:

| Suite | git 2.54 | git 2.39.5 |
|---|---|---|
| Spine `go test ./adapters/... ./core/... ./service/... ./cmd/...` | green | n/a |
| Spine `-tags scenario` scenarios | green | n/a |
| Adapter tests and the scratch grammar tests | green | green (container) |
| Engine merge tests | green | green (container) |
| gap0 and verify-r2-0 PoCs | inverted | inverted (gap0) |
| gap3 PoCs | inverted | n/a |
| SMP gate (§6) | green | n/a |

Remaining differences from the prototype:
- The prototype's probe falls back to the bare name on any probe error; §1 requires failing closed.
- The prototype tightens `refname.Validate` inline instead of calling TASK-001's
  `ValidateQualified`.
- The prototype has no `CheckGitVersion` or `Manager` guard.

> **Design-check note (2026-10-06): the prototype and the amended overlay.**
>
> - **The table above is not fully green.** Under the prototype, `TestProto_ArgvInjectionRefused`
>   fails. Its `MergeTarget("")` case expects a refusal for an empty `Target`, which §1 allows
>   ("current HEAD", F8). That is a bug in the scratch test, not in the design, but "Adapter tests
>   and the scratch grammar tests: green" overstates it.
> - **The prototype is vulnerable to DC-1, DC-2 and DC-5.**
>   - `TestDC_LocalDoubleQualifiedShadowBreaksPush` reproduces DC-1.
>   - `TestDC_MissingBranchQualifiedNameFollowsShadow` reproduces DC-2.
>   - Reproduced on git 2.39.5 and 2.54. Test file:
>     `_work/task002-check/proto/tree/adapters/git/zz_dc_bypass_test.go`.
> - **Amended design-check prototype.** It implements DC-1, DC-2, DC-4 and DC-6, but not DC-5 or
>   DC-7, which were verified with shell probes.
>   - Diff: `tmp/init027-orchestration/spine-init023/_work/task002-check/task-002-design-check-amended.diff`.
>     It applies cleanly to `61994fe`.
>   - Overlay: `_work/task002-check/overlay-amended.json`.
>   - Results:
>     - the gap0 PoCs invert;
>     - `verify-r2-0` scenario A inverts (re-run on a copy of its work directory);
>     - DC-1 and DC-2 are closed;
>     - the poisoned-ref tests pass unmodified;
>     - the full Spine suite is otherwise green;
>     - the SMP gate is green.
>   - It remains a validation prototype, like the original.

## Design check

Adversarial check on 2026-10-06, against Spine `61994fe` and SMP `80995559`.

**Method.**
- git behaviour was probed on 2.54 (host) and 2.39.5 (`golang:1.26-bookworm`, the shipped
  version).
- The designer's prototype was run as an overlay, as was an amended prototype.
- The audit PoCs (`gap0`, `verify-r2-0` phases 1 and 2 on a copy of the work directory) were
  re-run under both.

**Repositories untouched.** Neither repository was modified. All artifacts are under
`tmp/init027-orchestration/spine-init023/_work/task002-check/`.

| # | Change | Why |
|---|---|---|
| DC-1 | `Push` and `PushBranch` push `<sha>^{commit}:refs/heads/<n>`, with `<sha>` from the exact lookup. `PushBranch` drops `-u` (owner question 4). D7 is amended. AC 2 is extended | **Bypass of the F10 DoS fix.** A local `refs/tags/refs/heads/main`, `refs/refs/heads/main` or `refs/heads/refs/heads/main` makes the designed `refs/heads/main:refs/heads/main` push fail with `src refspec … matches more than one` (Permanent). Every governed merge in standalone fails. A raw-SHA source is shadowed by `refs/tags/<sha>`. Verified on 2.39.5, on 2.54 and through the prototype |
| DC-2 | Branch existence is tested by exact lookup (`show-ref --verify --hash`). The **object name** reaches git for resolved branches, Commit-class arguments and Rev input `refs/heads/<n>`. A missing branch gives `NotFound`, never DWIM. D1(d) is reversed. ACs 1, 4 and 10 are extended | **Bypass when the branch is missing.** git DWIM-resolves a qualified `refs/heads/<n>` to `refs/tags/refs/heads/<n>` and similar names. The designed `rev-parse --verify --quiet` probe exits 0 on such a shadow. In the prototype, `Merge(Source: <deleted run>)` merged the attacker commit, and `ReadFile` and `RefSHA` returned it |
| DC-3 | Remote-side doubly-qualified refs are recorded as a residual this task **cannot** fix. Admission must refuse them (D12, owner question 5, AC 17). The "embedded latent" claim and the out-of-scope claims are corrected | **New finding.** A ref such as `refs/heads/refs/heads/main` on the **remote** makes every client push to that branch fail with `dst refspec … matches more than one`, whatever the argv (verified on both versions). Branch protection admits it for a Contributor (overlay test). In SMP, one member push breaks every push to `main` today: the governed merge, the artifact writer and the workflow writer. SMP E4-TASK-005 §7 refuses these names. Spine TASK-016 does not (it classifies them as `branch`). Its upgrade note misses them, and E4-TASK-005's owner question 1 rationale ("only a missing branch") is wrong |
| DC-4 | Two-phase validation: all arguments are checked before any lookup or git call. ACs 5 and 7 are amended | The fail-closed per-argument probe contradicts "no git process before a refusal". It breaks four subtests of `TestDiffRejectsPoisonedRef` and `TestMergeBaseRejectsPoisonedRef` that AC 7 requires to pass unmodified (verified). The prototype passed only because it falls back on probe errors |
| DC-5 | `EnterBranch` runs `worktree add --detach <sha>`, then `switch --no-guess <n>`, then the post-condition. D13. AC 9 is extended | `worktree add <dir> <n>` looks up `<n>` by DWIM before attaching. A `refs/<n>` or `refs/tags/<n>` pointing at a blob makes it die (`invalid reference`) although the branch exists. This is a per-branch DoS, verified on both versions and present at `61994fe`. `switch` is immune and keeps the checked-out guard |
| DC-6 | Rule 1 passes only `HEAD`, not `^[A-Z_]*HEAD$` | A branch such as `HOTFIXHEAD` would skip branch-first resolution and be shadowable. No caller passes any other pseudo-ref (grep) |
| DC-7 | `core/artifact.Service.List` resolves `ref` once (in scope, AC 18) | Branch-first resolution in each per-file `ReadFile` is O(N) extra processes on SMP AddToRun (`artifacts_addtorun.go:111`, a site §6 missed) and on standalone `/artifacts/add` |
| DC-8 | Claim corrections | §6 missed `artifacts_addtorun.go:111`. The prototype's "scratch tests green" overstates it (`TestProto_ArgvInjectionRefused` fails on its own empty-`Target` case). `switch` refuses during in-progress operations (fails closed). An unborn `HEAD` now gives a name in `CurrentBranch`. The dormant code-repo `default_branch` no longer DWIMs to `origin/<d>`. AC 11 is tightened: no `repoClients.Client` call, and the primary branch is rolled back |

**Verified correct as written.**
- Every Spine and SMP `file:line` cited in Purpose, §1-§7 and §6's table matches its line, with
  the one row-level omission DC-8 names.
- The engine-contract claims hold:
  - no `GitClient` method-set change;
  - SMP's operator prefixes `refs/heads/`;
  - the abort sentinel.
- The P3-36 dormancy analysis and the guard placement hold.
- The `refname` convergence plan, the 2.30 floor rationale and the SMP compile surface hold.
- **SMP gate under the amended overlay:**
  - `go build -mod=readonly ./internal/... ./cmd/...` passes, and so does `go vet`;
  - `go test` passes, default and `-tags integration`, for `spinecore`, `projectionwriter`,
    `workspace` (default only), `spineembedded` and `gitclone`;
  - no SMP edits were needed.

**Cross-task requirements raised by this check.** Each must be accepted by the owning task.
- **Spine TASK-016.**
  - Add the D12 rule: refuse, for every principal, any created or updated ref with a non-first
    `refs` component or a `HEAD` component.
  - Extend the upgrade note to doubly-qualified refs.
- **Spine TASK-005.**
  - `ResolveBranchHead` must use this task's exact lookup. Its `rev-parse --verify --quiet
    refs/heads/<b>^{commit}` DWIM-resolves a missing branch to a shadow, which would be captured
    as the "reviewed" SHA.
  - `PushCommit` must push `<sha>^{commit}:refs/heads/<b>`, not `<sha>:…`. A raw SHA source is
    matched against refs first: TASK-005 DC-4's "a 40-hex ref name is ignored by git" holds for
    rev-parse but **not** for push sources (verified).
- **SMP E4-TASK-005.**
  - §7's refusal is load-bearing for the availability of every SMP push to an existing branch,
    not only for missing branches.
  - Its owner question 1 default ("no automatic deletion") must at least tell admins that
    existing such refs break pushes now.
  - Its operator probe pattern (`rev-parse --verify --quiet refs/heads/<b>`,
    `gitoperator.go:259`) has DC-2's weakness.

## Implementation Notes

Implemented 2026-10-07 on branch `init/INIT-023-TASK-002-ref-grammar-and-argv-hardening`, from Spine
local `main` `ee3c249` (origin `61994fe` + the INIT-023 scaffold + merged TASK-001). Diff base:
`git diff main...HEAD`. Status is left **In Progress**; the orchestrator closes the task after merge.

### Orchestrator rulings and owner questions (defaults pending the owner)

The orchestrator ruled that the owner questions take their defaults; each is implemented as a default
**pending the owner's confirmation**:

| OQ | Default implemented | Where |
|---|---|---|
| 1 | `Clone` adds `--no-tags` (persisted `remote.origin.tagOpt=--no-tags`); CHANGELOG notes that `?ref=<upstream tag>` stops working on newly provisioned clones. **Wider cost (review round 1):** in standalone the provisioned clone (`provision.go:221`) is the repository `githttp` serves (`ResolveRepoPath` → `cfg.RepoPath`), so runners' clones of a newly provisioned workspace carry no upstream tags either (`git describe --tags`, tag-derived versions). The owner should weigh this; the alternative that keeps them is a one-off `fetch --tags` after the provisioning clone | `cli.go` `Clone`; CHANGELOG "Changed" |
| 2 | `Push` accepts branches only; CHANGELOG "Changed (embedders)" | `cli.go` `pushBranch` |
| 3 | `spine serve` refuses to start below git 2.30 | `serve_cmd.go`; `git.CheckGitVersion` |
| 4 | `PushBranch` drops upstream tracking (`-u`); CHANGELOG | `cli.go` |
| 5 | The doubly-qualified refusal rule stays with TASK-016 (standalone) and SMP E4-TASK-005 §7. This task ships only the upgrade-note text: list `git -C <repo> for-each-ref refs/refs refs/heads/refs refs/tags/refs refs/remotes/refs` and delete every listed ref; no automatic deletion | CHANGELOG "Upgrade notes"; KNOWN-LIMITATIONS "Git Ref Arguments" |

The ruling to build on TASK-001's `refname.ValidateQualified` (now on `main`) instead of a second grammar
is followed: `refname.Validate` calls it (§3); there is one refname grammar.

### What was built (commits, oldest first)

1. `770e397` — `refname.Validate` converges on `ValidateQualified` (§3): existing checks unchanged, plus
   `HEAD`/`@`, no `refs/` prefix, and `ValidateQualified("refs/heads/"+name)`.
2. `9a6eca5` — the grammar (`adapters/git/refgrammar.go`, new) and every `CLIClient` method, `EnterBranch`
   and `CurrentBranch` rebuilt on it; `errors.go` gains `Needed a single revision`; `ValidateRef` doc only.
   New exported API exactly as §1: `ErrInvalidRef`, `IsFullObjectName`, `QualifyBranch`,
   `ValidateBranchName`, `ValidateRemoteName`, `CheckGitVersion`, `MinGitVersion = "2.30"`.
   `testutil` gains `InstallGitRecorder` (a recording pass-through `git` wrapper on `PATH`) and
   `HermeticGit`.
3. `41d14ec` — `artifact.Service.List` resolves its ref once (DC-7).
4. `27262e4` — F10 regression tests through `MergeRunBranch` (PoCs gap0 and verify-r2-0 phase 2, inverted).
5. `93c8e30` — P3-36 `default_branch` guards (engine `resolveCodeRepoForBranch` and
   `attemptCodeRepositoryMerge`; `repository.Manager.Register`/`Update`).
6. `15f4d88` — `spine serve` git floor and the CI `git-floor` job (`golang:1.26-bookworm`, git 2.39.5).
7. `60f9194` — CHANGELOG, KNOWN-LIMITATIONS, `architecture/git-integration.md` §6.4,
   `architecture/multi-repository-integration.md` and `docs/operator-runbook.md` (default_branch 400 row).
8. `4b989d2` — errcheck on the new failed-attach cleanup (lint gate).
9. `0e98f31` — codex review P2: the exact lookup runs with `LC_ALL=C` (locale-independent missing-branch
   detection), with a regression test.

Final argv (the amended §2 table, DC-1/DC-2/DC-5): `show-ref --verify --hash refs/heads/<n>` for every
branch lookup; `switch --no-guess --end-of-options <n>`; `merge [...] --end-of-options <oid>` (abort is
exactly `merge --abort`); `branch --end-of-options <n> <HEAD|oid>`; `branch -D --end-of-options <n>`;
`diff --name-status --end-of-options <from'> <to'> --`; `log --format=... [-nN] --end-of-options
[<since'>..HEAD] -- [<path>]`; `show --end-of-options <ref'>:<path> --`; `ls-tree -r --name-only
--end-of-options <ref'>`; `rev-parse --verify --end-of-options <ref'>` (no call at all for an existing
branch); `merge-base --end-of-options <a'> <b'>`; `push --end-of-options <remote>
<oid>^{commit}:refs/heads/<n>` (Push and PushBranch, no `-u`); `push --delete --end-of-options <remote>
refs/heads/<n>`; `clone --no-tags -- <url> <path>`; `EnterBranch`: lookup, `worktree add --detach
--end-of-options <dir> <oid>`, `switch --no-guess --end-of-options <n>` in the worktree,
`symbolic-ref -q HEAD` post-condition; `CurrentBranch`: `symbolic-ref -q HEAD`.

### Per-AC evidence

| AC | Status | Evidence |
|---|---|---|
| 1 | done | `core/engine/merge_refshadow_test.go` `TestRefShadow_MergeRunBranchIgnoresShadowRef` (`refs/tags/<run>`, `refs/<run>`: run completes, origin `main:f.txt` reviewed, reviewed tip ancestor and `main^2`, attacker not an ancestor); `TestRefShadow_MergeRunBranchMissingBranchShadow` (DC-2: `CLIClient.Merge` non-retryable NotFound, attacker not on main, run not completed); adapter half `adapters/git` `TestRefShadow_MissingBranchNeverFollowsShadow` (recorder shows no `merge`/`switch`). All fail at `main` (run against an exported `main` tree). Mutations checked: bare merge source (M1) and a `rev-parse --verify --quiet` lookup (M2) fail them. |
| 2 | done | `TestRefShadow_ShadowedMainDoesNotBlockGovernedMerges` (two runs complete, origin main advances twice, then `CLIClient.Push` nil, with `refs/tags/main` and each doubly-qualified local shadow); `adapters/git` `TestRefShadow_PushIgnoresLocalShadows` adds `refs/tags/<local main oid>`. Mutations: `refs/heads/main:refs/heads/main` source fails the three DC-1 cases (M4); raw `<oid>:` source fails the `refs/tags/<oid>` case (M3). Review round 1: the `refs/tags/<local main oid>` case also runs through the governed merge (`TestRefShadow_ObjectIDShadowOfMainAtPushTime`), and the run-start twin of the DoS (HEAD-named refs) is closed (`TestRefShadow_StartRunCutsFromTheRealBase`). |
| 3 | done | `TestRefShadow_StatusCascadeReadsTheBranch`; fails at `main`; removing the branch-first lookup (M9) fails it. |
| 4 | done | `TestRefShadow_ReadsResolveBranchFirst` (ReadFile/RefSHA/MergeBase/Diff/ListFiles; unchanged resolution for tag-only, 8-hex, `refs/tags/<b>`, HEAD, full oid, `refs/remotes/origin/<b>`; `HOTFIXHEAD`); `TestRefShadow_MissingBranchNeverFollowsShadow` (DC-2, three doubly-qualified shadows). Mutations M2, M5 (`^[A-Z_]*HEAD$`), M9 fail them. |
| 5 | done | `adapters/git/refgrammar_test.go` `TestRefGrammar_InputClassTable` (18 method/parameter rows x 20 input classes + remote class; every refusal: `ErrInvalidRef`, Permanent, `invalid git ref`, no raw control byte, **zero recorded git processes**); `TestRefGrammar_TwoPhase` (DC-4, both argument orders for Diff, MergeBase, Merge, CreateBranch, Push, PushBranch, DeleteRemoteBranch, Log). Mutation M10 (resolve before checking the second arg) fails it and `TestDiffRejectsPoisonedRef`. See deviation 2 for abbreviated ids. |
| 6 | done | `TestRefGrammar_ArgvShape`: exact argv for every method (real object ids), the abort sentinels, `EnterBranch` and `CurrentBranch`; asserts no `rev-parse` argument starts with `refs/heads/`. See deviation 7 (recorder). Review round 1: `CreateBranch` now ends in `<oid>^{commit}` / `HEAD^{commit}` (the amended §2 row passed a bare object id, which `git branch` re-resolves by DWIM). |
| 7 | done | `TestRefGrammar_ArgvInjectionRefusedBeforeExec`; `TestDiffRejectsPoisonedRef` and `TestMergeBaseRejectsPoisonedRef` pass unmodified. |
| 8 | done | `TestCheckout_NeverDetaches`; mutation `checkout <n>` (M6) fails it. |
| 9 | done | `TestEnterBranch_RefusesWithoutLeftovers` (tag-only, branch in use, post-condition via a faked `symbolic-ref`; one worktree, empty `TMPDIR`), `TestEnterBranch_AttachesDespiteShadows` (tag, blob `refs/<n>`, blob `refs/tags/<n>`), `TestCurrentBranch_ShadowDetachedUnborn`, `core/artifact` `TestCreate_AutoPushWithShadowTag`. Mutations: `worktree add <dir> <n>` (M15) fails the blob cases; no post-condition (M16) fails the post-condition subtest; `--abbrev-ref` (M14) fails the CurrentBranch and auto-push tests. See deviation 5. |
| 10 | done | `TestRefShadow_CloneNoTagsAndErrorKinds` (no tags after clone and after a fetch, `tagOpt`, NotFound kinds, empty rev refused with zero git processes, a lookup failure in a non-repository returned classified with no fallback). Mutations M7 (`--no-tags`) and M13 (`Needed a single revision`) fail it. `TestClone` passes. |
| 11 | done | `core/engine/multi_repo_defaultbranch_test.go`: `TestDefaultBranchGuard_StartRunRefusesBeforeAnyCodeRepoGit` (the 8 values of `invalidDefaultBranches`, `refs/heads/develop` included since round 1: ErrPrecondition text, factory never invoked, stub records nothing, no run row, primary run branch gone locally and on origin), `TestDefaultBranchGuard_MergeRefusesBeforeAnyCodeRepoGit` (`--pathspec-from-file=<secret>` plus the same 8 values since review round 2: ErrPrecondition naming the invalid default_branch, no client/merge/push, no outcome row, secret in no error or slog line), `TestDefaultBranchGuard_ValidBranchesStillMerge`; `adapters/repository` `TestManagerDefaultBranchMustBeABranchName`. Removing either engine guard (M11, M12) fails them; each guard's short-name rule is pinned by its own test (review round 2). |
| 12 | done | `refname_test.go` `TestValidate_ConvergedGrammar`, `TestValidate_NeverLooserThanGit` (corpus + 390-byte sweep vs `git check-ref-format --branch`); existing `TestValidate` unchanged and green; `TestGenerateBranchName_PassesTheBranchGrammar`. |
| 13 | done | `TestRefGrammar_GitVersionFloor` (2.29.2 refused; 2.30.0, 2.39.5, `2.54.0 (Apple Git-157)`, `2.45.1.windows.1` accepted), `TestRefGrammar_GitVersionOnThisHost`, `service/cmd/spine` `TestServe_RefusesGitBelowFloor` (error before push auth: `SPINE_GIT_PUSH_TOKEN` not scrubbed). CI job `git-floor` added; the exact AC command plus the whole git adapter and the artifact/workflow services pass in `golang:1.26-bookworm` (git 2.39.5) locally. See deviation 1. |
| 14 | done | `adapters/git/client.go` is byte-identical to `main` (`git diff main...HEAD -- adapters/git/client.go` empty); `GitError`/`MergeOpts`/`FileDiff` unchanged; no existing test modified except additions (`manager_test.go` gained a test and gofmt import order; `refname_test.go` gained tests). `go test ./...`, `go vet ./...`, `./dev/lint-boundaries.sh` green; scenario suite: see gates. |
| 15 | done | SMP gate below: build/vet (plain and `-tags integration`), 38 dependent packages, the named five with `-tags integration`, and the DB-gated runtime suites through `dev/testgate`; no SMP edits. |
| 16 | done | `CLIClient` doc carries the class table and argv rules; KNOWN-LIMITATIONS "Git Ref Arguments" (grammar, floor, `?ref=` DWIM residual, missing non-branch residual, remote doubly-qualified residual with owners and listing command, dormant code-repo default-branch change); CHANGELOG (Security, Changed, Changed (embedders) incl. Push/Clone/floor/PushBranch tracking, Upgrade notes); §6.4 "resolve exactly, by object name". |
| 17 | done | `TestRefShadow_RemoteDoubleQualifiedPushResidual` (three remote shadows: non-retryable `*GitError`, local main unchanged); its comment names TASK-016 and SMP E4-TASK-005 §7. |
| 18 | done | `core/artifact` `TestList_ResolvesTheRefOnce` (one lookup, five `show` calls on one oid, `ls-tree` on that oid, same artifacts as listing the checked-out branch); per-file resolution (M8) fails it. |
| 19 | done (gate) | Source persisted in this artifact (Review round 1, "AC 19 test source"). SMP-side overlay test `TestTask002AC19_WorkflowWriterPushIgnoresLocalShadows` (injected with `go test -overlay`, not committed to SMP): `WorkflowWriter.writeAndPush` with a local `refs/tags/refs/heads/main`, `refs/refs/heads/main`, `refs/heads/refs/heads/main` or `refs/tags/main` in the scope repository pushes, and origin `main` equals local `main`. Passed under `-race` in the testgate run. SMP TASK-008 item 3 commits the equivalent. |

### Deviations (closest safe alternative, with reasons)

1. **Git floor placement.** `spine serve` checks the git version as the first step of `RunE`, not
   "immediately before `LoadPushAuthFromEnv`". It is still before it (AC 13 holds), and it fails before
   any database pool, workspace resolver or secret is opened; it is also testable without a database.
2. **Abbreviated ids in the Commit class.** The table lists abbreviated SHAs as refused, but `abc1234` (or
   `cafe`) is a syntactically valid branch name, so phase 1 cannot refuse it without refusing such
   branches. It is treated as a branch name: exact lookup, `ErrKindNotFound` unless that branch exists,
   and it is never resolved as an object. Pinned by `TestRefGrammar_AbbreviatedIDIsNeverAnObject`; the
   CLIClient doc says so. In the Rev class it resolves as before.
3. **Full object ids are refused as branch names** inside `QualifyBranch` (so also by
   `ValidateBranchName`, e.g. a 40-hex `default_branch`), per the table's "object names" refusal.
4. **`Merge` looks the source up before switching to `Target`**, so a missing source changes nothing in the
   working tree. The argv of each git call is as designed.
5. **`EnterBranch` looks the branch up before allocating the temporary parent**, so a missing branch
   never creates one. The post-condition is therefore a third layer, and AC 9's literal mutation
   ("removing the post-condition fails the tag-only case") cannot fail that case; the post-condition is
   pinned by its own subtest, which fakes `symbolic-ref` through the recorder
   (`SPINE_GIT_RECORDER_FAKE_SYMREF`).
6. *(Superseded in review round 1: `core/engine` `TestRefShadow_ObjectIDShadowOfMainAtPushTime` now plants
   the shadow at push time through a `Push` decorator, so the case runs through the governed merge.)*
   **AC 2's `refs/tags/<local main oid>` case** needs the shadow planted between the governed merge and
   its push (the merged oid is not known before), so it is pinned at the adapter level
   (`TestRefShadow_PushIgnoresLocalShadows`); the governed-merge test covers the other four shadows.
7. **AC 5/6 shim.** `testutil.InstallGitRecorder` records argv and then runs the real git, instead of a
   fake that answers lookups. Expected argv use the real object ids; the real git answers `show-ref`
   (exit 128 + `not a valid ref` when missing, verified on 2.39.5 and 2.54).
8. **Rev input `refs/heads/<x>` where `<x>` is not a valid branch name** (e.g. `refs/heads/x@{1}`) passes
   through as a rev expression (rule 3), not refused.
9. **Empty values that are not ref arguments** keep their meaning: `MergeOpts.Target` "" (current branch,
   F8/TASK-012), `LogOpts.Since` "" (no lower bound), `EnterBranch` branch "" (unbranched scope).
10. **Locale.** Not in the design: the exact lookup forces `LC_ALL=C` (codex P2), because "not a valid
    ref" is translatable. `classifyGitError`'s English matching is pre-existing for every other command
    and unchanged.
11. **Extra docs**: `architecture/multi-repository-integration.md` and `docs/operator-runbook.md` (the
    `default_branch` rule and its 400), because the API contract changed.
12. **Recorded behaviour deltas beyond §7** (all in CHANGELOG): `Diff`/`MergeBase` refusals are now
    `*GitError` (text kept); `Merge` of a branch source with no `Message` gets git's `Merge commit
    '<oid>'` text; `CreateBranch` from a base with no local branch is NotFound; `CurrentBranch` outside a
    repository returns an error that includes git's stderr.

### Gates (commands and results; Spine at `0e98f31` unless noted)

| Gate | Command | Result |
|---|---|---|
| Boundary lint | `./dev/lint-boundaries.sh` | OK |
| Build | `make build` | OK |
| Embed smoketest | `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK ("library surface is sound") |
| Unit | `go test -p 4 -count=1 ./...` | all ok |
| Vet | `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| Race | `go test -race -p 4 -count=1 ./adapters/git/... ./adapters/repository/ ./core/artifact/ ./core/engine/ ./service/cmd/spine/ ./testutil/` (at `4b989d2`), re-run for `./adapters/git/... ./core/artifact/ ./core/engine/ ./core/workflow/` after `0e98f31` (the other packages did not change) | all ok |
| Integration (at `4b989d2`) | `SPINE_TEST_DATABASE_URL=.../s002_it go test -tags integration -count=1 -p 1 ./testutil/ ./adapters/projection/ ./core/engine/ ./core/artifact/ ./adapters/git/... ./adapters/repository/` | all ok (51 DB tests passed, 0 skipped in testutil + projection) |
| CI scenario job (at `4b989d2`) | `SPINE_TEST_DATABASE_URL=.../s002_scen go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` | 19 PASS, 0 SKIP |
| Full scenario suite (at `4b989d2`) | `go test -tags scenario -count=1 -p 1 ./service/scenariotest/...` on this branch (`s002_scen`) and on an exported `main` tree (`s002_scen_main`) | 30 failures on both, **identical sets** (pre-existing: fixture `validation_failed`, `TestSchemaMatchesProduction` metadata NOT NULL); no new failure. The CI comment already records the rest of the suite as not green on `main`. |
| git 2.39.5 | `docker run --rm golang:1.26-bookworm ... go test -count=1 ./adapters/git/... ./core/engine/ -run 'RefShadow|RefGrammar|Checkout|EnterBranch|CurrentBranch' && go test -count=1 ./adapters/git/... ./core/artifact/ ./core/workflow/` (worktree and module cache mounted read-only, `GOPROXY=off`) | all ok (image present; container removed) |
| golangci-lint | `golangci-lint run ./adapters/git/... ./adapters/repository/ ./core/artifact/ ./core/engine/ ./service/cmd/spine/ ./testutil/` vs the same on `main` | 0 new issues (5 pre-existing gofmt findings fixed by import sorting in touched files) |
| gosec | same with `--enable-only=gosec` | 0 issues (main: 0) |
| gofmt | `gofmt -l` on every changed `.go` file | empty |
| Fails at base | new adapter/engine/artifact regression tests run against an exported `main` tree (one stub `ErrInvalidRef`) | the PoC-derived tests fail there (F10 reads, merge, cascade, `refs/tags/main` DoS, missing-branch shadow, Checkout, EnterBranch blob shadows, CurrentBranch, Clone, List) |
| Mutations | 16 single-point mutations in a scratch copy (M1-M16 above, plus the locale fix) | each fails its named test |
| codex | `codex review --base main` | round 1: one P2 (locale-dependent missing-branch detection) — fixed in `0e98f31` with a test. Round 2 (`0e98f31`, after a capacity retry): "No actionable regressions were identified". No finding was rejected. |

### SMP compatibility gate

SMP checkout `/Users/bartoszszymichowski/42sid/i23/s-T002/smp`, detached at SMP `7d4e7cc9`, `../spine`
= this worktree, no `vendor/`, no `tmp/`; `GOFLAGS=-mod=readonly`; the checkout stayed clean. Run at
`4b989d2` and re-run in full at the final code commit `0e98f31` (same results).

- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`: OK.
- Packages depending on a changed Spine package (`go list -test -deps`; 37, plus `gitclone` named by AC 15):
  `go test -p 4 -count=1` on all 38: all ok.
- AC 15's named five with `-tags integration` (`spinecore`, `projectionwriter`, `workspace`,
  `spineembedded`, `gitclone`): all ok.
- DB-gated runtime suites like SMP CI's "Spine runtime integration suites" step, against
  `s002_smpspine` (`go run ./dev/apply-migrations -set spine-runtime`) and `s002_smpplatform`
  (`-set platform`): `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip'
  -allow-skip 'TEST_PROVISIONING_DSN unset' -- -race -p 4 -count=1 -timeout 20m -overlay <AC 19 overlay>
  ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/
  ./internal/customer/executionreadiness/` — **PASS: 1541 tests passed, 0 failed**, 4 allowed skips.
- `projectionwriter` platform-DB suite: `TEST_DATABASE_URL=TEST_PLATFORM_DB=.../s002_smpplatform go run
  ./dev/testgate -allow-skip 'TEST_PROVISIONING_DSN' -- -race -count=1 ./internal/customer/projectionwriter/`
  — PASS (8 passed, 2 allowed skips).

**Not run, with reason and exact command.** The suites gated on `TEST_PROVISIONING_DSN` allocate per-test
`ws_*` databases and roles as `smp_prov`, which the shared-database rule (only `s002_*` databases) forbids:
3 `spinecore` dispatch-store tests (not git-related), 2 `projectionwriter` worker tests and the
`workspace` live-Postgres suites. Command for an isolated server: `TEST_PROVISIONING_DSN=postgres://smp_prov:smp_prov@<host>/postgres?sslmode=disable go run ./dev/testgate -- -race -count=1 ./internal/customer/spinecore/ ./internal/customer/projectionwriter/ ./internal/customer/workspace/` (plus `TEST_SPINE_DB`/`TEST_PLATFORM_DB` as above). Spine's own `adapters/projection`
integration suite (projection over `CLIClient`) passed.

All `s002_*` scratch databases (`s002_it`, `s002_scen`, `s002_scen_main`, `s002_smpplatform`,
`s002_smpspine`) were dropped after the gates; no container was left running.

### Handoffs

- **SMP INIT-027 EPIC-004 TASK-008:** bump `.spine-revision` (no SMP code change is needed to compile);
  call `spinegit.CheckGitVersion(ctx)` in `bootCore`; commit the AC 19 workflow-writer test, whose full source is under Review round 1 → "AC 19 test source" (local
  `refs/tags/refs/heads/<default>` and siblings, push succeeds, origin equals local) plus the §6 item 3
  tests (AddToRun `NextID` with `refs/tags/<run>` in the pooled clone; `RunBranchDiffReader` guard).
- **Spine TASK-005:** use this task's exact lookup (`lookupBranch` / `QualifyBranch`) for
  `ResolveBranchHead`, and push `<sha>^{commit}:refs/heads/<b>`.
- **Spine TASK-016 / SMP E4-TASK-005 §7:** the D12 admission rule (DC-3) and the upgrade note's listing
  command are the remaining owners of the remote-side residual (`TestRefShadow_RemoteDoubleQualifiedPushResidual` pins it).
- **Follow-up (not done, as the design says):** `classifyGitError` matches the substring `lock` first, so a
  git error that echoes a branch name containing "lock" is classified transient.

### Review round 1 (2026-10-07)

Three reviewers (correctness and SMP compatibility, adversarial security, completeness and tests)
reviewed the branch at `24e97a6`. Every finding was verified against the code and against git
(host 2.54.0; `golang:1.26-bookworm` 2.39.5) before acting.

Commits, oldest first: `c6c6633` (CreateBranch start point and regression tests), `d04a1e4`
(`default_branch` short-name rule), `9f248ec` (CHANGELOG, KNOWN-LIMITATIONS and docs
corrections), `bf7fafe` (ReadFile miss classification test), then this notes commit.

#### Findings

| # | Sev | Finding | Disposition |
|---|---|---|---|
| R1 | P2 | `CreateBranch` passes a bare object id to `git branch`, which re-resolves its start point by DWIM, so a ref named after the base's object id replaces the base | **FIXED** (`c6c6633`). Reproduced on 2.39.5 and 2.54 for `refs/<oid>`, `refs/tags/<oid>`, `refs/heads/<oid>` and `refs/remotes/<oid>`. Probing also confirmed that `merge`, `show`, `ls-tree`, `diff`, `merge-base`, `rev-parse` and `worktree add --detach` keep a full hex id as the object; only `git branch` re-DWIMs (branch.c `dwim_branch_start`). `CreateBranch` now passes `<oid>^{commit}`: `^` is illegal in refnames, so `dwim_ref` matches nothing, and no tracking is written. Tests: adapter `TestRefShadow_ObjectIDNamedRefsAreInert` (CreateBranch from `main`, `refs/heads/main` and the oid; Merge, ReadFile, RefSHA, ListFiles and EnterBranch by object id) and engine `TestRefShadow_StartRunCutsFromTheRealBase` (real CLIClients; the code-repo run branch and `RepositoryBaselines` both hold the real tip). The bare start point (mutation) fails both. The AC 6 expected argv was updated. The `refgrammar.go` comment ("git never DWIM-resolves a full hex id"), the `CLIClient` doc, KNOWN-LIMITATIONS and §6.4 now name the `git branch` exception. |
| R2 | P2 | `git branch <run> HEAD` dies `ambiguous object name: 'HEAD'` next to any `refs/HEAD`, `refs/tags/HEAD`, `refs/heads/HEAD` or `refs/remotes/HEAD`. StartRun and divergence cut from `HEAD`, so this is a workspace-wide run-start denial of service (it predates this task) | **FIXED** (`c6c6633`) with `HEAD^{commit}` rather than the suggested `rev-parse` pre-resolution. The effect is the same: the revision parser reads `$GIT_DIR/HEAD` first (DWIM rule 1, a warning at most), and `^` makes `dwim_ref` match nothing. It needs no extra process, and an unborn `HEAD` keeps its Permanent `not a valid object name` classification; a `rev-parse --verify` pre-step would have turned that into NotFound `Needed a single revision`. Verified on 2.39.5 and 2.54, including `refs/remotes/HEAD/HEAD`, `branch.autoSetupMerge=always` and a detached HEAD. Tests: adapter `TestRefShadow_HEADNamedRefsAreInert` (CreateBranch, Head, RefSHA, ReadFile) and the four HEAD cases of `TestRefShadow_StartRunCutsFromTheRealBase`. The upgrade notes (CHANGELOG, KNOWN-LIMITATIONS) now also list HEAD-named and object-id-named refs. Spine's own calls are immune to both, but other git clients are not. Both listing commands were verified to match exactly those refs and not `refs/remotes/origin/HEAD`. |
| R3 | P3 | The CHANGELOG ReadFile error text is wrong, and the `exists on disk, but not in` case (Permanent/500 becomes NotFound/404) is unrecorded | **FIXED** (`9f248ec`, `bf7fafe`). Verified: with the trailing `--`, every miss reads `fatal: bad revision '<rev>:<path>'`, including HEAD and full-oid reads. The CHANGELOG "Error text" entry is corrected, and a "Changed" entry records the 404 reclassification (artifact and workflow reads, `evidence.Load` returning `Found: false`). §7 delta 7 is superseded by this. The trailing `--` is kept (D3): without it, a working-tree file literally named `<rev>:<path>` is taken as a pathspec. The new kind is the correct one (the path does not exist at that revision). In SMP the case needs an untracked file in a scope clone and is an error path either way; this is recorded for TASK-008. Test `TestRefShadow_ReadFileMissIsNotFound` (kind and `bad revision` text, five cases); dropping the `--` fails its on-disk cases. |
| R4 | P3 | "Changed (embedders)" omits the Rev-class `ValidateRef` narrowing and the loss of remote-tracking guessing in `Checkout`/`EnterBranch` | **FIXED** (`9f248ec`). There are two new bullets. No Spine or SMP call site passes a rev expression (grep), and the gateways' `?ref=` already applied `ValidateRef`. The existing grammar-table rows `a~1`, `a^`, `a:b`, `a..b` pin the narrowing. The guessing loss is pinned by the new `TestRefShadow_RemoteTrackingBranchIsNeverGuessed`. **Correction to DC-8 and to the CHANGELOG I wrote:** `git branch <run> <d>` never reached `origin/<d>`. The DWIM rules try `refs/remotes/<d>`, not `refs/remotes/origin/<d>`, and git failed with `not a valid object name` (Permanent; verified with `origin` configured). Only `checkout` and `worktree add` guessed. The `CreateBranch` delta is therefore Permanent→NotFound, not "refused instead of guessed". The CHANGELOG, the KNOWN-LIMITATIONS dormant entry and the `CreateBranch` comment are corrected. |
| R5 | P3 | The cost of `clone --no-tags` is wider than stated: the provisioned clone is the served repository, so runner clones lose upstream tags | **FIXED (docs)** (`9f248ec`; the OQ 1 row above). Verified: `provision.go` clones into `RepoPath`, and `githttp`'s `ResolveRepoPath` returns `cfg.RepoPath`. The default is unchanged pending the owner. The alternative that keeps the tags (a one-off tag fetch after the provisioning clone) is recorded for the owner, not implemented, because it would reverse the ruled default. |
| R6 | P3 | DC-2 adds one `show-ref` per Rev read of `refs/heads/<n>`. SMP's `RunBranchDiffReader` pays about N+2 extra processes per run-artifacts request, and §1's "hot paths pay nothing" is stale | **FIXED (docs and handoff)**. Results are unchanged; only latency changes. There is no Spine code change, because an exact lookup and a read cannot share one git process: `show-ref --verify` is the only DWIM-free lookup, and `show`/`rev-parse` of a name DWIM when the branch is missing. A cache keyed by name would reopen a stale-tip window. **Correction to §1:** under DC-2, `RunBranchDiffReader`'s `refs/heads/<run>` is resolved exactly, once per `MergeBase`, `Diff` and per-file `ReadFile`. Projection (SHAs) and `HEAD` reads still pay nothing. Recorded in CHANGELOG "Changed (embedders)" and KNOWN-LIMITATIONS. Handoff to SMP TASK-008 below. |
| R7 / R8 | P3 | A `default_branch` of `refs/heads/main` is accepted and stored verbatim, although the docs say `refs/...` is refused, and the rule is not pinned at the API layer | **FIXED (code)** (`d04a1e4`). One rule, matching the docs: the short name only. The new additive `git.ValidateShortBranchName` is `ValidateBranchName` without the `refs/heads/<n>` form. `Manager.Register`/`Update` (400) and both engine guards (precondition, no git call) use it. `refs/heads/` values were added to `TestManagerDefaultBranchMustBeABranchName` and to the engine guard's invalid list, and removed from its valid list (both lists were added by this task, so no pre-existing test changed). Reverting to `ValidateBranchName` fails them. The docs now say "`main`, not `refs/heads/main`". |
| R9 | P3 | The AC 19 test exists only as a scratchpad overlay | **FIXED**. Its full source is under "AC 19 test source" below, and the TASK-008 handoff points there. It was re-run this round: 4/4 PASS, and under `-race` in the testgate run. It is still not committed in SMP, because this task must not modify the SMP checkout. |
| R10 | P3 | The `EnterBranch` loss of remote-tracking guessing is unrecorded, and "the final state is unchanged" overclaims | **FIXED** (`9f248ec`, test in `c6c6633`). Verified on 2.54 with `origin` configured: `worktree add <dir> <n>` created `<n>` tracking `origin/<n>`. Now a wrapped NotFound, with no local branch, no extra worktree and no temporary parent (`TestRefShadow_RemoteTrackingBranchIsNeverGuessed`). The CHANGELOG wording now reads "for an existing local branch the resulting worktree is the same as before". |

#### Acceptance-criteria gaps

| AC | Disposition |
|---|---|
| 2 (`refs/tags/<local main oid>` only at adapter level) | **FIXED**. `TestRefShadow_ObjectIDShadowOfMainAtPushTime` wraps the engine's `CLIClient` in a `Push` decorator that plants `refs/tags/<merged main oid>` on an attacker commit built on origin `main`, right before the governed push. The run completes, origin `main` equals the merge, and the attacker commit is not on it. A raw `<oid>:refs/heads/main` source fails it: the attacker commit was published as a fast-forward and the run "completed". Deviation 6 is superseded. |
| 2 (run-start DoS through HEAD-named refs) | **FIXED** (R2). |
| 6 (CreateBranch argv not shadow-proof) | **FIXED** (R1). |
| 16 (CHANGELOG error text; KNOWN-LIMITATIONS and §6.4 overclaims; upgrade listing) | **FIXED** (R1 to R4, R10). |
| 19 (overlay only) | **FIXED** as far as this task can go (R9). |
| 14 (`make test-scenario` not green) | **NOT REAL as a regression of this task**, because the failures are identical on `main`. The full scenario suite was re-run this round at `9f248ec` (`s002_r1scen`) and on an exported `main` tree (`s002_r1scenmain`): 30 failures each, **identical sets**, and 190 PASS each. The failures are fixtures that fail validation, plus the gateway-connection cases the reviewer described, at `main` too; the CI comment already records the suite as not green on `main`. `docker-compose.test.yaml` was not used because the shared machine forbids building or pulling images. The CI scenario job (`^TestGitHTTP_`) passes: 19 PASS, 0 SKIP. Fixing the pre-existing scenario fixtures is outside this task. |
| 5 (abbreviated SHA looked up as a branch, not refused) | **NOT REAL as a defect** (deviation 2). Phase 1 cannot refuse `cafe` or `abc1234` without refusing legitimate branches of those names, which git allows. The safety property the AC wants, that an abbreviated id is never resolved to an object in the Commit class, holds and is pinned by `TestRefGrammar_AbbreviatedIDIsNeverAnObject`. |
| 9 (literal mutation "removing the post-condition fails the tag-only case") | **NOT REAL as a defect** (deviation 5). The exact lookup refuses a tag-only name before any worktree exists, so the post-condition cannot be what fails that case. The post-condition is pinned by its own subtest (faked `symbolic-ref`), which was confirmed by mutation. |
| 15 (`TEST_PROVISIONING_DSN` suites not run) | **NOT RUN, by rule**. Those suites allocate per-test `ws_*` databases and roles as `smp_prov`. The shared-database rule allows only `s002_*` databases. The command for an isolated server is recorded under "SMP compatibility gate". Everything else in the SMP gate was re-run this round (below). |
| 13 (git-floor job not yet run on GitHub Actions) | **NOT RUNNABLE here**: a GitHub Actions run needs a push, which is forbidden. The job's exact commands were re-run this round in `golang:1.26-bookworm` (git 2.39.5), as root against the host-owned checkout mounted read-only. All tests ran with 0 SKIP, including the five new round-1 tests. On Actions, modules are downloaded (network allowed there), and the runner-owned checkout does not matter. These tests touch only temporary repositories under `testutil.HermeticGit` (their own `HOME`, `GIT_CONFIG_NOSYSTEM`), so `safe.directory` never applies, and `go test` does not stamp VCS information. |

#### Review round 1 gates (Spine at `9f248ec` unless noted; `bf7fafe` adds one adapter test, run separately)

| Gate | Command | Result |
|---|---|---|
| Boundary lint | `./dev/lint-boundaries.sh` | OK |
| Build | `make build` | OK |
| Embed smoketest | `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK ("library surface is sound") |
| Unit | `go test -p 4 -count=1 ./...` | all ok |
| Vet | `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| Race | `go test -race -p 4 -count=1 ./adapters/git/... ./adapters/repository/ ./core/engine/ ./core/artifact/ ./core/workflow/ ./core/divergence/ ./service/cmd/spine/ ./testutil/` | all ok |
| Integration | `SPINE_TEST_DATABASE_URL=.../s002_r1it go test -tags integration -count=1 -p 1 ./testutil/ ./adapters/projection/ ./core/engine/ ./core/artifact/ ./core/workflow/ ./core/divergence/ ./adapters/git/... ./adapters/repository/` | all ok (1214 PASS, 0 SKIP) |
| CI scenario job | `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (`s002_r1scen`) | 19 PASS, 0 SKIP |
| Full scenario suite | `go test -tags scenario -count=1 -p 1 ./service/scenariotest/...`, branch vs exported `main` | 30 failures each, identical sets; 190 PASS each |
| git 2.39.5 | the `git-floor` job's two `go test` steps in `golang:1.26-bookworm` (read-only mounts, `GOPROXY=off`) | all ok; `-v` shows every new test PASS, 0 SKIP; containers removed |
| golangci-lint | `golangci-lint run ./adapters/git/... ./adapters/repository/ ./core/artifact/ ./core/engine/ ./service/cmd/spine/ ./testutil/`, branch vs `main` (issues compared without line numbers) | 0 new issues (80 vs 85; the 5 fewer are gofmt import order fixed in touched files) |
| gosec | same, `--enable-only=gosec` | 0 issues (`main`: 0) |
| gofmt | `gofmt -l` on every changed `.go` file | empty |
| Mutations | bare `CreateBranch` start point; raw `<oid>:` push source; `ValidateBranchName` in the three `default_branch` guards; no trailing `--` on `show` | each fails its named test (ObjectIDNamedRefsAreInert, HEADNamedRefsAreInert, StartRunCutsFromTheRealBase; ObjectIDShadowOfMainAtPushTime; TestManagerDefaultBranchMustBeABranchName and the engine guard test; ReadFileMissIsNotFound). **Corrected in review round 2:** this held for the Manager and StartRun guards only. The merge-guard mutation alone failed no test. Round 2 pins that guard and re-runs each guard mutation separately (see Review round 2) |
| codex | `codex review --base main` (at `9f248ec`) | "No actionable regressions were identified." No finding was rejected. |
| SMP gate | SMP `7d4e7cc9`, `../spine` = this worktree, `GOFLAGS=-mod=readonly` | `go build ./...`, `go vet ./...`, `go vet -tags integration ./...` OK. All 38 SMP packages depending on a changed Spine package (`go list -test -deps`, plus `gitclone`) pass `go test -p 4`. The named five pass with `-tags integration`. `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -allow-skip 'TEST_PROVISIONING_DSN unset' -- -race -p 4 -count=1 -timeout 20m -overlay <AC 19 overlay> ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` (`s002_r1smpspine`, `s002_r1smpplatform`, migrated with `dev/apply-migrations`) gives **PASS: 1541 passed, 0 failed, 4 allowed skips**. The `projectionwriter` platform-DB testgate gives **PASS: 8 passed, 2 allowed skips**. The AC 19 test run directly with `-race`: 4/4 PASS. The SMP checkout stayed clean. |

All `s002_r1*` databases were dropped and no container was left running. The SMP-reachable
deltas of this round are none in behaviour. SMP never calls `CLIClient.CreateBranch` (its engine
uses its own `RealGitOperator`), and it wires no repository `Manager` or code-repo catalog. The
round changes only Spine-side argv, guards and docs.

#### Handoffs added in round 1

- **SMP INIT-027 EPIC-004 TASK-008:**
  - **(a)** `NewRunBranchDiffReader` should resolve `runRef` once with `RefSHA("refs/heads/<run>")`,
    whose exact lookup yields the object id with no extra process. It should then pass that oid
    to `MergeBase`, `Diff` and every `ReadFile`. This removes about N+2 `show-ref` processes per
    run-artifacts request and reads one consistent snapshot.
  - **(b)** Commit the AC 19 test whose full source is under "AC 19 test source" below.
  - **(c)** Note the 404 reclassification of an "exists on disk, but not in" read (R3), which can
    reach `ArtifactService.ReadRaw` only through an untracked file in a scope clone.
- **Spine TASK-016:**
  - Besides D12, consider refusing created or updated refs whose last component is a full object
    id (`refs/<oid>`, `refs/{tags,heads,remotes}/<oid>`). Spine's own calls are immune after round
    1, but other git clients are not: `git checkout <oid>` lands on `refs/heads/<oid>`, and
    `git branch <n> <oid>` re-DWIMs. The current rule classifies `refs/heads/<40 hex>` as an
    ordinary branch.
  - The upgrade listing for such refs ships in CHANGELOG.
- **Spine TASK-005:** any new `git branch` (or `checkout -b`) start point must be `<oid>^{commit}`,
  exactly like the push source. A bare object id there is re-resolved by DWIM.

#### AC 19 test source

The SMP-side test was injected into `internal/customer/spinecore` with `go test -overlay` and run in
the SMP gate. SMP TASK-008 commits it (or an equivalent with the same setup and assertions):

<details>
<summary><code>internal/customer/spinecore/zz_task002_ac19_test.go</code></summary>

```go
package spinecore

// INIT-023 TASK-002 AC 19 (SMP-side twin of Spine AC 2's DC-1 cases),
// injected with `go test -overlay` for the Spine compatibility gate; SMP
// INIT-027 EPIC-004 TASK-008 item 3 commits the equivalent test. The
// workflow writer pushes the authoritative branch with
// git.CLIClient.Push(origin, <default>); a local doubly-qualified shadow
// of that branch in the scope worktree's repository (or a plain
// refs/tags/<default>) must not break the push.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	spinegit "github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/workflow"
)

func TestTask002AC19_WorkflowWriterPushIgnoresLocalShadows(t *testing.T) {
	requireGitForIntegration(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	for _, shadow := range []string{"refs/tags/refs/heads/main", "refs/refs/heads/main", "refs/heads/refs/heads/main", "refs/tags/main"} {
		t.Run(shadow, func(t *testing.T) {
			root := t.TempDir()
			origin := filepath.Join(root, "origin.git")
			dir := filepath.Join(root, "scope")
			gitRun(t, root, "init", "--quiet", "--bare", "-b", "main", origin)
			gitRun(t, root, "init", "--quiet", "-b", "main", dir)
			gitRun(t, dir, "config", "user.email", "t@example.com")
			gitRun(t, dir, "config", "user.name", "test")
			gitRun(t, dir, "remote", "add", "origin", origin)
			if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitRun(t, dir, "add", "a.txt")
			gitRun(t, dir, "commit", "--quiet", "-m", "base")
			gitRun(t, dir, "push", "--quiet", "origin", "refs/heads/main:refs/heads/main")
			// The shadow points at an unrelated commit.
			gitRun(t, dir, "switch", "--quiet", "--orphan", "attacker")
			if err := os.WriteFile(filepath.Join(dir, "evil.txt"), []byte("evil\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitRun(t, dir, "add", "evil.txt")
			gitRun(t, dir, "commit", "--quiet", "-m", "attacker")
			evil := gitRun(t, dir, "rev-parse", "HEAD")
			gitRun(t, dir, "switch", "--quiet", "main")
			gitRun(t, dir, "branch", "--quiet", "-D", "attacker")
			gitRun(t, dir, "update-ref", shadow, evil)

			client := spinegit.NewCLIClient(dir)
			w := &WorkflowWriter{git: client, defaultBranch: "main", repoDir: dir}
			_, err := w.writeAndPush(context.Background(), func() (*workflow.WriteResult, error) {
				if err := os.MkdirAll(filepath.Join(dir, "workflows"), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "workflows", "w.yaml"), []byte("id: w\n"), 0o600); err != nil {
					return nil, err
				}
				gitRun(t, dir, "add", "workflows/w.yaml")
				gitRun(t, dir, "commit", "--quiet", "-m", "workflow write")
				return &workflow.WriteResult{}, nil
			})
			if err != nil {
				t.Fatalf("writeAndPush with local %s: %v", shadow, err)
			}
			local := gitRun(t, dir, "show-ref", "--verify", "--hash", "refs/heads/main")
			if got := gitRun(t, origin, "show-ref", "--verify", "--hash", "refs/heads/main"); got != local {
				t.Fatalf("origin main = %s, want local main %s", got, local)
			}
		})
	}
}
```

The overlay file was `{"Replace":{"<smp>/internal/customer/spinecore/zz_task002_ac19_test.go":"<this file>"}}`, used as
`go test -overlay overlay.json -run TestTask002AC19 ./internal/customer/spinecore/`.

</details>

### Review round 2 (2026-10-07)

A second-round reviewer examined the branch at `c90ab13`. Both findings and all four gaps were
verified against the code (and, for the cost finding, against git) before acting.

Commits, oldest first: `13133af` (merge-guard test), `7e41c15` (cost notes and the argv rows that
pin them), then this notes commit. No non-test Go file changed this round.

#### Findings

| # | Sev | Finding | Disposition |
|---|---|---|---|
| R2-1 | P3 | No test pins the round-1 short-name rule on the code-repository merge guard (`multi_repo_merge.go`, `attemptCodeRepositoryMerge`). The merge test iterated over only `--pathspec-from-file=<secret>` and `--force`, and the round-1 Mutations row overclaimed | **FIXED** (`13133af`). Reproduced first: with only the merge guard reverted to `ValidateBranchName` and the round-1 test file (both through `go test -overlay`, worktree untouched), every `TestDefaultBranchGuard*` test stays green. `TestDefaultBranchGuard_MergeRefusesBeforeAnyCodeRepoGit` now iterates over `--pathspec-from-file=<secret>` plus all 8 `invalidDefaultBranches`, `refs/heads/develop` included. For each value it asserts ErrPrecondition, an error that names the invalid default_branch (so the refusal comes from this guard, not the empty-branch check), zero client, Merge and Push calls, no outcome row, and the secret in no error or log line. Each guard mutation was then run **on its own** over `./core/engine/ ./adapters/repository/ ./service/...`: merge guard → only `MergeRefusesBeforeAnyCodeRepoGit/refs/heads/develop` fails; StartRun guard (`resolveCodeRepoForBranch`) → only `StartRunRefusesBeforeAnyCodeRepoGit/refs/heads/develop`; `Manager` → only `TestManagerDefaultBranchMustBeABranchName`. The unmutated tree is green. The round-1 Mutations row and the AC 11 evidence row are corrected in place. |
| R2-2 | P3 | The cost note (CHANGELOG, KNOWN-LIMITATIONS) says only a bare *existing* branch or `refs/heads/<n>` pays the extra `git show-ref`, but `resolveRev` rule 4 looks up every bare name that passes `QualifyBranch` | **FIXED** (`7e41c15`). Verified by counting `show-ref` processes per `ReadFile` under `GIT_TRACE` (a throwaway overlay test): `main`, `v1.2`, an abbreviated id, `origin/main`, a missing bare name, `refs/heads/main` and a missing `refs/heads/nope` → 1 each; a full object id, `HEAD`, `refs/tags/v1.2` and `refs/remotes/origin/main` → 0. Both notes now state exactly that and name the Rev-class methods it applies to (`ReadFile`, `ListFiles`, `RefSHA`, `Diff`, `MergeBase`, `LogOpts.Since`). The CHANGELOG adds that `RefSHA` of an existing branch runs the lookup instead of `rev-parse`. The resolve-once-with-`RefSHA` advice now covers tags and `origin/<n>` too. `TestRefGrammar_ArgvShape` gains one row per class: abbreviated id, `origin/<n>`, missing bare name (NotFound), `refs/heads/<n>` present and missing (lookup only, then NotFound), object id, `refs/tags/`, and `refs/remotes/`. The documented model is therefore pinned by exact argv; it passes on host git and on 2.39.5. No code change: the lookup is what makes branch-first resolution safe, and knowing that a name is *not* a branch requires the lookup itself. **Correction to §1 and DC-2 "Cost":** "only for bare non-SHA names" (§1) and "one process per resolved branch" (DC-2) undercount in the same way. The design text is kept as the record, and this row supersedes it. SMP's `RunBranchDiffReader` reads by `refs/heads/<run>`, so the round-1 handoff (a) is unchanged. |

#### Acceptance-criteria gaps

| AC | Disposition |
|---|---|
| 11 (merge guard's `refs/heads/<n>` refusal unpinned) | **FIXED** (R2-1). |
| 13 (`git-floor` never run on GitHub Actions) | **NOT RUNNABLE here**: a GitHub Actions run needs a push, which is forbidden. It was re-run this round because the new argv rows are in its filter. In `golang:1.26-bookworm` (git 2.39.5, read-only mounts, `GOPROXY=off`, `--pull=never`), step 1 `go test -count=1 -v ./adapters/git/... ./core/engine/ -run 'RefShadow\|RefGrammar\|Checkout\|EnterBranch\|CurrentBranch'` gives 70 PASS, 0 SKIP and 0 FAIL. Step 2 (`./adapters/git/... ./core/artifact/ ./core/workflow/`) is all ok. The container was removed. The gap is environmental, not a defect. |
| 15 (`TEST_PROVISIONING_DSN` suites) | **NOT RUN, by rule** (unchanged from round 1): they allocate `ws_*` databases and `smp_prov` roles. The command for an isolated server is under "SMP compatibility gate". Everything else in the SMP gate was re-run (below). Round 2 changed no non-test Spine Go file, so the library SMP compiles is byte-identical to round 1's. |
| 19 (overlay only) | **Expected handoff state**, not a defect. The overlay was re-extracted from the appendix above (identical to the round-1 overlay file) and re-run: 4/4 PASS under `-race`, and inside the testgate run. SMP TASK-008 commits it. |

#### Review round 2 gates (Spine at `7e41c15`)

| Gate | Command | Result |
|---|---|---|
| Boundary lint | `./dev/lint-boundaries.sh` | OK |
| Build | `make build` | OK |
| Embed smoketest | `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK ("library surface is sound") |
| Unit | `go test -p 4 -count=1 ./...` | exit 0; 38 ok, 2 without test files, 0 FAIL |
| Vet | `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| Race | `go test -race -p 4 -count=1 ./adapters/git/... ./core/engine/` (the touched packages) | all ok |
| Integration | `SPINE_TEST_DATABASE_URL=.../s002_r2it go test -tags integration -count=1 -p 1 ./testutil/ ./adapters/projection/ ./core/engine/ ./core/artifact/ ./core/workflow/ ./core/divergence/ ./adapters/git/... ./adapters/repository/` | 9 packages ok; 1222 PASS, 0 SKIP, 0 FAIL |
| CI scenario job | `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (`s002_r2scen`) | 19 PASS, 0 SKIP |
| git 2.39.5 | the `git-floor` job's two steps (AC 13 row above) | all ok, 0 SKIP |
| Mutations | `ValidateShortBranchName` → `ValidateBranchName` in each of the three guards, one at a time (`go test -overlay`) | each fails only its own test (R2-1) |
| golangci-lint | `golangci-lint run ./adapters/git/... ./adapters/repository/ ./core/artifact/ ./core/engine/ ./service/cmd/spine/ ./testutil/`, branch vs exported `main` (issues compared without line numbers) | 0 new issues (80 vs 85; the 5 fewer are gofmt fixes in touched files) |
| gosec | same, `--enable-only=gosec` | 0 issues (`main`: 0) |
| gofmt | `gofmt -l` on every `.go` file changed on the branch | empty |
| codex | `codex review --base main` (at `7e41c15`) | "No actionable regressions were identified in the changes relative to the supplied merge base." No finding was rejected. |
| SMP gate | SMP `7d4e7cc9`, `../spine` = this worktree, `GOFLAGS=-mod=readonly` | `go build ./...`, `go vet ./...` and `go vet -tags integration ./...` OK. The 38 SMP packages that depend on a Spine package changed on the branch (`go list -test -deps`, `gitclone` included) pass `go test -p 4 -count=1` (37 ok, 1 without test files). The named five pass with `-tags integration`. `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -allow-skip 'TEST_PROVISIONING_DSN unset' -- -race -p 4 -count=1 -timeout 20m -overlay <AC 19 overlay> ./internal/customer/spinecore/ ./internal/customer/spineembedded/ ./internal/customer/runner/ ./internal/customer/executionreadiness/` (`s002_r2smpspine`, `s002_r2smpplatform`, migrated with `dev/apply-migrations`) gives **PASS: 1541 passed, 0 failed, 4 allowed skips**. The `projectionwriter` platform-DB testgate gives **PASS: 8 passed, 2 allowed skips**. The AC 19 test run directly with `-race` passes 4/4. The SMP checkout stayed clean. |

All `s002_r2*` databases were dropped and no container was left running. SMP-reachable deltas of
this round: none (only Spine tests and docs changed).

### Integration (wave 2 merge) (2026-10-08)

Integration branch `init/INIT-023-wave2-integration` (worktree `i23/s-int2/spine`), cut from Spine
local `main` `ee3c249`. Merge order, each `git merge --no-ff` and built and tested before the next:
TASK-002 `b674b90`, TASK-016 `350fe87`, TASK-004 `50b9c8e`, TASK-009 `8925de9`, TASK-007 `2aca054`,
TASK-011 `c90c049`, TASK-010 `1e63692`, TASK-013 `85bf4b3`. Coordination commits: `3b88fb1`
(TreeEntryType joins the ref grammar), `0c26c91` (step-entry policy through the shared pool
constructor), `9e43640` (a refused signing secret holds its deliveries), `d61359d` (security model:
skills are a step-entry boundary), `1291e74` (codex round 1). Nothing was pushed; `main` and the task
branches are unchanged.

**This task in the integration.** Merged first (`b674b90`, before wave 2's other branches).

- *With TASK-016:* `architecture/git-integration.md` §6.4 kept this task's exact-resolution block
  and TASK-016's enforced-namespace paragraph. The "Git ref arguments" residuals in
  `KNOWN-LIMITATIONS.md` and the `CLIClient` doc now say that TASK-016 rule 0b refuses created or
  updated doubly-qualified and `HEAD`-component refs, rather than that it must. D12's
  recommendation to also refuse object-id-named refs was not adopted by TASK-016; it stays a
  documented residual (listed and deleted per the upgrade notes).
- *With TASK-011 (semantic, `3b88fb1`):* TASK-011's new `CLIClient.TreeEntryType` handed git the
  ref as given (ValidateRef only, no `--end-of-options`), so a pushed `refs/tags/<branch>` or
  `refs/<branch>` answered for a bare branch name. It is now a Rev-class argument (checkRev,
  resolveRev, `--end-of-options`); the trailing `--` was dropped because after `--end-of-options`
  `ls-tree` reads it as a pathspec (checked on git 2.54 and, in the git-floor run, 2.39.5). The
  grammar table lists the method. Tests: `TestRefGrammar_TreeEntryType*` (argv per ref class,
  refusal before exec, shadowing by `refs/tags/<n>` and `refs/<n>`, which failed on the pre-fix code).
- *With TASK-004:* `WriteAndStageFile` keeps TASK-004's `--literal-pathspecs … --` (its §7). Its new
  `git.TrackedInHEAD` is a package-level `ls-tree … HEAD -- <path>` with a literal `HEAD`, not a
  grammar input; left as is (`HEAD` resolves to the gitdir's HEAD before any `refs/tags/HEAD`).
- *With TASK-013:* `.github/workflows/ci.yml`: the `git-floor` job follows TASK-013's new step in
  `scenario-git-http`. `serve_cmd.go` still checks the git floor first.
- *Codex round 1 (P2, `1291e74`):* on macOS `/usr/bin/git` is Apple's xcrun launcher, which writes
  `$TMPDIR/xcrun_db` when its default cache is not writable, so `TestEnterBranch_RefusesWithoutLeftovers`
  and `TestRefShadow_RemoteTrackingBranchIsNeverGuessed` failed in a sandbox. `assertEmptyDir`
  ignores only `xcrun_db*` (`TestLeftoverEntries_IgnoresOnlyTheXcrunCache`); reproduced with a git
  shim before and after.

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

**This task.** Finding 4 (a bare name that is both a branch and a tag resolves to the branch;
TASK-016 made tags operator-only, so the branch is now the less trusted name) is documented and
left as a follow-up owned here; finding 7's TASK-002 half (the object-id-named-refs
"recommendation" and `TreeEntryType` missing from the Rev-class lists) is fixed in the
CHANGELOG.

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

### Status after GitHub CI (2026-11-04)

- **2026-11-04: Completed.** Spine `main` was pushed on 2026-11-04 with the owner's approval. GitHub
  CI run 41875102030 at `main` `ce0b9c9e` (the INIT-024 closing commit `fd35f802` plus INIT-024
  TASK-010's git-identity fix) passed all 8 jobs, among them the `git-floor` job on the shipped git
  2.39.x in the pinned `golang:1.26.10-bookworm` image: AC 13's CI clause, the last open item. AC
  14's scenario suite keeps review round 1's reading (the failures are `main`'s pre-existing set),
  AC 15's run is recorded in these notes (no PR was opened), and the owner questions stay at their
  recorded defaults.
