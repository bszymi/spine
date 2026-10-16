# Git Push Guide

This guide shows how to push to a Spine-hosted repository, how to push when a branch is protected, and what the error messages mean when a push is refused. It assumes the deployment has opted into push by setting `SPINE_GIT_RECEIVE_PACK_ENABLED=true` (see `docs/integration-guide.md` §7).

---

## 1. Prerequisites

- A Spine bearer token for the target workspace. The trusted-CIDR bypass used for clone/fetch does not apply to push — every push must carry `Authorization: Bearer <token>`.
- `git` on the client with HTTPS support (any version from the last few years is fine; `git push -o` requires 2.10+, `http.extraHeader` requires 2.18+).

### Sending the bearer token on every Git HTTPS request

Git's default credential helpers (`cache`, `store`, `osxkeychain`, …) handle HTTP Basic auth, but Spine's Git endpoint accepts **only** a literal `Authorization: Bearer <token>` header. The supported ways to send that header per request are:

1. **`http.extraHeader` on the clone directory** (recommended for per-clone scope):

   ```bash
   git clone \
     -c http.extraHeader="Authorization: Bearer $SPINE_TOKEN" \
     https://<spine-host>/git/<workspace-id> my-workspace
   ```

   `git clone -c <key>=<value>` embeds the setting in `my-workspace/.git/config`, so every subsequent `git fetch`/`git push` from that clone re-sends the header. Use a per-host section if you want the header scoped to one Spine deployment:

   ```bash
   git config --local http.https://<spine-host>/.extraHeader \
     "Authorization: Bearer $SPINE_TOKEN"
   ```

2. **One-shot push with `-c http.extraHeader=...`** (useful for CI and operator overrides — the token lives only for the duration of the command):

   ```bash
   git -c http.extraHeader="Authorization: Bearer $SPINE_TOKEN" \
     push -o spine.override=true origin main
   ```

3. **A credential helper that understands OAuth-style bearer tokens.** `git-credential-oauth` (2.39+) and platform SDKs (`gh auth`, etc.) emit bearer headers; if your organisation already standardises on one, it will work with Spine as long as the header sent is `Authorization: Bearer <token>`.

`git config credential.helper "cache"` alone is **not sufficient** — it stores a username/password for HTTP Basic and will be ignored by the Spine gateway, which will then return `401 Unauthorized`. If a push is failing with `401`, verify the header is going over the wire with `GIT_TRACE=1 git push` (the trace prints outgoing headers).

---

## 2. Pushing to an unprotected branch

Clone (sending the bearer header per §1), commit, push:

```bash
git clone -c http.extraHeader="Authorization: Bearer $SPINE_TOKEN" \
  https://<spine-host>/git/<workspace-id> my-workspace
cd my-workspace
git checkout -b feature/my-change
# ... edit, add, commit ...
git push origin feature/my-change
```

`feature/*` and any other branch outside the reserved `spine/` namespace that no rule in `/.spine/branch-protection.yaml` targets is treated as unprotected. The push goes through `git-http-backend` as a plain passthrough. Tags and other non-branch refs, and `spine/**` branches, are admitted separately (§6).

---

## 3. Pushing to a protected branch

By default `main` is protected with `no-direct-write` and `no-delete`. A contributor-role push to `main` is rejected before the server advances the ref:

```bash
$ git push origin main
To https://spine.example.com/git/ws-1
 ! [remote rejected] main -> main (pre-receive hook declined)
remote: branch-protection: rule "no-direct-write" blocks this operation on branch "refs/heads/main"
error: failed to push some refs to 'https://spine.example.com/git/ws-1'
```

The intended path for contributor writes is a planning run or a standard run that ends with a governed merge — see `docs/integration-guide.md` §7 and ADR-009. There is no force-push or `--no-verify` escape hatch from this gate: every ref update goes through the same policy.

---

## 4. Overriding protection as an operator

Operators (role `operator+`) can override a matching rule for a single push with the `spine.override=true` push option. Send the bearer header the same way as for any push — inline is typical so a single command is auditable:

```bash
git -c http.extraHeader="Authorization: Bearer $SPINE_TOKEN" \
  push -o spine.override=true origin main
```

**What happens:**

1. The pre-receive gate evaluates each ref update with `Override=true`.
2. The role gate in `branchprotect.Policy` confirms the caller is `operator+`. Contributors who try the same option see `override not authorised` and the push is rejected.
3. For each ref where the override actually bypassed a rule (an "honored override"), exactly one `branch_protection.override` governance event is emitted with the ADR-009 §4 payload, including a `pre_receive_ref: {old_sha, new_sha, ref}` block.
4. The client-produced commit is pushed byte-identically — Spine does not rewrite the commit to add a trailer. The event is the sole audit record on this path.
5. Pushing `-o spine.override=true` to a branch that does not need override (no matching rule) is allowed silently — no event is emitted.

