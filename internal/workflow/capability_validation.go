package workflow

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// ValidateCapabilities checks that required_capabilities values on workflow steps
// reference registered skill names. Unregistered capabilities produce warnings
// (not errors) to maintain backward compatibility during migration.
func ValidateCapabilities(ctx context.Context, wf *domain.WorkflowDefinition, st store.Store) []domain.ValidationError {
	skills, err := st.ListSkills(ctx)
	if err != nil {
		// If we can't load skills, skip validation silently — this is a warning-only check
		return nil
	}

	skillNames := make(map[string]bool, len(skills))
	for _, s := range skills {
		if s.Status == domain.SkillStatusActive {
			skillNames[s.Name] = true
		}
	}

	// If no skills are registered yet, skip — the system is still using legacy capabilities
	if len(skillNames) == 0 {
		return nil
	}

	var warnings []domain.ValidationError
	for i, step := range wf.Steps {
		if step.Execution == nil || len(step.Execution.RequiredCapabilities) == 0 {
			continue
		}
		for _, cap := range step.Execution.RequiredCapabilities {
			if !skillNames[cap] {
				warnings = append(warnings, domain.ValidationError{
					RuleID:   "capability_registry",
					Field:    fmt.Sprintf("steps[%d].execution.required_capabilities", i),
					Severity: "warning",
					Message:  fmt.Sprintf("capability %q is not a registered skill", cap),
				})
			}
		}
	}

	return warnings
}
