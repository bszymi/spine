package artifact_test

import (
	"testing"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
)

func TestParseInitiative(t *testing.T) {
	content := []byte(`---
id: INIT-001
type: Initiative
title: Foundations
status: In Progress
owner: bszymi
created: 2026-03-04
links:
  - type: related_to
    target: /governance/charter.md
---

# Foundations

This is the body content.
`)

	a, err := artifact.Parse("initiatives/INIT-001/initiative.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.ID != "INIT-001" {
		t.Errorf("expected ID INIT-001, got %s", a.ID)
	}
	if a.Type != domain.ArtifactTypeInitiative {
		t.Errorf("expected type Initiative, got %s", a.Type)
	}
	if a.Title != "Foundations" {
		t.Errorf("expected title Foundations, got %s", a.Title)
	}
	if a.Status != domain.StatusInProgress {
		t.Errorf("expected status In Progress, got %s", a.Status)
	}
	if a.Metadata["owner"] != "bszymi" {
		t.Errorf("expected owner bszymi, got %s", a.Metadata["owner"])
	}
	if a.Metadata["created"] != "2026-03-04" {
		t.Errorf("expected created 2026-03-04, got %s", a.Metadata["created"])
	}
	if len(a.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(a.Links))
	}
	if a.Links[0].Type != domain.LinkTypeRelatedTo {
		t.Errorf("expected link type related_to, got %s", a.Links[0].Type)
	}
	if a.Links[0].Target != "/governance/charter.md" {
		t.Errorf("expected target /governance/charter.md, got %s", a.Links[0].Target)
	}
	if a.Content != "# Foundations\n\nThis is the body content.\n" {
		t.Errorf("unexpected body content: %q", a.Content)
	}
}

func TestParseEpic(t *testing.T) {
	content := []byte(`---
id: EPIC-004
type: Epic
title: Governance Refinement
status: Pending
initiative: /initiatives/INIT-001-foundations/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-001-foundations/initiative.md
---

# Epic content
`)

	a, err := artifact.Parse("initiatives/INIT-001/epics/EPIC-004/epic.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Type != domain.ArtifactTypeEpic {
		t.Errorf("expected type Epic, got %s", a.Type)
	}
	if a.Metadata["initiative"] != "/initiatives/INIT-001-foundations/initiative.md" {
		t.Errorf("expected initiative path, got %s", a.Metadata["initiative"])
	}
}

func TestParseTask(t *testing.T) {
	content := []byte(`---
id: TASK-001
type: Task
title: Artifact Schema Definition
status: Completed
epic: /initiatives/INIT-001/epics/EPIC-004/epic.md
initiative: /initiatives/INIT-001/initiative.md
work_type: implementation
acceptance: Approved
acceptance_rationale: All deliverables met
links:
  - type: parent
    target: /initiatives/INIT-001/epics/EPIC-004/epic.md
  - type: blocked_by
    target: /initiatives/INIT-001/epics/EPIC-003/tasks/TASK-002.md
---

# Task content
`)

	a, err := artifact.Parse("tasks/TASK-001.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Type != domain.ArtifactTypeTask {
		t.Errorf("expected type Task, got %s", a.Type)
	}
	if a.Status != domain.StatusCompleted {
		t.Errorf("expected status Completed, got %s", a.Status)
	}
	if a.Metadata["work_type"] != "implementation" {
		t.Errorf("expected work_type implementation, got %s", a.Metadata["work_type"])
	}
	if a.Metadata["acceptance"] != "Approved" {
		t.Errorf("expected acceptance Approved, got %s", a.Metadata["acceptance"])
	}
	if a.Metadata["acceptance_rationale"] != "All deliverables met" {
		t.Errorf("expected acceptance_rationale, got %s", a.Metadata["acceptance_rationale"])
	}
	if a.Metadata["epic"] != "/initiatives/INIT-001/epics/EPIC-004/epic.md" {
		t.Errorf("expected epic path, got %s", a.Metadata["epic"])
	}
	if len(a.Links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(a.Links))
	}
	if a.Links[1].Type != domain.LinkTypeBlockedBy {
		t.Errorf("expected blocked_by, got %s", a.Links[1].Type)
	}
}

