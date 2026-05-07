---
id: TASK-003
type: Task
title: "Add internal/yamlsafe/decoder_test.go"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-003 — Add internal/yamlsafe/decoder_test.go

---

## Purpose

`internal/yamlsafe/decoder.go` has no `_test.go` sibling, despite being
the only DoS guard between six YAML ingestion seams (`workflow/parser.go`,
`artifact/parser.go`, `repository/catalog.go`, `config/config.go`,
`cli/workflow.go`, `branchprotect/config/parser.go`,
`domain/validation_policy.go`) and a billion-laughs / depth-bomb
attack. The bounds at `decoder.go:18-24` (`MaxBytes` 64KiB, `MaxNodes`
10k, `MaxDepth` 64, `MaxAliases` 100) and the cyclic-alias-no-follow
path at `:91-93` are silently load-bearing.

This is a P1 finding from the 2026-05-07 code review.

## Deliverable

Add `internal/yamlsafe/decoder_test.go` with one focused case per limit
plus the cyclic-alias case:

- **MaxBytes**: input one byte over the limit returns the documented
  error class; input exactly at the limit succeeds.
- **MaxNodes**: a flat list of `MaxNodes+1` scalars rejects; a list of
  `MaxNodes` scalars succeeds.
- **MaxDepth**: nested mappings `MaxDepth+1` deep reject; `MaxDepth`
  succeed.
- **MaxAliases**: a document with `MaxAliases+1` aliases rejects;
  `MaxAliases` succeeds.
- **Cyclic alias**: an alias that points back into its own anchor's
  subtree returns the cycle-detection error rather than infinite-looping.
- **Happy path**: a small typical workflow / artifact YAML decodes
  successfully.

Tests should be table-driven where natural and use focused builder
helpers for the depth / node / alias inputs (no fixture files unless
unavoidable).

## Acceptance Criteria

- `go test ./internal/yamlsafe` passes; no integration tag required.
- All five limit branches and the cyclic-alias path have a passing
  case + a failing case.
- Coverage for `internal/yamlsafe/decoder.go` reaches at least 90%
  line coverage (verifiable via `go test -coverprofile`).

## Out of Scope

- Changing `MaxBytes`/`MaxNodes`/etc. The defaults are part of the
  contract — verify them, don't tune them.
- Adding test cases for higher-level parsers (workflow, artifact). The
  individual parsers have their own coverage; this task is the
  primitive-level guard.