### Changing `.spine/branch-protection.yaml` (and re-affirming a relaxation)

With the governance-path guards on (the `spine serve` default; ADR-016, INIT-023 TASK-006):

- **A run cannot change the config on its own.** A governed merge that touches `.spine/branch-protection.yaml` (or anything else under `.spine/`, `.spine.yaml` or a workflows directory) is held until an operator approves the run's bound commit: `GET /api/v1/runs/{run_id}/governance` shows the commit and the paths, `POST /api/v1/runs/{run_id}/governance-decision` with `{"decision":"approve","commit_sha":"<bound commit>"}` merges it (`"reject"`, with a `reason`, sends the run back).
- **The projection installs a relaxation only with an operator record.** A config that removes `no-direct-write` or `no-delete` from `main` is installed only when an honoured override push, or an approved governed merge, **changed** the file to exactly those bytes — and an approved merge only if its own diff removed the protection (the config before the merge applied it). Otherwise the previous rules stay and the server logs a Warn and emits `branch_protection.removal_refused`.
- **Re-affirming.** If a relaxation was refused (it arrived another way, or the runtime database was rebuilt from git and has no record of the original authorisation), an operator re-affirms it with an override push that **edits** the file — a changed comment line is enough:

  ```bash
  echo "# re-affirmed $(date -u +%F) by $USER" >> .spine/branch-protection.yaml
  git commit -am "Re-affirm branch protection"
  git -c http.extraHeader="Authorization: Bearer $SPINE_TOKEN" \
    push -o spine.override=true origin main
  ```

  An override push that does not change the file proves nothing about it and is ignored for this purpose. An override push that **does** change it vouches for the whole file it leaves: before you push an unrelated edit to the file (adding a rule, say), check that the installed rules match it — a `branch_protection.removal_refused` event names both — or your push also installs a relaxation someone else left there. The override event is the record: with the guards on, the push path writes it to `runtime.event_log` before git updates the ref, whether or not event delivery is enabled. Tightening the rules never needs a record.

### Deleting a protected branch

```bash
git -c http.extraHeader="Authorization: Bearer $SPINE_TOKEN" \
  push -o spine.override=true origin --delete staging
```

A `staging` marked `no-delete` would refuse a plain `git push --delete`; the override above bypasses it and emits one event with `operation: "delete"` and `commit_sha: null`.

---

## 5. Error message catalogue

When the pre-receive gate refuses a push, the Spine gateway responds with a git-shaped `x-git-receive-pack-result` body, so the client renders it as `remote: …` lines followed by per-ref status. The catalogue below maps the most common wire messages to their cause and remediation.

