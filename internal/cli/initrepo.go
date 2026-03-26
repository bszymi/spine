package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// InitRepo initializes a Spine repository at the given path.
// Creates directory structure per repository-structure.md, seeds governance
// templates, and initializes Git if needed. Idempotent: skips existing files.
func InitRepo(repoPath string) error {
	if repoPath == "" {
		repoPath = "."
	}

	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Create directory structure.
	dirs := []string{
		"governance",
		"initiatives",
		"architecture",
		"product",
		"workflows",
		"templates",
		"tmp",
	}
	for _, dir := range dirs {
		p := filepath.Join(absPath, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Seed files — only write if file doesn't already exist.
	seeds := map[string]string{
		"governance/charter.md":              seedCharter,
		"governance/constitution.md":         seedConstitution,
		"governance/guidelines.md":           seedGuidelines,
		"governance/repository-structure.md": seedRepoStructure,
		"governance/naming-conventions.md":   seedNamingConventions,
		"templates/task-template.md":         seedTaskTemplate,
		"templates/epic-template.md":         seedEpicTemplate,
		"templates/initiative-template.md":   seedInitiativeTemplate,
		"templates/adr-template.md":          seedADRTemplate,
	}

	for relPath, content := range seeds {
		fullPath := filepath.Join(absPath, relPath)
		if _, err := os.Stat(fullPath); err == nil {
			continue // file exists, skip
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", relPath, err)
		}
	}

	// Initialize Git if not already a repository.
	gitDir := filepath.Join(absPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		cmd := exec.Command("git", "init", absPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %s: %w", string(out), err)
		}
	}

	return nil
}

// Seed content — minimal valid artifacts with YAML front matter.

const seedCharter = `---
type: Governance
title: Charter
status: Draft
---

# Charter

This document defines the project's mission, values, and principles.
`

const seedConstitution = `---
type: Governance
title: Constitution
status: Draft
---

# Constitution

This document defines the governance rules and constraints for the project.
`

const seedGuidelines = `---
type: Governance
title: Guidelines
status: Draft
---

# Guidelines

This document defines coding, contribution, and operational guidelines.
`

const seedRepoStructure = `---
type: Governance
title: Repository Structure
status: Draft
---

# Repository Structure

Defines the directory layout and file organization conventions for the repository.
`

const seedNamingConventions = `---
type: Governance
title: Naming Conventions
status: Draft
---

# Naming Conventions

Defines naming rules for artifacts, directories, branches, and identifiers.
`

const seedTaskTemplate = `---
id: TASK-XXX
type: Task
title: "[Task Title]"
status: Pending
epic: /initiatives/INIT-XXX/epics/EPIC-XXX/epic.md
initiative: /initiatives/INIT-XXX/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-XXX/epics/EPIC-XXX/epic.md
---

# TASK-XXX — [Task Title]

## Purpose

[What this task accomplishes and why it is needed.]

## Deliverable

- [Expected deliverable]

## Acceptance Criteria

- [Verifiable condition]
`

const seedEpicTemplate = `---
id: EPIC-XXX
type: Epic
title: "[Epic Title]"
status: Pending
initiative: /initiatives/INIT-XXX/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-XXX/initiative.md
---

# EPIC-XXX — [Epic Title]

## Purpose

[What this epic accomplishes.]

## Key Work Areas

- [Work area]

## Acceptance Criteria

- [Verifiable condition]
`

const seedInitiativeTemplate = `---
id: INIT-XXX
type: Initiative
title: "[Initiative Title]"
status: Pending
owner: "[owner]"
---

# INIT-XXX — [Initiative Title]

## Intent

[What this initiative aims to accomplish.]

## Scope

- [Scope item]

## Success Criteria

- [Measurable outcome]
`

const seedADRTemplate = `---
id: ADR-XXX
type: ADR
title: "[Decision Title]"
status: Proposed
---

# ADR-XXX — [Decision Title]

## Context

[What is the issue that motivates this decision?]

## Decision

[What is the change that we're proposing?]

## Consequences

[What becomes easier or more difficult as a result?]
`
