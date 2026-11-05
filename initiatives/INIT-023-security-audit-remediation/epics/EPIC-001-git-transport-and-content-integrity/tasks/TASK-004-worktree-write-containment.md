---
id: TASK-004
type: Task
title: "Confine worktree writes against symlinked path components and .git/"
status: In Progress
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-11-04
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-001-git-transport-and-content-integrity/epic.md
  - type: related_to
    target: /governance/go-coding-guidelines.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-004-security-audit-2026-04/tasks/TASK-023-misc-polish-errors-pagination-worktree-traceid.md
---

# TASK-004 — Confine worktree writes against symlinked path components and .git/

---

## Purpose

This task closes audit finding **F5** (P2), "dangling-symlink final path component lets
`artifact.Create` write outside the worktree" (security audit 2026-10-06, Spine `61994fe`,
SMP `80995559`). It covers every place Spine writes a file into a git working tree.

Line numbers refer to Spine `61994fe` unless stated. "SMP" means
`spine-management-platform` at `80995559`.

### The defect

1. **The containment check misses a dangling symlink in the last component.**
   - `safePathIn` (`core/artifact/service.go:103-146`) resolves the target with
     `resolveToExistingAncestor` (`:136`, `:540-567`).
   - `filepath.EvalSymlinks` fails when the final component is a symlink whose target does
     not exist yet. The helper then resolves only the parent and re-appends the *unresolved*
     leaf, so the prefix check at `:142` passes.
   - `writeAndCommit` then follows the link twice. The `os.Stat` pre-check (`:220`) reports
     "not exists". `os.WriteFile` (`:230`) creates the file at the link target, outside the
     repository or worktree.
   - git checks out committed symlink blobs as real symlinks. Neither repository sets
     `core.symlinks=false` (no match in either tree).
2. **The commit fails, but the outside file stays.**
   - `git add` stages the symlink itself, which is unchanged, so the scoped commit fails with
     "nothing to commit".
   - The rollback `os.Remove(fullPath)` (`:259`) deletes the link, not the file written through
     it. The audit PoC observed exactly this (`Create` returned `git_error`, and the outside
     file remained).
3. **Relative links that stay inside the root are not checked at all.**
   - `EvalSymlinks` resolves an in-root link (for example
     `governance/a.md -> ../.git/info/attributes`) to a path that has the root prefix, so it
     passes.
   - In the standalone main working tree, `.git/` is a real directory inside the root.
   - Re-checked on 2026-10-06 with an overlay test: `Create` on such a link wrote a
     `.git/info/attributes` holding attacker-chosen lines (for example `* merge=union`), and the
     file **survived the rollback**.
   - git applies `$GIT_DIR/info/attributes` with the highest precedence to every later merge
     in that repository (gitattributes(5)).
