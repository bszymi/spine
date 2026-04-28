package artifact_test

import (
	"strings"
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

func TestParseRejectsOversizedFrontMatter(t *testing.T) {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: Governance\ntitle: Test\nstatus: Living Document\n")
	// Pad with a long scalar that pushes past the 64 KiB cap.
	b.WriteString("extra: ")
	b.WriteString(strings.Repeat("a", 70*1024))
	b.WriteString("\n---\n# body\n")

	_, err := artifact.Parse("huge.md", []byte(b.String()))
	if err == nil {
		t.Fatal("expected error for oversized front matter")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Fatalf("expected byte-cap error, got %v", err)
	}
}

func TestParseRejectsDeeplyNestedFrontMatter(t *testing.T) {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: Governance\ntitle: Deep\nstatus: Living Document\n")
	b.WriteString("nested:\n")
	// 100 levels of nested maps; well over the depth cap.
	for i := 0; i < 100; i++ {
		b.WriteString(strings.Repeat("  ", i+1))
		b.WriteString("k:\n")
	}
	b.WriteString("---\n# body\n")

	_, err := artifact.Parse("deep.md", []byte(b.String()))
	if err == nil {
		t.Fatal("expected error for deeply-nested YAML")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Fatalf("expected depth error, got %v", err)
	}
}

func TestParseRejectsAliasExpansion(t *testing.T) {
	// Billion-laughs style: define an anchor, reference it many times.
	// We stay under the 64 KB byte cap to exercise the alias guard, not
	// the size guard.
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: Governance\ntitle: Aliases\nstatus: Living Document\n")
	b.WriteString("anchor: &a hello\n")
	b.WriteString("refs:\n")
	for i := 0; i < 200; i++ {
		b.WriteString("  - *a\n")
	}
	b.WriteString("---\n# body\n")

	_, err := artifact.Parse("aliases.md", []byte(b.String()))
	if err == nil {
		t.Fatal("expected error for alias-heavy YAML")
	}
	if !strings.Contains(err.Error(), "alias") {
		t.Fatalf("expected alias-count error, got %v", err)
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

func TestParseUnknownType(t *testing.T) {
	content := []byte("---\ntype: Goverance\ntitle: Test\nstatus: Draft\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for unknown artifact type")
	}
	parseErr := err.(*artifact.ParseError)
	if parseErr.Message != "unknown artifact type: Goverance" {
		t.Errorf("unexpected error message: %s", parseErr.Message)
	}
}

func TestParseInitiativeMissingCreated(t *testing.T) {
	content := []byte("---\nid: INIT-001\ntype: Initiative\ntitle: Test\nstatus: Pending\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for Initiative missing created")
	}
}

func TestParseMissingType(t *testing.T) {
	content := []byte("---\ntitle: Test\nstatus: Draft\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for missing type")
	}
}

func TestParseMissingTitle(t *testing.T) {
	content := []byte("---\ntype: Governance\nstatus: Living Document\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestParseMissingStatus(t *testing.T) {
	content := []byte("---\ntype: Governance\ntitle: Test\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for missing status")
	}
}

func TestParseTaskMissingEpic(t *testing.T) {
	content := []byte("---\nid: TASK-001\ntype: Task\ntitle: Test\nstatus: Pending\ninitiative: /init.md\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for Task missing epic")
	}
}

func TestParseTaskMissingInitiative(t *testing.T) {
	content := []byte("---\nid: TASK-001\ntype: Task\ntitle: Test\nstatus: Pending\nepic: /epic.md\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for Task missing initiative")
	}
}

func TestParseEpicMissingInitiative(t *testing.T) {
	content := []byte("---\nid: EPIC-001\ntype: Epic\ntitle: Test\nstatus: Pending\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for Epic missing initiative")
	}
}

func TestParseADRMissingDate(t *testing.T) {
	content := []byte("---\nid: ADR-001\ntype: ADR\ntitle: Test\nstatus: Proposed\ndecision_makers: Team\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for ADR missing date")
	}
}

func TestParseADRMissingDecisionMakers(t *testing.T) {
	content := []byte("---\nid: ADR-001\ntype: ADR\ntitle: Test\nstatus: Proposed\ndate: 2026-01-01\n---\n# Test\n")
	_, err := artifact.Parse("test.md", content)
	if err == nil {
		t.Fatal("expected error for ADR missing decision_makers")
	}
}

