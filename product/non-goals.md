---
type: Product
title: Non-Goals and Anti-Requirements
status: Living Document
version: "0.1"
---

# Non-Goals and Anti-Requirements

---

## 1. Purpose

This document explicitly defines what Spine is not and what it will not do.

It serves as a filter for evaluating feature requests, architectural proposals, and scope changes. If a proposed capability conflicts with a non-goal listed here, it should be rejected or require a formal amendment with documented rationale.

---

## 2. Core Boundary

Spine operates at the coordination layer — governing intent, artifacts, and execution — not at the tool layer.

The system is structured in three layers, as defined in the [Charter](/governance/charter.md):

- **Artifact Layer** — versioned truth in Git
- **Execution Layer** — governed workflows
- **Actor Layer** — humans and AI agents

Spine coordinates between these layers. It does not attempt to become the tools that operate within them.

---

## 3. Non-Goals

### 3.1 Spine does not require replacing development tools

Spine integrates with existing tools (GitHub, Jira, CI/CD systems, messaging platforms) rather than replacing them.

In some environments, Spine may reduce the need for certain tools — its artifact-centric model can make external ticketing systems redundant. But replacing them is not a design goal and Spine does not depend on doing so.

Spine is the center of gravity that tools orbit around.

**Filter:** If a feature request replicates an existing tool's UI (Kanban boards, sprint planning, velocity tracking), evaluate whether integration is sufficient before building it.

---

### 3.2 Spine is not a CI/CD system

Spine does not implement CI/CD engines or manage build infrastructure.

Existing CI/CD systems (GitHub Actions, Jenkins, GitLab CI, etc.) remain responsible for build, test, and deployment pipelines.

Spine may trigger or be triggered by CI/CD systems as part of governed workflows, but it does not replace CI/CD tooling.

**Filter:** If a feature request looks like "run tests on PR" or "deploy to staging," it is out of scope.

---

### 3.3 Spine is not a forge UI

Spine depends on Git as the foundational infrastructure for artifact storage and versioning, and — per [Boundaries §2.1](/product/boundaries-and-constraints.md) — hosts the governed Git repository itself. Spine also owns the *governance* of change proposals: PR state, review discussions, approval outcomes, and merge gates live in Spine's Run and discussion model. What Spine does not own is the *presentation surface* that renders those things.

Out of scope:

- Rendering PR diffs, inline review comments, code browsing, code search
- Hosting wikis, releases pages, sponsor tooling, forge-native social features
- CI/CD integration surfaces (checks API, status badges, deployment environments)

In scope (and often confused with the above):

- The state machine behind a PR — branch, review, approval, merge — is governance, owned by Spine.
- Review discussions attached to a change are governance artifacts, owned by Spine.
- Approval outcomes and merge authority are Spine's to grant.

A team that wants a rendered forge-style UI over this state can use an external platform as a **client** — mirroring refs, surfacing PR/review data, forwarding user actions back to Spine for evaluation. The forge never authorizes a merge or owns review state; it is an interface. This is the "forge as client" boundary in [Boundaries §2.3](/product/boundaries-and-constraints.md); the architectural programme that makes this cleanly possible is tracked as a follow-up initiative.

**Filter:** If a feature request is "render a PR view," "browse code," "host a wiki," or "mirror releases," it is out of scope. If it is "run a workflow when a PR is opened," "require a review before merge," "reject a push that violates governance," or "attach a review comment to a change" — it is in scope. The dividing line is: does this describe governance state and transitions (in), or does it describe the surface that users see (out)?

---

### 3.4 Spine is not a project management tool

Spine does not provide Gantt charts, resource allocation, timeline estimation, capacity planning, or burndown tracking.

Project management tools optimize for scheduling and resource utilization. Spine optimizes for structural integrity between intent and execution.

**Filter:** If a feature request looks like "estimate task duration" or "show team capacity," it is out of scope.

---

### 3.5 Spine is not a documentation platform

Spine may expose structural navigation and relationships between artifacts (for example graphs or lineage views), but it is not designed to be a documentation publishing platform or knowledge base.

Rich editing, publishing workflows, and knowledge discovery features remain the responsibility of external documentation tools.

**Filter:** If a feature request looks like "render documentation as a website" or "add collaborative editing," it is out of scope.

---

### 3.6 Spine is not an AI orchestration framework

Spine governs AI agents as execution actors, but it is not LangChain, CrewAI, or an agent orchestration platform.