func TestParseADR(t *testing.T) {
	content := []byte(`---
id: ADR-004
type: ADR
title: Evaluation and Acceptance Model
status: Accepted
date: 2026-03-11
decision_makers: Spine Architecture
links:
  - type: related_to
    target: /architecture/domain-model.md
---

# ADR-004
`)

	a, err := artifact.Parse("architecture/adr/ADR-004.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Type != domain.ArtifactTypeADR {
		t.Errorf("expected type ADR, got %s", a.Type)
	}
	if a.Status != domain.StatusAccepted {
		t.Errorf("expected status Accepted, got %s", a.Status)
	}
	if a.Metadata["date"] != "2026-03-11" {
		t.Errorf("expected date, got %s", a.Metadata["date"])
	}
	if a.Metadata["decision_makers"] != "Spine Architecture" {
		t.Errorf("expected decision_makers, got %s", a.Metadata["decision_makers"])
	}
}

func TestParseGovernance(t *testing.T) {
	content := []byte(`---
type: Governance
title: Constitution
status: Foundational
version: "0.1"
---

# Constitution content
`)

	a, err := artifact.Parse("governance/constitution.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Type != domain.ArtifactTypeGovernance {
		t.Errorf("expected type Governance, got %s", a.Type)
	}
	if a.ID != "" {
		t.Errorf("governance docs have no ID, got %s", a.ID)
	}
	if a.Metadata["version"] != "0.1" {
		t.Errorf("expected version 0.1, got %s", a.Metadata["version"])
	}
}

func TestParseArchitecture(t *testing.T) {
	content := []byte(`---
type: Architecture
title: Domain Model
status: Living Document
version: "0.1"
links:
  - type: related_to
    target: /governance/constitution.md
---

# Domain Model
`)

	a, err := artifact.Parse("architecture/domain-model.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Type != domain.ArtifactTypeArchitecture {
		t.Errorf("expected type Architecture, got %s", a.Type)
	}
	if a.Status != domain.StatusLivingDocument {
		t.Errorf("expected Living Document, got %s", a.Status)
	}
}

func TestParseProduct(t *testing.T) {
	content := []byte(`---
type: Product
title: Product Definition
status: Living Document
version: "0.1"
---

# Product Definition
`)

	a, err := artifact.Parse("product/product-definition.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Type != domain.ArtifactTypeProduct {
		t.Errorf("expected type Product, got %s", a.Type)
	}
}

func TestParseNoFrontMatter(t *testing.T) {
	content := []byte("# Just a regular markdown file\n\nNo front matter here.\n")

	_, err := artifact.Parse("README.md", content)
	if err == nil {
		t.Fatal("expected error for file without front matter")
	}

	parseErr, ok := err.(*artifact.ParseError)
	if !ok {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Path != "README.md" {
		t.Errorf("expected path README.md, got %s", parseErr.Path)
	}
}

func TestParseInvalidYAML(t *testing.T) {
	content := []byte("---\n: invalid: yaml: [broken\n---\n# Content\n")

	_, err := artifact.Parse("bad.md", content)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseUnclosedFrontMatter(t *testing.T) {
	content := []byte("---\ntype: Governance\ntitle: Test\n# No closing delimiter\n")

	_, err := artifact.Parse("unclosed.md", content)
	if err == nil {
		t.Fatal("expected error for unclosed front matter")
	}
}

func TestParseNoLinks(t *testing.T) {
	content := []byte(`---
type: Governance
title: Test Doc
status: Living Document
---

# Test
`)

	a, err := artifact.Parse("test.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(a.Links) != 0 {
		t.Errorf("expected 0 links, got %d", len(a.Links))
	}
}

func TestParsePathPreserved(t *testing.T) {
	content := []byte(`---
type: Governance
title: Test
status: Living Document
---

# Test
`)

	a, err := artifact.Parse("governance/some/deep/path.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Path != "governance/some/deep/path.md" {
		t.Errorf("expected path preserved, got %s", a.Path)
	}
}

func TestIsArtifact(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"governance", "---\ntype: Governance\ntitle: Test\n---\n# Test", true},
		{"task", "---\ntype: Task\nid: TASK-001\ntitle: Test\n---\n", true},
		{"adr", "---\ntype: ADR\nid: ADR-001\n---\n", true},
		{"no front matter", "# Just markdown", false},
		{"unknown type", "---\ntype: Unknown\n---\n", false},
		{"empty", "", false},
		{"yaml only", "---\nfoo: bar\n---\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := artifact.IsArtifact([]byte(tt.content))
			if got != tt.want {
				t.Errorf("IsArtifact = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseExtraFields(t *testing.T) {
	content := []byte(`---
type: Task
id: TASK-001
title: Test Task
status: Pending
epic: /path/to/epic.md
initiative: /path/to/init.md
custom_field: custom_value
---

# Task
`)

	a, err := artifact.Parse("task.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if a.Metadata["custom_field"] != "custom_value" {
		t.Errorf("expected custom_field=custom_value, got %s", a.Metadata["custom_field"])
	}
}
