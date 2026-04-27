---
id: TASK-001
type: Task
title: SecretClient interface and value types
status: Completed
epic: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
initiative: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-021-workspace-runtime-secret-and-pool-hardening/epics/EPIC-001-secret-client/epic.md
---

# TASK-001 — SecretClient interface and value types

---

## Purpose

Land the `SecretClient` interface and its value types in
`internal/secrets`. This is the substrate the two providers
implement and the resolver consumes.

## Deliverable

`internal/secrets/client.go`:

```go
// SecretRef is a typed string in URI form:
//   secret-store://workspaces/{workspace_id}/{purpose}
// `purpose` is one of: runtime_db, projection_db, git.
// Callers MUST construct refs via the helpers below, not by
// string concatenation, so the URI shape stays in one place.
type SecretRef string

// WorkspaceRef builds the canonical ref for a workspace credential.
//   secrets.WorkspaceRef("acme", "runtime_db")
// → SecretRef("secret-store://workspaces/acme/runtime_db")
func WorkspaceRef(workspaceID, purpose string) SecretRef

// ParseRef extracts (workspaceID, purpose) from a SecretRef. Returns
// ErrInvalidRef if the URI shape does not match.
func ParseRef(ref SecretRef) (workspaceID, purpose string, err error)

type SecretValue struct {
    raw []byte
}

func (v SecretValue) Reveal() []byte // single deliberate accessor
func (v SecretValue) String() string // returns "<redacted>"

type VersionID string

type SecretClient interface {
    Get(ctx context.Context, ref SecretRef) (SecretValue, VersionID, error)
    Invalidate(ctx context.Context, ref SecretRef) error
}

// Read-only by design. Rotation and seeding are platform-side
// concerns (see ADR-010, ADR-011). No Spine code writes secrets.

var (
    ErrSecretNotFound  = errors.New("secret not found")
    ErrAccessDenied    = errors.New("secret access denied")
    ErrSecretStoreDown = errors.New("secret store unreachable")
    ErrInvalidRef      = errors.New("invalid secret ref")
)
```

## Acceptance Criteria

- Interface, value types, and ref helpers defined.
- `WorkspaceRef` + `ParseRef` round-trip; `ParseRef` returns
  `ErrInvalidRef` for malformed input.
- `SecretValue.String` and `MarshalJSON` redact.
- Logger redaction rule registered for `SecretValue`.
- Sentinel errors are distinguishable.
- Package doc states: callers construct refs via `WorkspaceRef`,
  never by string concatenation.
- Contract suite scaffolding lives at
  `internal/secrets/contract/`. The package exposes a
  `RunContract(t *testing.T, newClient func() SecretClient)`
  helper that the AWS and file providers (TASK-002, TASK-003)
  invoke from their own test files. Suite covers `Get`,
  `Invalidate`, sentinel-error mapping, and redaction.