4. **The same pattern exists, unguarded, in four more sinks:**
   - **Workflow writes.** `workflow.Service.writeAndCommit` (`core/workflow/service.go:144-192`)
     joins `scope.RepoDir` + `workflows/<id>.yaml` and runs `os.Stat` / `os.ReadFile` /
     `os.MkdirAll` / `os.WriteFile` with **no containment check of any kind**. The update path
     also reads through `readFromDisk` (`:290-302`).
     - Re-checked on 2026-10-06: a dangling `workflows/evil.yaml -> /outside/x.yaml` leaves
       `/outside/x.yaml` behind.
     - An `Update` through `workflows/evil.yaml -> /outside/victim.yaml` overwrites the outside
       file. It is restored only because the commit then fails.

     > **Design-check note (2026-10-06) — the workflow sink is also an unbounded-read (OOM) sink.**
     > `Update` with `workflows/zero.yaml -> /dev/zero` committed on the written branch passes the
     > `os.Stat` existence check (a character device exists) and then `readFromDisk`'s
     > `os.ReadFile` never reaches EOF. Overlay probe against `61994fe`
     > (`scratchpad/devzero/devzero_test.go`): `HeapAlloc` passed 256 MiB within 0.17 s with the
     > call still reading. On SMP's single 512Mi `customer-app` this is the F4 cross-tenant OOM
     > reached through a filesystem path instead of `git show` (embedded precondition: the link on
     > the default branch plus an admin workflow update). The artifact sinks are not affected today
     > (`EvalSymlinks("/dev/zero")` resolves outside the root and is refused). The leaf-symlink
     > refusal (I2) closes this; §1c (amended) also bounds regular-file reads, which TASK-003 does
     > not cover (it bounds `git` stdout only).
   - **The merge-cascade sink.** `CLIClient.WriteAndStageFile`
     (`adapters/git/cli.go:507-520`) does `filepath.Join(c.repoPath, path)` + `os.MkdirAll` +
     `os.WriteFile` with **no validation at all**. Re-checked on 2026-10-06:
     - `../x.md` and `a/../../x.md` are written outside the repository;
     - `.git/info/attributes` is written, and `git add` exits 0;
     - a leaf symlink to an existing outside file is overwritten.
     - The caller is the governed-merge status cascade (`core/engine/merge.go:643-651`). It reads
       each blob with `git show` at `:606` and then checks the branch out at `:643`. A push to
       the `spine/*` branch between those two lines (or F9's shared-tree interleaving) puts a
       symlink on disk where a regular blob was read.
   - **The other artifact read/write paths.** `acceptance.go:81-86` and `successor.go:118-123`
     read through `safePath` + `os.ReadFile`. The `Update` rollback `os.WriteFile`
     (`service.go:257`) follows a leaf symlink.
   - **Renumber.** `RenumberArtifact` (`core/artifact/renumber.go:42-83`) and
     `UpdateLinksToRenamedArtifact` (`:87-115`, `filepath.Walk` + `os.ReadFile`/`os.WriteFile`
     on symlinked `.md` leaves) have no containment. They have **no production caller** in Spine
     or SMP at these heads (dormant), but are guarded here rather than descoped.

### Attackers and reach

The attacker is **A1** (commits a symlink to a branch) combined with **A2** (an authenticated
member or contributor who triggers the write). In SMP, members and runner bearers can push
`spine/*` run branches (audit F2).

- **Standalone (demonstrated).**
  - A contributor pushes a run branch where an artifact path is a link to an absolute path, or to
    `../../../x`.
  - They then call `POST /artifacts` with `write_context`
    (`service/internal/gateway/handlers_artifacts.go:81-90`).
  - The worktree is `$TMPDIR/spine-wt-parent-*/wt` (`adapters/git/branchwrite.go:69-75`), so a
    relative link needs no knowledge of the random name.
  - The workflow sink and the cascade sink are standalone-reachable too.
- **Embedded (code-traced, narrower; the audit marks it partly disputed).** SMP roots every
  Spine writer at a gitclone worktree: `<clone-root>/.worktrees/<hex>/wt`
  (`internal/customer/gitclone/pool.go:135`, `worktree.go:350-367`). The other tenants' clones
  live in the same root, at `<clone-root>/<workspace>/<repo>` (`pool.go:457`).
  - A file written through a link therefore escapes into the shared clone root, which may be
    another tenant's clone area.
  - **`AddToRun` is not a practical vector.** `artifact.NextID` counts every tree entry that
    matches the ID pattern, including a planted link (`core/artifact/id_allocator.go:75-112`).
    A link planted at the predicted path therefore uses up its own ID.
  - **Document-type `CreateEntry` is the remaining entry point.** Its path is
    `governance|product/<slug>.md`, chosen from the title with no ID
    (`internal/customer/spineembedded/artifacts_createentry.go:125-132`, `:333-338`). It writes
    on a branch forked from `main`, so it needs a dangling link already on the default branch.
    That link can come from F1's NUL-ref push (P1) or from a self-reviewed governed merge (F3).
  - Embedded workflow writes are admin-only and also write `main`.

### Impact

- Filesystem containment is broken: attacker content is created outside the tree. The file must
  pass artifact or workflow Parse/Validate, but its Markdown body is free text.
- In the standalone main tree, attacker lines can be planted in `.git/info/attributes` (and
  other absent `.git/` files). Merge behaviour changes for every later merge.
- In SMP, files can be placed in the shared clone root.
- Through the cascade sink, an existing outside file the process can write may be
  **overwritten**.
- `KNOWN-LIMITATIONS.md:72` currently claims the opposite: "`safePath` in artifact service
  resolves symlinks and validates prefix".

## Design

### 0. Invariants

- **I1.** Every byte Spine writes, reads back for rewriting, or removes inside a git working tree
  goes through one helper. Every operation is anchored to a directory file descriptor obtained
  without following a symbolic link below the tree root.
- **I2.** No path component below the root may be a symbolic link. This includes the final
  component, whether the link dangles or not, and whatever it points at. A symlink anywhere in
  the path is refused with a typed error **before any filesystem change**.
- **I3.** No path component may name the git directory (`.git`, or the case, NTFS and HFS variants
  git itself treats as `.git`). The check is the same in a main working tree, where `.git` is a
  directory, and in a linked worktree, where `.git` is a file.
- **I4.** New files are created with `O_CREAT|O_EXCL`, so the final component is never followed.
  Updates never open the existing file for writing: they write a fresh `O_EXCL` temp file and
  `renameat` it over the leaf. A failed create removes the file and every directory this call
  made.
- **I5.** The tree root itself is trusted and chosen by the server (the repo path, the
  `git worktree` dir, SMP's scope dir). Symlinks in the root's own path are followed, for example
  macOS `/var -> /private/var` or an operator-symlinked clone root.
- **I6.** No exported Spine signature changes. This covers `artifact.Service`, `workflow.Service`,
  `git.GitClient`, `engine.GitOperator` (which SMP implements) and `git.CLIClient`. The change
  needs no schema change, no migration, no new environment variable and no new config.

> **Design-check note (2026-10-06) — two invariants added.**
> - **I7. Bounded resources.** `Tree.ReadFile` (and every read `WalkRegularFiles` drives) refuses a
>   leaf whose `Lstat` size exceeds `DefaultMaxReadBytes` (4 MiB, the same value as TASK-003's
>   `git.DefaultMaxBlobBytes`; TASK-003 owner question 1 governs both) and reads through an
>   `io.LimitReader(limit+1)`, so a file that grows after the check is still capped. `Clean`
>   refuses a path over 4096 bytes or with a component over 255 bytes (the Linux `PATH_MAX` /
>   `NAME_MAX` the old `os.*` calls hit implicitly; an fd-anchored walk is no longer limited by
>   `PATH_MAX`, so the cap must be explicit).
> - **I8. No handle leaks.** Every `os.Root` the walk opens is closed on every return path;
>   `Undo.Rollback` and `Undo.Release` are idempotent, so `defer undo.Release()` after an explicit
>   `Rollback()` is safe. The multi-tenant `customer-app` must not accumulate descriptors.

### 1. New package `core/worktreefs` (stdlib-only)

It lives in `core/` so that `core/artifact`, `core/workflow` and `adapters/git` can all use it.
`adapters/` may import `core/` under ADR-017. It adds no new `core -> adapters` crossing to the
`dev/lint-boundaries.sh` allowlist. SMP may import it, because its lint allows `core/...` except
`core/internal/...`.

```go
package worktreefs

var ErrUnsafePath = errors.New("worktreefs: unsafe path") // every refusal unwraps to this

type Reason string
const (
    ReasonInvalid    Reason = "invalid"       // empty, NUL, absolute, non-canonical, "." / ".." component, the root itself
    ReasonGitDir     Reason = "git_dir"       // a component names the git directory
    ReasonSymlink    Reason = "symlink"       // a component (incl. the leaf) is a symbolic link
    ReasonNotDir     Reason = "not_directory" // a non-final component exists and is not a directory
    ReasonNotRegular Reason = "not_regular"   // the leaf exists and is not a regular file
    ReasonChanged    Reason = "changed"       // a component changed between its check and its open
)

// PathError carries ONLY repo-relative, slash-form strings — never an absolute path
// (keeps INIT-008 TASK-023's "errors do not leak filesystem detail" contract).
type PathError struct{ Path, Component string; Reason Reason }
func (e *PathError) Error() string   // `unsafe path "governance/evil.md": component "governance/evil.md" is a symbolic link`
func (e *PathError) Unwrap() error   { return ErrUnsafePath }

func Clean(rel string) (string, error)   // lexical validation (§1a); returns the canonical slash form

type Tree struct{ /* *os.Root */ }
func Open(dir string) (*Tree, error)     // os.OpenRoot(dir)
func (t *Tree) Close() error
func (t *Tree) Lstat(rel string) (fs.FileInfo, error)                   // walk; missing leaf/dir -> wraps fs.ErrNotExist
func (t *Tree) ReadFile(rel string) ([]byte, error)                     // leaf must be a regular file
func (t *Tree) Create(rel string, data []byte, perm fs.FileMode) (*Undo, error) // O_CREAT|O_EXCL; existing regular leaf -> wraps fs.ErrExist
func (t *Tree) Replace(rel string, data []byte) error                   // existing regular leaf; temp + fchmod + renameat
func (t *Tree) WriteFile(rel string, data []byte, perm fs.FileMode) error // Replace if a regular leaf exists, else Create
func (t *Tree) Remove(rel string) error                                 // regular leaf only; unlinkat relative to the held parent fd
func (t *Tree) WalkRegularFiles(relDir string, fn func(rel string) error) error // fs.WalkDir over Root.FS(); symlinks skipped

type Undo struct{ /* held parent dir handles + created names */ }
func (u *Undo) Rollback() error // remove the created leaf, then the dirs this Create made (deepest first, only if empty), via the held handles
func (u *Undo) Release()        // close held handles without removing anything
```

> **Design-check note (2026-10-06) — API amendments.**
> - **Error text must not echo the raw caller input.** As specified, `PathError.Error()` quotes
>   `Path` (`unsafe path "../../etc/passwd": …`). The existing
>   `TestSafePath_ErrorDoesNotLeakFilesystemPaths` (`core/artifact/service_internals_test.go:119-131`)
>   asserts the message for `safePath("../../etc/passwd")` does not contain `/etc/passwd`, so the
>   design as written fails `go test ./...` (AC12). Rule: for `ReasonInvalid` the message names
>   only the offending component (`component ".." is not allowed`); for the other reasons it names
>   the validated repo-relative `Component` prefix, never `Path`. `Path` stays on the struct for
>   server-side logs.
> - **Size refusal is not a path refusal.** Add `var ErrTooLarge = errors.New("worktreefs: file
>   exceeds the read limit")`, `type SizeError struct{ Path string; Size, Limit int64 }` (unwraps
>   to `ErrTooLarge`, not `ErrUnsafePath`), `const DefaultMaxReadBytes int64 = 4 << 20`, and
>   `func (t *Tree) ReadFileLimit(rel string, max int64) ([]byte, error)`; `ReadFile` uses the
>   default. Callers map it to `domain.ErrPayloadTooLarge` (413), the mapping TASK-003 uses for
>   `git.BlobTooLargeError`, which both the standalone gateway and SMP's
>   `httpStatusForSpineErrorCode` already translate.
> - **`WalkRegularFiles`** accepts `""` for the tree root (the one place `Clean`'s refusal of the
>   root does not apply), never descends into or yields a component that names the git directory
>   (I3), skips every non-regular entry, and bounds each read (I7).

**1a. Lexical rules (`Clean`).**

- Input may use `/` or the OS separator.
- Refused with `ReasonInvalid`:
  - an empty path, `.` (the root itself), an absolute path, a NUL byte;
  - any empty, `.` or `..` component, even when `filepath.Clean` would fold it away (this matches
    SMP's `validateRepoRelativePath`, `internal/customer/spinecore/gitplumbing.go:122-140`).
    *(Corrected by the wave 2 cross-task review: it matches only for `..`. SMP's
    `validateRepoRelativePath` refuses empty, absolute, NUL and `..` paths, not empty or `.`
    components, and guards only SMP's own `RealGitOperator`. SMP is unaffected for another reason:
    its embedded artifact entry points pass canonical paths. See "Cross-task review (wave 2)".)*
- Refused with `ReasonGitDir`: any component that names the git directory. The check mirrors
  git's own `verify_path` `.git` handling, in this order:
  1. drop the code points git's HFS check ignores (U+200C–U+200F, U+202A–U+202E, U+206A–U+206F,
     U+FEFF);
  2. strip trailing `.` and space characters (the NTFS rule);
  3. compare `.git` case-insensitively; separately, compare `git~1` case-insensitively.
- Still accepted: `.gitignore`, `.gitattributes`, `.github/...`, `a.git/...` and `git/...`
  (Owner question 2).

> **Design-check note (2026-10-06) — lexical rule amendments.**
> - **Parity with git's `is_ntfs_dotgit`.** git treats `.git` / `git~1` followed by only dots and
>   spaces *and then end of component or `:`* as the git dir (`.git:x`, `.git. :y`). Step 2 must
>   also cut at the first `:` before comparing. On Linux and macOS these names are not the git
>   directory, but git's own `verify_path` (protectNTFS, on by default) already refuses them, so
>   refusing them costs nothing and keeps the rule a faithful mirror.
> - **Length caps (I7).** Refuse with `ReasonInvalid` a cleaned path over 4096 bytes or any
>   component over 255 bytes.
> - **Write the HFS test row as an escape.** The AC2 row for the HFS-ignorable case contains an
>   invisible U+200C; in the test table it must be written `".g‌it/x"` so a reviewer can see
>   it.

**1b. The walk (I1–I3).**

1. `Open` calls `os.OpenRoot(dir)` (Go 1.24+; the module is on `go 1.26`).
2. For each non-final component `c` of the cleaned path (prefix `p`), on the current
   directory handle `cur`:
   - `fi := cur.Lstat(c)` (`fstatat(AT_SYMLINK_NOFOLLOW)`, which never follows).
     - **Missing:** `Create` makes it with `cur.Mkdir(c, 0o755)` (`mkdirat`, which never follows,
       and returns `EEXIST` on a planted link) and records `p` as created. Then it re-runs
       `Lstat`. Every other operation returns `fs.ErrNotExist`.
     - **Symlink:** `PathError{ReasonSymlink, p}`.
     - **Not a directory:** `PathError{ReasonNotDir, p}`.
   - `next := cur.OpenRoot(c)`, then `nfi := next.Stat(".")`. If `!os.SameFile(fi, nfi)`, the
     component was swapped for a link between the check and the open, so the result is
     `PathError{ReasonChanged, p}`. Then descend.
3. Leaf: `lfi := cur.Lstat(leaf)`.
   - A symlink gives `ReasonSymlink` (this includes a dangling link, which is the F5 case).
   - Something that exists and is not a regular file gives `ReasonNotRegular`.
4. All leaf operations use **single-component names on the held parent handle**, so no
   multi-component path is ever resolved again after the check.

> **Design-check note (2026-10-06) — what the `SameFile` check does and does not guarantee.**
> - The check binds each descent to the **inode** that passed `Lstat`. A prototype of this walk
>   (`scratchpad/wtfsprobe/`, go1.26) confirmed: a component swapped for a link into `.git/info`
>   between `Lstat` and `OpenRoot` is refused with `changed`; with the comparison removed the write
>   lands in `.git/info/attributes`. A 2-second stress run (60 019 creates against a goroutine
>   flipping `gov` between a real dir, a link to an outside dir and a link to `.git/info`) wrote
>   nothing outside the root or into `.git/`.
> - A swap **after** the walk (the held fd is already open) cannot redirect the write: the fd
>   still names the checked directory, or a deleted one (`ENOENT`). So the race hook for AC4 must
>   sit **between a component's `Lstat` and its `OpenRoot`** — `testHookAfterComponentLstat(op,
>   prefix string)` — not before the leaf op. With `testHookBeforeLeafOp` as written, removing
>   `SameFile` changes nothing observable and AC4's mutation check cannot fail (prototype: the
>   write landed in the renamed original directory either way).
> - Residual, outside the threat model: a local process that **renames** a checked directory into
>   `.git/` after the check keeps the inode identical. git checkout never renames directories (it
>   unlinks and recreates), and no tenant can run arbitrary filesystem operations in SMP.

**1c. Leaf operations (I4).**

- **`Create`.**
  1. `cur.OpenFile(leaf, O_WRONLY|O_CREATE|O_EXCL, perm)`. The kernel never follows the final
     component under `O_EXCL`, and `os.Root` maps a dangling link to `EEXIST`
     (`os/root_unix.go:88-97`).
  2. On `EEXIST`, re-run `Lstat`: a link gives `ReasonSymlink`; anything else wraps `fs.ErrExist`.
  3. Then write and `Close`.
  4. On any failure after the open, `Rollback` runs before returning: `unlinkat` of the leaf on
     the held parent, then `unlinkat(AT_REMOVEDIR)` of each created dir, deepest first, through
     its held parent handle. A directory that is not empty is left alone.
  5. On success the `*Undo` is returned. The caller runs `Rollback()` when the following commit
     fails, otherwise `Release()`.
- **`Replace`.**
  1. The leaf must `Lstat` as a regular file.
  2. Create the temp `.spine-tmp-<16 hex>` in the same directory with `O_CREAT|O_EXCL`, mode
     `0o600`, and write it.
  3. `f.Chmod(lfi.Mode().Perm())` (an fchmod on the fd, which keeps the git executable bit), then
     `Close`.
  4. `cur.Rename(tmp, leaf)`. `renameat` replaces the directory entry, so even a link swapped in
     after the check is replaced rather than followed.
  5. On failure the temp file is removed.
  6. This never truncates or opens the existing file for writing.
- **`ReadFile`.**
  1. The leaf must `Lstat` as regular.
  2. `cur.OpenFile(leaf, O_RDONLY, 0)`.
  3. `f.Stat()` must be `SameFile` with `lfi`, otherwise `ReasonChanged`.
  4. Read.

  > **Design-check note (2026-10-06).** Between steps 1 and 2: if `lfi.Size() > limit`, return
  > `*SizeError` without opening. Step 4 reads through `io.LimitReader(f, limit+1)` into a buffer
  > pre-sized from `lfi.Size()`, and returns `*SizeError` if more than `limit` bytes arrive (I7).
  > Opening with `O_NONBLOCK` is unnecessary: a FIFO or device cannot pass step 1, git cannot check
  > one out, and a link swapped in after step 1 that points outside the parent directory (for
  > example `/dev/zero`) is refused by `os.Root` ("path escapes from parent").
  >
  > *(Corrected by the codex 6.1 catch-up, 2026-10-14. A leaf **replaced** by a FIFO between step 1
  > and step 2 does not need a link. Step 2's plain `O_RDONLY` then blocks until a writer appears,
  > before step 3 can refuse it, and `Create`'s pin has the same shape. Both opens are now
  > `O_NONBLOCK`, and the read descriptor is made blocking again once the checks pass. Step 3 also
  > requires the opened file to be regular: nothing holds the checked inode, so on Linux the FIFO
  > can take the freed inode number and pass `SameFile`. See "Codex 6.1 catch-up".)*

**1d. Why `os.Root` and the walk are both needed (AC3).**

- `os.Root` alone refuses absolute links and links that escape the root ("path escapes from
  parent").
- But it *follows* relative links that stay inside the root, both for intermediate components and
  for a non-exclusive final open (`os/root_unix.go:82-101`, `:113-131`). Re-checked on
  2026-10-06 against go1.26.8:
  - `OpenFile("attrs.md", O_CREATE|O_TRUNC)` with `attrs.md -> .git/info/attributes` created
    `.git/info/attributes`;
  - `O_EXCL` under an in-root symlinked directory landed in the link target.
- The walk alone, built on `os.Lstat` + `os.OpenFile(path-join)`, would be TOCTOU-prone.
- So the walk enforces I2/I3, and `os.Root` is the containment backstop: if the walk is wrong or
  bypassed, nothing can still leave the root.
- A package-internal hook, `testHookSkipComponentChecks`, disables the walk's symlink check in
  tests only. It proves the second layer on its own (AC5).
- A second hook, `testHookBeforeLeafOp(op, rel string)`, lets tests swap entries between the
  check and the operation (AC4, AC6).

> **Design-check note (2026-10-06) — correct scope of the `os.Root` backstop.**
> - Because the walk descends with `cur.OpenRoot(c)`, each leaf operation runs on a **sub-root
>   confined to the immediate parent directory**, not to the tree root. Probed on go1.26: from
>   sub-root `gov`, `OpenRoot("sib")` with `sib -> ../other` (inside the tree, outside `gov`) fails
>   with "path escapes from parent". `O_RDONLY` and `O_TRUNC` opens of an absolute or escaping
>   leaf link fail the same way. A **same-directory** link (`samedir.md -> x.md`) is still followed
>   for `O_RDONLY` / `O_TRUNC`, and so is a link at the tree root into `.git/` (the designer's
>   `rootprobe`).
> - For `O_CREATE|O_EXCL`, `os.Root` never follows a leaf link and returns **`EEXIST`** for every
>   leaf link, dangling or not, escaping or not. It does not return "path escapes".
> - Consequences for the tests: AC5 must accept `EEXIST` for the exclusive-create row, and the
>   only cases that prove the walk is *necessary* (not merely that it changes the error shape)
>   are links that `os.Root` would follow: a same-directory leaf link and a tree-root link into
>   `.git/`. AC3 and AC6(b) are amended to include them.
> - The design's own leaf write primitives (`O_EXCL` create, `renameat` replace, `unlinkat`
>   remove) never follow a leaf link. Only `ReadFile`'s `O_RDONLY` open and the walk's
>   `OpenRoot` can follow one. So the defence is three overlapping layers: the explicit
>   symlink / not-dir checks, the `SameFile` identity checks (which also refuse a pre-existing
>   intermediate link, because `Lstat` describes the link while `OpenRoot` opens its target), and
>   `os.Root` + `O_EXCL` / `renameat`. The AC5 hook must disable the first two to isolate the
>   third.
> - A third hook is needed: `testHookAfterComponentLstat(op, prefix string)` (see the §1b note).

### 2. `core/artifact`

- **`safePathIn`** (`service.go:103-146`) is kept as the single validation entry so the ported
  PoC tests keep their shape:
  1. Refuse an absolute input (unchanged; it runs *before* `repoRelativePath`, as today).
  2. `rel, err := worktreefs.Clean(s.repoRelativePath(path))`.
  3. `tree.Lstat(rel)` on `worktreefs.Open(root)`, accepting `fs.ErrNotExist`.
  4. Return `filepath.Join(root, rel)`, which is used for logging only.
  - `resolveToExistingAncestor` (`:537-567`) and the `EvalSymlinks` root resolution (`:120-125`)
    are deleted.
  - Errors map through a new `mapPathError`:
    - `*worktreefs.PathError` → `domain.NewErrorWithCause(domain.ErrInvalidParams, pe.Error(), err)`;
    - anything else → logged server-side and returned as a generic `ErrInvalidParams` /
      `ErrInternal` "invalid path".
    - `errors.Is(err, worktreefs.ErrUnsafePath)` holds through `SpineError.Unwrap`
      (`core/domain/errors.go:43-44`).

  > **Design-check note (2026-10-06) — validate the caller's path before `repoRelativePath`.**
  > With `artifacts_dir` set (standalone `WithArtifactsDir`, `service/cmd/spine/serve_builders.go:48`),
  > `repoRelativePath` returns `filepath.Join(artifactsDir, path)`, which **folds `..` away**:
  > `"a/../../x.md"` with `artifacts_dir: spine` becomes `"x.md"` and passes `Clean`. The write
  > stays in the repository but escapes the artifacts directory, and the design's own claim that
  > `..` is refused "even when `filepath.Clean` would fold it away" is false on this path. Fix:
  > step 2 becomes `worktreefs.Clean(path)` on the caller's artifact-relative path first, and then
  > `worktreefs.Clean(s.repoRelativePath(cleaned))`. SMP never sets `artifacts_dir` (no
  > `WithArtifactsDir` call in SMP), so this does not affect SMP. `writeAndCommit` passes the same
  > cleaned `rel` to `StageAndCommit` (today it recomputes `repoRelativePath(path)` at `:235`), so
  > the path that was checked is the path that is staged.
  >
  > Error mapping additions: `*worktreefs.SizeError` → `domain.ErrPayloadTooLarge` (413). A
  > `Create` at a path that is an in-root symlink to an **existing** file changes from 409
  > `already_exists` (today `EvalSymlinks` resolves in-root and `os.Stat` follows) to 400
  > `invalid_params`. That is intended, and it is listed for SMP TASK-008.
- **`writeAndCommit`** (`:173-279`). After `enterBranch` (`:196`), open
  `tree := worktreefs.Open(scope.RepoDir)` (deferred `Close`) and use the cleaned `rel`.
  - **Update** (`requireExists`):
    - `tree.Lstat(rel)`: not exist → `ErrNotFound` (unchanged contract);
    - `originalContent := tree.ReadFile(rel)` replaces `:209-218`;
    - the write is `tree.Replace(rel, content)`.
  - **Create:**
    - `undo, err := tree.Create(rel, content, 0o644)` replaces the `os.Stat`/`os.MkdirAll`/
      `os.WriteFile` at `:220-233`;
    - `fs.ErrExist` → `ErrAlreadyExists` (unchanged 409 contract).
  - **Rollback** after a `StageAndCommit` failure (`:253-261`):
    - Update → `tree.Replace(rel, originalContent)`;
    - Create → `undo.Rollback()`, which also removes directories this call created (today they
      leak);
    - on success → `undo.Release()`.
  - Refusals happen before any filesystem change and before `StageAndCommit`, so a refused write
    never stages anything.
- **`acceptance.go:81-90`, `successor.go:118-127`:**
  - replace `s.safePath(path)` + `os.ReadFile` with `worktreefs.Open(s.repo)` +
    `ReadFile(Clean(repoRelativePath(path)))`;
  - map errors as above;
  - the root stays `s.repo`, as today (no root semantics change).
- **`renumber.go`** (dormant; guarded rather than descoped):
  - `RenumberArtifact` (`:42-83`) opens a `Tree` on `repoRoot` and uses `ReadFile` / `WriteFile`
    / `Remove`. `cleanEmptyParents` (`:137-146`) removes only empty dirs, through the tree.
  - `UpdateLinksToRenamedArtifact` (`:87-115`) uses `WalkRegularFiles` (symlinks skipped, never
    read or written) + `Replace`.
  - The `//nolint:gosec` G703/G122 suppressions at `service.go:257` and `renumber.go:64/99/111`
    become obsolete and are removed.

  > **Design-check note (2026-10-06).** `UpdateLinksToRenamedArtifact` must skip `.git` (the
  > amended `WalkRegularFiles`). Today `filepath.Walk` over a `searchDir` of `""` would descend
  > into `.git/` in the standalone main tree and rewrite any `.md` under it. Its existing tests
  > pass `searchDir` `"tasks"` (`core/artifact/renumber_test.go:150`, `:231`) and keep passing.

### 3. `core/workflow`

`writeAndCommit` (`service.go:119-197`):

- `path := filePath(id)` stays as it is (`idPattern` already forbids `/`). Validate it with
  `worktreefs.Clean` and open `tree := worktreefs.Open(scope.RepoDir)` after `enterBranch`.
- **Update:**
  - `tree.Lstat` not exist → `ErrNotFound` (unchanged);
  - `readFromDisk` (`:290-302`) becomes `tree.ReadFile` + `Parse`;
  - the duplicate `os.ReadFile` at `:163` uses the same bytes;
  - the write is `tree.Replace`.
- **Create:** `tree.Create` (`fs.ErrExist` → `ErrAlreadyExists`).
- **Rollback** (`:188-192`): `Replace(original)` / `undo.Rollback()`.
- **Errors:** a local `mapPathError` maps a `PathError` to `ErrInvalidParams` with the cause
  (the same contract as artifacts).
- This task does **not** add the branch-protection check to `workflow.Service`; that is TASK-014
  (F7). The two tasks touch adjacent lines, which is why TASK-014 is ordered after this one.

### 4. `adapters/git` and the merge cascade

**`CLIClient.WriteAndStageFile`** (`cli.go:507-520`) becomes:

```go
func (c *CLIClient) WriteAndStageFile(ctx context.Context, path, content string) error {
    rel, err := worktreefs.Clean(path)
    if err != nil { return fmt.Errorf("write %s: %w", path, err) }
    tree, err := worktreefs.Open(c.repoPath)
    if err != nil { return fmt.Errorf("open worktree: %w", err) }
    defer tree.Close()
    if err := tree.WriteFile(rel, []byte(content), 0o644); err != nil {
        return fmt.Errorf("write %s: %w", path, err)
    }
    if _, err := c.run(ctx, "add", "--literal-pathspecs", "add", "--", rel); err != nil {
        return fmt.Errorf("stage %s: %w", path, err)
    }
    return nil
}
```

- `--literal-pathspecs` brings this call into line with `go-coding-guidelines.md` §4.2. Today it
  is the only path-taking `git add` in the package without literal pathspecs.
- `WriteFile` keeps the current create-or-overwrite semantics. An overwrite now goes through
  `Replace`, which preserves the mode.
- **`core/engine/merge.go` is not changed.**
  - `applyCommitStatus` already turns a `WriteAndStageFile` error into "checkout main + return
    error" (`:647-650`), and `preflightCheck` logs it and proceeds with the merge (`:101-106`,
    contract at `:62-64`). Owner question 3 covers whether that should change.
  - Two things remain for TASK-012 (F9): earlier cascade files that were already staged when a
    later one is refused, and serialising the shared standalone tree. That is why TASK-012 is
    ordered after this task.
  - TASK-005 removes the read/checkout TOCTOU at its source: the cascade reads and writes the
    validated SHA.

