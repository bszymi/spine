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

`feature/*` and any other branch that no rule in `/.spine/branch-protection.yaml` targets is treated as unprotected. The push goes through `git-http-backend` as a plain passthrough.

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
| `branch-protection: malformed push: …` | The pre-receive parser rejected the client's pkt-line stream. Very rarely caused by a buggy client. | Retry with a recent `git` client; if it persists, file a bug with the full output of `GIT_TRACE_PACKET=1 git push`. |

All rejection responses also include a per-ref `ng <ref> pre-receive hook declined` status line for each ref that was part of the push — pre-receive is all-or-nothing, so every ref is `ng`-ed when any is denied, even the ones that would have been allowed in isolation.

**Status codes that are NOT pre-receive rejections** (operators will occasionally see these too):

| HTTP response | Cause |
|---|---|
| `401 Unauthorized` with body `authorization required for external git access` | No `Authorization: Bearer <token>` header, and the caller is not inside a trusted CIDR (and trusted CIDRs don't help for push anyway). |
| `403 Forbidden` with body `git push is disabled — enable via git.receive_pack_enabled (SPINE_GIT_RECEIVE_PACK_ENABLED=true); see ADR-009` | `SPINE_GIT_RECEIVE_PACK_ENABLED` is not set (or not `true`) on the server. |
| `403 Forbidden` with body `unsupported git operation` | Unrecognised request against the git endpoint (neither clone/fetch nor push). |

---

## 6. References

- [ADR-009 — Branch Protection](/architecture/adr/ADR-009-branch-protection.md)
- [`docs/integration-guide.md`](./integration-guide.md) §7 — Git HTTP endpoint configuration
- [`architecture/git-integration.md`](../architecture/git-integration.md) §6.5 — Enforcement sequence
- [`product/features/branch-protection.md`](../product/features/branch-protection.md) — Product-level model
