package validation_test

// ADR-017 §4: AL-001 and AL-002.

import (
	"context"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/validation"
)

const asmPath = "assumptions/ASM-001-x.md"

func evidenceLink(t domain.LinkType, target string) []domain.Link {
	return []domain.Link{{Type: t, Target: target}}
}

func TestAL_EvidenceRules(t *testing.T) {
	for _, c := range []struct {
		name     string
		status   string
		evidence []domain.Link // carried by another artifact
		want001  bool
		want002  bool
	}{
		{"supported without evidence", "Supported", nil, true, false},
		{"supported with supports", "Supported", evidenceLink("supports", "/"+asmPath), false, false},
		{"supported with supports, bare target", "Supported", evidenceLink("supports", asmPath), false, false},
		{"supported with only refutes", "Supported", evidenceLink("refutes", "/"+asmPath), true, false},
		{"supported with only depends_on", "Supported", evidenceLink("depends_on", "/"+asmPath), true, false},
		{"contradicted without evidence", "Contradicted", nil, false, true},
		{"contradicted with refutes", "Contradicted", evidenceLink("refutes", "/"+asmPath), false, false},
		{"contradicted with only supports", "Contradicted", evidenceLink("supports", "/"+asmPath), false, true},
		{"evidence for another assumption", "Supported", evidenceLink("supports", "/assumptions/ASM-002-y.md"), true, false},
		{"untested", "Untested", nil, false, false},
		{"proposed", "Proposed", nil, false, false},
		{"rejected", "Rejected", nil, false, false},
		{"superseded", "Superseded", nil, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := newFakeStore()
			addArtifact(fs, asmPath, "Assumption", c.status, map[string]string{"created": "2026-11-10"}, nil)
			if c.evidence != nil {
				addArtifact(fs, "initiatives/i/tasks/TASK-001-spike.md", "Task", "Completed", nil, c.evidence)
			}
			result := validation.NewEngine(fs).Validate(context.Background(), asmPath)
			if got := hasRuleWarning(result, "AL-001"); got != c.want001 {
				t.Errorf("AL-001 = %v, want %v", got, c.want001)
			}
			if got := hasRuleWarning(result, "AL-002"); got != c.want002 {
				t.Errorf("AL-002 = %v, want %v", got, c.want002)
			}
			if hasRuleError(result, "AL-001") || hasRuleError(result, "AL-002") {
				t.Error("AL rules are warnings, never errors")
			}
		})
	}
}

// The rules look only at Assumptions: another type with the same status
// string is left alone.
func TestAL_OnlyAssumptions(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, "architecture/x.md", "Architecture", "Supported", nil, nil)
	result := validation.NewEngine(fs).Validate(context.Background(), "architecture/x.md")
	if hasRuleWarning(result, "AL-001") || hasRuleWarning(result, "AL-002") {
		t.Error("AL rule fired for a non-Assumption")
	}
}

// An assumption's own link to itself is not evidence.
func TestAL_SelfLinkIsNotEvidence(t *testing.T) {
	fs := newFakeStore()
	addArtifact(fs, asmPath, "Assumption", "Supported", map[string]string{"created": "2026-11-10"},
		evidenceLink("supports", "/"+asmPath))
	result := validation.NewEngine(fs).Validate(context.Background(), asmPath)
	if !hasRuleWarning(result, "AL-001") {
		t.Error("a self-link was accepted as evidence")
	}
}
