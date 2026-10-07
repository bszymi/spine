---
id: TASK-001
type: Task
title: "Reject NUL/control-byte and invalid refs on every pre-receive command line"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: bugfix
created: 2026-10-06
last_updated: 2026-10-07
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-004-git-enforcement/tasks/TASK-002-pre-receive-enforcement.md
  - type: related_to
    target: /initiatives/INIT-018-branch-protection/epics/EPIC-002-policy-core/tasks/TASK-002-policy-package.md
---

# TASK-001 — Reject NUL/control-byte and invalid refs on every pre-receive command line

---

## Purpose

**Finding:** F1 (P1) in the 2026-10-06 security audit, Spine side (`F1.spine`). This is wave 1 of
INIT-023 and is kept deliberately small.

**Defect.** `parseRefUpdates` (`service/internal/githttp/prereceive.go:54-103`) strips the
`\x00<capabilities>` suffix only from the **first** command pkt-line (`if first {…}`,
`:82-90`). `git receive-pack` treats every command line the same way: the ref ends at the first
NUL (`strlen`), and anything after it is parsed as a feature list. So a NUL placed after the ref on
line 2 or later is applied by git as the literal ref (`refs/heads/main`). The gate, however,
evaluates `refs/heads/main\x00`:

- `normalizeBranch` (`core/branchprotect/policy.go:193-195`) turns it into `main\x00`.
- `path.Match("main", "main\x00")` (`core/branchprotect/config/parser.go:147`) is false.
- `Evaluate` returns `Allow` with `no_matching_rule` (`policy.go:92-98`).
- The handler hands the push to CGI (`service/internal/githttp/handler.go:398-476`) and emits
  **no** `branch_protection.override` event, because the allow was not an override
  (`handler.go:435-437`).

**Who.**
- Any authenticated actor, of any role, once `SPINE_GIT_RECEIVE_PACK_ENABLED=true` (default off,
  `handler.go:204-215`). `validateGitAuth` checks the token, not the role
  (`service/internal/gateway/handlers_git.go:104-131`).
- An unauthenticated client when the gateway runs in `devMode`, which bypasses git auth
  (`handlers_git.go:104`; the bypass itself is P3-30, TASK-015).

> **Design-check note (2026-10-06): citation.** `handlers_git.go:104-131` is the auth block inside
> `handleGit` (`:93-133`), which calls `validateGitAuth` at `:123`. The function itself is at
> `:405-445`. The claim stands: it validates the token and returns whatever actor the token maps
> to, whatever that actor's role.

**Impact.** A contributor:
- writes unreviewed commits straight to a literal-protected branch (`main: [no-direct-write]`);
- deletes any literal `no-delete` branch except the checked-out HEAD (git's own
  `receive.denyDeleteCurrent` still saves `main`);
- leaves no audit event;
- can replace `.spine/branch-protection.yaml` with `rules: []` in the same push. Projection then
  installs that as live policy, a durable governance takeover (ADR-009 §5).

Wildcard rules (`release/*`) are **not** bypassed, because `*` matches the NUL byte.

> **Design-check note (2026-10-06): standalone `main` needs a qualifier.**
> - **Why it differs.** Spine's standalone repository is **non-bare**: `init-repo` runs
>   `git init -b main`, and no production code creates a bare repo (only
>   `service/scenariotest/harness/repo.go:151` does). `ensureReceivePackConfig`
>   (`handler.go:683-686`) sets only `http.receivepack` and `receive.advertisePushOptions`, not
>   `receive.denyCurrentBranch`. git's default `refuse` therefore rejects an update to the
>   **checked-out** branch, even after the gate has allowed it.
> - **Reproduced at `61994fe`** with a non-bare repo and `main` checked out:
>   - the NUL push returned `ng refs/heads/main branch is currently checked out`;
>   - `main` did not move;
>   - the line-1 decoy branch *was* created.
> - **Standalone exposure:**
>   - The direct write to `main` lands only while the working tree's HEAD is on another branch.
>     So does the `rules: []` takeover through `main`. HEAD does move off `main` during engine
>     work (F9).
>   - Every other literal-protected branch (`develop: [no-direct-write]`) and every non-HEAD
>     delete are exposed unconditionally.
> - **SMP** serves bare per-workspace repos, as do the audit PoCs, so the Impact above applies to
>   SMP in full.
> - **Effect on this task:** none. `denyCurrentBranch` is incidental git behaviour, not a
>   control, and this task does not rely on it.

**Reproduced at `61994fe`** while designing this task. The audit PoCs
(`poc/verify-r1-0/nulref_verify_test.go`, `poc/governance/nul_ref_bypass_test.go`) ran through the
real `git-http-backend` CGI:
- the control push to `main` is denied with `no-direct-write`;
- the NUL push returns `ok refs/heads/main`, and `main` advances to the attacker commit;
- a no-delete `release`/`develop` branch is deleted;
- 0 override events.

**Second differential found during design (same parser, latent).** `RefUpdate.IsDelete`
(`prereceive.go:24-26`) recognises only the 40-zero SHA-1 null id. In a SHA-256 repository, a delete
sends 64 zeros. The gate therefore classifies it as `OpDirectWrite`, and a no-delete-only rule
lets it through. Verified at `61994fe`: a contributor deleted `develop: [no-delete]` in a
`--object-format=sha256` bare repo.

Spine's own `init-repo` creates SHA-1 repositories (`git init -b main`,
`service/internal/cli/initrepo.go:43`). A workspace cloned from a SHA-256 upstream, or created
under `init.defaultObjectFormat=sha256`, is still exposed.

**SMP.** SMP's gateway carries a byte-identical copy of this parser
(`internal/customer/githttp/prereceive.go:54-103`). That copy is fixed by SMP INIT-027/EPIC-004
TASK-001, not here. See Design §5 for how this task's `Evaluate` guard also protects SMP after the
`.spine-revision` bump.

## Deliverable

Branch `INIT-023/EPIC-001/TASK-001-pre-receive-refname-validation`, one small PR. Non-test Go is
limited to four files:
- `adapters/git/refname/qualified.go` (new);
- `service/internal/githttp/prereceive.go`;
- `core/branchprotect/policy.go`;
- no change to `handler.go`.

It also includes the regression and unit tests listed in the Acceptance Criteria, and three
documentation touches.

> **Design-check note (2026-10-06): deliverable count.**
> - **Non-test Go files.** The list names **three** non-test Go files plus "no change to
>   `handler.go`", so the count is three, not four.
> - **Documentation touches.** The three were never enumerated: the file was cut off before the
>   Acceptance Criteria. They are now fixed in AC 12:
>   - `docs/git-push-guide.md` §5;
>   - `KNOWN-LIMITATIONS.md` → Security Notes;
>   - the `CHANGELOG.md` "Unreleased" staging section.
> - **Prototype.** The prototype under
>   `tmp/init027-orchestration/spine-init023/prototypes/TASK-001/` (`overlay.json`) implements
>   exactly these three non-test files. It also carries the five test files named in the ACs.

## Design

### 0. Invariant

Every ref update the gate evaluates must be **byte-identical to the ref git will update**. A
command line that git could read differently from the gate is refused before any policy decision.
This covers a NUL or control byte, a name git would refuse, and an object id git would not parse.
`Policy.Evaluate` independently refuses control-byte branch names, so a future parser slip fails
closed instead of becoming `Allow(no_matching_rule)`.

### 1. Strict refname grammar — `adapters/git/refname/qualified.go` (new)

```go
// ValidateQualified enforces git's refname grammar on a fully-qualified ref
// exactly as `git receive-pack` applies it to a pushed ref.
func ValidateQualified(ref string) error
```