> **Design-check note (2026-10-06) — what a refused cascade path leaves behind.**
> - The cascade writes paths in Go map order (`merge.go:646`). A refusal on path *k* returns after
>   paths 1..k-1 are already staged on the run branch. `applyCommitStatus` then runs
>   `_ = o.git.Checkout(ctx, authoritativeBranch)`, which carries those staged edits onto `main`'s
>   index or fails. Either way the standalone shared tree is left dirty, and with F9 nothing cleans
>   it, so later governed merges fail.
> - This task makes no new failure class here. At `61994fe` the same mid-loop failure is already
>   reachable through a committed symlinked directory (`git add` → "beyond a symbolic link"). The
>   fix belongs to TASK-012, which this task deliberately precedes. TASK-012's acceptance must
>   cover a multi-path cascade in which a later path is refused by `worktreefs`.
> - For a **single-path** cascade, the refusal happens before any filesystem change, so the tree
>   must end clean with `HEAD` on `main`. AC8 is amended to assert this.
> - The realistic trigger is a **committed** symlink on a run-branch tip pushed between the read
>   (`:606`) and the checkout (`:643`). An on-disk swap after the checkout would leave a dirty
>   typechange that makes the `main` checkout itself fail, so it tests a different (and
>   unrealistic) path. AC8 is amended accordingly.

### 5. Documentation (AC13)

- **`KNOWN-LIMITATIONS.md:71-74` "Path Traversal".** Replace line 72 with:
  > Worktree file I/O (artifact create/update and their rollbacks, acceptance/successor reads,
  > workflow create/update, the merge-time status cascade, renumber) goes through
  > `core/worktreefs`. Each path component is checked with `Lstat` from a directory handle opened
  > without following symlinks, inside an `os.Root`. Any symbolic link component is refused
  > (`invalid_params`), and so is any `.git` component (including git's case, NTFS and HFS
  > variants). New files are created `O_CREAT|O_EXCL` and updates replace by rename. A failed
  > create removes what it made.
  >
  > Residual: committed symlinks are still checked out as links (Spine does not set
  > `core.symlinks=false`), and reads through `git show` never follow them.
- **`governance/go-coding-guidelines.md` §4.1 (`:147-159`).** State that all worktree I/O uses
  `worktreefs.Tree`; never `os.*` on a repo-joined path; `safePath` is validation only.
- **`CHANGELOG.md` staging section.** Add a "Security" entry.

### 6. SMP: compatibility gate and the dependent task

**Gate: nothing legitimately writes through a symlink.**

1. **No write through a symlink succeeds today.** Re-checked on 2026-10-06 with overlay tests
   against `61994fe`:
   - a write under a symlinked parent dir fails at `git add` with "pathspec 'linkdir/new.md' is
     beyond a symbolic link" (artifact). The workflow sink fails the same way:
     "pathspec 'workflows/probe.yaml' is beyond a symbolic link";
   - an `Update` through a leaf symlink to a tracked file fails with "no changes added to commit";
   - a dangling leaf fails with "nothing to commit, working tree clean".
   - Every such write already ends in `ErrGit` (500) plus a rollback. This task moves the refusal
     before the side effect and turns it into `ErrInvalidParams` (400). No working flow is lost.
2. **SMP's write roots are all fresh gitclone worktrees.** SMP roots every Spine writer at one:
   - `internal/customer/spinecore/artifactservice.go:306-315`, `:447-452`;
   - `workflowwriter.go:335-349`;
   - `planningwriter.go:112-113`, which forces the in-place write.
   - In a worktree `.git` is a *file*, so I3 refuses no legitimate SMP path.
   - The clone root may sit behind symlinks (I5).
3. **Neither repository tracks a symlink** (`git ls-files -s`, mode `120000`: none in Spine or
   SMP).
4. **SMP already follows this rule elsewhere.**
   - Its seed backfill refuses to write through a symlinked `workflows/`
     (`internal/customer/workspace/filesystem_git_allocator_test.go:255-280`).
   - Its importer skips symlinks (`internal/import/repository_import_test.go:185`).
5. **Symlinked dirs SMP supports for reads are never on a write path.** SMP's profile inventory
   supports in-repo symlinked dirs (`workflows/steps -> ci-steps`,
   `.spine/agents -> ../workflows/agents`;
   `internal/customer/profileinventory/inventory_integration_test.go:153-177`).
   - It reads them through git trees, which is unaffected.
   - `workflow.Service` writes only `workflows/<id>.yaml`, so a symlinked `workflows/steps` never
     sits on a write path. A symlinked `workflows/` itself would, but that already fails today
     (item 1).

**API impact on SMP.**

- **Additive only:** the new package `core/worktreefs`.
- **No signature changes** (I6). SMP compiles unchanged against the task branch. AC12 includes an
  SMP build against it.
- **Status code changes** at SMP's embedded endpoints for unsafe paths, from 500 `git_error` to
  400 `invalid_params`:
  - `wrapEmbeddedError` → `httpStatusForSpineErrorCode`
    (`internal/customer/spineembedded/proxy.go:1607-1629`) already maps `ErrInvalidParams` to
    400 (`:1621-1622`).
  - SMP's own `RealGitOperator` implements `WriteAndStageFile` itself, buffering until
    `Commit` (`gitoperator.go:726-735`), so the cascade change does not reach SMP. That sink is
    SMP's own, and SMP TASK-004 (F5.smp-own) owns it.

**What SMP TASK-008 (F5.smp-embedded) must do after the Spine merge.**

1. Pick up this commit (the `replace ../spine` dev build, then the `.spine-revision` bump in SMP
   TASK-012). No code change is needed to compile.
2. Grep SMP tests and UI copy for expectations of `git_error` / 500 on these paths, and update
   them to 400 `invalid_params`.
3. Add embedded regression tests through the SMP surfaces:
   - document-type `CreateEntry` when `governance/<slug>.md` on the default branch is a dangling
     symlink → 400, and nothing created outside `<clone-root>/.worktrees/<hex>/wt`;
   - embedded workflow `Create` with a dangling `workflows/<id>.yaml` → 400, nothing outside;
   - `WriteContent` through a leaf symlink to an in-tree file → 400, and the target's mtime is
     unchanged.
4. Optionally move SMP's own sinks onto `worktreefs`:
   - the `Commit` write loop (`gitoperator.go:726-735`, if SMP TASK-004 shipped its own helper);
   - `WorkflowWriter.ActiveWorkflows` (`workflowwriter.go:79-107`), whose `os.ReadFile` on a
     `WalkDir` entry follows a symlinked `.yaml`.
   - SMP's `dev/lint-spine-internals` permits `core/...`.
5. Run SMP's full gates.

> **Design-check note (2026-10-06) — SMP follow-ups made firmer.**
> - **`ActiveWorkflows` is not optional.** `filepath.WalkDir` reports a symlinked
>   `workflows/x.yaml` as a non-directory entry, and `os.ReadFile` then follows it. A link to
>   `/dev/zero` on the default branch is an unbounded read in the shared `customer-app`, the same
>   OOM this note shows for Spine's workflow sink, and it runs before every admin workflow write
>   (binding-conflict check, `internal/customer/spineembedded/workflow_binding_overlap.go:94`). SMP TASK-004 (SMP-own sinks, no Spine dependency) or SMP
>   TASK-008 must make it skip non-regular entries and bound the read: `d.Type().IsRegular()`
>   plus a size cap now, `worktreefs.Tree.WalkRegularFiles` after the bump. Its acceptance needs a
>   `/dev/zero` row.
> - **Status-change list for SMP TASK-008 item 2:** 500 `git_error` → 400 `invalid_params`
>   (unsafe paths); 409 `already_exists` → 400 (Create at an in-root symlink to an existing file);
>   500 → 413 `payload_too_large` (a worktree file over the read cap on Update / Accept / Reject).
>   *Review round 1 correction (2026-10-07):* the before-state of the oversized case was wrong
>   for artifacts. Artifact Update / Accept / Reject of an existing worktree file over the read
>   cap **succeeded** before this task (no cap; yamlsafe bounds only the front matter), so it is
>   **2xx → 413**, a newly refused flow, not a status remap: SMP TASK-008 must look for callers
>   that expect success (`WriteContent`, accept/reject of a large pushed or imported task), not
>   only for 500 expectations. Workflow Update is **500 → 413** (yamlsafe's 64 KiB parse cap
>   already failed it). The cap applies to the existing file read, not to the new content.
> - **The `CreateEntry` regression (item 3)** also asserts that the refused planning run leaves no
>   run branch locally or on origin. `StartPlanningRun` deletes the branch when the writer's
>   `Create` fails (`core/engine/run.go:271-278`), and the test pins that.

### 7. Coordination and ordering

- **TASK-012 (F8/F9/P3-27)** builds on this task's `WriteAndStageFile`. It adds the per-repo
  working-tree serialisation and the reset/clean of partially staged cascades.
- **TASK-014 (F7/P3-6)** builds on this task's `workflow.Service.writeAndCommit` file-I/O lines.
  It adds the branch-protection call ahead of them.
- **TASK-002 (F10 refs)** also edits `adapters/git/cli.go`. This task touches only the
  `WriteAndStageFile` body (`:507-520`). If TASK-002 lands first with `--end-of-options` on
  `add`, keep both flags.
- **TASK-005 (F2 merge binding)** later pins the cascade to the validated SHA. This task's guard
  stays as defence in depth.
- **TASK-006 (F3)** owns merge-time path guards. Owner question 2 sends `.gitattributes` /
  `.gitmodules` there.
- **Dependencies:** none. Wave 2.

## Decisions

1. **DECIDED — Two layers: a per-component `Lstat` walk anchored to directory fds, plus
   `os.Root`.**
   - *Rejected: `os.Root` alone.* It follows in-root relative links, and the standalone main tree
     contains `.git/`. The 2026-10-06 probe created `.git/info/attributes` through `os.Root`.
   - *Rejected: patching `resolveToExistingAncestor` to `Lstat` the leaf.* It stays
     check-then-use, races, and still admits in-root links into `.git/`.
   - *Rejected: `Lstat` walk + `os.OpenFile(join, O_NOFOLLOW)`.* `O_NOFOLLOW` covers only the
     last component. The kernel's path walk still follows parent links that are swapped in after
     the check.
2. **DECIDED — Updates replace by `renameat` of an `O_EXCL` temp file and never truncate in
   place.**
   - *Rejected: `O_TRUNC` open of the leaf.* `os.Root` follows an in-root link that is swapped in
     at the leaf, and truncation happens before any post-open check can run.
   - Consequences:
     - the inode changes; git rehashes on stat mismatch;
     - the mode is preserved explicitly through fchmod;
     - a crash between create and rename can leave a `.spine-tmp-*` file (untracked, harmless).
