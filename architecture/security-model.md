---
type: Architecture
title: Security Model
status: Living Document
version: "0.1"
---

# Security Model

---

## 1. Purpose

This document defines the security model for Spine at v0.x — how the system authenticates actors, enforces authorization, manages credentials, and maintains security boundaries between components.

The [Access Surface](/architecture/access-surface.md) defines the external interface and introduces authentication and authorization at a high level. This document makes those concepts concrete and extends them with credential management, component trust boundaries, and Git-level security guarantees.

All security decisions must comply with the [Constitution](/governance/constitution.md), particularly Actor Neutrality (§5) and Governed Execution (§4).

---

## 2. Security Principles

- **No anonymous access** — every operation must be attributable to an identified actor
- **Actor neutrality** — security constraints apply uniformly regardless of actor type (human, AI, automated)
- **Least privilege** — actors receive only the permissions required for their role
- **Credentials never in Git** — secrets, tokens, and credentials must never appear in artifact content or metadata
- **Trust the gateway, verify at boundaries** — internal components trust the identity established by the Access Gateway; security enforcement happens at defined boundaries, not everywhere
- **Auditability** — all authentication and authorization decisions must be traceable

---

## 3. Authentication

### 3.1 Actor Identity

Every request to Spine must carry a verified actor identity. The Access Gateway is responsible for establishing identity before any request reaches internal components.

**Identity attributes established at authentication:**

| Attribute | Description |
|-----------|-------------|
| `actor_id` | Stable unique identifier for the actor |
| `actor_type` | Classification: `human`, `ai_agent`, `automated_system` |
| `actor_role` | Authorization role (see §4) |
| `session_id` | Identifier for the current authentication session |

### 3.2 Authentication Methods

| Access Mode | Method | Identity Source |
|-------------|--------|-----------------|
| CLI | API token | Token maps to actor record |
| API | Bearer token (Authorization header) | Token maps to actor record |
| GUI | Session-based (login with credentials) | Credentials verified against actor record |
| Actor Gateway (inbound) | Service token | Pre-registered service identity for AI agents and automated systems |

**Note:** Local identity derived from Git configuration must not be used for authentication in shared or production environments. All access must be authenticated through the Access Gateway using valid credentials (e.g., API tokens or sessions).

### 3.2.1 Authentication by Actor Type

Spine supports different authentication mechanisms depending on actor type while maintaining a unified internal identity model.

- **Human actors** authenticate using interactive mechanisms (e.g., session-based login with credentials).
- **AI agents and automated systems** authenticate using service accounts and non-interactive credentials (API tokens).

Regardless of authentication method, all actors are normalized into the same internal identity model (`actor_id`, `actor_type`, `actor_role`) and are subject to the same authorization and workflow governance rules (Actor Neutrality).

Future versions may support stronger machine authentication mechanisms such as asymmetric key-based authentication or signed requests.

### 3.3 API Tokens

API tokens are the primary authentication mechanism for programmatic access.

**Token properties:**

- Tokens are opaque strings (no embedded claims)
- Each token is bound to exactly one actor
- Tokens have an optional expiration timestamp
- Tokens may be scoped to specific operations (read-only, contributor, full access)
- Tokens are stored as hashed values — the plaintext is returned only at creation time

**Token lifecycle:**

- Created by an admin or by the actor themselves (self-service)
- Revocable at any time by the owning actor or an admin
- Expired tokens are rejected immediately
- Revoked tokens are rejected immediately

**Security note:** API tokens are bearer credentials and must be treated as secrets with the same sensitivity as passwords.

### 3.4 Service Accounts

AI agents and automated systems authenticate via service accounts.

**Service account properties:**

- Each service account has a stable `actor_id` and `actor_type`
- Service accounts authenticate using API tokens (non-interactive credentials)
- Each service account may have multiple tokens for different environments or runtimes
- Tokens may be scoped and have expiration policies
- Service accounts are subject to the same authorization rules as human actors (Actor Neutrality §5)

**Best practices:**

- Do not share tokens across multiple agents or systems
- Use separate service accounts for distinct responsibilities (e.g., `ci-system`, `ai-reviewer`, `integration-webhook`)
- Rotate tokens regularly and revoke unused tokens

Service accounts provide identity for non-human actors but do not grant additional privileges beyond assigned roles and skills.

### 3.5 Session Management

| Access Mode | Session Duration | Refresh |
|-------------|-----------------|---------|
| CLI | Per-invocation (stateless) or cached token | Token re-read on each invocation |
| API | Per-request (stateless) | No session state; token on each request |
| GUI | Time-limited session with refresh | Session expires after inactivity timeout |

---

## 4. Authorization

### 4.1 Role-Based Access Control

Spine v0.x uses a role-based authorization model. Roles are assigned to actors, not to access modes.

