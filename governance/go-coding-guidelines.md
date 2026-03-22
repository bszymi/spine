---
type: Governance
title: Go Coding Guidelines
status: Living Document
version: "0.1"
---

# Go Coding Guidelines

---

## 1. Purpose

This document defines coding standards for the Spine Go codebase. These guidelines reflect enduring architectural principles, governance constraints, and implementation patterns established during early Spine development.

The goal of this document is to ensure that code behaves correctly within the Spine model — including safety, determinism, observability, and compatibility with governed workflows. These standards are intended to remain stable across iterations of the platform, even as tooling, validation mechanisms, and execution models evolve.

Following these guidelines helps ensure compatibility with:
- Current validation and linting mechanisms
- Code review expectations
- Future Spine-native validation, workflow checks, and runtime enforcement

This document distinguishes between:
- Enduring rules derived from Spine architecture and governance
- Default implementation conventions based on current patterns
- Examples illustrating recommended approaches

Following these standards helps ensure code aligns with Spine governance expectations and passes current validation mechanisms, while remaining compatible with future Spine-native enforcement.

---

## 1.1 Relationship to Spine Enforcement

Spine enforces correctness through multiple layers that evolve over time:

- Development-time validation (linting, validation tools, CI checks)
- Code review practices
- Runtime workflow execution and step-level validation
- Future Spine-native enforcement embedded in workflow orchestration

This document is not tied to any single enforcement mechanism. Instead, it defines the coding behaviors expected for all Spine-compliant implementations, regardless of how enforcement is performed.

As Spine evolves, specific tools (such as validation commands or CI checks) may change or be replaced. However, the principles and patterns defined in this document are intended to remain stable and authoritative.

---

## 2. Error Handling

### 2.1 When to Use domain.SpineError

Use `domain.SpineError` for application-level errors that have meaning to API consumers:

```go
return nil, domain.NewError(domain.ErrNotFound, "artifact not found")
return nil, domain.NewErrorWithDetail(domain.ErrValidationFailed, "validation failed", result.Errors)
```

### 2.2 When to Use fmt.Errorf

Use `fmt.Errorf` with `%w` for infrastructure-level errors (database, file I/O, Git subprocess):

```go
return fmt.Errorf("connect to database: %w", err)
return fmt.Errorf("read file %s: %w", path, err)
```

Always provide context before the wrapped error.

### 2.3 When to Silence Errors

Use `_ =` only for idempotent cleanup operations where failure is acceptable:

```go
_ = os.Remove(fullPath)        // cleanup on commit failure
_ = gitReset(ctx, s.repo, path) // unstage on commit failure
_ = tx.Rollback(ctx)            // cleanup in test teardown
```

Never silence errors on data mutations, Git commits, or database writes.

### 2.4 Git Error Classification

Git operations return `git.GitError` with `Kind` field for retry decisions:

```go
if gitErr, ok := err.(*git.GitError); ok {
    if gitErr.Kind == git.ErrKindNotFound {
        return nil, domain.NewError(domain.ErrNotFound, ...)
    }
    if gitErr.IsRetryable() {
        // retry logic
    }
}
```

### 2.5 Transaction Error Handling

When both function error and rollback error occur, wrap both:

```go
if rbErr := pgxTx.Rollback(ctx); rbErr != nil {
    return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
}
```

---

## 3. Context and Observability

### 3.1 Context Values

Set context values at the entry boundary (request handler, CLI command, Run start):

```go
ctx = observe.WithTraceID(ctx, traceID)
ctx = observe.WithActorID(ctx, actorID)
ctx = observe.WithRunID(ctx, runID)
ctx = observe.WithComponent(ctx, "workflow_engine")
```

### 3.2 Logger Usage

Always use `observe.Logger(ctx)` at the start of service methods. The logger automatically enriches with context values:

```go
func (s *Service) Create(ctx context.Context, ...) error {
    log := observe.Logger(ctx)
    log.Info("creating artifact", "path", path)
    ...
    log.Warn("failed to emit event", "error", err)
}
```

### 3.3 Git Commit Trailers

Use `observe.TrailersFromContext` for all Git commits:

```go
trailers := observe.TrailersFromContext(ctx, "artifact.create")
```

This produces deterministic trailer ordering: Trace-ID, Actor-ID, Run-ID, Operation.

---

## 4. Git Operation Safety

### 4.1 Path Validation

All artifact paths must be validated before file or Git operations:

```go
fullPath, err := s.safePath(path)
if err != nil {
    return nil, err
}
```

`safePath` resolves symlinks via `filepath.EvalSymlinks` walking up to the nearest existing ancestor.

