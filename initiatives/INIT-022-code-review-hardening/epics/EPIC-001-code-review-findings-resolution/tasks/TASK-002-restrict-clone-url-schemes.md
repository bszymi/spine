---
id: TASK-002
type: Task
title: "Restrict repository.ValidateCloneURL to safe schemes"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-001-validate-local-path-and-wire-repo-base.md
acceptance_rationale: |
  Replaced the permissive scheme switch in `internal/repository/clone_url.go`
  with an explicit allowlist (https / ssh / git+ssh / SCP-like) and rejected
  every other form, including `http://` (cleartext), `git://` (cleartext, no
  auth), `file://` (local-FS exfiltration), `ext::` (smart-protocol command
  execution), unknown schemes (ftp, rsync, smb, etc.), and any URL starting
  with `-` (flag injection). Both `Manager.Register` and `Manager.Update`
  exercise the single tightened validator.

  Took the optional convergence path: tightened
  `internal/git.ValidateCloneURL` to the same allowlist with a documented
  carve-out for bare local paths (workspace provisioning seeds clones from
  local bare repos in tests and bootstrap scripts; SCP-like remotes are
  also accepted). Both validators now agree on what a "safe" clone URL is.

  Codex iterative review surfaced two findings that were not in the
  original task scope:
    - **P2 git-validator coverage gap**: my first cut only added prefix-match
      rejections for `git://` and `http://` to `internal/git`, leaving `ftp://`,
      `rsync://`, etc. falling through to acceptance. Replaced with a
      `schemeURLPrefix` regex + scheme switch so any URL with a scheme is
      gated by the allowlist while non-URL forms (SCP-like, local paths)
      remain accepted unconditionally.
    - **P1 redaction-bypass via padded URLs**: an early version trimmed
      whitespace before validating, but `Register`/`Update` persist the raw
      string and downstream `RedactCloneURL` falls back to echoing the raw
      input when `url.Parse` fails on a leading space — leaking embedded
      `user:token` credentials. Fixed by rejecting any value where
      `raw != strings.TrimSpace(raw)`.

  The runbook §2.5 failure-modes table was updated to advertise the new
  allowlist and the padding/leading-`-` rejections, so operators see the
  authoritative list of accepted forms in the same place they look up the
  400-error remedy.

  17 new test cases across `clone_url_test.go` (per-form positive +
  per-rejection-class negative, plus the padded-URL credential-leak case)
  and `internal/git/credentials_test.go` (new scheme rejections + the
  preserved-local-path case). Two consecutive clean codex passes after the
  fixes.
---

# TASK-002 — Restrict repository.ValidateCloneURL to safe schemes

---

## Purpose

`internal/repository/clone_url.go:33-44`'s `ValidateCloneURL` accepts
`file://` and `git://` schemes for code-repo bindings, while
`internal/git/credentials.go:72-88`'s sister function rejects `file://`.
The repository manager uses the permissive validator; the URL flows
through to `Pool.cloner.Clone(ctx, repo.CloneURL, …)` at
`internal/gitpool/pool.go:381`. An Operator registers
`{"clone_url":"file:///root/.ssh"}`, the pool clones that local path
into the workspace's repo tree, then any actor with `query.history`
gets file contents back through `git show <ref>:<path>`.

This is a P1 security finding from the 2026-05-07 code review.

## Deliverable

**Note on scope:** delegating to `internal/git.ValidateCloneURL` is
NOT sufficient — that function today only rejects empty, `ext::`,
`file://`, and leading-`-` URLs; it does not reject `git://`. The fix
must therefore use an **explicit allowlist** at the repository
boundary OR also tighten `git.ValidateCloneURL` itself.

- In `internal/repository/clone_url.go`, replace the current scheme
  switch with an **explicit allowlist** of the URL forms Spine
  actually supports for code-repo bindings:
  - `https://...` (URL form)
  - `ssh://...` (URL form)
  - `git+ssh://...` (URL form)
  - **SCP-like SSH** (`[user@]host:path`, e.g.
    `git@github.com:org/repo.git`) — currently accepted, widely used
    for GitHub/GitLab; must be preserved. Detect via the standard
    git heuristic: contains a `:` before any `/`, and does not start
    with a recognized scheme.
  Reject everything else, including `http` (cleartext), `git`
  (cleartext, no auth), `file`, `ext::`, and any scheme starting
  with `-` (leading-dash injection).
- Confirm both call sites — `Manager.Register` and `Manager.Update` —
  exercise the tightened validator. There SHOULD be exactly one
  validation path; if there are two, consolidate.
- (Optional, recommended) tighten `internal/git.ValidateCloneURL`
  with the same allowlist so the two functions converge. If you take
  this path, document the convergence in the package comment.

## Acceptance Criteria

- A `POST /repositories` body with each of the following
  `clone_url` values returns 400 `invalid_params`: `file:///etc`,
  `git://example.com/repo.git`, `http://example.com/repo.git`,
  `ext::ssh foo`, `--upload-pack=cmd https://...`.
- Bodies with `https://`, `ssh://`, `git+ssh://`, AND SCP-like
  (`git@github.com:org/repo.git`) succeed.
- A unit test in `internal/repository/clone_url_test.go` enumerates
  every disallowed form with one case per form + one positive case
  per allowed form (including SCP-like).
- If `git.ValidateCloneURL` is also tightened, its existing tests
  in `internal/git/credential_test.go` (or sibling) are extended to
  cover the new rejections AND continue to cover the SCP-like
  positive case.

## Out of Scope

- Adding new allowed schemes (e.g. `git+http`). Stick to the
  recommended set unless an explicit operator workflow needs another.
- Path-on-disk validation for cloned content — TASK-001 covers that
  surface.

## Notes

TASK-001 and TASK-002 ship as **separate stacked PRs** (TASK-002
on top of TASK-001) so each fix has its own codex pass and merge
window — this matches the initiative's "No combined PRs" constraint
in §5 of `initiative.md`. Reviewers can read them as a unit because
they share a surface, but each lands independently.