3. **DECIDED — Refuse `.git` by name (git's variants), not by comparing against
   `git rev-parse --git-dir`.** With symlinks refused, a name is the only route into the git
   dir. A per-write `git` subprocess and inode comparison adds cost without coverage.
4. **DECIDED — Do not write blobs straight into the index (`git hash-object -w` +
   `update-index --cacheinfo`) instead of the working tree.** It would avoid touching the
   filesystem. But it leaves the standalone main tree's files stale against the index and `HEAD`,
   which breaks later checkouts there, and SMP's flows expect the file on disk.
5. **DECIDED — Do not set `core.symlinks=false` from Spine.** Spine does not own SMP's clones, and
   existing checkouts keep their links. The defence must hold however the tree was checked out.
   This stays a possible SMP-side hardening (Out of scope).
6. **DECIDED — The package is `core/worktreefs`, stdlib-only.**
   - *Rejected: `adapters/git/worktreefs`.* It adds a `core -> adapters` crossing that
     `dev/lint-boundaries.sh` calls debt.
   - *Rejected: `core/internal/...`.* SMP could not reuse it.
7. **DECIDED — Use `ErrInvalidParams` with a cause; add no new domain error code.** This matches
   the existing "path escapes repository" contract (400), changes no wire schema for SMP, and
   `errors.Is(…, worktreefs.ErrUnsafePath)` still distinguishes the cause.
   > **Design-check note (2026-10-06).** Still no *new* code. The size refusal (I7) uses the
   > existing `ErrPayloadTooLarge` (413), matching TASK-003's `BlobTooLargeError` mapping, so an
   > oversized file gets the same status whether it is read through `git show` or from the worktree.
8. **DECIDED — Guard renumber rather than descope it.** It is dormant (no caller in either repo
   at these heads), but the guard is cheap and stops the sink from coming back when a caller is
   wired.
9. **DECIDED — Keep `engine.GitOperator` unchanged.** SMP implements it. All validation sits
   inside `CLIClient.WriteAndStageFile`.

## Owner questions

The design works without answers to these, using the stated defaults.

1. **In-repo symlinked directories on a write path** (for example a repo whose `workflows/` is a
   link to `ci/workflows/`). Should Spine refuse the write, or resolve the link inside the root
   and write and stage the target path?
   - *Default:* refuse (I2).
   - git already refuses to stage beyond a symlink, so such writes fail today with a 500. Writing
     to the target would be a new feature with its own review questions: which path is
     "governed", and which path the projection reads.
2. **Git-meaningful dotfiles through the write API** (`.gitattributes`, `.gitmodules`,
   `.gitignore`). Should the artifact and workflow writers also refuse them?
   - *Default:* no change here; only `.git` itself is refused.
   - These are tracked content an author can push anyway. The control belongs in TASK-006's
     merge-time governance-path guard, and this task recommends adding `.gitattributes` and
     `.gitmodules` to that guard.
3. **The cascade hitting an unsafe path at merge time.** Should the governed merge fail
   permanently?
   - *Default:* keep today's semantics. The status rewrite is best-effort: the error is logged and
     the merge proceeds (`merge.go:62-64`, `:101-106`).
   - A symlink at a cascade path means tampering. But changing merge outcomes belongs to TASK-005
     / TASK-017, which bind the merge to the validated commit and own the cascade's contract.

## Acceptance Criteria

1. **New `core/worktreefs` package.**
   - It exists as specified in §1 and imports only the standard library.
   - `make lint-boundaries` passes with no new allowlist entry.
   - `go build ./cmd/embed-smoketest` passes.
2. **Lexical rules** (`TestClean`, table-driven).
   - Refused with `ReasonInvalid`: `""`, `"."`, `"/abs.md"`, `"a/../b.md"`, `"../x.md"`,
     `"a//b.md"`, `"a/./b.md"`, `"a\x00b.md"`.
   - Refused with `ReasonGitDir`: `.git`, `.git/x`, `a/.git/x`, `.GIT/x`, `.Git/x`, `.git./x`,
     `".git /x"`, `git~1/x`, `".g‌it/x"`.
   - Accepted: `.gitignore`, `.gitattributes`, `.github/x.md`, `a.git/x.md`, `git/x.md`,
     `governance/charter.md`.
   - Every refusal satisfies `errors.Is(err, ErrUnsafePath)`.
   - *Mutation check:* dropping the `.git` comparison, or the NTFS/HFS folding, fails the
     corresponding rows.
   - *Design-check addition (2026-10-06):*
     - more `ReasonGitDir` rows: `".git:x/y"` and `".git. :z/y"`;
     - more `ReasonInvalid` rows: a 4097-byte path, a 256-byte component, and `"a/"`;
     - the HFS row is written `".g‌it/x"`;
     - for every `ReasonInvalid` row, `err.Error()` does not contain the input string; it names
       only the offending component.
     - **Artifacts-dir row** (in `core/artifact`): with `WithArtifactsDir("spine")`,
       `Create("a/../../x.md")` is refused `ErrInvalidParams` and neither `x.md` nor `spine/x.md`
       exists afterwards. *Mutation check:* calling `Clean` only after `repoRelativePath` fails it.
3. **AC1 — symlink components refused** (`TestTree_RefusesSymlinkComponents`).
   - Each operation is covered: `Lstat`, `ReadFile`, `Create`, `Replace`, `WriteFile`, `Remove`.
   - Each link shape is covered: a leaf link (dangling absolute, dangling relative-escaping,
     relative in-root to an existing file, relative in-root into `.git/info/`) and an
     intermediate dir link (in-root and escaping).
   - Each returns `*PathError` with `Reason == ReasonSymlink` and `Component` equal to the exact
     repo-relative prefix.
   - A recursive snapshot (paths + contents + mtimes) of the tree and of a sibling "outside" dir
     is identical before and after.
   - *Mutation check:* treating a symlink like its target in the walk fails the in-root cases.
     (The escaping cases are still caught by `os.Root`; see AC5.)
   - *Design-check addition (2026-10-06):* the in-root link shapes must include the two that
     `os.Root` would **follow** under the per-directory sub-root, so the mutation check fails by
     side effect, not only by error shape. The mutation is "`Stat` instead of `Lstat` in the walk",
     which descends through a link whose target `SameFile` then agrees with.
     - an **intermediate link at the tree root** `gov -> .git/info` (with `.git/info` existing),
       for `Create("gov/attributes")` and `WriteFile`. Under the mutation,
       `.git/info/attributes` is created. With the fix it does not exist.
     - a **same-directory leaf link** `gov/link.md -> x.md` for `ReadFile`. Under the mutation the
       returned bytes are `x.md`'s. With the fix there are no bytes and the error is
       `ReasonSymlink`.
     - **Leaf links on the write operations** (`Create`, `Replace`, `WriteFile`, `Remove`) prove
       only the error shape. `O_EXCL`, `renameat` and `unlinkat` never follow a leaf link, so
       there the leaf check is defence in depth by construction. That is expected and is not a gap
       in the test.
4. **Race guard** (`TestTree_ComponentSwappedBetweenCheckAndOpen`).
   - Using `testHookBeforeLeafOp`, the test swaps a checked intermediate directory for a symlink
     (to an in-root dir and to `.git`) after its `Lstat`.
   - Expected: `ReasonChanged` (or `ReasonSymlink`), and nothing is written at either target.
   - *Mutation check:* removing the `os.SameFile` comparison fails this test.
   - *Design-check correction (2026-10-06):* the swap must be driven by
     `testHookAfterComponentLstat(op, prefix)`, which runs between a component's `Lstat` and its
     `OpenRoot`, not by `testHookBeforeLeafOp`. After the walk the held fd already names the
     checked inode, so a swap there cannot redirect the write. With the leaf hook the mutation
     check cannot fail (prototype-verified). The test performs the swap as
     `rename(gov, gov.bak); symlink(target, gov)` for target ∈ {`other` (in-root dir), `.git/info`}
     and asserts that `.git/info/attributes` and `other/<leaf>` do not exist afterwards.
   - *Design-check addition:* a **leaf-hook** row documents the post-walk case: swapping `gov` in
     `testHookBeforeLeafOp` leaves nothing in `.git/` or at the link target. The file lands in the
     renamed checked directory or the call fails with `ENOENT`.
5. **AC3 — `os.Root` is an independent backstop** (`TestTree_RootConfinementWithoutWalk`).
   - With `testHookSkipComponentChecks = true` (walk symlink checks disabled), absolute and
     escaping-relative leaf and dir links are still refused with an `os.Root` "path escapes"
     error, and nothing is written outside the root.
   - *Mutation check:* replacing `os.Root` calls with `os.OpenFile(filepath.Join(...))` fails it.
   - *Design-check correction (2026-10-06):* for `Create` (`O_CREATE|O_EXCL`), `os.Root` returns
     `EEXIST` for every leaf link, not "path escapes". The `Create` row expects
     `errors.Is(err, fs.ErrExist)`, or the `ReasonSymlink` that the post-`EEXIST` `Lstat` produces
     if the hook leaves that `Lstat` active. Every row asserts the side effect (nothing outside the
     root, no `.git/` change), not only the error text. The escaping-relative rows include a link
     that stays inside the tree but leaves the parent directory (`gov/sib -> ../other`), which the
     per-directory sub-root refuses.
   - *Design-check correction:* `testHookSkipComponentChecks` must disable **every** walk check:
     the symlink check, the not-a-directory check (`Lstat` of a link is not a directory, so it
     would refuse first with `ReasonNotDir`), the `SameFile` comparison, and the leaf `Lstat`
     checks. With only the symlink check skipped, an intermediate link never reaches `os.Root`,
     and AC5 proves nothing about the second layer.
6. **AC4 — exclusive create, rename-replace, nothing left behind.**
   - **(a)** `Create` on an existing regular leaf wraps `fs.ErrExist`, and the content and mtime
     are unchanged.
   - **(b)** A symlink swapped in at the leaf after the check (hook), pointing at an existing
     in-root file, is refused and its target is untouched.
     - *Mutation check:* `O_TRUNC` instead of `O_EXCL` fails (b).
     - *Design-check correction (2026-10-06):* the swapped link must point at a file **in the same
       directory** (`gov/new.md -> victim.md`). A target elsewhere in the tree
       (`../other/victim.md`) is refused by the parent sub-root even under `O_TRUNC`, so the
       mutation check would not fail. Prototype: `O_EXCL` gives `EEXIST`, then `ReasonSymlink`,
       and `victim.md` stays `"orig"`.
   - **(c)** An injected write failure after the leaf open, in a path that needs two new dirs,
     leaves neither the leaf nor the two dirs; pre-existing dirs survive.
     - *Mutation check:* removing the cleanup fails (c).
   - **(d)** `Undo.Rollback` removes exactly the created leaf and dirs.
   - **(e)** `Replace` keeps `0o755` and leaves no `.spine-tmp-*` file after success or after an
     injected failure.
   - **(f)** A leaf swapped for a symlink before `Replace`'s rename (hook) ends as a regular file;
     the link target is untouched (content and mtime).
7. **The audit PoCs become committed regression tests, each failing on `61994fe`.**
   - **`core/artifact/safepath_dangling_internal_test.go` (package `artifact`)**, ported from
     `poc/git/safepath_dangling_test.go` and `poc/parsing/safepath_dangling_test.go`:
     `safePathIn` refuses a dangling leaf link (`a.md`, `dir/a.md`, `dir/rel.md -> ../../outside/…`).
   - **`core/artifact/symlink_containment_test.go`**, ported from `poc/verify-r1-8` plus the new
     cases:
     - `TestF5_Create_DanglingSymlinkAbsolute_NoEscape` and
       `TestF5_Create_DanglingSymlinkRelative_NoEscape` (`TMPDIR` pinned; `../../../escaped.md`):
       no file outside the repo or worktree, no leaked `spine-wt-parent-*`, and
       `ErrInvalidParams` with `errors.Is(err, worktreefs.ErrUnsafePath)`;
     - `TestF5_Create_InRootSymlinkIntoDotGit_Refused`: `governance/attrs.md -> ../.git/info/attributes`
       in the standalone main tree, no write context; `.git/info/attributes` is not created;
     - `TestF5_Create_DotGitPath_RefusedBeforeWrite`;
     - `TestF5_Update_SymlinkLeafToTrackedFile_TargetUntouched`: the target's mtime does not move
       (no write-then-restore).
   - **`core/workflow/symlink_containment_test.go`:**
     - `TestF5_WorkflowCreate_DanglingLeaf_NoEscape`;
     - `TestF5_WorkflowCreate_SymlinkedWorkflowsDir_TypedRefusal`;
     - `TestF5_WorkflowUpdate_SymlinkToOutsideWorkflow_NotTouched` (mtime).
   - **`adapters/git/writeandstage_symlink_test.go`:**
     - `TestF5_WriteAndStageFile_SymlinkLeafToExistingOutside_Refused`;
     - `TestF5_WriteAndStageFile_TraversalAndDotGit_Refused` (`../x.md`, `a/../../x.md`,
       `.git/info/attributes`);
     - plus the guard `TestF5_WriteAndStageFile_RegularFile_ContentAndModePreserved`, which
       passes before and after.
   - **Fail-first evidence.** The Implementation Notes record a run of these files against
     `61994fe` (for example with `go test -overlay`) showing every test except the mode guard
     failing.
     - Design-phase drafts of these files compile against `61994fe` and were run on 2026-10-06:
       all failed as expected, and the mode guard passed. They are a starting point, not a
       mandate.
8. **The cascade sink under a read/checkout swap**
   (`core/engine`, `TestApplyCommitStatus_SymlinkSwappedAfterRead`).
   - Setup: a real `CLIClient` is wrapped so that, right after `Checkout(run.BranchName)`, the
     task file is replaced by a symlink to an existing outside file.
   - Expected:
     - the outside file's content and mtime are unchanged;
     - `applyCommitStatus` returns an error satisfying `errors.Is(err, worktreefs.ErrUnsafePath)`;
     - `preflightCheck` still proceeds to the merge (Owner question 3 default).
   - *Design-check correction (2026-10-06):*
     - **Setup.** The wrapper does not replace the file on disk after `Checkout`, because that
       dirty typechange makes the `main` checkout fail and tests the wrong path. Instead, inside
       `Checkout(run.BranchName)` and **before** delegating, it commits a symlink at
       `run.TaskPath` (pointing at an existing outside file) onto `run.BranchName`'s tip, using a
       side worktree or `hash-object` + `update-index` + `commit-tree` + `update-ref`. This is the
       real attack: a push between the read at `:606` and the checkout at `:643`.
     - **Additional expectations** for this single-path cascade:
       - `git status --porcelain` in the repository is empty after `applyCommitStatus` returns;
       - `git symbolic-ref HEAD` is `refs/heads/main`;
       - the outside file's mtime and content are unchanged.
     - **Fail-first:** on `61994fe` the outside file is overwritten with the status-rewritten
       content.
     - The multi-path partial-staging case is TASK-012's (see the §4 note).
9. **Every sink goes through `worktreefs`** (`TestNoUnconfinedWorktreeIO`, a `go/ast` scan of
   the non-test files of `core/artifact`, `core/workflow` and `adapters/git`).
   - It finds no call to `os.{WriteFile,ReadFile,OpenFile,Create,Mkdir,MkdirAll,Remove,RemoveAll,Rename,Stat,Lstat,ReadDir,Symlink,Chmod}`
     or `filepath.{Walk,WalkDir,EvalSymlinks}`.
   - Allowed exceptions:
     - `createAskpassScript` and `Close` in `adapters/git/cli.go` (askpass temp file);
     - `EnterBranch` in `adapters/git/branchwrite.go` (`MkdirTemp` / `RemoveAll` of the private
       worktree parent).
   - `resolveToExistingAncestor` is gone.
   - *Mutation check:* re-adding the `os.WriteFile` at the old `service.go:230` site fails this
     test.
   - *Design-check addition (2026-10-06):*
     - The scan also covers `core/engine`, `core/divergence` and `adapters/projection`. They have
       no filesystem calls at `61994fe`, so this costs nothing and stops a future sink appearing
       there.
     - The forbidden set also lists `os.{Truncate,Link,Chtimes,Chown,Lchown,DirFS,NewFile}`, every
       `io/ioutil` file function, and `syscall.{Open,Openat,Creat,Unlink,Unlinkat,Rename,Renameat,Mkdir,Mkdirat}`.
     - The `EnterBranch` exception stays safe: `os.RemoveAll` does not follow symlinks inside the
       worktree.
10. **Error contract.**
    - Unsafe paths return `domain.ErrInvalidParams` (400 from the standalone gateway,
      `service/internal/gateway/response.go:92-93`), with messages that hold only repo-relative
      paths. `TestSafePath_ErrorDoesNotLeakFilesystemPaths` gains a symlink case.
    - A Create on an existing regular file still returns `ErrAlreadyExists`.
    - An Update of a missing file still returns `ErrNotFound`.
    - A gateway test `POST /artifacts` with `write_context` on a branch carrying a planted
      dangling link returns 400 `invalid_params`, and no outside file exists.
    - *Design-check addition (2026-10-06):*
      - The existing `TestSafePath_ErrorDoesNotLeakFilesystemPaths` passes **unchanged** for
        `../../etc/passwd`. As first written, the `PathError` text quoted the raw input and would
        have failed it (see the §1 note).
      - A `Create` at an in-root symlink to an existing file now returns 400, not 409. A test pins
        this, and the change is recorded for SMP TASK-008.
11. **Rollback hygiene.**
    - A Create whose `StageAndCommit` fails (forced through a failing commit hook or a read-only
      index) leaves neither the file nor the directories it created.
    - An Update whose commit fails restores the original bytes and mode through `Replace`.
12. **No regression.**
    - Spine passes `go test ./...`, `go vet ./...` and `make lint` (golangci-lint incl. gosec,
      with the obsolete G703/G122 `//nolint` comments removed). The integration and scenario
      suites CI runs also pass.
    - No exported signature changes in `artifact`, `workflow`, `git` or `engine.GitOperator`.
    - No migration, environment variable or config.
    - SMP builds against the task branch: `go build ./... && go vet ./internal/customer/spinecore/... ./internal/customer/spineembedded/...`
      with `replace ../spine` at the branch head. The result is recorded in the Implementation
      Notes.
    - *Design-check addition (2026-10-06):* with the same `replace`, run
      `go test ./internal/customer/spinecore/... ./internal/customer/spineembedded/...` (the
      tests that need no Docker or Postgres; set `TEST_PLATFORM_DB` if it is available).
      - Either the run is green, or every failure is listed in the Implementation Notes as a
        status-code expectation (500→400, 409→400, 500→413) handed to SMP TASK-008.
        *(Review round 1: the oversized-file case is 2xx→413 for artifacts, 500→413 for
        workflows; see the §6 design-check note.)*
      - Any other failure blocks the merge. This catches an SMP break before the
        `.spine-revision` bump rather than after it.
13. **AC5 — documentation.**
    - `KNOWN-LIMITATIONS.md` §"Path Traversal" carries the §5 text, and the old line 72 claim is
      gone.
    - `governance/go-coding-guidelines.md` §4.1 is updated.
    - The `CHANGELOG.md` staging section has a Security entry.
14. **Audit PoC parity.** Run via `go test -overlay` against the merge commit, the original
    audit PoCs show the gap closed:
    - `poc/git` logs a non-nil error;
    - `poc/parsing` passes;
    - `poc/verify-r1-8` stops at its "NOT REPRODUCED" fatal for both variants.
    - Recorded in the Implementation Notes.
15. **Bounded worktree reads (I7)** — *added by the 2026-10-06 design check.*
    - **(a)** `core/workflow`, `TestF5_WorkflowUpdate_LeafLinkToDevZero_RefusedWithoutRead`:
      - Setup: `workflows/zero.yaml -> /dev/zero` is committed on the written branch.
      - Expected: `Update` returns `ErrInvalidParams` with `errors.Is(err, worktreefs.ErrUnsafePath)`
        within 2 s, and `runtime.MemStats.TotalAlloc` grows by less than 16 MiB across the call.
      - Fail-first: on `61994fe` it is still reading after 0.17 s at more than 256 MiB of heap (run
        it with a watchdog that ends the process).
    - **(b)** `core/worktreefs`, `TestTree_ReadFileOverLimit`:
      - A regular file of `limit+1` bytes gives `*SizeError` / `ErrTooLarge` without reading it
        (`TotalAlloc` under 1 MiB for `limit` = 4 MiB).
      - A file that grows past the limit after `Lstat` (hook) is still capped at `limit+1` bytes
        read.
    - **(c)** `core/artifact`: `Update` on a path whose worktree file is over the limit returns
      `domain.ErrPayloadTooLarge`, and a gateway test maps it to 413.
    - *Mutation check:* dropping the size pre-check or the `LimitReader` fails (b).
16. **Handle hygiene (I8)** — *added by the 2026-10-06 design check.*
    - `core/worktreefs`, `TestTree_NoHandleLeak` runs 1 000 mixed operations: successful and
      refused `Create`, `Replace`, `ReadFile`, `Remove`, and `Rollback` followed by `Release`.
    - Afterwards a package-internal open-handle counter is back to its starting value. On Linux, CI
      also checks that the `/proc/self/fd` count is unchanged.
    - Calling `Rollback` then `Release`, or `Release` twice, does not panic and closes nothing
      twice.

## Out of scope / follow-ups

- **Governance-path guard (TASK-006).** Recommend adding `.gitattributes` / `.gitmodules` to
  the merge-time guard (Owner question 2).
- **Working-tree serialisation (TASK-012).** Covers the standalone shared tree, the cleanup of
  partially staged cascades, and bounded merge retries.
- **Merge binding (TASK-005).** Binding the cascade read and the merge to the validated SHA
  removes the read/checkout TOCTOU at its source.
- **SMP's own sinks.** SMP TASK-004 (`gitoperator.go:726-735`) and SMP TASK-008 (adoption,
  embedded regression tests, the optional move of `WorkflowWriter.ActiveWorkflows` onto
  `worktreefs`).
