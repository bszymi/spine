# Non-Goals and Anti-Requirements

**Project:** Spine
**Version:** 0.1
**Status:** Living Document

---

## 1. Purpose

This document explicitly defines what Spine is not and what it will not do.

It serves as a filter for evaluating feature requests, architectural proposals, and scope changes. If a proposed capability conflicts with a non-goal listed here, it should be rejected or require a formal amendment with documented rationale.

---

## 2. Non-Goals

### 2.1 Spine is not a ticketing system

Spine does not aim to replace Jira, Linear, GitHub Issues, or any issue tracker.

Ticketing systems are actor-centric — they organize work around people and status boards. Spine is artifact-centric — it organizes work around versioned intent and governed execution.

Spine may integrate with ticketing systems, but it does not replicate their features (assignment boards, sprint planning, velocity tracking, notifications).

**Filter:** If a feature request looks like "add a Kanban board" or "track story points," it is out of scope.

---

### 2.2 Spine is not a CI/CD system

Spine does not build, test, or deploy code.

CI/CD systems (GitHub Actions, Jenkins, GitLab CI) are execution engines for build and deployment pipelines. Spine governs the structural integrity of work — it does not execute builds or manage deployment environments.

Spine may trigger or be triggered by CI/CD systems, but it does not replace them.

**Filter:** If a feature request looks like "run tests on PR" or "deploy to staging," it is out of scope.

---

### 2.3 Spine is not a project management tool

Spine does not provide Gantt charts, resource allocation, timeline estimation, capacity planning, or burndown tracking.

Project management tools optimize for scheduling and resource utilization. Spine optimizes for structural integrity between intent and execution.

**Filter:** If a feature request looks like "estimate task duration" or "show team capacity," it is out of scope.

---

### 2.4 Spine is not a documentation platform

Spine uses Markdown artifacts as the medium of truth, but it is not a wiki, knowledge base, or documentation hosting system.

It does not provide rich editing, search indexing, or publishing features. Spine treats documents as governed artifacts, not as content to be browsed.

**Filter:** If a feature request looks like "add full-text search across docs" or "render documentation as a website," it is out of scope.

---

### 2.5 Spine is not an AI orchestration framework

Spine governs AI agents as execution actors, but it is not LangChain, CrewAI, or an agent orchestration platform.

Spine does not manage prompts, chain LLM calls, or provide agent memory. It defines what an AI agent may do within a workflow, validates its output, and records its contributions.

**Filter:** If a feature request looks like "add prompt chaining" or "manage agent memory," it is out of scope.

---

### 2.6 Spine does not prioritize speed over integrity

Spine will not sacrifice traceability, reproducibility, or auditability for faster execution.

Features that bypass governance (skip validation, auto-approve, disable audit) are anti-requirements. Acceleration must emerge from structural clarity, not from weakening constraints.

**Filter:** If a feature request looks like "skip review for small changes" or "auto-merge when tests pass," it requires careful evaluation against constitutional principles.

---

### 2.7 Spine does not enable uncontrolled AI autonomy

AI agents in Spine operate under the same governance as human actors. Spine will not provide features that allow AI agents to self-assign work, escalate their own permissions, or bypass workflow constraints.

**Filter:** If a feature request looks like "let the agent decide what to work on next" or "auto-approve agent output," it is out of scope.

---

### 2.8 Spine does not aim for universal adoption

Spine is designed for teams that need structural integrity between product intent and execution — particularly hybrid teams of humans and AI agents.

It is not designed for solo developers, casual projects, or teams that prefer minimal process. See the [anti-persona](/product/users-and-use-cases.md#31-casual-solo-hacker) in the users and use cases document.

**Filter:** If a feature request begins with "make it easier for someone who doesn't want governance," it conflicts with Spine's core purpose.

---

## 3. Common Misconceptions

| Misconception | Reality |
|---------------|---------|
| Spine is a better Jira | Spine is not a ticketing system. It governs execution through versioned artifacts, not boards and sprints. |
| Spine replaces GitHub | Spine depends on Git. It adds a governance and execution layer on top of Git, not a replacement for it. |
| Spine is an AI agent framework | Spine governs AI agents as actors within workflows. It does not orchestrate LLM calls or manage agent internals. |
| Spine makes teams faster | Spine makes teams more structurally sound. Speed is a secondary effect of clarity, not a primary goal. |
| Spine is only for large teams | Spine is for any team where structural integrity between intent and execution matters — size is not the determining factor. |

---

## 4. Boundary Summary

| Adjacent Category | Spine's Relationship | Spine Does Not |
|-------------------|---------------------|----------------|
| Issue trackers (Jira, Linear) | May integrate as external input/output | Replace boards, sprints, or assignment UI |
| CI/CD (GitHub Actions, Jenkins) | May trigger or be triggered by pipelines | Build, test, or deploy code |
| Project management (Asana, MS Project) | Operates on a different axis (integrity vs. scheduling) | Estimate timelines or track capacity |
| Documentation (Confluence, Notion) | Uses Markdown artifacts as truth | Host, render, or index documentation |
| AI frameworks (LangChain, CrewAI) | Governs agents as workflow actors | Orchestrate prompts or manage agent memory |
| Version control (Git, GitHub) | Depends on Git as foundational infrastructure | Replace Git or repository hosting |

---

## 5. Using This Document

When evaluating a feature request or scope change:

1. Check if it conflicts with any non-goal listed above
2. If it does, reject it or require a formal amendment with documented rationale
3. If it falls in a boundary area, evaluate whether Spine should own the capability or integrate with an external tool
4. When in doubt, prefer integration over consolidation

---

## 6. Evolution Policy

This document is expected to evolve as the product matures and new boundary questions arise.

Changes must be versioned in Git and must not contradict the [Charter](/governance/Charter.md) or [Constitution](/governance/Constitution.md).