func TestParseCRLFLineEndings(t *testing.T) {
	content := []byte("---\r\ntype: Governance\r\ntitle: Test\r\nstatus: Living Document\r\n---\r\n# Content\r\n")
	a, err := artifact.Parse("test.md", content)
	if err != nil {
		t.Fatalf("Parse CRLF: %v", err)
	}
	if a.Content[0] == '\r' {
		t.Error("body should not start with \\r")
	}
}

func TestParseTaskRepositoriesAbsent(t *testing.T) {
	// A pre-INIT-014 task with no `repositories` field must still parse
	// cleanly and produce an empty Repositories slice — this is the
	// backward-compatibility guarantee from EPIC-002 acceptance #5.
	content := []byte(`---
id: TASK-001
type: Task
title: Legacy single-repo task
status: Pending
epic: /init/epic.md
initiative: /init/initiative.md
---

# Legacy
`)
	a, err := artifact.Parse("tasks/TASK-001.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(a.Repositories) != 0 {
		t.Errorf("expected empty Repositories, got %v", a.Repositories)
	}
}

func TestParseTaskRepositoriesSingleAndMulti(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "single",
			yaml: "repositories:\n  - payments-service\n",
			want: []string{"payments-service"},
		},
		{
			name: "multi",
			yaml: "repositories:\n  - payments-service\n  - api-gateway\n",
			want: []string{"payments-service", "api-gateway"},
		},
		{
			name: "empty list",
			yaml: "repositories: []\n",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte("---\nid: TASK-002\ntype: Task\ntitle: T\nstatus: Pending\nepic: /e.md\ninitiative: /i.md\n" + tt.yaml + "---\n# body\n")
			a, err := artifact.Parse("t.md", content)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(a.Repositories) != len(tt.want) {
				t.Fatalf("Repositories len: got %d (%v), want %d (%v)", len(a.Repositories), a.Repositories, len(tt.want), tt.want)
			}
			for i, id := range tt.want {
				if a.Repositories[i] != id {
					t.Errorf("Repositories[%d]: got %q, want %q", i, a.Repositories[i], id)
				}
			}
			// repositories must not also leak into the metadata map —
			// downstream code reads typed Artifact.Repositories, not
			// stringified YAML.
			if _, ok := a.Metadata["repositories"]; ok {
				t.Errorf("repositories must not appear in Metadata, got %q", a.Metadata["repositories"])
			}
		})
	}
}

func TestParseTaskRepositoriesRejectsBadShapes(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "scalar instead of list",
			yaml: "repositories: payments-service\n",
		},
		{
			name: "map instead of list",
			yaml: "repositories:\n  payments-service: true\n",
		},
		{
			name: "empty string entry",
			yaml: "repositories:\n  - \"\"\n",
		},
		{
			name: "whitespace-only entry",
			yaml: "repositories:\n  - \"   \"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte("---\nid: TASK-003\ntype: Task\ntitle: T\nstatus: Pending\nepic: /e.md\ninitiative: /i.md\n" + tt.yaml + "---\n# body\n")
			if _, err := artifact.Parse("t.md", content); err == nil {
				t.Fatal("expected parse error for bad repositories shape")
			}
		})
	}
}

func TestParseRepositoriesRejectedOnNonTask(t *testing.T) {
	// `repositories` is a Task-only field. A stray entry on another
	// artifact must be rejected at parse time so it can never reach
	// downstream resolution code. Cover non-empty, empty, and null
	// shapes — the field's mere presence on a non-Task is the error,
	// not its content.
	cases := []struct {
		name string
		yaml string
	}{
		{"non-empty", "repositories:\n  - payments-service\n"},
		{"empty list", "repositories: []\n"},
		{"null", "repositories: null\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte("---\nid: EPIC-001\ntype: Epic\ntitle: T\nstatus: Pending\ninitiative: /i.md\n" + tc.yaml + "---\n\n# Body\n")
			_, err := artifact.Parse("epic.md", content)
			if err == nil {
				t.Fatal("expected parse error: repositories on Epic")
			}
			if !strings.Contains(err.Error(), "repositories is only valid on Task") {
				t.Errorf("unexpected error: %v", err)
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