- **`core.symlinks=false`.** Possible SMP-side hardening for gitclone clones (Decision 5); this
  is SMP's decision.
- **Operator CLI writes.** `service/internal/cli/initrepo.go` writes into a fresh directory the
  operator chooses at `spine init-repo` time. The path is operator-controlled, not
  attacker-reachable, and is not touched.
- **Read-side bounds.** Bounded reads of git blobs are TASK-003 (F4). `worktreefs.ReadFile` reads
  only Spine's own just-checked-out files.
  > **Design-check note (2026-10-06) — superseded in part.** "Just-checked-out" files are
  > attacker-sized: a run branch can carry a large compressible `.md`, and a link to `/dev/zero`
  > was an unbounded read in the workflow sink. TASK-003 bounds only `git` stdout, so worktree
  > reads are bounded **here** (I7, AC15). The one thing not bounded here is the on-disk size
  > `git worktree add` materialises for an oversized blob. That is a disk-pressure concern for SMP
  > TASK-004's per-blob push cap / `receive.maxInputSize` and is recorded there, not in this task.
- **`core/checkrunner` working directory** (*design-check addition*). `LocalCommandRunner`
  `os.Stat`s `req.WorkingDir` and runs a governed command in it
  (`core/checkrunner/local_command.go:212-232`). It writes no file and is not wired by `spine
  serve` or SMP at `61994fe`. It is a code-execution-plane concern, not an F5 sink, so it is
  excluded from AC9's scan scope deliberately.

## Design check

Adversarial check on 2026-10-06 against Spine `61994fe` and SMP `80995559`, read-only. The
designer's text is kept. Every change is marked *Design-check note / addition / correction
(2026-10-06)* in place.

**Verified, no change needed.**
- Every file:line reference in Purpose, Design §2–§6 and Decisions was re-read at the stated heads.
  Small drifts (±2 lines) were not worth amending.
- The drafted regressions (`_work/task004/regression`) compile and fail on `61994fe` as the design
  says. The mode guard is not part of that run.
- The audit PoCs reproduce (`poc/git` "GAP CONFIRMED", `poc/parsing` FAIL, `poc/verify-r1-8` both
  variants CONFIRMED).
- The compat probes (`_work/task004/probe`) confirm that writes through symlinks already fail with
  500 today, so the default for Owner question 1 loses no working flow.
- Neither repository tracks a symlink. SMP calls no `WithArtifactsDir`, does not call
  `CLIClient.WriteAndStageFile` (its own `RealGitOperator` buffers), and has no renumber caller.
- `os.Root` semantics and the sub-root behaviour were probed on go1.26
  (`scratchpad/wtfsprobe/`). A prototype of the §1b walk withstood a 60 019-iteration race against
  a goroutine flipping a directory between a real dir, an outside link and a `.git/info` link.

**Changed, and why.**
1. **A defect that breaks AC12:** `PathError` text echoed raw caller input, so the existing
   `TestSafePath_ErrorDoesNotLeakFilesystemPaths` would fail. `ReasonInvalid` messages now name
   only the offending component (§1 note, AC2, AC10).
2. **A bypass of the stated lexical rule:** with `artifacts_dir` set, `repoRelativePath`'s
   `filepath.Join` folds `..` before `Clean` sees it, so a write escapes the artifacts directory
   (inside the repo). The caller's path is now cleaned first, and the checked `rel` is the one
   staged (§2 note, AC2).
3. **An unbounded-read sink the design did not name:** workflow `Update` through a
   `/dev/zero` leaf link was demonstrated (more than 256 MiB in 0.17 s), and attacker-sized regular
   files are read whole. Added I7 / §1c bounded reads with the 413 mapping TASK-003 uses, and AC15.
   The leaf refusal closes the `/dev/zero` case. SMP's `ActiveWorkflows` has the same `/dev/zero`
   read, so its move is made **required** for SMP TASK-004/008.
4. **Unfalsifiable ACs:**
   - AC4's race hook was placed after the walk, where a swap cannot redirect the write. It moves
     between `Lstat` and `OpenRoot` (prototype-verified).
   - AC5 expected "path escapes" where `O_EXCL` yields `EEXIST`.
   - AC3 and AC6(b) used link targets that the per-directory sub-root already refuses, so their
     mutation checks could not fail. They are now same-directory / tree-root targets.
5. **An AC8 test that did not reproduce the attack:** an on-disk swap after checkout is not the
   attack. It is now a commit on the run-branch tip between read and checkout, with
   clean-tree / `HEAD`-on-`main` assertions for the single-path case. Partial staging in a
   multi-path cascade is flagged for TASK-012's acceptance.
6. **Cheap hardening:**
   - path length caps (the fd walk no longer hits `PATH_MAX` implicitly);
   - the `.git:` NTFS terminator, for parity with git;
   - `.git` skipped by `WalkRegularFiles`;
   - idempotent `Undo` and a handle-leak AC (AC16);
   - a wider AC9 scan (more packages and functions).
7. **Compatibility list completed:** 409→400 (Create at an in-root link to an existing file) and
   500→413 (oversized) are added to the status changes SMP TASK-008 must absorb. AC12 also runs
   SMP's `spinecore` / `spineembedded` unit tests against the branch before the bump.
   *(Review round 1 correction: oversized is 2xx→413 for artifact Update / Accept / Reject — a
   newly refused flow — and 500→413 for workflow Update.)*

**Coordination impact.** Still no hard dependency. I7's 4 MiB must equal TASK-003's
`git.DefaultMaxBlobBytes`. Whichever task lands second adds a test asserting the two constants are
equal (`worktreefs` is stdlib-only, so it cannot import the value). TASK-012's acceptance should add
the multi-path cascade refusal case.

**Not changed.** Owner questions 1–3 and their defaults stand. No new owner question: the read-cap
value is governed by TASK-003's owner question 1.

## Implementation Notes

Implemented 2026-10-07 on branch `init/INIT-023-TASK-004-worktree-write-containment` (from Spine
local `main` `ee3c249` = origin `61994fe` + the INIT-023 scaffold + merged TASK-001). TASK-001
did not touch any file this task changes (`git diff --stat 61994fe ee3c249 -- core/artifact
core/workflow adapters/git/cli.go adapters/git/branchwrite.go core/engine/merge.go` is empty), so
every "fails on 61994fe" claim below was measured against `main` and holds for `61994fe`.

### Orchestrator rulings (recorded as defaults pending the owner)

- **Owner question 1 — default taken: refuse.** A write through an in-repo symlinked directory
  (for example `workflows -> ci/wf`) is refused `invalid_params` (`ReasonSymlink`); it is never
  resolved to the target. Pinned by `TestF5_WorkflowCreate_SymlinkedWorkflowsDir_TypedRefusal`
  and the `dir in-root` rows of `TestTree_RefusesSymlinkComponents`.
- **Owner question 2 — default taken: only `.git` and its variants are refused.**
  `.gitignore`, `.gitattributes`, `.gitmodules`, `.github/…`, `a.git/…`, `git/…` stay writable
  (`TestClean` accepted rows). **Recommendation for TASK-006:** add `.gitattributes` and
  `.gitmodules` (any depth) to its merge-time governance-path guard.
- **Owner question 3 — default taken: best-effort.** A cascade path refused at merge time is
  logged by `preflightCheck` and the merge proceeds; `core/engine/merge.go` is unchanged
  (`TestPreflightCheck_SymlinkSwappedCascade_ProceedsToMerge`). TASK-005 / TASK-017 own any change.
- **Read cap = TASK-003's constant.** `worktreefs.DefaultMaxReadBytes = 4 << 20` is defined in
  `core/worktreefs` (stdlib-only, so it cannot import `adapters/git`). TASK-003 is not started:
  it should define `git.DefaultMaxBlobBytes` **as** `worktreefs.DefaultMaxReadBytes`
  (`adapters/git` may import `core/`, ADR-017) or add the equality test the design-check note
  asks for; TASK-003 owner question 1 then governs both.

### What was built (commits)

| Commit | Content |
|---|---|
| `dc8b73e` | `core/worktreefs` (path.go, tree.go) + `path_test.go`, `tree_test.go` |
| `08b5cf2` | `artifact.Service` (service/acceptance/successor/renumber) on worktreefs; artifact + gateway regression tests |
| `db11466` | `workflow.Service` on worktreefs; workflow regression tests (incl. `/dev/zero`) |
| `2db3aeb` | `CLIClient.WriteAndStageFile` on worktreefs + `--literal-pathspecs`; AC8 cascade tests; AC9 AST scan |
| `af73bc6` | KNOWN-LIMITATIONS, go-coding-guidelines §4.1, CHANGELOG |
| `3f7a7c3` | Test reorganisation so the PoC port compiles against `61994fe`; guards renamed |
| `946456f` | `Undo.Rollback` matches size + mtime of the written version (inode reuse found on Linux) — superseded by `d278b86` |
| `ae4fb95` | Review round 1: git's backslash spellings of `.git` refused by `Clean` |
| `3b1a1fc` | Review round 1: `ReadFileLimit` clamp; tests pinning the read and walk `SameFile` checks |
| `d278b86` | Review round 1: `Undo` pins the created leaf (identity-only rollback); O(1) handles per walk/Undo |
| `7ff33a4` | Review round 1: `TMPDIR` pinned in the absolute-link PoC port |
| `04e7486` | Review round 1: CHANGELOG / KNOWN-LIMITATIONS corrections (oversized file is 2xx→413 for artifacts) |
| `676814d` | Review round 1: I8 package doc states the handle bound |
| `9aba2fd` | Review round 2: `Undo.RollbackClaiming` + `git.TrackedInHEAD`; create rollbacks claim a hook-replaced file that is untracked in HEAD |
| `a4c6cac` | Review round 2: I8 names the transient Rollback / WalkRegularFiles handle peaks; the depth test measures them |
| `3b6f542` | Review round 2: CHANGELOG / KNOWN-LIMITATIONS / go-coding-guidelines for the two above |
| `55d6863` | Review round 2 (codex P2): update rollbacks re-create a file a failing hook deleted (`Tree.WriteFile`, original mode) |

- **`core/worktreefs`** (stdlib-only): `Clean`; `Open`/`Tree` with `Lstat`, `ReadFile`,
  `ReadFileLimit`, `Create` (+ `Undo.Rollback`/`Release`), `Replace`, `WriteFile`, `Remove`,
  `RemoveEmptyDir`, `WalkRegularFiles`; `PathError`/`ErrUnsafePath`; `SizeError`/`ErrTooLarge`;
  `DefaultMaxReadBytes`, `MaxPathBytes`, `MaxComponentBytes`. Every operation descends from a
  private duplicate of the root handle (`OpenRoot(".")`), `Lstat`s each component on the held
  handle (symlink / not-dir refused), opens it with `OpenRoot` and binds it with `os.SameFile`;
  leaf ops are single-component on the held parent. Errors carry only repo-relative paths (`relErr`
  rewrites the absolute names `os.Root` files carry).
- **`artifact.Service`**: `cleanPath` (absolute check, `Clean(path)` **before**
  `repoRelativePath`, then `Clean` of the joined path), `safePathIn` = `cleanPath` + `Tree.Lstat`
  (missing is fine), returning the repo-relative path that is also staged; `worktreeError` maps
  `PathError` → `ErrInvalidParams`, `SizeError` → `ErrPayloadTooLarge`, else logged + generic
  `ErrInternal`; `writeAndCommit` validates lexically before `enterBranch`, then
  `Lstat`/`ReadFile`/`Replace` (update) or `Create` (create), rollback via `Replace(original)` /
  `undo.Rollback()`; `readRepoFile` for acceptance/successor; renumber on the tree.
  `resolveToExistingAncestor`, the `EvalSymlinks` root resolution and the G703 `//nolint` are gone.
- **`workflow.Service`**: `Clean` before `enterBranch`; one capped `ReadFile` serves the prior
  parse and the rollback copy (`readFromDisk` → `parseExisting`); `Create`/`Replace`/rollback as
  above; local `worktreeError` with the same contract.
- **`CLIClient.WriteAndStageFile`**: exactly the design §4 body (`Clean`, `Open`, `WriteFile`,
  `git --literal-pathspecs add -- rel`). `core/engine` unchanged.

### Per-AC evidence

1. **AC1** — `core/worktreefs` exists; `go list -deps ./core/worktreefs` lists only the standard
   library; `./dev/lint-boundaries.sh` → `OK` with no allowlist change; `go build -o
   bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` → `library surface is
   sound`. `GOOS=linux` and `GOOS=windows go vet ./core/worktreefs/` clean.
2. **AC2** — `TestClean`: every listed `ReasonInvalid` row (incl. 4097-byte path, 256-byte
   component, `"a/"`), every `ReasonGitDir` row (incl. `".git:x/y"`, `".git. :z/y"`, the HFS row
   written `".g‌it/x"`, plus `"﻿.git/x"`, `GIT~1/x`), every accepted row; all refusals
   `errors.Is(err, ErrUnsafePath)`; no `ReasonInvalid` message contains its input.
   Artifacts-dir row: `TestF5_Create_ArtifactsDirTraversal_Refused` (+ internal
   `TestCleanPath_ArtifactsDirTraversal`). Mutation checks (all run with `go test -overlay`, all
   **killed**): dropping the `.git` comparison, the HFS fold, the NTFS trailing-dot/space strip;
   cleaning only after `repoRelativePath`.
3. **AC3 (design AC1)** — `TestTree_RefusesSymlinkComponents`: 12 link shapes × 7 operations (84
   subtests) incl. the design-check rows `gitlink -> .git/info` (Create) and `gov/samedir.md ->
   x.md` (ReadFile asserts no bytes); `Reason == ReasonSymlink`, exact `Component`; recursive
   before/after snapshot (paths, contents, link targets, modes, mtimes, directories included) of
   the tree and the outside dir. Mutation "`Stat` instead of `Lstat` in the walk" **killed by side
   effect**: `.git/info/attributes` added, `ReadFile returned bytes through a link: x`.