### 4.2 Pathspec Safety

All git commands that accept user-controlled paths must use:

1. `GIT_LITERAL_PATHSPECS=1` environment variable
2. `--` separator between options and paths

```go
cmd := execCommand(ctx, "git", "add", "--", path)
cmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
```

### 4.3 Scoped Commits

Never use bare `git commit` — always scope to the specific file:

```go
args := []string{"commit", "-m", message, "--", path}
```

This prevents unrelated staged files from being included.

### 4.4 Index Cleanup on Failure

If `git add` succeeds but commit fails, unstage the file:

```go
if err != nil {
    _ = gitReset(ctx, s.repo, path)
    return git.CommitResult{}, err
}
```

---

## 5. Store and Database

### 5.1 NULL Handling

Use the `nilIfEmpty` helper for optional string fields:

```go
func nilIfEmpty(s string) *string {
    if s == "" { return nil }
    return &s
}

// Usage:
_, err := s.pool.Exec(ctx, query, nilIfEmpty(run.CurrentStepID), ...)
```

When scanning, use pointer variables and check after:

```go
var currentStepID *string
rows.Scan(..., &currentStepID, ...)
if currentStepID != nil {
    run.CurrentStepID = *currentStepID
}
```

### 5.2 Dynamic Query Building

Build WHERE clauses with parameterized placeholders:

```go
var conditions []string
var args []any
argIdx := 1

if query.Type != "" {
    conditions = append(conditions, fmt.Sprintf("artifact_type = $%d", argIdx))
    args = append(args, query.Type)
    argIdx++
}
```

Column names are hardcoded (never from user input). Only values are parameterized.

### 5.3 Upsert Pattern

Use `INSERT ... ON CONFLICT DO UPDATE` for projections:

```go
INSERT INTO projection.artifacts (...) VALUES (...)
ON CONFLICT (artifact_path) DO UPDATE SET
    field = EXCLUDED.field,
    synced_at = now()
```

### 5.4 Transactions

Use `WithTx` for multi-statement operations:

```go
err := s.WithTx(ctx, func(tx store.Tx) error {
    if err := tx.CreateRun(ctx, run); err != nil {
        return err
    }
    return tx.CreateStepExecution(ctx, exec)
})
```

---

## 6. Event Emission

### 6.1 Fire-and-Forget Pattern

Events are emitted asynchronously. Log failures but don't propagate:

```go
if err := s.events.Emit(ctx, evt); err != nil {
    log := observe.Logger(ctx)
    log.Warn("failed to emit event", "event_type", eventType, "error", err)
}
```

### 6.2 Event Payload

Include all relevant fields for subscriber usefulness:

```go
evt := domain.Event{
    EventID:      eventID,
    Type:         domain.EventArtifactCreated,
    Timestamp:    time.Now(),
    ActorID:      observe.ActorID(ctx),
    RunID:        observe.RunID(ctx),
    ArtifactPath: artifact.Path,
    TraceID:      observe.TraceID(ctx),
    Payload:      mustJSON(map[string]string{
        "commit_sha":    commitSHA,
        "artifact_id":   artifact.ID,
        "artifact_type": string(artifact.Type),
    }),
}
```

### 6.3 Event Idempotency

Events use `EventID` as the queue idempotency key. Generate with `observe.GenerateTraceID()`.

---

## 7. Validation

### 7.1 Return ValidationResult, Not Errors

Validation functions return `domain.ValidationResult`, never `error`:

```go
func Validate(a *domain.Artifact) domain.ValidationResult {
    var errors []domain.ValidationError
    // ... check rules ...
    return domain.ValidationResult{
        Status: status, // "passed", "failed", "warnings"
        Errors: errors,
    }
}
```

### 7.2 Structured Validation Errors

Every validation error includes context:

```go
domain.ValidationError{
    RuleID:       "schema",
    ArtifactPath: path,
    Field:        "status",
    Severity:     "error",
    Message:      "invalid status for Task",
}
```

### 7.3 Multi-Level Validation

Run validation in phases (schema → structural → semantic). Only proceed to the next phase if the previous passed:

```go
allErrors = append(allErrors, ValidateSchema(wf)...)
if len(allErrors) == 0 {
    allErrors = append(allErrors, ValidateStructure(wf)...)
    allErrors = append(allErrors, ValidateSemantic(wf)...)
}
```

---

## 8. Testing

### 8.1 Test File Naming