| Wire message (`remote: …`) | Cause | Remediation |
|---|---|---|
| `branch-protection: rule "no-direct-write" blocks this operation on branch "refs/heads/<X>"` | The branch matches a `no-direct-write` rule and the push is a direct advance. | Route the change through a run (planning or standard) or use `git push -o spine.override=true` as an operator. |
| `branch-protection: rule "no-delete" blocks this operation on branch "refs/heads/<X>"` | The branch matches a `no-delete` rule and the push is a ref deletion. | Operator override is the only bypass — `git push -o spine.override=true origin --delete <X>`. |
| `branch-protection: override requested but actor lacks operator role (rule "<kind>" still applies on branch "refs/heads/<X>")` | The caller set `-o spine.override=true` but their role is below `operator`. | Ask an operator to perform the push, or have an operator grant you the role (out-of-band). |
| `branch-protection: evaluation error on <X>` | The policy backend (projection) returned an error. Push is rejected as a fail-closed measure. | Check Spine server logs for the underlying error; the push can be retried once the backend is reachable. |
| `ref-admission: <X> is a run branch; only the actor assigned to the run's current step, or an operator, may push it` | `<X>` is a run or planning branch (`spine/run/…`, `spine/plan/…`) and you are not bound to it: you do not hold the latest execution of the run's current step (`assigned` or `in_progress`), the run is not `active` (for example `committing` during its merge), the name is not the run's own branch name, or your role is below `contributor`. | Claim the step first (§6), or ask an operator. The message never says who holds the step. |
| `ref-admission: <X> is a divergence branch; only the actor assigned to that branch's current step, or an operator, may push it` | The same, for a divergence branch (`spine/<run-id>/<run-id>-div-<id>/<branch-id>`): you must hold that branch's current step, which the branch must still be open on. | Claim that branch's step, or ask an operator. |
| `ref-admission: deleting <X> requires the operator role` | A non-operator deleted a run, planning or divergence branch. Even the bound actor may not. | Ask an operator. |
| `ref-admission: <X> is frozen while its current step is a review or engine-internal step; operator role required` | You are bound, but the step you hold is a `review` (or engine-`internal`) step: the branch under review cannot move. | Finish the review with a `needs_rework`-style outcome to return the run to an editable step, or ask an operator. |
| `ref-admission: cannot resolve the current step of <X>; operator role required` | You are bound, but the server could not load the step's definition from the run's pinned workflow, so it cannot rule out the review freeze (fail closed). | Check the server logs and the run's workflow; an operator can push meanwhile. |
| `ref-admission: spine/init is reserved for 'spine init-repo'; operator role required` | `spine/init` is the bootstrap branch `spine init-repo` writes. `spine init-repo` run with a non-operator token prints this line in its `Note: origin refused the push` and leaves `spine/init` local only. | Push the local branch from that clone with an operator token: `git -c http.extraHeader="Authorization: Bearer $OPERATOR_TOKEN" push -u origin spine/init`. Re-running `spine init-repo` in the same clone does **not** retry the push: with nothing left to commit it stops before pushing, prints nothing and exits 0. |
| `ref-admission: <X> is in the reserved spine namespace; operator role required` | `<X>` is `spine` itself or another `spine/**` name that is not a run, planning or divergence branch — including any case-fold spelling of `spine` (`SPINE/…`, `Spine/…`, `ſpine/…`) and names over 1024 bytes. | Use a branch name outside `spine/`, or ask an operator. |
| `ref-admission: <ref> is not a branch; only operators may push tags or other non-branch refs` | Tags, notes, `refs/<x>` and every other ref outside `refs/heads/` are operator-only: a tag named after a branch shadows it in bare-name git calls. | Push tags with an operator token (CI release tagging included). |
| `ref-admission: <ref> has a 'refs' component after the first or a 'HEAD' component, which would shadow another ref; push refused (deleting it is allowed)` | Names such as `refs/heads/refs/heads/main`, `refs/tags/refs/heads/main` or `refs/heads/HEAD` make every client push of the branch they shadow fail. Refused for everyone, operators included. | Rename the ref. Deleting an existing one is allowed. |
| `ref-admission: ref name contains a control byte; push refused` | Defence in depth behind the parser (which normally answers `malformed push` first). Refused for everyone. | Check the ref name with `git check-ref-format`. |
| `ref-admission: run-branch admission is not configured on this server; operator role required for <X>` | The server has no run store or no step-definition lookup for this workspace (for example a workspace without a database yet), so it cannot tell who is bound. | Ask an operator, or fix the server wiring. |
| `ref-admission: <X> not evaluated (push already refused)` | An earlier run or divergence ref in the same push was refused, so the rest are refused without further lookups. | Fix the first refusal; push the refs separately if needed. |
| `ref-admission: evaluation error on <X>` | The run store returned an error. Rejected fail-closed. | Check the server logs; retry once the store is reachable. |
| `branch-protection: malformed push: …` (usually `command N: …`, naming the offending command line) | The pre-receive parser refused the client's pkt-line stream before evaluating any rule, because git could read it differently from the gate or would refuse it anyway. Causes: an invalid ref name (`command N: invalid ref name: …` — not under `refs/`, or containing `..`, `@{`, a `.lock` component, a space or one of `~ ^ : ? * [ \`); a control byte in a ref, including a NUL after a later command's ref (`… contains control byte 0x00`); a malformed object id (`command N: malformed object ids …`); a capability list on the first command that is not printable ASCII (`command 1: malformed capability list …`); a push from a shallow clone (its `shallow <oid>` line is refused as `command 1: malformed ref update …`); or a truncated or mis-framed stream. A stock `git` client never sends the first four from a full clone. | Push from a full (non-shallow) clone with a recent `git` client and check the ref name with `git check-ref-format`. If it persists, file a bug with the full output of `GIT_TRACE_PACKET=1 git push`. |

A policy or admission rejection (every row above except `malformed push`) also includes a per-ref `ng <ref> pre-receive hook declined` status line for each ref that was part of the push — pre-receive is all-or-nothing, so every ref is `ng`-ed when any is denied, even the ones that would have been allowed in isolation. A `malformed push` rejection carries **no** per-ref `ng` lines, only `unpack ok`: the gate could not trust the refs it would have named, so it names none, and nothing in the push is applied. Untrusted bytes in its message are escaped (`\x00`), never echoed raw.