4. **AC4** — `TestTree_ComponentSwappedBetweenCheckAndOpen` uses `testHookAfterComponentLstat`
   (`rename(gov, gov.bak); symlink(target, gov)`, targets `other` and `.git/info`; Create, Replace,
   WriteFile): `ReasonChanged`, nothing written at either target. Mutation "remove the `SameFile`
   comparison" **killed** (the swap writes `other/attributes` / `.git/info/attributes`).
   Leaf-hook row: `TestTree_ComponentSwappedAfterWalk` (lands in `gov.bak`, never in `.git/`).
5. **AC5 (design AC3)** — `TestTree_RootConfinementWithoutWalk`, 15 rows with
   `testHookSkipComponentChecks` disabling every walk check (symlink, not-dir, `SameFile`, leaf
   `Lstat`/regular/size): Create on leaf links → `fs.ErrExist`; dir links, `gov/sib -> ../other`,
   leaf reads → "path escapes from parent"; Replace/Remove on a leaf link replace/unlink the link
   entry only. Every row asserts no change outside the root, in `.git/` or in `other/`.
   `TestTree_RootAloneFollowsInRootLinks` pins why the walk is needed (with the walk off, os.Root
   follows `gitlink -> .git/info`). *Review round 1:* the "replace `os.Root` with
   `os.OpenFile(filepath.Join(...))`" mutation **was run** (`openSubRoot` →
   `os.OpenRoot(filepath.Join(parent.Name(), name))`, `openFileIn` →
   `os.OpenFile(filepath.Join(parent.Name(), name), …)`, via `-overlay`): **killed**, 10 of 15 rows
   fail by side effect (every dir-link row and every `ReadFile` row). The 5 surviving rows are the
   leaf-link rows of `Create` (×3), `Replace` and `Remove`, where `O_EXCL`, `renameat` and
   `unlinkat` never follow a leaf link whatever the opener — the design's "defence in depth by
   construction" case (AC3 note), not a gap.
6. **AC6** — (a) `TestTree_CreateExistingLeaf`; (b) `TestTree_CreateLeafSwappedForLinkAfterCheck`
   (same-directory `gov/new.md -> victim.md`; mutation `O_TRUNC` for `O_EXCL` **killed**);
   (c) `TestTree_CreateWriteFailureRemovesLeafAndDirs` (`testHookAfterLeafOpen`; mutation "no
   deferred Rollback" **killed**); (d) `TestTree_UndoRollback` (exact leaf + dirs; a dir that
   gained content is kept; a replaced leaf is kept — same and different size; usable after
   `Tree.Close`); (e) `TestTree_ReplaceKeepsModeNoTemp` (0755 kept, no `.spine-tmp-*` after
   success or injected failure); (f) `TestTree_ReplaceLeafSwappedBeforeRename`.
7. **AC7** — the four files exist with the named tests (plus `TestF5_Create_SymlinkedParentDir_NoEscape`,
   `TestF5_Create_InRootSymlinkToExistingFile_Is400Not409`, `TestF5_WriteAndStageFile_SymlinkedParentDir_Refused`).
   **Fail-first:** the committed test files plus `core/worktreefs` (as a library only) copied into
   a `git archive main` export: **every `TestF5_*` fails** except the designated
   `TestF5_WriteAndStageFile_RegularFile_ContentAndModePreserved` mode guard (passes). The three
   guards that pass before and after (`TestWorktreeContract_ExistingAndMissing`,
   `TestUpdate_CommitFailure_RestoresBytesAndMode`, `TestWorkflowUpdate_CommitFailure_RestoresBytes`)
   carry no `TestF5_` prefix. The same run also fails the AC8, AC9, gateway, renumber and
   `TestWriteAndStageFile_LiteralPathspec` tests on `main`. *Review round 1:* the three
   `TestF5_*` tests it added (`…_BackslashDotGitPath_RefusedBeforeWrite`,
   `…_CommitFailure_HookRewritesFile_StillRemoved`, and the `TMPDIR`-pinned
   `…_DanglingSymlinkAbsolute_NoEscape`) fail on the same `main` export too.
8. **AC8** — `TestApplyCommitStatus_SymlinkSwappedAfterRead`: a real `CLIClient` wrapped so that
   inside `Checkout(run.BranchName)` and before delegating it commits `run.TaskPath` → outside
   victim onto the branch tip with plumbing (`hash-object`, private `GIT_INDEX_FILE`,
   `update-index --cacheinfo 120000`, `write-tree`, `commit-tree`, `update-ref`). Asserts
   `errors.Is(err, worktreefs.ErrUnsafePath)`, victim content + mtime unchanged, `git status
   --porcelain` empty, `git symbolic-ref HEAD` = `refs/heads/main`. Split from the preflight half
   (`TestPreflightCheck_SymlinkSwappedCascade_ProceedsToMerge`, fresh scenario: in one test the
   second cascade would re-read the already-swapped blob and fail at the read, not the write).
   Fail-first on `main`: both fail (victim overwritten).
9. **AC9** — `core/worktreefs/scan_test.go` `TestNoUnconfinedWorktreeIO` scans the non-test
   files of `core/artifact`, `core/workflow`, `adapters/git`, `core/engine`, `core/divergence`,
   `adapters/projection` (import-alias aware, dot-imports flagged) for the design set plus
   `os.{Open,CreateTemp,MkdirTemp,Readlink,OpenRoot,OpenInRoot}`, `filepath.Glob`,
   `syscall.{Rmdir,Symlink,Link,Truncate,Chmod}`; exceptions `createAskpassScript`,
   `CLIClient.Close`, `EnterBranch`; asserts `resolveToExistingAncestor` is gone. On `main` it
   lists all 32 sinks. Mutation (re-adding `os.WriteFile` in `writeAndCommit`) **killed** — run as
   a real temporary edit, restored byte-identical, because the scan parses source from disk and
   `-overlay` does not apply to it.
10. **AC10** — unsafe paths → `ErrInvalidParams` with repo-relative text; the existing
    `TestSafePath_ErrorDoesNotLeakFilesystemPaths` passes **unchanged**; symlink case added as
    `TestSafePath_SymlinkErrorDoesNotLeakFilesystemPaths`; `ErrAlreadyExists` / `ErrNotFound`
    pinned (`TestWorktreeContract_ExistingAndMissing`); 409→400 pinned
    (`TestF5_Create_InRootSymlinkToExistingFile_Is400Not409`); gateway
    `TestF5_Gateway_CreateThroughDanglingSymlink_400` (`POST /api/v1/artifacts` with
    `write_context` on a planning run whose branch carries the link → 400 `invalid_params`, no
    outside file, no absolute path in the message).
11. **AC11** — `TestF5_Create_CommitFailure_RemovesFileAndCreatedDirs` (failing `pre-commit`
    hook; file and both created dirs gone, pre-existing dir kept, `git status` clean; fails on
    `main` — dirs leaked), `TestUpdate_CommitFailure_RestoresBytesAndMode` (0755 + bytes, no temp
    file); workflow equivalents. *Review round 1:* plus
    `TestF5_Create_CommitFailure_HookRewritesFile_StillRemoved` (the hook appends to the staged
    file before failing; file and dirs gone, a retry fails the same way, not `already_exists`;
    fails on `main` — dirs leaked — and on `82a55dc` — file left behind). *Review round 2:* plus
    `TestF5_Create_CommitFailure_HookReplacesFileByRename_Removed` and the workflow equivalent
    (the hook replaces the staged file through a temp file and `mv`; file and dirs gone, the
    retry is not `already_exists`; fail on `main` — dirs leaked — and on `524d306` — file left
    behind), `TestF5_Create_CommitFailure_HookCommitsReplacedFile_Kept` and the workflow
    equivalent (a file tracked in HEAD by the time of the rollback is kept; fail on `main`, which
    removed it by name), and for updates `TestUpdate_CommitFailure_HookDeletesFile_Restored`,
    `TestWorkflowUpdate_CommitFailure_HookDeletesFile_Restored` and
    `TestF5_Update_CommitFailure_HookPlantsLink_NotFollowed` (see Review round 2).
12. **AC12** — see Gates. *Review round 1 reading of "`make lint` passes":* `make lint` runs
    `golangci-lint run` over the whole module, which is red on `main` too (324 pre-existing
    issues: gofmt, errcheck, gocritic, staticcheck, one gosec). The criterion is met as "no new
    issues": `golangci-lint run --new-from-rev main ./...` → 0 issues; whole module 319 on the
    branch vs 324 on a `git archive main` export; `make lint-security`
    (`--enable-only=gosec ./...`) → 0 issues (main: 1, removed here). No exported signature
    changed outside the new package (`git diff main...HEAD` on non-test files: only unexported
    funcs change); no migration, env var or config. The obsolete G703/G122 `//nolint` comments
    are gone. *Review round 2:* one exported **addition** outside the new package,
    `git.TrackedInHEAD` (plus `worktreefs.Undo.RollbackClaiming` inside it); no exported
    signature changed. Whole module still 319 on the branch vs 324 on `main`.
13. **AC13** — KNOWN-LIMITATIONS "Path Traversal" carries the §5 text (plus the read cap); the old
    claim is gone; go-coding-guidelines §4.1 rewritten (and the `EvalSymlinks` anti-pattern row);
    CHANGELOG staging section has the TASK-004 Security / Changed / For embedders entry.
14. **AC14** — original audit PoCs via `go test -overlay` (overlay mapping the three PoC files
    into `core/artifact/`) against the branch head (the merge commit does not exist yet; the
    orchestrator re-runs it there): `poc/git` logs `err=invalid_params: unsafe path: component
    "a.md" is a symbolic link`; `poc/parsing` **passes**; `poc/verify-r1-8` stops at
    `NOT REPRODUCED` for both the absolute and relative variants. Re-run after review round 1 at
    `04e7486` with the same result, and after review round 2 at `55d6863` with the same result
    (`poc/git` and `poc/parsing` pass with the non-nil refusal logged). **Still partial:** it
    must be re-run on the merge commit.
15. **AC15** — (a) `TestF5_WorkflowUpdate_LeafLinkToDevZero_RefusedWithoutRead`: refused
    `ErrInvalidParams`/`ErrUnsafePath` in < 0.1 s, `TotalAlloc` growth < 16 MiB; on `main` its
    watchdog ends the process at 0.19 s with HeapAlloc 264 MiB. (b) `TestTree_ReadFileOverLimit`:
    a `limit+1` file is refused without being read (`TotalAlloc` < 1 MiB); a file grown after the
    `Lstat` is read exactly `limit+1` bytes (`testHookObserveRead`); mutations "drop the size
    pre-check" and "drop the LimitReader" both **killed**. (c)
    `TestF5_Update_OversizedWorktreeFile_PayloadTooLarge` + gateway
    `TestF5_Gateway_UpdateOversizedWorktreeFile_413` (413 `payload_too_large`); workflow
    `TestF5_WorkflowUpdate_OversizedFile_PayloadTooLarge`.
16. **AC16** — `TestTree_NoHandleLeak`: 1 000 mixed operations; the package handle counter
    returns to its baseline (a double close would drive it below); `Rollback`+`Release`, double
    `Release`, nil `Undo` are no-ops. Run on Linux (below) the `/proc/self/fd` count is unchanged.
    *Review round 2:* `TestTree_HandlesBoundedByDepth` also measures the transient peaks I8 now
    names (Rollback ≤ 2 + created − 1; WalkRegularFiles ≤ depth + 2).

### Deviations and design points resolved against the real code

1. **`safePathIn` returns the repo-relative path**, not `filepath.Join(root, rel)`: it is what the
   caller stages, so the checked path is the staged path; no production caller used the absolute
   form for I/O any more. The PoC ports only check the error, `TestSafePath_Valid` still passes.
2. **Lexical validation moved before `enterBranch`** (artifact and workflow), so a lexically
   refused path never creates a worktree; the filesystem check still runs on the worktree.
3. **`Create` on an existing non-regular leaf** (a directory) is `ReasonNotRegular` (400), per the
   design's leaf rule; it was 409 before. Listed with the other status changes in the CHANGELOG.
4. **Added `Tree.RemoveEmptyDir`** (exported, additive): the design's API had no way for
   `cleanEmptyParents` to remove empty directories "through the tree".
5. **`WalkRegularFiles` is an fd-anchored recursive walk** (each directory opened by `OpenRoot`
   from its held parent + `SameFile`, entries `Lstat`ed on the held handle, sorted) instead of
   `fs.WalkDir(Root.FS())`: `Root.FS()` re-resolves each directory by name from the root and
   follows an in-root link swapped in mid-walk. Every read/write re-walks anyway; this keeps I1
   for enumeration too.
6. **Each operation starts from a private `OpenRoot(".")` duplicate of the root**, so an `Undo`
   owns all its handles and stays usable after `Tree.Close` (tested).
7. **Generic `ErrInternal` text for non-path I/O failures** (`"<action> <path> failed"`, cause
   logged server-side): the old messages embedded `os` errors with absolute paths.
8. **Rollback identity** (found by the Linux run): `os.SameFile` alone is unsafe after inode
   reuse, so after a successful write `Undo` also matched size + mtime (commit `946456f`).
   *Superseded in review round 1 (`d278b86`):* the size/mtime match also kept a file a failing
   hook had rewritten in place. `Create` now pins the created inode with a read-only descriptor
   the `Undo` holds until it is resolved; an inode that is still open cannot be freed, so its
   number cannot be reused and `os.SameFile` alone is exact. On Linux the "drop the pin" mutation
   is **killed** by all three replaced-leaf rows (incl. a same-size, same-mtime replacement); on
   macOS it survives (APFS does not reuse an inode number at once).
9. **Extra test hooks**: `testHookAfterLeafOpen` (failure injection, AC6(c)/(e)) and
   `testHookObserveRead` (AC15(b) byte count), beside the design's three.
10. **Guard tests renamed** off the `TestF5_` prefix (see AC7).