The function mirrors `check_refname_format` as receive-pack applies it. The name must start with
`refs/`, and the remainder is checked with `REFNAME_ALLOW_ONELEVEL`. git itself refuses to *create*
a one-level ref, but allows deleting one. Rejected:
- missing `refs/` prefix;
- any byte `< 0x20` or `0x7f` (including NUL, CR, LF, TAB), space, or any of `~ ^ : ? * [ \`;
- `..` or `@{` anywhere;
- an empty component (`//`, trailing `/`, bare `refs/`);
- a component that starts with `.` or ends with `.lock`;
- a trailing `.`.

The check is **byte-wise**, so invalid UTF-8 cannot hide a byte. Bytes `>= 0x80` are accepted, as
git accepts them. A leading `-` inside a component (`refs/heads/-x`) is accepted: git accepts it,
and the `refs/` prefix rules out flag injection.

The function returns `*domain.SpineError{Code: ErrInvalidParams}`, like the existing
`refname.Validate` (`refname.go:31-63`). Its messages quote the ref with `%q`, so control bytes
never reach logs or side-band output raw.

This is an additive exported function in the package whose doc already claims "the
security-sensitive rules live in one place" (`refname.go:1-9`). `refname.Validate` and
`git.ValidateRef` (`adapters/git/credentials.go:61`) are **not** changed here. Converging them onto
this grammar is TASK-002's job ("one ref grammar"); see §6.

> **Design-check note (2026-10-06): where the grammar differs from receive-pack, and the wire
> text.**
> - **Two refs the gate accepts but git refuses.** `ValidateQualified` checks the **whole** name,
>   as `git check-ref-format --allow-onelevel` does (`TestValidateQualified_AgreesWithGit` pins
>   exactly that). receive-pack runs `check_refname_format` on the **remainder** after `refs/`,
>   with `REFNAME_ALLOW_ONELEVEL` only for deletes. It therefore also refuses:
>   - `refs/@` (remainder `@`);
>   - a one-level create or update such as `refs/foo`.
> - **Why that is safe.** On those two shapes the gate is looser than git, and git refuses them
>   itself ("funny refname"). The gate is never looser on a ref git would apply, so the invariant
>   in §0 holds. No code change is needed. The doc comment should name both exceptions rather than
>   say "exactly as `git receive-pack` applies it".
> - **The "§6" cross-reference.** It pointed into the truncated part of the file and is now §6
>   below.
> - **Wire text.** `domain.SpineError.Error()` prefixes the code, so the wire text reads
>   `malformed push: command 2: invalid ref name: invalid_params: ref "refs/heads/main\x00" …`
>   (seen in the PoC run). This is cosmetic. Unwrapping to the message is optional and must not
>   drop the `%q` quoting.

### 2. Parser — `service/internal/githttp/prereceive.go`

Change `parseRefUpdates` (`:54-103`) as follows:

1. **Count commands, don't flag the first.** Replace `first := true` (`:57`) with a 1-based command
   index `n`, incremented per pkt-line.
2. **Strip exactly one trailing LF.** Use `strings.TrimSuffix(string(payload), "\n")`, applied
   before the NUL split. This replaces `strings.TrimRight(line, "\n")` (`:91`) and mirrors git's
   `PACKET_READ_CHOMP_NEWLINE`. A second LF stays in the ref and is rejected.
3. **Split capabilities off line 1 only** (`if n == 1`). This keeps the existing `:82-90` body. On
   every later line a NUL stays in the ref, and step 5 rejects it. git `send-pack` only ever sends
   capabilities on the first command, which the real-client test in AC 7 confirms.
4. **Shape errors keep the 3-field rule** (`:93-96`), now prefixed `command %d:`.
5. **Validate every command** with a new `validateRefUpdate(u RefUpdate) error`:
   - `OldSHA` and `NewSHA` must each be hex (either case, as git's parser accepts) of length
     40 or 64, and of equal length. Otherwise: `malformed object ids in ref update for %q`.
   - `Ref` must pass `refname.ValidateQualified`. Otherwise: `invalid ref name: …`.
   - A failure returns `command %d: <err>` from `parseRefUpdates`.

   The existing caller already turns any parse error into an all-or-nothing denial with
   `branch-protection: malformed push: <err>` and no per-ref lines (`handler.go:324-332`). So
   `handler.go` needs **no change**, and nothing after the parser sees an invalid ref.
6. **SHA-256 null id.** Add `const zeroSHA256 = zeroSHA + "000000000000000000000000"`, and make
   `IsDelete` return `u.NewSHA == zeroSHA || u.NewSHA == zeroSHA256`.

The parser's doc comment (`:36-53`) is updated to say:
- capabilities are accepted on the first command only;
- every command's object ids and ref are validated.

Its stale reference to a non-existent `readPreamble` is fixed in the same touch.

> **Design-check note (2026-10-06): parser claims checked against real git 2.54.**
>
> **Confirmed.** `GIT_TRACE_PACKET` on a two-ref `git push` shows `send-pack`:
> - sends capabilities on command 1 only;
> - sends **no trailing LF** on any command line.
>
> So step 2's `TrimSuffix` is a no-op on real traffic. It exists to mirror
> `PACKET_READ_CHOMP_NEWLINE` for hand-built bodies.
>
> **Other ways for the gate and git to read the stream differently.** Each was checked and
> fails closed, before and after this task:
> - **`shallow <oid>` lines** (a push from a shallow clone): fewer than 3 fields, so the push is
>   refused.
> - **A `push-cert\0<caps>` frame:** 1 field, so the push is refused.
> - **`0001`/`0002` delim packets:** the length is below 4, so the push is refused. git would end
>   the command list there.
> - **A `Content-Encoding: gzip` body:** git-http-backend would inflate it. The gate would read
>   `1f 8b` as the pkt length, which is not hex, so the push is refused.
> - **A lowercase `post` method:** `IsReceivePack` is false. `isReadOnly` is false for a
>   non-upload-pack POST, so the handler refuses the request. git-http-backend `strcmp`s the method
>   anyway.
> - **A push-options capability on a later line:** git honours a feature list on any command line.
>   Once a NUL is refused on lines 2+, the gate and git agree on whether an options section
>   follows.
>
> AC 1 pins the first three in the parser table so they stay refused.

### 3. Defence in depth — `core/branchprotect/policy.go`

In `Evaluate` (`:51-108`), insert a guard immediately after the operation-kind switch (`:57-62`)
and **before** the governed-merge early return (`:67`) and the non-branch-ref allow (`:80-85`):

```go
if i := indexControlByte(req.Branch); i >= 0 {
    return DecisionDeny, nil, fmt.Errorf(
        "branchprotect: branch %q contains control byte 0x%02x (malformed ref from caller)",
        req.Branch, req.Branch[i])
}
```

Here `indexControlByte` returns the first byte `< 0x20 || == 0x7f`, or -1. The guard sits before
every allow path, so it covers every kind and every rule set, including `rules: []` and tags.

The shape is `Deny + error`, the same contract as an unclassified kind (`:57-62`) and a rule-source
failure (`:87-90`). All callers already treat an error as a refusal:

| Caller | Effect of the error |
|---|---|
| Git push gate, standalone (`handler.go:416-422`) | Push refused |
| Git push gate, SMP | Push refused |
| Artifact service (`core/artifact/service.go:419-430`) | `ErrInternal` |
| Engine merge (`core/engine/merge.go:545-553`) | `ErrInternal` |
| Divergence (`core/divergence/service.go:63-67`) | `ErrInternal` |

The guard is reachable only through a caller bug:
- The API write path builds its branch from the stored `run.BranchName`
  (`service/internal/gateway/handlers_artifacts.go:421-437`), never from raw client input.
- The engine merges into the constant `authoritativeBranch = "main"` (`core/engine/merge.go:23`).
- Divergence branch names are generated.

`core/branchprotect` gains **no** import. The check is a byte loop, so there is no new
`core → adapters` crossing for the ADR-017 lint.

> **Design-check note (2026-10-06): doc contract and ordering.**
> - **Doc comment.** The `Policy` interface doc (`policy.go:12-17`) says a non-nil error "signals
>   an infrastructure failure (rule source unavailable)". The new guard adds a second meaning,
>   *malformed request (caller bug)*. Update that comment, and the decision-matrix comment on
>   `Evaluate` (`:36-50`), in the same change. Embedders read them.
> - **Guard ordering.** The guard must stay before the non-branch allow (`:80-85`). Otherwise
>   `refs/tags/v1\x00` (a non-`refs/heads/` ref) would still get `Allow`.
> - **Mutation M3 confirmed.** Disabling the guard fails `TestEvaluate_ControlByteBranchFailsClosed`.

### 4. Data, format, migration

- No schema, migration, configuration, environment-variable or event-catalog change.
- No change to the push wire response for accepted pushes.
- **Message-text change:** parse-error denials now read
  `branch-protection: malformed push: command N: …`. The documented prefix
  (`docs/git-push-guide.md:115`) is unchanged.
- **Public Go API:** one new exported function, `refname.ValidateQualified`. One behavioural
  contract change: `Policy.Evaluate` now returns `(Deny, nil, err)` for a control-byte `Branch`.

### 5. Backward compatibility and SMP

**Well-formed pushes are unchanged.** AC 7 proves this before and after with a real `git push`
client: a multi-ref push, a push with `-o` push-options, a delete, and a protected-`main` denial.
All existing tests pass un *[designer's text truncated here]*

> **Design-check note (2026-10-06): the designer's text ends at the truncation marker above.** The file was cut off
> mid-sentence at 11,330 bytes (last written 19:22). The rest of §5 and every later section were
> never written: Decisions, Owner questions, Acceptance Criteria, Out of scope and Implementation
> Notes. **Everything from here to the end of the file is design-check text.** It is built from:
> - the designer's prototype (`tmp/init027-orchestration/spine-init023/prototypes/TASK-001/`);
> - the sibling SMP INIT-027/EPIC-004 TASK-001 (`batch7/E4-TASK-001-nul-ref-push-gate.md`), kept at
>   parity;
> - the runs recorded against each AC.

**Existing tests.** The designer's sentence, completed: all existing tests pass unchanged. Verified on
2026-10-06 with `prototypes/TASK-001/overlay.json` on `61994fe` (git 2.54, go 1.26.5):
- Spine `go test ./...` reports 40 packages `ok`.
- `go build ./...` and `go vet` on the touched packages are clean.
- `go test -tags scenario -run TestGitHTTP ./service/scenariotest/scenarios/` compiles, but every
  scenario **skips** locally without `SPINE_TEST_DATABASE_URL`. That is 15 in
  `git_http_serve_test.go` and 19 in `scenarios/` overall. AC 9 makes CI run them.

**What changes on purpose.**
- **Refused, previously applied to a different ref.** A push the gate used to admit, which git then
  applied to a *different* ref (a NUL on line 2+), is now refused.
- **Refused earlier, previously refused by git.** A push the gate used to admit, which git then
  refused itself, is now refused by the gate. This covers bare names, `..`, `@{`, `.lock`, control
  bytes and malformed ids. Its denial moves from git's "funny refname" to the gate's `malformed push`.
- **Nothing git would apply is lost.** No push that git would have applied as written is newly
  refused (AC 5, AC 7).

**Shapes that were already refused stay refused**, only with new message text:
- `shallow` lines;
- `push-cert`;
- `0001`/`0002` packets;
- gzip bodies.

**SMP (embedded).**
- **Parser.** SMP does not import `service/`, so the parser change does not reach it. SMP's own copy
  (`internal/customer/githttp/prereceive.go`, byte-identical at `80995559`) is fixed by SMP
  INIT-027/EPIC-004 TASK-001, which ships independently and needs no `.spine-revision` bump.
- **Evaluate guard.** SMP passes `u.Ref` raw to `Policy.Evaluate`
  (`internal/customer/githttp/handler.go:574`). After the bump, the guard therefore closes F1 in SMP
  even if SMP's parser were left unfixed. Verified on 2026-10-06 with only the Spine prototype
  applied:
  - `poc/skeptic_nul/smp_nul_ref_test.go` and `poc/verify-r1-0/nulref_verify_test.go`, overlaid on
    SMP's `githttp`, no longer reproduce:
    - `main` stays put;
    - `release` survives;
    - the response is `branch-protection: evaluation error on main\x00`.
  - These SMP suites are green with the guard:
    - `go test ./internal/customer/... ./cmd/customer-app/... ./internal/engine/...`;
    - `-tags integration ./internal/customer/githttp/...`.
- **Verification trap: the local `vendor/` directory.**
  - **The problem.** SMP has an untracked, gitignored `vendor/` (today it matches `61994fe`: 170
    files, 0 diffs). A local `go test -overlay` that targets `../spine/...` paths is silently
    shadowed by `vendor/`, so it proves nothing.
  - **The designer's runs.** The `overlay-smp.json` runs were vacuous for this reason. Under them the
    same PoC still reproduced the bypass.
  - **How to verify.** Either use `-mod=readonly`, which resolves `../spine` through the `replace`
    and never rewrites `go.mod`, or overlay the `vendor/` paths.
  - **CI** has no `vendor/` and is not affected.
  - **After the bump,** a developer who keeps a local `vendor/` must re-run `go mod vendor`.
    Otherwise their local runs keep testing the old Spine.
- **Interim cosmetic effect.** If the bump lands before SMP TASK-001, SMP's denial echoes the raw
  NUL in its side-band text and `ng` lines (`evaluation error on main\x00`,
  `ng refs/heads/main\x00 …`). This is harmless:
  - the text goes only to the attacker's own client;
  - structured logs escape it;
  - SMP TASK-001 removes it.
- **Older runners and clients: no change.**
  - Only the git CLI pushes to either gateway. Neither module depends on go-git, JGit or libgit2.
  - git, go-git, JGit and libgit2 all send capabilities on the first command only.
- **Untouched:** stored data, the wire format for accepted pushes, configuration and migrations.

### 6. Residual differential: case- or normalization-insensitive repository storage (design check)

> **Design-check note (2026-10-06): a bypass this fix does not close.**
>
> **Setup.** Reproduced against the prototype on macOS APFS (case- and normalization-insensitive),
> git 2.54:
> - contributor actor, no override;
> - rule `main: [no-delete, no-direct-write]`;
> - `main` **packed**, as it is in any `git clone --bare` repo and after `git gc`/`pack-refs`.
>
> **Findings.**
> - **`refs/heads/MAIN`, plain client.** A plain `git push origin HEAD:refs/heads/MAIN`, with no
>   hand-crafted body, returned `[new branch]`:
>   - the policy saw `MAIN`, no rule matched, and the push was allowed;
>   - git wrote a loose `refs/heads/MAIN` file, which on this storage *is* `refs/heads/main`;
>   - `git rev-parse refs/heads/main` now returns the attacker commit;
>   - no override event was emitted.
> - **`refs/HEADS/main`** does the same through the **non-branch allow** (`policy.go:80-85`). Folding
>   rule matching alone would therefore not close it.
> - **Non-ASCII aliases.** APFS also aliases the Kelvin sign (U+212A → `k`) and NFD/NFC forms
>   (`é` ↔ `é`). ASCII-only folding is therefore incomplete.
> - **Loose `main` is refused.** With a **loose** `main`, git 2.54 refuses
>   (`cannot lock ref 'refs/heads/MAIN': unable to resolve reference`).
> - **TASK-016's premise is false for packed refs.** That loose case is the only one TASK-016's
>   out-of-scope line covers ("not exploitable on git 2.54, which has `core.ignorecase` collision
>   refusal"). The premise fails for packed refs, even with `core.ignorecase=true`, which the bare
>   clone had set.
>
> **Why this is not F1.** The gate evaluates the same bytes git writes, so §0's invariant holds; the
> filesystem then aliases two names.
>
> **Exposure is environmental.**
> - **Safe:** the shipped compose keeps the repo on the named volume `repo:/repo` (Linux,
>   case-sensitive).
> - **Exposed:**
>   - a native macOS or Windows `spine serve` with `SPINE_GIT_RECEIVE_PACK_ENABLED=true`;
>   - a bind mount from such a host;
>   - an SMB-backed or other case-insensitive volume.
> - **SMP** has the same exposure (SMP TASK-001 Owner question 2).
>
> **Disposition:** Owner question 1. This task adds only the documentation guard in AC 12.

## Decisions

*(Design check: reconstructed from the prototype, and kept at parity with SMP TASK-001 Decisions 1-5.)*

1. **Reject a NUL on line 2+, rather than strip capabilities from every line the way git does.**
   Stripping would also close F1. It was rejected because:
   - it would accept a protocol shape no git client emits;
   - it would still trust the parser to agree with git on every other byte.

   Mutation M1 (`n >= 1`) pins this choice.
2. **Use the full git refname grammar, rather than a NUL/control-byte check only.** Every extra rule
   rejects a ref that git refuses anyway. It also hands TASK-002 and TASK-016 canonical `refs/…`
   input.
3. **Add a policy-level guard as well as fixing the parser.** The parser is Spine's only constructor
   of `RefUpdate` from wire bytes. `Evaluate`, however, is the shared boundary that SMP and every
   engine caller reach. It is also the layer that protects SMP independently of SMP's own parser
   (verified, §5). Mutation M2 (parser validation skipped) shows `main` still held by the guard.
4. **Fix the SHA-256 null id here.** It is the same parser and one constant, and it is the same
   "classify differently from git" mechanism.
5. **Treat case-insensitive storage as a follow-up, not part of this P1** (Owner question 1). A
   correct fix needs two things, and neither is small:
   - Unicode case folding plus normalization, or a storage probe that fails closed;
   - a test seam, because Spine's own suite runs on APFS.

   F1 does not depend on it.

## Owner questions

*(Design check. Any questions the designer wrote were lost to the truncation. The design works
without answers, using the stated defaults.)*

1. **Case- and normalization-insensitive repository storage (§6).**
   - **Current ownership: none.**
     - Nothing in INIT-023 owns this today.
     - TASK-016 lists "a case-folding audit of `branchprotect` rule matching" as out of scope, on a
       premise §6 disproves.
     - SMP TASK-001 Owner question 2 defers SMP's side to a follow-up.
   - ***Default.***
     - This task documents the requirement (AC 12).
     - A **new INIT-023 EPIC-001 follow-up task**, paired with SMP's follow-up, designs
       fail-closed handling. It chooses between:
       - a per-repo case-sensitivity probe that refuses receive-pack, with an explicit opt-out for
         tests;
       - fold-plus-NFC matching in `Evaluate`, covering both the rule match and the `refs/heads/`
         prefix test.
     - TASK-016's out-of-scope line is corrected to point at that task.
   - ***Alternative.*** Add ASCII case folding to `Evaluate` in this task (about 5 lines, fail-closed).
     - This closes `MAIN`, `Main` and `refs/HEADS/main` for ASCII rules.
     - It leaves Kelvin-sign and NFD variants open.
   - ***Ruling (orchestrator, 2026-10-07): default taken, pending the owner.** Implemented as the
     AC 12 documentation guard, the follow-up recorded in the EPIC-001 epic's Out of scope, and
     TASK-016's Out-of-scope bullet and owner question 5 corrected to point at it.
2. **Pushes from shallow clones** (parity with SMP TASK-001 Owner question 1).
   - Today's gate refuses `shallow <oid>` lines as malformed, and this task keeps that (pinned in
     AC 1).
   - No standalone Spine client is known to push from a shallow clone.
   - *Default:* keep refusing. Any widening is designed together with TASK-016's run-branch
     admission.
   - ***Ruling (orchestrator, 2026-10-07): default taken, pending the owner.** `shallow <oid>`
     lines stay refused as malformed (two parser-table rows pin it).
3. **Audit surface for a refused malformed push** (parity with SMP TASK-001 Owner question 3).
   - *Default:* a WARN log only.
   - A new governance event type is an event-catalogue change, which does not belong in a wave-1 fix.
   - ***Ruling (orchestrator, 2026-10-07): default taken, pending the owner.** The existing
     `pre-receive parse failed` WARN is the only record; no event type was added.

## Acceptance Criteria

*(Design check: written from the designer's prototype tests.)*
- "Fails at HEAD" means the test, overlaid on `61994fe` without the production changes, fails.
- Every "verified" claim was run on 2026-10-06 with git 2.54 and go 1.26.5.

1. **Parser table** — `TestParseRefUpdates_RejectsMalformedCommandOnAnyLine`
   (`service/internal/githttp/prereceive_refvalidation_test.go`).
   - **Refused, and the error names `command N`:**
     - a NUL after the ref on line 2 or line 3, with or without trailing caps;
     - CR, DEL, TAB or space in a ref;
     - a second trailing LF;
     - a control byte before the line-1 NUL;
     - a bare name (`main`);
     - `..`, `@{`, `.lock`, or a trailing `/`;
     - a short, non-hex or mixed-length object id.
   - **Still refused** (pinned; design-check additions):
     - a leading `shallow <oid>` line;
     - a `push-cert\0<caps>` line;
     - a `0001` delim packet.
   - **Parsed:**
     - line-1 capabilities, with `report-status` present in the returned set;
     - SHA-256 ids;
     - *(design-check addition)* lines with **no** trailing LF, which is what `git send-pack`
       actually sends.
   - **Error strings:** none contains a raw NUL or CR byte.
   - **At HEAD:** the table fails.
2. **End-to-end NUL regression through the real `git-http-backend`.**
   - **Tests:**
     - `TestPreReceive_NulAfterRefOnLaterLine_DirectWriteRefused`, with variants `nul`,
       `nul+caps`, `nul+junk` and `spine-run decoy`;
     - `TestPreReceive_NulAfterRefOnLaterLine_DeleteRefused`.
   - **Expected after the fix:**
     - `main` does not move;
     - `develop` (`no-delete`) survives;
     - the line-1 decoy (`feature-x`, `spine/run/decoy` or `feature-y`) is **not** created or
       deleted;
     - the response contains `malformed push`;
     - **0** `branch_protection.override` events are emitted.
   - **Controls:** the same pushes without the NUL are denied by `no-direct-write` and `no-delete`.
   - **At HEAD:** both tests fail with `BYPASS` (verified).
3. **Audit PoCs flip.**
   - **The PoCs:** `poc/verify-r1-0/nulref_verify_test.go` and
     `poc/governance/nul_ref_bypass_test.go`.
   - **Fixed tree:** overlaid on it, they **fail** with `NOT REPRODUCED` / `expected main advanced`
     (verified).
   - **At `61994fe`:** they pass, i.e. the bypass reproduces (verified).
   - AC 2 holds the committed, inverted ports.
4. **SHA-256 null id.**
   - `TestPreReceive_SHA256NullOIDDeleteRefusedByNoDelete`:
     - in a `--object-format=sha256` bare repo, a contributor's 64-zero delete of
       `develop: [no-delete]` is denied with `no-delete`;
     - `develop` survives;
     - at HEAD the test fails (verified).
   - `TestRefUpdate_IsDeleteRecognisesBothNullOIDs` passes.
   - In CI the SHA-256 test must report **PASS, not SKIP**. The ubuntu-24.04 git supports
     `--object-format=sha256`, so a skip there fails this AC.
5. **The grammar agrees with git.**
   - `TestValidateQualified`: every corpus row gets its stated verdict, and every rejection is a
     `*domain.SpineError` with `ErrInvalidParams`.
   - `TestValidateQualified_AgreesWithGit`: for every row without a NUL, the verdict equals
     `git check-ref-format --allow-onelevel` restricted to `refs/…`.
   - **The gate is never stricter than git on a ref git applies.** The corpus therefore includes
     these accepted names:
     - `refs/heads/-x`;
     - `refs/heads/a./b`;
     - `refs/heads/@`;
     - `refs/heads/é`;
     - a name containing NBSP.
6. **Policy guard.**
   - `TestEvaluate_ControlByteBranchFailsClosed` checks the full cross-product and expects
     `(DecisionDeny, nil, non-nil error)` for every combination:
     - each of the 9 control-byte branches, including `refs/tags/v1\x00`;
     - each kind: `OpDirectWrite`, `OpDelete`, `OpGovernedMerge`;
     - each rule set: the `main` rule, bootstrap `nil`, and explicit `rules: []`.
   - `TestEvaluate_WellFormedBranchesUnchanged` keeps its pre-fix decisions.
   - `core/branchprotect`'s import list is unchanged, and `./dev/lint-boundaries.sh` passes with no
     new allowlist line.
   - *(Design-check addition.)* The `Policy` interface doc comment and the `Evaluate` doc comment
     both name the malformed-request error.
7. **Well-formed pushes are unchanged, before and after.**
   - `TestPreReceive_RealGitClientPushesUnchanged` and
     `TestPreReceive_WellFormedMultiRefPushStillApplies` pass both at `61994fe` and with the fix
     (verified).
   - The real-client test drives the real `git push` client through `Handler.ServeHTTP` and covers:
     - a 3-ref push that includes a `spine/run/*` ref;
     - `-o ci.skip`;
     - a delete of an unprotected branch;
     - a direct write to protected `main`, still denied with `no-direct-write`.
8. **Mutations are caught.** Each mutation below must make the named tests fail.
   - **M1:** `if n == 1` → `if n >= 1` (strip capabilities on every line). AC 1 and AC 2 fail
     (verified).
   - **M2:** skip `validateRefUpdate`. AC 1 and AC 2 fail on `malformed push`, while `main` still
     holds via the guard (verified). This proves the second layer.
   - **M3:** disable the `Evaluate` guard. AC 6 fails (verified).
   - **M4:** drop `zeroSHA256` from `IsDelete`. AC 4 fails.
   - **M5:** `TrimSuffix` → `TrimRight`. AC 1's "second LF kept" row fails.
9. **Existing suites are green.**
   - `go test ./...` (verified with the prototype: 40 packages `ok`).
   - `make lint`, which runs `lint-boundaries` and golangci-lint.
   - `go test -tags scenario ./service/scenariotest/...` **in CI, with a database.**
     - The `TestGitHTTP_*` push scenarios in `service/scenariotest/scenarios/git_http_serve_test.go`
       must report **PASS, not SKIP**. They cover:
       - an accepted push;
       - a policy rejection;
       - an operator override, with exactly one event;
       - a contributor override, refused;
       - `no-delete`;
       - an unused override, which emits nothing;
       - a mixed push, rejected all-or-nothing;
       - a byte-identical commit.
     - These scenarios skip locally without `SPINE_TEST_DATABASE_URL`, so a local run proves nothing
       for them.
10. **SMP compatibility (this task makes no SMP change).**
    - **Suites:**
      - with `../spine` at this task's merge commit, SMP's CI (which has no `vendor/`) is green;
      - locally, `go test -mod=readonly ./...` is green.
    - **The guard alone protects SMP:**
      - `poc/skeptic_nul/smp_nul_ref_test.go` is overlaid on SMP's `internal/customer/githttp`, with
        SMP's parser **unfixed**;
      - it no longer reproduces, which proves the guard alone protects SMP after the bump.
    - **Verified with `-mod=readonly`** for:
      - `internal/customer/...`;
      - `cmd/customer-app/...`;
      - `internal/engine/...`;
      - `-tags integration ./internal/customer/githttp/...`.
    - **Overlay rule:** any local SMP check that overlays `../spine` paths must use `-mod=readonly`
      (§5). Otherwise the check is vacuous.
11. **Diff scope.**
    - Non-test Go changes are confined to:
      - `adapters/git/refname/qualified.go` (new);
      - `service/internal/githttp/prereceive.go`;
      - `core/branchprotect/policy.go`.
    - `handler.go` is untouched.
    - None of these change: `go.mod`/`go.sum`, migrations, configuration, environment variables, or
      the event catalogue.
12. **Documentation (the three touches).**
    - **`docs/git-push-guide.md` §5.**
      - The `malformed push` row's cause lists:
        - an invalid ref name;
        - a control byte, or a NUL after a later command's ref;
        - a malformed object id;
        - a push from a shallow clone.
      - The sentence at `:117` is corrected: a malformed-push rejection carries **no** per-ref `ng`
        lines, because `handler.go:328-330` passes `nil` updates.
    - **`KNOWN-LIMITATIONS.md` → Security Notes.** States that served repositories must live on
      case- and normalization-sensitive storage, and why (§6).
    - **`CHANGELOG.md` → "Unreleased".** One entry each for:
      - the F1 fix;
      - the SHA-256 delete fix;
      - the `Policy.Evaluate` contract change visible to embedders: `(Deny, nil, err)` for a
        control-byte `Branch`.
13. **SMP sequencing.**
    - This PR's merge commit is the one that SMP's later `.spine-revision` bump names.
    - The PR description links SMP INIT-027/EPIC-004 TASK-001.
    - It states that SMP's parser fix does **not** wait for this task.

## Out of scope / follow-ups

- **SMP's parser copy.** SMP INIT-027/EPIC-004 TASK-001 handles it, independently and with no
  revision bump.
- **One ref grammar.** Converging `refname.Validate` and `git.ValidateRef` onto it is TASK-002.
- **Run-branch push admission and the reserved `spine/*` namespace.** This is TASK-016. It must keep
  `validateRefUpdate` on the parse path, and it may rely on canonical refs.
- **The `devMode` git-auth bypass and trusted-CIDR handling.** TASK-015 (P3-30).
- **Case- and normalization-insensitive storage.** Owner question 1, a new follow-up. TASK-016's
  out-of-scope line needs correcting.
- **Symref aliasing (design check).**
  - **The behaviour.** A push to a symbolic ref under `refs/heads/` updates the ref it points to,
    while the gate evaluates the symref's own name. Verified with plain git:
    `refs/heads/alias -> refs/heads/main` advanced `main`.
  - **Not reachable today:**
    - push cannot create a symref;
    - neither Spine nor SMP creates one under `refs/` (only `HEAD`, which cannot be pushed);
    - a mirror import turns remote symrefs into plain refs.
  - **Recorded for** TASK-016 and the case-insensitivity follow-up, which both reason about ref
    aliasing.
- **`receive.denyCurrentBranch` on standalone non-bare repos.** It is incidental and nothing relies
  on it (see the Purpose note).

## Implementation Notes

*(Implementation, 2026-10-07. Branch `init/INIT-023-TASK-001-pre-receive-refname-validation`, cut
from Spine local `main` `5b5df5b` (`61994fe` + the INIT-023 scaffold). The orchestrator named the
branch, so it differs from the `INIT-023/EPIC-001/TASK-001-…` name in the Deliverable. Run on
macOS, git 2.54.0, go 1.26.5. The local PostgreSQL is 17.10; CI uses 18.)*

### What was built

| Commit | Change |
|---|---|
| `cde1176` | `adapters/git/refname/qualified.go` (new): `ValidateQualified`, Design §1. Its doc comment names both receive-pack exceptions, as the §1 design-check note asked. `qualified_test.go`: corpus, agreement with git, byte sweep |
| `fec38fe` | `service/internal/githttp/prereceive.go`: Design §2 steps 1–6, plus SMP's line-1 capability-list guard (see Additions). New tests: `prereceive_refvalidation_test.go`, `prereceive_nulref_test.go`, `prereceive_realclient_test.go` |
| `6d540d7` | `core/branchprotect/policy.go`: the Design §3 guard, `indexControlByte`, and both doc comments. `policy_controlbyte_test.go` |
| `9bd5236` | The three AC 12 documentation touches |
| `0a6bfe3` | OQ1 default: the follow-up is recorded in the EPIC-001 epic's Out of scope. TASK-016's Out-of-scope bullet and owner question 5 are corrected |
| `56f242b` | AC 9: the CI job `scenario-git-http`, plus a fixture fix in `TestGitHTTP_MixedPushRejectedAllOrNothing` |
| `79c029a`, `1fae0fa` | Hygiene: gofmt on the scenario file's imports (they were unformatted on `main`), and one gocritic fix in a new test helper |

`handler.go`, `go.mod`, `go.sum`, migrations, configuration, environment variables and the event
catalogue are untouched (`git diff --quiet main...HEAD -- go.mod go.sum service/internal/githttp/handler.go migrations`).

### Per-AC evidence

"At HEAD" means: new tests run with `go test -overlay`, which swaps `main`'s `prereceive.go`
and `policy.go` back in (plus a one-line test shim that defines `zeroSHA256` so the tests compile).
The worktree itself was never reverted.

1. **Parser table.** `TestParseRefUpdates_RejectsMalformedCommandOnAnyLine` has 37 rows, covering:
   - every listed refusal, each error naming `command N`;
   - the pinned `shallow`, `push-cert` and `0001` rows, plus a `shallow <oid> <ref>` (3-field) row
     and an empty-pkt-line row;
   - parsed rows for line-1 caps, SHA-256 ids, uppercase hex, **no trailing LF**, and a real-shaped
     capability list.

   No error contains a raw NUL, CR, `\v` or DEL. `TestParseRefUpdates_CapabilitiesFromLine1Only`
   pins the caps set and the empty list. **At HEAD:** 31 of the 37 rows fail. The 6 that pass are
   the 5 parsed rows and the `0001` row, which was already refused.
2. **End-to-end through the real `git-http-backend`.** Two tests, each run as contributor and as
   operator. The operator pushes **without** `-o spine.override`, the case the audit flagged as an
   unaudited override:
   - `TestPreReceive_NulAfterRefOnLaterLine_DirectWriteRefused`, variants `nul`, `nul+caps`,
     `nul+junk` and `spine-run decoy`;
   - `TestPreReceive_NulAfterRefOnLaterLine_DeleteRefused`: `develop` and `release/1` (the
     wildcard row is kept for SMP parity), with a `feature-y` decoy.

   The attacker commit carries the takeover shape (`.spine/branch-protection.yaml` with
   `rules: []`). Each test asserts all of the following:
   - the response contains `malformed push: command 2: invalid ref name: ref "refs/heads/main\x00…`;
   - there are no `ng` lines and no raw NUL;
   - `main` does not move;
   - `develop`, `release/1` and the decoys survive;
   - 0 events.

   The controls without the NUL are denied by `no-direct-write` and `no-delete`. **At HEAD:**
   `BYPASS — main moved`, and `develop` and `feature-y` are deleted, for both roles.
3. **Audit PoCs flip.** `poc/verify-r1-0/nulref_verify_test.go` and
   `poc/governance/nul_ref_bypass_test.go` were overlaid unchanged:
   - **On the fix:** both **fail** (`NOT REPRODUCED`, `release still present`,
     `expected main advanced to B by bypass`).
   - **At HEAD:** both **pass** (`CONFIRMED …`).

   AC 2 holds the inverted ports.
4. **SHA-256.** `TestPreReceive_SHA256NullOIDDeleteRefusedByNoDelete` passes, and a control
   delete of an unprotected branch in the same repository applies. **At HEAD:** `BYPASS — no-delete
   branch develop deleted in a SHA-256 repo`. `TestRefUpdate_IsDeleteRecognisesBothNullOIDs`
   passes. The test calls `t.Fatal` instead of `t.Skip` when `CI` is set, so the `go test ./...`
   CI job cannot skip it.
5. **Grammar.** Three tests pass:
   - `TestValidateQualified`: the 41-row corpus, every rejection a `*domain.SpineError` with
     `ErrInvalidParams`, and no raw control byte;
   - `TestValidateQualified_AgreesWithGit`: the corpus also includes `refs/heads/-x`,
     `refs/heads/a./b`, `refs/heads/@`, `refs/heads/é` and NBSP;
   - `TestValidateQualified_ByteSweepAgreesWithGit` (an SMP-parity addition): 390 refs, with
     bytes `0x01`–`0x7f`, `0x80`, `0xc2` and `0xff` in three positions, each checked against
     `git check-ref-format --allow-onelevel`. 0 mismatches, about 4 s.
6. **Policy guard.** `TestEvaluate_ControlByteBranchFailsClosed` runs 9 branches × 3 kinds × 3
   rule sets × override on/off, with an admin actor. Every case returns `(Deny, nil, error)`. At
   HEAD, **all 162 fail**. `TestEvaluate_WellFormedBranchesUnchanged` (which includes `é` and NBSP)
   passes at HEAD and on the fix. `TestIndexControlByte` covers all 256 bytes.
   - `core/branchprotect` imports are unchanged.
   - `./dev/lint-boundaries.sh` passes with no allowlist change.
   - The `Policy` and `Evaluate` doc comments name the malformed-request error.
7. **Well-formed pushes are unchanged.** `TestPreReceive_RealGitClientPushesUnchanged`,
   `TestPreReceive_WellFormedMultiRefPushStillApplies` and
   `TestParseRefUpdates_CapabilitiesFromLine1Only` pass **at HEAD and on the fix**. The real-client
   test covers:
   - a 3-ref push that includes `spine/run/*`;
   - `-o ci.skip`;
   - an unprotected delete;
   - `main` denied with `no-direct-write`;
   - *(parity with SMP AC 7)* a contributor's `-o spine.override=true` refused;
   - *(parity with SMP AC 7)* an operator's override push landing with exactly 1 event.
8. **Mutations.** Each mutant was applied alone via overlay. All were **killed**:

   | Mutant | Killed by |
   |---|---|
   | M1 (caps split on every line) | AC 1 and AC 2 tests |
   | M2 (validateRefUpdate skipped) | AC 1 and AC 2, on `malformed push` |
   | M3 (guard off) | `TestEvaluate_ControlByteBranchFailsClosed` |
   | M4 (IsDelete SHA-1 only) | AC 4 |
   | M5 (TrimRight) | AC 1, the second-LF row |
   | M6 (drop hex check) | AC 1 |
   | M7 (drop equal-length check) | AC 1 |
   | M8 (drop `refs/` rule) | AC 1 and the grammar tests |
   | M9 (accept DEL) | AC 1 and the grammar tests |
   | M10 (`%s` instead of `%q`) | AC 1, AC 2 and the grammar tests |
   | M11 (reject only NUL) | AC 1 and the grammar tests |
   | M12 (accept `..`) | AC 1 and the grammar tests |
   | M14 (drop the line-1 capability check) | AC 1 and `TestPreReceive_Line1CapabilityListRefused` |

   Under M2, `main` still held through the `Evaluate` guard (no `BYPASS`, and
   `branch-protection evaluation failed` was logged). This proves the second layer. M13 and M15
   (SMP's log fields) do not apply here; see Parity.
9. **Existing suites.**
   - `go test ./...` is green.
   - The `TestGitHTTP_*` scenarios: 19 of 19 **PASS, 0 SKIP**, against a real database. That
     includes all 8 push scenarios AC 9 lists.
   - **CI:** a new `scenario-git-http` job runs them against a `postgres:18-bookworm` service and
     fails on any `--- SKIP: TestGitHTTP_` (see Deviations).
   - One of the 8, `TestGitHTTP_MixedPushRejectedAllOrNothing`, **failed on `main` too**, with
     ENOENT writing `feature/a.md`. It is a latent fixture bug that no CI job had run. Fixed
     (`os.MkdirAll`); the assertion is unchanged.
   - `make lint` (the lint commands themselves) shows no new issues; see Gates.
10. **SMP compatibility.** The SMP checkout was at SMP `main` `7d4e7cc9`, which carries SMP's
    **unfixed** parser. It has no `vendor/` and no `tmp/`, and `../spine` is this worktree.
    Every SMP command below ran with `-mod=readonly`:
    - `go build ./...` and `go vet ./...` are clean.
    - `go test -p 4 ./...`: 148 packages ok, 0 fail.
    - `go test -p 4 ./internal/customer/... ./cmd/customer-app/... ./internal/engine/...`: 55 ok.
    - `go test -tags integration ./internal/customer/githttp/...`: ok.
    - **The guard alone:** `poc/skeptic_nul/smp_nul_ref_test.go` and
      `poc/verify-r1-0/nulref_verify_test.go` were overlaid on SMP's `internal/customer/githttp`.
      Both **fail to reproduce** (`main not moved`, `NOT REPRODUCED`, `release still present`), and
      SMP answers `branch-protection: evaluation error on main\x00`.
    - **Control:** the same run, also overlaying Spine `main`'s `policy.go`, **reproduces**
      (`SMP CONFIRMED`). So the overlay was not vacuous.
    - The SMP checkout was left unmodified (`git status` clean).
11. **Diff scope.**
    - The non-test Go files are exactly `qualified.go` (new), `prereceive.go` and `policy.go`.
    - Test files: the five named ones, plus the scenario fixture fix.
    - Non-Go files: `ci.yml`, the three docs and the planning artifacts.
12. **Documentation.**
    - **`docs/git-push-guide.md` §5:**
      - the `malformed push` row lists the AC's four causes, plus the capability-list cause and
        the `command N` text;
      - the paragraph below the table now says a malformed-push rejection carries **no** `ng`
        lines.
    - **`KNOWN-LIMITATIONS.md` → Security Notes:** a new subsection, "Served Repositories Need
      Case- and Normalization-Sensitive Storage". The packed-ref `refs/heads/MAIN` aliasing was
      re-checked on APFS with plain git 2.54 before writing it.
    - **`CHANGELOG.md` (the Unreleased staging file):** a Security section (F1, SHA-256 delete), a
      Changed section, and a For-embedders section (the `Policy.Evaluate` contract and the new
      `refname.ValidateQualified`).
13. **SMP sequencing.** This branch is merged by the orchestrator, and the merge commit is the one
    SMP's bump names. The PR description below links SMP TASK-001 and says SMP's parser fix does
    not wait for it.

### Deviations and additions (with reasons)

- **Added: the line-1 capability-list guard (`isCapabilityList`).** It refuses a non-printable-ASCII
  capability list as `command 1: malformed capability list %q`.
  - **Origin.** SMP TASK-001's design check (§1 step 3a, Decision 9, §7 note 3) found that this
    task's prototype had the same line-1 differential, and asked Spine to adopt the guard.
  - **The differential.** git ends the feature list at a second NUL and splits it on `isspace`;
    `strings.Fields` does not behave the same way.
  - **Reproduced at HEAD through the real CGI** (`TestPreReceive_Line1CapabilityListRefused`):
    - an operator's `spine.override=true` was honoured, and **1 override event** was emitted, for
      a push git then failed to unpack;
    - a push-options block that git read was invisible to the gate.
  - **Not a protection bypass,** but it falsified §0's invariant.
  - The orchestrator ruled that validation rules stay equivalent across the two gateways, so it is
    adopted here.
  - Real clients are unaffected (the real-client test passes).
- **Added: wire-text parity.** `validateRefUpdate` unwraps the `*domain.SpineError` message, so the
  denial reads `invalid ref name: ref "…" contains control byte 0x00`, with no `invalid_params:`
  prefix. That is the same text as SMP's copy, and the `%q` quoting is kept (§1 design-check note:
  optional).
- **Added: the byte-sweep test, and the delete test's `release/*` row.** These keep parity with
  SMP AC 5 and AC 2.
- **AC 9, CI wiring scoped to `TestGitHTTP_*`.** Closest safe alternative.
  - **Why scoped.** The full `go test -tags scenario ./service/scenariotest/...` is **not green on
    `main`**. Locally, `-p 1` against PostgreSQL 17 gave the same 31-failure set on a pristine
    `main` export and on this branch (before the fixture fix); after it, 30. The failures are
    artifact-validation, run-lifecycle, gateway and fixture steps, unrelated to the push gate. A
    full-suite job would be red from day one.
  - **What the job does.** It runs exactly the scenarios AC 9 names. It fails if any of them skips
    and if no `TestGitHTTP_Push*` passes. The guard was checked locally: pointed at an unreachable
    database, it fails.
  - **Follow-up.** Making the whole scenario suite green in CI is out of scope; recorded under
    "Not run".
- **SHA-256 test:** fails instead of skipping under `CI`, so "PASS, not SKIP" is enforced
  mechanically (AC 4).
- **Test hermeticity.** Every git command the new tests run uses `GIT_CONFIG_GLOBAL=/dev/null`,
  `GIT_CONFIG_NOSYSTEM=1` and `GIT_TERMINAL_PROMPT=0`. Otherwise a developer's
  `init.defaultObjectFormat=sha256` or `commit.gpgsign` would change what the tests build.
- **The corpus writes NBSP as ` `,** not as a literal invisible character. The bytes are
  identical to SMP's `qualifiedRefCases`.

### Parity with SMP INIT-027 EPIC-004 TASK-001

- **Equivalent.** The refusal set and the ref grammar are equivalent:
  - `parseRefUpdates`, `validateRefUpdate`, `isHexOID`, `isCapabilityList`, `IsDelete` and
    `zeroSHA256` behave identically;
  - `ValidateQualified` and `validateQualifiedRef` have identical rules and messages;
  - the corpora match entry for entry;
  - the sweep shape is the same;
  - the wire text is identical.
- **Intentional code differences:**
  - `ValidateQualified` returns `*domain.SpineError` (`ErrInvalidParams`, like `refname.Validate`),
    where SMP returns plain errors. `validateRefUpdate` therefore unwraps the message, and SMP's
    copy does not need to.
  - Comments and a local variable name (`capList`) differ.
- **Intentional difference: the parse-failure log.**
  - **SMP** adds `actor_id` and `repo_path` to its two `pre-receive parse failed` warnings (its
    M13, M15).
  - **Spine** keeps `handler.go` untouched (AC 11; OQ3: WARN only). Its warning already carries
    `remote_addr`, the error (`%q`-escaped) and `workspace_id`; the last comes through
    `observe.Logger`, because `handleGit` sets it. It does not carry the actor.
  - Adding `actor_id` would be a one-line `handler.go` change. It is left to the owner, as a
    possible follow-up next to OQ3.
- **Residual differentials,** recorded and not fixed (as in SMP's §0 note):
  - **`push-options=<value>` tokens.** git counts them as the push-options feature; the gate does
    not. The options are then inert and the gate decides with override=false, which is the safe
    side.
  - **Symref aliasing.** See Out of scope.
  - **Case- and normalization-insensitive storage.** OQ1 and §6.

### Gates (commands and results)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | ok |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` | exit 0 (38 packages ok) |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -race -p 4 -count=1 ./adapters/git/refname/... ./service/internal/githttp/... ./core/branchprotect/...` | all ok |
| `SPINE_TEST_DATABASE_URL=…/s001_scenario go test -race -tags scenario -p 4 -count=1 -run '^TestGitHTTP_' ./service/scenariotest/scenarios/` | ok; 19/19 PASS, 0 SKIP (non-race `-v` run) |
| `SPINE_TEST_DATABASE_URL=…/s001_scenario go test -tags scenario -count=1 -p 1 ./service/scenariotest/...` | 190 PASS / 6 SKIP / 30 FAIL. The SKIPs need `SPINE_REGISTRY_TEST_DATABASE_URL`. The FAIL set is `main`'s 31 minus the fixed mixed-push scenario (pre-existing; a `main` export against `s001_scenario_main`) |
| `SPINE_TEST_DATABASE_URL=…/s001_integration go test -tags integration -count=1 -p 1 ./...` | 36 ok, 2 FAIL: `TestAWSClient_Contract_LocalStack` (local :4566 is not LocalStack Secrets Manager, HTTP 501) and `TestRunModeDatabaseDefault` (NULL `workflow_version_label`). Same set on the `main` export (`s001_integration_main`); neither is in a touched package. The touched packages have no integration-tagged tests |
| `golangci-lint run ./adapters/git/refname/... ./service/internal/githttp/... ./core/branchprotect/...` | 22 issues, an **identical** set to `main` (20 gocritic in existing tests and `prereceive.go`'s `sidebandChunks`, 2 gofmt in `core/branchprotect/projection`). **0 new** |
| `golangci-lint run --build-tags scenario ./service/scenariotest/scenarios/` | 1 issue fewer than `main` (the gofmt import fix), 0 new |
| `golangci-lint run --enable-only=gosec` (touched packages) | 0 issues |
| `gofmt -l` on every touched Go file | empty. The repository has pre-existing unformatted files elsewhere on `main`, which are untouched |
| SMP gate (Per-AC 10) | build, vet, 148/148 packages, integration githttp, and the guard-alone PoC check: all green |
| `codex review --base main` | "No actionable regressions were found." No P1 or P2, so nothing to fix or reject. Its sandbox could not run the boundary lint, which was run above |

The `s001_scenario`, `s001_scenario_main`, `s001_integration` and `s001_integration_main` databases
were dropped after the runs.

### Not run

- **The new CI job itself,** which cannot run locally. Its exact command,
  `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/`, ran
  locally against PostgreSQL 17.10 rather than CI's `postgres:18-bookworm`. Its skip guard was run
  against an unreachable database.
- **SHA-256 PASS on the ubuntu runner.** Verified locally on git 2.54. On CI, `CI=true` turns a
  skip into a failure.
- **A green full scenario suite in CI.** Pre-existing failures on `main` (above). This is a
  follow-up for whoever owns the scenario harness. The command is
  `make test-scenario` with `SPINE_TEST_DATABASE_URL` set.

### PR description (draft, AC 13)

> **INIT-023 EPIC-001 TASK-001 — Reject NUL/control-byte and invalid refs on every pre-receive
> command line (audit F1, P1).**
>
> - The standalone gateway's pre-receive parser now validates every command line: object ids,
>   git's refname grammar (new `refname.ValidateQualified`), capabilities on line 1 only and as
>   printable ASCII, and the SHA-256 null id.
> - `branchprotect.Policy.Evaluate` fails closed on control-byte branch names (embedder-visible
>   contract change; see CHANGELOG).
> - A new CI job runs the `TestGitHTTP_*` scenarios against Postgres.
>
> SMP counterpart: `smp:/initiatives/INIT-027-pilot-readiness/epics/EPIC-004-security-remediation/`
> TASK-001 (`TASK-001-nul-ref-push-gate.md`). SMP's own parser fix does **not** wait for this PR.
> It ships independently, with no `.spine-revision` bump. This PR's merge commit is the one SMP's
> later `.spine-revision` bump names (SMP TASK-012 adds the `Evaluate` pin test).

## Design check

*(2026-10-06: an adversarial review of this artifact against Spine `61994fe` and SMP `80995559`.)*

What was changed, and why:

1. **Completed the truncated file.**
   - It ended mid-sentence in §5 ("All existing tests pass un").
   - It had no Decisions, Owner questions, ACs, Out of scope or Implementation Notes.
   - These were rebuilt from the designer's prototype and the SMP sibling, and are clearly marked.
2. **Found a bypass of the fix on case-insensitive storage (§6).**
   - A plain `git push` to `refs/heads/MAIN` or `refs/HEADS/main` reproduces it when `main` is
     packed.
   - It is not F1, and this task does not close it.
   - It is recorded as Owner question 1, plus a documentation guard (AC 12).
   - TASK-016's contrary premise is disproved.
3. **Corrected how SMP was verified.**
   - SMP's untracked `vendor/` shadows `../spine` overlays, so the designer's `overlay-smp.json`
     runs were vacuous.
   - Re-run with `-mod=readonly`, the claim holds: the `Evaluate` guard alone closes F1 in SMP, and
     SMP's suites stay green.
   - AC 10 now spells out how to verify it.
4. **Qualified the standalone Impact.**
   - On a non-bare repo, git's `denyCurrentBranch` refuses the update to a checked-out `main`
     (reproduced).
   - Other literal-protected branches and deletes are fully exposed. SMP is fully exposed.
5. **Fixed citations and counts:**
   - the `validateGitAuth` line range;
   - "four files" → three;
   - the dangling "§6" reference;
   - the "three documentation touches", which were never enumerated.
6. **Made the grammar claim precise.**
   - `ValidateQualified` mirrors `git check-ref-format` on the whole name.
   - receive-pack additionally refuses `refs/@` and one-level creates.
   - The gate is looser only where git refuses anyway, which is safe. The doc comment should say so.
7. **Strengthened the ACs:**
   - parser-table rows for lines with no LF (what real git sends), `shallow`, `push-cert` and
     `0001`;
   - the SHA-256 and scenario push tests must PASS rather than SKIP in CI;
   - mutations M1-M5, with M1-M3 verified;
   - the `Policy` doc comment;
   - the correction to `git-push-guide.md:117`.
8. **Checked with no remaining finding:**
   - other parse differentials: gzip, method case, delim packets, push-cert, push-options on a later
     line;
   - the other `Evaluate` callers: artifact, engine merge, divergence, and SMP's factory and artifact
     services. All build `Branch` from stored or constant names, so the guard is reachable only
     through a caller bug;
   - wire compatibility with real clients;
   - stored data, which the task does not touch.