Spine does not manage prompts, chain LLM calls, or provide agent memory. It defines what an AI agent may do within a workflow, validates its output, and records its contributions.

**Filter:** If a feature request looks like "add prompt chaining" or "manage agent memory," it is out of scope.

---

### 3.7 Spine is not an AI model platform

Spine orchestrates AI agents as actors but does not train, host, or provide large language models.

Spine has no opinion on which models agents use. It governs what agents may do and what they produce, not how they reason internally.

**Filter:** If a feature request looks like "fine-tune a model" or "host an inference endpoint," it is out of scope.

---

### 3.8 Spine is not a personal productivity tool

Spine is designed for structured execution where governance and traceability matter.

While it can be used by small teams or individuals, its design prioritizes structural integrity over the speed and flexibility typically preferred in casual solo development workflows. See the [anti-persona](/product/users-and-use-cases.md#31-casual-solo-hacker) in the users and use cases document.

**Filter:** If a feature request begins with "make it easier for someone who doesn't want governance," it conflicts with Spine's core purpose.

---

### 3.9 Spine does not prioritize speed over integrity

Spine will not sacrifice traceability, reproducibility, or auditability for faster execution.

Features that bypass governance (skip validation, auto-approve, disable audit) are anti-requirements. Acceleration must emerge from structural clarity, not from weakening constraints.

**Filter:** If a feature request looks like "skip review for small changes" or "auto-merge when tests pass," it requires careful evaluation against constitutional principles.

---

### 3.10 Spine does not enable uncontrolled AI autonomy

AI agents in Spine operate under the same governance as human actors. Spine will not provide features that allow AI agents to self-assign work, escalate their own permissions, or bypass workflow constraints.

**Filter:** If a feature request looks like "let the agent decide what to work on next" or "auto-approve agent output," it is out of scope.

---

## 4. Common Misconceptions

| Misconception | Reality |
|---------------|---------|
| Spine is a better Jira | Spine governs execution through versioned artifacts. It may reduce the need for ticketing tools but does not aim to replace them. |
| Spine replaces GitHub | Spine owns the governance behind PRs (state, reviews, approvals, merges) and hosts the Git repo. It does not replace the forge *UI* — PR diff rendering, code browsing, wikis, releases. Forges integrate as clients of Spine's governance engine, not as authorities over it. |
| Spine is an AI agent framework | Spine governs AI agents as actors within workflows. It does not orchestrate LLM calls or manage agent internals. |
| Spine makes teams faster | Spine makes teams more structurally sound. Speed is a secondary effect of clarity, not a primary goal. |
| Spine is only for large teams | Spine is for any team where structural integrity between intent and execution matters — size is not the determining factor. |
| Spine hosts or trains AI models | Spine orchestrates agents as actors. It has no opinion on model infrastructure. |

---

## 5. Boundary Summary

| Adjacent Category | Spine's Relationship | Spine Does Not |
|-------------------|---------------------|----------------|
| Issue trackers (Jira, Linear) | May integrate; may reduce need in some environments | Aim to replace boards, sprints, or assignment UI |
| CI/CD (GitHub Actions, Jenkins) | May trigger or be triggered by pipelines | Build, test, or deploy code |
| Project management (Asana, MS Project) | Operates on a different axis (integrity vs. scheduling) | Estimate timelines or track capacity |
| Documentation (Confluence, Notion) | Uses Markdown artifacts as truth | Host, render, or index documentation |
| AI frameworks (LangChain, CrewAI) | Governs agents as workflow actors | Orchestrate prompts or manage agent memory |
| AI model platforms | Agnostic to model infrastructure | Train, host, or provide models |
| Code collaboration platforms (GitHub, GitLab) | Hosts the governed repo; owns PR/review/approval state; forges integrate as clients to surface that state | Render PR diffs, host wikis, own issue UIs, provide forge-native social features |
| Version control (Git) | Depends on Git as foundational infrastructure | Replace Git |

---

## 6. Using This Document

When evaluating a feature request or scope change:

1. Check if it conflicts with any non-goal listed above
2. If it does, reject it or require a formal amendment with documented rationale
3. If it falls in a boundary area, evaluate whether Spine should own the capability or integrate with an external tool
4. When in doubt, prefer integration over consolidation

---

## 7. Evolution Policy

This document is expected to evolve as the product matures and new boundary questions arise.

Changes must be versioned in Git and must not contradict the [Charter](/governance/charter.md) or [Constitution](/governance/constitution.md).