**Status codes that are NOT pre-receive rejections** (operators will occasionally see these too):

| HTTP response | Cause |
|---|---|
| `401 Unauthorized` with body `authorization required for external git access` | No `Authorization: Bearer <token>` header, and the caller is not inside a trusted CIDR (and trusted CIDRs don't help for push anyway). |
| `403 Forbidden` with body `git push is disabled — enable via git.receive_pack_enabled (SPINE_GIT_RECEIVE_PACK_ENABLED=true); see ADR-009` | `SPINE_GIT_RECEIVE_PACK_ENABLED` is not set (or not `true`) on the server. |
| `403 Forbidden` with body `unsupported git operation` | Unrecognised request against the git endpoint (neither clone/fetch nor push). |
| `400 Bad Request` with body `Chunked request bodies are not supported by CGI.` (the client prints `error: RPC failed; HTTP 400`) | The push body was larger than the client's `http.postBuffer` (1 MiB by default), so git sent it chunked, and the server's CGI bridge refuses chunked bodies (`KNOWN-LIMITATIONS.md`). Raise the buffer above the push size (`git config http.postBuffer 157286400`) and push again. |

---

## 6. Pushing a run branch

Run, planning and divergence branches (`refs/heads/spine/**`) are admitted by **ref class** before any branch-protection rule (INIT-023 TASK-016; [API Operations](../architecture/api-operations.md) §6.1). Pushing one works only like this:

1. **Hold the step.** You must be the actor of the **latest** execution of the run's current step (for a divergence branch: of that branch's current step), in state `assigned` or `in_progress`, on an `active` run, with role `contributor` or above. Claim it with `POST /api/v1/execution/claim`, or have an operator assign it (`POST /api/v1/runs/{run_id}/steps/{step_id}/assign`). Push with the same bearer token (§1), to the same repository the step targets.
2. **Push the run's own branch name.** It must equal the run's `branch_name` exactly, case included. Create, update and force-push are allowed for the bound actor.
3. **Do not delete it.** Deleting a run, planning or divergence branch needs the operator role, for the bound actor too.
4. **Not during review.** While the current step is a `review` (or engine-`internal`) step the branch is frozen for everyone below operator, the reviewer included. While the run is diverged its own branch is operator-only; push the divergence branch you hold instead.
5. **Tags need an operator.** Tags, notes and every other non-branch ref are operator-only, CI release tagging included.

**The `spine` namespace is reserved under any case-fold spelling.** `refs/heads/spine` itself, `spine/init`, any other `spine/**` name and every spelling a case-insensitive filesystem would alias onto `spine` (`SPINE/…`, `Spine/…`, `ſpine/…` with U+017F) are operator-only. A case variant is never treated as the run branch it would alias, even for the bound actor.

**Admission is decided when the push starts.** The gate decides as soon as the push's command section arrives; git updates the ref only after the pack has been uploaded **and processed** (indexed, deltas resolved, connectivity checked). A push *started* while you are bound can therefore still land after you have submitted the step, while review is open. That window has no fixed bound: the upload is limited by the server's HTTP `ReadTimeout` (30 s in `spine serve`), but git's processing of the received pack is not — `git-http-backend` runs under Go's `net/http/cgi`, which neither honours the request context nor bounds the child process — and how long it takes depends on the pack the pusher sends. "Only the bound actor writes the run branch", and the review freeze (item 4), are push-start checks, not write-time ones; merging exactly the reviewed commit (INIT-023 TASK-005) is the content control for that window.

**Operators** may push, force-push and delete any of these refs with no binding; the server logs each such admission. Every push presents a bearer token (`git -c http.extraHeader="Authorization: Bearer <token>" push …`): since INIT-023 TASK-015 no setting lets a push skip authentication — `SPINE_DEV_MODE` does not affect the git endpoint, and `SPINE_GIT_HTTP_DEV_NO_AUTH` (development only) opens clones and fetches only.

---

## 7. References

- [ADR-009 — Branch Protection](/architecture/adr/ADR-009-branch-protection.md)
- [`docs/integration-guide.md`](./integration-guide.md) §7 — Git HTTP endpoint configuration
- [`architecture/git-integration.md`](../architecture/git-integration.md) §6.5 — Enforcement sequence
- [`product/features/branch-protection.md`](../product/features/branch-protection.md) — Product-level model
