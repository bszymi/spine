---
id: EPIC-008
type: Epic
title: Divergence & Convergence
status: Pending
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/initiative.md
---

# EPIC-008 — Divergence & Convergence

---

## Purpose

Enable governed parallel execution. When a workflow step references a divergence point, the engine must create branch contexts, route steps to branches, and trigger convergence when the entry policy is met.

The state machines and convergence strategies exist from INIT-002. This epic wires them into the engine orchestrator.

---

## Key Work Areas

- Structured divergence orchestration (predefined branches)
- Exploratory divergence (dynamic branch creation)
- Convergence entry policy evaluation
- Convergence strategy execution (select_one, select_subset, merge, require_all)
- Actor-driven convergence evaluation step

---

## Primary Outputs

- Divergence trigger logic in engine orchestrator
- Branch context management during execution
- Convergence evaluation wired into step progression
- Integration tests with real Git branches

---

## Acceptance Criteria

- Divergence points create branch execution contexts
- Steps route to correct branches during divergence
- Convergence triggers when entry policy is met
- Convergence strategies produce correct results
- All branch outcomes are preserved (selected and rejected)
- Actor evaluation step works for convergence decisions
