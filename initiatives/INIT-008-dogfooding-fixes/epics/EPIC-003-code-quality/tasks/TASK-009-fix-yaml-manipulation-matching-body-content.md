---
id: TASK-009
type: Task
title: "Fix line-based YAML manipulation matching body content"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-003-code-quality/epic.md
---

# TASK-009 — Fix Line-Based YAML Manipulation Matching Body Content

---

## Purpose

`insertAcceptanceFields` in `/internal/artifact/acceptance.go` (lines 115-147) and `insertLink` in `/internal/artifact/successor.go` (lines 136-178) use line-by-line parsing that matches `---` in markdown body content (e.g., horizontal rules). The `statusRegexp` in `acceptance.go` (line 150) replaces `status:` anywhere in the file, including the markdown body.

---

## Deliverable

1. Scope `statusRegexp` replacement to front matter only (between the first and second `---` delimiters)
2. Improve `---` delimiter detection to track front matter state correctly and not match body content
3. Consider using proper YAML parsing for front matter manipulation instead of regex

---

## Acceptance Criteria

- `---` in markdown body (horizontal rules) does not trigger front matter insertion
- `status:` in markdown body is not modified by acceptance recording
- Existing acceptance and successor tests pass; add tests with `---` in body