| Role | Permissions |
|------|------------|
| `reader` | Read artifacts, query projected state, view Runs and execution history |
| `contributor` | Reader + create/update artifacts, submit step results, start Runs |
| `reviewer` | Contributor + approve/reject tasks, execute governance steps |
| `operator` | Reviewer + system operations (projection rebuild, health checks, Run cancellation) |
| `admin` | Full access including actor management, token management, and configuration |

### 4.2 Role Hierarchy

Roles are hierarchical — each role includes all permissions of the roles below it:

```
admin > operator > reviewer > contributor > reader
```

### 4.3 Enforcement Points

Authorization is enforced at multiple layers to ensure both coarse-grained and fine-grained control:

**Access Gateway (primary):**

- Every request is checked against the actor's role before being forwarded to internal components
- Unauthorized requests are rejected with a clear error before reaching the engine
- The Access Gateway attaches the verified `actor_role` to the internal request model

**Workflow Engine (step-level):**

- Workflow definitions may impose additional constraints on who can execute specific steps
- Step-level constraints are expressed in the workflow's `execution` block (e.g., `eligible_actor_types`, `required_skills`)
- These constraints are checked when a step is assigned, not at the gateway level

### 4.4 Step-Level Authorization

Workflow definitions may restrict step execution beyond role-based permissions:

```yaml
steps:
  - id: architecture_review
    execution:
      eligible_actor_types: [human]
      required_skills: [architecture_review]
```

The Workflow Engine evaluates these constraints when assigning actors to steps. An actor must have both sufficient role permissions and meet step-level requirements.

### 4.5 Authorization and Git Operations

When the Artifact Service commits to Git on behalf of an actor:

- The commit author is set to the authenticated actor's identity
- The actor must have sufficient role permissions for the operation that triggered the commit
- The Artifact Service does not perform additional authorization checks — it trusts the authorization established by the Access Gateway and Workflow Engine


### 4.6 Skills (Execution-Level Permissions)

In addition to role-based access control, workflows may require specific skills for step execution.

Skills are fine-grained, domain-specific permissions (e.g., `architecture_review`, `security_approval`, `deployment_access`).

**Properties:**

- Skills are assigned to actors alongside roles
- Skills are evaluated by the Workflow Engine (not the Access Gateway)
- Skills do not grant access to operations — they constrain who may execute specific workflow steps
- Roles define *what an actor can do*; skills define *which actor is eligible to perform a specific step*

Skills extend the authorization model at execution time but do not replace role-based access control.

---

## 5. Credential Management

### 5.1 Credential Categories

| Category | Examples | Storage |
|----------|----------|---------|
| Actor credentials | Passwords, API tokens | Access Gateway's credential store (hashed) |
| Integration secrets | External API keys, webhook secrets, LLM provider tokens | Runtime configuration (encrypted at rest) |
| Git credentials | SSH keys, access tokens for Git hosting | Runtime environment (not managed by Spine) |

### 5.2 Credential Rules

- **Never in Git** — no credential may appear in artifact content, metadata, commit messages, or any file tracked by Git
- **Never in events** — credentials must not appear in event payloads or log messages
- **Hashed at rest** — actor credentials (passwords, tokens) are stored as salted hashes
- **Encrypted at rest** — integration secrets are stored encrypted in runtime configuration
- **Scoped access** — components access only the credentials they need (Actor Gateway accesses LLM tokens; Access Gateway accesses actor credentials)

### 5.3 Credential Rotation

- API tokens may be rotated by creating a new token and revoking the old one
- Integration secrets are rotated by updating runtime configuration
- Rotation does not require Git commits (credentials are not governed artifacts)
- The system should support concurrent validity of old and new credentials during rotation windows

### 5.4 Credential Validation on Artifact Writes

The Artifact Service should reject commits that contain patterns matching known credential formats (API keys, tokens, passwords) in artifact content. This is a best-effort safeguard, not a guarantee.

---

## 6. Component Trust Boundaries

### 6.1 Trust Model

```
Untrusted Zone          │  Trust Boundary        │  Trusted Zone
                        │                        │
CLI / API / GUI ────────┤── Access Gateway ──────┤── Artifact Service
External actors         │   (authenticates,      │   Workflow Engine
External integrations   │    authorizes)         │   Projection Service
                        │                        │   Event Router
                        │                        │   Validation Service
Actor Gateway ──────────┤── (outbound: trusted   │
  (inbound from actors) │    to untrusted)       │
                        │── (inbound: untrusted  │
                        │    to trusted)         │
```

### 6.2 Boundary Rules

**Access Gateway boundary (inbound):**

- All external requests are untrusted until authenticated
- The Access Gateway is the single point of authentication for external access
- After authentication, the internal request model carries verified identity — internal components trust this identity

