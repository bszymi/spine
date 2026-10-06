---
id: EPIC-001
type: Epic
title: Git Transport and Content Integrity
status: Pending
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
owner: bszymi
created: 2026-10-06
last_updated: 2026-10-06
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/initiative.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /architecture/git-integration.md
  - type: related_to
    target: smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/epic.md
---

# EPIC-001 — Git Transport and Content Integrity

---

## 1. Purpose

Make the git surface trustworthy:

- **The pre-receive gate evaluates the ref git actually applies** (F1, the P1).
- **One ref grammar for the git CLI client.** Branch-typed arguments are qualified, every ref
  argument is validated and passed after `--end-of-options`, and dormant `default_branch`
  values are validated (F10 client half, P3-32, P3-36).
- **The standalone gateway admits pushes by ref class:** run, plan, divergence, init and
  non-branch refs (F2 push half, F10 gateway half).
- **Blob reads are bounded and listings are NUL-delimited** (F4, P3-9).
- **Worktree writes are confined** against symlinked path components (F5).

---

## 2. Scope

### In scope

- `service/internal/githttp` pre-receive parsing, and the `core/branchprotect` defence-in-depth
  check (TASK-001).
- Argv construction and ref handling in `adapters/git` (`cli.go`, `branchwrite.go`), and the
  engine and repository call sites that pass refs (TASK-002).
- Bounded reads and `-z` listings in `adapters/git`, with skip-and-record in `core/artifact`,
  `core/workflow` and `adapters/projection` (TASK-003).
- The new `core/worktreefs` package and every worktree write sink: artifact, workflow, renumber
  and the merge cascade (TASK-004).
- The new `core/refadmit` package, wired into the standalone pre-receive and `handlers_git.go`
  (TASK-016).

### Out of scope

- SMP's own gateway (`internal/customer/githttp`) and plumbing (`internal/customer/spinecore`,
  `gitclone`). Those are SMP EPIC-004 TASK-001, -004, -005 and -007.
- Binding the merge to a commit (EPIC-002 TASK-005). This epic decides *who may push* and
  *how refs are named*, not *what merges*.
- Git-endpoint authentication (trusted CIDRs, dev mode). That is EPIC-004 TASK-015.

---

## 3. Task Inventory