| Pattern | Purpose |
|---------|---------|
| `*_test.go` | Unit tests (same package) |
| `*_integration_test.go` | Database/system tests (behind `//go:build integration`) |
| `*_coverage_test.go` | Additional tests targeting coverage gaps |
| `export_test.go` | Exported test helpers (internal functions exposed for testing) |

### 8.2 Coverage Threshold

Every implementation package must have at least **80%** statement coverage. Target **90%+** where practical.

### 8.3 Test Helpers

Use `testutil.NewTempRepo` for Git tests, `store.NewTestStore` for DB tests:

```go
repo := testutil.NewTempRepo(t)     // creates temp Git repo, cleaned up on test end
db := store.NewTestStore(t)          // connects to test DB, applies migrations, cleaned up on test end
```

### 8.4 Integration Test Pattern

```go
//go:build integration

func TestFoo(t *testing.T) {
    db := store.NewTestStore(t)
    defer db.CleanupTestData(ctx, t)
    // ... test ...
}
```

### 8.5 Table-Driven Tests

Use for parametric testing:

```go
tests := []struct {
    name    string
    input   string
    wantErr bool
}{
    {"valid", "good input", false},
    {"empty", "", true},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) { ... })
}
```

### 8.6 No Assertion Libraries

Use standard `testing.T` methods: `t.Errorf`, `t.Fatalf`, `t.Fatal`, `t.Skip`.

---

## 9. Serialization

### 9.1 Dual JSON/YAML Tags

All domain types that may be parsed from YAML files must have both tags:

```go
type WorkflowDefinition struct {
    ID   string `json:"id" yaml:"id"`
    Name string `json:"name" yaml:"name"`
}
```

### 9.2 Naming Convention

Use `snake_case` for both JSON and YAML field names. Match the field names used in the architecture specification.

### 9.3 Omitempty

Use `omitempty` for optional fields:

```go
Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
```

### 9.4 Excluding Fields from YAML

Use `yaml:"-"` for fields set programmatically (not from YAML parsing):

```go
Path      string `json:"path" yaml:"-"`       // set by parser
CommitSHA string `json:"commit_sha" yaml:"-"` // set at binding time
```

---

## 10. Package Organization

### 10.1 Dependency Direction

Dependencies flow inward. Never import upstream:

```
cmd/spine → internal/{service packages} → internal/domain
```

`internal/domain` has no dependencies on other internal packages.

### 10.2 Interface Ownership

Each component package defines the interfaces it needs. The `main.go` entry point binds implementations to interfaces.

### 10.3 Doc Files

Use `doc.go` for package-level documentation only in placeholder packages. Remove `doc.go` once real files exist.

---

## 11. Anti-Patterns to Avoid

These were found repeatedly during code reviews. Avoid them to pass spine-validate-task on the first attempt.

| Anti-Pattern | Correct Pattern |
|-------------|----------------|
| Silencing errors on data operations | Only silence cleanup/rollback errors |
| Bare `git commit` (captures all staged) | Use `git commit -- path` for scoped commits |
| Missing `--` in git commands | Always use `--` separator before paths |
| Missing `GIT_LITERAL_PATHSPECS` | Set on all git commands with user paths |
| Advancing sync state on partial failure | Only advance when all operations succeed |
| Not checking `filepath.EvalSymlinks` | Always resolve symlinks for path safety |
| `range` over large structs by value | Use `for i := range` with pointer access |
| Missing `yaml:` tags on domain types | Always add both `json:` and `yaml:` tags |
| `len(s) > 0` instead of `s != ""` | Use string comparison for clarity |
| Manual substring search | Use `strings.Contains` |

---

## 12. Lint Configuration

The project uses `golangci-lint` with these linters enabled:

- `errcheck` — unchecked errors (excluded in test files)
- `govet` — suspicious constructs
- `staticcheck` — static analysis
- `unused` — unused code
- `ineffassign` — ineffectual assignments
- `gosimple` — simplifications
- `gocritic` — style and performance
- `gofmt` — formatting
- `goimports` — import ordering

Disabled gocritic checks: `hugeParam`, `unnamedResult`, `filepathJoin`.

All code must pass `golangci-lint run ./...` with zero issues before committing.

---

## 13. Cross-References

- [Implementation Guide](/architecture/implementation-guide.md) — Package layout, interfaces, build
- [Docker Runtime](/architecture/docker-runtime.md) §14 — Logging to stdout in JSON
- [Observability](/architecture/observability.md) §5 — Log fields and levels
- [Git Integration](/architecture/git-integration.md) §5 — Commit trailer format
- [Error Handling](/architecture/error-handling-and-recovery.md) — Failure classification
- [Constitution](/governance/constitution.md) §2 — Git as source of truth