**Actor Gateway boundary (outbound and inbound):**

- Outbound requests to actors (step assignments) carry trace context but not internal credentials
- Inbound responses from actors are untrusted — the Workflow Engine validates all actor responses against workflow constraints before accepting outcomes
- Actor responses that produce durable artifacts must pass through the Artifact Service's validation

**Inter-component trust:**

- Internal components (Artifact Service, Workflow Engine, Projection Service, Event Router, Validation Service) trust each other
- No inter-component authentication is required in a single-process deployment
- If components are extracted into separate services, mutual TLS or service tokens should be introduced

### 6.3 Actor Response Validation

Actor responses are untrusted input. The system must:

- Validate that the response corresponds to an active step assignment
- Validate that the response conforms to the expected output schema
- Validate that any artifacts produced conform to the artifact schema
- Not execute arbitrary code provided by actors
- Not trust actor-provided metadata for authorization decisions

---

## 7. Git Security

### 7.1 Commit Attribution

Every Git commit must be attributable to a specific actor:

- The commit author field maps to the actor's identity
- System-generated commits (e.g., projection rebuilds, automated transitions) use a dedicated system actor identity
- Commit messages include the `Trace-ID` trailer for correlation with runtime execution (per [Observability](/architecture/observability.md) §3.3)

### 7.2 Commit Signing (v0.x Guidance)

Commit signing is recommended but not required for v0.x:

- When enabled, commits are signed using the actor's GPG or SSH key
- The Artifact Service signs commits on behalf of actors using a service-level signing key
- Signature verification may be enforced at the Git hosting level (e.g., GitHub branch protection)

Commit signing becomes more important as the system matures and audit requirements increase.

### 7.3 Branch Protection

The authoritative branch (typically `main`) should be protected:

- Direct pushes by actors are prohibited — changes must go through the Artifact Service
- Force pushes are prohibited (commits are never rewritten, per Constitution §7)
- Branch protection rules are enforced at the Git hosting level, not by Spine itself

### 7.4 Repository Access

- The Artifact Service requires read/write access to the Git repository
- The Projection Service requires read access
- No other component requires direct Git access
- Actor access to the repository should be mediated through Spine, not through direct Git operations

---

## 8. Sensitive Data Handling

### 8.1 What Must Not Be Stored in Git

- Credentials (passwords, API keys, tokens, secrets)
- Personally identifiable information (PII) unless explicitly governed
- Encryption keys or certificates
- Runtime configuration containing secrets

### 8.2 Log and Event Sanitization

- Log entries must not contain credentials or tokens
- Event payloads must not contain sensitive data
- Error messages should not leak internal system details to external actors
- Trace IDs and run IDs are safe to include in all contexts


### 8.3 Audit Logging

The system must record audit-relevant security events, including:

- Authentication attempts (success and failure)
- Authorization decisions (allow/deny)
- Token creation, usage, and revocation
- Actor-initiated operations affecting durable state (e.g., Git commits)

Audit logs are append-only and must not contain sensitive data such as credentials or tokens.

Audit logging is required for governance visibility but may be implemented incrementally in v0.x.

---

## 9. Constitutional Alignment

| Principle | How the Security Model Supports It |
|-----------|-----------------------------------|
| Actor Neutrality (§5) | All actors authenticate and authorize through the same mechanism; roles apply uniformly |
| Governed Execution (§4) | Authorization enforcement ensures actors can only execute operations permitted by their role and workflow constraints |
| Source of Truth (§2) | Credentials are excluded from Git; commit attribution preserves authorship integrity |
| Reproducibility (§7) | Commit signing and attribution ensure audit trail integrity |

---

## 10. Cross-References

- [Access Surface](/architecture/access-surface.md) §4 — Authentication and authorization overview
- [System Components](/architecture/components.md) §4.1 — Access Gateway, §4.6 — Actor Gateway
- [Domain Model](/architecture/domain-model.md) §3.4 — Actor entity
- [Observability](/architecture/observability.md) §3 — Trace ID propagation, §4 — Audit trail
- [Error Handling](/architecture/error-handling-and-recovery.md) §9 — Operator visibility
- [Constitution](/governance/constitution.md) §4 — Governed Execution, §5 — Actor Neutrality

---

## 11. Evolution Policy

This security model is expected to evolve as the system is implemented and security requirements become more concrete.

Areas expected to require refinement:

- Fine-grained permissions (per-artifact or per-workflow access control)
- OAuth2 / OIDC integration for human authentication
- Mutual TLS for inter-component communication in distributed deployments
- Audit log for authentication and authorization events
- Rate limiting and abuse prevention
- Commit signing enforcement policy

Changes that alter the trust model, authentication mechanism, or authorization enforcement should be captured as ADRs.