| ID | Title | Findings | Wave | Blocked by | Audit PoCs → regression tests | SMP counterpart |
|---|---|---|---|---|---|---|
| TASK-001 | Reject NUL/control-byte and invalid refs on every pre-receive command line | F1 | 1 | — | `skeptic_nul`, `verify-r1-0` | SMP TASK-001 (its own gateway, parity); SMP TASK-012 (pin test) |
| TASK-002 | One ref grammar for the git CLI client: qualified branch arguments, validated refs, `--end-of-options` | F10 (client), P3-32, P3-36 | 2 | — (reuses TASK-001's grammar) | `gap0-ref-namespace-shadow`, `verify-r2-0`, `gap3-multi-repo-code-repo` | SMP TASK-008 adopts; SMP TASK-005 fixes SMP's own plumbing |
| TASK-004 | Confine worktree writes against symlinked path components and `.git/` | F5 | 2 | — | `git`, `verify-r1-8`, `parsing` | SMP TASK-008 adopts; SMP TASK-004 fixes SMP's own `Commit` |
| TASK-016 | Ref-class push admission in the standalone git gateway (run, plan, divergence, init and non-branch refs) | F2 (push), F10 (gateway) | 2 | — (after TASK-001 by preference) | `standalone-service`, `verify-r2-0` | SMP TASK-007 mirrors the rule; SMP TASK-012 differential test |
| TASK-003 | Bounded blob reads and NUL-delimited `Diff`/`ListFiles` in the git CLI client | F4, P3-9 | 3 | TASK-002; **SMP TASK-003 on SMP `main`** | `verify-r1-9`, `skeptic-bigblob`, `parsing` | SMP TASK-003 (precondition); SMP TASK-008 adopts; SMP TASK-004 bounds SMP's own reads |

The PoC directories are under the audit's `poc/`, which is held out of tree (initiative §1).
Each task commits the scaled-down regression test it derives from them.

---

## 4. Key Work Areas

- **TASK-001 — the P1, small and first.**
  - `parseRefUpdates` validates **every** command line, not only the first. A ref with a NUL or
    any other control byte, or one that fails git's refname rules for a fully qualified ref,
    refuses the whole push as malformed before branch protection runs.
  - The grammar lives in `adapters/git/refname`, and an `AgreesWithGit` corpus checks it
    against real git.
  - `core/branchprotect.Policy.Evaluate` also denies a branch name that holds a control byte.
    That is defence in depth for every caller, including SMP's gate after a bump.
  - No migration, no configuration and no API change.
- **TASK-002 — one ref grammar in the client.** There are four argument classes:
  - branches, as `refs/heads/<n>`;
  - fully qualified refs;
  - full object ids;
  - rev syntax, through `ValidateRef`, whose accept set does not change.

  Every ref argument comes after `--end-of-options`. `Checkout` uses `git switch --no-guess`.
  Pushes use explicit `src:dst` refspecs. A git version floor applies (`CheckGitVersion`), and a
  repository's `default_branch` is validated (P3-36, dormant). The `git.GitClient` method set
  does not change, so SMP compiles unchanged.
- **TASK-003 — bounded reads and `-z`.**
  - `ReadFile` probes the blob size, then reads with a bound (`ErrBlobTooLarge`).
  - Every other command's stdout is capped, and git's own allocation is limited.
  - The defaults are 4 MiB per blob and 16 MiB per output (owner question 5), set with
    `WithMaxBlobBytes` and `WithMaxOutputBytes`.
  - `Diff` and `ListFiles` use `-z`.
  - Discovery and projection skip and record oversized or unsafe-named files instead of failing
    or silently dropping them.
- **TASK-004 — confined writes.**
  - New stdlib-only `core/worktreefs`, on `os.Root`. A write path with a symlinked component, a
    `.git/` component or a dot-segment escape is refused.
  - Every write sink goes through it. The cascade's partially staged state is handed to TASK-012
    for cleanup.
  - `KNOWN-LIMITATIONS.md` is corrected.
- **TASK-016 — push admission by ref class.**
  - New `core/refadmit` (`Classify`, `RunReader`, `Evaluator`), wired into the standalone
    pre-receive.
  - A reserved branch (`spine/run`, `spine/plan`, divergence, init) admits only the actor bound
    to the latest execution of the run's current step. Operators are exempt. Deletes are
    refused.
  - Refs outside `refs/heads/` are operator-only.
  - The rule must equal SMP TASK-007's (owner question 3).

---

## 5. Primary Outputs

- `adapters/git/refname` (new): the one strict refname grammar, with its git-agreement corpus.
- `core/worktreefs` (new) and `core/refadmit` (new).
- Hardened `adapters/git` (`cli.go`, `branchwrite.go`), `service/internal/githttp` and
  `service/internal/gateway/handlers_git.go`.
- Committed regression tests from the PoCs listed in §3.
- Corrected `KNOWN-LIMITATIONS.md`, `architecture/git-integration.md` and
  `docs/integration-guide.md`. A CHANGELOG upgrade note tells operators to list and delete
  pre-existing shadow refs.

---

## 6. Acceptance Criteria

- **TASK-001 merged first,** as one PR with no migration, configuration or API change. The F1
  PoCs are refused as malformed pushes, and no `branch_protection.override` path is taken.
- **One grammar.** Exactly one refname grammar exists (`adapters/git/refname`). The pre-receive
  parser and every `CLIClient` ref argument use it.
- **No unchecked ref reaches git.** No `CLIClient` method passes a ref or branch name to git
  without validation and `--end-of-options`. Branch-typed arguments are qualified, and push
  refspecs are explicit. A pushed `refs/tags/<run-branch>` or `refs/tags/main` changes neither
  what a governed merge reads nor whether `main` can be pushed (F10 PoCs inverted).
- **Bounded reads.**
  - No blob read buffers more than the configured limit.
  - A 256 MiB compressible blob no longer drives the read path past the limit (F4 PoC
    inverted).
  - Oversized and unsafe-named files are skipped and recorded, and listings are NUL-delimited
    (P3-9).
- **Confined writes.** Every worktree write sink goes through `core/worktreefs`. A symlinked
  final component, a symlinked directory or a `.git/` path is refused, and nothing is created
  outside the root (F5 PoCs inverted).
- **Push admission.** The standalone gateway admits reserved-branch pushes only from the bound
  actor or an operator, refuses deletes of reserved branches, and admits non-branch refs only
  from operators.
- **SMP stays green.** Each task's SMP compatibility gate passed against SMP `main`. Spine
  TASK-003 merged only after SMP TASK-003 was on SMP `main`.

---

## 7. Cross-Repo Coordination

- **SMP TASK-001** fixes F1 in SMP's own gateway in the same wave, with no Spine dependency.
  Whichever lands second aligns the grammar corpus and comments. After a bump, SMP's gate also
  gets TASK-001's `Evaluate` layer.
- **SMP TASK-003** moves SMP's run-branch diff reader onto SMP's own NUL-delimited diff. It must
  be on SMP `main` before TASK-003 merges here, otherwise the next SMP bump breaks
  `RunBranchDiffReader`.
- **SMP TASK-008** bumps `.spine-revision` to the earliest Spine `main` commit containing
  TASK-002, TASK-003 and TASK-004. It adds the boot-time git version check and SMP-side
  regression tests, and it collapses SMP TASK-004's interim read bound onto Spine's.
- **SMP TASK-005 and SMP TASK-007** qualify refs in SMP's own plumbing and add run ownership to
  SMP's gateway. SMP TASK-007 implements the same admission table as TASK-016. SMP TASK-012
  adds a differential test that keeps SMP's rule never looser than `core/refadmit`.