### Gates (final head; every command run from the Spine worktree unless noted)

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh` | `lint-boundaries: OK` |
| `make build` | ok |
| `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `engine constructed and exercised; library surface is sound.` |
| `go test -p 4 -count=1 ./...` | rc 0, 39 packages `ok`, no FAIL |
| `go vet ./...`; `go vet -tags integration ./...`; `go vet -tags scenario ./...` | all clean |
| `go test -race -p 4 -count=1 ./core/worktreefs/ ./core/artifact/ ./core/workflow/ ./adapters/git/... ./core/engine/ ./service/internal/gateway/` | all `ok` |
| Linux: same packages, `-race -p 4`, ×3, in `golang:1.26-bookworm` (`docker run --rm`, worktree + module cache mounted read-only, `GOPROXY=off`) | all `ok` ×3 (after `946456f`; before it, the replaced-leaf rollback row failed — deviation 8); `TestTree_NoHandleLeak` with the `/proc/self/fd` check passes |
| `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (CI scenario-git-http job), DB `s004_scen` | rc 0, 19 `TestGitHTTP_*` PASS, no SKIP |
| Full scenario suites `go test -tags scenario -count=1 -p 1 -v ./service/scenariotest/...` on the branch (`s004_scen`; run at `af73bc6` and again at `946456f`) **and** on a `git archive main` export (`s004_scenmain`) | all rc 1 with **identical** per-test results (1 538 PASS/FAIL/SKIP lines incl. subtests: 190 PASS, 30 FAIL, 6 SKIP top-level); the 30 failures are pre-existing on `main` (fixture `validation_failed`, `TestSchemaMatchesProduction`; CI runs only `TestGitHTTP_*` for this reason) |
| `go test -tags integration -count=1 ./adapters/projection/` (`s004_proj`; no touched package has integration-tagged tests) | `ok` |
| `golangci-lint run` on `core/artifact core/workflow adapters/git/... core/engine service/internal/gateway` (+ `core/worktreefs`) vs the same on the `main` export, normalised without line numbers | **0 new issues**; 5 fewer (the workflow G703 gosec finding, a `renumber.go` errcheck, three gofmt import-order findings in touched files); `core/worktreefs` 0 issues |
| `golangci-lint run --enable-only=gosec ./...` | branch **0 issues** (main: 1, the workflow G703 this task removes) |
| `gofmt -l` on every changed `.go` file (`git diff main...HEAD --name-only -- '*.go'`) | empty |
| `codex review --base main` (round 1 after `3f7a7c3`, round 2 after `946456f`) | both: "No actionable regressions were identified" — no P1/P2/P3 findings to fix or reject |

### SMP compatibility gate

SMP checkout `/Users/bartoszszymichowski/42sid/i23/s-T004/smp` (detached at SMP `main` `7d4e7cc9`,
`../spine` = this worktree, no `vendor/`, `go.mod`/`go.sum` unchanged after every run). Build,
vet, unit and Spine-runtime suites were run twice, at `3f7a7c3` and again at `946456f` (same
results); the platform-DB suites at `3f7a7c3`:

- `go build ./... && go vet ./... && go vet -tags integration ./...` → OK.
- `go test -p 4 -count=1` on the **37** SMP packages whose test dependency graph contains
  `core/artifact`, `core/workflow`, `adapters/git` or `core/worktreefs` (`go list -test -deps`;
  the 6 direct importers are `executionreadiness`, `projectionwriter`, `proposals`, `spinecore`,
  `spineembedded`, `workspace`), DB env unset → all `ok` (one `[no test files]`).
- CI's "Spine runtime integration suites" step through `go run ./dev/testgate` with
  `TEST_SPINE_DB` = `s004_spine_rt` (Spine runtime migrations) and `TEST_PLATFORM_DB` =
  `s004_smp_it` (platform migrations + `vector`), `-race -count=1 -p 4` on `spinecore`,
  `spineembedded`, `runner`, `executionreadiness` → **PASS: 1 536 tests passed, 0 failed**, 4
  allowed skips (the parked `TestRealGitOperator_Promoted_FullSequence` and three
  `TEST_PROVISIONING_DSN` dispatch-store tests).
- Platform-DB suites of the dependent packages that need no provisioning DSN (`automation`,
  `brain`, `discovery`, `notification`, `profileinventory`, `provenance`, `opshealth`,
  `recovery`) through testgate → 707 passed, 0 failed; testgate exits 1 only on the
  `TestObjectsCopyModeLiveMinIO` skip (no MinIO here).
- No SMP status-code expectation broke, so nothing had to be listed for SMP TASK-008 beyond the
  CHANGELOG list (500→400, 409→400; oversized existing file: artifact 2xx→413, workflow
  500→413 — corrected in review round 1, see below).

### Not run (exact commands)

- `TEST_PROVISIONING_DSN`-gated SMP suites (`internal/customer/workspace`, `spineruntime`,
  `eventdelivery`, `projectionwriter`, `workspace/workspacetest`, the three spinecore
  dispatch-store tests, the three `internal/recovery` provisioning tests): they allocate one
  database per test with names outside the `s004_*` namespace this task may create.
  `TEST_PROVISIONING_DSN=postgres://smp_prov:…@localhost:5432/postgres go run ./dev/testgate --
  -race -count=1 ./internal/customer/workspace/ ./internal/customer/spineruntime/
  ./internal/customer/eventdelivery/ ./internal/customer/projectionwriter/
  ./internal/customer/workspace/workspacetest/` (SMP CI runs them).
- `TestObjectsCopyModeLiveMinIO` (needs `TEST_MINIO_*`).
- AC14 against the merge commit: run against the branch head; re-run with the same overlay after
  the merge.
- Databases used: `s004_scen`, `s004_scenmain`, `s004_scenh` (unused), `s004_proj`, `s004_smp_it`,
  `s004_spine_rt` on the shared PostgreSQL 17.10 (CI uses 18); all dropped after the gates.

### Review round 1 (2026-10-07)

Three reviewers (correctness + SMP compatibility, adversarial security, completeness/tests)
examined `82a55dc`. Each finding was first reproduced or checked against the code. Commits
`ae4fb95`…`676814d` (see the commit table) carry the fixes. Every new test was shown to fail
against the code it guards (go test `-overlay` mutants, or a `git archive main` export for the
`TestF5_*` additions).

**Findings.**

| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | P2 | CHANGELOG / handoff list said the 4 MiB read cap turned artifact Update / accept / reject from 500 into 413 | **Fixed** (`04e7486`, this commit). Verified: on `main` these calls succeeded, because there was no cap and yamlsafe bounds only the front matter. The CHANGELOG now reads **2xx → 413 (a newly refused flow)** for artifact Update / accept / reject, and **500 → 413** for workflow Update, where yamlsafe's 64 KiB parse cap already failed the file. It also says the cap applies to the existing file, not to the new content, and that a successor's best-effort `follow_up_from` link is skipped and logged. The "For embedders" note tells SMP TASK-008 to look for callers that expect success. KNOWN-LIMITATIONS names the refused flow. The §6 design-check note, the AC12 note, design-check item 7 and the SMP-gate line carry *review round 1 corrections*. The behaviour itself is unchanged: AC15(c) requires it, and it matches TASK-003's blob cap. |
| 2 | P3 | `Undo.Rollback`'s size+mtime match left a file a failing hook had rewritten in place, plus its directories, so the retry became `already_exists` | **Fixed** (`d278b86`). Rather than return to the inode-only match (which reopens the inode-reuse hole `946456f` closed; the reviewer was right that the window is short, but the standalone shared tree can still interleave there) or check "not tracked in HEAD" (worktreefs is stdlib-only and knows no git), `Create` now **pins** the created inode with a read-only descriptor that the `Undo` holds until it is resolved. An open inode cannot be freed, so its number cannot be reused, and `os.SameFile` alone is exact: an in-place rewrite is removed and a re-created file never matches. Tests: `TestTree_UndoRollback/removes_a_leaf_modified_in_place_since`, a `same size and mtime` replacement row, `TestTree_CreateLeafSwappedBeforePin`, and `TestF5_Create_CommitFailure_HookRewritesFile_StillRemoved` (AC11 with a hook that appends to the staged file; fails on `82a55dc` and on `main`). Linux: dropping the pin fails all three replaced-leaf rows. `Create`'s `perm` must now leave the file readable by the process; every caller uses `0o644`, and `git add` must read the file anyway. |
| 3 | P3 | `isGitDirName` did not stop at `\`, so `.git\x.md`, `GIT~1\x.md` and `governance/.git\x.md` passed `Clean` and became a 500 after a write | **Fixed** (`ae4fb95`). Verified with git 2.54 `git add`: with core.protectNTFS on, git refuses `.git\x.md`, `GIT~1\x.md`, `a\.git`, `a:b\.git`, `x\git~1.`, `x\.git.\y` and `x\.GIT :s`, and accepts `a\b.md`, `.github\x.md` and `a\.gitignore`. Each component is now split at `\` **before** the `:` cut (otherwise `a:b\.git` is cut to `a`). The doc comment is corrected. Tests: `TestClean` backslash rows, `TestClean_BackslashGitDirNamesComponent`, and `TestF5_Create_BackslashDotGitPath_RefusedBeforeWrite` (400 `ReasonGitDir`, clean working tree). The old `path.go` fails all of them, and the ":-cut-first" mutant fails the `a:b\.git` row. |
| 4 | P3 | `ReadFileLimit(…, math.MaxInt64)` overflowed `limit+1` and returned empty content with a nil error | **Fixed** (`3b1a1fc`): the limit is clamped to `math.MaxInt64-1`. Test: `TestTree_ReadFileOverLimit/math.MaxInt64_limit_reads_the_whole_file`; the mutant without the clamp returns `""`. |
| 5 | P3 | Descriptor amplification: one `os.Root` per component held by the `Undo` across the commit | **Fixed** (`d278b86`). A walk now keeps only `cur` and, if it created directories, `anchor` (the parent of the first one). Intermediate handles are closed as the descent passes them. Rollback re-opens the created levels from `anchor`, each bound by Lstat (directory, not a link) and `os.SameFile` to the identity recorded on the way down (`openBound`), and closes them again. Measured at 2 044 components: walk peak 2 (was 2 044) and `Undo` holds 3 (was 2 045). Test: `TestTree_HandlesBoundedByDepth` (300 components: ≤ 2 during the walk, ≤ 3 held by the `Undo` (≤ 2 when no directory is created), baseline after `Rollback`); the pre-review `tree.go` fails it. `TestTree_UndoRollback/does_not_follow_a_created_dir_swapped_for_a_link` covers the re-walk. **No component-count cap added in `Clean`**: the cost left is O(depth) syscalls while the request runs, and on Linux it is the same order as `main` (2 040–2 044 levels, Linux container: branch create 1.06 s / rollback 0.24 s; `main`'s `os.MkdirAll`+`os.WriteFile` 0.57 s / `RemoveAll` 0.14 s). A cap would be a new refusal for embedder input with no security gain left (initiative: opt-in or provably identical). APFS is slow at this depth (create 7.7 s, rollback 16 s; the pre-review code was 8.0 s / 13.9 s). There `main` refused such paths with ENAMETOOLONG (macOS PATH_MAX 1024 on the absolute path), but macOS is not a deployment target. |
| 6 | P2 | Same as 1, from the second reviewer (gateway test gets HTTP 200 on `main`) | **Fixed** with 1. The reviewer's observation is correct: on `main`, `TestF5_Gateway_UpdateOversizedWorktreeFile_413` gets 200, which is why that test is `TestF5_` (fails on `main`). |
| 7 | P3 | No test for `ReadFileLimit`'s post-open `os.SameFile` | **Fixed** (`3b1a1fc`): `TestTree_ReadLeafSwappedForLinkAfterCheck`. The mutant without the check returns `"secret"`. |
| 8 | P3 | No test for `walkDir`'s per-directory `os.SameFile` | **Fixed** (`3b1a1fc`). `walkDir` calls `testHookAfterComponentLstat("walk", rel)` between Lstat and OpenRoot. `TestTree_WalkSubdirSwappedForLinkMidWalk` swaps `gov` for a link to `.git/info`; the mutant without the check yields `gov/exclude` with no error. |
| 9 | P3 | `TestF5_Create_DanglingSymlinkAbsolute_NoEscape` did not pin `TMPDIR` or check for leaked `spine-wt-parent-*` | **Fixed** (`7ff33a4`). It still fails on `main` (outside file written). |

**Acceptance-criteria gaps.**

- **AC5 mutation not run / not recorded** → **Done.** It killed 10 of 15 rows; the 5 survivors are leaf-link rows, safe by construction. Recorded in AC5 above.
- **AC14 against the merge commit** → re-run at `04e7486`: `poc/git` logs a non-nil error (`component "a.md" is a symbolic link`), `poc/parsing` passes, and `poc/verify-r1-8` stops at NOT REPRODUCED for both variants. It stays **partial** until it is re-run on the merge commit.
- **AC13 / "No silent behaviour changes"** → fixed with finding 1.
- **AC11 with a file-modifying hook** → fixed with finding 2.
- **AC10 backslash spellings** → fixed with finding 3.
- **AC12 "`make lint` passes"** → read as "no new issues" (see AC12): `--new-from-rev main` gives 0, the whole module has 319 issues against 324 on `main`, and gosec gives 0.
- **AC16 `/proc/self/fd` only on Linux** → re-run in `golang:1.26-bookworm` (below). `TestTree_NoHandleLeak` and `TestTree_HandlesBoundedByDepth` pass with the `/proc/self/fd` check active.

**Gates (round 1; code at `676814d`, which differs from `04e7486` only in a comment).**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `OK`; ok; `library surface is sound` |
| `go vet ./...`; `-tags integration`; `-tags scenario`; `GOOS=linux` / `GOOS=windows go vet ./core/worktreefs/` | all clean |
| `go test -p 4 -count=1 ./...` | every package `ok`, no FAIL |
| `go test -race -p 4 -count=1 ./core/worktreefs/ ./core/artifact/ ./core/workflow/ ./adapters/git/... ./core/engine/ ./service/internal/gateway/` (macOS) | all `ok` |
| The same in `golang:1.26-bookworm` (`docker run --rm`, sources and module cache read-only, `GOPROXY=off`), 3 times | all `ok` 3 times; `/proc/self/fd` leak check passes |
| Intermediate commit states (`ae4fb95`, `3b1a1fc`, `d278b86`) through `go vet` / `go test -overlay` on worktreefs, artifact, workflow and adapters/git | all `ok` |
| `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (`s004_r1scen`) | 19 `TestGitHTTP_*` PASS |
| Full `go test -tags scenario -count=1 -p 1 -v ./service/scenariotest/...` (`s004_r1scenfull`) | Same as the recorded baseline: 190 PASS / 30 FAIL / 6 SKIP top-level and 1 538 result lines. The failures are the pre-existing fixture-content errors (`validation_failed`, `missing required field: initiative`, `TestSchemaMatchesProduction`). |
| Integration-tagged suites | none in the packages touched this round (`core/worktreefs`, `core/artifact` tests) |
| `golangci-lint run --new-from-rev main ./...` / `--enable-only=gosec ./...` / `./core/worktreefs/...` | 0 / 0 / 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| Mutants (`-overlay`) | All killed: no read `SameFile`; no walk `SameFile`; no clamp; pre-review `path.go`; `:` cut before the `\` split; pre-review `tree.go` (in-place rewrite, depth bound, hook test); no pin (Linux, 3 rows); `os.Root` → `filepath.Join` opens (10/15). |
| `codex review --base main` (at `04e7486`) | "No actionable regressions were identified": no P1/P2/P3 to fix or reject |

**SMP compatibility gate (round 1).** Checkout `/Users/bartoszszymichowski/42sid/i23/s-T004/smp`
(SMP `7d4e7cc9`, `../spine` = this worktree). `go.mod`/`go.sum` were byte-identical after every run.

- `go build ./... && go vet ./... && go vet -tags integration ./...` → OK.
- `go test -p 4 -count=1` on the 37 SMP packages whose test dependency graph contains
  `core/worktreefs`, `core/artifact`, `core/workflow` or `adapters/git`, with DB env unset →
  36 `ok`, 1 `[no test files]`.
- `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -allow-skip
  TEST_PROVISIONING_DSN -- -race -count=1 -p 4 ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/`
  with `TEST_SPINE_DB` = `s004_r1_spine_rt` (spine-runtime migrations) and `TEST_PLATFORM_DB` =
  `s004_r1_smp_it` (platform migrations + `vector`) → **PASS: 1 536 passed, 0 failed**, 4 allowed
  skips.
- testgate on `automation`, `brain`, `discovery`, `notification`, `profileinventory`,
  `provenance`, `opshealth`, `recovery` (`TEST_PLATFORM_DB` = `TEST_DATABASE_URL` =
  `s004_r1_smp_it`) → 707 passed, 0 failed. testgate exits 1 only on the
  `TestObjectsCopyModeLiveMinIO` skip (no MinIO).
- No SMP expectation broke. The SMP TASK-008 handoff now names one newly refused flow (artifact
  Update / accept / reject of an existing file over 4 MiB: 2xx → 413).
- Databases created and dropped this round: `s004_r1scen`, `s004_r1scenfull`, `s004_r1_smp_it`,
  `s004_r1_spine_rt`. Containers: `s004-t004r1-linux`, `s004-t004r1-deep`, `s004-t004r1-deep2`,
  all run with `--rm`, from the image already present.

### Review round 2 (2026-10-07)

A second-round reviewer examined `524d306`. Both findings were reproduced before they were fixed.
Commits `9aba2fd`…`55d6863` carry the fixes (see the commit table).

**Findings.**

| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | P3 | Round-1 finding 2 was only partly fixed. A failing hook that replaces the staged file through a temp file and a rename (`sed -i`, formatters) gives the path a new inode. The pinned identity match then kept the file and every directory the Create made, so the retry got `already_exists` (main: `git_error` twice, clean tree). | **Fixed, option (b)** (`9aba2fd`). Reproduced first: `TestF5_Create_CommitFailure_HookReplacesFileByRename_Removed` and the workflow row fail on `524d306` (file and `workflows/` left). **Change:** `worktreefs.Undo.RollbackClaiming(claim)`. `claim` is asked only when the leaf exists, is a regular file (never for a link or a directory) and is not the pinned inode. On true, the file is unlinked through the held parent, but only while the name still refers to the inode `claim` was asked about (`claim` runs git). Then, as before, the created directories are removed while they are empty, so they go too. `Rollback` = `RollbackClaiming(nil)`. **Callers:** the artifact and workflow create rollbacks pass `claimUntracked`, which claims while `git.TrackedInHEAD` (new: `git ls-tree -z --name-only HEAD -- path`, literal pathspecs) reports the path untracked. An unborn HEAD or any git failure is an error, and the file is kept. The check runs on `context.WithoutCancel` with a 10 s timeout, so a client that disconnects does not turn the rollback into "keep". **Why (b), not (a):** the residual only exists in the shared standalone tree (a private worktree is force-removed by `Cleanup`), and there it dirties the server checkout and turns every retry into a misleading 409. The claim is decided in the callers because worktreefs is stdlib-only and knows no git. Doing it inside the rollback, before the directories are removed, means the directories go too; the reviewer's "after Rollback" would leave them. **Accepted race,** recorded in KNOWN-LIMITATIONS: a concurrent update of the same still-uncommitted path that lands just before the rollback is removed with it. **Tests:** `TestTree_UndoRollbackClaiming` (8 rows: leaf ours, claim not asked; leaf already gone; replaced + claimed; replaced + not claimed; nil claim; replaced by a symlink or a directory, claim not asked and entry kept; replaced again while `claim` ran, newer file kept; handles back to baseline, second call a no-op). `TestBranchWrite_TrackedInHEAD` (HEAD only, not index or worktree; directory entry; glob characters and `:(glob)` magic literal; linked worktree's own HEAD; unborn HEAD is an error). AC11 rows for artifact and workflow: rename → removed; rename + committed onto HEAD by the hook through plumbing → kept, `git status` clean. **Mutants (`-overlay`), all killed:** claim ignored; no post-claim re-check; claim asked for any kind of entry; artifact or workflow `claimUntracked` always true; workflow on plain `Rollback`; `TrackedInHEAD` without `GIT_LITERAL_PATHSPECS`; `TrackedInHEAD` returning "untracked" on error. All four new `TestF5_*` fail on a `git archive main` export (with `core/worktreefs` copied in as a library). |
| 2 | P3 | The I8 doc said the number of handles held does not grow with depth. `rollbackDirs` holds one handle per created level until it returns, and so does `descend`'s error path; `WalkRegularFiles` holds one per level. The depth test sampled only inside the walk hook. | **Fixed by correcting the doc** (`a4c6cac`), the reviewer's primary option. I8 now says that what is held across a caller's own work is bounded (a descent: 2; an `Undo` across `git add` + `git commit`: 3). It also says that `Undo.Rollback` (and a `Create` whose descent fails after making directories) re-opens one directory handle per directory made, all open until it returns, and that `WalkRegularFiles` holds one per level it is inside, including while `fn` runs. Both are bounded by `MaxPathBytes` at about 2 000. The `walk`, `rollbackDirs` and `Undo` comments and the CHANGELOG "For embedders" note say the same. **Falsifiable:** new test hook `testHookHandleOpened` (called with the counter after every open). `TestTree_HandlesBoundedByDepth` asserts Rollback peak ≤ 2 + (created − 1) = 301 at 300 levels (≤ 2 when nothing was created), and adds a `WalkRegularFiles` row asserting ≤ depth + 2 = 302. Both bounds are tight: "unpin after the directory removals" (302) and "keep the listing handle open while descending" (602) both fail them. **No depth cap and no O(√depth) re-walk:** the peak is transient, under a second on Linux at the maximum depth, and the same order as `main`'s `os.RemoveAll`. A component cap would be a new refusal for embedder input (round 1 already declined it, and the reviewer agreed). |
| codex | P2 | `codex review --base main` (at `3b6f542`): when a failing hook **deleted** the updated file, the update rollback's `Tree.Replace` failed on the missing leaf, so the rejected Update left the artifact or workflow deleted in the standalone tree, where `main`'s `os.WriteFile` re-created it. | **Fixed** (`55d6863`). Reproduced first (codex's probe, and `TestUpdate_CommitFailure_HookDeletesFile_Restored` failing on `3b6f542`). Both update rollbacks now restore with `Tree.WriteFile(rel, original, originalMode)`, where the mode comes from the pre-update `Lstat`: `Replace` when the file is there, otherwise an O_EXCL create with that mode, re-creating a removed directory as well. A symlink a hook put in its place is still refused and logged, never followed; `main` wrote through it. **Tests:** `TestUpdate_CommitFailure_HookDeletesFile_Restored` (deleted file; deleted 0755 file keeps its exec bit; removed directory; `git status` clean), `TestWorkflowUpdate_CommitFailure_HookDeletesFile_Restored`, and `TestF5_Update_CommitFailure_HookPlantsLink_NotFollowed` (outside victim untouched, link left in place; fails on `main`). On `main` the plain-delete rows pass (guards), and the 0755 and removed-directory rows fail (`main` restored with 0644 and could not re-create the directory). Mutant "restore with 0644": the 0755 row fails. A second `codex review --base main` at `55d6863`: "No actionable regressions were identified". |

**Gaps.**

- **AC14 partial** → still partial. Re-run at `55d6863` with the same overlay and the same result: `poc/git` and `poc/parsing` pass (the refusal is logged), and `poc/verify-r1-8` stops at NOT REPRODUCED for both variants. It must be re-run on the merge commit.
- **AC12 "`make lint` passes" read as "no new issues"** → unchanged and confirmed. `--new-from-rev main ./...` gives 0. The touched packages have 162 issues against 167 on a `main` export, with none new after line-number normalisation. The whole module has 319 against 324. gosec-only gives 0. CI does not run golangci-lint.
- **AC11 rename-rewriting hook** → fixed with finding 1, plus the update-side rows from the codex finding.
- **Round-1 dispositions** → the reviewer's verification is recorded; no action.

**Gates (code at `55d6863`; this notes commit changes only this file).**

| Command | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `OK`; ok; `library surface is sound` |
| `go vet ./...`; `-tags integration`; `-tags scenario`; `GOOS=linux go vet` (worktreefs, adapters/git, artifact, workflow); `GOOS=windows go vet ./core/worktreefs/` | all clean |
| `go test -p 4 -count=1 ./...` | rc 0, 39 packages `ok` |
| `go test -race -p 4 -count=1 ./core/worktreefs/ ./core/artifact/ ./core/workflow/ ./adapters/git/... ./core/engine/ ./service/internal/gateway/` | all `ok` |
| The same four Spine packages + `adapters/git/refname`, `-race -p 4`, 3 times, in `golang:1.26-bookworm` (`docker run --rm`, sources and module cache read-only, `GOPROXY=off`, git 2.39.5) | all `ok` 3 times |
| `go test -tags scenario -count=1 -run '^TestGitHTTP_' -v ./service/scenariotest/scenarios/` (`s004_r2scen2`) | rc 0, 19 `TestGitHTTP_*` PASS, no SKIP |
| Full `go test -tags scenario -count=1 -p 1 -v ./service/scenariotest/...` on the branch (`s004_r2scen`, then `s004_r2scen2` at `55d6863`) and on a `git archive main` export (`s004_r2scenmain`) | per-test results **identical** to `main`: 190 PASS / 30 FAIL / 6 SKIP top-level, 1 538 result lines; the failures are the pre-existing fixture-content errors |
| Integration-tagged suites of touched packages | none (no `integration`-tagged tests in worktreefs, artifact, workflow or adapters/git) |
| `golangci-lint run --new-from-rev main ./...` / `--enable-only=gosec ./...` / `./core/worktreefs/...` | 0 / 0 / 0 issues |
| `gofmt -l` on every changed `.go` file | empty |
| Mutants (`-overlay`) | all killed (finding 1: 8 mutants; finding 2: 2 bound mutants; codex: mode mutant) |
| `codex review --base main` | at `3b6f542`: one P2 (fixed in `55d6863`); at `55d6863`: "No actionable regressions were identified" |

**SMP compatibility gate (round 2).** Checkout `/Users/bartoszszymichowski/42sid/i23/s-T004/smp`
(SMP `7d4e7cc9`, `../spine` = this worktree). `go.mod` and `go.sum` were byte-identical after every
run, and the checkout stayed clean. The gate was run at `3b6f542` and again at `55d6863`, with
the same results:

- `go build ./... && go vet ./... && go vet -tags integration ./...` → OK.
- `go test -p 4 -count=1` on the **37** SMP packages whose test dependency graph contains
  `core/worktreefs`, `core/artifact`, `core/workflow` or `adapters/git` (direct importers:
  `projectionwriter`, `proposals`, `spinecore`, `spineembedded`, `workspace`), with DB env unset →
  36 `ok`, 1 `[no test files]`.
- `go run ./dev/testgate -allow-skip 'Un-skip to add an operator-level round-trip' -allow-skip
  TEST_PROVISIONING_DSN -- -race -count=1 -p 4 ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/`
  with `TEST_SPINE_DB` (spine-runtime migrations via `dev/apply-migrations`) and `TEST_PLATFORM_DB`
  (platform migrations + `vector`) → **PASS: 1 536 passed, 0 failed**, 4 allowed skips.
- testgate on `automation`, `customer/brain`, `customer/discovery`, `customer/notification`,
  `customer/profileinventory`, `customer/provenance`, `observability/opshealth`, `recovery`
  (`-allow-skip TEST_PROVISIONING_DSN -allow-skip TEST_MINIO`) → **PASS: 707 passed, 0 failed**,
  4 allowed skips. A first run that also allowed `TEST_AWS_OBJECT_STORE_` exited 1 only because
  that allowance matched nothing (stale), with the same 707 passing.
- No SMP expectation broke. Behaviour reachable from SMP changes only on a failed commit: a file
  a hook replaced is removed while it is untracked in HEAD, and a file a hook deleted on update
  is re-created. Both are closer to `main` than `524d306` was. Nothing new for SMP TASK-008.
- Not run, as in round 1: the `TEST_PROVISIONING_DSN`-gated suites (they allocate databases
  outside `s004_*`) and `TestObjectsCopyModeLiveMinIO`.
- Databases created and dropped this round: `s004_r2scen`, `s004_r2scenmain`, `s004_r2scen2`,
  `s004_r2_spine_rt`, `s004_r2_smp_it`, `s004_r2_spine_rt2`, `s004_r2_smp_it2`. Containers:
  `s004-t004r2-linux`, `s004-t004r2-linux2`, both run with `--rm` from the image already present.

### Integration (wave 2 merge) (2026-10-08)

Integration branch `init/INIT-023-wave2-integration` (worktree `i23/s-int2/spine`), cut from Spine
local `main` `ee3c249`. Merge order, each `git merge --no-ff` and built and tested before the next:
TASK-002 `b674b90`, TASK-016 `350fe87`, TASK-004 `50b9c8e`, TASK-009 `8925de9`, TASK-007 `2aca054`,
TASK-011 `c90c049`, TASK-010 `1e63692`, TASK-013 `85bf4b3`. Coordination commits: `3b88fb1`
(TreeEntryType joins the ref grammar), `0c26c91` (step-entry policy through the shared pool
constructor), `9e43640` (a refused signing secret holds its deliveries), `d61359d` (security model:
skills are a step-entry boundary), `1291e74` (codex round 1). Nothing was pushed; `main` and the task
branches are unchanged.

**This task in the integration.** Merge `50b9c8e`.

- *Conflicts:* `CHANGELOG.md` only; `adapters/git/cli.go`, `branchwrite.go` and
  `core/artifact/service.go` merged cleanly over TASK-002, keeping both flags as §7 asked.
- *With TASK-011 (`c90c049`):* `core/workflow/service.go` — TASK-011 still had `readFromDisk`
  (`os.ReadFile`). The resolution keeps this task's `parseExisting` over one capped `worktreefs`
  read and TASK-011's `ProjectionRoundTripRuleID`; `writeAndCommit` runs TASK-011's
  `ProjectionDefinition` parity check before `Clean`/`enterBranch` and this task's confined
  `Create`/`Replace`/rollback after it. No other TASK-011 write path does worktree I/O
  (`StartWorkflowPlanningRun` only validates the body).
- *Read cap:* `worktreefs.DefaultMaxReadBytes` (4 MiB) is unchanged; TASK-003 is not in wave 2, so
  the "define `git.DefaultMaxBlobBytes` as this constant" handoff stands.
- `git.TrackedInHEAD` was checked against TASK-002's grammar: a literal `HEAD`, left as is.

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

**This task.** Finding 6: the CHANGELOG entry for non-canonical paths, and the §1a design
note, named SMP's `validateRepoRelativePath` as a guard it is not. Both are corrected; SMP is
unaffected because its embedded artifact entry points pass canonical paths.

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
from `d23b897`). Nothing was pushed, and `main` and the other branches are unchanged.

**This task.** One finding (range `sp-T004`).

| # | Finding | Severity | Disposition | Commit | Regression test |
|---|---|---|---|---|---|
| 1 | `Create`'s pin re-opens the leaf it has just made with a plain `O_RDONLY` (`tree.go:763`). If the leaf is swapped for a FIFO after the `O_EXCL` create, the open blocks until a writer appears, before the `SameFile` check can refuse it. `Create` takes no context, so it hangs with three descriptors held, and request cancellation cannot reach it. A skeptic found the same shape in `ReadFileLimit` (`tree.go:478-493`) | Codex P2. Skeptics: not a bug (outside the threat model) and P3. **Accepted as P3** | **FIXED** (cheap, and makes the code's `SameFile` comment hold). Both opens add `O_NONBLOCK` on unix (`openNoBlock` in `nonblock_unix.go`; 0 in `nonblock_other.go`, because no FIFO lives in a directory tree there). `ReadFileLimit` puts its descriptor back into blocking mode after the checks, so the read is unchanged. Running the tests in Linux turned up a gap: `ext4`/`overlayfs` give the deleted leaf's freed inode number straight to the new FIFO, so `SameFile` passed and the non-blocking read returned empty content. `ReadFileLimit` now also requires the opened file to be regular. The pin needs no such check: the `O_EXCL` descriptor keeps its inode allocated, so its `SameFile` check is exact. The §1c design-check note is corrected in place | `de9c013`; lint `2aa99d6` | `TestTree_CreateLeafSwappedForFIFOBeforePin` and `TestTree_ReadLeafSwappedForFIFOAfterCheck` (`fifo_unix_test.go`). They use the existing `pin`/`read` hooks to swap in a FIFO; if the call is still running after 3 s they open a writer and fail. Both fail on `main` on macOS and on Linux ("the call blocked"). Dropping the regular-file check fails the read test on Linux. `TestTree_ReadFileThroughNonBlockingOpen` reads a 1 MiB file in full |

**Severity reasoning.** Git cannot check out a FIFO. Runners reach the worktree only through the
git gateway, and the worktrees are server-private (`0700`). So only a local process writing inside
the server's worktree can trigger this, which §1b places outside the threat model. Such a process
could already corrupt the worktree directly. The hang is confined to the one request's goroutine:
the callers take no repo-wide lock around `Create`.

**Unchanged for every other input.** `O_NONBLOCK` has no effect when opening a regular file or a
directory. The read descriptor is blocking again before its first read. Windows builds and vets
(`GOOS=windows go vet ./core/worktreefs`).

**Gates and the Codex round** are recorded once, in TASK-011's "Codex 6.1 catch-up" subsection
(the same branch, the same runs).

### Status after GitHub CI (2026-11-04)

- **2026-11-04: stays In Progress.** Spine `main` was pushed on 2026-11-04 with the owner's
  approval. GitHub CI run 41875102030 at `main` `ce0b9c9e` (the INIT-024 closing commit `fd35f802`
  plus INIT-024 TASK-010's git-identity fix) passed all 8 jobs; AC 16's Linux descriptor count ran
  on GitHub's runner in the `go test ./...` job. Remaining:
  - **AC 14 (a check):** run the three original audit PoCs (`poc/git`, `poc/parsing`,
    `poc/verify-r1-8`) with `go test -overlay` against the merge commit (`main` `d23b897`, the wave
    2 merge) and record the result here. They ran only against the branch head (review round 2,
    "Gaps").
