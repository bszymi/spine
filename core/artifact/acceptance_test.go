package artifact

import (
	"strings"
	"testing"
)

func TestEscapeYAMLString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", `"simple"`},
		{`with "quotes"`, `"with \"quotes\""`},
		{`with\backslash`, `"with\\backslash"`},
		{"with\nnewline", `"with\nnewline"`},
		{"", `""`},
		{`both "quotes" and \backslash`, `"both \"quotes\" and \\backslash"`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeYAMLString(tt.input)
			if got != tt.expected {
				t.Errorf("escapeYAMLString(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestInsertAcceptanceFields_Approved(t *testing.T) {
	content := `---
type: Task
title: My Task
status: pending
---

## Description

Some content here.
`
	result := insertAcceptanceFields(content, "approved", "Looks good", "completed")

	if !strings.Contains(result, "acceptance: approved") {
		t.Error("expected 'acceptance: approved' in result")
	}
	if !strings.Contains(result, "acceptance_rationale:") {
		t.Error("expected 'acceptance_rationale' in result")
	}
	if !strings.Contains(result, "status: completed") {
		t.Error("expected 'status: completed' in result")
	}
	// Original pending status should be replaced.
	if strings.Contains(result, "status: pending") {
		t.Error("expected old status 'pending' to be removed")
	}
}

func TestInsertAcceptanceFields_NoRationale(t *testing.T) {
	content := `---
type: Task
title: My Task
status: pending
---

Content.
`
	result := insertAcceptanceFields(content, "approved", "", "completed")

	if !strings.Contains(result, "acceptance: approved") {
		t.Error("expected 'acceptance: approved' in result")
	}
	// Empty rationale should not add acceptance_rationale line.
	if strings.Contains(result, "acceptance_rationale") {
		t.Error("expected no acceptance_rationale line for empty rationale")
	}
}

func TestInsertAcceptanceFields_Rejected(t *testing.T) {
	content := `---
type: Task
title: My Task
status: in_review
---

Content.
`
	result := insertAcceptanceFields(content, "rejected_closed", "Not acceptable", "rejected")

	if !strings.Contains(result, "acceptance: rejected_closed") {
		t.Error("expected 'acceptance: rejected_closed' in result")
	}
	if !strings.Contains(result, "status: rejected") {
		t.Error("expected 'status: rejected' in result")
	}
}

func TestInsertAcceptanceFields_NoFrontMatter(t *testing.T) {
	content := "Just plain content without front matter."
	result := insertAcceptanceFields(content, "approved", "", "completed")

	// Should return unchanged if no front matter markers.
	if result != content {
		t.Errorf("expected unchanged content when no front matter, got %q", result)
	}
}

func TestInsertAcceptanceFields_PreservesBodyContent(t *testing.T) {
	content := `---
type: Task
title: My Task
status: pending
---

## Description

This content must be preserved.
`
	result := insertAcceptanceFields(content, "approved", "", "completed")

	if !strings.Contains(result, "This content must be preserved.") {
		t.Error("expected body content to be preserved")
	}
}
