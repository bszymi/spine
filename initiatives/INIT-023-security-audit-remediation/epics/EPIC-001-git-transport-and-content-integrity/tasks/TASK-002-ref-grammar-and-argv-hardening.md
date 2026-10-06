---
id: TASK-002
type: Task
title: "One ref grammar for the git CLI client: qualified branch arguments, validated refs, --end-of-options"
status: Pending
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: bugfix
created: 2026-10-06
last_updated: 2026-10-06
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

