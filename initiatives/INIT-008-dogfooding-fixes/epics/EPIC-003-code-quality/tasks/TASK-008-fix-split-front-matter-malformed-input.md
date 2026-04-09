---
id: TASK-008
type: Task
title: "Fix splitFrontMatter accepting malformed front matter"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-008 — Fix splitFrontMatter Accepting Malformed Front Matter

---

## Purpose

`splitFrontMatter` in `/internal/artifact/parser.go` (lines 192-211) accepts `---` prefix without requiring a newline after it. A file starting with `---xyz` passes the prefix check. The closing delimiter search also doesn't verify `---` is followed by newline or EOF, allowing malformed documents to pass parsing.

---

## Deliverable

1. Require `"---\n"` or `"---\r\n"` at the start of the file
2. Require the closing `---` to be followed by `\n`, `\r\n`, or EOF
3. Reject content that does not meet these requirements

---

## Acceptance Criteria

- Files starting with `---xyz` are rejected
- Files with `---` not on its own line are rejected
- Valid front matter continues to parse correctly
- Existing parser tests pass; add edge case tests
